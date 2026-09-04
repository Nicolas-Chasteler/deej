package deej

import (
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/omriharel/deej/pkg/deej/util"
)

// buttonRunner listens for button presses coming off the serial line and runs
// the shell command each button is mapped to.
type buttonRunner struct {
	deej   *Deej
	logger *zap.SugaredLogger
}

// commandTimeout bounds how long a button's command is allowed to run before we
// stop waiting on it and log a warning. The command isn't killed - this only
// stops a hung command from occupying its goroutine forever without anyone
// noticing. Anything long-running belongs behind a launcher anyway.
const commandTimeout = 10 * time.Second

func newButtonRunner(deej *Deej, logger *zap.SugaredLogger) *buttonRunner {
	logger = logger.Named("buttons")

	logger.Debug("Created button runner instance")

	return &buttonRunner{
		deej:   deej,
		logger: logger,
	}
}

func (r *buttonRunner) initialize() {
	pressChannel := r.deej.serial.SubscribeToButtonPressEvents()

	go func() {
		for event := range pressChannel {

			// hand off immediately - this loop must never be the thing that's
			// busy when the next press arrives
			go r.runCommand(event)
		}
	}()

	r.logger.Debug("Listening for button presses")
}

func (r *buttonRunner) runCommand(event ButtonPressEvent) {
	r.logger.Infow("Running button command", "button", event.ButtonID, "command", event.Command)

	done := make(chan struct{})

	go func() {
		defer close(done)

		output, err := util.RunShellCommand(event.Command)
		if err != nil {
			r.logger.Warnw("Button command failed",
				"button", event.ButtonID,
				"command", event.Command,
				"error", err,
				"output", strings.TrimSpace(string(output)))

			return
		}

		if r.deej.Verbose() {
			r.logger.Debugw("Button command finished",
				"button", event.ButtonID,
				"output", strings.TrimSpace(string(output)))
		}
	}()

	select {
	case <-done:
	case <-time.After(commandTimeout):
		r.logger.Warnw("Button command is still running, no longer waiting on it",
			"button", event.ButtonID,
			"command", event.Command,
			"timeout", commandTimeout)
	}
}
