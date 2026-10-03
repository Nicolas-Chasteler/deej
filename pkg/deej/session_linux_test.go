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
