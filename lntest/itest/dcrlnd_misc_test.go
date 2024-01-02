package itest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
)

// testConcurrentNodeConnection tests whether we can repeatedly connect and
// disconnect two nodes and that trying to connect "at the same time" will not
// cause problems.
//
// "At the same time" is used in scare quotes, since at the level of abstraction
// used in these tests it's not possible to really enforce that the connection
// attempts are sent in any specific order or parallelism. So we resort to doing
// a number of repeated number of trial runs, with as much concurrency as
// possible.
func testConcurrentNodeConnection(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	aliceToBobReq := &lnrpc.ConnectPeerRequest{
		Addr: &lnrpc.LightningAddress{
			Pubkey: net.Bob.PubKeyStr,
			Host:   net.Bob.P2PAddr(),
		},
		Perm: false,
	}

	bobToAliceReq := &lnrpc.ConnectPeerRequest{
		Addr: &lnrpc.LightningAddress{
			Pubkey: net.Alice.PubKeyStr,
			Host:   net.Alice.P2PAddr(),
		},
		Perm: false,
	}

	connect := func(node *lntest.HarnessNode, req *lnrpc.ConnectPeerRequest, wg *sync.WaitGroup) error {
		_, err := node.ConnectPeer(ctxb, req)
		wg.Done()
		return err
	}

	// Initially disconnect Alice and Bob. Several connection attempts will
	// be performed later on. Ignore errors if they are not connected and
	// give some time for the disconnection to clear all resources.
	net.DisconnectNodes(ctxb, net.Alice, net.Bob)
	time.Sleep(50 * time.Millisecond)

	// Perform a number of trial runs in sequence, so we have some reasonable
	// chance actually performing connections "at the same time".
	nbAttempts := 10
	for i := 0; i < nbAttempts; i++ {
		// Sanity check that neither node has a connection.
		assertNumConnections(t, net.Alice, net.Bob, 0)

		logLine := fmt.Sprintf("=== %s: Starting connection iteration %d\n",
			time.Now(), i)
		net.Alice.AddToLog(logLine)
		net.Bob.AddToLog(logLine)

		var aliceReply, bobReply error
		wg := new(sync.WaitGroup)

		// Start two go routines which will try to connect "at the same
		// time".
		wg.Add(2)
		go func() { aliceReply = connect(net.Alice, aliceToBobReq, wg) }()
		go func() { bobReply = connect(net.Bob, bobToAliceReq, wg) }()

		wgWaitChan := make(chan struct{})
		go func() {
			wg.Wait()
			close(wgWaitChan)
		}()

		select {
		case <-wgWaitChan:
			if aliceReply != nil && bobReply != nil {
				// Depending on exact timings, one of the replies might fail
				// due to the nodes already being connected, but not both.
				t.Fatalf("Both replies should not error out")
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("Timeout while waiting for connection reply")
		}

		// Give the nodes time to settle their connections and background
		// processes.
		time.Sleep(50 * time.Millisecond)

		logLine = fmt.Sprintf("=== %s: Connections requests sent. Will check on status\n",
			time.Now())
		net.Alice.AddToLog(logLine)
		net.Bob.AddToLog(logLine)

		// Sanity check connection number.
		assertNumConnections(t, net.Alice, net.Bob, 1)

		// Check whether the connection was made alice -> bob or bob ->
		// alice.  The assert above ensures we can safely access
		// alicePeers[0].
		alicePeers, err := net.Alice.ListPeers(ctxb, &lnrpc.ListPeersRequest{})
		if err != nil {
			t.Fatalf("unable to fetch carol's peers %v", err)
		}
		if !alicePeers.Peers[0].Inbound {
			// Connection was made in the alice -> bob direction.
			net.DisconnectNodes(ctxb, net.Alice, net.Bob)
		} else {
			// Connection was made in the alice <- bob direction.
			net.DisconnectNodes(ctxb, net.Alice, net.Bob)
		}
	}
	logLine := fmt.Sprintf("=== %s: Reconnection tests successful\n",
		time.Now())
	net.Alice.AddToLog(logLine)
	net.Bob.AddToLog(logLine)

	// Wait for the final disconnection to release all resources, then
	// ensure both nodes are connected again.
	time.Sleep(time.Millisecond * 50)
	assertNumConnections(t, net.Alice, net.Bob, 0)
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.EnsureConnected(ctxt, t.t, net.Alice, net.Bob)
	time.Sleep(time.Millisecond * 50)
}
