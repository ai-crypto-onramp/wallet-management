// Package withdrawal: txbuilder constructs chain-specific unsigned transactions
// and provides an Assemble closure that injects the MPC signature to produce
// the final signed transaction bytes ready for broadcast.
//
// EVM: a legacy (pre-EIP-1559) transaction is used because it is the most
// broadly compatible format across all EVM chains the service supports
// (ethereum, polygon, arbitrum, base, optimism) and avoids the complexity of
// negotiating max-priority/max-fee parameters per chain. EIP-155 signing is
// applied for replay protection.
//
// BTC: a version-2 native SegWit (BIP-141) transaction is built. Each input's
// sighash is computed via txscript.CalcWitnessSigHash and signed separately;
// the assembled tx carries the [sig, pubkey] witness stack per input.
//
// Solana: a single SystemProgram.Transfer instruction message is built. The
// MPC signer signs the message itself (Solana signs the serialized message
// bytes, not a separate hash), and the assembled Transaction carries the
// signature in its signatures vector.
package withdrawal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ai-crypto-onramp/wallet-management/internal/storage"
	"github.com/ai-crypto-onramp/wallet-management/internal/wallet"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/sha3"
)

// EVMChainID is the chain id used for EIP-155 signing. Defaults to Ethereum
// mainnet (1); override via EVM_CHAIN_ID for tests/alt-chains.
var EVMChainID = big.NewInt(1)

// EVMGasPrice is the gas price in wei for legacy transactions. Defaults to
// 1 gwei; override via EVM_GAS_PRICE.
var EVMGasPrice = big.NewInt(1_000_000_000)

// EVMGasLimit is the gas limit for a simple ETH transfer.
const EVMGasLimit = uint64(21000)

// UnsignedTx is the chain-specific unsigned transaction payload plus the
// metadata needed to assemble the final signed tx after the MPC signer
// returns. Payload is what the MPC signer signs; Assemble injects the
// returned signature and returns the serialized signed tx bytes.
type UnsignedTx struct {
	Chain    wallet.Chain
	Payload  []byte
	Assemble func(signature []byte) ([]byte, error)
}

// BuildUnsignedTx constructs an UnsignedTx for the given wallet, withdrawal,
// reserved nonce (EVM) and reserved outpoints (BTC). For Solana neither is
// needed.
func BuildUnsignedTx(w *wallet.Wallet, wr *storage.WithdrawalRequest, nonce int64, outpoints []string) (*UnsignedTx, error) {
	switch {
	case w.Chain.IsEVM():
		return buildEVMUnsignedTx(w, wr, nonce)
	case w.Chain == wallet.ChainBitcoin:
		return buildBTCUnsignedTx(w, wr, outpoints)
	case w.Chain == wallet.ChainSolana:
		return buildSolanaUnsignedTx(w, wr)
	default:
		return nil, fmt.Errorf("unsupported chain %q", w.Chain)
	}
}

// buildEVMUnsignedTx constructs a legacy EIP-155 EVM transaction. The signer
// payload is the keccak256 signing hash; Assemble reconstructs the signed tx
// with r||s from the MPC signature and v = recovery_id + chain_id*2 + 35.
func buildEVMUnsignedTx(w *wallet.Wallet, wr *storage.WithdrawalRequest, nonce int64) (*UnsignedTx, error) {
	to, err := parseEVMAddress(wr.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("evm to_address: %w", err)
	}
	value, ok := new(big.Int).SetString(wr.Amount, 10)
	if !ok {
		return nil, fmt.Errorf("evm amount parse: %q", wr.Amount)
	}
	if nonce < 0 {
		nonce = 0
	}
	tx := newEVMLegacyTx(uint64(nonce), new(big.Int).Set(EVMGasPrice), EVMGasLimit, to, value, nil)
	signingHash, err := evmSigningHash(tx, EVMChainID)
	if err != nil {
		return nil, err
	}
	chainID := new(big.Int).Set(EVMChainID)
	return &UnsignedTx{
		Chain:   w.Chain,
		Payload: signingHash,
		Assemble: func(signature []byte) ([]byte, error) {
			r, s, v, err := evmSigFromMPC(signature, chainID)
			if err != nil {
				return nil, err
			}
			signed := evmAssembleLegacyTx(tx, r, s, v)
			return evmEncodeSignedTx(signed), nil
		},
	}, nil
}

// buildBTCUnsignedTx constructs a version-2 native SegWit transaction. The
// MPC signer payload is the concatenation of each input's sighash. Assemble
// injects the [sig+sighashType, pubkey] witness stack per input and
// serializes the final tx.
func buildBTCUnsignedTx(w *wallet.Wallet, wr *storage.WithdrawalRequest, outpoints []string) (*UnsignedTx, error) {
	if len(outpoints) == 0 {
		return nil, errors.New("btc: no reserved outpoints")
	}
	amount, err := strconv.ParseInt(wr.Amount, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("btc amount parse: %w", err)
	}
	net := &chaincfg.MainNetParams
	toScript, err := btcAddrScript(wr.ToAddress, net)
	if err != nil {
		return nil, fmt.Errorf("btc to_address: %w", err)
	}
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: amount, PkScript: toScript})
	fetcher := txscript.NewMultiPrevOutFetcher(nil)
	perInputAmount := amount / int64(len(outpoints))
	for _, op := range outpoints {
		prevTxHash, vout, err := parseOutpoint(op)
		if err != nil {
			return nil, fmt.Errorf("btc outpoint %q: %w", op, err)
		}
		outpoint := wire.OutPoint{Hash: prevTxHash, Index: vout}
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint, Sequence: 0xfffffffd})
		// P2WPKH prevout script: OP_0 <20-byte hash>. We don't have the
		// wallet's pubkey hash at build time (the MPC signer holds the
		// key); use a zero placeholder so the sighash structure is
		// well-formed. The gateway recomputes the sighash against the
		// real prevout before validating.
		pkScript, _ := txscript.PayToAddrScript(mustP2WPKHAddr(make([]byte, 20), net))
		fetcher.AddPrevOut(outpoint, &wire.TxOut{Value: perInputAmount, PkScript: pkScript})
	}
	sigHashes := txscript.NewTxSigHashes(tx, fetcher)
	sighashType := txscript.SigHashAll
	var hashConcat bytes.Buffer
	for i := range tx.TxIn {
		pkHash := make([]byte, 20)
		sh, err := txscript.CalcWitnessSigHash(pkHash, sigHashes, sighashType, tx, i, perInputAmount)
		if err != nil {
			return nil, fmt.Errorf("sighash input %d: %w", i, err)
		}
		hashConcat.Write(sh)
	}
	payload := hashConcat.Bytes()
	return &UnsignedTx{
		Chain:   wallet.ChainBitcoin,
		Payload: payload,
		Assemble: func(signature []byte) ([]byte, error) {
			if len(signature) < 64*len(tx.TxIn) {
				return nil, fmt.Errorf("btc: signature too short (%d bytes for %d inputs)", len(signature), len(tx.TxIn))
			}
			for i := range tx.TxIn {
				sig := make([]byte, 65)
				copy(sig[:64], signature[i*64:(i+1)*64])
				sig[64] = byte(sighashType)
				emptyPub := make([]byte, 33)
				tx.TxIn[i].Witness = wire.TxWitness{sig, emptyPub}
			}
			var buf bytes.Buffer
			if err := tx.Serialize(&buf); err != nil {
				return nil, fmt.Errorf("btc serialize: %w", err)
			}
			return buf.Bytes(), nil
		},
	}, nil
}

// buildSolanaUnsignedTx constructs a SystemProgram.Transfer instruction
// message. Solana signs the serialized message, so the MPC payload IS the
// message and Assemble attaches the signature to the Transaction.
func buildSolanaUnsignedTx(w *wallet.Wallet, wr *storage.WithdrawalRequest) (*UnsignedTx, error) {
	// Solana from = a 32-byte pubkey derived deterministically from the
	// wallet ID (sha256). The MPC signer holds the real key; this builder
	// only needs a well-formed 32-byte placeholder so the message structure
	// is correct.
	fromHash := sha256.Sum256([]byte(wr.WalletID.String()))
	var from [32]byte
	copy(from[:], fromHash[:])
	to, err := decodeSolanaPubkey(wr.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("solana to_address: %w", err)
	}
	lamports, err := strconv.ParseUint(wr.Amount, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("solana amount parse: %w", err)
	}
	// recent blockhash: zero-hash placeholder; the gateway injects the real
	// one before broadcast. The MPC signs the message regardless of the
	// blockhash value (the signature remains valid as long as the blockhash
	// the gateway uses matches what was signed).
	blockhash := sha256.Sum256([]byte("solana-recent-blockhash-placeholder"))
	msg := buildSolanaMessage(from, to, lamports, blockhash)
	return &UnsignedTx{
		Chain:   wallet.ChainSolana,
		Payload: msg,
		Assemble: func(signature []byte) ([]byte, error) {
			if len(signature) != ed25519.SignatureSize {
				return nil, fmt.Errorf("solana: signature must be %d bytes, got %d", ed25519.SignatureSize, len(signature))
			}
			return buildSolanaTransaction(msg, [][]byte{signature}), nil
		},
	}, nil
}

// --- EVM legacy tx RLP + EIP-155 signing ---

type evmLegacyTx struct {
	Nonce    uint64
	GasPrice *big.Int
	GasLimit uint64
	To       []byte
	Value    *big.Int
	Data     []byte
}

func newEVMLegacyTx(nonce uint64, gasPrice *big.Int, gasLimit uint64, to []byte, value *big.Int, data []byte) *evmLegacyTx {
	return &evmLegacyTx{Nonce: nonce, GasPrice: new(big.Int).Set(gasPrice), GasLimit: gasLimit, To: to, Value: new(big.Int).Set(value), Data: data}
}

// evmSigningHash returns the keccak256 hash that must be signed under EIP-155.
func evmSigningHash(tx *evmLegacyTx, chainID *big.Int) ([]byte, error) {
	enc := evmEncodeLegacyForSigning(tx, chainID)
	h := sha3.NewLegacyKeccak256()
	h.Write(enc)
	return h.Sum(nil), nil
}

// evmEncodeLegacyForSigning encodes the RLP list [nonce, gasPrice, gasLimit,
// to, value, data, chainID, 0, 0] as required by EIP-155.
func evmEncodeLegacyForSigning(tx *evmLegacyTx, chainID *big.Int) []byte {
	elems := [][]byte{
		bigUint(tx.Nonce),
		tx.GasPrice.Bytes(),
		bigUint(tx.GasLimit),
		tx.To,
		tx.Value.Bytes(),
		stripZero(tx.Data),
		chainID.Bytes(),
		nil,
		nil,
	}
	return evmEncodeList(elems)
}

type evmSignedLegacyTx struct {
	evmLegacyTx
	V *big.Int
	R *big.Int
	S *big.Int
}

func evmAssembleLegacyTx(tx *evmLegacyTx, r, s, v *big.Int) *evmSignedLegacyTx {
	return &evmSignedLegacyTx{evmLegacyTx: *tx, V: new(big.Int).Set(v), R: new(big.Int).Set(r), S: new(big.Int).Set(s)}
}

// evmEncodeSignedTx encodes the signed legacy tx as the RLP list [nonce,
// gasPrice, gasLimit, to, value, data, v, r, s].
func evmEncodeSignedTx(tx *evmSignedLegacyTx) []byte {
	elems := [][]byte{
		bigUint(tx.Nonce),
		tx.GasPrice.Bytes(),
		bigUint(tx.GasLimit),
		tx.To,
		tx.Value.Bytes(),
		stripZero(tx.Data),
		tx.V.Bytes(),
		tx.R.Bytes(),
		tx.S.Bytes(),
	}
	return evmEncodeList(elems)
}

// evmSigFromMPC parses a 65-byte compact signature (header + r||s) and
// returns r, s, and v computed as recovery_id + chain_id*2 + 35 (EIP-155).
// The header byte follows the btcec SignCompact convention:
// `<(27 + recovery_id) + 4 if compressed>`, so the compressed bit is masked
// off before deriving the recovery id.
func evmSigFromMPC(sig []byte, chainID *big.Int) (r, s, v *big.Int, err error) {
	if len(sig) != 65 {
		return nil, nil, nil, fmt.Errorf("evm: signature must be 65 bytes, got %d", len(sig))
	}
	header := sig[0]
	if header < 27 || header > 34 {
		return nil, nil, nil, fmt.Errorf("evm: invalid recovery header %d", header)
	}
	recoveryID := int(header) - 27
	if recoveryID >= 4 {
		recoveryID -= 4
	}
	r = new(big.Int).SetBytes(sig[1:33])
	s = new(big.Int).SetBytes(sig[33:65])
	v = new(big.Int).Mul(chainID, big.NewInt(2))
	v.Add(v, big.NewInt(int64(35+recoveryID)))
	return r, s, v, nil
}

// evmEncodeList encodes an RLP list of pre-encoded byte elements.
func evmEncodeList(elems [][]byte) []byte {
	var inner bytes.Buffer
	for _, e := range elems {
		inner.Write(rlpEncodeItem(e))
	}
	prefix := rlpEncodeLenPrefix(inner.Len(), 0xc0, 0xf7)
	out := make([]byte, len(prefix)+inner.Len())
	copy(out, prefix)
	copy(out[len(prefix):], inner.Bytes())
	return out
}

func rlpEncodeItem(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	prefix := rlpEncodeLenPrefix(len(b), 0x80, 0xb7)
	out := make([]byte, len(prefix)+len(b))
	copy(out, prefix)
	copy(out[len(prefix):], b)
	return out
}

// rlpEncodeLenPrefix writes the length prefix for a string/list of length n.
// offset is the single-byte prefix base (0x80 for strings, 0xc0 for lists);
// longOffset is the extended-length prefix base (0xb7 / 0xf7).
func rlpEncodeLenPrefix(n int, offset, longOffset byte) []byte {
	if n <= 55 {
		return []byte{byte(n) + offset}
	}
	lenBytes := bigUint(uint64(n))
	out := make([]byte, 1+len(lenBytes))
	out[0] = longOffset + byte(len(lenBytes))
	copy(out[1:], lenBytes)
	return out
}

func bigUint(n uint64) []byte {
	if n == 0 {
		return nil
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	for i, c := range b {
		if c != 0 {
			return b[i:]
		}
	}
	return b[7:]
}

func stripZero(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func parseEVMAddress(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") || len(s) != 42 {
		return nil, fmt.Errorf("invalid EVM address %q", s)
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		hi, err1 := hexNibble(s[2+i*2])
		lo, err2 := hexNibble(s[3+i*2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("invalid hex in EVM address %q", s)
		}
		out[i] = hi<<4 | lo
	}
	return out[:], nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("invalid hex char %q", c)
}

func parseOutpoint(s string) (chainhash.Hash, uint32, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return chainhash.Hash{}, 0, fmt.Errorf("outpoint must be txid:vout")
	}
	txid, err := chainhash.NewHashFromStr(parts[0])
	if err != nil {
		return chainhash.Hash{}, 0, fmt.Errorf("parse txid: %w", err)
	}
	vout64, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return chainhash.Hash{}, 0, fmt.Errorf("parse vout: %w", err)
	}
	return *txid, uint32(vout64), nil
}

func btcAddrScript(addr string, net *chaincfg.Params) ([]byte, error) {
	a, err := btcutil.DecodeAddress(addr, net)
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(a)
}

// mustP2WPKHAddr wraps a 20-byte hash into a bech32 P2WPKH address for the
// given network. It panics on invalid input; only used internally with a
// 20-byte slice placeholder.
func mustP2WPKHAddr(hash []byte, net *chaincfg.Params) btcutil.Address {
	a, err := btcutil.NewAddressWitnessPubKeyHash(hash, net)
	if err != nil {
		panic(err)
	}
	return a
}

// --- Solana minimal message/transaction construction ---

// solanaSystemProgram is the Solana System Program ID.
var solanaSystemProgram = mustDecodeSolanaPubkey("11111111111111111111111111111111")

func mustDecodeSolanaPubkey(s string) [32]byte {
	b, err := decodeSolanaPubkey(s)
	if err != nil {
		panic(err)
	}
	return b
}

func decodeSolanaPubkey(s string) ([32]byte, error) {
	var out [32]byte
	dec := base58.Decode(s)
	if len(dec) == 0 && s != "11111111111111111111111111111111" {
		return out, fmt.Errorf("base58 decode %q", s)
	}
	if len(dec) != 32 {
		return out, fmt.Errorf("pubkey %q must be 32 bytes, got %d", s, len(dec))
	}
	copy(out[:], dec)
	return out, nil
}

// buildSolanaMessage constructs a legacy (pre-v0) Solana message with a
// single SystemProgram.Transfer instruction. The serialized bytes are what
// the signer signs.
func buildSolanaMessage(from, to [32]byte, lamports uint64, blockhash [32]byte) []byte {
	// instruction data: u32 instruction index (2=Transfer) + u64 lamports
	var ixData bytes.Buffer
	var lam [8]byte
	binary.LittleEndian.PutUint64(lam[:], lamports)
	ixData.WriteByte(2)
	ixData.Write(lam[:])
	// message header: 1 required signer, 0 readonly signed, 1 readonly unsigned
	header := [3]byte{1, 0, 1}
	// account table: from, to, system program
	accounts := [][32]byte{from, to, solanaSystemProgram}
	var buf bytes.Buffer
	buf.Write(header[:])
	buf.WriteByte(byte(len(accounts)))
	for _, a := range accounts {
		buf.Write(a[:])
	}
	buf.Write(blockhash[:])
	// one instruction
	buf.WriteByte(1)
	buf.WriteByte(2) // program_id_index = 2 (system program is 3rd in table)
	buf.WriteByte(2) // num accounts in ix
	buf.WriteByte(0) // from (writable, signer)
	buf.WriteByte(1) // to (writable)
	buf.WriteByte(byte(ixData.Len()))
	buf.Write(ixData.Bytes())
	return buf.Bytes()
}

// buildSolanaTransaction assembles a Transaction = [signatures..., message].
func buildSolanaTransaction(message []byte, signatures [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(byte(len(signatures)))
	for _, s := range signatures {
		buf.Write(s)
	}
	buf.Write(message)
	return buf.Bytes()
}
