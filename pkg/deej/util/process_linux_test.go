package util

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunShellCommandKillsTheWholeTreeOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	pidFile := t.TempDir() + "/child.pid"

	// a backgrounded child that outlives the shell and holds the output pipe
	start := time.Now()
	RunShellCommand(ctx, "sleep 30 & echo $! > "+pidFile+"; wait")

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("RunShellCommand took %v, want it to give up shortly after the timeout", elapsed)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}

	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err != nil {
		t.Fatal(err)
	}

	// give the kernel a moment to deliver the kill
	time.Sleep(100 * time.Millisecond)

	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d survived the timeout", pid)
	}
}
