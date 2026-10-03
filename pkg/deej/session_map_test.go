package deej

import (
	"sync"
	"testing"

	"go.uber.org/zap"
)

type fakeSession struct {
	baseSession

	lock    sync.Mutex
	volume  float32
	setCall []float32
}

func newFakeSession(name string) *fakeSession {
	s := &fakeSession{}
	s.name = name
	s.logger = zap.NewNop().Sugar()

	return s
}

func (s *fakeSession) GetVolume() float32 {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.volume
}

func (s *fakeSession) SetVolume(v float32) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.volume = v
	s.setCall = append(s.setCall, v)

	return nil
}

func (s *fakeSession) Release() {}

type fakeFinder struct {
	names []string
}

func (f *fakeFinder) GetAllSessions() ([]Session, error) {
	sessions := []Session{}
	for _, name := range f.names {
		sessions = append(sessions, newFakeSession(name))
	}

	return sessions, nil
}

func (f *fakeFinder) NewSessionChannel() <-chan struct{} { return nil }
func (f *fakeFinder) ConnectionLost() <-chan struct{}    { return nil }
func (f *fakeFinder) Release() error                     { return nil }

func newTestSessionMap(t *testing.T, mapping map[int][]string, names ...string) (*sessionMap, *SerialIO) {
	sliders := newSliderMap()
	for idx, targets := range mapping {
		sliders.set(idx, targets)
	}

	d := &Deej{
		config: &CanonicalConfig{
			values: configValues{
				SliderMapping: sliders,
				ButtonMapping: newButtonMap(),
			},
		},
	}

	d.serial = &SerialIO{deej: d, logger: zap.NewNop().Sugar()}

	m, err := newSessionMap(d, zap.NewNop().Sugar(), &fakeFinder{names: names})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.reacquireSessions(); err != nil {
		t.Fatal(err)
	}

	return m, d.serial
}

func TestConcurrentRefreshesDoNotDuplicateSessions(t *testing.T) {
	m, _ := newTestSessionMap(t, map[int][]string{0: {"firefox"}}, "firefox", "discord", "spotify")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.refreshSessions(true)
		}()
	}

	// readers running alongside, the way the watchdog and slider goroutines do
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.get("firefox")
			m.resolveTarget(specialTargetTransformPrefix + specialTargetAllUnmapped)
		}()
	}

	wg.Wait()

	for _, name := range []string{"firefox", "discord", "spotify"} {
		if sessions, _ := m.get(name); len(sessions) != 1 {
			t.Errorf("%s has %d sessions after concurrent refreshes, want 1", name, len(sessions))
		}
	}

	unmapped := m.resolveTarget(specialTargetTransformPrefix + specialTargetAllUnmapped)
	if len(unmapped) != 2 {
		t.Errorf("got unmapped %v, want discord and spotify once each", unmapped)
	}
}

func TestWatchdogSkipsSlidersThatHaveNotReported(t *testing.T) {
	m, serial := newTestSessionMap(t, map[int][]string{0: {"firefox"}, 1: {"discord"}}, "firefox", "discord")

	// slider 0 has reported, slider 1 is still at the -1 placeholder
	serial.currentSliderPercentValues = []float32{0.5, -1}

	m.assertSliderVolumes()

	firefox, _ := m.get("firefox")
	discord, _ := m.get("discord")

	if got := firefox[0].GetVolume(); got != 0.5 {
		t.Errorf("firefox volume is %v, want 0.5", got)
	}

	for _, v := range discord[0].(*fakeSession).setCall {
		if v < 0 {
			t.Fatalf("watchdog set discord's volume to %v", v)
		}
	}
}
