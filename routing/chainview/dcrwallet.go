package chainview

import (
	"decred.org/dcrwallet/v4/wallet"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainntnfs/dcrwnotify"
	"github.com/decred/dcrlnd/chainntnfs/remotedcrwnotify"
	"google.golang.org/grpc"
)

func NewDcrwalletFilteredChainView(w *wallet.Wallet, blockCache *blockcache.BlockCache) (FilteredChainView, error) {
	src := dcrwnotify.NewDcrwChainSource(w, blockCache)
	return newChainscanFilteredChainView(src)
}

func NewRemoteWalletFilteredChainView(conn *grpc.ClientConn, blockCache *blockcache.BlockCache) (FilteredChainView, error) {
	src := remotedcrwnotify.NewRemoteWalletChainSource(conn, blockCache)
	return newChainscanFilteredChainView(src)
}
