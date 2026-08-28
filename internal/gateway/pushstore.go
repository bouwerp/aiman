package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	PushFileName      = "gateway-push.json"
	stateWaitingInput = "waiting_input"
	stateWorking      = "working"
	stateExited       = "exited"
	stateErrored      = "errored"
)

// PushDevice is one phone's Expo registration. Token is never logged.
type PushDevice struct {
	Token    string   `json:"token"`
	DeviceID string   `json:"device_id,omitempty"`
	States   []string `json:"states,omitempty"`
}

type pushFile struct {
	Devices []PushDevice `json:"devices"`
}

// PushStore persists Expo tokens as 0600 JSON under the aiman directory.
type PushStore struct {
	path string
	mu   sync.Mutex
	devs []PushDevice
}

func OpenPushStore(path string) (*PushStore, error) {
	s := &PushStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var f pushFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("gateway push store: %w", err)
	}
	s.devs = f.Devices
	return s, nil
}

func (s *PushStore) Devices() []PushDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PushDevice, len(s.devs))
	copy(out, s.devs)
	return out
}

func (s *PushStore) Register(d PushDevice) error {
	d.Token = strings.TrimSpace(d.Token)
	d.DeviceID = strings.TrimSpace(d.DeviceID)
	if d.Token == "" {
		return fmt.Errorf("token is required")
	}
	d.States = normalizeStates(d.States)
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, existing := range s.devs {
		if sameDevice(existing, d) {
			s.devs[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		s.devs = append(s.devs, d)
	}
	return s.saveLocked()
}

func (s *PushStore) Unregister(token, deviceID string) error {
	token = strings.TrimSpace(token)
	deviceID = strings.TrimSpace(deviceID)
	if token == "" && deviceID == "" {
		return fmt.Errorf("token or device_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.devs[:0]
	for _, d := range s.devs {
		if token != "" && d.Token == token {
			continue
		}
		if deviceID != "" && d.DeviceID != "" && d.DeviceID == deviceID {
			continue
		}
		kept = append(kept, d)
	}
	s.devs = kept
	return s.saveLocked()
}

func (s *PushStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(pushFile{Devices: s.devs}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(body, '\n'), 0o600)
}

func sameDevice(a, b PushDevice) bool {
	if b.DeviceID != "" && a.DeviceID == b.DeviceID {
		return true
	}
	return a.Token == b.Token
}

func normalizeStates(states []string) []string {
	if states == nil {
		return []string{stateWaitingInput}
	}
	var out []string
	seen := map[string]bool{}
	for _, st := range states {
		st = strings.TrimSpace(st)
		if st == "" || st == stateWorking || seen[st] {
			continue
		}
		seen[st] = true
		out = append(out, st)
	}
	return out
}

func wantsState(d PushDevice, state string) bool {
	if state == "" || state == stateWorking {
		return false
	}
	for _, st := range d.States {
		if st == state {
			return true
		}
	}
	return false
}
