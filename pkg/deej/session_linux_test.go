package deej

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jfreymuth/pulse/proto"
	"go.uber.org/zap"
)

func TestSessionStringDoesNotQueryVolume(t *testing.T) {
	s := newFakeSession("firefox")
	s.humanReadableDesc = "firefox"

	// String() runs whenever a session is logged, so it mustn't do a request
	if got := (&paSession{baseSession: s.baseSession}).String(); !strings.Contains(got, "firefox") {
		t.Errorf("got %q", got)
	}
}

func TestConnectionFailedTellsServerErrorsFromTransportErrors(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{proto.ErrNoSuchEntity, false},
		{fmt.Errorf("wrapped: %w", proto.ErrInvalidArgument), false},
		{context.DeadlineExceeded, false},
		{io.EOF, true},
		{syscall.ECONNRESET, true},
	}

	for _, c := range cases {
		if got := connectionFailed(c.err); got != c.want {
			t.Errorf("connectionFailed(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestPAClientReportsDroppedConnection(t *testing.T) {
	ours, theirs := net.Pipe()

	client := &proto.Client{}
	client.SetTimeout(time.Second)
	client.Open(ours)

	c := newPAClient(client, zap.NewNop().Sugar())

	// the server going away. the read loop's error then comes back from every
	// request, and the first one to see it reports the connection lost
	theirs.Close()

	err := c.Request(&proto.GetSinkInfo{SinkIndex: proto.Undefined}, &proto.GetSinkInfoReply{})
	if err == nil {
		t.Fatal("request on a closed connection succeeded")
	}

	select {
	case <-c.lost:
	case <-time.After(time.Second):
		t.Fatalf("connection loss not reported (request error: %v)", err)
	}
}

func TestDefaultDeviceTrackerSettles(t *testing.T) {
	var tracker defaultDeviceTracker

	// whatever is default when deej starts is enforced straight away
	tracker.observe("bluez_output")
	if tracker.settling() {
		t.Fatal("the first device seen is settling, want it enforced immediately")
	}

	// a brief switch - deej-mic-toggle, or EasyEffects restarting - must not be
	// enforced, or the slider value lands on a device with its own volume
	tracker.observe("alsa_output")
	if !tracker.settling() {
		t.Fatal("a device that just became default isn't settling")
	}

	tracker.observe("alsa_output")
	if !tracker.settling() {
		t.Fatal("re-observing the same device ended the settle window early")
	}

	tracker.changedAt = time.Now().Add(-defaultDeviceSettleTime)
	if tracker.settling() {
		t.Fatal("still settling after defaultDeviceSettleTime")
	}
}
