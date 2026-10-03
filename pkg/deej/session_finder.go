package deej

// SessionFinder represents an entity that can find all current audio sessions
type SessionFinder interface {
	GetAllSessions() ([]Session, error)

	// NewSessionChannel returns a channel that receives a signal whenever a new
	// audio session is detected. Callers should refresh sessions on each signal.
	NewSessionChannel() <-chan struct{}

	// ConnectionLost returns a channel that's closed if the connection to the
	// audio server dies for good. A nil channel means the finder can't tell.
	ConnectionLost() <-chan struct{}

	Release() error
}
