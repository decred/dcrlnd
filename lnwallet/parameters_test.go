package lnwallet

import (
	"fmt"
	"testing"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestDefaultRoutingFeeLimitForAmount tests that we use the correct default
// routing fee depending on the amount.
func TestDefaultRoutingFeeLimitForAmount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		amount        lnwire.MilliAtom
		expectedLimit lnwire.MilliAtom
	}{
		{
			amount:        1,
			expectedLimit: 1,
		},
		{
			amount:        lnwire.NewMAtomsFromAtoms(1_000),
			expectedLimit: lnwire.NewMAtomsFromAtoms(1_000),
		},
		{
			amount:        lnwire.NewMAtomsFromAtoms(1_001),
			expectedLimit: 50_050,
		},
		{
			amount:        5_000_000_000,
			expectedLimit: 250_000_000,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(fmt.Sprintf("%d sats", test.amount), func(t *testing.T) {
			feeLimit := DefaultRoutingFeeLimitForAmount(test.amount)
			require.Equal(t, int64(test.expectedLimit), int64(feeLimit))
		})
	}
}

// TestDustLimitForSize tests that we receive the expected dust limits for
// various script types from btcd's GetDustThreshold function.
func TestDustLimitForSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		size          int64
		expectedLimit dcrutil.Amount
	}{
		{
			name:          "p2pkh dust limit",
			size:          input.P2PKHPkScriptSize,
			expectedLimit: 6030,
		},
		{
			name:          "p2sh dust limit",
			size:          input.P2SHPkScriptSize,
			expectedLimit: 6030,
		},
		{
			name:          "unknown pk script limit",
			size:          25,
			expectedLimit: 6030,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			dustlimit := DustLimitForSize(test.size)
			require.Equal(t, test.expectedLimit, dustlimit)
		})
	}
}
