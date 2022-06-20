package initchainsyncrpc

import (
	"fmt"
	"time"

	"github.com/decred/dcrlnd/lnwallet"
)

// Server implements the initial chain sync gRPC service server.
type Server struct {
	chainCtrl lnwallet.WalletController
}

// A compile time check to ensure that Server fully implements the
// InitialChainSync gRPC service.
var _ InitialChainSyncServer = (*Server)(nil)

// NewInitCHainSyncService initializes a new InitChainSyncService.
func New(chainCtrl lnwallet.WalletController) *Server {
	return &Server{chainCtrl}
}

func (s *Server) SubscribeChainSync(sub *ChainSyncSubscription, svr InitialChainSync_SubscribeChainSyncServer) error {
	var synced = false
	delay := time.Millisecond
	for !synced {
		select {
		case <-svr.Context().Done():
			return svr.Context().Err()
		case <-s.chainCtrl.InitialSyncChannel():
			synced = true
		case <-time.After(delay):
		}

		height, hash, ts, err := s.chainCtrl.BestBlock()
		if err != nil {
			return fmt.Errorf("unable to fetch current best block: %v", err)
		}
		updt := &ChainSyncUpdate{
			BlockHeight:    height,
			BlockHash:      hash[:],
			BlockTimestamp: ts,
		}
		if err := svr.Send(updt); err != nil {
			return err
		}

		delay = 5 * time.Second
	}
	return nil
}
