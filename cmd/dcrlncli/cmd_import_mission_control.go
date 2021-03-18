package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/decred/dcrlnd/lnrpc/routerrpc"
	"github.com/decred/dcrlnd/routing/route"
	"github.com/urfave/cli"
)

const argsStr = "[source node] [dest node] [unix ts seconds] [amount in msat]"

var importMissionControlCommand = cli.Command{
	Name:      "importmc",
	Category:  "Payments",
	Usage:     "Import a result to the internal mission control state.",
	ArgsUsage: fmt.Sprintf("importmc %v", argsStr),
	Action:    actionDecorator(importMissionControl),
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "failure",
			Usage: "whether the routing history entry was a failure",
		},
	},
}

func importMissionControl(ctx *cli.Context) error {
	conn := getClientConn(ctx, false)
	defer conn.Close()

	if ctx.NArg() != 4 {
		return fmt.Errorf("please provide args: %v", argsStr)
	}

	args := ctx.Args()

	sourceNode, err := route.NewVertexFromStr(args[0])
	if err != nil {
		return fmt.Errorf("please provide valid source node: %v", err)
	}

	destNode, err := route.NewVertexFromStr(args[1])
	if err != nil {
		return fmt.Errorf("please provide valid dest node: %v", err)
	}

	ts, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return fmt.Errorf("please provide unix timestamp "+
			"in seconds: %v", err)
	}

	if ts <= 0 {
		return errors.New("please provide positive timestamp")
	}

	amt, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return fmt.Errorf("please provide amount in msat: %v", err)
	}

	if amt <= 0 {
		return errors.New("amount must be >0")
	}

	client := routerrpc.NewRouterClient(conn)

	importResult := &routerrpc.PairHistory{
		NodeFrom: sourceNode[:],
		NodeTo:   destNode[:],
		History:  &routerrpc.PairData{},
	}

	if ctx.IsSet("failure") {
		importResult.History.FailAmtMAtoms = amt
		importResult.History.FailTime = ts
	} else {
		importResult.History.SuccessAmtMAtoms = amt
		importResult.History.SuccessTime = ts
	}

	req := &routerrpc.XImportMissionControlRequest{
		Pairs: []*routerrpc.PairHistory{
			importResult,
		},
	}

	rpcCtx := context.Background()
	_, err = client.XImportMissionControl(rpcCtx, req)
	return err
}
