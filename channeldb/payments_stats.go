package channeldb

import (
	"github.com/decred/dcrlnd/kvdb"
)

// PaymentCountStats returns stats about the payments stored in the DB.
type PaymentCountStats struct {
	Total     uint64
	Failed    uint64
	Succeeded uint64

	HTLCAttempts uint64
	HTLCFailed   uint64
	HTLCSettled  uint64

	OldDupePayments uint64
}

// CalcPaymentStats goes through the DB, counting up the payment stats.
func (d *DB) CalcPaymentStats() (*PaymentCountStats, error) {
	res := &PaymentCountStats{}
	err := kvdb.View(d, func(tx kvdb.RTx) error {
		paymentsBucket := tx.ReadBucket(paymentsRootBucket)
		if paymentsBucket == nil {
			return nil
		}

		// Standard payments.
		return paymentsBucket.ForEach(func(k, v []byte) error {
			payBucket := paymentsBucket.NestedReadBucket(k)
			if payBucket == nil {
				return nil
			}

			res.Total += 1

			htlcsBucket := payBucket.NestedReadBucket(paymentHtlcsBucket)
			if htlcsBucket != nil {
				var inflight, settled bool
				err := htlcsBucket.ForEach(func(k, _ []byte) error {
					res.HTLCAttempts += 1
					htlcBucket := htlcsBucket.NestedReadBucket(k)
					hasFail := htlcBucket.Get(htlcFailInfoKey) != nil
					hasSettle := htlcBucket.Get(htlcSettleInfoKey) != nil
					if hasFail {
						res.HTLCFailed += 1
					} else if hasSettle {
						res.HTLCSettled += 1
						settled = true
					} else {
						inflight = true
					}
					return nil
				})
				if err != nil {
					return err
				}

				hasFailureReason := payBucket.Get(paymentFailInfoKey) != nil

				switch {
				// If any of the the HTLCs did succeed and there are no HTLCs in
				// flight, the payment succeeded.
				case !inflight && settled:
					res.Succeeded += 1

				// If we have no in-flight HTLCs, and the payment failure is set, the
				// payment is considered failed.
				case !inflight && hasFailureReason:
					res.Failed += 1
				}

			}

			// Old duplicate payments.
			dupBucket := payBucket.NestedReadBucket(duplicatePaymentsBucket)
			if dupBucket == nil {
				return nil
			}

			return dupBucket.ForEach(func(k, v []byte) error {
				subBucket := dupBucket.NestedReadBucket(k)
				if subBucket == nil {
					return nil
				}
				res.OldDupePayments += 1
				return nil
			})
		})
	}, func() {})

	return res, err
}
