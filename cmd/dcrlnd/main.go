package main

import (
	"fmt"
	"os"

	"github.com/decred/dcrlnd"
	"github.com/decred/dcrlnd/signal"
	flags "github.com/jessevdk/go-flags"
)

func main() {
	// Load the configuration, and parse any command line options. This
	// function will also set up logging properly.
	loadedConfig, err := dcrlnd.LoadConfig()
	if err != nil {
		if e, ok := err.(*flags.Error); !ok || e.Type != flags.ErrHelp {
			// Print error if not due to help request.
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		// Help was requested, exit normally.
		os.Exit(0)
	}

	// Hook interceptor for os signals.
	if err := signal.Intercept(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Call the "real" main in a nested manner so the defers will properly
	// be executed in the case of a graceful shutdown.
	err = dcrlnd.Main(
		loadedConfig, dcrlnd.ListenerCfg{}, signal.ShutdownChannel(),
	)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
