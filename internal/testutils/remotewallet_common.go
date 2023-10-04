//go:build !windows
// +build !windows

package testutils

import (
	"os"
	"os/exec"
)

// setOSWalletCmdOptions sets platform-specific options needed to run dcrwallet.
func setOSWalletCmdOptions(pipeTX, pipeRX *ipcPipePair, cmd *exec.Cmd) {
	cmd.ExtraFiles = []*os.File{
		pipeTX.w,
		pipeRX.r,
	}
}

// appendOSWalletArgs appends platform-specific arguments needed to run dcrwallet.
func appendOSWalletArgs(pipeTX, pipeRX *ipcPipePair, args []string) []string {
	args = append(args, "--pipetx=3")
	args = append(args, "--piperx=4")
	return args
}
