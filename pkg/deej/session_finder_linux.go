package deej

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/jfreymuth/pulse/proto"
	"go.uber.org/zap"
)

// pwDumpNode is used to parse relevant fields from pw-dump JSON output
type pwDumpNode struct {
	ID   uint32 `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props map[string]interface{} `json:"props"`
	} `json:"info"`
}

// PulseAudio subscription event bit masks
const (
	paEventFacilityMask  = 0x000F
	paEventFacilitySinkInput = 0x0002
	paEventTypeMask      = 0x0030
	paEventTypeNew       = 0x0000
	paSubscriptionMaskSinkInput = 0x0004
)

type paSessionFinder struct {
	logger        *zap.SugaredLogger
	sessionLogger *zap.SugaredLogger

	client       *proto.Client
	conn         net.Conn
	newSessionCh chan struct{}
}

func newSessionFinder(logger *zap.SugaredLogger) (SessionFinder, error) {
	client, conn, err := proto.Connect("")
	if err != nil {
		logger.Warnw("Failed to establish PulseAudio connection", "error", err)
		return nil, fmt.Errorf("establish PulseAudio connection: %w", err)
	}

	request := proto.SetClientName{
		Props: proto.PropList{
			"application.name": proto.PropListString("deej"),
		},
	}
	reply := proto.SetClientNameReply{}

	if err := client.Request(&request, &reply); err != nil {
		return nil, err
	}

	sf := &paSessionFinder{
		logger:        logger.Named("session_finder"),
		sessionLogger: logger.Named("sessions"),
		client:        client,
		conn:          conn,
		newSessionCh:  make(chan struct{}, 1),
	}

	// subscribe to sink input events so we're notified the moment a new audio
	// stream opens, rather than waiting for the next polling cycle
	client.Callback = func(msg interface{}) {
		event, ok := msg.(*proto.SubscribeEvent)
		if !ok {
			return
		}
		facility := event.Event & paEventFacilityMask
		eventType := event.Event & paEventTypeMask
		if facility == paEventFacilitySinkInput && eventType == paEventTypeNew {
			// non-blocking send: if a signal is already queued, no need to queue another
			select {
			case sf.newSessionCh <- struct{}{}:
			default:
			}
		}
	}

	if err := client.Request(&proto.Subscribe{Mask: paSubscriptionMaskSinkInput}, nil); err != nil {
		sf.logger.Warnw("Failed to subscribe to PulseAudio events (non-fatal)", "error", err)
	}

	sf.logger.Debug("Created PA session finder instance")

	return sf, nil
}

func (sf *paSessionFinder) GetAllSessions() ([]Session, error) {
	sessions := []Session{}

	// get the master sink session
	masterSink, err := sf.getMasterSinkSession()
	if err == nil {
		sessions = append(sessions, masterSink)
	} else {
		sf.logger.Warnw("Failed to get master audio sink session", "error", err)
	}

	// get the master source session
	masterSource, err := sf.getMasterSourceSession()
	if err == nil {
		sessions = append(sessions, masterSource)
	} else {
		sf.logger.Warnw("Failed to get master audio source session", "error", err)
	}

	// enumerate sink inputs and add sessions along the way
	if err := sf.enumerateAndAddSessions(&sessions); err != nil {
		sf.logger.Warnw("Failed to enumerate audio sessions", "error", err)
		return nil, fmt.Errorf("enumerate audio sessions: %w", err)
	}

	// build a set of process names already found via PulseAudio to avoid duplicates
	existingNames := make(map[string]bool)
	for _, s := range sessions {
		existingNames[s.Key()] = true
	}

	// enumerate native PipeWire sessions (apps that bypass PulseAudio entirely)
	if err := sf.enumeratePipeWireSessions(&sessions, existingNames); err != nil {
		sf.logger.Warnw("Failed to enumerate PipeWire sessions (non-fatal)", "error", err)
	}

	return sessions, nil
}

func (sf *paSessionFinder) enumeratePipeWireSessions(sessions *[]Session, existingNames map[string]bool) error {
	out, err := exec.Command("pw-dump").Output()
	if err != nil {
		return fmt.Errorf("run pw-dump: %w", err)
	}

	var nodes []pwDumpNode
	if err := json.Unmarshal(out, &nodes); err != nil {
		return fmt.Errorf("parse pw-dump output: %w", err)
	}

	// pass 1: build a clientID → process name map from PipeWire Client objects.
	// the app name lives on the Client, not on the stream Node itself.
	clientNames := make(map[uint32]string)
	for _, node := range nodes {
		if node.Type != "PipeWire:Interface:Client" {
			continue
		}
		props := node.Info.Props
		var name string
		if binary, ok := props["application.process.binary"].(string); ok && binary != "" {
			name = binary
		} else if appName, ok := props["application.name"].(string); ok && appName != "" {
			name = appName
		}
		if name != "" {
			clientNames[node.ID] = name
		}
	}

	// pass 2: find audio output stream Nodes and resolve their name via client.id.
	// only stream Nodes (not Clients) support wpctl volume control.
	for _, node := range nodes {
		if node.Type != "PipeWire:Interface:Node" {
			continue
		}

		props := node.Info.Props

		mclass, _ := props["media.class"].(string)
		if mclass != "Stream/Output/Audio" {
			continue
		}

		// link the stream node back to its owning client to get the app name
		clientIDFloat, ok := props["client.id"].(float64)
		if !ok {
			continue
		}
		clientID := uint32(clientIDFloat)

		name, ok := clientNames[clientID]
		if !ok {
			continue
		}

		nameLower := strings.ToLower(name)

		// skip anything already handled by PulseAudio (or a previous PW iteration)
		if existingNames[nameLower] {
			continue
		}

		sf.logger.Debugw("Found PipeWire-native audio session", "name", name, "nodeID", node.ID)

		newSession := newPipewireSession(sf.sessionLogger, node.ID, name)
		*sessions = append(*sessions, newSession)
		existingNames[nameLower] = true
	}

	return nil
}

func (sf *paSessionFinder) NewSessionChannel() <-chan struct{} {
	return sf.newSessionCh
}

func (sf *paSessionFinder) Release() error {
	if err := sf.conn.Close(); err != nil {
		sf.logger.Warnw("Failed to close PulseAudio connection", "error", err)
		return fmt.Errorf("close PulseAudio connection: %w", err)
	}

	sf.logger.Debug("Released PA session finder instance")

	return nil
}

func (sf *paSessionFinder) getMasterSinkSession() (Session, error) {
	request := proto.GetSinkInfo{
		SinkIndex: proto.Undefined,
	}
	reply := proto.GetSinkInfoReply{}

	if err := sf.client.Request(&request, &reply); err != nil {
		sf.logger.Warnw("Failed to get master sink info", "error", err)
		return nil, fmt.Errorf("get master sink info: %w", err)
	}

	// create the master sink session
	sink := newMasterSession(sf.sessionLogger, sf.client, reply.SinkIndex, reply.Channels, true)

	return sink, nil
}

func (sf *paSessionFinder) getMasterSourceSession() (Session, error) {
	request := proto.GetSourceInfo{
		SourceIndex: proto.Undefined,
	}
	reply := proto.GetSourceInfoReply{}

	if err := sf.client.Request(&request, &reply); err != nil {
		sf.logger.Warnw("Failed to get master source info", "error", err)
		return nil, fmt.Errorf("get master source info: %w", err)
	}

	// create the master source session
	source := newMasterSession(sf.sessionLogger, sf.client, reply.SourceIndex, reply.Channels, false)

	return source, nil
}

func (sf *paSessionFinder) enumerateAndAddSessions(sessions *[]Session) error {
	request := proto.GetSinkInputInfoList{}
	reply := proto.GetSinkInputInfoListReply{}

	if err := sf.client.Request(&request, &reply); err != nil {
		sf.logger.Warnw("Failed to get sink input list", "error", err)
		return fmt.Errorf("get sink input list: %w", err)
	}

	for _, info := range reply {
		name, ok := info.Properties["application.process.binary"]

		if !ok {
			sf.logger.Warnw("Failed to get sink input's process name",
				"sinkInputIndex", info.SinkInputIndex)

			continue
		}

		// create the deej session object
		newSession := newPASession(sf.sessionLogger, sf.client, info.SinkInputIndex, info.Channels, name.String())

		// add it to our slice
		*sessions = append(*sessions, newSession)

	}

	return nil
}
