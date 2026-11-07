package awslogin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/fsutil"
)

// registrationMargin is how close to expiry a registration is replaced.
const registrationMargin = 24 * time.Hour

// registration is the client Identity Center issued to aiman-serve for one start
// URL and region. It identifies the app and grants no AWS access; the file saves
// a round trip and is not a security control. That is also why a failure to read
// or write the file only costs a new registration and never fails a login.
type registration struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	ExpiresAt    time.Time `json:"expires_at"`
	Region       string    `json:"region"`
	StartURL     string    `json:"start_url"`
}

type registrationFile struct {
	Clients []registration `json:"clients"`
}

func (r registration) matches(d Delegation) bool {
	return r.Region == d.SSORegion && r.StartURL == d.StartURL
}

// authorize starts the device authorization with the cached client, replacing a
// client Identity Center no longer accepts.
func (m *Manager) authorize(ctx context.Context, d Delegation) (registration, deviceAuthorization, error) {
	reg, ok := m.cachedRegistration(d)
	if !ok {
		var err error
		if reg, err = m.newRegistration(ctx, d); err != nil {
			return registration{}, deviceAuthorization{}, err
		}
	}
	auth, err := m.startDeviceAuthorization(ctx, d, reg)
	if apiCode(err) == codeInvalidClient {
		if reg, err = m.newRegistration(ctx, d); err != nil {
			return registration{}, deviceAuthorization{}, err
		}
		auth, err = m.startDeviceAuthorization(ctx, d, reg)
	}
	if err != nil {
		return registration{}, deviceAuthorization{}, err
	}
	return reg, auth, nil
}

func (m *Manager) newRegistration(ctx context.Context, d Delegation) (registration, error) {
	reg, err := m.registerClient(ctx, d)
	if err != nil {
		return registration{}, err
	}
	m.updateRegistrations(func(f *registrationFile) {
		kept := f.Clients[:0]
		for _, c := range f.Clients {
			if !c.matches(d) {
				kept = append(kept, c)
			}
		}
		kept = append(kept, reg)
		f.Clients = kept
	})
	return reg, nil
}

func (m *Manager) cachedRegistration(d Delegation) (registration, bool) {
	m.regMu.Lock()
	defer m.regMu.Unlock()
	path := m.registrationPath()
	if path == "" {
		return registration{}, false
	}
	now := m.o.Now()
	for _, c := range readRegistrations(path).Clients {
		if c.matches(d) && c.ExpiresAt.Sub(now) > registrationMargin {
			return c, true
		}
	}
	return registration{}, false
}

// forgetRegistration drops reg so the next start registers again. It compares
// the client id, so a client a newer start already replaced it with stays.
func (m *Manager) forgetRegistration(reg registration) {
	m.updateRegistrations(func(f *registrationFile) {
		kept := f.Clients[:0]
		for _, c := range f.Clients {
			if c.ClientID != reg.ClientID || c.Region != reg.Region || c.StartURL != reg.StartURL {
				kept = append(kept, c)
			}
		}
		f.Clients = kept
	})
}

func (m *Manager) updateRegistrations(change func(*registrationFile)) {
	m.regMu.Lock()
	defer m.regMu.Unlock()
	path := m.registrationPath()
	if path == "" {
		return
	}
	f := readRegistrations(path)
	change(&f)
	_ = writeRegistrations(path, f) // see registration: the cache is optional
}

func (m *Manager) registrationPath() string {
	home := m.o.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return ""
		}
	}
	return filepath.Join(home, ".aiman", "aws-sso-client.json")
}

// readRegistrations treats a missing or corrupt file as empty.
func readRegistrations(path string) registrationFile {
	var f registrationFile
	data, err := os.ReadFile(path) // #nosec G304 -- fixed name under the configured home
	if err != nil {
		return registrationFile{}
	}
	if json.Unmarshal(data, &f) != nil {
		return registrationFile{}
	}
	return f
}

func writeRegistrations(path string, f registrationFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := fsutil.WriteIfChanged(path, data, 0o600); err != nil {
		return err
	}
	// WriteIfChanged leaves unchanged bytes alone, whatever their mode.
	return os.Chmod(path, 0o600)
}
