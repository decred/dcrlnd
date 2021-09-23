package lnwallet

import (
	"decred.org/dcrwallet/v3/wallet/txrules"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

// DustLimitForSize retrieves the dust limit for a given pkscript size.  It
// must be called with a proper size parameter or else a panic occurs.
func DustLimitForSize(scriptSize int64) dcrutil.Amount {
	// With the size of the script, determine which type of pkscript to
	// create. This will be used in the call to GetDustThreshold. We pass
	// in an empty byte slice since the contents of the script itself don't
	// matter.
	switch scriptSize {
	case input.P2PKHPkScriptSize, input.P2SHPkScriptSize:
	default:
		panic("invalid script size")

	}

	return defaultDustLimit()
}

// defaultDustLimit is used to calculate the default dust threshold limit,
// assuming the network uses the defaultRelayFeePerKb of the wallet.
func defaultDustLimit() dcrutil.Amount {
	return DustThresholdForRelayFee(chainfee.AtomPerKByte(txrules.DefaultRelayFeePerKb))
}

// DustThresholdForRelayFee returns the minimum amount an output of the given
// size should have in order to not be considered a dust output for networks
// using the provided relay fee.
//
// It is assumed the output is paying to a P2PKH script.
func DustThresholdForRelayFee(relayFeeRate chainfee.AtomPerKByte) dcrutil.Amount {
	// Size to redeem a p2pkh script is the size of an input + size of a
	// serialized p2pkh signature script (varint length + OP_DATA_73 + 73 +
	// OP_DATA_33 + 33)
	//
	// This adds up to 165 bytes.
	inputRedeemSize := input.InputSize + input.P2PKHSigScriptSize

	// Calculate the total (estimated) cost to the network.  This is
	// calculated using the serialize size of the output plus the serial
	// size of a transaction input which redeems it.  The output is assumed
	// to be compressed P2PKH as this is the most common script type. The serialized
	// varint size of a P2PKH script is 1.
	//
	// This adds up to 201 bytes.
	scriptSize := input.P2PKHPkScriptSize
	totalSize := input.OutputSize + 1 + scriptSize + inputRedeemSize

	// Calculate the relay fee for this test tx in atoms, given its
	// estimated totalSize and the provided relayFeeRate in atoms/kB.
	//
	// With the currently standard min relay fee rate of 10000 atoms/kB,
	// this adds up to 2010.
	relayFee := totalSize * int64(relayFeeRate) / 1000

	// Threshold for dustiness is determined as 3 times the relay fee.  The
	// final result for the currently standard fee rate is 6030 atoms.
	return dcrutil.Amount(3 * relayFee)
}
