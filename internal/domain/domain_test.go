package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestChainIsEVM(t *testing.T) {
	t.Parallel()
	evm := []Chain{ChainEthereum, ChainPolygon, ChainArbitrum, ChainBase, ChainOptimism}
	for _, c := range evm {
		if !c.IsEVM() {
			t.Errorf("%s should be EVM", c)
		}
	}
	non := []Chain{ChainSolana, ChainBitcoin, Chain("cardano")}
	for _, c := range non {
		if c.IsEVM() {
			t.Errorf("%s should not be EVM", c)
		}
	}
}

func TestValidators(t *testing.T) {
	t.Parallel()
	if !ValidChain(ChainEthereum) || ValidChain(Chain("nope")) {
		t.Error("ValidChain mismatch")
	}
	if !ValidWalletType(WalletTypeHot) || ValidWalletType("quantum") {
		t.Error("ValidWalletType mismatch")
	}
	if !ValidWalletState(WalletStateActive) || ValidWalletState("zombie") {
		t.Error("ValidWalletState mismatch")
	}
}

func TestErrWalletRetired(t *testing.T) {
	if ErrWalletRetired.Error() != "wallet is retired" {
		t.Errorf("unexpected error text: %s", ErrWalletRetired)
	}
}

func TestWalletAddressTypes(t *testing.T) {
	w := Wallet{ID: uuid.New(), Chain: ChainBitcoin, Type: WalletTypeCold, State: WalletStateActive}
	if w.Chain != ChainBitcoin || w.Type != WalletTypeCold || w.State != WalletStateActive {
		t.Error("wallet field assignment mismatch")
	}
	rd := 3
	w.RotationDays = &rd
	if *w.RotationDays != 3 {
		t.Error("rotation days pointer mismatch")
	}
	a := Address{ID: uuid.New(), WalletID: w.ID, Chain: ChainBitcoin, State: AddressStateActive}
	if a.State != AddressStateActive {
		t.Error("address state mismatch")
	}
}