package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DevicesFileName holds paired devices with their token hashes.
	DevicesFileName = "gateway-devices.json"
	// lastSeenEvery limits last_seen writes to one per device per minute.
	lastSeenEvery = time.Minute
	deviceIDBytes = 8
)

// ErrDeviceNotFound is returned for an id no device has.
var ErrDeviceNotFound = errors.New("device not found")

// Device is one paired phone. TokenHash is the sha256 of its bearer token;
// the token itself is never stored.
type Device struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	TokenHash        string    `json:"token_sha256"`
	CreatedAt        time.Time `json:"created_at"`
	LastSeen         time.Time `json:"last_seen,omitzero"`
	Login            string    `json:"login,omitempty"`
	AllowDestructive bool      `json:"allow_destructive"`
}

// DeviceInfo is a Device without its token hash, for listings.
type DeviceInfo struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	CreatedAt        time.Time `json:"created_at"`
	LastSeen         time.Time `json:"last_seen,omitzero"`
	Login            string    `json:"login,omitempty"`
	AllowDestructive bool      `json:"allow_destructive"`
}

// Info drops the token hash.
func (d Device) Info() DeviceInfo {
	return DeviceInfo{ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt, LastSeen: d.LastSeen, Login: d.Login, AllowDestructive: d.AllowDestructive}
}

type devicesFile struct {
	Devices []Device `json:"devices"`
}

// DeviceStore is the 0600 device file. The service and the CLI both write
// it: writes are locked read-modify-writes, and reads reload the file
// whenever a stat shows it was replaced.
type DeviceStore struct {
	path string
	now  func() time.Time

	mu   sync.Mutex
	devs []Device
	stat os.FileInfo
}

// NewDeviceStore returns the device file at path; it need not exist yet.
func NewDeviceStore(path string) *DeviceStore {
	return &DeviceStore{path: path, now: time.Now}
}

// Create adds a device and returns it with its token, the only time the
// token exists outside the phone.
func (s *DeviceStore) Create(name, login string) (Device, string, error) {
	token, err := randomHex(tokenBytes)
	if err != nil {
		return Device{}, "", fmt.Errorf("generating device token: %w", err)
	}
	id, err := randomHex(deviceIDBytes)
	if err != nil {
		return Device{}, "", fmt.Errorf("generating device id: %w", err)
	}
	dev := Device{
		ID:        id,
		Name:      strings.TrimSpace(name),
		TokenHash: hashHex(token),
		CreatedAt: s.now().UTC(),
		Login:     strings.TrimSpace(login),
	}
	err = s.update(func(devs []Device) ([]Device, error) {
		return append(devs, dev), nil
	})
	if err != nil {
		return Device{}, "", err
	}
	return dev, token, nil
}

// Authenticate finds the device whose token hash matches token and records
// it as seen, at most once a minute.
func (s *DeviceStore) Authenticate(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	want := []byte(hashHex(token))
	s.mu.Lock()
	if err := s.refreshLocked(); err != nil {
		s.mu.Unlock()
		return Device{}, false
	}
	var dev Device
	found := false
	for _, d := range s.devs {
		if subtle.ConstantTimeCompare([]byte(d.TokenHash), want) == 1 {
			dev, found = d, true
		}
	}
	s.mu.Unlock()
	if !found {
		return Device{}, false
	}
	now := s.now().UTC()
	if now.Sub(dev.LastSeen) >= lastSeenEvery {
		// A failed last_seen write does not fail the request.
		_ = s.update(func(devs []Device) ([]Device, error) {
			for i := range devs {
				if devs[i].ID == dev.ID {
					devs[i].LastSeen = now
				}
			}
			return devs, nil
		})
		dev.LastSeen = now
	}
	return dev, true
}

// List returns every device.
func (s *DeviceStore) List() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(); err != nil {
		return nil, err
	}
	out := make([]Device, len(s.devs))
	copy(out, s.devs)
	return out, nil
}

// Revoke removes the device with id.
func (s *DeviceStore) Revoke(id string) error {
	return s.update(func(devs []Device) ([]Device, error) {
		for i, d := range devs {
			if d.ID == id {
				return append(devs[:i], devs[i+1:]...), nil
			}
		}
		return nil, ErrDeviceNotFound
	})
}

// SetAllowDestructive turns destructive methods on or off for device id.
func (s *DeviceStore) SetAllowDestructive(id string, on bool) error {
	return s.update(func(devs []Device) ([]Device, error) {
		for i := range devs {
			if devs[i].ID == id {
				devs[i].AllowDestructive = on
				return devs, nil
			}
		}
		return nil, ErrDeviceNotFound
	})
}

func (s *DeviceStore) update(fn func([]Device) ([]Device, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return withFileLock(s.path, func() error {
		devs, err := s.read()
		if err != nil {
			return err
		}
		devs, err = fn(devs)
		if err != nil {
			return err
		}
		if devs == nil {
			devs = []Device{}
		}
		body, err := json.MarshalIndent(devicesFile{Devices: devs}, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomic(s.path, append(body, '\n')); err != nil {
			return err
		}
		s.devs = devs
		s.stat, _ = os.Stat(s.path)
		return nil
	})
}

// refreshLocked rereads the file when it differs from the cached stat. Every
// write replaces the file by rename, so a new inode, size or mtime marks a
// change made by this process or another.
func (s *DeviceStore) refreshLocked() error {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.devs, s.stat = nil, nil
		return nil
	}
	if err != nil {
		return err
	}
	if s.stat != nil && os.SameFile(s.stat, st) && st.ModTime().Equal(s.stat.ModTime()) && st.Size() == s.stat.Size() {
		return nil
	}
	devs, err := s.read()
	if err != nil {
		return err
	}
	s.devs, s.stat = devs, st
	return nil
}

func (s *DeviceStore) read() ([]Device, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f devicesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("gateway devices: %w", err)
	}
	return f.Devices, nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
