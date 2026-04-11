package deej

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"

	"github.com/jfreymuth/pulse/proto"
)

// normal PulseAudio volume (100%)
const maxVolume = 0x10000

var errNoSuchProcess = errors.New("No such process")

type paSession struct {
	baseSession

	processName string

	client *proto.Client

	sinkInputIndex    uint32
	sinkInputChannels byte
}

type masterSession struct {
	baseSession

	client *proto.Client

	streamIndex    uint32
	streamChannels byte
	isOutput       bool
}

func newPASession(
	logger *zap.SugaredLogger,
	client *proto.Client,
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
	client *proto.Client,
	streamIndex uint32,
	streamChannels byte,
	isOutput bool,
) *masterSession {

	s := &masterSession{
		client:         client,
		streamIndex:    streamIndex,
		streamChannels: streamChannels,
		isOutput:       isOutput,
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
		s.logger.Warnw("Failed to get session volume", "error", err)
	}

	level := parseChannelVolumes(reply.ChannelVolumes)

	return level
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
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc, s.GetVolume())
}

func (s *masterSession) GetVolume() float32 {
	var level float32

	if s.isOutput {
		request := proto.GetSinkInfo{
			SinkIndex: s.streamIndex,
		}
		reply := proto.GetSinkInfoReply{}

		if err := s.client.Request(&request, &reply); err != nil {
			s.logger.Warnw("Failed to get session volume", "error", err)
			return 0
		}

		level = parseChannelVolumes(reply.ChannelVolumes)
	} else {
		request := proto.GetSourceInfo{
			SourceIndex: s.streamIndex,
		}
		reply := proto.GetSourceInfoReply{}

		if err := s.client.Request(&request, &reply); err != nil {
			s.logger.Warnw("Failed to get session volume", "error", err)
			return 0
		}

		level = parseChannelVolumes(reply.ChannelVolumes)
	}

	return level
}

func (s *masterSession) SetVolume(v float32) error {
	var request proto.RequestArgs

	volumes := createChannelVolumes(s.streamChannels, v)

	if s.isOutput {
		request = &proto.SetSinkVolume{
			SinkIndex:      s.streamIndex,
			ChannelVolumes: volumes,
		}
	} else {
		request = &proto.SetSourceVolume{
			SourceIndex:    s.streamIndex,
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
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc, s.GetVolume())
}

func createChannelVolumes(channels byte, volume float32) []uint32 {
	volumes := make([]uint32, channels)

	for i := range volumes {
		volumes[i] = uint32(volume * maxVolume)
	}

	return volumes
}

func parseChannelVolumes(volumes []uint32) float32 {
	var level uint32

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
	out, err := exec.Command("wpctl", "get-volume", fmt.Sprintf("%d", s.nodeID)).Output()
	if err != nil {
		s.logger.Warnw("Failed to get PipeWire session volume", "error", err)
		return 0
	}

	// wpctl outputs e.g. "Volume: 0.5000\n"
	var vol float32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "Volume: %f", &vol); err != nil {
		s.logger.Warnw("Failed to parse wpctl volume output", "output", string(out), "error", err)
		return 0
	}

	return vol
}

func (s *pipewireSession) SetVolume(v float32) error {
	s.logger.Debugw("Adjusting PipeWire session volume", "to", fmt.Sprintf("%.2f", v))

	if err := exec.Command("wpctl", "set-volume", fmt.Sprintf("%d", s.nodeID), fmt.Sprintf("%.4f", v)).Run(); err != nil {
		s.logger.Warnw("Failed to set PipeWire session volume", "error", err, "volume", v)
		return fmt.Errorf("set pipewire volume: %w", err)
	}

	return nil
}

func (s *pipewireSession) Release() {
	s.logger.Debug("Releasing PipeWire audio session")
}

func (s *pipewireSession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc, s.GetVolume())
}
