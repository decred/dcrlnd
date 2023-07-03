package sweep

import (
	"github.com/decred/dcrlnd/input"
)

// sizeEstimator wraps a standard weight estimator instance.
type sizeEstimator struct {
	estimator input.TxSizeEstimator
}

// newSizeEstimator instantiates a new sweeper weight estimator.
func newSizeEstimator() *sizeEstimator {
	return &sizeEstimator{}
}

// clone returns a copy of this weight estimator.
func (w *sizeEstimator) clone() *sizeEstimator {
	return &sizeEstimator{
		estimator: w.estimator,
	}
}

// add adds the weight of the given input to the weight estimate.
func (w *sizeEstimator) add(inp input.Input) error {
	wt := inp.WitnessType()

	return wt.AddSizeEstimation(&w.estimator)
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
