package deej

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jacobsa/go-serial/serial"
	"go.uber.org/zap"

	"github.com/omriharel/deej/pkg/deej/util"
)

// SerialIO provides a deej-aware abstraction layer to managing serial I/O
type SerialIO struct {
	deej   *Deej
	logger *zap.SugaredLogger

	// closed by Stop. the connection goroutine owns the port and everything
	// below that isn't explicitly locked, so shutting down is just this
	stopChannel chan struct{}
	stopOnce    sync.Once
	startOnce   sync.Once

	// signalled when the config reloads. buffered so the config watcher never
	// waits on a connection goroutine that's busy delivering a line
	configReloaded chan struct{}

	// set on config reload so the next line re-emits every slider. it's an
	// atomic flag rather than a reset of the slider count so that buttons keep
	// their held state - re-zeroing them made any held button fire again
	resendSliders atomic.Bool

	// channel count the arduino has settled on, plus a candidate replacement
	// that has to repeat before we believe it. owned by the connection goroutine
	lastKnownNumSliders int
	pendingNumSliders   int
	pendingCount        int

	// read by the volume watchdog and new-session goroutines via
	// CurrentSliderValues while the connection goroutine writes it
	valuesLock                 sync.Mutex
	currentSliderPercentValues []float32

	// currentButtonStates tracks whether each button channel is currently held
	// down, so we can fire only on the press edge. indexed like the channels
	// themselves; entries for slider channels are meaningless
	currentButtonStates []bool

	// set up before Start and never changed afterwards
	sliderMoveConsumers  []chan SliderMoveEvent
	buttonPressConsumers []chan ButtonPressEvent
}

// SliderMoveEvent represents a single slider move captured by deej
type SliderMoveEvent struct {
	SliderID     int
	PercentValue float32
}

// ButtonPressEvent represents a single button press captured by deej
type ButtonPressEvent struct {
	ButtonID int
	Command  string
}

// buttons report a raw analog value that sits at one rail or the other. we read
// them with hysteresis so a channel resting near the middle (a floating pin, or
// a slider someone mapped as a button by mistake) can't oscillate: it takes a
// clear high to register a press and a clear low to release it again.
// contact bounce is handled on the arduino side, where the timing authority lives
const (
	buttonPressedThreshold  = 700
	buttonReleasedThreshold = 300
)

const (

	// how long to wait between attempts to open the port while it's missing or
	// busy - the box being unplugged is a normal state, not an error to exit on
	reconnectInterval = 2 * time.Second

	// a different channel count has to show up on this many consecutive lines
	// before we accept it. a single line that lost a separator in transit would
	// otherwise shift every later channel down one, sending a button's 1023 to a
	// slider and making a held button look newly pressed
	channelCountConfirmations = 3

	// the largest value analogRead can produce
	maxRawValue = 1023
)

var expectedLinePattern = regexp.MustCompile(`^\d{1,4}(\|\d{1,4})*\r\n$`)

// NewSerialIO creates a SerialIO instance that uses the provided deej
// instance's connection info to establish communications with the arduino chip
func NewSerialIO(deej *Deej, logger *zap.SugaredLogger) (*SerialIO, error) {
	logger = logger.Named("serial")

	sio := &SerialIO{
		deej:                 deej,
		logger:               logger,
		stopChannel:          make(chan struct{}),
		configReloaded:       make(chan struct{}, 1),
		sliderMoveConsumers:  []chan SliderMoveEvent{},
		buttonPressConsumers: []chan ButtonPressEvent{},
	}

	logger.Debug("Created serial i/o instance")

	// respond to config changes
	sio.setupOnConfigReload()

	return sio, nil
}

// Start begins connecting to the arduino in the background. It keeps trying
// until Stop is called: a missing or busy port is retried rather than fatal,
// and a connection that drops (the box unplugged) is re-opened when the port
// comes back.
func (sio *SerialIO) Start() {
	sio.startOnce.Do(func() {
		go sio.connectionLoop()
	})
}

// Stop shuts down the serial connection, if there is one, and stops retrying
func (sio *SerialIO) Stop() {
	sio.stopOnce.Do(func() {
		sio.logger.Debug("Shutting down serial connection")
		close(sio.stopChannel)
	})
}

// CurrentSliderValues returns a copy of the last known slider percent values.
// Returns nil if no values have been read yet. Entries are -1 for sliders that
// haven't reported since the last reset.
func (sio *SerialIO) CurrentSliderValues() []float32 {
	sio.valuesLock.Lock()
	defer sio.valuesLock.Unlock()

	if sio.currentSliderPercentValues == nil {
		return nil
	}

	values := make([]float32, len(sio.currentSliderPercentValues))
	copy(values, sio.currentSliderPercentValues)

	return values
}

// SubscribeToSliderMoveEvents returns an unbuffered channel that receives
// a sliderMoveEvent struct every time a slider moves
func (sio *SerialIO) SubscribeToSliderMoveEvents() chan SliderMoveEvent {
	ch := make(chan SliderMoveEvent)
	sio.sliderMoveConsumers = append(sio.sliderMoveConsumers, ch)

	return ch
}

// SubscribeToButtonPressEvents returns a buffered channel that receives a
// ButtonPressEvent every time a mapped button is pressed.
//
// Unlike the slider channels this one is buffered on purpose: its consumer
// shells out to run a command, and a blocking send here would stall the serial
// read loop and freeze every slider until the command returned.
func (sio *SerialIO) SubscribeToButtonPressEvents() chan ButtonPressEvent {
	const buttonEventBufferSize = 16

	ch := make(chan ButtonPressEvent, buttonEventBufferSize)
	sio.buttonPressConsumers = append(sio.buttonPressConsumers, ch)

	return ch
}

func (sio *SerialIO) setupOnConfigReload() {
	configReloadedChannel := sio.deej.config.SubscribeToChanges()

	go func() {
		for range configReloadedChannel {

			// re-send every slider on the next line, so process volumes are
			// re-applied against whatever the new mapping says
			sio.resendSliders.Store(true)

			// let the connection goroutine check whether the port or baud rate
			// changed. non-blocking: one pending signal is as good as several
			select {
			case sio.configReloaded <- struct{}{}:
			default:
			}
		}
	}()
}

func connOptionsFromConfig(info connectionInfo) serial.OpenOptions {

	// set minimum read size according to platform (0 for windows, 1 for linux)
	// this prevents a rare bug on windows where serial reads get congested,
	// resulting in significant lag
	minimumReadSize := 0
	if util.Linux() {
		minimumReadSize = 1
	}

	return serial.OpenOptions{
		PortName:        info.COMPort,
		BaudRate:        uint(info.BaudRate),
		DataBits:        8,
		StopBits:        1,
		MinimumReadSize: uint(minimumReadSize),
	}
}

// connectionLoop owns the serial port for deej's whole lifetime: open it, read
// until it fails or the config points somewhere else, close it, repeat.
func (sio *SerialIO) connectionLoop() {
	failedAttempts := 0

	for {
		connOptions := connOptionsFromConfig(sio.deej.config.current().ConnectionInfo)

		sio.logger.Debugw("Attempting serial connection",
			"comPort", connOptions.PortName,
			"baudRate", connOptions.BaudRate,
			"minReadSize", connOptions.MinimumReadSize)

		conn, err := serial.Open(connOptions)
		if err != nil {

			// say so once per outage, not once per retry
			if failedAttempts == 0 {
				sio.logger.Warnw("Failed to open serial connection, will keep retrying",
					"comPort", connOptions.PortName,
					"error", err,
					"retryInterval", reconnectInterval)

				sio.deej.notifier.Notify(fmt.Sprintf("Can't connect to %s", connOptions.PortName),
					serialOpenErrorHint(err))
			}

			failedAttempts++

			if !sio.waitBeforeRetry() {
				return
			}

			continue
		}

		if failedAttempts > 0 {
			sio.logger.Infow("Serial port is available again", "failedAttempts", failedAttempts)
		}

		failedAttempts = 0

		if stopped := sio.serve(conn, connOptions); stopped {
			return
		}
	}
}

func serialOpenErrorHint(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "The port is busy - close any serial monitor or other deej instance. Retrying in the background."
	case errors.Is(err, os.ErrNotExist):
		return "The port doesn't exist - is the box plugged in, and is com_port right? Retrying in the background."
	default:
		return fmt.Sprintf("%v. Retrying in the background.", err)
	}
}

// waitBeforeRetry sleeps until the next connection attempt is due. a config
// reload cuts the wait short, since it may have fixed the port. returns false
// if we were stopped meanwhile
func (sio *SerialIO) waitBeforeRetry() bool {
	select {
	case <-sio.stopChannel:
		return false
	case <-sio.configReloaded:
		return true
	case <-time.After(reconnectInterval):
		return true
	}
}

// serve reads lines from an open connection until it fails, the connection
// parameters change, or we're stopped (in which case it returns true). the
// connection is always closed on return
func (sio *SerialIO) serve(conn io.ReadWriteCloser, connOptions serial.OpenOptions) bool {
	logger := sio.logger.Named(strings.ToLower(connOptions.PortName))
	logger.Infow("Connected", "baudRate", connOptions.BaudRate)

	// a fresh connection starts from scratch: the channel count has to be
	// re-established and every slider is re-sent once it is
	sio.lastKnownNumSliders = 0
	sio.pendingCount = 0

	lines := make(chan string)
	readErrors := make(chan error, 1)
	done := make(chan struct{})

	// deferred in this order so the port is closed first, which unblocks a
	// reader stuck in ReadString, and then the reader is told to give up on a
	// line it's still trying to hand over
	defer close(done)
	defer func() {
		if err := conn.Close(); err != nil {
			logger.Warnw("Failed to close serial connection", "error", err)
		} else {
			logger.Debug("Serial connection closed")
		}
	}()

	go func() {
		reader := bufio.NewReader(conn)

		// the first line after opening the port usually starts mid-transmission,
		// and a truncated first value still parses - "23" from "1023" - so it
		// can't be told apart from a real reading. drop it unconditionally
		firstLine := true

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				readErrors <- err
				return
			}

			if firstLine {
				firstLine = false
				continue
			}

			if sio.deej.Verbose() {
				logger.Debugw("Read new line", "line", line)
			}

			select {
			case lines <- line:
			case <-done:
				return
			}
		}
	}()

	for {
		select {
		case <-sio.stopChannel:
			return true

		case <-sio.configReloaded:
			if connOptionsFromConfig(sio.deej.config.current().ConnectionInfo) != connOptions {
				logger.Info("Detected change in connection parameters, reconnecting")
				return false
			}

		case err := <-readErrors:
			logger.Warnw("Serial connection lost, reconnecting when the port comes back", "error", err)
			return false

		case line := <-lines:
			sio.handleLine(logger, line)
		}
	}
}

func (sio *SerialIO) handleLine(logger *zap.SugaredLogger, line string) {

	// this function receives an unsanitized line which is guaranteed to end with LF,
	// but most lines will end with CRLF. it may also have garbage instead of
	// deej-formatted values, so we must check for that! just ignore bad ones
	if !expectedLinePattern.MatchString(line) {
		return
	}

	// trim the suffix
	line = strings.TrimSuffix(line, "\r\n")

	// split on pipe (|), this gives a slice of numerical strings between "0" and "1023"
	splitLine := strings.Split(line, "|")
	numSliders := len(splitLine)

	// convert string values to integers ("1023" -> 1023). the pattern allows four
	// digits, so a line with two values run together ("5000") gets through it -
	// reject the whole line rather than hand a slider 488%
	rawValues := make([]int, numSliders)
	for idx, stringValue := range splitLine {
		number, _ := strconv.Atoi(stringValue)

		if number > maxRawValue {
			logger.Debugw("Got malformed line from serial, ignoring", "line", line)
			return
		}

		rawValues[idx] = number
	}

	if !sio.confirmChannelCount(logger, numSliders) {
		return
	}

	config := sio.deej.config.current()

	moveEvents := []SliderMoveEvent{}
	pressEvents := []ButtonPressEvent{}

	sio.valuesLock.Lock()

	// a config reload asks for every slider to be re-sent. buttons keep their
	// state: re-zeroing them would make any held button fire again
	if sio.resendSliders.Swap(false) {
		for idx := range sio.currentSliderPercentValues {
			sio.currentSliderPercentValues[idx] = -1.0
		}
	}

	for sliderIdx, number := range rawValues {

		// channels mapped as buttons never reach the volume path - they carry a
		// pressed/released state, not a level
		if command, isButton := config.ButtonMapping.get(sliderIdx); isButton {
			if pressEvent, pressed := sio.handleButtonValue(logger, sliderIdx, number, command); pressed {
				pressEvents = append(pressEvents, pressEvent)
			}

			continue
		}

		// map the value from raw to a "dirty" float between 0 and 1 (e.g. 0.15451...)
		dirtyFloat := float32(number) / maxRawValue

		// normalize it to an actual volume scalar between 0.0 and 1.0 with 2 points of precision
		normalizedScalar := util.NormalizeScalar(dirtyFloat)

		// if sliders are inverted, take the complement of 1.0
		if config.InvertSliders {
			normalizedScalar = 1 - normalizedScalar
		}

		// check if it changes the desired state (could just be a jumpy raw slider value)
		if util.SignificantlyDifferent(sio.currentSliderPercentValues[sliderIdx], normalizedScalar, config.NoiseReductionLevel) {

			// if it does, update the saved value and create a move event
			sio.currentSliderPercentValues[sliderIdx] = normalizedScalar

			moveEvents = append(moveEvents, SliderMoveEvent{
				SliderID:     sliderIdx,
				PercentValue: normalizedScalar,
			})

			if sio.deej.Verbose() {
				logger.Debugw("Slider moved", "event", moveEvents[len(moveEvents)-1])
			}
		}
	}

	sio.valuesLock.Unlock()

	// button presses go first. their consumers are buffered, but a full buffer
	// still can't be allowed to block the read loop - drop instead, and say so.
	// slider consumers are unbuffered and may be busy re-acquiring sessions, so
	// delivering presses after them would delay a press for no reason
	for _, consumer := range sio.buttonPressConsumers {
		for _, pressEvent := range pressEvents {
			select {
			case consumer <- pressEvent:
			default:
				logger.Warnw("Button press consumer is backed up, dropping press",
					"button", pressEvent.ButtonID)
			}
		}
	}

	// deliver move events if there are any, towards all potential consumers
	for _, consumer := range sio.sliderMoveConsumers {
		for _, moveEvent := range moveEvents {
			consumer <- moveEvent
		}
	}
}

// confirmChannelCount decides whether a line with numSliders channels can be
// used. A count that differs from the one we've settled on has to repeat on
// channelCountConfirmations consecutive lines first - that covers both the
// first lines of a connection and a sketch that genuinely changed, while a one-
// off line that lost or gained a separator is simply dropped.
func (sio *SerialIO) confirmChannelCount(logger *zap.SugaredLogger, numSliders int) bool {
	if numSliders == sio.lastKnownNumSliders {
		sio.pendingCount = 0
		return true
	}

	if numSliders == sio.pendingNumSliders && sio.pendingCount > 0 {
		sio.pendingCount++
	} else {
		sio.pendingNumSliders = numSliders
		sio.pendingCount = 1
	}

	if sio.pendingCount < channelCountConfirmations {
		return false
	}

	logger.Infow("Detected sliders", "amount", numSliders)

	sio.lastKnownNumSliders = numSliders
	sio.pendingCount = 0

	sio.valuesLock.Lock()
	sio.currentSliderPercentValues = make([]float32, numSliders)

	// reset everything to be an impossible value to force the slider move event later
	for idx := range sio.currentSliderPercentValues {
		sio.currentSliderPercentValues[idx] = -1.0
	}
	sio.valuesLock.Unlock()

	sio.currentButtonStates = make([]bool, numSliders)

	return true
}

// handleButtonValue folds a raw analog reading into the button's held/released
// state, returning a press event on the rising edge only. holding the button
// down fires once, not once per line.
func (sio *SerialIO) handleButtonValue(
	logger *zap.SugaredLogger,
	buttonIdx int,
	rawValue int,
	command string,
) (ButtonPressEvent, bool) {

	wasPressed := sio.currentButtonStates[buttonIdx]

	// keep the slider array at a sane value for this channel, so a channel
	// mapped as both a button and a slider can't push a negative volume
	sio.currentSliderPercentValues[buttonIdx] = 0

	switch {
	case !wasPressed && rawValue >= buttonPressedThreshold:
		sio.currentButtonStates[buttonIdx] = true

		if sio.deej.Verbose() {
			logger.Debugw("Button pressed", "button", buttonIdx, "command", command)
		}

		return ButtonPressEvent{ButtonID: buttonIdx, Command: command}, true

	case wasPressed && rawValue <= buttonReleasedThreshold:
		sio.currentButtonStates[buttonIdx] = false

		if sio.deej.Verbose() {
			logger.Debugw("Button released", "button", buttonIdx)
		}
	}

	return ButtonPressEvent{}, false
}
