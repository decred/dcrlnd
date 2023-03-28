package remotedcrwallet

import (
	"errors"
	"io"

	pb "decred.org/dcrwallet/v2/rpc/walletrpc"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrlnd/lnwallet"
)

var _ lnwallet.ExtendedWalletController = (*DcrWallet)(nil)

func (b *DcrWallet) DeriveNextAccount(name string) error {
	return errors.New("unimplemented")
}

func (b *DcrWallet) ExportPrivKey(addr stdaddr.Address) (*secp256k1.PrivateKey, error) {
	return nil, errors.New("unimplemented")
}

func (b *DcrWallet) RescanWallet(startHeight int32, progress func(height int32) error) error {
	cli, err := b.wallet.Rescan(b.ctx, &pb.RescanRequest{BeginHeight: startHeight})
	if err != nil {
		return err
	}

	for {
		progr, err := cli.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if progress != nil {
			err := progress(progr.RescannedThrough)
			if err != nil {
				return err

			}
		}
	}
}
