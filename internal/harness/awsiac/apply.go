package awsiac

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Applier runs a plan's steps against the emulator, signing every call
// with the run's fixed key, and stops at the first failure.
type Applier struct {
	// Endpoint is the emulator (the edge tap); empty is the plan's.
	Endpoint string
	Key      string
	Secret   string
	// Client sends the calls; nil is a client with no proxy.
	Client *http.Client
	// Root is the repository tree the seed files are read from; symlinks
	// and paths outside it are refused.
	Root *os.Root
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// TableWait bounds the wait for a created table to turn ACTIVE; zero
	// is 30 seconds.
	TableWait time.Duration

	objects, items, messages int
	objectBytes              int64
}

// StepResult is one step as the applier ran it. Count is the objects,
// items or messages a seed step wrote.
type StepResult struct {
	N         int    `json:"n"`
	Resource  string `json:"resource,omitempty"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
	Status    int    `json:"status,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// Label names the step as a run's failed step does:
// aws <n>: <stack>.<logical id> <operation>.
func (s StepResult) Label() string {
	if s.Resource == "" {
		return fmt.Sprintf("aws %d: %s", s.N, s.Operation)
	}
	return fmt.Sprintf("aws %d: %s %s", s.N, s.Resource, s.Operation)
}

// ApplyResult is the applier's answer: each step that ran, the attributes
// it learned per resource key (a stream ARN, a subscription ARN), and the
// step that failed, if one did.
type ApplyResult struct {
	Steps   []StepResult                 `json:"steps"`
	Learned map[string]map[string]string `json:"learned,omitempty"`
	Failed  *StepResult                  `json:"failed,omitempty"`
}

// Apply runs every step of p in order. A step that fails ends the apply:
// the result names it and Apply returns its error.
func (a *Applier) Apply(ctx context.Context, p *Plan) (*ApplyResult, error) {
	if a.Client == nil {
		a.Client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Minute}
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Endpoint == "" {
		a.Endpoint = p.Endpoint
	}
	res := &ApplyResult{Steps: []StepResult{}, Learned: map[string]map[string]string{}}
	for _, st := range p.Steps {
		sr := StepResult{N: st.N, Resource: st.Resource, Service: st.Service, Operation: st.Operation}
		err := a.step(ctx, p, st, &sr, res)
		if err != nil {
			if sr.Message == "" {
				sr.Message = err.Error()
			}
			res.Steps = append(res.Steps, sr)
			res.Failed = &sr
			return res, fmt.Errorf("%s: %w", sr.Label(), err)
		}
		res.Steps = append(res.Steps, sr)
	}
	return res, nil
}

func (a *Applier) learn(p *Plan, res *ApplyResult, key, attr, value string) {
	if value == "" {
		return
	}
	p.Learn(key, attr, value)
	if res.Learned[key] == nil {
		res.Learned[key] = map[string]string{}
	}
	res.Learned[key][attr] = value
	if r := p.Resource(key); r != nil && attr == AttrStreamARN {
		res.Learned[key][AttrStreamLabel] = r.Attrs[AttrStreamLabel]
	}
}

func (a *Applier) step(ctx context.Context, p *Plan, st Step, sr *StepResult, res *ApplyResult) error {
	if st.Seed != nil {
		return a.seed(ctx, p, st, sr)
	}
	r := p.Resource(st.Resource)
	if r == nil {
		return fmt.Errorf("the plan has no resource %s", st.Resource)
	}
	c, err := p.resourceCall(st, r)
	if err != nil {
		return err
	}
	body, err := a.do(ctx, p, c, sr)
	if err != nil {
		return err
	}
	switch st.Operation {
	case "CreateTable":
		var out struct {
			TableDescription struct {
				LatestStreamArn string `json:"LatestStreamArn"`
				TableStatus     string `json:"TableStatus"`
			} `json:"TableDescription"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return fmt.Errorf("read CreateTable's answer: %w", err)
		}
		a.learn(p, res, r.Key(), AttrStreamARN, out.TableDescription.LatestStreamArn)
		if out.TableDescription.TableStatus != "ACTIVE" {
			if err := a.waitActive(ctx, p, r, res); err != nil {
				return err
			}
		}
		if r.Table.StreamViewType != "" && r.Attrs[AttrStreamARN] == "" {
			return errors.New("the emulator gave the table no stream ARN")
		}
	case "Subscribe":
		var out struct {
			Arn string `xml:"SubscribeResult>SubscriptionArn"`
		}
		if err := xml.Unmarshal(body, &out); err != nil || out.Arn == "" {
			return fmt.Errorf("read Subscribe's answer: %v", err)
		}
		a.learn(p, res, r.Key(), AttrRefValue, out.Arn)
		a.learn(p, res, r.Key(), AttrARN, out.Arn)
	}
	return nil
}

// waitActive polls DescribeTable until the table is ACTIVE.
func (a *Applier) waitActive(ctx context.Context, p *Plan, r *Resource, res *ApplyResult) error {
	wait := a.TableWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	deadline := a.Now().Add(wait)
	for {
		c, err := jsonCall("dynamodb", dynamoTarget+"DescribeTable", map[string]string{"TableName": r.Physical})
		if err != nil {
			return err
		}
		var sr StepResult
		body, err := a.do(ctx, p, c, &sr)
		if err != nil {
			return err
		}
		var out struct {
			Table struct {
				TableStatus     string `json:"TableStatus"`
				LatestStreamArn string `json:"LatestStreamArn"`
			} `json:"Table"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return err
		}
		a.learn(p, res, r.Key(), AttrStreamARN, out.Table.LatestStreamArn)
		if out.Table.TableStatus == "ACTIVE" {
			return nil
		}
		if a.Now().After(deadline) {
			return fmt.Errorf("table %s is still %s", r.Physical, out.Table.TableStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// UserAgent marks the applier's calls; the edge tap logs them as client
// aiman.
const UserAgent = "aiman-harness-aws-apply/1"

// maxAnswer bounds an answer the applier reads.
const maxAnswer = 4 << 20

// do signs and sends one call. A non-2xx answer is an error whose code and
// message come from the answer.
func (a *Applier) do(ctx context.Context, p *Plan, c call, sr *StepResult) ([]byte, error) {
	return a.send(ctx, p, c, bytes.NewReader(c.body), int64(len(c.body)), hashHex(c.body), sr)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *Applier) send(ctx context.Context, p *Plan, c call, body io.Reader, size int64, payloadHash string, sr *StepResult) ([]byte, error) {
	rawPath, plainPath := "/", "/"
	if c.bucket != "" {
		rawPath, plainPath = awsPath(c.bucket, c.key)
	}
	method := c.method
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.Endpoint, "/")+"/", body)
	if err != nil {
		return nil, err
	}
	req.URL.Path, req.URL.RawPath = plainPath, rawPath
	if len(c.query) > 0 {
		req.URL.RawQuery = canonicalQuery(c.query)
	}
	req.ContentLength = size
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", UserAgent)
	signV4(req, c.service, p.Region, a.Key, a.Secret, payloadHash, a.Now())
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	sr.Status = resp.StatusCode
	if resp.StatusCode/100 != 2 {
		sr.Code, sr.Message = errorOf(out)
		if sr.Code == "" {
			sr.Code = strconv.Itoa(resp.StatusCode)
		}
		return nil, fmt.Errorf("%s %s", sr.Code, sr.Message)
	}
	return out, nil
}

// errorOf reads the code and message of an error answer: JSON __type
// (after its #) and message, or XML Code and Message.
func errorOf(b []byte) (code, msg string) {
	var j struct {
		Type    string `json:"__type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		MsgCap  string `json:"Message"`
	}
	if json.Unmarshal(b, &j) == nil && (j.Type != "" || j.Code != "") {
		code = j.Type
		if i := strings.LastIndex(code, "#"); i >= 0 {
			code = code[i+1:]
		}
		if code == "" {
			code = j.Code
		}
		msg = j.Message
		if msg == "" {
			msg = j.MsgCap
		}
		return code, oneLine(msg)
	}
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
		Inner   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if xml.Unmarshal(b, &x) == nil {
		if x.Code == "" {
			x.Code, x.Message = x.Inner.Code, x.Inner.Message
		}
		return x.Code, oneLine(x.Message)
	}
	return "", oneLine(string(b))
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// seed runs one seed step.
func (a *Applier) seed(ctx context.Context, p *Plan, st Step, sr *StepResult) error {
	if a.Root == nil {
		return errors.New("no repository tree for the seed files")
	}
	s := p.Seed
	switch st.Seed.Kind {
	case SeedKindObjects:
		return a.seedObjects(ctx, p, s.Objects[st.Seed.Index], sr)
	case SeedKindItems:
		return a.seedItems(ctx, p, s.Items[st.Seed.Index], sr)
	case SeedKindMessages:
		return a.seedMessages(ctx, p, s.Messages[st.Seed.Index], sr)
	}
	return fmt.Errorf("unknown seed kind %q", st.Seed.Kind)
}

// regular opens a regular file of the tree, refusing a symlink.
func (a *Applier) regular(name string) (*os.File, int64, error) {
	fi, err := a.Root.Lstat(name)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file (symlinks are not read)", name)
	}
	f, err := a.Root.Open(name)
	return f, fi.Size(), err
}

func (a *Applier) seedObjects(ctx context.Context, p *Plan, e SeedObjects, sr *StepResult) error {
	from := path.Clean(e.From)
	fi, err := a.Root.Lstat(from)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", e.From)
	}
	return fs.WalkDir(a.Root.FS(), from, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; seed files are read without following links", name)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, size, err := a.regular(name)
		if err != nil {
			return err
		}
		defer f.Close()
		a.objects++
		a.objectBytes += size
		switch {
		case a.objects > MaxSeedObjects:
			return fmt.Errorf("the seed has more than %d objects", MaxSeedObjects)
		case a.objectBytes > MaxSeedObjectBytes:
			return fmt.Errorf("the seed's objects are over %d bytes", MaxSeedObjectBytes)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(name, from), "/")
		ct := e.ContentType
		if ct == "" || ct == "auto" {
			ct = mime.TypeByExtension(path.Ext(name))
			if ct == "" {
				ct = "application/octet-stream"
			}
		}
		c := call{service: "s3", method: "PUT", bucket: e.Physical, key: e.Prefix + rel, header: map[string]string{"Content-Type": ct}}
		if _, err := a.send(ctx, p, c, f, size, unsignedPayload, sr); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		sr.Count++
		return nil
	})
}

// lines calls fn with each non-empty line of a seed file.
func (a *Applier) lines(name string, fn func(n int, line []byte) error) error {
	f, _, err := a.regular(path.Clean(name))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), MaxSeedLineBytes)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(n, line); err != nil {
			return fmt.Errorf("%s:%d: %w", name, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// batchWrite is the most items one BatchWriteItem takes.
const batchWrite = 25

func (a *Applier) seedItems(ctx context.Context, p *Plan, e SeedItems, sr *StepResult) error {
	var batch []any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := a.writeItems(ctx, p, e.Physical, batch, sr)
		sr.Count += len(batch)
		batch = batch[:0]
		return err
	}
	err := a.lines(e.File, func(_ int, line []byte) error {
		item, err := ItemAttributes(line, e.Format)
		if err != nil {
			return err
		}
		if a.items++; a.items > MaxSeedItems {
			return fmt.Errorf("the seed has more than %d items", MaxSeedItems)
		}
		batch = append(batch, map[string]any{"PutRequest": map[string]any{"Item": item}})
		if len(batch) == batchWrite {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

// writeItems sends one batch, sending unprocessed items again a few times.
func (a *Applier) writeItems(ctx context.Context, p *Plan, table string, batch []any, sr *StepResult) error {
	reqs := append([]any(nil), batch...)
	for try := 0; try < 5 && len(reqs) > 0; try++ {
		c, err := jsonCall("dynamodb", dynamoTarget+"BatchWriteItem", map[string]any{"RequestItems": map[string]any{table: reqs}})
		if err != nil {
			return err
		}
		body, err := a.do(ctx, p, c, sr)
		if err != nil {
			return err
		}
		var out struct {
			Unprocessed map[string][]any `json:"UnprocessedItems"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return err
		}
		reqs = out.Unprocessed[table]
	}
	if len(reqs) > 0 {
		return fmt.Errorf("%d items stayed unprocessed", len(reqs))
	}
	return nil
}

// sendBatch is the most messages one SendMessageBatch takes.
const sendBatch = 10

func (a *Applier) seedMessages(ctx context.Context, p *Plan, e SeedMessages, sr *StepResult) error {
	var batch []map[string]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		c, err := jsonCall("sqs", sqsTarget+"SendMessageBatch", map[string]any{"QueueUrl": e.URL, "Entries": batch})
		if err != nil {
			return err
		}
		body, err := a.do(ctx, p, c, sr)
		if err != nil {
			return err
		}
		var out struct {
			Failed []struct {
				ID      string `json:"Id"`
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Failed"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return err
		}
		if len(out.Failed) > 0 {
			f := out.Failed[0]
			sr.Code, sr.Message = f.Code, oneLine(f.Message)
			return fmt.Errorf("%d messages failed, first %s: %s", len(out.Failed), f.Code, f.Message)
		}
		sr.Count += len(batch)
		batch = batch[:0]
		return nil
	}
	err := a.lines(e.File, func(n int, line []byte) error {
		m, err := ParseMessageLine(line)
		if err != nil {
			return err
		}
		if a.messages++; a.messages > MaxSeedMessages {
			return fmt.Errorf("the seed has more than %d messages", MaxSeedMessages)
		}
		entry := map[string]any{"Id": "m" + strconv.Itoa(n), "MessageBody": m.Body}
		if len(m.Attributes) > 0 {
			attrs := map[string]any{}
			for k, v := range m.Attributes {
				attrs[k] = map[string]string{"DataType": "String", "StringValue": v}
			}
			entry["MessageAttributes"] = attrs
		}
		if e.Fifo {
			if m.GroupID == "" {
				m.GroupID = "seed"
			}
			if m.DedupID == "" {
				m.DedupID = "seed-" + strconv.Itoa(n)
			}
		}
		if m.GroupID != "" {
			entry["MessageGroupId"] = m.GroupID
		}
		if m.DedupID != "" {
			entry["MessageDeduplicationId"] = m.DedupID
		}
		batch = append(batch, entry)
		if len(batch) == sendBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}
