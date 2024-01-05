package lnwallet

import (
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
)

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

// DeriveAddrsFromExtPub derives a number of internal and external addresses from
// an xpub key.
func DeriveAddrsFromExtPub(xpub *hdkeychain.ExtendedKey, addrParams stdaddr.AddressParamsV0, count int) ([]stdaddr.Address, []stdaddr.Address, error) {

	const (
		externalBranch uint32 = 0
		internalBranch uint32 = 1
	)

	extKey, err := xpub.Child(externalBranch)
	if err != nil {
		return nil, nil, err
	}
	intKey, err := xpub.Child(internalBranch)
	if err != nil {
		return nil, nil, err
	}

	var intIdx, extIdx uint32
	var intAddrs, extAddrs []stdaddr.Address
	for i := 0; i < count; i++ {
		// Derive internal address. Loop because sometimes the address
		// is invalid.
		var addr stdaddr.Address
		child, err := intKey.Child(intIdx)
		for ; err != nil; intIdx += 1 {
			child, err = intKey.Child(intIdx)
			if err != nil {
				continue
			}
			addr, err = stdaddr.NewAddressPubKeyEcdsaSecp256k1V0Raw(child.SerializedPubKey(), addrParams)
			if err != nil {
				continue
			}
		}
		intAddrs = append(intAddrs, addr)

		// Derive external address. Loop because sometimes the address
		// is invalid.
		child, err = extKey.Child(extIdx)
		for ; err != nil; extIdx += 1 {
			child, err = extKey.Child(extIdx)
			if err != nil {
				continue
			}
			addr, err = stdaddr.NewAddressPubKeyEcdsaSecp256k1V0Raw(child.SerializedPubKey(), addrParams)
			if err != nil {
				continue
			}
		}
		extAddrs = append(extAddrs, addr)
	}

	return intAddrs, extAddrs, nil
}

// cacheCommitmentTxHash fills the cachedTxHash of a *commitment for logging.
func cacheCommitmentTxHash(commit *commitment) *commitment {
	if commit != nil && commit.txn != nil {
		commit.txn.CachedTxHash()
	}
	return commit
}
