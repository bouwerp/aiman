package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/redact"
)

// AWSTapPort is the port the tap listens on at the edge's inner address,
// which the aws service's name resolves to on the inner network.
const AWSTapPort = 4566

// Call log bounds.
const (
	// MaxAWSCalls is how many calls the tap keeps; older ones are dropped.
	MaxAWSCalls = 2000
	// DefaultAWSCallsLimit and MaxAWSCallsLimit bound one read.
	DefaultAWSCallsLimit = 50
	MaxAWSCallsLimit     = 200
	// maxAWSCallsReply bounds one read's calls in bytes.
	maxAWSCallsReply = 4 << 20
	// awsBodyPeek is how much of a JSON or query request body the tap reads
	// for the resource and params; the rest streams past.
	awsBodyPeek = 64 << 10
	// awsErrorPeek is how much of a failed answer the tap reads for its
	// error code.
	awsErrorPeek = 8 << 10
)

// AWSApplierAgent marks the plan applier: a request whose User-Agent holds
// it is logged with client "aiman". It is a label, not proof.
const AWSApplierAgent = "aiman-harness-aws-apply"

// AWSClientAiman is the client name of the applier's calls.
const AWSClientAiman = "aiman"

// CodeAimanRefused is the error code of a call the tap refused.
const CodeAimanRefused = "AimanRefused"

// awsTapRefused are emulator admin paths the tap answers 403: they change
// the emulator's configuration or reach services a run does not load.
var awsTapRefused = []string{
	"/_ministack/config", "/_ministack/cfn-", "/_ministack/lambda-", "/_ministack/eks-auth",
	"/_ministack/rds/", "/_ministack/transfer/",
}

// AWSCall is one AWS API call through the tap. It holds no object body,
// item value or message body: params carry DynamoDB attribute values as
// their types and message bodies as their sizes.
type AWSCall struct {
	ID       int64     `json:"id"`
	Start    time.Time `json:"start"`
	Duration float64   `json:"duration_ms"`
	// Client is the compose service the call came from, "aiman" for the
	// applier, else the caller's address.
	Client    string      `json:"client,omitempty"`
	Protocol  string      `json:"protocol"`
	Service   string      `json:"service"`
	Operation string      `json:"operation"`
	Method    string      `json:"method"`
	Resource  AWSResource `json:"resource"`
	Status    int         `json:"status,omitempty"`
	ErrorCode string      `json:"error_code,omitempty"`
	// Error is a transport failure: the emulator did not answer.
	Error         string `json:"error,omitempty"`
	Pending       bool   `json:"pending,omitempty"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	Presigned     bool   `json:"presigned,omitempty"`
	// Region and AccessKey come from the SigV4 credential scope. The access
	// key is shown only when it is "test"; ForeignKey marks any other.
	Region     string `json:"region,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	ForeignKey bool   `json:"foreign_key,omitempty"`
	Unsigned   bool   `json:"unsigned,omitempty"`
	// Params are the request's parameters as compact JSON, cut at 4 KiB.
	Params          string `json:"params,omitempty"`
	ParamsTruncated bool   `json:"params_truncated,omitempty"`
	Redacted        int    `json:"redacted,omitempty"`

	clientIP string
	applier  bool
}

// AWSCallsInfo describes the call log a read came from.
type AWSCallsInfo struct {
	// Service is the aws service the tap fronts.
	Service string `json:"service"`
	Total   int64  `json:"total"`
	Dropped int64  `json:"dropped"`
	Next    int64  `json:"next"`
	More    bool   `json:"more,omitempty"`
}

// AWSTap forwards the app's AWS calls to the emulator unchanged: host,
// path, query, headers and body stay as sent, so SigV4 stays valid, and
// bodies stream both ways. Each call is recorded in the call log.
type AWSTap struct {
	name string
	rp   *httputil.ReverseProxy
	srv  *http.Server
	now  func() time.Time

	store   awsCallStore
	clients clientNames
}

// AWSTapOption configures a tap.
type AWSTapOption func(*AWSTap)

// WithAWSTapName names the aws service the tap fronts.
func WithAWSTapName(name string) AWSTapOption { return func(t *AWSTap) { t.name = name } }

// WithAWSTapResolver replaces the reverse lookup of a client address
// (tests). It returns the PTR names of addr.
func WithAWSTapResolver(fn func(ctx context.Context, addr string) ([]string, error)) AWSTapOption {
	return func(t *AWSTap) { t.clients.lookup = fn }
}

// WithAWSTapClock replaces the clock (tests).
func WithAWSTapClock(now func() time.Time) AWSTapOption { return func(t *AWSTap) { t.now = now } }

// NewAWSTap returns a tap to target (host:port of the emulator).
func NewAWSTap(target string, opts ...AWSTapOption) (*AWSTap, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, errors.New("aws tap target must be host:port")
	}
	u := &url.URL{Scheme: "http", Host: target}
	t := &AWSTap{now: time.Now}
	t.clients.lookup = net.DefaultResolver.LookupAddr
	for _, o := range opts {
		o(t)
	}
	t.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if rec, ok := w.(*awsRecorder); ok {
				rec.err = err
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	t.srv = &http.Server{Handler: t, ReadHeaderTimeout: handshakeTimeout, IdleTimeout: idleConnTimeout, ErrorLog: log.New(io.Discard, "", 0)}
	return t, nil
}

// Name is the aws service the tap fronts.
func (t *AWSTap) Name() string { return t.name }

// ServeHTTP refuses the admin paths and forwards everything else, recording
// each call.
func (t *AWSTap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := t.now()
	cl := classifyAWS(r)
	c := &AWSCall{
		Start: start, Protocol: cl.protocol, Service: cl.service, Operation: cl.operation, Method: r.Method,
		Resource: cl.resource, Presigned: cl.presigned, Pending: true,
		clientIP: clientIP(r.RemoteAddr), applier: strings.Contains(r.UserAgent(), AWSApplierAgent),
	}
	if cl.scoped {
		c.Region = cl.scope.region
		if cl.scope.key == "test" {
			c.AccessKey = "test"
		} else {
			c.ForeignKey = true
		}
	} else {
		c.Unsigned = true
	}
	if c.applier {
		// The applier is a one-shot container whose address the app or a
		// check may take next: the calls already made from the address keep
		// the name they had, and the next caller there is looked up afresh.
		t.store.nameFrom(c.clientIP, t.clients.forget(c.clientIP))
	} else {
		go t.clients.resolve(c.clientIP)
	}
	t.store.add(c)

	for _, p := range awsTapRefused {
		if strings.HasPrefix(r.URL.Path, p) {
			http.Error(w, "aiman edge: "+r.URL.Path+" is not reachable in a harness run", http.StatusForbidden)
			t.store.update(c, func(c *AWSCall) {
				c.Status, c.ErrorCode, c.Pending = http.StatusForbidden, CodeAimanRefused, false
				c.Duration = durationMS(t.now().Sub(start))
			})
			return
		}
	}

	var peek *peekBody
	if r.Body != nil && r.Body != http.NoBody {
		peek = &peekBody{rc: r.Body, keep: cl.protocol == AWSProtocolJSON || cl.protocol == AWSProtocolQuery}
		r.Body = peek
	}
	rec := &awsRecorder{ResponseWriter: w}
	t.rp.ServeHTTP(rec, r)

	params, cl := t.params(r, cl, peek)
	text, cut, n := finishParams(params)
	var reqBytes int64
	if peek != nil {
		reqBytes = peek.n
	}
	t.store.update(c, func(c *AWSCall) {
		c.Pending = false
		c.Duration = durationMS(t.now().Sub(start))
		c.Status = rec.status
		if c.Status == 0 {
			c.Status = http.StatusOK
		}
		c.RequestBytes, c.ResponseBytes = reqBytes, rec.n
		c.Resource, c.Operation, c.Service = cl.resource, cl.operation, cl.service
		c.Params, c.ParamsTruncated, c.Redacted = text, cut, n
		if c.Status >= 300 {
			c.ErrorCode = awsErrorCode(rec.Header(), rec.head.Bytes())
		}
		if rec.err != nil {
			c.Error, _ = redact.Line(rec.err.Error(), nil)
		}
		c.Redacted += redactResource(&c.Resource)
	})
}

// params builds the call's params from what the request carried (the body
// for JSON and query protocol, the query and some headers for S3) and
// completes the classification with what the body names: the resource and,
// for a query protocol POST, the action and service.
func (t *AWSTap) params(r *http.Request, cl awsClassified, peek *peekBody) (map[string]any, awsClassified) {
	switch cl.protocol {
	case AWSProtocolJSON:
		if peek == nil || peek.over {
			return map[string]any{"body": "not read: over 64 KiB"}, cl
		}
		m, ok := jsonParams(peek.buf.Bytes())
		if !ok {
			return nil, cl
		}
		jsonResource(m, &cl.resource)
		typed, _ := typeValues(m).(map[string]any)
		return typed, cl
	case AWSProtocolQuery:
		q := r.URL.Query()
		if peek != nil && !peek.over {
			if form, err := url.ParseQuery(peek.buf.String()); err == nil {
				for k, vs := range form {
					q[k] = append(q[k], vs...)
				}
			}
		}
		if cl.operation == "" {
			cl.operation = q.Get("Action")
		}
		if cl.service == "" {
			cl.service = queryVersions[q.Get("Version")]
		}
		queryResource(q, &cl.resource)
		return queryParams(q), cl
	case AWSProtocolREST:
		return s3Params(r.URL.Query(), r.Header), cl
	}
	return nil, cl
}

// redactResource runs the line rules over the resource names.
func redactResource(res *AWSResource) int {
	n := 0
	for _, f := range []*string{&res.Bucket, &res.Key, &res.Table, &res.Index, &res.Queue, &res.Topic, &res.Stream} {
		if len(*f) > 1024 {
			*f = (*f)[:1024]
		}
		var c int
		*f, c = redact.Line(*f, nil)
		n += c
	}
	return n
}

// RedactAWSCall runs the redaction rules over a call again and returns how
// many values it replaced. Serve runs it before a call leaves the host.
func RedactAWSCall(c *AWSCall) int {
	n := redactResource(&c.Resource)
	var k int
	c.Error, k = redact.Line(c.Error, nil)
	n += k
	c.Client, k = redact.Line(c.Client, nil)
	n += k
	if c.Params != "" {
		if p, ok := jsonParams([]byte(c.Params)); ok {
			text, _, k := finishParams(p)
			c.Params, n = text, n+k
		} else {
			c.Params, k = redact.Line(c.Params, nil)
			n += k
		}
	}
	if c.AccessKey != "" && c.AccessKey != "test" {
		c.AccessKey, c.ForeignKey = "", true
	}
	return n
}

func durationMS(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Calls reads up to limit calls with an ID above after, oldest first.
func (t *AWSTap) Calls(after int64, limit int) ([]AWSCall, AWSCallsInfo) {
	calls, info := t.store.read(after, limit, t.now())
	for i := range calls {
		if calls[i].Client == "" {
			calls[i].Client = t.clients.name(calls[i].clientIP, calls[i].applier)
		}
	}
	info.Service = t.name
	return calls, info
}

// Serve answers on ln until Close.
func (t *AWSTap) Serve(ln net.Listener) error {
	err := t.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the tap.
func (t *AWSTap) Close() error { return t.srv.Close() }

// peekBody counts a request body as it streams to the emulator and keeps
// its first awsBodyPeek bytes when keep is set.
type peekBody struct {
	rc   io.ReadCloser
	keep bool
	buf  bytes.Buffer
	n    int64
	over bool
}

func (p *peekBody) Read(b []byte) (int, error) {
	n, err := p.rc.Read(b)
	p.n += int64(n)
	if p.keep && n > 0 && !p.over {
		if p.buf.Len()+n > awsBodyPeek {
			p.over = true
			p.buf.Reset()
		} else {
			p.buf.Write(b[:n])
		}
	}
	return n, err
}

func (p *peekBody) Close() error { return p.rc.Close() }

// awsRecorder counts the answer and keeps the head of a failed one.
type awsRecorder struct {
	http.ResponseWriter
	status int
	n      int64
	head   bytes.Buffer
	err    error
}

// WriteHeader keeps the first final status: an informational answer such
// as 100 Continue passes through unrecorded.
func (r *awsRecorder) WriteHeader(code int) {
	if r.status == 0 && code >= 200 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *awsRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 300 && r.head.Len() < awsErrorPeek {
		r.head.Write(b[:min(len(b), awsErrorPeek-r.head.Len())])
	}
	n, err := r.ResponseWriter.Write(b)
	r.n += int64(n)
	return n, err
}

// Flush keeps streaming answers streaming.
func (r *awsRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection.
func (r *awsRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// clientNames maps a caller's address to its compose service through the
// inner network's reverse lookup, cached per address.
type clientNames struct {
	lookup func(ctx context.Context, addr string) ([]string, error)
	mu     sync.Mutex
	names  map[string]clientName
}

type clientName struct {
	name string
	at   time.Time
	busy bool
}

// clientNameTTL is how long a lookup's answer, or its failure, stands.
const clientNameTTL = time.Minute

// resolve looks addr up unless a fresh answer or a lookup in flight exists.
func (c *clientNames) resolve(addr string) {
	if addr == "" || c.lookup == nil {
		return
	}
	c.mu.Lock()
	if c.names == nil {
		c.names = map[string]clientName{}
	}
	cur, ok := c.names[addr]
	if ok && (cur.busy || time.Since(cur.at) < clientNameTTL) {
		c.mu.Unlock()
		return
	}
	cur.busy = true
	c.names[addr] = cur
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ptrs, err := c.lookup(ctx, addr)
	name := ""
	if err == nil && len(ptrs) > 0 {
		name = composeService(ptrs[0])
	}
	c.mu.Lock()
	if name == "" {
		name = cur.name
	}
	c.names[addr] = clientName{name: name, at: time.Now()}
	c.mu.Unlock()
}

// forget drops addr's cached name, so the next caller there is looked up
// afresh, and returns the name it dropped.
func (c *clientNames) forget(addr string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.names[addr]
	if !ok || cur.busy {
		return ""
	}
	delete(c.names, addr)
	return cur.name
}

// name is addr's client name: "aiman" for the applier, else the service
// from the lookup, else the address.
func (c *clientNames) name(addr string, applier bool) string {
	if applier {
		return AWSClientAiman
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.names[addr].name; n != "" {
		return n
	}
	return addr
}

// replicaSuffix is compose's container suffix: -<n> for a service's
// container, -run-<hex> for a `compose run` one.
var replicaSuffix = regexp.MustCompile(`-(\d+|run-[0-9a-f]+)$`)

// composeService turns Docker's PTR answer for a container
// (<project>-<service>-<n>.<project>_<network>.) into its compose service;
// any other answer keeps its first label.
func composeService(ptr string) string {
	ptr = strings.TrimSuffix(ptr, ".")
	label, network, _ := strings.Cut(ptr, ".")
	if project, _, ok := strings.Cut(network, "_"); ok && strings.HasPrefix(label, project+"-") {
		return replicaSuffix.ReplaceAllString(strings.TrimPrefix(label, project+"-"), "")
	}
	return label
}

// awsCallStore keeps the newest MaxAWSCalls calls. A call is stored when
// it starts and updated in place when it ends.
type awsCallStore struct {
	mu    sync.Mutex
	calls []*AWSCall
	next  int64
}

func (s *awsCallStore) add(c *AWSCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	c.ID = s.next
	s.calls = append(s.calls, c)
	if len(s.calls) > MaxAWSCalls {
		s.calls[0] = nil
		s.calls = s.calls[1:]
	}
}

// nameFrom fixes the client of every stored call from addr that has none:
// name, else the address.
func (s *awsCallStore) nameFrom(addr, name string) {
	if addr == "" {
		return
	}
	if name == "" {
		name = addr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.clientIP == addr && c.Client == "" && !c.applier {
			c.Client = name
		}
	}
}

func (s *awsCallStore) update(c *AWSCall, fn func(*AWSCall)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(c)
}

// read copies up to limit calls with an ID above after, oldest first. A
// pending call younger than pendingHold holds the cursor, so the next read
// returns it again once it has ended.
func (s *awsCallStore) read(after int64, limit int, now time.Time) ([]AWSCall, AWSCallsInfo) {
	if limit <= 0 {
		limit = DefaultAWSCallsLimit
	}
	limit = min(limit, MaxAWSCallsLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	info := AWSCallsInfo{Total: s.next, Next: after}
	if len(s.calls) > 0 {
		info.Dropped = s.calls[0].ID - 1
	}
	out := []AWSCall{}
	size := 0
	hold, held := int64(0), false
	for _, c := range s.calls {
		if c.ID <= after {
			continue
		}
		if len(out) >= limit {
			info.More = true
			break
		}
		cp := *c
		sz := len(cp.Params) + 512
		if len(out) > 0 && size+sz > maxAWSCallsReply {
			info.More = true
			break
		}
		size += sz
		out = append(out, cp)
		info.Next = c.ID
		if !held && c.Pending && now.Sub(c.Start) < pendingHold {
			hold, held = c.ID-1, true
		}
	}
	if held {
		info.Next = hold
	}
	return out, info
}

// SetAWSTap attaches the AWS tap whose call log the control socket reads.
// Its owner closes it.
func (e *Edge) SetAWSTap(t *AWSTap) {
	e.mu.Lock()
	e.awsTap = t
	e.mu.Unlock()
}

func (e *Edge) awsCallTap() *AWSTap {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.awsTap
}

// AWSCalls reads up to limit calls of the tap with an ID above after.
func (c Client) AWSCalls(ctx context.Context, after int64, limit int) ([]AWSCall, AWSCallsInfo, error) {
	resp, err := c.call(ctx, Request{Op: OpAWSCalls, After: after, Limit: limit})
	if err != nil {
		return nil, AWSCallsInfo{}, err
	}
	if resp.AWSInfo == nil {
		return nil, AWSCallsInfo{}, errors.New("edge aws_calls: reply has no call log")
	}
	if resp.AWSCalls == nil {
		resp.AWSCalls = []AWSCall{}
	}
	return resp.AWSCalls, *resp.AWSInfo, nil
}
