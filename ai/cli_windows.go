//go:build windows

package ai

import (
	"os/exec"
	"strconv"
	"syscall"
)

// prepareCommand starts the tool in its own process group, so a cancel
// can end the tree with taskkill.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

func killCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
