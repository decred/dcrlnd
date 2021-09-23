package lnwallet

import (
	"testing"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/stretchr/testify/require"
)

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
