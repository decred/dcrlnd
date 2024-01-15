package main

import (
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/urfave/cli"
)

var calcPayStatsCommand = cli.Command{
	Name:      "calcpaystats",
	Usage:     "Scans the db and generates a report on total payment counts.",
	ArgsUsage: "",
	Category:  "Payments",
	Description: `
	Goes through the DB and generates a report on total number of payments
	made, settled and failed.

	NOTE: This requires a scan through the entire set of payments in the DB,
	so it may be slow on nodes that have a large number of payments.
	`,
	Action: actionDecorator(calcPayStats),
}

func calcPayStats(ctx *cli.Context) error {
	ctxc := getContext()

	client, cleanUp := getClient(ctx)
	defer cleanUp()

	req := &lnrpc.CalcPaymentStatsRequest{}
	resp, err := client.CalcPaymentStats(ctxc, req)
	if err != nil {
		return err
	}

	printRespJSON(resp)

	return nil
}
