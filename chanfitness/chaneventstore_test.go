package chanfitness

import (
	"errors"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/clock"
	"github.com/decred/dcrlnd/routing/route"
	"github.com/decred/dcrlnd/subscribe"
	"github.com/stretchr/testify/require"
)

// testNow is the current time tests will use.
var testNow = time.Unix(1592465134, 0)

// TestStartStoreError tests the starting of the store in cases where the setup
// functions fail. It does not test the mechanics of consuming events because
// these are covered in a separate set of tests.
func TestStartStoreError(t *testing.T) {
	// Ok and erroring subscribe functions are defined here to de-clutter
	// tests.
	okSubscribeFunc := func() (subscribe.Subscription, error) {
		return newMockSubscription(t), nil
	}

	errSubscribeFunc := func() (subscribe.Subscription, error) {
		return nil, errors.New("intentional test err")
	}

	tests := []struct {
		name          string
		ChannelEvents func() (subscribe.Subscription, error)
		PeerEvents    func() (subscribe.Subscription, error)
		GetChannels   func() ([]*channeldb.OpenChannel, error)
	}{
		{
			name:          "Channel events fail",
			ChannelEvents: errSubscribeFunc,
		},
		{
			name:          "Peer events fail",
			ChannelEvents: okSubscribeFunc,
			PeerEvents:    errSubscribeFunc,
		},
		{
			name:          "Get open channels fails",
			ChannelEvents: okSubscribeFunc,
			PeerEvents:    okSubscribeFunc,
			GetChannels: func() ([]*channeldb.OpenChannel, error) {
				return nil, errors.New("intentional test err")
			},
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			clock := clock.NewTestClock(testNow)

			store := NewChannelEventStore(&Config{
				SubscribeChannelEvents: test.ChannelEvents,
				SubscribePeerEvents:    test.PeerEvents,
				GetOpenChannels:        test.GetChannels,
				Clock:                  clock,
			})

			err := store.Start()
			// Check that we receive an error, because the test only
			// checks for error cases.
			if err == nil {
				t.Fatalf("Expected error on startup, got: nil")
			}
		})
	}
}

// TestMonitorChannelEvents tests the store's handling of channel and peer
// events. It tests for the unexpected cases where we receive a channel open for
// an already known channel and but does not test for closing an unknown channel
// because it would require custom logic in the test to prevent iterating
// through an eventLog which does not exist. This test does not test handling
// of uptime and lifespan requests, as they are tested in their own tests.
func TestMonitorChannelEvents(t *testing.T) {
	privKey, _ := secp256k1.GeneratePrivateKey()
	var (
		pubKey = privKey.PubKey()
		chan1  = wire.OutPoint{Index: 1}
		chan2  = wire.OutPoint{Index: 2}
	)

	peer1, err := route.NewVertexFromBytes(pubKey.SerializeCompressed())
	require.NoError(t, err)

	t.Run("peer comes online after channel open", func(t *testing.T) {
		gen := func(ctx *chanEventStoreTestCtx) {
			ctx.sendChannelOpenedUpdate(pubKey, chan1)
			ctx.peerEvent(peer1, true)
		}

		testEventStore(t, gen, 1, map[route.Vertex]bool{
			peer1: true,
		})
	})

	t.Run("duplicate channel open events", func(t *testing.T) {
		gen := func(ctx *chanEventStoreTestCtx) {
			ctx.sendChannelOpenedUpdate(pubKey, chan1)
			ctx.sendChannelOpenedUpdate(pubKey, chan1)
			ctx.peerEvent(peer1, true)
		}

		testEventStore(t, gen, 1, map[route.Vertex]bool{
			peer1: true,
		})
	})

	t.Run("peer online before channel created", func(t *testing.T) {
		gen := func(ctx *chanEventStoreTestCtx) {
			ctx.peerEvent(peer1, true)
			ctx.sendChannelOpenedUpdate(pubKey, chan1)
		}

		testEventStore(t, gen, 1, map[route.Vertex]bool{
			peer1: true,
		})
	})

	t.Run("multiple channels for peer", func(t *testing.T) {
		gen := func(ctx *chanEventStoreTestCtx) {
			ctx.peerEvent(peer1, true)
			ctx.sendChannelOpenedUpdate(pubKey, chan1)

			ctx.peerEvent(peer1, false)
			ctx.sendChannelOpenedUpdate(pubKey, chan2)
		}

		testEventStore(t, gen, 2, map[route.Vertex]bool{
			peer1: false,
		})
	})

	t.Run("multiple channels for peer, one closed", func(t *testing.T) {
		gen := func(ctx *chanEventStoreTestCtx) {
			ctx.peerEvent(peer1, true)
			ctx.sendChannelOpenedUpdate(pubKey, chan1)

			ctx.peerEvent(peer1, false)
			ctx.sendChannelOpenedUpdate(pubKey, chan2)

			ctx.closeChannel(chan1, pubKey)
			ctx.peerEvent(peer1, true)
		}

		testEventStore(t, gen, 2, map[route.Vertex]bool{
			peer1: true,
		})
	})

}

// testEventStore creates a new test contexts, generates a set of events for it
// and tests that it has the number of channels and online state for peers that
// we expect.
func testEventStore(t *testing.T, generateEvents func(*chanEventStoreTestCtx),
	expectedChannels int, expectedPeers map[route.Vertex]bool) {

	testCtx := newChanEventStoreTestCtx(t)
	testCtx.start()

	generateEvents(testCtx)

	// Shutdown the store so that we can safely access the maps in our event
	// store.
	testCtx.stop()
	require.Len(t, testCtx.store.channels, expectedChannels)
	require.Equal(t, expectedPeers, testCtx.store.peers)
}

// TestAddChannel tests that channels are added to the event store with
// appropriate timestamps. This test addresses a bug where offline channels
// did not have an opened time set, and checks that an online event is set for
// peers that are online at the time that a channel is opened.
func TestAddChannel(t *testing.T) {
	ctx := newChanEventStoreTestCtx(t)
	ctx.start()

	// Create a channel for a peer that is not online yet.
	_, _, channel1 := ctx.createChannel()

	// Get a set of values for another channel, but do not create it yet.
	//
	peer2, pubkey2, channel2 := ctx.newChannel()
	ctx.peerEvent(peer2, true)
	ctx.sendChannelOpenedUpdate(pubkey2, channel2)

	ctx.stop()

	// Assert that our peer that was offline on connection has no events
	// and our peer that was online on connection has one.
	require.Len(t, ctx.store.channels[channel1].events, 0)

	chan2Events := ctx.store.channels[channel2].events
	require.Len(t, chan2Events, 1)
	require.Equal(t, peerOnlineEvent, chan2Events[0].eventType)
}

// TestGetChanInfo tests the GetChanInfo function for the cases where a channel
// is known and unknown to the store.
func TestGetChanInfo(t *testing.T) {
	ctx := newChanEventStoreTestCtx(t)
	ctx.start()

	// Make a note of the time that our mocked clock starts on.
	now := ctx.clock.Now()

	// Create mock vars for a channel but do not add them to our store yet.
	peer, pk, channel := ctx.newChannel()

	// Send an online event for our peer, although we do not yet have an
	// open channel.
	ctx.peerEvent(peer, true)

	// Try to get info for a channel that has not been opened yet, we
	// expect to get an error.
	_, err := ctx.store.GetChanInfo(channel)
	require.Equal(t, ErrChannelNotFound, err)

	// Now we send our store a notification that a channel has been opened.
	ctx.sendChannelOpenedUpdate(pk, channel)

	// Wait for our channel to be recognized by our store. We need to wait
	// for the channel to be created so that we do not update our time
	// before the channel open is processed.
	require.Eventually(t, func() bool {
		_, err = ctx.store.GetChanInfo(channel)
		return err == nil
	}, timeout, time.Millisecond*20)

	// Increment our test clock by an hour.
	now = now.Add(time.Hour)
	ctx.clock.SetTime(now)

	// At this stage our channel has been open and online for an hour.
	info, err := ctx.store.GetChanInfo(channel)
	require.NoError(t, err)
	require.Equal(t, time.Hour, info.Lifetime)
	require.Equal(t, time.Hour, info.Uptime)

	// Now we send a peer offline event for our channel.
	ctx.peerEvent(peer, false)

	// Since we have not bumped our mocked time, our uptime calculations
	// should be the same, even though we've just processed an offline
	// event.
	info, err = ctx.store.GetChanInfo(channel)
	require.NoError(t, err)
	require.Equal(t, time.Hour, info.Lifetime)
	require.Equal(t, time.Hour, info.Uptime)

	// Progress our time again. This time, our peer is currently tracked as
	// being offline, so we expect our channel info to reflect that the peer
	// has been offline for this period.
	now = now.Add(time.Hour)
	ctx.clock.SetTime(now)

	info, err = ctx.store.GetChanInfo(channel)
	require.NoError(t, err)
	require.Equal(t, time.Hour*2, info.Lifetime)
	require.Equal(t, time.Hour, info.Uptime)

	ctx.stop()
}
