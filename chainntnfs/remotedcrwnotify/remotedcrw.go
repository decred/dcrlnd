package remotedcrwnotify

import (
	"context"

	"decred.org/dcrwallet/v4/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainntnfs"
	csnotify "github.com/decred/dcrlnd/chainntnfs/chainscannotify"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"google.golang.org/grpc"
)

type RemoteWalletChainSource struct {
	*csdrivers.RemoteWalletCSDriver
	wsvc walletrpc.WalletServiceClient
}

func (s *RemoteWalletChainSource) GetBlockHash(ctx context.Context, height int32) (*chainhash.Hash, error) {
	req := &walletrpc.BlockInfoRequest{
		BlockHeight: height,
	}
	resp, err := s.wsvc.BlockInfo(ctx, req)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(resp.BlockHash)
}

func (s *RemoteWalletChainSource) GetBlockHeader(ctx context.Context, hash *chainhash.Hash) (*wire.BlockHeader, error) {
	req := &walletrpc.BlockInfoRequest{
		BlockHash: hash[:],
	}
	resp, err := s.wsvc.BlockInfo(ctx, req)
	if err != nil {
		return nil, err
	}

	var header wire.BlockHeader
	err = header.FromBytes(resp.BlockHeader)
	if err != nil {
		return nil, err
	}

	return &header, err
}

func (s *RemoteWalletChainSource) StoresReorgedHeaders() bool {
	return true
}

func NewRemoteWalletChainSource(conn *grpc.ClientConn, bCache *blockcache.BlockCache) *RemoteWalletChainSource {
	wsvc := walletrpc.NewWalletServiceClient(conn)
	nsvc := walletrpc.NewNetworkServiceClient(conn)
	cs := csdrivers.NewRemoteWalletCSDriver(wsvc, nsvc, bCache)
	return &RemoteWalletChainSource{
		wsvc:                 wsvc,
		RemoteWalletCSDriver: cs,
	}
}

var _ csnotify.ChainSource = (*RemoteWalletChainSource)(nil)

// New returns a new DcrdNotifier instance. This function assumes the dcrd node
// detailed in the passed configuration is already running, and willing to
// accept new websockets clients.
func New(conn *grpc.ClientConn, chainParams *chaincfg.Params,
	spendHintCache chainntnfs.SpendHintCache,
	confirmHintCache chainntnfs.ConfirmHintCache,
	blockCache *blockcache.BlockCache) (*csnotify.ChainscanNotifier, error) {

	src := NewRemoteWalletChainSource(conn, blockCache)
	return csnotify.New(src, chainParams, spendHintCache, confirmHintCache)
}
