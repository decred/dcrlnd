// Copyright (c) 2020 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/jessevdk/go-flags"
)

type config struct {
	RPCUser    string   `short:"u" long:"rpcuser" description:"Username for RPC connections"`
	RPCPass    string   `short:"P" long:"rpcpass" description:"Password for RPC connections"`
	RPCCert    string   `long:"rpccert" description:"File containing the certificate file"`
	RPCConnect string   `short:"c" long:"rpcconnect" description:"Network address of dcrd RPC server"`
	TestNet    bool     `long:"testnet" description:"Use the test network"`
	Targets    []string `short:"t" long:"target" description:"Target address to search for. Can be multiple."`
}

type dcrdConfig struct {
	RPCUser string `short:"u" long:"rpcuser" description:"Username for RPC connections"`
	RPCPass string `short:"P" long:"rpcpass" description:"Password for RPC connections"`
	RPCCert string `long:"rpccert" description:"File containing the certificate file"`
	TestNet bool   `long:"testnet" description:"Use the test network"`
}

// dcrdChainSource implements a HistoricalChainSource that fetches data from an
// rpc client connected to a dcrd instance.
type dcrdChainSource struct {
	c *rpcclient.Client
}

func (s *dcrdChainSource) GetCFilter(ctx context.Context, height int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error) {
	var cfkey [16]byte

	bh, err := s.c.GetBlockHash(ctx, int64(height))
	if err != nil {
		return nil, cfkey, nil, err
	}

	head, err := s.c.GetBlockHeader(ctx, bh)
	if err != nil {
		return nil, cfkey, nil, err
	}

	// No need to check for proof inclusion of the cfilter in the header
	// since we trust this dcrd instance, but on SPV clients this would be
	// needed when fetching via the P2P network.
	cf, err := s.c.GetCFilterV2(ctx, bh)
	if err != nil {
		return nil, cfkey, nil, err
	}

	if height%10000 == 0 {
		log.Printf("Fetched CFilter for block height %d", height)
	}

	copy(cfkey[:], head.MerkleRoot[:])
	return bh, cfkey, cf.Filter, nil
}

func (s *dcrdChainSource) GetBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
	bl, err := s.c.GetBlock(ctx, bh)
	if err != nil {
		return nil, err
	}

	log.Printf("Fetched block for height %d", bl.Header.Height)
	return bl, err
}

func (s *dcrdChainSource) CurrentTip(ctx context.Context) (*chainhash.Hash, int32, error) {
	bh, h, err := s.c.GetBestBlock(ctx)
	return bh, int32(h), err
}

func main() {
	dcrdHomeDir := dcrutil.AppDataDir("dcrd", false)
	opts := &config{
		RPCCert: filepath.Join(dcrdHomeDir, "rpc.cert"),
	}
	dcrdCfgFile := filepath.Join(dcrdHomeDir, "dcrd.conf")
	if _, err := os.Stat(dcrdCfgFile); err == nil {
		// ~/.dcrd/dcrd.conf exists. Read precfg data from it.
		dcrdOpts := &dcrdConfig{}
		parser := flags.NewParser(dcrdOpts, flags.Default)
		err := flags.NewIniParser(parser).ParseFile(dcrdCfgFile)
		if err == nil {
			opts.RPCUser = dcrdOpts.RPCUser
			opts.RPCPass = dcrdOpts.RPCPass
			switch {
			case dcrdOpts.TestNet:
				opts.RPCConnect = "localhost:19109"
				opts.TestNet = true
			default:
				opts.RPCConnect = "localhost:9109"
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

	// Connect to local dcrd RPC server using websockets.
	certs, err := ioutil.ReadFile(opts.RPCCert)
	if err != nil {
		log.Fatal(err)
	}
	connCfg := &rpcclient.ConnConfig{
		Host:         opts.RPCConnect,
		Endpoint:     "ws",
		User:         opts.RPCUser,
		Pass:         opts.RPCPass,
		Certificates: certs,
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		log.Fatal(err)
	}

	_, bestHeight, err := client.GetBestBlock(context.Background())
	if err != nil {
		log.Fatal(err)
	}
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

	chainSrc := &dcrdChainSource{c: client}
	hist := chainscan.NewHistorical(chainSrc)
	histCtx, cancel := context.WithCancel(context.Background())
	go hist.Run(histCtx)

	hist.FindMany(targets)
	<-completeChan

	endTime := time.Now()
	log.Printf("Completed search in %s", endTime.Sub(startTime))

	cancel()
	client.Shutdown()
}
