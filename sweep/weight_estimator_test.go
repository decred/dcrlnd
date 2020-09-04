package sweep

import (
	"testing"

	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

// TestWeightEstimator tests weight estimation for inputs with and without
// unconfirmed parents.
func TestWeightEstimator(t *testing.T) {
	testFeeRate := chainfee.AtomPerKByte(20000)

	w := newSizeEstimator(testFeeRate)

	// Add an input without unconfirmed parent tx.
	input1 := input.MakeBaseInput(
		&wire.OutPoint{}, input.CommitmentAnchor,
		&input.SignDescriptor{}, 0, nil,
	)

	require.NoError(t, w.add(&input1))

	// The expectations is that this input is added.
	const expectedWeight1 = 12 + 57 + 1 + 115 + 3
	require.Equal(t, expectedWeight1, w.size())
	require.Equal(t, testFeeRate.FeeForSize(expectedWeight1), w.fee())

	// Define a parent transaction that pays a fee of 30000 sat/kw.
	parentTxHighFee := &input.TxInfo{
		Size: 100,
		Fee:  3000,
	}

	// Add an output of the parent tx above.
	input2 := input.MakeBaseInput(
		&wire.OutPoint{}, input.CommitmentAnchor,
		&input.SignDescriptor{}, 0,
		parentTxHighFee,
	)

	require.NoError(t, w.add(&input2))

	// Pay for parent isn't possible because the parent pays a higher fee
	// rate than the child. We expect no additional fee on the child.
	const expectedWeight2 = expectedWeight1 + 57 + 1 + 115
	require.Equal(t, expectedWeight2, w.size())
	require.Equal(t, testFeeRate.FeeForSize(expectedWeight2), w.fee())

	// Define a parent transaction that pays a fee of 10000 sat/kw.
	parentTxLowFee := &input.TxInfo{
		Size: 100,
		Fee:  1000,
	}

	// Add an output of the low-fee parent tx above.
	input3 := input.MakeBaseInput(
		&wire.OutPoint{}, input.CommitmentAnchor,
		&input.SignDescriptor{}, 0,
		parentTxLowFee,
	)
	require.NoError(t, w.add(&input3))

	// Expect the weight to increase because of the third input.
	const expectedWeight3 = expectedWeight2 + 57 + 1 + 115
	require.Equal(t, expectedWeight3, w.size())

	// Expect the fee to cover the child and the parent transaction at 20
	// sat/kw after subtraction of the fee that was already paid by the
	// parent.
	expectedFee := testFeeRate.FeeForSize(
		expectedWeight3+parentTxLowFee.Size,
	) - parentTxLowFee.Fee

	require.Equal(t, expectedFee, w.fee())
}
