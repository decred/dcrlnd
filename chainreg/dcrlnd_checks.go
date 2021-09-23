package chainreg

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/wire"
)

// checkDcrdNode checks whether the dcrd node reachable using the provided
// config is usable as source of chain information, given the requirements of a
// dcrlnd node.
func checkDcrdNode(wantNet wire.CurrencyNet, rpcConfig rpcclient.ConnConfig) error {
	connectTimeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	rpcConfig.DisableConnectOnNew = true
	rpcConfig.DisableAutoReconnect = false
	chainConn, err := rpcclient.New(&rpcConfig, nil)
	if err != nil {
		return err
	}

	// Try to connect to the given node.
	if err := chainConn.Connect(ctx, true); err != nil {
		return err
	}
	defer chainConn.Shutdown()

	// Verify whether the node is on the correct network.
	net, err := chainConn.GetCurrentNet(ctx)
	if err != nil {
		return err
	}
	if net != wantNet {
		return fmt.Errorf("dcrd node network mismatch")
	}

	return nil
}
