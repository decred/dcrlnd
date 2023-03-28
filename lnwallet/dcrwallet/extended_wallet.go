package dcrwallet

import (
	"context"

	base "decred.org/dcrwallet/v2/wallet"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrlnd/lnwallet"
)

var _ lnwallet.ExtendedWalletController = (*DcrWallet)(nil)

func (b *DcrWallet) DeriveNextAccount(name string) error {
	nb, err := b.wallet.NextAccount(context.Background(), name)
	if err == nil {
		dcrwLog.Infof("Created account %q (%d)", name, nb)
	}
	return err
}

func (b *DcrWallet) ExportPrivKey(addr stdaddr.Address) (*secp256k1.PrivateKey, error) {
	str, err := b.wallet.DumpWIFPrivateKey(context.Background(), addr)
	if err != nil {
		return nil, err
	}
	wif, err := dcrutil.DecodeWIF(str, b.cfg.NetParams.PrivateKeyID)
	pk := secp256k1.PrivKeyFromBytes(wif.PrivKey())
	return pk, err
}

func (b *DcrWallet) RescanWallet(startHeight int32, progress func(height int32) error) error {
	nb, err := b.wallet.NetworkBackend()
	if err != nil {
		return err
	}

	// Stop rescan if we exit early.
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	done := make(chan struct{})

	// Start rescan.
	p := make(chan base.RescanProgress)
	go func() {
		b.wallet.RescanProgressFromHeight(ctx, nb, startHeight, p)
		close(done)
	}()

	// Progress through rescan.
	for {
		select {
		case progr := <-p:
			if progr.Err == nil && progress != nil {
				err := progress(progr.ScannedThrough)
				if err != nil {
					return err
				}
			}
			if progr.Err != nil {
				return progr.Err
			}

		case <-done:
			return nil
		}
	}
}
