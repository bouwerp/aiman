package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// pairMaxFailures is how many bad codes a rolling minute tolerates; the
	// next one locks pairing until the window drops back to this count.
	pairMaxFailures = 5
	pairWindow      = time.Minute
	defaultDevice   = "device"
	maxDeviceName   = 64
)

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

type pairResponse struct {
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
}

// pair redeems a pairing code for a device token. It needs no bearer token
// but does need a tailnet identity, so it is refused on Funnel.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Funnel {
		writeJSONError(w, http.StatusForbidden, "", "forbidden", "pairing is not available on Funnel")
		return
	}
	login, err := s.Auth.tailnetLogin(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "", "forbidden", "a tailnet identity is required to pair")
		return
	}
	if s.PairCodes == nil || s.Auth.Devices == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "", "unavailable", "pairing is not configured")
		return
	}
	var body pairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "", "invalid_params", "invalid json")
		return
	}
	if s.pairLocked() {
		writeJSONError(w, http.StatusTooManyRequests, "", "locked", "too many failed pairing codes; try again in a minute")
		return
	}
	hint, ok, err := s.PairCodes.Consume(body.Code)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "", "internal", "pairing codes unavailable")
		return
	}
	if !ok {
		s.pairFailure()
		writeJSONError(w, http.StatusUnauthorized, "", "invalid_code", "invalid or expired pairing code")
		return
	}
	name := firstNonEmpty(body.DeviceName, hint, defaultDevice)
	if runes := []rune(name); len(runes) > maxDeviceName {
		name = string(runes[:maxDeviceName])
	}
	dev, token, err := s.Auth.Devices.Create(name, login)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "", "internal", "could not store the device")
		return
	}
	writeJSON(w, http.StatusOK, pairResponse{DeviceID: dev.ID, Token: token})
}

// pairLocked drops failures older than the window and reports whether more
// than pairMaxFailures remain.
func (s *Server) pairLocked() bool {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	now := s.clock()
	kept := s.pairFailed[:0]
	for _, at := range s.pairFailed {
		if now.Sub(at) < pairWindow {
			kept = append(kept, at)
		}
	}
	s.pairFailed = kept
	return len(kept) > pairMaxFailures
}

func (s *Server) pairFailure() {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.pairFailed = append(s.pairFailed, s.clock())
}

func (s *Server) listDevices(w http.ResponseWriter, _ *http.Request) {
	devs, err := s.Auth.devicesList()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "", "internal", "devices unavailable")
		return
	}
	out := make([]DeviceInfo, 0, len(devs))
	for _, d := range devs {
		out = append(out, d.Info())
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// revokeDevice removes a device. A device token may revoke itself; revoking
// another device takes the host token or allow_destructive.
func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := principalFrom(r.Context())
	if !p.host && p.device.ID != id && !p.device.AllowDestructive {
		writeJSONError(w, http.StatusForbidden, "", "forbidden", "revoking another device needs allow_destructive")
		return
	}
	if s.Auth.Devices == nil {
		writeJSONError(w, http.StatusNotFound, "", "not_found", "device not found")
		return
	}
	err := s.Auth.Devices.Revoke(id)
	switch {
	case errors.Is(err, ErrDeviceNotFound):
		writeJSONError(w, http.StatusNotFound, "", "not_found", "device not found")
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "", "internal", "could not revoke the device")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a Auth) devicesList() ([]Device, error) {
	if a.Devices == nil {
		return nil, nil
	}
	return a.Devices.List()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
