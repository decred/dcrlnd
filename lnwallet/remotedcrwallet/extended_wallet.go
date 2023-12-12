package remotedcrwallet

import (
	"errors"
	"io"

	pb "decred.org/dcrwallet/v4/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrlnd/lnwallet"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func (b *DcrWallet) GetWalletTransaction(txh chainhash.Hash) (*lnwallet.WalletTransaction, error) {
	req := &pb.GetTransactionRequest{
		TransactionHash: txh[:],
	}
	res, err := b.wallet.GetTransaction(b.ctx, req)
	if status.Code(err) == codes.NotFound {
		return nil, lnwallet.ErrWalletTxNotExist
	}
	if err != nil {
		return nil, err
	}
	var bh *chainhash.Hash
	if res.BlockHash != nil {
		bh, err = chainhash.NewHash(res.BlockHash)
		if err != nil {
			return nil, err
		}
	}
	return &lnwallet.WalletTransaction{
		RawTx:         res.Transaction.Transaction,
		Confirmations: res.Confirmations,
		BlockHash:     bh,
	}, nil
}
