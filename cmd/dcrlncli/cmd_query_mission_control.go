// +build !no_routerrpc

package main

import (
	"context"
	"encoding/hex"

	"github.com/decred/dcrlnd/lnrpc/routerrpc"

	"github.com/urfave/cli"
)

var queryMissionControlCommand = cli.Command{
	Name:     "querymc",
	Category: "Payments",
	Usage:    "Query the internal mission control state.",
	Action:   actionDecorator(queryMissionControl),
}

func queryMissionControl(ctx *cli.Context) error {
	conn := getClientConn(ctx, false)
	defer conn.Close()

	client := routerrpc.NewRouterClient(conn)

	req := &routerrpc.QueryMissionControlRequest{}
	rpcCtx := context.Background()
	snapshot, err := client.QueryMissionControl(rpcCtx, req)
	if err != nil {
		return err
	}

	type displayPairHistory struct {
		NodeFrom, NodeTo      string
		LastAttemptSuccessful bool
		Timestamp             int64
		MinPenalizeAmtAtoms   int64
	}

	displayResp := struct {
		Pairs []displayPairHistory
	}{}

	for _, n := range snapshot.Pairs {
		displayResp.Pairs = append(
			displayResp.Pairs,
			displayPairHistory{
				NodeFrom:              hex.EncodeToString(n.NodeFrom),
				NodeTo:                hex.EncodeToString(n.NodeTo),
				LastAttemptSuccessful: n.LastAttemptSuccessful,
				Timestamp:             n.Timestamp,
				MinPenalizeAmtAtoms:   n.MinPenalizeAmtAtoms,
			},
		)
	}

	printJSON(displayResp)

	return nil
}