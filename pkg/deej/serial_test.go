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
		SliderMapping:       newSliderMap(),
		ButtonMapping:       buttons,
		NoiseReductionLevel: "default",
	}

	sio := &SerialIO{
		deej:                 &Deej{config: config},
		logger:               zap.NewNop().Sugar(),
		sliderMoveConsumers:  []chan SliderMoveEvent{},
		buttonPressConsumers: []chan ButtonPressEvent{},
	}

	return sio, sio.SubscribeToButtonPressEvents()
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
	sio.handleLine(sio.logger, "500|500|500|500|500|0\r\n")
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

	sio.handleLine(sio.logger, "0|1023|0|0|0|1023\r\n")
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
