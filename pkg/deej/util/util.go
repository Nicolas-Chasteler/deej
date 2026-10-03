package util

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// EnsureDirExists creates the given directory path if it doesn't already exist
func EnsureDirExists(path string) error {
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return fmt.Errorf("ensure directory exists (%s): %w", path, err)
	}

	return nil
}

// FileExists checks if a file exists and is not a directory before we
// try using it to prevent further errors.
func FileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// Linux returns true if we're running on Linux
func Linux() bool {
	return runtime.GOOS == "linux"
}

// SetupCloseHandler creates a 'listener' on a new goroutine which will notify the
// program if it receives an interrupt from the OS
func SetupCloseHandler() chan os.Signal {
	// buffered, as signal.Notify requires: it never blocks to deliver, so a
	// signal arriving while nobody is receiving would otherwise be dropped
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	return c
}

// GetCurrentWindowProcessNames returns the process names (including extension, if applicable)
// of the current foreground window. This includes child processes belonging to the window.
// This is currently only implemented for Windows
func GetCurrentWindowProcessNames() ([]string, error) {
	return getCurrentWindowProcessNames()
}

// OpenExternal spawns a detached window with the provided command and argument.
// It returns once the process has started, not when it exits - the tray calls
// this from its event loop, which mustn't stay blocked while an editor is open
func OpenExternal(logger *zap.SugaredLogger, cmd string, arg string) error {

	// use cmd for windows, run the opener directly on linux
	execCommandArgs := []string{"cmd.exe", "/C", "start", "/b", cmd, arg}
	if Linux() {
		execCommandArgs = []string{cmd, arg}
	}

	command := exec.Command(execCommandArgs[0], execCommandArgs[1:]...)

	if err := command.Start(); err != nil {
		logger.Warnw("Failed to spawn detached process",
			"command", cmd,
			"argument", arg,
			"error", err)

		return fmt.Errorf("spawn detached proc: %w", err)
	}

	// reap it whenever it exits
	go command.Wait()

	return nil
}

// RunShellCommand runs the given command line through the platform's shell and
// waits for it to finish, returning its combined output for logging purposes.
// If ctx ends first the command is killed, along with anything it started.
//
// Unlike OpenExternal this takes a whole command line rather than a command and
// a single argument, because it's fed straight from the user's config - letting
// the shell do the word splitting is the whole point.
func RunShellCommand(ctx context.Context, commandLine string) ([]byte, error) {
	execCommandArgs := []string{"cmd.exe", "/C", commandLine}
	if Linux() {
		execCommandArgs = []string{"/bin/sh", "-c", commandLine}
	}

	command := exec.CommandContext(ctx, execCommandArgs[0], execCommandArgs[1:]...)
	killProcessTreeOnCancel(command)

	// a child that's been backgrounded can hold our output pipe open after the
	// shell exits, which would keep Wait from returning. give up on the pipe
	// shortly after the process itself is done or killed
	command.WaitDelay = time.Second

	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("run shell command: %w", err)
	}

	return output, nil
}

// NormalizeScalar "trims" the given float32 to 2 points of precision (e.g. 0.15442 -> 0.15)
// This is used both for windows core audio volume levels and for cleaning up slider level values from serial
func NormalizeScalar(v float32) float32 {
	return float32(math.Floor(float64(v)*100) / 100.0)
}

// SignificantlyDifferent returns true if there's a significant enough volume difference between two given values
func SignificantlyDifferent(old float32, new float32, noiseReductionLevel string) bool {

	const (
		noiseReductionHigh = "high"
		noiseReductionLow  = "low"
	)

	// this threshold is solely responsible for dealing with hardware interference when
	// sliders are producing noisy values. this value should be a median value between two
	// round percent values. for instance, 0.025 means volume can move at 3% increments
	var significantDifferenceThreshold float64

	// choose our noise reduction level based on the config-provided value
	switch noiseReductionLevel {
	case noiseReductionHigh:
		significantDifferenceThreshold = 0.035
		break
	case noiseReductionLow:
		significantDifferenceThreshold = 0.015
		break
	default:
		significantDifferenceThreshold = 0.025
		break
	}

	if math.Abs(float64(old-new)) >= significantDifferenceThreshold {
		return true
	}

	// special behavior is needed around the edges of 0.0 and 1.0 - this makes it snap (just a tiny bit) to them
	if (almostEquals(new, 1.0) && old != 1.0) || (almostEquals(new, 0.0) && old != 0.0) {
		return true
	}

	// values are close enough to not warrant any action
	return false
}

// a helper to make sure volume snaps correctly to 0 and 100, where appropriate
func almostEquals(a float32, b float32) bool {
	return math.Abs(float64(a-b)) < 0.000001
}
