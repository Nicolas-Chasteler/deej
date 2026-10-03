package deej

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/omriharel/deej/pkg/deej/util"
)

// buttonRunner listens for button presses coming off the serial line and runs
// the shell command each button is mapped to.
type buttonRunner struct {
	deej   *Deej
	logger *zap.SugaredLogger

	// buttons whose command is still running. a second press of the same button
	// meanwhile is dropped: two deej-mic-toggle runs at once would each read the
	// state the other is halfway through changing
	runningLock sync.Mutex
	running     map[int]bool
}

// commandTimeout bounds how long a button's command may run before it's killed,
// along with anything it started. Button commands are meant to be quick
// actions; anything long-running belongs behind a launcher.
const commandTimeout = 10 * time.Second

func newButtonRunner(deej *Deej, logger *zap.SugaredLogger) *buttonRunner {
	logger = logger.Named("buttons")

	logger.Debug("Created button runner instance")

	return &buttonRunner{
		deej:    deej,
		logger:  logger,
		running: make(map[int]bool),
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
	if !r.claim(event.ButtonID) {
		r.logger.Infow("Button's previous command is still running, ignoring press",
			"button", event.ButtonID,
			"command", event.Command)

		return
	}

	defer r.release(event.ButtonID)

	r.logger.Infow("Running button command", "button", event.ButtonID, "command", event.Command)

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	output, err := util.RunShellCommand(ctx, event.Command)

	if ctx.Err() == context.DeadlineExceeded {
		r.logger.Warnw("Button command timed out and was killed",
			"button", event.ButtonID,
			"command", event.Command,
			"timeout", commandTimeout)

		return
	}

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
}

func (r *buttonRunner) claim(buttonID int) bool {
	r.runningLock.Lock()
	defer r.runningLock.Unlock()

	if r.running[buttonID] {
		return false
	}

	r.running[buttonID] = true

	return true
}

func (r *buttonRunner) release(buttonID int) {
	r.runningLock.Lock()
	defer r.runningLock.Unlock()

	delete(r.running, buttonID)
}
