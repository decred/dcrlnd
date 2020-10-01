package psbt

import (
	"bytes"
	"fmt"

	"github.com/decred/dcrd/wire"
)

// SumUtxoInputValues tries to extract the sum of all inputs specified in the
// UTXO fields of the PSBT. An error is returned if an input is specified that
// does not contain any UTXO information.
func SumUtxoInputValues(packet *Packet) (int64, error) {
	// We take the TX ins of the unsigned TX as the truth for how many
	// inputs there should be, as the fields in the extra data part of the
	// PSBT can be empty.
	if len(packet.UnsignedTx.TxIn) != len(packet.Inputs) {
		return 0, fmt.Errorf("TX input length doesn't match PSBT " +
			"input length")

	}
	inputSum := int64(0)
	for idx, in := range packet.Inputs {
		switch {
		case in.WitnessUtxo != nil:
			// Witness UTXOs only need to reference the TxOut.
			inputSum += in.WitnessUtxo.Value

		case in.NonWitnessUtxo != nil:
			// Non-witness UTXOs reference to the whole transaction
			// the UTXO resides in.
			utxOuts := in.NonWitnessUtxo.TxOut
			txIn := packet.UnsignedTx.TxIn[idx]
			inputSum += utxOuts[txIn.PreviousOutPoint.Index].Value

		default:
			return 0, fmt.Errorf("input %d has no UTXO information",
				idx)
		}
	}
	return inputSum, nil
}

// TxOutsEqual returns true if two transaction outputs are equal.
func TxOutsEqual(out1, out2 *wire.TxOut) bool {
	if out1 == nil || out2 == nil {
		return out1 == out2

	}
	return out1.Value == out2.Value &&
		bytes.Equal(out1.PkScript, out2.PkScript)

}

// VerifyOutputsEqual verifies that the two slices of transaction outputs are
// deep equal to each other. We do the length check and manual loop to provide
// better error messages to the user than just returning "not equal".
func VerifyOutputsEqual(outs1, outs2 []*wire.TxOut) error {
	if len(outs1) != len(outs2) {
		return fmt.Errorf("number of outputs are different")

	}
	for idx, out := range outs1 {
		// There is a byte slice in the output so we can't use the
		// equality operator.
		if !TxOutsEqual(out, outs2[idx]) {
			return fmt.Errorf("output %d is different", idx)
		}
	}
	return nil
}

// VerifyInputPrevOutpointsEqual verifies that the previous outpoints of the
// two slices of transaction inputs are deep equal to each other. We do the
// length check and manual loop to provide better error messages to the user
// than just returning "not equal".
func VerifyInputPrevOutpointsEqual(ins1, ins2 []*wire.TxIn) error {
	if len(ins1) != len(ins2) {
		return fmt.Errorf("number of inputs are different")
	}
	for idx, in := range ins1 {
		if in.PreviousOutPoint != ins2[idx].PreviousOutPoint {
			return fmt.Errorf("previous outpoint of input %d is "+
				"different", idx)
		}
	}
	return nil
}
