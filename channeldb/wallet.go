package channeldb

import (
	"github.com/decred/dcrlnd/kvdb"
)

const accountDiscoveryDisabled byte = 1

var (
	walletBucket              = []byte("wallet")
	disableDiscoverAcctBucket = []byte("discoverAccounts")
)

func (d *DB) AccountDiscoveryDisabled() (bool, error) {
	var res bool
	err := kvdb.Update(d, func(tx kvdb.RwTx) error {
		wallet, err := tx.CreateTopLevelBucket(walletBucket)
		if err != nil {
			return err
		}

		disableDiscoverAcct := wallet.Get(disableDiscoverAcctBucket)
		if len(disableDiscoverAcct) == 0 {
			return nil
		}

		res = disableDiscoverAcct[0] == accountDiscoveryDisabled
		return nil
	}, func() { res = false })
	return res, err
}

func (d *DB) DisableAccountDiscovery() error {
	return kvdb.Update(d, func(tx kvdb.RwTx) error {
		wallet, err := tx.CreateTopLevelBucket(walletBucket)
		if err != nil {
			return err
		}

		v := []byte{accountDiscoveryDisabled}
		wallet.Put(disableDiscoverAcctBucket, v)
		return nil
	}, func() {})
}
