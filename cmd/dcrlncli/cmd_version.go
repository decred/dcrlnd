package main

import (
	"fmt"
	"runtime"

	"github.com/decred/dcrlnd/build"
	"github.com/decred/dcrlnd/lnrpc/lnclipb"
	"github.com/decred/dcrlnd/lnrpc/verrpc"
	"github.com/urfave/cli"
)

var versionCommand = cli.Command{
	Name:  "version",
	Usage: "Display lncli and lnd version info.",
	Description: `
	Returns version information about both lncli and lnd. If lncli is unable
	to connect to lnd, the command fails but still prints the lncli version.
	`,
	Action: actionDecorator(version),
}

func version(ctx *cli.Context) error {
	ctxc := getContext()
	conn := getClientConn(ctx, false)
	defer conn.Close()

	major, minor, patch := build.MajorMinorPatch()

	versions := &lnclipb.VersionResponse{
		Lncli: &verrpc.Version{
			Commit:        build.Commit,
			CommitHash:    build.Commit,
			Version:       build.Version(),
			AppMajor:      uint32(major),
			AppMinor:      uint32(minor),
			AppPatch:      uint32(patch),
			AppPreRelease: build.PreRelease,
			BuildTags:     nil,
			GoVersion:     runtime.Version(),
		},
	}

	client := verrpc.NewVersionerClient(conn)

	lndVersion, err := client.GetVersion(ctxc, &verrpc.VersionRequest{})
	if err != nil {
		printRespJSON(versions)
		return fmt.Errorf("unable fetch version from lnd: %v", err)
	}
	versions.Lnd = lndVersion

	printRespJSON(versions)

	return nil
}
