package automation

import (
	"context"
	"time"

	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lncfg"
	"github.com/decred/dcrlnd/lnrpc"
)

// Config are the config parameters for the automation server.
type Config struct {
	*lncfg.Automation

	// CloseChannel should be set to the rpc server function that allows
	// closing a channel.
	CloseChannel func(in *lnrpc.CloseChannelRequest,
		updateStream lnrpc.Lightning_CloseChannelServer) error

	DB *channeldb.DB
}

// Server is set of automation services for dcrlnd nodes.
type Server struct {
	cfg       *Config
	ctx       context.Context
	cancelCtx func()
}

// NewServer creates a new automation server.
func NewServer(cfg *Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:       cfg,
		ctx:       ctx,
		cancelCtx: cancel,
	}
	return s
}

// runForceCloseStaleChanReestablish autocloses channels where a remote peer
// has been online without sending ChannelReestablish messages.
func (s *Server) runForceCloseStaleChanReestablish() {
	// Use a default ticker for 1 hour, but reduce if the threshold is lower
	// than that (useful for tests).
	forceCloseInterval := time.Duration(s.cfg.ForceCloseChanReestablishWait) * time.Second
	tickerInterval := time.Hour
	if forceCloseInterval < tickerInterval {
		tickerInterval = forceCloseInterval
	}
	log.Debugf("Performing force close check for stale channels based on "+
		"ChannelReestablish every %s", tickerInterval)

	ticker := time.NewTicker(tickerInterval)
	for {
		select {
		case <-s.ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
		}

		log.Debugf("Time to check channels for force close due to stale " +
			"chan reestablish messages")

		chans, err := s.cfg.DB.FetchAllOpenChannels()
		if err != nil {
			log.Errorf("Unable to list open channels: %v", err)
			continue
		}

		for _, c := range chans {
			sid := c.ShortChannelID
			waitTime, err := s.cfg.DB.GetChanReestablishWaitTime(sid)
			if err != nil {
				log.Errorf("Unable to get chan reestablish msg "+
					"times for %s: %v", sid, err)
				continue
			}

			if waitTime < forceCloseInterval {
				log.Tracef("Skipping autoclose of %s due to low "+
					"wait time %s", sid, waitTime)
				continue
			}

			// Start force close.
			chanPoint := c.FundingOutpoint
			log.Infof("Starting force-close attempt of channel %s (%s) "+
				"due to channel reestablish msg wait time %s greater "+
				"than max interval %s", chanPoint,
				sid, waitTime, forceCloseInterval)
			go func() {
				req := &lnrpc.CloseChannelRequest{
					ChannelPoint: lnrpc.OutpointToChanPoint(&chanPoint),
					Force:        true,
				}
				err = s.cfg.CloseChannel(req, nil)
				if err != nil {
					log.Errorf("Unable to force-close channel %s: %v",
						sid, err)
				}
			}()
		}
	}
}

func (s *Server) Start() error {
	if s.cfg.ForceCloseChanReestablishWait > 0 {
		go s.runForceCloseStaleChanReestablish()
	}
	return nil
}

func (s *Server) Stop() error {
	s.cancelCtx()
	return nil
}
