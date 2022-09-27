package channeldb

import (
	"errors"
	"testing"
	"time"

	"github.com/decred/dcrlnd/routing/route"
	"github.com/stretchr/testify/require"
)

func TestGossiperTS(t *testing.T) {
	db, cleanup, err := MakeTestDB()
	require.NoError(t, err)
	defer cleanup()

	// Try to read the ts of an unrecorded peer.
	_, err = db.ReadPeerLastGossipMsgTS(testPub)
	if !errors.Is(err, ErrNoPeerLastGossipMsgTS) {
		t.Fatalf("Unexpected error: got %v, want %v", err, ErrNoPeerLastGossipMsgTS)
	}

	var (
		testPub2 = route.Vertex{2, 2, 2}
		wantTS   = time.Unix(200, 23)
	)

	// Write a test TS.
	err = db.UpdatePeerLastGossipMsgTS(testPub2, wantTS)
	require.NoError(t, err)

	// Read back the test TS.
	gotTS, err := db.ReadPeerLastGossipMsgTS(testPub2)
	require.NoError(t, err)
	require.Equal(t, wantTS, gotTS)

	// Write a TS that is before the last recorded TS. This should error.
	err = db.UpdatePeerLastGossipMsgTS(testPub2, wantTS.Add(-1))
	if !errors.Is(err, ErrOutdatedLastGossipMsgTS) {
		t.Fatalf("Unexpected error: got %v, want %v", err, ErrOutdatedLastGossipMsgTS)
	}

	// The TS should still be the old one.
	gotTS, err = db.ReadPeerLastGossipMsgTS(testPub2)
	require.NoError(t, err)
	require.Equal(t, wantTS, gotTS)

	// Write a TS that is after the last recorded TS.
	newWantTS := wantTS.Add(1)
	err = db.UpdatePeerLastGossipMsgTS(testPub2, newWantTS)
	require.NoError(t, err)

	// The TS should have been updated.
	gotTS, err = db.ReadPeerLastGossipMsgTS(testPub2)
	require.NoError(t, err)
	require.Equal(t, newWantTS, gotTS)
}
