package channeldb

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/btcsuite/btcwallet/walletdb"
)

var (
	// paymentsInflightIndexBucket is the name of the top-level bucket
	// within the database that stores an index of payment hashes that are
	// still inflight.
	//
	// This is used to speedup startup by having a faster way to determine
	// payments that are still inflight.
	paymentsInflightIndexBucket = []byte("payments-inflight-index-bucket")

	// errPaymentsInflightIndexNotExists is used to signal that the
	// inflight payments index is disabled.
	errPaymentsInflightIndexNotExists = errors.New("payments inflight index does not exist")
)

// paymentInflightIndexValueVersion is set as the value used in the inflight
// payments index.
const paymentInflightIndexValueVersion = 0

// createPaymentInflightIndexEntry marks a payment with the given sequence
// number as inflight.
func createPaymentInflightIndexEntry(tx walletdb.ReadWriteTx, seqNum []byte) error {
	indexBucket := tx.ReadWriteBucket(paymentsInflightIndexBucket)

	// If the bucket doesn't exist, indexing of inflight payments is disabled.
	if indexBucket == nil {
		return nil
	}

	v := []byte{paymentInflightIndexValueVersion}
	err := indexBucket.Put(seqNum, v)
	if err != nil {
		return fmt.Errorf("unable to mark inflight: %v", err)
	}
	return err
}

// deletePaymentInflightIndexEntry marks a payment with the given sequence
// number as not inflight.
func deletePaymentInflightIndexEntry(tx walletdb.ReadWriteTx, seqNum []byte) error {
	indexBucket := tx.ReadWriteBucket(paymentsInflightIndexBucket)

	// If the bucket doesn't exist, the entry can't be removed.
	if indexBucket == nil {
		return nil
	}

	return indexBucket.Delete(seqNum)
}

// updatePaymentInflightIndexEntry updates the entry of the given payment
// in the inflight payments index based on the payment's status.
func updatePaymentInflightIndexEntry(tx walletdb.ReadWriteTx, payment *MPPayment) error {
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], payment.SequenceNum)

	if payment.Status == StatusInFlight {
		return createPaymentInflightIndexEntry(tx, seqBytes[:])
	}

	return deletePaymentInflightIndexEntry(tx, seqBytes[:])
}

// recreatePaymentsInflightIndex recreates the index of inflight payments in
// the DB.
func recreatePaymentsInflightIndex(tx walletdb.ReadWriteTx) error {
	indexBucket := tx.ReadWriteBucket(paymentsInflightIndexBucket)
	if indexBucket == nil {
		var err error
		_, err = tx.CreateTopLevelBucket(paymentsInflightIndexBucket)
		if err != nil {
			return err
		}
	}

	payments := tx.ReadBucket(paymentsRootBucket)
	if payments == nil {
		return nil
	}

	var nbPayments, nbInflight uint64
	err := payments.ForEach(func(k, _ []byte) error {
		bucket := payments.NestedReadBucket(k)
		if bucket == nil {
			return fmt.Errorf("non bucket element")
		}

		p, err := fetchPayment(bucket)
		if err != nil {
			return err
		}

		nbPayments += 1
		if p.Status == StatusInFlight {
			nbInflight += 1
		}

		return updatePaymentInflightIndexEntry(tx, p)
	})
	if err != nil {
		return err
	}

	log.Infof("Recreated in-flight payments index: %d payments, %d in-flight",
		nbPayments, nbInflight)

	return nil
}

// fetchInflightPaymentsByIndex fetches the list of inflight payments using the
// inflight payments index. If the index is disabled, errPaymentsInflightIndexNotExists
// is returned.
func fetchInflightPaymentsByIndex(tx walletdb.ReadTx) ([]*MPPayment, error) {
	indexBucket := tx.ReadBucket(paymentsInflightIndexBucket)

	// If the bucket doesn't exist, notify caller.
	if indexBucket == nil {
		return nil, errPaymentsInflightIndexNotExists
	}

	payments := tx.ReadBucket(paymentsRootBucket)
	if payments == nil {
		return nil, fmt.Errorf("payments root bucket does not exist")
	}

	seqIndexes := tx.ReadBucket(paymentsIndexBucket)
	if seqIndexes == nil {
		return nil, fmt.Errorf("index bucket does not exist")
	}

	var inFlights []*MPPayment
	err := indexBucket.ForEach(func(k, v []byte) error {
		hash := seqIndexes.Get(k)
		if hash == nil {
			return fmt.Errorf("seqnum %s does not exist in index db",
				hex.EncodeToString(k))
		}
		r := bytes.NewReader(hash)
		paymentHash, err := deserializePaymentIndex(r)
		if err != nil {
			return err
		}

		bucket := payments.NestedReadBucket(paymentHash[:])
		p, err := fetchPayment(bucket)
		if err != nil {
			return err
		}

		// Double check our assumption that this payment is in fact
		// in-flight. If it is not, we warn about an inconsistency
		// but otherwise don't completely fail to load the inflight
		// payments to avoid breaking the ability to open the app.
		if p.Status != StatusInFlight {
			log.Warnf("Broken assumption: Payment hash %s (seqnum %d) "+
				"in inflight payments index is not actually "+
				"inflight (status %s - %d)",
				paymentHash, p.SequenceNum, p.Status, p.Status)
			return nil
		}

		inFlights = append(inFlights, p)
		return nil
	})
	return inFlights, err
}
