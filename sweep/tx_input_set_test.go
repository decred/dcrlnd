package sweep

import (
	"testing"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet"
)

// TestTxInputSet tests adding various sized inputs to the set.
func TestTxInputSet(t *testing.T) {
	const (
		feeRate   = 5e4
		relayFee  = 1e4
		maxInputs = 10
	)
	set := newTxInputSet(nil, feeRate, relayFee, maxInputs)

	wantDust := dcrutil.Amount(6030)
	if set.dustLimit != 6030 {
		t.Fatalf("incorrect dust limit; want=%d got=%d", wantDust, set.dustLimit)
	}

	// Create a 10001 atom input. The fee to sweep this input to a P2PKH
	// output is 10850 atoms. That means that this input yields -849 atoms
	// and we expect it not to be added.
	if set.add(createP2PKHInput(10001), constraintsRegular) {
		t.Fatal("expected add of negatively yielding input to fail")
	}

	// A 15000 atom input should be accepted into the set, because it
	// yields positively.
	if !set.add(createP2PKHInput(15001), constraintsRegular) {
		t.Fatal("expected add of positively yielding input to succeed")
	}

	// The tx output should now be 15000-10850 = 4151 atoms. The dust limit
	// isn't reached yet.
	wantOutputValue := dcrutil.Amount(4151)
	if set.outputValue != wantOutputValue {
		t.Fatalf("unexpected output value. want=%d got=%d", wantOutputValue, set.outputValue)
	}
	if set.dustLimitReached() {
		t.Fatal("expected dust limit not yet to be reached")
	}

	// Add a 13703 atoms input. This increases the tx fee to 19150 atoms.
	// The tx output should now be 13703+15001 - 19150 = 9554 atoms.
	if !set.add(createP2PKHInput(13703), constraintsRegular) {
		t.Fatal("expected add of positively yielding input to succeed")
	}
	wantOutputValue = 9554
	if set.outputValue != wantOutputValue {
		t.Fatalf("unexpected output value. want=%d got=%d", wantOutputValue, set.outputValue)
	}
	if !set.dustLimitReached() {
		t.Fatal("expected dust limit to be reached")
	}
}

// TestTxInputSetFromWallet tests adding a wallet input to a TxInputSet to
// reach the dust limit.
func TestTxInputSetFromWallet(t *testing.T) {
	const (
		feeRate   = 2e4
		relayFee  = 1e4
		maxInputs = 10
	)

	wallet := &mockWallet{}
	set := newTxInputSet(wallet, feeRate, relayFee, maxInputs)

	// Add a 10000 atoms input to the set. It yields positively, but
	// doesn't reach the output dust limit.
	if !set.add(createP2PKHInput(10000), constraintsRegular) {
		t.Fatal("expected add of positively yielding input to succeed")
	}
	if set.dustLimitReached() {
		t.Fatal("expected dust limit not yet to be reached")
	}

	// Expect that adding a negative yield input fails.
	if set.add(createP2PKHInput(50), constraintsRegular) {
		t.Fatal("expected negative yield input add to fail")
	}

	// Force add the negative yield input. It should succeed.
	if !set.add(createP2PKHInput(50), constraintsForce) {
		t.Fatal("expected forced add to succeed")
	}

	err := set.tryAddWalletInputsIfNeeded()
	if err != nil {
		t.Fatal(err)
	}

	if !set.dustLimitReached() {
		t.Fatal("expected dust limit to be reached")
	}
}

// createP2PKHInput returns a P2PKH test input with the specified amount.
func createP2PKHInput(amt dcrutil.Amount) input.Input {
	input := createTestInput(int64(amt), input.PublicKeyHash)
	return &input
}

type mockWallet struct {
	Wallet
}

func (m *mockWallet) ListUnspentWitnessFromDefaultAccount(minconfirms, maxconfirms int32) (
	[]*lnwallet.Utxo, error) {

	return []*lnwallet.Utxo{
		{
			AddressType: lnwallet.PubKeyHash,
			Value:       8000,
		},
	}, nil
}
