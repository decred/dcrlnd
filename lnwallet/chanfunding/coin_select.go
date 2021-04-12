package chanfunding

import (
	"fmt"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

// ErrInsufficientFunds is a type matching the error interface which is
// returned when coin selection for a new funding transaction fails to due
// having an insufficient amount of confirmed funds.
type ErrInsufficientFunds struct {
	amountAvailable dcrutil.Amount
	amountSelected  dcrutil.Amount
}

// Error returns a human readable string describing the error.
func (e *ErrInsufficientFunds) Error() string {
	return fmt.Sprintf("not enough witness outputs to create funding "+
		"transaction, need %v only have %v  available",
		e.amountAvailable, e.amountSelected)
}

// errUnsupportedInput is a type matching the error interface, which is returned
// when trying to calculate the fee of a transaction that references an
// unsupported script in the outpoint of a transaction input.
type errUnsupportedInput struct {
	PkScript []byte
}

// Error returns a human readable string describing the error.
func (e *errUnsupportedInput) Error() string {
	return fmt.Sprintf("unsupported address type: %x", e.PkScript)
}

// Coin represents a spendable UTXO which is available for channel funding.
// This UTXO need not reside in our internal wallet as an example, and instead
// may be derived from an existing watch-only wallet. It wraps both the output
// present within the UTXO set, and also the outpoint that generates this coin.
type Coin struct {
	wire.TxOut

	wire.OutPoint
}

// selectInputs selects a slice of inputs necessary to meet the specified
// selection amount. If input selection is unable to succeed due to insufficient
// funds, a non-nil error is returned. Additionally, the total amount of the
// selected coins are returned in order for the caller to properly handle
// change+fees.
func selectInputs(amt dcrutil.Amount, coins []Coin) (dcrutil.Amount, []Coin, error) {
	atomSelected := dcrutil.Amount(0)
	for i, coin := range coins {
		atomSelected += dcrutil.Amount(coin.Value)
		if atomSelected >= amt {
			return atomSelected, coins[:i+1], nil
		}
	}

	return 0, nil, &ErrInsufficientFunds{amt, atomSelected}
}

// calculateFees returns for the specified utxos and fee rate two fee
// estimates, one calculated using a change output and one without. The weight
// added to the estimator from a change output is for a P2WKH output.
func calculateFees(utxos []Coin, feeRate chainfee.AtomPerKByte) (dcrutil.Amount,
	dcrutil.Amount, error) {

	var sizeEstimate input.TxSizeEstimator
	for _, utxo := range utxos {
		scriptClass := stdscript.DetermineScriptType(utxo.Version,
			utxo.PkScript)

		switch scriptClass {
		case stdscript.STPubKeyHashEcdsaSecp256k1:
			sizeEstimate.AddP2PKHInput()
		default:
			return 0, 0, &errUnsupportedInput{utxo.PkScript}
		}
	}

	// Channel funding multisig output is P2SH.
	sizeEstimate.AddP2SHOutput()

	// Estimate the fee required for a transaction without a change
	// output.
	totalSize := sizeEstimate.Size()
	requiredFeeNoChange := feeRate.FeeForSize(totalSize)

	// Estimate the fee required for a transaction with a change output.
	// Assume that change output is a P2PKH output.
	sizeEstimate.AddP2PKHOutput()

	// Now that we have added the change output, redo the fee
	// estimate.
	totalSize = sizeEstimate.Size()
	requiredFeeWithChange := feeRate.FeeForSize(totalSize)

	return requiredFeeNoChange, requiredFeeWithChange, nil
}

// sanityCheckFee checks if the specified fee amounts to over 20% of the total
// output amount and raises an error.
func sanityCheckFee(totalOut, fee dcrutil.Amount) error {
	// Fail if more than 20% goes to fees.
	// TODO(halseth): smarter fee limit. Make configurable or dynamic wrt
	// total funding size?
	if fee > totalOut/5 {
		return fmt.Errorf("fee %v on total output value %v", fee,
			totalOut)
	}
	return nil
}

// CoinSelect attempts to select a sufficient amount of coins, including a
// change output to fund amt satoshis, adhering to the specified fee rate. The
// specified fee rate should be expressed in sat/kw for coin selection to
// function properly.
func CoinSelect(feeRate chainfee.AtomPerKByte, amt dcrutil.Amount,
	coins []Coin) ([]Coin, dcrutil.Amount, error) {

	amtNeeded := amt
	for {
		// First perform an initial round of coin selection to estimate
		// the required fee.
		totalAtoms, selectedUtxos, err := selectInputs(amtNeeded, coins)
		if err != nil {
			return nil, 0, err
		}

		// Obtain fee estimates both with and without using a change
		// output.
		_, requiredFeeWithChange, err := calculateFees(
			selectedUtxos, feeRate,
		)
		if err != nil {
			return nil, 0, err
		}

		// The difference between the selected amount and the amount
		// requested will be used to pay fees, and generate a change
		// output with the remaining.
		overShootAmt := totalAtoms - amt

		// Based on the estimated size and fee rate, if the excess
		// amount isn't enough to pay fees, then increase the requested
		// coin amount by the estimate required fee, performing another
		// round of coin selection.
		if overShootAmt < requiredFeeWithChange {
			amtNeeded = amt + requiredFeeWithChange
			continue
		}

		// If the fee is sufficient, then calculate the size of the
		// change output.
		changeAmt := overShootAmt - requiredFeeWithChange

		return selectedUtxos, changeAmt, nil
	}
}

// CoinSelectSubtractFees attempts to select coins such that we'll spend up to
// amt in total after fees, adhering to the specified fee rate. The selected
// coins, the final output and change values are returned.
func CoinSelectSubtractFees(feeRate chainfee.AtomPerKByte, amt,
	dustLimit dcrutil.Amount, coins []Coin) ([]Coin, dcrutil.Amount,
	dcrutil.Amount, error) {

	// First perform an initial round of coin selection to estimate
	// the required fee.
	totalAtoms, selectedUtxos, err := selectInputs(amt, coins)
	if err != nil {
		return nil, 0, 0, err
	}

	// Obtain fee estimates both with and without using a change
	// output.
	requiredFeeNoChange, requiredFeeWithChange, err := calculateFees(
		selectedUtxos, feeRate)
	if err != nil {
		return nil, 0, 0, err
	}

	// For a transaction without a change output, we'll let everything go
	// to our multi-sig output after subtracting fees.
	outputAmt := totalAtoms - requiredFeeNoChange
	changeAmt := dcrutil.Amount(0)

	// If the the output is too small after subtracting the fee, the coin
	// selection cannot be performed with an amount this small.
	if outputAmt <= dustLimit {
		return nil, 0, 0, fmt.Errorf("output amount(%v) after "+
			"subtracting fees(%v) below dust limit(%v)", outputAmt,
			requiredFeeNoChange, dustLimit)
	}

	// For a transaction with a change output, everything we don't spend
	// will go to change.
	newOutput := amt - requiredFeeWithChange
	newChange := totalAtoms - amt

	// If adding a change output leads to both outputs being above
	// the dust limit, we'll add the change output. Otherwise we'll
	// go with the no change tx we originally found.
	if newChange > dustLimit && newOutput > dustLimit {
		outputAmt = newOutput
		changeAmt = newChange
	}

	// Sanity check the resulting output values to make sure we
	// don't burn a great part to fees.
	totalOut := outputAmt + changeAmt
	err = sanityCheckFee(totalOut, totalAtoms-totalOut)
	if err != nil {
		return nil, 0, 0, err
	}

	return selectedUtxos, outputAmt, changeAmt, nil
}
