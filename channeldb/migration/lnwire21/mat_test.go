package lnwire

import (
	"testing"

	"github.com/decred/dcrd/dcrutil/v4"
)

func TestMilliAtomConversion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		mAtAmount MilliAtom
		atAmount  dcrutil.Amount
		dcrAmount float64
	}{
		{
			mAtAmount: 0,
			atAmount:  0,
			dcrAmount: 0,
		},
		{
			mAtAmount: 10,
			atAmount:  0,
			dcrAmount: 0,
		},
		{
			mAtAmount: 999,
			atAmount:  0,
			dcrAmount: 0,
		},
		{
			mAtAmount: 1000,
			atAmount:  1,
			dcrAmount: 1e-8,
		},
		{
			mAtAmount: 10000,
			atAmount:  10,
			dcrAmount: 0.00000010,
		},
		{
			mAtAmount: 100000000000,
			atAmount:  100000000,
			dcrAmount: 1,
		},
		{
			mAtAmount: 2500000000000,
			atAmount:  2500000000,
			dcrAmount: 25,
		},
		{
			mAtAmount: 5000000000000,
			atAmount:  5000000000,
			dcrAmount: 50,
		},
		{
			mAtAmount: 21 * 1e6 * 1e8 * 1e3,
			atAmount:  21 * 1e6 * 1e8,
			dcrAmount: 21 * 1e6,
		},
	}

	for i, test := range testCases {
		if test.mAtAmount.ToAtoms() != test.atAmount {
			t.Fatalf("test #%v: wrong atom amount, expected %v "+
				"got %v", i, int64(test.atAmount),
				int64(test.mAtAmount.ToAtoms()))
		}
		if test.mAtAmount.ToCoin() != test.dcrAmount {
			t.Fatalf("test #%v: wrong dcr amount, expected %v "+
				"got %v", i, test.dcrAmount,
				test.mAtAmount.ToCoin())
		}
	}
}
