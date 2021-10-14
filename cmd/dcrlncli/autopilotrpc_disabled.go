//go:build no_autopilotrpc
// +build no_autopilotrpc

package main

import "github.com/urfave/cli"

// autopilotCommands will return nil for non-autopilotrpc builds.
func autopilotCommands() []cli.Command {
	return nil
}
