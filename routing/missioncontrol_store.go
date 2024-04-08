package routing

import (
	"bytes"
	"container/list"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/lnwire"
)

var (
	// resultsKey is the fixed key under which the attempt results are
	// stored.
	resultsKey = []byte("missioncontrol-results")

	// Big endian is the preferred byte order, due to cursor scans over
	// integer keys iterating in order.
	byteOrder = binary.BigEndian
)

const (
	// unknownFailureSourceIdx is the database encoding of an unknown error
	// source.
	unknownFailureSourceIdx = -1
)

// missionControlStore is a bbolt db based implementation of a mission control
// store. It stores the raw payment attempt data from which the internal mission
// controls state can be rederived on startup. This allows the mission control
// internal data structure to be changed without requiring a database migration.
// Also changes to mission control parameters can be applied to historical data.
// Finally, it enables importing raw data from an external source.
type missionControlStore struct {
	done    chan struct{}
	wg      sync.WaitGroup
	db      kvdb.Backend
	queueMx sync.Mutex

	// queue stores all pending payment results not yet added to the store.
	queue *list.List

	// queueChan is signalled when the first item is put into queue after
	// a storeResult().
	queueChan chan struct{}

	// keys holds the stored MC store item keys in the order of storage.
	// We use this list when adding/deleting items from the database to
	// avoid cursor use which may be slow in the remote DB case.
	keys *list.List

	// keysMap holds the stored MC store item keys. We use this map to check
	// if a new payment result has already been stored.
	keysMap map[string]struct{}

	// maxRecords is the maximum amount of records we will store in the db.
	maxRecords int

	// flushInterval is the configured interval we use to store new results
	// and delete outdated ones from the db.
	flushInterval time.Duration
}

func newMissionControlStore(db kvdb.Backend, maxRecords int,
	flushInterval time.Duration) (*missionControlStore, error) {

	var (
		keys    *list.List
		keysMap map[string]struct{}
	)

	// Create buckets if not yet existing.
	err := kvdb.Update(db, func(tx kvdb.RwTx) error {
		resultsBucket, err := tx.CreateTopLevelBucket(resultsKey)
		if err != nil {
			return fmt.Errorf("cannot create results bucket: %v",
				err)
		}

		// Collect all keys to be able to quickly calculate the
		// difference when updating the DB state.
		c := resultsBucket.ReadCursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			keys.PushBack(string(k))
			keysMap[string(k)] = struct{}{}
		}

		return nil
	}, func() {
		keys = list.New()
		keysMap = make(map[string]struct{})
	})
	if err != nil {
		return nil, err
	}

	log.Infof("Loaded %d mission control entries", len(keysMap))

	return &missionControlStore{
		done:          make(chan struct{}),
		db:            db,
		queue:         list.New(),
		queueChan:     make(chan struct{}, 1),
		keys:          keys,
		keysMap:       keysMap,
		maxRecords:    maxRecords,
		flushInterval: flushInterval,
	}, nil
}

// clear removes all results from the db.
func (b *missionControlStore) clear() error {
	b.queueMx.Lock()
	defer b.queueMx.Unlock()

	err := kvdb.Update(b.db, func(tx kvdb.RwTx) error {
		if err := tx.DeleteTopLevelBucket(resultsKey); err != nil {
			return err
		}

		_, err := tx.CreateTopLevelBucket(resultsKey)
		return err
	}, func() {})

	if err != nil {
		return err
	}

	b.queue = list.New()
	return nil
}

// fetchAll returns all results currently stored in the database.
func (b *missionControlStore) fetchAll() ([]*paymentResult, error) {
	var results []*paymentResult

	err := kvdb.View(b.db, func(tx kvdb.RTx) error {
		resultBucket := tx.ReadBucket(resultsKey)
		results = make([]*paymentResult, 0)

		return resultBucket.ForEach(func(k, v []byte) error {
			result, err := deserializeResult(k, v)
			if err != nil {
				return err
			}

			results = append(results, result)

			return nil
		})

	}, func() {
		results = nil
	})
	if err != nil {
		return nil, err
	}

	return results, nil
}

// serializeResult serializes a payment result and returns a key and value byte
// slice to insert into the bucket.
func serializeResult(rp *paymentResult) ([]byte, []byte, error) {
	// Write timestamps, success status, failure source index and route.
	var b bytes.Buffer

	var dbFailureSourceIdx int32
	if rp.failureSourceIdx == nil {
		dbFailureSourceIdx = unknownFailureSourceIdx
	} else {
		dbFailureSourceIdx = int32(*rp.failureSourceIdx)
	}

	err := channeldb.WriteElements(
		&b,
		uint64(rp.timeFwd.UnixNano()),
		uint64(rp.timeReply.UnixNano()),
		rp.success, dbFailureSourceIdx,
	)
	if err != nil {
		return nil, nil, err
	}

	if err := channeldb.SerializeRoute(&b, *rp.route); err != nil {
		return nil, nil, err
	}

	// Write failure. If there is no failure message, write an empty
	// byte slice.
	var failureBytes bytes.Buffer
	if rp.failure != nil {
		err := lnwire.EncodeFailureMessage(&failureBytes, rp.failure, 0)
		if err != nil {
			return nil, nil, err
		}
	}
	err = wire.WriteVarBytes(&b, 0, failureBytes.Bytes())
	if err != nil {
		return nil, nil, err
	}

	// Compose key that identifies this result.
	key := getResultKey(rp)

	return key, b.Bytes(), nil
}

// deserializeResult deserializes a payment result.
func deserializeResult(k, v []byte) (*paymentResult, error) {
	// Parse payment id.
	result := paymentResult{
		id: byteOrder.Uint64(k[8:]),
	}

	r := bytes.NewReader(v)

	// Read timestamps, success status and failure source index.
	var (
		timeFwd, timeReply uint64
		dbFailureSourceIdx int32
	)

	err := channeldb.ReadElements(
		r, &timeFwd, &timeReply, &result.success, &dbFailureSourceIdx,
	)
	if err != nil {
		return nil, err
	}

	// Convert time stamps to local time zone for consistent logging.
	result.timeFwd = time.Unix(0, int64(timeFwd)).Local()
	result.timeReply = time.Unix(0, int64(timeReply)).Local()

	// Convert from unknown index magic number to nil value.
	if dbFailureSourceIdx != unknownFailureSourceIdx {
		failureSourceIdx := int(dbFailureSourceIdx)
		result.failureSourceIdx = &failureSourceIdx
	}

	// Read route.
	route, err := channeldb.DeserializeRoute(r)
	if err != nil {
		return nil, err
	}
	result.route = &route

	// Read failure.
	failureBytes, err := wire.ReadVarBytes(
		r, 0, lnwire.FailureMessageLength, "failure",
	)
	if err != nil {
		return nil, err
	}
	if len(failureBytes) > 0 {
		result.failure, err = lnwire.DecodeFailureMessage(
			bytes.NewReader(failureBytes), 0,
		)
		if err != nil {
			return nil, err
		}
	}

	return &result, nil
}

// AddResult adds a new result to the db.
func (b *missionControlStore) AddResult(rp *paymentResult) {
	b.queueMx.Lock()
	b.queue.PushBack(rp)
	signalRun := b.queue.Len() == 1
	b.queueMx.Unlock()

	// Signal run() that the queue is non-empty.
	if signalRun {
		select {
		case <-b.done:
		case b.queueChan <- struct{}{}:
		}
	}
}

// stop stops the store ticker goroutine.
func (b *missionControlStore) stop() {
	close(b.done)
	b.wg.Wait()
}

// run runs the MC store ticker goroutine.
func (b *missionControlStore) run() {
	b.wg.Add(1)

	go func() {
		// TimerChan is non-null when there are items to be stored.
		var timerChan <-chan time.Time
		defer b.wg.Done()

		for {
			select {
			case <-b.queueChan:
				timerChan = time.After(b.flushInterval)

			case <-timerChan:
				timerChan = nil
				if err := b.storeResults(); err != nil {
					log.Errorf("Failed to update mission "+
						"control store: %v", err)
				}

			case <-b.done:
				return
			}
		}
	}()
}

// storeResults stores all accumulated results.
func (b *missionControlStore) storeResults() error {
	b.queueMx.Lock()
	l := b.queue

	// Only recreate queue if its not empty because if it is empty we will
	// return early from the func without running any updates.
	isEmpty := l.Len() == 0
	if !isEmpty {
		b.queue = list.New()
	}
	b.queueMx.Unlock()

	// Return early when no new items will be updated.
	if isEmpty {
		return nil
	}

	// Create a deduped list of new entries.
	newKeys := make(map[string]*paymentResult, l.Len())
	for e := l.Front(); e != nil; {
		pr := e.Value.(*paymentResult)
		key := string(getResultKey(pr))
		if _, ok := b.keysMap[key]; ok {
			e, _ = e.Next(), l.Remove(e)
			continue
		}
		if _, ok := newKeys[key]; ok {
			e, _ = e.Next(), l.Remove(e)
			continue
		}
		newKeys[key] = pr
		e = e.Next()
	}

	// Create a list of entries to delete.
	toDelete := b.keys.Len() + len(newKeys) - b.maxRecords
	var delKeys []string
	if b.maxRecords > 0 && toDelete > 0 {
		delKeys = make([]string, 0, toDelete)

		// Delete as many as needed from old keys.
		for e := b.keys.Front(); len(delKeys) < toDelete && e != nil; {
			delKeys = append(delKeys, e.Value.(string))
			e = e.Next()
		}

		// If more deletions are needed, simply do not add from the
		// list of new keys.
		for e := l.Front(); len(delKeys) < toDelete && e != nil; {
			toDelete--
			pr := e.Value.(*paymentResult)
			key := string(getResultKey(pr))
			delete(newKeys, key)
			l.Remove(e)
			e = l.Front()
		}
	}

	var storeCount, pruneCount int

	err := kvdb.Update(b.db, func(tx kvdb.RwTx) error {
		bucket := tx.ReadWriteBucket(resultsKey)

		for e := l.Front(); e != nil; e = e.Next() {
			pr := e.Value.(*paymentResult)
			// Serialize result into key and value byte slices.
			k, v, err := serializeResult(pr)
			if err != nil {
				return err
			}

			// Put into results bucket.
			if err := bucket.Put(k, v); err != nil {
				return err
			}

			storeCount++
		}

		// Prune oldest entries.
		for _, key := range delKeys {
			if err := bucket.Delete([]byte(key)); err != nil {
				return err
			}
			pruneCount++
		}

		return nil
	}, func() {
		storeCount, pruneCount = 0, 0
	})

	if err != nil {
		return err
	}

	log.Debugf("Stored mission control results: %d added, %d deleted",
		storeCount, pruneCount)

	// DB Update was successful, update the in-memory cache.
	for _, key := range delKeys {
		delete(b.keysMap, key)
		b.keys.Remove(b.keys.Front())
	}
	for e := l.Front(); e != nil; e = e.Next() {
		pr := e.Value.(*paymentResult)
		key := string(getResultKey(pr))
		b.keys.PushBack(key)
	}

	return nil
}

// getResultKey returns a byte slice representing a unique key for this payment
// result.
func getResultKey(rp *paymentResult) []byte {
	var keyBytes [8 + 8 + 33]byte

	// Identify records by a combination of time, payment id and sender pub
	// key. This allows importing mission control data from an external
	// source without key collisions and keeps the records sorted
	// chronologically.
	byteOrder.PutUint64(keyBytes[:], uint64(rp.timeReply.UnixNano()))
	byteOrder.PutUint64(keyBytes[8:], rp.id)
	copy(keyBytes[16:], rp.route.SourcePubKey[:])

	return keyBytes[:]
}
