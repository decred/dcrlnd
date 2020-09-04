// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2019 The Decred developers
// Copyright (C) 2015-2017 The Lightning Network Developers

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	macaroon "gopkg.in/macaroon.v2"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/build"
	"github.com/decred/dcrlnd/lncfg"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/macaroons"
	"github.com/urfave/cli"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	defaultDataDir          = "data"
	defaultChainSubDir      = "chain"
	defaultTLSCertFilename  = "tls.cert"
	defaultMacaroonFilename = "admin.macaroon"
	defaultRPCPort          = "10009"
	defaultRPCHostPort      = "localhost:" + defaultRPCPort
)

var (
	defaultLndDir      = dcrutil.AppDataDir("dcrlnd", false)
	defaultTLSCertPath = filepath.Join(defaultLndDir, defaultTLSCertFilename)

	// maxMsgRecvSize is the largest message our client will receive. We
	// set this to 200MiB atm.
	maxMsgRecvSize = grpc.MaxCallRecvMsgSize(1 * 1024 * 1024 * 200)
)

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "[dcrlncli] %v\n", err)
	os.Exit(1)
}

func getWalletUnlockerClient(ctx *cli.Context) (lnrpc.WalletUnlockerClient, func()) {
	conn := getClientConn(ctx, true)

	cleanUp := func() {
		conn.Close()
	}

	return lnrpc.NewWalletUnlockerClient(conn), cleanUp
}

func getClient(ctx *cli.Context) (lnrpc.LightningClient, func()) {
	conn := getClientConn(ctx, false)

	cleanUp := func() {
		conn.Close()
	}

	return lnrpc.NewLightningClient(conn), cleanUp
}

func getClientConn(ctx *cli.Context, skipMacaroons bool) *grpc.ClientConn {
	// First, we'll parse the args from the command.
	tlsCertPath, macPath, err := extractPathArgs(ctx)
	if err != nil {
		fatal(err)
	}

	// Load the specified TLS certificate and build transport credentials
	// with it.
	creds, err := credentials.NewClientTLSFromFile(tlsCertPath, "")
	if err != nil {
		fatal(err)
	}

	// Create a dial options array.
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
	}

	// Only process macaroon credentials if --no-macaroons isn't set and
	// if we're not skipping macaroon processing.
	if !ctx.GlobalBool("no-macaroons") && !skipMacaroons {
		// Load the specified macaroon file.
		macBytes, err := ioutil.ReadFile(macPath)
		if err != nil {
			fatal(fmt.Errorf("unable to read macaroon path (check "+
				"the network setting!): %v", err))
		}

		mac := &macaroon.Macaroon{}
		if err = mac.UnmarshalBinary(macBytes); err != nil {
			fatal(fmt.Errorf("unable to decode macaroon: %v", err))
		}

		macConstraints := []macaroons.Constraint{
			// We add a time-based constraint to prevent replay of the
			// macaroon. It's good for 60 seconds by default to make up for
			// any discrepancy between client and server clocks, but leaking
			// the macaroon before it becomes invalid makes it possible for
			// an attacker to reuse the macaroon. In addition, the validity
			// time of the macaroon is extended by the time the server clock
			// is behind the client clock, or shortened by the time the
			// server clock is ahead of the client clock (or invalid
			// altogether if, in the latter case, this time is more than 60
			// seconds).
			// TODO(aakselrod): add better anti-replay protection.
			macaroons.TimeoutConstraint(ctx.GlobalInt64("macaroontimeout")),

			// Lock macaroon down to a specific IP address.
			macaroons.IPLockConstraint(ctx.GlobalString("macaroonip")),

			// ... Add more constraints if needed.
		}

		// Apply constraints to the macaroon.
		constrainedMac, err := macaroons.AddConstraints(mac, macConstraints...)
		if err != nil {
			fatal(err)
		}

		// Now we append the macaroon credentials to the dial options.
		cred := macaroons.NewMacaroonCredential(constrainedMac)
		opts = append(opts, grpc.WithPerRPCCredentials(cred))
	}

	// We need to use a custom dialer so we can also connect to unix sockets
	// and not just TCP addresses.
	genericDialer := lncfg.ClientAddressDialer(defaultRPCPort)
	opts = append(opts, grpc.WithContextDialer(genericDialer))
	opts = append(opts, grpc.WithDefaultCallOptions(maxMsgRecvSize))

	conn, err := grpc.Dial(ctx.GlobalString("rpcserver"), opts...)
	if err != nil {
		fatal(fmt.Errorf("unable to connect to RPC server: %v", err))
	}

	return conn
}

// extractPathArgs parses the TLS certificate and macaroon paths from the
// command.
func extractPathArgs(ctx *cli.Context) (string, string, error) {
	// We'll start off by parsing the active chain and network. These are
	// needed to determine the correct path to the macaroon when not
	// specified.
	chain := strings.ToLower(ctx.GlobalString("chain"))
	switch chain {
	case "decred":
	default:
		return "", "", fmt.Errorf("unknown chain: %v", chain)
	}

	// We default to mainnet if no other options are specified.
	network := "mainnet"

	networks := []string{"testnet", "regtest", "simnet"}
	numNets := 0

	for _, n := range networks {
		if ctx.GlobalBool(n) {
			network = n
			numNets++
		}
	}

	if numNets > 1 {
		str := "extractPathArgs: The testnet, regtest, and simnet params" +
			"can't be used together -- choose one of the three"
		err := fmt.Errorf(str)

		return "", "", err
	}

	// We'll now fetch the lnddir so we can make a decision  on how to
	// properly read the macaroons (if needed) and also the cert. This will
	// either be the default, or will have been overwritten by the end
	// user.
	lndDir := lncfg.CleanAndExpandPath(ctx.GlobalString("lnddir"))

	// If the macaroon path as been manually provided, then we'll only
	// target the specified file.
	var macPath string
	if ctx.GlobalString("macaroonpath") != "" {
		macPath = lncfg.CleanAndExpandPath(ctx.GlobalString("macaroonpath"))
	} else {
		// Otherwise, we'll go into the path:
		// lnddir/data/chain/<chain>/<network> in order to fetch the
		// macaroon that we need.
		macPath = filepath.Join(
			lndDir, defaultDataDir, defaultChainSubDir, chain,
			network, defaultMacaroonFilename,
		)
	}

	tlsCertPath := lncfg.CleanAndExpandPath(ctx.GlobalString("tlscertpath"))

	// If a custom dcrlnd directory was set, we'll also check if custom
	// paths for the TLS cert and macaroon file were set as well. If not,
	// we'll override their paths so they can be found within the custom
	// dcrlnd directory set. This allows us to set a custom lnd directory,
	// along with custom paths to the TLS cert and macaroon file.
	if lndDir != defaultLndDir {
		tlsCertPath = filepath.Join(lndDir, defaultTLSCertFilename)
	}

	return tlsCertPath, macPath, nil
}

func main() {
	// Use the standart decred -V (capital V) flag for version.
	cli.VersionFlag = cli.BoolFlag{
		Name:  "version, V",
		Usage: "print the version",
	}

	// Build the standard version string.
	versionStr := fmt.Sprintf("%s (Go version %s %s/%s)\n",
		build.Version(), runtime.Version(), runtime.GOOS, runtime.GOARCH)

	app := cli.NewApp()
	app.Name = "dcrlncli"
	app.Version = versionStr
	app.Usage = "control plane for your Decred Lightning Network Daemon (dcrlnd)"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "rpcserver",
			Value: defaultRPCHostPort,
			Usage: "host:port of Decred LN daemon",
		},
		cli.StringFlag{
			Name:  "lnddir",
			Value: defaultLndDir,
			Usage: "path to dcrlnd's base directory",
		},
		cli.StringFlag{
			Name:  "tlscertpath",
			Value: defaultTLSCertPath,
			Usage: "path to TLS certificate",
		},
		cli.StringFlag{
			Name:  "chain, c",
			Usage: "the chain lnd is running on e.g. decred",
			Value: "decred",
		},
		cli.BoolFlag{
			Name:  "testnet",
			Usage: "Use the test network",
		},
		cli.BoolFlag{
			Name:  "simnet",
			Usage: "Use the simulation network",
		},
		cli.BoolFlag{
			Name:  "regtest",
			Usage: "Use the regression test network",
		},
		cli.BoolFlag{
			Name:  "no-macaroons",
			Usage: "disable macaroon authentication",
		},
		cli.StringFlag{
			Name:  "macaroonpath",
			Usage: "path to macaroon file",
		},
		cli.Int64Flag{
			Name:  "macaroontimeout",
			Value: 60,
			Usage: "anti-replay macaroon validity time in seconds",
		},
		cli.StringFlag{
			Name:  "macaroonip",
			Usage: "if set, lock macaroon to specific IP address",
		},
	}
	app.Commands = []cli.Command{
		createCommand,
		unlockCommand,
		changePasswordCommand,
		newAddressCommand,
		estimateFeeCommand,
		sendManyCommand,
		sendCoinsCommand,
		listUnspentCommand,
		connectCommand,
		disconnectCommand,
		openChannelCommand,
		closeChannelCommand,
		closeAllChannelsCommand,
		abandonChannelCommand,
		listPeersCommand,
		walletBalanceCommand,
		channelBalanceCommand,
		getInfoCommand,
		getRecoveryInfoCommand,
		pendingChannelsCommand,
		sendPaymentCommand,
		payInvoiceCommand,
		sendToRouteCommand,
		addInvoiceCommand,
		lookupInvoiceCommand,
		listInvoicesCommand,
		listChannelsCommand,
		closedChannelsCommand,
		listPaymentsCommand,
		describeGraphCommand,
		getNodeMetricsCommand,
		getChanInfoCommand,
		getNodeInfoCommand,
		queryRoutesCommand,
		getNetworkInfoCommand,
		debugLevelCommand,
		decodePayReqCommand,
		listChainTxnsCommand,
		stopCommand,
		signMessageCommand,
		verifyMessageCommand,
		feeReportCommand,
		updateChannelPolicyCommand,
		forwardingHistoryCommand,
		exportChanBackupCommand,
		verifyChanBackupCommand,
		restoreChanBackupCommand,
		bakeMacaroonCommand,
		listMacaroonIDsCommand,
		deleteMacaroonIDCommand,
		listPermissionsCommand,
		printMacaroonCommand,
		trackPaymentCommand,
		versionCommand,
		profileSubCommand,
	}

	// Add any extra commands determined by build flags.
	app.Commands = append(app.Commands, autopilotCommands()...)
	app.Commands = append(app.Commands, invoicesCommands()...)
	app.Commands = append(app.Commands, routerCommands()...)
	app.Commands = append(app.Commands, walletCommands()...)
	app.Commands = append(app.Commands, watchtowerCommands()...)
	app.Commands = append(app.Commands, wtclientCommands()...)

	if err := app.Run(os.Args); err != nil {
		fatal(err)
	}
}
