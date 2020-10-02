package autopilot

import (
	"testing"
	"time"

	prand "math/rand"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/lnwire"
)

func TestConstraintsChannelBudget(t *testing.T) {
	t.Parallel()

	prand.Seed(time.Now().Unix())

	const (
		minChanSize = 0
		maxChanSize = dcrutil.Amount(dcrutil.AtomsPerCoin)

		chanLimit = 3

		threshold = 0.5
	)

	constraints := NewConstraints(
		minChanSize,
		maxChanSize,
		chanLimit,
		0,
		threshold,
	)

	randChanID := func() lnwire.ShortChannelID {
		return lnwire.NewShortChanIDFromInt(uint64(prand.Int63()))
	}

	testCases := []struct {
		channels  []LocalChannel
		walletAmt dcrutil.Amount

		needMore     bool
		amtAvailable dcrutil.Amount
		numMore      uint32
	}{
		// Many available funds, but already have too many active open
		// channels.
		{
			[]LocalChannel{
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(prand.Int31()),
				},
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(prand.Int31()),
				},
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(prand.Int31()),
				},
			},
			dcrutil.Amount(dcrutil.AtomsPerCoin * 10),
			false,
			0,
			0,
		},

		// Ratio of funds in channels and total funds meets the
		// threshold.
		{
			[]LocalChannel{
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
			},
			dcrutil.Amount(dcrutil.AtomsPerCoin * 2),
			false,
			0,
			0,
		},

		// Ratio of funds in channels and total funds is below the
		// threshold. We have 10 DCR allocated amongst channels and
		// funds, atm. We're targeting 50%, so 5 DCR should be
		// allocated. Only 1 DCR is atm, so 4 DCR should be
		// recommended. We should also request 2 more channels as the
		// limit is 3.
		{
			[]LocalChannel{
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
			},
			dcrutil.Amount(dcrutil.AtomsPerCoin * 9),
			true,
			dcrutil.Amount(dcrutil.AtomsPerCoin * 4),
			2,
		},

		// Ratio of funds in channels and total funds is below the
		// threshold. We have 14 DCR total amongst the wallet's
		// balance, and our currently opened channels. Since we're
		// targeting a 50% allocation, we should commit 7 DCR. The
		// current channels commit 4 DCR, so we should expected 3 DCR
		// to be committed. We should only request a single additional
		// channel as the limit is 3.
		{
			[]LocalChannel{
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin * 3),
				},
			},
			dcrutil.Amount(dcrutil.AtomsPerCoin * 10),
			true,
			dcrutil.Amount(dcrutil.AtomsPerCoin * 3),
			1,
		},

		// Ratio of funds in channels and total funds is above the
		// threshold.
		{
			[]LocalChannel{
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
				{
					ChanID:   randChanID(),
					Capacity: dcrutil.Amount(dcrutil.AtomsPerCoin),
				},
			},
			dcrutil.Amount(dcrutil.AtomsPerCoin),
			false,
			0,
			0,
		},
	}

	for i, testCase := range testCases {
		amtToAllocate, numMore := constraints.ChannelBudget(
			testCase.channels, testCase.walletAmt,
		)

		if amtToAllocate != testCase.amtAvailable {
			t.Fatalf("test #%v: expected %v, got %v",
				i, testCase.amtAvailable, amtToAllocate)
		}
		if numMore != testCase.numMore {
			t.Fatalf("test #%v: expected %v, got %v",
				i, testCase.numMore, numMore)
		}
	}
}
