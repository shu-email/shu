//go:build !windows

package ai

import (
	"os/exec"
	"syscall"
)

// prepareCommand puts the tool in a process group of its own, so that a
// cancel ends the whole tree it may have started.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
