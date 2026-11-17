package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// Network modes of a run.
const (
	NetworkOffline = "offline"
	NetworkOnline  = "online"
)

// Run directory files of the capture proxy.
const (
	// CAFile is the run CA's certificate, read from the edge; the key never
	// leaves the edge.
	CAFile = "ca.pem"
	// CaptureFile holds the flows the edge had when the run stopped.
	CaptureFile = "capture.json"
	// maxStubBytes and maxStubsBytes bound one stub body and all of a
	// scenario's.
	maxStubBytes  = 1 << 20
	maxStubsBytes = 8 << 20
)

// EdgeCapture reads a run's capture proxy over the control socket. SocketEdge
// implements it; an EdgeControl without it runs no capture reads.
type EdgeCapture interface {
	Capture(ctx context.Context, socket, token string, after int64, limit int) ([]edge.Flow, edge.CaptureInfo, error)
	CA(ctx context.Context, socket, token string) ([]byte, error)
}

// Capture reads the edge's flows.
func (SocketEdge) Capture(ctx context.Context, socket, token string, after int64, limit int) ([]edge.Flow, edge.CaptureInfo, error) {
	return edge.Client{Socket: socket, Token: token}.Capture(ctx, after, limit)
}

// CA reads the run CA's certificate.
func (SocketEdge) CA(ctx context.Context, socket, token string) ([]byte, error) {
	return edge.Client{Socket: socket, Token: token}.CA(ctx)
}

// UpstreamRootPaths are where Linux distributions keep the CA bundle, in the
// order Go's crypto/x509 reads them.
var UpstreamRootPaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// FindUpstreamRoots returns the host's CA bundle with symlinks resolved, so
// a bind mount names the file itself.
func FindUpstreamRoots() (string, error) {
	for _, p := range UpstreamRootPaths {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(real); err == nil && fi.Mode().IsRegular() {
			return real, nil
		}
	}
	return "", errors.New("no CA bundle on the host for an online run's edge (looked in " + UpstreamRootPaths[0] + " and the usual places)")
}

// network is the run's network mode from the options.
func (m *Manager) network() string {
	if m.opts.Network == NetworkOnline {
		return NetworkOnline
	}
	return NetworkOffline
}

// proxySpec is the run's capture proxy for the compose file.
func (m *Manager) proxySpec(sc *Scenario) (*ProxySpec, error) {
	p := &ProxySpec{Online: m.network() == NetworkOnline, BodyBytes: m.opts.CaptureBodyBytes, Stubs: len(sc.Stubs) > 0}
	if p.Online {
		roots := m.opts.UpstreamRoots
		if roots == "" {
			var err error
			if roots, err = FindUpstreamRoots(); err != nil {
				return nil, err
			}
		}
		p.UpstreamRoots = roots
	}
	return p, nil
}

// writeStubs writes the scenario's stubs, with each body_file read from the
// exported tree, to the control directory, which only the edge mounts.
func writeStubs(sc *Scenario, src, controlDir string) error {
	if len(sc.Stubs) == 0 {
		return nil
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	out := make([]edge.Stub, 0, len(sc.Stubs))
	total := 0
	for i, st := range sc.Stubs {
		body := []byte(st.Body)
		if st.BodyFile != "" {
			fi, err := root.Stat(filepath.Clean(st.BodyFile))
			if err != nil {
				return invalid(fmt.Sprintf("scenario %s stubs[%d]: body_file %s: %v", sc.ID, i, st.BodyFile, err))
			}
			if !fi.Mode().IsRegular() || fi.Size() > maxStubBytes {
				return invalid(fmt.Sprintf("scenario %s stubs[%d]: body_file %s must be a regular file of at most 1 MiB", sc.ID, i, st.BodyFile))
			}
			if body, err = root.ReadFile(filepath.Clean(st.BodyFile)); err != nil {
				return invalid(fmt.Sprintf("scenario %s stubs[%d]: body_file %s: %v", sc.ID, i, st.BodyFile, err))
			}
		}
		if total += len(body); total > maxStubsBytes {
			return invalid(fmt.Sprintf("scenario %s: stub bodies exceed 8 MiB", sc.ID))
		}
		out = append(out, edge.Stub{Host: st.Host, Path: st.Path, Status: st.Status, Body: body})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(controlDir, StubsFile), b, 0o600)
}

// readCA stores the run CA's certificate from the edge in the run directory.
// A failure leaves the run without ca.pem; the edge still serves.
func (m *Manager) readCA(ctx context.Context, dir, token string) {
	ec, ok := m.opts.Edge.(EdgeCapture)
	if !ok {
		return
	}
	pem, err := ec.CA(ctx, filepath.Join(dir, "control", ControlSocketName), token)
	if err != nil {
		m.logf("read run CA in %s: %v", dir, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, CAFile), pem, 0o644); err != nil { //nolint:gosec // G306: a public certificate
		m.logf("write run CA in %s: %v", dir, err)
	}
}

// Capture reads a run's flows with an ID above after, oldest first: from the
// edge while the run is live, else from the flows saved when it stopped.
func (m *Manager) Capture(ctx context.Context, id string, after int64, limit int) ([]edge.Flow, edge.CaptureInfo, error) {
	run, err := m.Get(id)
	if err != nil {
		return nil, edge.CaptureInfo{}, err
	}
	dir := m.runDir(run.Session, run.ID)
	if !run.Live() {
		return readCapture(dir, run, after, limit)
	}
	m.Touch(id)
	ec, ok := m.opts.Edge.(EdgeCapture)
	if !ok {
		return nil, edge.CaptureInfo{}, &Error{Code: "unavailable", Msg: "this serve reads no capture"}
	}
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return readCapture(dir, run, after, limit)
	}
	lr.mu.Lock()
	token := lr.token
	lr.mu.Unlock()
	return ec.Capture(ctx, filepath.Join(dir, "control", ControlSocketName), token, after, limit)
}

// savedCapture is capture.json.
type savedCapture struct {
	Info  edge.CaptureInfo `json:"info"`
	Flows []edge.Flow      `json:"flows"`
}

// saveCapture keeps every flow the edge holds in capture.json before the
// edge goes, so capture reads work on a stopped run.
func (m *Manager) saveCapture(ctx context.Context, r Run, token string) {
	ec, ok := m.opts.Edge.(EdgeCapture)
	if !ok || token == "" {
		return
	}
	dir := m.runDir(r.Session, r.ID)
	sock := filepath.Join(dir, "control", ControlSocketName)
	var saved savedCapture
	after := int64(0)
	for range edge.MaxFlows/edge.MaxCaptureLimit + 1 {
		flows, info, err := ec.Capture(ctx, sock, token, after, edge.MaxCaptureLimit)
		if err != nil {
			if len(saved.Flows) == 0 {
				return
			}
			break
		}
		saved.Info = info
		saved.Flows = append(saved.Flows, flows...)
		if !info.More || info.Next <= after {
			break
		}
		after = info.Next
	}
	for i := range saved.Flows {
		edge.RedactFlow(&saved.Flows[i])
	}
	b, err := json.Marshal(saved)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, CaptureFile), b, 0o600)
	}
	if err != nil {
		m.logf("save capture of %s: %v", r.ID, err)
	}
}

func readCapture(dir string, run Run, after int64, limit int) ([]edge.Flow, edge.CaptureInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, CaptureFile)) //nolint:gosec // G304: the run's own directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, edge.CaptureInfo{}, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is %s and kept no capture", run.ID, run.State)}
	}
	if err != nil {
		return nil, edge.CaptureInfo{}, err
	}
	var saved savedCapture
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, edge.CaptureInfo{}, err
	}
	sort.Slice(saved.Flows, func(i, j int) bool { return saved.Flows[i].ID < saved.Flows[j].ID })
	if limit <= 0 {
		limit = edge.DefaultCaptureLimit
	}
	limit = min(limit, edge.MaxCaptureLimit)
	info := saved.Info
	info.Next, info.More = after, false
	out := []edge.Flow{}
	for _, f := range saved.Flows {
		if f.ID <= after {
			continue
		}
		if len(out) >= limit {
			info.More = true
			break
		}
		out = append(out, f)
		info.Next = f.ID
	}
	return out, info, nil
}
