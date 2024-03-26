package discovery

import (
	"errors"
	"time"

	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/routing/route"
)

// GossiperState is an interface that defines the functions necessary to persist
// gossip data about a peer.
type GossiperState interface {
	// UpdatePeerLastGossipMsgTS stores the specified timestamp as the
	// timestamp of the most recent gossip message received from the
	// specified peer. If this timestamp is older than a previously stored
	// timestamp, this function returns false and a nil error.
	UpdatePeerLastGossipMsgTS(peer route.Vertex, ts time.Time) (bool, error)

	// ReadPeerLastGossipMsgTS returns the timestamp stored as most recent
	// for a gossip message received from the specified peer.
	ReadPeerLastGossipMsgTS(peer route.Vertex) (time.Time, error)
}

// gossiperStateDB fulfills the GossiperState interface by using a backing
// channeldb.DB instance.
type gossiperStateDB struct {
	db *channeldb.DB
}

// NewGossiperState initializes a new GossiperState using the specified channel
// database for persistent storage.
func NewGossiperState(db *channeldb.DB) GossiperState {
	return &gossiperStateDB{db: db}
}

// UpdatePeerLastGossipMsgTS stores the specified timestamp as the
// timestamp of the most recent gossip message received from the
// specified peer. If this timestamp is older than a previously stored
// timestamp, this function returns an error.
func (gs *gossiperStateDB) UpdatePeerLastGossipMsgTS(peer route.Vertex, ts time.Time) (bool, error) {
	err := gs.db.UpdatePeerLastGossipMsgTS(peer, ts)
	if errors.Is(err, channeldb.ErrOutdatedLastGossipMsgTS) {
		// We mute this error, because it could happen due to the
		// ordering of processing the messages and it doesn't affect
		// future queries.
		log.Tracef("GossipSyncer(%s): Outdated ts %s",
			peer, ts)
		return false, nil
	}
	return err == nil, err
}

// ReadPeerLastGossipMsgTS returns the timestamp stored as most recent
// for a gossip message received from the specified peer.
func (gs *gossiperStateDB) ReadPeerLastGossipMsgTS(peer route.Vertex) (time.Time, error) {
	return gs.db.ReadPeerLastGossipMsgTS(peer)
}

// updateGossiperMsgTS is called after processing a remote gossip message to
// persist the state of the gossip syncer.
func (d *AuthenticatedGossiper) updateGossiperMsgTS(peer route.Vertex, ts time.Time) {
	syncer, ok := d.syncMgr.GossipSyncer(peer)
	if !ok {
		log.Warnf("Gossip syncer for peer=%s not found",
			peer)
		return
	}

	// Ignore updates when the timestamp is in the future, to avoid missing
	// future updates when someone announced with an incorrect timestamp.
	if ts.After(time.Now()) {
		log.Tracef("GossipSyncer(%s): ignoring update to future ts %s",
			peer, ts)
		return
	}

	// Only update the stored last sync time if we're in active sync mode.
	// Storing at other times could cause us to store a timestamp received
	// from a historical search.
	if syncer.syncState() != chansSynced || syncer.SyncType() != ActiveSync {
		log.Tracef("GossipSyncer(%s): ignoring update to ts %s",
			peer, ts)
		return
	}

	updated, err := d.cfg.GossiperState.UpdatePeerLastGossipMsgTS(peer, ts)
	if err != nil {
		log.Warnf("GossipSyncer(%s): Unable to update last gossip msg ts: %v",
			peer, err)
		return
	}

	if updated {
		log.Debugf("GossipSyncer(%s): Updated last gossip msg ts to %s",
			peer, ts)
	}
}

// initialGossipTimestamp returns the initial timestamp to use for the next
// gossip timestamp range message. This is either the timestamp for the last
// message we received from this syncer or the current time (if we never synced
// to this peer before).
func (g *GossipSyncer) initialGossipTimestamp() time.Time {
	ts, err := g.cfg.gossiperState.ReadPeerLastGossipMsgTS(g.cfg.peerPub)
	if err != nil {
		return time.Now()
	}

	// When we wrongly stored the timestamp as a date in the future, use
	// a date in the past as initial gossip timestamp. This prevents some
	// classes of bugs where we never receive new gossip messages because
	// we stored a timestamp in the future.
	now := time.Now()
	if ts.After(now) {
		ts = now.Add(-time.Hour * 24)
	}

	return ts
}
