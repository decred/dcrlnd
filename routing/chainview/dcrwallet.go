package chainview

import (
	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrlnd/chainntnfs/dcrwnotify"
	"github.com/decred/dcrlnd/chainntnfs/remotedcrwnotify"
	"google.golang.org/grpc"
)

func NewDcrwalletFilteredChainView(w *wallet.Wallet) (FilteredChainView, error) {
	src := dcrwnotify.NewDcrwChainSource(w)
	return newChainscanFilteredChainView(src)
}

func NewRemoteWalletFilteredChainView(conn *grpc.ClientConn) (FilteredChainView, error) {
	src := remotedcrwnotify.NewRemoteWalletChainSource(conn)
	return newChainscanFilteredChainView(src)
}
