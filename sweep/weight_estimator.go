package sweep

import (
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

// sizeEstimator wraps a standard weight estimator instance.
type sizeEstimator struct {
	estimator   input.TxSizeEstimator
	feeRate     chainfee.AtomPerKByte
	parents     map[chainhash.Hash]struct{}
	parentsFee  dcrutil.Amount
	parentsSize int64
}

// newSizeEstimator instantiates a new sweeper size estimator.
func newSizeEstimator(feeRate chainfee.AtomPerKByte) *sizeEstimator {
	return &sizeEstimator{
		feeRate: feeRate,
		parents: make(map[chainhash.Hash]struct{}),
	}
}

// clone returns a copy of this weight estimator.
func (w *sizeEstimator) clone() *sizeEstimator {
	parents := make(map[chainhash.Hash]struct{}, len(w.parents))
	for hash := range w.parents {
		parents[hash] = struct{}{}
	}

	return &sizeEstimator{
		estimator:   w.estimator,
		feeRate:     w.feeRate,
		parents:     parents,
		parentsFee:  w.parentsFee,
		parentsSize: w.parentsSize,
	}
}

// add adds the size of the given input to the size estimate.
func (w *sizeEstimator) add(inp input.Input) error {
	// If there is a parent tx, add the parent's fee and size.
	w.tryAddParent(inp)

	wt := inp.WitnessType()

	return wt.AddSizeEstimation(&w.estimator)
}

// tryAddParent examines the input and updates parent tx totals if required for
// cpfp.
func (w *sizeEstimator) tryAddParent(inp input.Input) {
	// Get unconfirmed parent info from the input.
	unconfParent := inp.UnconfParent()

	// If there is no parent, there is nothing to add.
	if unconfParent == nil {
		return
	}

	// If we've already accounted for the parent tx, don't do it
	// again. This can happens when two outputs of the parent tx are
	// included in the same sweep tx.
	parentHash := inp.OutPoint().Hash
	if _, ok := w.parents[parentHash]; ok {
		return
	}

	// Calculate parent fee rate.
	parentFeeRate := chainfee.AtomPerKByte(unconfParent.Fee) * 1000 /
		chainfee.AtomPerKByte(unconfParent.Size)

	// Ignore parents that pay at least the fee rate of this transaction.
	// Parent pays for child is not happening.
	if parentFeeRate >= w.feeRate {
		return
	}

	// Include parent.
	w.parents[parentHash] = struct{}{}
	w.parentsFee += unconfParent.Fee
	w.parentsSize += unconfParent.Size
}

// addP2PKHOutput updates the size estimate to account for an additional
// native P2PKH output.
func (w *sizeEstimator) addP2PKHOutput() {
	w.estimator.AddP2PKHOutput()
}

// size gets the estimated size of the transaction.
func (w *sizeEstimator) size() int {
	return int(w.estimator.Size())
}

// fee returns the tx fee to use for the aggregated inputs and outputs, taking
// into account unconfirmed parent transactions (cpfp).
func (w *sizeEstimator) fee() dcrutil.Amount {
	// Calculate fee and size for just this tx.
	childSize := w.estimator.Size()

	// Add combined weight of unconfirmed parent txes.
	totalSize := childSize + w.parentsSize

	// Subtract fee already paid by parents.
	fee := w.feeRate.FeeForSize(totalSize) - w.parentsFee

	// Clamp the fee to what would be required if no parent txes were paid
	// for. This is to make sure no rounding errors can get us into trouble.
	childFee := w.feeRate.FeeForSize(childSize)
	if childFee > fee {
		fee = childFee
	}

	return fee
}
