//go:build mobile
// +build mobile

package dcrlndmobile

import (
	"fmt"
	"os"
	"strings"

	lnd "github.com/decred/dcrlnd"
	"github.com/decred/dcrlnd/signal"
	flags "github.com/jessevdk/go-flags"
)

// Start starts lnd in a new goroutine.
//
// extraArgs can be used to pass command line arguments to lnd that will
// override what is found in the config file. Example:
//
//	extraArgs = "--bitcoin.testnet --lnddir=\"/tmp/folder name/\" --profile=5050"
//
// The rpcReady is called lnd is ready to accept RPC calls.
//
// NOTE: On mobile platforms the '--lnddir` argument should be set to the
// current app directory in order to ensure lnd has the permissions needed to
// write to it.
func Start(extraArgs string, rpcReady Callback) {
	// Split the argument string on "--" to get separated command line
	// arguments.
	var splitArgs []string
	for _, a := range strings.Split(extraArgs, "--") {
		// Trim any whitespace space, and ignore empty params.
		a := strings.TrimSpace(a)
		if a == "" {
			continue
		}

		// Finally we prefix any non-empty string with -- to mimic the
		// regular command line arguments.
		splitArgs = append(splitArgs, "--"+a)
	}

	// Add the extra arguments to os.Args, as that will be parsed in
	// LoadConfig below.
	os.Args = append(os.Args, splitArgs...)

	// Hook interceptor for os signals.
	shutdownInterceptor, err := signal.Intercept()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		rpcReady.OnError(err)
		return
	}

	// Load the configuration, and parse the extra arguments as command
	// line options. This function will also set up logging properly.
	loadedConfig, err := lnd.LoadConfig(shutdownInterceptor)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		rpcReady.OnError(err)
		return
	}

	// Set a channel that will be notified when the RPC server is ready to
	// accept calls.
	var (
		rpcListening = make(chan struct{})
		quit         = make(chan struct{})
	)

	// We call the main method with the custom in-memory listener called by
	// the mobile APIs, such that the grpc server will use it.
	cfg := lnd.ListenerCfg{
		RPCListener: &lnd.ListenerWithSignal{
			Listener: lightningLis,
			Ready:    rpcListening,
		},
	}

	// Call the "real" main in a nested manner so the defers will properly
	// be executed in the case of a graceful shutdown.
	go func() {
		defer close(quit)

		if err := lnd.Main(
			loadedConfig, cfg, shutdownInterceptor,
		); err != nil {
			if e, ok := err.(*flags.Error); ok &&
				e.Type == flags.ErrHelp {
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
			rpcReady.OnError(err)
			return
		}
	}()

	// By default we'll apply the admin auth options, which will include
	// macaroons.
	setDefaultDialOption(
		func() ([]grpc.DialOption, error) {
			return lnd.AdminAuthOptions(loadedConfig, false)
		},
	)

	// For the WalletUnlocker and StateService, the macaroons might not be
	// available yet when called, so we use a more restricted set of
	// options that don't include them.
	setWalletUnlockerDialOption(
		func() ([]grpc.DialOption, error) {
			return lnd.AdminAuthOptions(loadedConfig, true)
		},
	)
	setStateDialOption(
		func() ([]grpc.DialOption, error) {
			return lnd.AdminAuthOptions(loadedConfig, true)
		},
	)

	// Finally we start a go routine that will call the provided callback
	// when the RPC server is ready to accept calls.
	go func() {
		select {
		case <-rpcListening:
		case <-quit:
			return
		}

		rpcReady.OnResponse([]byte{})
	}()
}
