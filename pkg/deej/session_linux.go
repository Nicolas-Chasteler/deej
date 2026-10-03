package deej

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/jfreymuth/pulse/proto"
)

// normal PulseAudio volume (100%)
const maxVolume = 0x10000

// PulseAudio resolves these names to whatever the default device is at the time
// of each request, so the master and mic sessions follow a Bluetooth headset
// connecting or the default being switched without needing a refresh
const (
	defaultSinkName   = "@DEFAULT_SINK@"
	defaultSourceName = "@DEFAULT_SOURCE@"
)

var errNoSuchProcess = errors.New("No such process")

// paClient wraps the PulseAudio protocol client shared by the session finder
// and every session, so that whichever of them first sees the connection die
// can report it. Once the client's read loop fails it stores that error and
// returns it from every later request - the connection never comes back.
type paClient struct {
	*proto.Client

	lost     chan struct{}
	lostOnce sync.Once
	logger   *zap.SugaredLogger
}

func newPAClient(client *proto.Client, logger *zap.SugaredLogger) *paClient {
	return &paClient{
		Client: client,
		lost:   make(chan struct{}),
		logger: logger,
	}
}

// Request forwards to the underlying client, watching for connection failure
func (c *paClient) Request(req proto.RequestArgs, rpl proto.Reply) error {
	err := c.Client.Request(req, rpl)
	if err != nil && connectionFailed(err) {
		c.markLost(err)
	}

	return err
}

func (c *paClient) markLost(err error) {
	c.lostOnce.Do(func() {
		c.logger.Errorw("PulseAudio connection lost", "error", err)
		close(c.lost)
	})
}

// connectionFailed tells a dead connection apart from an ordinary error reply.
// A proto.Error means the server answered (no such entity, invalid argument),
// and a timeout means it's slow; anything else is the transport failing.
func connectionFailed(err error) bool {
	var serverError proto.Error
	if errors.As(err, &serverError) {
		return false
	}

	return !errors.Is(err, context.DeadlineExceeded)
}

type paSession struct {
	baseSession

	processName string

	client *paClient

	sinkInputIndex    uint32
	sinkInputChannels byte

	// set once PulseAudio stops recognising our sink input index, which means
	// the app closed or simply stopped playing. The session object is dead from
	// that point on and only a refresh of the map can replace it. Written by
	// whichever goroutine touched the session last, hence atomic.
	stale atomic.Bool
}

// masterSession controls the default output (master) or input (mic) device.
// It addresses the device by PulseAudio's @DEFAULT_SINK@/@DEFAULT_SOURCE@
// names rather than by index, so it always means "whatever is default now".
type masterSession struct {
	baseSession

	client *paClient

	isOutput bool

	// set while the default device can't be read - none exists, say, because
	// the only output was a headset that just disconnected. Used to log the
	// transition once instead of on every watchdog tick, and to stop the
	// watchdog writing a volume to a device that isn't there.
	unavailable atomic.Bool
}

func newPASession(
	logger *zap.SugaredLogger,
	client *paClient,
	sinkInputIndex uint32,
	sinkInputChannels byte,
	processName string,
) *paSession {

	s := &paSession{
		client:            client,
		sinkInputIndex:    sinkInputIndex,
		sinkInputChannels: sinkInputChannels,
	}

	s.processName = processName
	s.name = processName
	s.humanReadableDesc = processName

	// use a self-identifying session name e.g. deej.sessions.chrome
	s.logger = logger.Named(s.Key())
	s.logger.Debugw(sessionCreationLogMessage, "session", s)

	return s
}

func newMasterSession(
	logger *zap.SugaredLogger,
	client *paClient,
	isOutput bool,
) *masterSession {

	s := &masterSession{
		client:   client,
		isOutput: isOutput,
	}

	var key string

	if isOutput {
		key = masterSessionName
	} else {
		key = inputSessionName
	}

	s.logger = logger.Named(key)
	s.master = true
	s.name = key
	s.humanReadableDesc = key

	s.logger.Debugw(sessionCreationLogMessage, "session", s)

	return s
}

func (s *paSession) GetVolume() float32 {
	request := proto.GetSinkInputInfo{
		SinkInputIndex: s.sinkInputIndex,
	}
	reply := proto.GetSinkInputInfoReply{}

	if err := s.client.Request(&request, &reply); err != nil {

		// An app that closed or stopped playing takes its sink input with it,
		// and this is the first thing to notice. That's an ordinary lifecycle
		// event rather than a fault, so it's logged at debug - at warn, with the
		// volume watchdog polling twice a second, one closed app buries every
		// real message in the log.
		s.logger.Debugw("Sink input is gone, marking session stale", "error", err)
		s.stale.Store(true)

		// Falling through here was the actual bug. parseChannelVolumes on an
		// empty reply returns 0, so a dead session reported "volume 0", the
		// watchdog saw that as drift against the slider and tried to correct it,
		// forever.
		return 0
	}

	s.stale.Store(false)

	return parseChannelVolumes(reply.ChannelVolumes)
}

// Stale reports that this session's sink input no longer exists, so the session
// map is holding something dead and should re-acquire.
func (s *paSession) Stale() bool {
	return s.stale.Load()
}

func (s *paSession) SetVolume(v float32) error {
	volumes := createChannelVolumes(s.sinkInputChannels, v)
	request := proto.SetSinkInputVolume{
		SinkInputIndex: s.sinkInputIndex,
		ChannelVolumes: volumes,
	}

	if err := s.client.Request(&request, nil); err != nil {
		s.logger.Warnw("Failed to set session volume", "error", err)
		return fmt.Errorf("adjust session volume: %w", err)
	}

	s.logger.Debugw("Adjusting session volume", "to", fmt.Sprintf("%.2f", v))

	return nil
}

func (s *paSession) Release() {
	s.logger.Debug("Releasing audio session")
}

func (s *paSession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc)
}

func (s *masterSession) GetVolume() float32 {
	volumes, err := s.channelVolumes()
	if err != nil {
		if !s.unavailable.Swap(true) {
			s.logger.Warnw("Default device is unavailable", "error", err)
		}

		return 0
	}

	if s.unavailable.Swap(false) {
		s.logger.Info("Default device is available again")
	}

	return parseChannelVolumes(volumes)
}

// Stale reports that the last read of the default device failed. Refreshing
// the map won't bring a device back, but it does stop the watchdog writing to
// one that isn't there.
func (s *masterSession) Stale() bool {
	return s.unavailable.Load()
}

// channelVolumes reads the current per-channel volumes of the default device
func (s *masterSession) channelVolumes() (proto.ChannelVolumes, error) {
	if s.isOutput {
		request := proto.GetSinkInfo{
			SinkIndex: proto.Undefined,
			SinkName:  defaultSinkName,
		}
		reply := proto.GetSinkInfoReply{}

		if err := s.client.Request(&request, &reply); err != nil {
			return nil, fmt.Errorf("get default sink info: %w", err)
		}

		return reply.ChannelVolumes, nil
	}

	request := proto.GetSourceInfo{
		SourceIndex: proto.Undefined,
		SourceName:  defaultSourceName,
	}
	reply := proto.GetSourceInfoReply{}

	if err := s.client.Request(&request, &reply); err != nil {
		return nil, fmt.Errorf("get default source info: %w", err)
	}

	return reply.ChannelVolumes, nil
}

func (s *masterSession) SetVolume(v float32) error {
	var request proto.RequestArgs

	// the default device can change between refreshes, and with it the channel
	// count - a stereo speaker set and a mono headset mic can't take the same
	// volume list. ask the current device rather than remembering one
	current, err := s.channelVolumes()
	if err != nil {
		return fmt.Errorf("adjust session volume: %w", err)
	}

	volumes := createChannelVolumes(byte(len(current)), v)

	if s.isOutput {
		request = &proto.SetSinkVolume{
			SinkIndex:      proto.Undefined,
			SinkName:       defaultSinkName,
			ChannelVolumes: volumes,
		}
	} else {
		request = &proto.SetSourceVolume{
			SourceIndex:    proto.Undefined,
			SourceName:     defaultSourceName,
			ChannelVolumes: volumes,
		}
	}

	if err := s.client.Request(request, nil); err != nil {
		s.logger.Warnw("Failed to set session volume",
			"error", err,
			"volume", v)

		return fmt.Errorf("adjust session volume: %w", err)
	}

	s.logger.Debugw("Adjusting session volume", "to", fmt.Sprintf("%.2f", v))

	return nil
}

func (s *masterSession) Release() {
	s.logger.Debug("Releasing audio session")
}

func (s *masterSession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc)
}

func createChannelVolumes(channels byte, volume float32) proto.ChannelVolumes {
	volumes := make(proto.ChannelVolumes, channels)

	for i := range volumes {
		volumes[i] = proto.Volume(volume * maxVolume)
	}

	return volumes
}

func parseChannelVolumes(volumes proto.ChannelVolumes) float32 {
	var level proto.Volume

	for _, volume := range volumes {
		level += volume
	}

	return float32(level) / float32(len(volumes)) / float32(maxVolume)
}

// pipewireSession controls the volume of a native PipeWire audio stream
// via wpctl, for apps that bypass PulseAudio entirely (e.g. newer Spotify).
type pipewireSession struct {
	baseSession
	nodeID uint32

	// set when wpctl can't find or control our node, meaning the stream ended.
	// node ids are recycled, so a dead session also mustn't keep writing to
	// whatever picks up its id next - the watchdog skips stale sessions
	stale atomic.Bool
}

func newPipewireSession(logger *zap.SugaredLogger, nodeID uint32, processName string) *pipewireSession {
	s := &pipewireSession{
		nodeID: nodeID,
	}

	s.name = processName
	s.humanReadableDesc = processName
	s.logger = logger.Named(fmt.Sprintf("pipewire.%s", strings.ToLower(processName)))
	s.logger.Debugw(sessionCreationLogMessage, "session", s)

	return s
}

func (s *pipewireSession) GetVolume() float32 {

	// wpctl exits 0 and prints "Node not found" for a missing node, so the
	// output not parsing is the signal, not the exit code
	out, err := exec.Command("wpctl", "get-volume", fmt.Sprintf("%d", s.nodeID)).Output()

	var vol float32
	if err == nil {
		_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "Volume: %f", &vol)
	}

	if err != nil {
		if !s.stale.Swap(true) {
			s.logger.Debugw("PipeWire node is gone, marking session stale",
				"output", strings.TrimSpace(string(out)),
				"error", err)
		}

		return 0
	}

	s.stale.Store(false)

	return vol
}

// Stale reports that this session's node no longer exists
func (s *pipewireSession) Stale() bool {
	return s.stale.Load()
}

func (s *pipewireSession) SetVolume(v float32) error {
	s.logger.Debugw("Adjusting PipeWire session volume", "to", fmt.Sprintf("%.2f", v))

	if err := exec.Command("wpctl", "set-volume", fmt.Sprintf("%d", s.nodeID), fmt.Sprintf("%.4f", v)).Run(); err != nil {
		s.stale.Store(true)
		s.logger.Warnw("Failed to set PipeWire session volume", "error", err, "volume", v)
		return fmt.Errorf("set pipewire volume: %w", err)
	}

	return nil
}

func (s *pipewireSession) Release() {
	s.logger.Debug("Releasing PipeWire audio session")
}

func (s *pipewireSession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc)
}
