package main

import "github.com/urfave/cli"

// routerCommands will return nil for non-routerrpc builds.
func routerCommands() []cli.Command {
	return []cli.Command{
		queryMissionControlCommand,
		importMissionControlCommand,
		queryProbCommand,
		resetMissionControlCommand,
		buildRouteCommand,
		getCfgCommand,
		setCfgCommand,
		updateChanStatusCommand,
	}
}
