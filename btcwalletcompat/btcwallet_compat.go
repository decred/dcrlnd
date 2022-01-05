package btcwalletcompat

// CoinSelectionStrategy is a port of btcwallet's CoinSelectionStrategy type. It
// is not currently supported in dcrwallet.
type CoinSelectionStrategy int

const (
	// CoinSelectionLargest always picks the largest available utxo to add
	// to the transaction next.
	CoinSelectionLargest CoinSelectionStrategy = iota

	// CoinSelectionRandom randomly selects the next utxo to add to the
	// transaction. This strategy prevents the creation of ever smaller
	// utxos over time.
	CoinSelectionRandom
)

// ManagedPubKeyAddress is a shim for btcwallet's waddrmgr.ManagedPubKeyAddress.
type ManagedPubKeyAddress interface{}
