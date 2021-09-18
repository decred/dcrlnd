package testutils

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpcclient/v8"
	rpctest "github.com/decred/dcrtest/dcrdtest"
)

// NewSetupRPCTest attempts up to maxTries to setup an rpctest harness or
// errors. This is used to get around the fact the rpctest does not validate
// listening addresses beforehand and might try to listen on an used address in
// CI servers.
//
// The returned rpctest is already setup for use.
func NewSetupRPCTest(ctx context.Context, maxTries int, netParams *chaincfg.Params,
	handlers *rpcclient.NotificationHandlers,
	args []string, setupChain bool, numMatureOutputs uint32) (*rpctest.Harness, error) {

	// Append --nobanning because dcrd is now fast enough to block peers on
	// simnet.
	args = append(args, "--nobanning")

	var harness *rpctest.Harness
	var err error
	for i := 0; i < maxTries; i++ {
		harness, err = rpctest.New(nil, netParams, handlers, args)
		if err == nil {
			err = harness.SetUp(ctx, setupChain, numMatureOutputs)
			if err == nil {
				return harness, nil
			} else {
				err = fmt.Errorf("unable to setup node: %v", err)
			}
		} else {
			err = fmt.Errorf("unable to create harness node: %v", err)
		}

		time.Sleep(time.Second)
	}

	return nil, err
}

func init() {
	rpctest.SetPathToDCRD("dcrd-dcrlnd")
}
