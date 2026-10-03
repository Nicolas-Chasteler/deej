package deej

import (
	"strings"

	"go.uber.org/zap"
)

// Session represents a single addressable audio session
type Session interface {
	GetVolume() float32
	SetVolume(v float32) error

	// TODO: future mute support
	// GetMute() bool
	// SetMute(m bool) error

	Key() string
	Release()
}

// staleSession is implemented by sessions that can tell when the thing they
// point at has gone away: an app's stream that ended, or a default device that
// currently doesn't exist. It's not part of Session because the Windows
// sessions have no way to report it.
type staleSession interface {
	Stale() bool
}

const (

	// ideally these would share a common ground in baseSession
	// but it will not call the child GetVolume correctly :/
	sessionCreationLogMessage = "Created audio session instance"

	// format this with s.humanReadableDesc. deliberately no volume: reading it
	// is a request to the audio server, and String() runs whenever a session
	// is logged
	sessionStringFormat = "<session: %s>"
)

type baseSession struct {
	logger *zap.SugaredLogger
	system bool
	master bool

	// used by Key(), needs to be set by child
	name string

	// used by String(), needs to be set by child
	humanReadableDesc string
}

func (s *baseSession) Key() string {
	if s.system {
		return systemSessionName
	}

	if s.master {
		return strings.ToLower(s.name) // could be master or mic, or any device's friendly name
	}

	return strings.ToLower(s.name)
}
