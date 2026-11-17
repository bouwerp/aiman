package edge

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Capture bounds.
const (
	// MaxFlows is how many flows the edge keeps; older ones are dropped.
	MaxFlows = 1000
	// DefaultBodyBytes is harness.capture.body_bytes' default: how much of
	// each body a flow keeps.
	DefaultBodyBytes = 64 << 10
	// MaxBodyBytes caps harness.capture.body_bytes.
	MaxBodyBytes = 1 << 20
	// DefaultCaptureLimit and MaxCaptureLimit bound one capture read.
	DefaultCaptureLimit = 50
	MaxCaptureLimit     = 200
	// maxCaptureReply bounds the flows of one capture reply in bytes; a reply
	// holds at least one flow.
	maxCaptureReply = 4 << 20
)

// Flow outcomes.
const (
	// OutcomeUpstream: an online run forwarded the request.
	OutcomeUpstream = "upstream"
	// OutcomeStub: a scenario stub answered.
	OutcomeStub = "stub"
	// OutcomeOffline: an offline run had no stub and answered 502.
	OutcomeOffline = "offline"
	// OutcomeRefused: the proxy would not carry the request (a private
	// address, a protocol it does not capture).
	OutcomeRefused = "refused"
	// OutcomeError: the handshake, the upstream or the client failed.
	OutcomeError = "error"
	// OutcomePending: the response has not finished yet.
	OutcomePending = "pending"
)

// Flow is one request through the edge proxy and its response. Headers are
// redacted and each body keeps its first body_bytes bytes, also redacted.
type Flow struct {
	ID       int64     `json:"id"`
	Start    time.Time `json:"start"`
	Duration float64   `json:"duration_ms"`
	Client   string    `json:"client,omitempty"`
	Method   string    `json:"method"`
	URL      string    `json:"url"`
	Host     string    `json:"host"`
	Proto    string    `json:"proto,omitempty"`
	// TLS is set for a request the proxy read inside a CONNECT tunnel.
	TLS     bool   `json:"tls"`
	Status  int    `json:"status,omitempty"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`

	RequestHeaders    http.Header `json:"request_headers,omitempty"`
	RequestBody       string      `json:"request_body,omitempty"`
	RequestBodyBytes  int64       `json:"request_body_bytes"`
	RequestTruncated  bool        `json:"request_truncated,omitempty"`
	RequestBinary     bool        `json:"request_binary,omitempty"`
	ResponseHeaders   http.Header `json:"response_headers,omitempty"`
	ResponseBody      string      `json:"response_body,omitempty"`
	ResponseBodyBytes int64       `json:"response_body_bytes"`
	ResponseTruncated bool        `json:"response_truncated,omitempty"`
	ResponseBinary    bool        `json:"response_binary,omitempty"`
	// Redacted counts the values the edge replaced.
	Redacted int `json:"redacted,omitempty"`

	// dropped marks a flow that fell out of the store while pending.
	dropped bool
}

// CaptureInfo describes the store a capture read came from.
type CaptureInfo struct {
	// Network is offline or online.
	Network string `json:"network"`
	// Total counts every flow since the edge started; Dropped counts those
	// that fell out of the store.
	Total   int64 `json:"total"`
	Dropped int64 `json:"dropped"`
	// Next is the after value of the next read.
	Next int64 `json:"next"`
	// More is set when flows after Next exist already.
	More bool `json:"more,omitempty"`
}

// pendingHold is how long a pending flow holds the read cursor: the next
// read returns it again, finished, while it is younger than this.
const pendingHold = time.Minute

// flowStore keeps the newest MaxFlows flows. A flow is stored when it
// starts and updated in place until it ends, so a read sees pending ones.
type flowStore struct {
	mu    sync.Mutex
	flows []*Flow
	next  int64
	// bytes is the kept bodies' size; past storeBytes the oldest flows go
	// even when fewer than MaxFlows are kept, so the edge's memory limit
	// holds whatever body_bytes says.
	bytes int
}

// storeBytes bounds the bodies the edge keeps in memory.
const storeBytes = 64 << 20

func flowBytes(f *Flow) int { return len(f.RequestBody) + len(f.ResponseBody) }

func (s *flowStore) add(f *Flow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	f.ID = s.next
	s.flows = append(s.flows, f)
	s.bytes += flowBytes(f)
	s.trimLocked()
}

// update runs fn on f under the store's lock.
func (s *flowStore) update(f *Flow, fn func(*Flow)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.dropped {
		fn(f)
		return
	}
	before := flowBytes(f)
	fn(f)
	s.bytes += flowBytes(f) - before
	s.trimLocked()
}

// trimLocked drops the oldest flows past MaxFlows or storeBytes, keeping
// the newest. A dropped flow that is still pending is updated off the list.
func (s *flowStore) trimLocked() {
	for len(s.flows) > 1 && (len(s.flows) > MaxFlows || s.bytes > storeBytes) {
		s.bytes -= flowBytes(s.flows[0])
		s.flows[0].dropped = true
		s.flows[0] = nil
		s.flows = s.flows[1:]
	}
}

// read copies up to limit flows with an ID above after, oldest first,
// stopping early past maxCaptureReply bytes.
func (s *flowStore) read(after int64, limit int, now time.Time) ([]Flow, CaptureInfo) {
	if limit <= 0 {
		limit = DefaultCaptureLimit
	}
	limit = min(limit, MaxCaptureLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	info := CaptureInfo{Total: s.next, Next: after}
	if len(s.flows) > 0 {
		info.Dropped = s.flows[0].ID - 1
	}
	out := []Flow{}
	size := 0
	hold, held := int64(0), false
	for _, f := range s.flows {
		if f.ID <= after {
			continue
		}
		if len(out) >= limit {
			info.More = true
			break
		}
		cp := *f
		b, _ := json.Marshal(&cp)
		if len(out) > 0 && size+len(b) > maxCaptureReply {
			info.More = true
			break
		}
		size += len(b)
		out = append(out, cp)
		info.Next = f.ID
		if !held && f.Outcome == OutcomePending && now.Sub(f.Start) < pendingHold {
			hold, held = f.ID-1, true
		}
	}
	if held {
		info.Next = hold
	}
	return out, info
}
