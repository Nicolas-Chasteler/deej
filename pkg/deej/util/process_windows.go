package util

import "os/exec"

// killProcessTreeOnCancel leaves the default cancellation in place on Windows,
// which kills the cmd.exe process only
func killProcessTreeOnCancel(command *exec.Cmd) {}
