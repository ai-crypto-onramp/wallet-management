package deriver

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

// decompressSecp256k1 takes a 33-byte compressed pubkey and returns the
// 65-byte uncompressed form (0x04 || X || Y).
func decompressSecp256k1(compressed []byte) ([]byte, error) {
	if len(compressed) != 33 {
		return nil, fmt.Errorf("invalid compressed pubkey length %d", len(compressed))
	}
	pk, err := btcec.ParsePubKey(compressed)
	if err != nil {
		return nil, fmt.Errorf("parse pubkey: %w", err)
	}
	out := make([]byte, 65)
	out[0] = 0x04
	copy(out[1:33], pk.X().Bytes())
	copy(out[33:65], pk.Y().Bytes())
	return out, nil
}