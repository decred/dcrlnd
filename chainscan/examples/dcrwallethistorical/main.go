// Copyright (c) 2020 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"github.com/jessevdk/go-flags"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type config struct {
	RPCCert    string   `long:"rpccert" description:"File containing the certificate file"`
	RPCConnect string   `short:"c" long:"rpcconnect" description:"Network address of dcrd RPC server"`
	TestNet    bool     `long:"testnet" description:"Use the test network"`
	Targets    []string `short:"t" long:"target" description:"Target address to search for. Can be multiple."`
}

type dcrwConfig struct {
	RPCCert string `long:"rpccert" description:"File containing the certificate file"`
	TestNet bool   `long:"testnet" description:"Use the test network"`
}

func main() {
	dcrwHomeDir := dcrutil.AppDataDir("dcrwallet", false)
	opts := &config{
		RPCCert: filepath.Join(dcrwHomeDir, "rpc.cert"),
	}
	dcrwCfgFile := filepath.Join(dcrwHomeDir, "dcrwallet.conf")
	if _, err := os.Stat(dcrwCfgFile); err == nil {
		// ~/.dcrwallet/dcrwallet.conf exists. Read precfg data from
		// it.
		dcrwOpts := &dcrwConfig{}
		parser := flags.NewParser(dcrwOpts, flags.Default)
		err := flags.NewIniParser(parser).ParseFile(dcrwCfgFile)
		if err == nil {
			switch {
			case dcrwOpts.TestNet:
				opts.RPCConnect = "localhost:19111"
				opts.TestNet = true
			default:
				opts.RPCConnect = "localhost:9111"
			}
		}
	}

	parser := flags.NewParser(opts, flags.Default)
	_, err := parser.Parse()
	if err != nil {
		var e *flags.Error
		if errors.As(err, &e) && e.Type == flags.ErrHelp {
			os.Exit(0)
		}
		parser.WriteHelp(os.Stderr)
		return
	}

	params := chaincfg.MainNetParams()
	if opts.TestNet {
		params = chaincfg.TestNet3Params()
	}

	if len(opts.Targets) == 0 {
		log.Fatal("Specify at least one target address")
	}

	// Connect to local dcrwallet gRPC server using websockets.
	creds, err := credentials.NewClientTLSFromFile(opts.RPCCert, "localhost")
	if err != nil {
		log.Fatalf("Error creating credentials: %v", err)

	}
	conn, err := grpc.Dial(opts.RPCConnect, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatalf("Error connecting to dcrwallet's gRPC: %v", err)
	}
	defer conn.Close()

	w := walletrpc.NewWalletServiceClient(conn)
	n := walletrpc.NewNetworkServiceClient(conn)
	resp, err := w.BestBlock(context.Background(), &walletrpc.BestBlockRequest{})
	if err != nil {
		log.Fatal(err)
	}
	bestHeight := int32(resp.Height)
	log.Printf("Searching up to height %d", bestHeight)
	startTime := time.Now()

	var targets []chainscan.TargetAndOptions
	for _, t := range opts.Targets {
		addr, err := stdaddr.DecodeAddress(t, params)
		if err != nil {
			log.Fatal(err)
		}

		scriptVer, script := addr.PaymentScript()

		foundCb := func(e chainscan.Event, findMore chainscan.FindFunc) {
			outp := wire.OutPoint{
				Hash:  e.Tx.TxHash(),
				Index: uint32(e.Index),
				Tree:  e.Tree,
			}
			log.Printf("Found addr %s mined at block %d (output %s)",
				t, e.BlockHeight, outp)

			foundSpendCb := func(es chainscan.Event, _ chainscan.FindFunc) {
				log.Printf("Found addr %s (outpoint %s) spent at block %d (input %s:%d)",
					t, outp, es.BlockHeight,
					es.Tx.TxHash(), es.Index)
			}
			findMore(
				chainscan.SpentOutPoint(outp, scriptVer, script),
				chainscan.WithFoundCallback(foundSpendCb),
				chainscan.WithEndHeight(int32(bestHeight)),
			)
		}

		target := chainscan.TargetAndOptions{
			Target: chainscan.ConfirmedScript(0, script),
			Options: []chainscan.Option{
				chainscan.WithFoundCallback(foundCb),
				chainscan.WithEndHeight(int32(bestHeight)),
			},
		}
		targets = append(targets, target)
	}

	completeChan := make(chan struct{})
	targets[0].Options = append(targets[0].Options, chainscan.WithCompleteChan(completeChan))

	chainSrc := csdrivers.NewRemoteWalletCSDriver(w, n, nil)
	hist := chainscan.NewHistorical(chainSrc)
	histCtx, cancel := context.WithCancel(context.Background())
	go func() {
		err := hist.Run(histCtx)
		select {
		case <-histCtx.Done():
		default:
			log.Printf("Historical run errored: %v", err)
			close(completeChan)
		}
	}()

	hist.FindMany(targets)
	<-completeChan

	endTime := time.Now()
	log.Printf("Completed search in %s", endTime.Sub(startTime))

	cancel()
}
