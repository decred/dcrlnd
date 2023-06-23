package channeldb

import (
	"testing"

	"github.com/decred/dcrlnd/channeldb/kvdb"
	"github.com/stretchr/testify/require"
)

// assertFetchesInflight asserts that calling FetchInFlightPayments returns
// the required info.
func assertFetchesInflight(t *testing.T, pControl *PaymentControl, info *PaymentCreationInfo) {
	inFlight, err := pControl.FetchInFlightPayments()
	require.Nil(t, err)
	require.Len(t, inFlight, 1)
	require.Equal(t, inFlight[0].Info.PaymentHash, info.PaymentHash)
}

// testPaymentInflightIndexCase is a test case for fetching inflight payments.
// noIndex determines if the index is used from the start of the test case.
// recreateIndex determines if the index is re-created halfway through the
// test case.
func testPaymentInflightIndexCase(t *testing.T, noIndex, recreateIndex bool) {
	db, cleanup, err := MakeTestDB()
	defer cleanup()

	if err != nil {
		t.Fatalf("unable to init db: %v", err)
	}

	pControl := NewPaymentControl(db)

	// If this test case is to run without the payments inflight index, manually drop it.
	if noIndex {
		err = kvdb.Update(db, func(tx kvdb.RwTx) error {
			return tx.DeleteTopLevelBucket(paymentsInflightIndexBucket)
		})
		require.Nil(t, err)
	}

	// Init and settle a sample payment.
	settledInfo, settledAttempt, settlePreimage, err := genInfo()
	require.Nil(t, err)
	err = pControl.InitPayment(settledInfo.PaymentHash, settledInfo)
	require.Nil(t, err)
	_, err = pControl.RegisterAttempt(settledInfo.PaymentHash, settledAttempt)
	require.Nil(t, err)
	_, err = pControl.SettleAttempt(
		settledInfo.PaymentHash, settledAttempt.AttemptID,
		&HTLCSettleInfo{
			Preimage: settlePreimage,
		},
	)
	require.Nil(t, err)

	// Init and fail a sample payment.
	failedInfo, failedAttempt, _, err := genInfo()
	require.Nil(t, err)
	err = pControl.InitPayment(failedInfo.PaymentHash, failedInfo)
	require.Nil(t, err)
	_, err = pControl.RegisterAttempt(failedInfo.PaymentHash, failedAttempt)
	require.Nil(t, err)
	_, err = pControl.FailAttempt(failedInfo.PaymentHash, failedAttempt.AttemptID,
		&HTLCFailInfo{Reason: HTLCFailUnreadable})
	require.Nil(t, err)
	_, err = pControl.Fail(failedInfo.PaymentHash, FailureReasonNoRoute)
	require.Nil(t, err)

	// Setup the test inflight payment.
	info, attempt, preimg, err := genInfo()
	require.Nil(t, err)

	// Init the payment.
	err = pControl.InitPayment(info.PaymentHash, info)
	require.Nil(t, err)

	// We should find one payment inflight.
	assertFetchesInflight(t, pControl, info)

	// Register an attempt. Still inflight.
	_, err = pControl.RegisterAttempt(info.PaymentHash, attempt)
	require.Nil(t, err)
	assertFetchesInflight(t, pControl, info)

	// Fail the attempt. Still inflight.
	_, err = pControl.FailAttempt(info.PaymentHash, attempt.AttemptID,
		&HTLCFailInfo{Reason: HTLCFailUnreadable})
	require.Nil(t, err)
	assertFetchesInflight(t, pControl, info)

	// Make a second attempt.
	attempt.AttemptID += 1
	_, err = pControl.RegisterAttempt(info.PaymentHash, attempt)
	require.Nil(t, err)
	assertFetchesInflight(t, pControl, info)

	// If the test case requires it, recreate the index and test there's
	// still one inflight.
	if recreateIndex {
		err = kvdb.Update(db, func(tx kvdb.RwTx) error {
			return recreatePaymentsInflightIndex(tx)
		})
		require.Nil(t, err)
		assertFetchesInflight(t, pControl, info)
	}

	// Settle. No more inflight.
	_, err = pControl.SettleAttempt(
		info.PaymentHash, attempt.AttemptID,
		&HTLCSettleInfo{
			Preimage: preimg,
		},
	)
	require.Nil(t, err)
	inFlight, err := pControl.FetchInFlightPayments()
	require.Nil(t, err)
	require.Len(t, inFlight, 0)
}

func TestPaymentInflightIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		noIndex  bool
		recreate bool
	}{{
		name:    "no index",
		noIndex: true,
	}, {
		name: "index",
	}, {
		name:     "index and recreate",
		recreate: true,
	}, {
		name:     "no index and recreate",
		noIndex:  true,
		recreate: true,
	}}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testPaymentInflightIndexCase(t, tc.noIndex, tc.recreate)
		})
	}
}
