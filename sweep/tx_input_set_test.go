package sweep

import (
	"testing"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/stretchr/testify/require"
)

// TestTxInputSet tests adding various sized inputs to the set.
func TestTxInputSet(t *testing.T) {
	const (
		feeRate   = 5e4
		maxInputs = 10
	)
	set := newTxInputSet(nil, feeRate, maxInputs)

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

	fee := set.sizeEstimate(true).fee()
	require.Equal(t, dcrutil.Amount(10850), fee)

	// The tx output should now be 15000-10850 = 4151 atoms. The dust limit
	// isn't reached yet.
	wantOutputValue := dcrutil.Amount(4151)
	if set.totalOutput() != wantOutputValue {
		t.Fatalf("unexpected output value. want=%d got=%d", wantOutputValue, set.totalOutput())
	}
	if set.enoughInput() {
		t.Fatal("expected dust limit not yet to be reached")
	}

	// Add a 13703 atoms input. This increases the tx fee to 19150 atoms.
	// The tx output should now be 13703+15001 - 19150 = 9554 atoms.
	if !set.add(createP2PKHInput(13703), constraintsRegular) {
		t.Fatal("expected add of positively yielding input to succeed")
	}
	wantOutputValue = 9554
	if set.totalOutput() != wantOutputValue {
		t.Fatalf("unexpected output value. want=%d got=%d", wantOutputValue, set.totalOutput())
	}
	if !set.enoughInput() {
		t.Fatal("expected dust limit to be reached")
	}
}

// TestTxInputSetFromWallet tests adding a wallet input to a TxInputSet to
// reach the dust limit.
func TestTxInputSetFromWallet(t *testing.T) {
	const (
		feeRate   = 2e4
		maxInputs = 10
	)

	wallet := &mockWallet{}
	set := newTxInputSet(wallet, feeRate, maxInputs)

	// Add a 10000 atoms input to the set. It yields positively, but
	// doesn't reach the output dust limit.
	if !set.add(createP2PKHInput(10000), constraintsRegular) {
		t.Fatal("expected add of positively yielding input to succeed")
	}
	if set.enoughInput() {
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

	if !set.enoughInput() {
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

func (m *mockWallet) ListUnspentWitnessFromDefaultAccount(minConfs, maxConfs int32) (
	[]*lnwallet.Utxo, error) {

	return []*lnwallet.Utxo{
		{
			AddressType: lnwallet.PubKeyHash,
			Value:       8000,
		},
	}, nil
}

type reqInput struct {
	input.Input

	txOut *wire.TxOut
}

func (r *reqInput) RequiredTxOut() *wire.TxOut {
	return r.txOut
}

// TestTxInputSetRequiredOutput tests that the tx input set behaves as expected
// when we add inputs that have required tx outs.
func TestTxInputSetRequiredOutput(t *testing.T) {
	const (
		feeRate   = 50000
		maxInputs = 10
	)
	set := newTxInputSet(nil, feeRate, maxInputs)

	// Attempt to add an input with a required txout below the dust limit.
	// This should fail since we cannot trim such outputs.
	inp := &reqInput{
		Input: createP2PKHInput(10001),
		txOut: &wire.TxOut{
			Value:    10001,
			PkScript: make([]byte, 25),
		},
	}
	require.False(t, set.add(inp, constraintsRegular),
		"expected adding dust required tx out to fail")

	// Create a 15001 atoms input that also has a required TxOut of 15001 atoms.
	// The fee to sweep this input to a P2PKH output is 10850 sats.
	inp = &reqInput{
		Input: createP2PKHInput(15001),
		txOut: &wire.TxOut{
			Value:    15001,
			PkScript: make([]byte, 25),
		},
	}
	require.True(t, set.add(inp, constraintsRegular), "failed adding input")

	// The fee needed to pay for this input and output should be 10850 atoms.
	fee := set.sizeEstimate(false).fee()
	require.Equal(t, dcrutil.Amount(10850), fee)

	// Since the tx set currently pays no fees, we expect the current
	// change to actually be negative, since this is what it would cost us
	// in fees to add a change output.
	feeWithChange := set.sizeEstimate(true).fee()
	if set.changeOutput != -feeWithChange {
		t.Fatalf("expected negative change of %v, had %v",
			-feeWithChange, set.changeOutput)
	}

	// This should also be reflected by not having enough input.
	require.False(t, set.enoughInput())

	// Get a weight estimate without change output, and add an additional
	// input to it.
	dummyInput := createP2PKHInput(1000)
	weight := set.sizeEstimate(false)
	require.NoError(t, weight.add(dummyInput))

	// Now we add a an input that is large enough to pay the fee for the
	// transaction without a change output, but not large enough to afford
	// adding a change output.
	extraInput1 := weight.fee() + 100
	require.True(t, set.add(createP2PKHInput(extraInput1), constraintsRegular),
		"expected add of positively yielding input to succeed")

	// The change should be negative, since we would have to add a change
	// output, which we cannot yet afford.
	if set.changeOutput >= 0 {
		t.Fatal("expected change to be negaitve")
	}

	// Even though we cannot afford a change output, the tx set is valid,
	// since we can pay the fees without the change output.
	require.True(t, set.enoughInput())

	// Get another weight estimate, this time with a change output, and
	// figure out how much we must add to afford a change output.
	weight = set.sizeEstimate(true)
	require.NoError(t, weight.add(dummyInput))

	// We add what is left to reach this value.
	extraInput2 := weight.fee() - extraInput1 + 100

	// Add this input, which should result in the change now being 100 sats.
	require.True(t, set.add(createP2PKHInput(extraInput2), constraintsRegular))

	// The change should be 100, since this is what is left after paying
	// fees in case of a change output.
	change := set.changeOutput
	if change != 100 {
		t.Fatalf("expected change be 100, was %v", change)
	}

	// Even though the change output is dust, we have enough for fees, and
	// we have an output, so it should be considered enough to craft a
	// valid sweep transaction.
	require.True(t, set.enoughInput())

	// Finally we add an input that should push the change output above the
	// dust limit.
	weight = set.sizeEstimate(true)
	require.NoError(t, weight.add(dummyInput))

	// We expect the change to everything that is left after paying the tx
	// fee.
	extraInput3 := weight.fee() - extraInput1 - extraInput2 + 1000
	require.True(t, set.add(createP2PKHInput(extraInput3), constraintsRegular))

	change = set.changeOutput
	if change != 1000 {
		t.Fatalf("expected change to be %v, had %v", 1000, change)

	}
	require.True(t, set.enoughInput())
}
