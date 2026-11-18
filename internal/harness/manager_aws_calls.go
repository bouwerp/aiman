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

// AWSCallsFile holds the AWS tap's call log when the run stopped.
const AWSCallsFile = "aws_calls.json"

// EdgeAWSCalls reads a run's AWS call log over the control socket.
// SocketEdge implements it; an EdgeControl without it reads no call log.
type EdgeAWSCalls interface {
	AWSCalls(ctx context.Context, socket, token string, after int64, limit int) ([]edge.AWSCall, edge.AWSCallsInfo, error)
}

// AWSCalls reads the tap's call log.
func (SocketEdge) AWSCalls(ctx context.Context, socket, token string, after int64, limit int) ([]edge.AWSCall, edge.AWSCallsInfo, error) {
	return edge.Client{Socket: socket, Token: token}.AWSCalls(ctx, after, limit)
}

// AWSCalls reads a run's AWS calls with an ID above after, oldest first:
// from the edge while the run is live, else from aws_calls.json. A run
// that starts no aws service is a params error.
func (m *Manager) AWSCalls(ctx context.Context, id string, after int64, limit int) ([]edge.AWSCall, edge.AWSCallsInfo, error) {
	run, err := m.Get(id)
	if err != nil {
		return nil, edge.AWSCallsInfo{}, err
	}
	if len(run.AWSServices) == 0 {
		return nil, edge.AWSCallsInfo{}, paramsError(fmt.Sprintf("run %s starts no aws service", id))
	}
	dir := m.runDir(run.Session, run.ID)
	if !run.Live() {
		return readAWSCalls(dir, run, after, limit)
	}
	m.Touch(id)
	ec, ok := m.opts.Edge.(EdgeAWSCalls)
	if !ok {
		return nil, edge.AWSCallsInfo{}, &Error{Code: "unavailable", Msg: "this serve reads no aws call log"}
	}
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return readAWSCalls(dir, run, after, limit)
	}
	lr.mu.Lock()
	token := lr.token
	lr.mu.Unlock()
	return ec.AWSCalls(ctx, filepath.Join(dir, "control", ControlSocketName), token, after, limit)
}

// savedAWSCalls is aws_calls.json.
type savedAWSCalls struct {
	Info  edge.AWSCallsInfo `json:"info"`
	Calls []edge.AWSCall    `json:"calls"`
}

// saveAWSCalls keeps every call the tap holds in aws_calls.json before the
// edge goes, so call log reads work on a stopped run.
func (m *Manager) saveAWSCalls(ctx context.Context, r Run, token string) {
	ec, ok := m.opts.Edge.(EdgeAWSCalls)
	if !ok || token == "" || len(r.AWSServices) == 0 {
		return
	}
	dir := m.runDir(r.Session, r.ID)
	sock := filepath.Join(dir, "control", ControlSocketName)
	var saved savedAWSCalls
	after := int64(0)
	for range edge.MaxAWSCalls/edge.MaxAWSCallsLimit + 1 {
		calls, info, err := ec.AWSCalls(ctx, sock, token, after, edge.MaxAWSCallsLimit)
		if err != nil {
			if len(saved.Calls) == 0 {
				return
			}
			break
		}
		saved.Info = info
		saved.Calls = append(saved.Calls, calls...)
		if !info.More || info.Next <= after {
			break
		}
		after = info.Next
	}
	for i := range saved.Calls {
		edge.RedactAWSCall(&saved.Calls[i])
	}
	b, err := json.Marshal(saved)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, AWSCallsFile), b, 0o600)
	}
	if err != nil {
		m.logf("save aws calls of %s: %v", r.ID, err)
	}
}

func readAWSCalls(dir string, run Run, after int64, limit int) ([]edge.AWSCall, edge.AWSCallsInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, AWSCallsFile)) //nolint:gosec // G304: the run's own directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, edge.AWSCallsInfo{}, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is %s and kept no aws call log", run.ID, run.State)}
	}
	if err != nil {
		return nil, edge.AWSCallsInfo{}, err
	}
	var saved savedAWSCalls
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, edge.AWSCallsInfo{}, err
	}
	sort.Slice(saved.Calls, func(i, j int) bool { return saved.Calls[i].ID < saved.Calls[j].ID })
	if limit <= 0 {
		limit = edge.DefaultAWSCallsLimit
	}
	limit = min(limit, edge.MaxAWSCallsLimit)
	info := saved.Info
	info.Next, info.More = after, false
	out := []edge.AWSCall{}
	for _, c := range saved.Calls {
		if c.ID <= after {
			continue
		}
		if len(out) >= limit {
			info.More = true
			break
		}
		out = append(out, c)
		info.Next = c.ID
	}
	return out, info, nil
}
