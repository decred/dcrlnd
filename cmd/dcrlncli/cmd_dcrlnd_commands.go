package main

import (
	"errors"
	"io"

	"github.com/decred/dcrlnd/lnrpc/walletrpc"
	"github.com/urfave/cli"
)

var rescanWalletCommand = cli.Command{
	Name:  "rescanwallet",
	Usage: "Peform an on-chain rescan for wallet funds.",
	Description: `
	Performs an on-chain rescan for wallet funds.

	This does NOT do any changes to LN channels or recovers funds encumbered
	by LN channels, this only attempts to find on-chain funds that may have
	been missed by dcrwallet.`,
	ArgsUsage: "[height]",
	Flags: []cli.Flag{
		cli.Int64Flag{
			Name:  "height",
			Usage: "The starting height from which to perform rescan",
		},
	},
	Action: actionDecorator(rescanWallet),
}

func rescanWallet(ctx *cli.Context) error {
	var (
		height int32
		err    error
	)
	ctxc := getContext()
	client, cleanUp := getWalletClient(ctx)
	defer cleanUp()

	if ctx.IsSet("height") {
		height = int32(ctx.Int64("height"))
	}

	request := &walletrpc.RescanWalletRequest{
		BeginHeight: height,
	}

	resp, err := client.RescanWallet(ctxc, request)
	if err != nil {
		return err
	}

	progress, err := resp.Recv()
	for err == nil {
		printRespJSON(progress)
		progress, err = resp.Recv()
	}
	if !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}
