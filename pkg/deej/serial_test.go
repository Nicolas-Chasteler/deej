package deej

import (
	"testing"

	"go.uber.org/zap"
)

// newTestSerialIO builds a SerialIO wired to a config with the given button
// mapping, without touching an actual serial port. handleLine only needs the
// config and a logger, so this is enough to drive the whole parse path.
func newTestSerialIO(buttonCommands map[int]string) (*SerialIO, chan ButtonPressEvent) {
	buttons := newButtonMap()
	for idx, command := range buttonCommands {
		buttons.set(idx, command)
	}

	config := &CanonicalConfig{
		values: configValues{
			SliderMapping:       newSliderMap(),
			ButtonMapping:       buttons,
			NoiseReductionLevel: "default",
		},
	}

	sio := &SerialIO{
		deej:                 &Deej{config: config},
		logger:               zap.NewNop().Sugar(),
		sliderMoveConsumers:  []chan SliderMoveEvent{},
		buttonPressConsumers: []chan ButtonPressEvent{},
	}

	return sio, sio.SubscribeToButtonPressEvents()
}

// settle feeds the same line until the channel count is confirmed, the way the
// first lines of a real connection would
func settle(sio *SerialIO, line string) {
	for i := 0; i < channelCountConfirmations; i++ {
		sio.handleLine(sio.logger, line)
	}
}

func drainPresses(ch chan ButtonPressEvent) []ButtonPressEvent {
	events := []ButtonPressEvent{}

	for {
		select {
		case event := <-ch:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestHandleLineFiresButtonOnPressEdgeOnly(t *testing.T) {
	sio, presses := newTestSerialIO(map[int]string{5: "playerctl -p spotify play-pause"})

	// released
	settle(sio, "500|500|500|500|500|0\r\n")
	if got := drainPresses(presses); len(got) != 0 {
		t.Fatalf("released button fired %d presses, want 0", len(got))
	}

	// pressed - one event
	sio.handleLine(sio.logger, "500|500|500|500|500|1023\r\n")

	got := drainPresses(presses)
	if len(got) != 1 {
		t.Fatalf("press fired %d events, want 1", len(got))
	}
	if got[0].ButtonID != 5 {
		t.Errorf("got button %d, want 5", got[0].ButtonID)
	}
	if got[0].Command != "playerctl -p spotify play-pause" {
		t.Errorf("got command %q, want the full mapped command line", got[0].Command)
	}

	// still held - must not repeat, even though the arduino keeps sending
	for i := 0; i < 5; i++ {
		sio.handleLine(sio.logger, "500|500|500|500|500|1023\r\n")
	}
	if got := drainPresses(presses); len(got) != 0 {
		t.Fatalf("holding the button fired %d extra presses, want 0", len(got))
	}

	// released, then pressed again - fires once more
	sio.handleLine(sio.logger, "500|500|500|500|500|0\r\n")
	sio.handleLine(sio.logger, "500|500|500|500|500|1023\r\n")
	if got := drainPresses(presses); len(got) != 1 {
		t.Fatalf("second press fired %d events, want 1", len(got))
	}
}

func TestHandleLineHysteresisIgnoresMidRangeValues(t *testing.T) {
	sio, presses := newTestSerialIO(map[int]string{5: "true"})

	settle(sio, "500|500|500|500|500|0\r\n")

	// a value between the two thresholds is neither a press nor a release
	for _, rawValue := range []string{"400", "500", "600"} {
		sio.handleLine(sio.logger, "500|500|500|500|500|"+rawValue+"\r\n")
	}

	if got := drainPresses(presses); len(got) != 0 {
		t.Fatalf("mid-range values fired %d presses, want 0", len(got))
	}
}

func TestHandleLineKeepsButtonChannelOutOfSliderValues(t *testing.T) {
	sio, _ := newTestSerialIO(map[int]string{5: "true"})

	moves := sio.SubscribeToSliderMoveEvents()
	collected := []SliderMoveEvent{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range moves {
			collected = append(collected, event)
		}
	}()

	settle(sio, "0|1023|0|0|0|1023\r\n")
	close(moves)
	<-done

	for _, event := range collected {
		if event.SliderID == 5 {
			t.Errorf("button channel 5 emitted a slider move event: %+v", event)
		}
	}

	// the four real sliders plus channel 1 should all have reported
	if len(collected) != 5 {
		t.Errorf("got %d slider move events, want 5 (one per non-button channel)", len(collected))
	}

	// a button channel must never leave a negative value behind for the volume
	// watchdog to push at a session
	values := sio.CurrentSliderValues()
	if values[5] < 0 {
		t.Errorf("button channel 5 left slider value %v, want a non-negative placeholder", values[5])
	}
}

func TestHandleLineRejectsOutOfRangeValuesInAnyChannel(t *testing.T) {
	sio, presses := newTestSerialIO(map[int]string{5: "true"})
	settle(sio, "500|500|500|500|500|0\r\n")

	before := sio.CurrentSliderValues()

	// a separator lost in transit runs two values together. it must not reach
	// a slider as 488%, wherever in the line it lands
	sio.handleLine(sio.logger, "500|5000|500|500|1023\r\n")
	sio.handleLine(sio.logger, "500|500|500|500|500|1023|9999\r\n")

	after := sio.CurrentSliderValues()
	for idx := range before {
		if before[idx] != after[idx] {
			t.Errorf("slider %d changed from %v to %v on a malformed line", idx, before[idx], after[idx])
		}
	}

	if got := drainPresses(presses); len(got) != 0 {
		t.Fatalf("malformed lines fired %d presses, want 0", len(got))
	}
}

func TestHandleLineIgnoresOneOffChannelCountChange(t *testing.T) {
	sio, presses := newTestSerialIO(map[int]string{5: "true", 6: "true"})

	// button 6 held down
	settle(sio, "500|500|500|500|500|0|1023\r\n")
	drainPresses(presses)

	// one line short a channel would shift button 6's 1023 into button 5's
	// place and reset the held state. it has to be ignored instead
	sio.handleLine(sio.logger, "500|500|500|500|0|1023\r\n")
	sio.handleLine(sio.logger, "500|500|500|500|500|0|1023\r\n")

	if got := drainPresses(presses); len(got) != 0 {
		t.Fatalf("a single short line fired %d presses, want 0", len(got))
	}
}

func TestHandleLineAcceptsConfirmedChannelCountChange(t *testing.T) {
	sio, _ := newTestSerialIO(nil)
	settle(sio, "500|500|500\r\n")

	// a sketch that really changed keeps sending the new count
	settle(sio, "500|500|500|500\r\n")

	if got := len(sio.CurrentSliderValues()); got != 4 {
		t.Fatalf("got %d sliders after a sustained change, want 4", got)
	}
}

func TestConfigReloadResendsSlidersWithoutRefiringHeldButtons(t *testing.T) {
	sio, presses := newTestSerialIO(map[int]string{5: "true"})

	moves := sio.SubscribeToSliderMoveEvents()
	moveCount := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range moves {
			moveCount++
		}
	}()

	// button held
	settle(sio, "500|500|500|500|500|1023\r\n")
	drainPresses(presses)

	sio.resendSliders.Store(true)
	sio.handleLine(sio.logger, "500|500|500|500|500|1023\r\n")

	close(moves)
	<-done

	if got := drainPresses(presses); len(got) != 0 {
		t.Errorf("a held button fired %d presses across a config reload, want 0", len(got))
	}

	// five sliders on the first line, and all five again after the reload
	if moveCount != 10 {
		t.Errorf("got %d slider moves, want 10", moveCount)
	}
}
