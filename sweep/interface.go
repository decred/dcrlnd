package sweep

import (
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/lnwallet"
)

// Wallet contains all wallet related functionality required by sweeper.
type Wallet interface {
	// PublishTransaction performs cursory validation (dust checks, etc)
	// and broadcasts the passed transaction to the Decred network.
	PublishTransaction(tx *wire.MsgTx, label string) error

	// AbandonDoubleSpends removes all unconfirmed transactions that also
	// spend any of the specified outpoints from the wallet. This is used
	// to fix issues when an input used in multiple different sweep
	// transactions gets confirmed in one of them (rendering the other
	// transactions invalid).
	AbandonDoubleSpends(spentOutpoints ...*wire.OutPoint) error

	// ListUnspentWitnessFromDefaultAccount returns all unspent outputs
	// which are version 0 witness programs from the default wallet account.
	// The 'minConfs' and 'maxConfs' parameters indicate the minimum
	// and maximum number of confirmations an output needs in order to be
	// returned by this method.
	ListUnspentWitnessFromDefaultAccount(minConfs, maxConfs int32) (
		[]*lnwallet.Utxo, error)

	// WithCoinSelectLock will execute the passed function closure in a
	// synchronized manner preventing any coin selection operations from
	// proceeding while the closure is executing. This can be seen as the
	// ability to execute a function closure under an exclusive coin
	// selection lock.
	WithCoinSelectLock(f func() error) error

	// RemoveDescendants removes any wallet transactions that spends
	// outputs created by the specified transaction.
	RemoveDescendants(*wire.MsgTx) error

	// FetchTx returns the transaction that corresponds to the transaction
	// hash passed in. If the transaction can't be found then a nil
	// transaction pointer is returned.
	FetchTx(chainhash.Hash) (*wire.MsgTx, error)
}
