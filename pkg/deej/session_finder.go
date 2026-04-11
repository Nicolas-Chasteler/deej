package deej

// SessionFinder represents an entity that can find all current audio sessions
type SessionFinder interface {
	GetAllSessions() ([]Session, error)

	// NewSessionChannel returns a channel that receives a signal whenever a new
	// audio session is detected. Callers should refresh sessions on each signal.
	NewSessionChannel() <-chan struct{}

	Release() error
}
