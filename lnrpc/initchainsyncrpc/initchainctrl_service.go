package initchainsyncrpc

import (
	"fmt"
	"time"

	"github.com/decred/dcrlnd/lnwallet"
)

// Server implements the initial chain sync gRPC service server.
type Server struct {
	chainCtrl        lnwallet.WalletController
	chainCtrlSetChan chan struct{}
}

// A compile time check to ensure that Server fully implements the
// InitialChainSync gRPC service.
var _ InitialChainSyncServer = (*Server)(nil)

// NewInitCHainSyncService initializes a new InitChainSyncService.
func New() *Server {
	return &Server{
		chainCtrlSetChan: make(chan struct{}),
	}
}

// SetChainControl sets the internal chain/wallet controller. MUST only be
// called once.
func (s *Server) SetChainControl(cc lnwallet.WalletController) {
	s.chainCtrl = cc
	close(s.chainCtrlSetChan)
}

func (s *Server) SubscribeChainSync(sub *ChainSyncSubscription, svr InitialChainSync_SubscribeChainSyncServer) error {
	// Wait until the chain control is set.
	ccSet := false
	for !ccSet {
		select {
		case <-s.chainCtrlSetChan:
			ccSet = true
		case <-svr.Context().Done():
			return svr.Context().Err()
		}
	}

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
			Synced:         synced,
		}
		if err := svr.Send(updt); err != nil {
			return err
		}

		delay = 5 * time.Second
	}
	return nil
}
