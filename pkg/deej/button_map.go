package deej

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// buttonMap holds the mapping between button indexes (channels on the serial
// line, sharing the same index space as sliders) and the shell command each
// one runs when it's pressed.
type buttonMap struct {
	m    map[int]string
	lock sync.Locker
}

func newButtonMap() *buttonMap {
	return &buttonMap{
		m:    make(map[int]string),
		lock: &sync.Mutex{},
	}
}

// buttonMapFromConfig builds a buttonMap out of the raw value viper read for
// the button_mapping key.
//
// This deliberately walks the raw map[string]interface{} instead of using
// viper's GetStringMapStringSlice, because that helper runs strings.Fields over
// scalar values - which would shred "playerctl -p spotify play-pause" into four
// separate commands. Commands contain spaces; targets don't, which is why the
// slider mapping can get away with the helper and we can't.
//
// Any entry we can't make sense of is skipped and reported through the returned
// slice of warnings, so a typo shows up in the logs instead of silently running
// a mangled command.
func buttonMapFromConfig(raw interface{}) (*buttonMap, []string) {
	resultMap := newButtonMap()
	warnings := []string{}

	if raw == nil {
		return resultMap, warnings
	}

	// viper normally hands us map[string]interface{}, but depending on which
	// yaml decoder is in play a nested mapping can arrive keyed by interface{}
	var entries map[string]interface{}

	switch typed := raw.(type) {
	case map[string]interface{}:
		entries = typed
	case map[interface{}]interface{}:
		entries = make(map[string]interface{}, len(typed))
		for key, value := range typed {
			entries[fmt.Sprintf("%v", key)] = value
		}
	default:
		return resultMap, append(warnings,
			fmt.Sprintf("%s must be a mapping of button index to command, ignoring it entirely", configKeyButtonMapping))
	}

	// iterate deterministically so repeated loads log warnings in a stable order
	indexStrings := make([]string, 0, len(entries))
	for indexString := range entries {
		indexStrings = append(indexStrings, indexString)
	}
	sort.Strings(indexStrings)

	for _, indexString := range indexStrings {
		value := entries[indexString]

		buttonIdx, err := strconv.Atoi(indexString)
		if err != nil || buttonIdx < 0 {
			warnings = append(warnings,
				fmt.Sprintf("button index %q isn't a non-negative number, skipping it", indexString))

			continue
		}

		command, ok := value.(string)
		if !ok {
			warnings = append(warnings,
				fmt.Sprintf("button %d must be mapped to a single command string, skipping it", buttonIdx))

			continue
		}

		command = strings.TrimSpace(command)
		if command == "" {
			warnings = append(warnings,
				fmt.Sprintf("button %d has an empty command, skipping it", buttonIdx))

			continue
		}

		resultMap.set(buttonIdx, command)
	}

	return resultMap, warnings
}

func (m *buttonMap) iterate(f func(int, string)) {
	m.lock.Lock()
	defer m.lock.Unlock()

	for key, value := range m.m {
		f(key, value)
	}
}

func (m *buttonMap) get(key int) (string, bool) {
	m.lock.Lock()
	defer m.lock.Unlock()

	value, ok := m.m[key]
	return value, ok
}

func (m *buttonMap) set(key int, value string) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.m[key] = value
}

// has reports whether the given channel index is mapped to a button. serial
// uses this to keep button channels out of the slider path entirely.
func (m *buttonMap) has(key int) bool {
	m.lock.Lock()
	defer m.lock.Unlock()

	_, ok := m.m[key]
	return ok
}

func (m *buttonMap) String() string {
	m.lock.Lock()
	defer m.lock.Unlock()

	return fmt.Sprintf("<%d buttons mapped>", len(m.m))
}
