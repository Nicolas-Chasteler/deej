package util

import (
	"os/exec"
	"syscall"
)

// killProcessTreeOnCancel puts the command in its own process group and makes
// cancellation kill the whole group. Killing only the shell would leave
// whatever it launched (ydotool, playerctl) running.
func killProcessTreeOnCancel(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
