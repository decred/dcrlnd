package blob

import (
	"encoding/hex"

	"github.com/decred/dcrd/chaincfg/chainhash"
)

// BreachHintSize is the length of the identifier used to detect remote
// commitment broadcasts.
const BreachHintSize = 16

// BreachHint is the first 16-bytes of chainhash(txid), which is used to
// identify the breach transaction.
type BreachHint [BreachHintSize]byte

// NewBreachHintFromHash creates a breach hint from a transaction ID.
func NewBreachHintFromHash(hash *chainhash.Hash) BreachHint {
	h := chainhash.HashB(hash[:])
	var hint BreachHint
	copy(hint[:], h)
	return hint
}

// String returns a hex encoding of the breach hint.
func (h BreachHint) String() string {
	return hex.EncodeToString(h[:])
}

// BreachKey is computed as SHA256(txid || txid), which produces the key for
// decrypting a client's encrypted blobs.
type BreachKey [KeySize]byte

// NewBreachKeyFromHash creates a breach key from a transaction ID.
func NewBreachKeyFromHash(hash *chainhash.Hash) BreachKey {
	var h [64]byte
	copy(h[:], hash[:])
	copy(h[32:], hash[:])

	var key BreachKey
	copy(key[:], chainhash.HashB(h[:]))
	return key
}

// String returns a hex encoding of the breach key.
func (k BreachKey) String() string {
	return hex.EncodeToString(k[:])
}

// NewBreachHintAndKeyFromHash derives a BreachHint and BreachKey from a given
// txid in a single pass. The hint and key are computed as:
//
//	hint = chainhash(txid)
//	key = chainhash(txid || txid)
func NewBreachHintAndKeyFromHash(hash *chainhash.Hash) (BreachHint, BreachKey) {
	// The chainhash pkg does not currently export a New()/Sum() variant, so
	// use the default format for calculating it.
	return NewBreachHintFromHash(hash), NewBreachKeyFromHash(hash)
}
