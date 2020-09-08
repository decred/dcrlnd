package chanfitness

import (
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/channelnotifier"
	"github.com/decred/dcrlnd/peernotifier"
	"github.com/decred/dcrlnd/routing/route"
	"github.com/decred/dcrlnd/subscribe"
	"github.com/stretchr/testify/require"
)

// timeout is the amount of time we allow our blocking test calls.
var timeout = time.Second

// chanEventStoreTestCtx is a helper struct which can be used to test the
// channel event store.
type chanEventStoreTestCtx struct {
	t *testing.T

	store *ChannelEventStore

	channelSubscription *mockSubscription
	peerSubscription    *mockSubscription

	// testVarIdx is an index which will be used to deterministically add
	// channels and public keys to our test context. We use a single value
	// for a single pubkey + channel combination because its actual value
	// does not matter.
	testVarIdx int
}

// newChanEventStoreTestCtx creates a test context which can be used to test
// the event store.
func newChanEventStoreTestCtx(t *testing.T) *chanEventStoreTestCtx {
	testCtx := &chanEventStoreTestCtx{
		t:                   t,
		channelSubscription: newMockSubscription(t),
		peerSubscription:    newMockSubscription(t),
	}

	cfg := &Config{
		SubscribeChannelEvents: func() (subscribe.Subscription, error) {
			return testCtx.channelSubscription, nil
		},
		SubscribePeerEvents: func() (subscribe.Subscription, error) {
			return testCtx.peerSubscription, nil
		},
		GetOpenChannels: func() ([]*channeldb.OpenChannel, error) {
			return nil, nil
		},
	}

	testCtx.store = NewChannelEventStore(cfg)

	return testCtx
}

// start starts the test context's event store.
func (c *chanEventStoreTestCtx) start() {
	require.NoError(c.t, c.store.Start())
}

// stop stops the channel event store's subscribe servers and the store itself.
func (c *chanEventStoreTestCtx) stop() {
	c.store.Stop()

	// Make sure that the cancel function was called for both of our
	// subscription mocks.
	c.channelSubscription.assertCancelled()
	c.peerSubscription.assertCancelled()
}

// newChannel creates a new, unique test channel. Note that this function
// does not add it to the test event store, it just creates mocked values.
func (c *chanEventStoreTestCtx) newChannel() (route.Vertex, *secp256k1.PublicKey,
	wire.OutPoint) {

	// Create a pubkey for our channel peer.
	privKey, _ := secp256k1.GeneratePrivateKey()
	pubKey := privKey.PubKey()

	// Create vertex from our pubkey.
	vertex, err := route.NewVertexFromBytes(pubKey.SerializeCompressed())
	require.NoError(c.t, err)

	// Create a channel point using our channel index, then increment it.
	chanPoint := wire.OutPoint{
		Hash:  [chainhash.HashSize]byte{1, 2, 3},
		Index: uint32(c.testVarIdx),
	}

	// Increment the index we use so that the next channel and pubkey we
	// create will be unique.
	c.testVarIdx++

	return vertex, pubKey, chanPoint
}

// createChannel creates a new channel, notifies the event store that it has
// been created and returns the peer vertex, pubkey and channel point.
func (c *chanEventStoreTestCtx) createChannel() (route.Vertex, *secp256k1.PublicKey,
	wire.OutPoint) {

	vertex, pubKey, chanPoint := c.newChannel()
	c.sendChannelOpenedUpdate(pubKey, chanPoint)

	return vertex, pubKey, chanPoint
}

// closeChannel sends a close channel event to our subscribe server.
func (c *chanEventStoreTestCtx) closeChannel(channel wire.OutPoint,
	peer *secp256k1.PublicKey) {

	update := channelnotifier.ClosedChannelEvent{
		CloseSummary: &channeldb.ChannelCloseSummary{
			ChanPoint: channel,
			RemotePub: peer,
		},
	}

	c.channelSubscription.sendUpdate(update)
}

// peerEvent sends a peer online or offline event to the store for the peer
// provided.
func (c *chanEventStoreTestCtx) peerEvent(peer route.Vertex, online bool) {
	var update interface{}
	if online {
		update = peernotifier.PeerOnlineEvent{PubKey: peer}
	} else {
		update = peernotifier.PeerOfflineEvent{PubKey: peer}
	}

	c.peerSubscription.sendUpdate(update)
}

// sendChannelOpenedUpdate notifies the test event store that a channel has
// been opened.
func (c *chanEventStoreTestCtx) sendChannelOpenedUpdate(pubkey *secp256k1.PublicKey,
	channel wire.OutPoint) {

	update := channelnotifier.OpenChannelEvent{
		Channel: &channeldb.OpenChannel{
			FundingOutpoint: channel,
			IdentityPub:     pubkey,
		},
	}

	c.channelSubscription.sendUpdate(update)
}

// mockSubscription is a mock subscription client that blocks on sends into the
// updates channel. We use this mock rather than an actual subscribe client
// because they do not block, which makes tests race (because we have no way
// to guarantee that the test client consumes the update before shutdown).
type mockSubscription struct {
	t       *testing.T
	updates chan interface{}

	// Embed the subscription interface in this mock so that we satisfy it.
	subscribe.Subscription
}

// newMockSubscription creates a mock subscription.
func newMockSubscription(t *testing.T) *mockSubscription {
	return &mockSubscription{
		t:       t,
		updates: make(chan interface{}),
	}
}

// sendUpdate sends an update into our updates channel, mocking the dispatch of
// an update from a subscription server. This call will fail the test if the
// update is not consumed within our timeout.
func (m *mockSubscription) sendUpdate(update interface{}) {
	select {
	case m.updates <- update:

	case <-time.After(timeout):
		m.t.Fatalf("update: %v timeout", update)
	}
}

// Updates returns the updates channel for the mock.
func (m *mockSubscription) Updates() <-chan interface{} {
	return m.updates
}

// Cancel should be called in case the client no longer wants to subscribe for
// updates from the server.
func (m *mockSubscription) Cancel() {
	close(m.updates)
}

// assertCancelled asserts that the cancel function has been called for this
// mock.
func (m *mockSubscription) assertCancelled() {
	select {
	case _, open := <-m.updates:
		require.False(m.t, open, "subscription not cancelled")

	case <-time.After(timeout):
		m.t.Fatalf("assert cancelled timeout")
	}
}
