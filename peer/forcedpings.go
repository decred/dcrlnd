package peer

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// EnforcePing attempts to send a ping to the peer and wait for the response. If
// the passed context is canceled before the response is received, then the
// peer is forced to disconnect.
func (p *Brontide) EnforcePing(ctx context.Context) (time.Duration, error) {
	c := make(chan time.Duration)
	p.enforcePingMtx.Lock()
	needsPing := len(p.enforcePongChans) == 0
	p.enforcePongChans = append(p.enforcePongChans, c)
	p.enforcePingMtx.Unlock()

	if needsPing {
		go func() {
			select {
			case <-p.quit:
			case p.enforcePingChan <- struct{}{}:
			}
		}()
	}

	select {
	case <-p.quit:
		return 0, fmt.Errorf("peer has quitted")
	case <-ctx.Done():
		// Context closed before a reply was received. Disconnect the
		// peer.
		err := fmt.Errorf("enforced ping context error: %v", ctx.Err())
		p.Disconnect(err)
		return 0, err
	case res := <-c:
		return res, nil
	}
}

// handlePong calls the forced ping handlers with either the error or the
// current ping time.
func (p *Brontide) handlePong() {
	p.enforcePingMtx.Lock()
	pongChans := p.enforcePongChans
	p.enforcePongChans = nil
	p.enforcePingMtx.Unlock()

	microPT := atomic.LoadInt64(&p.pingTime)
	pingTime := time.Duration(microPT) * time.Microsecond

	for _, c := range pongChans {
		c := c
		go func() { c <- pingTime }()
	}
}
