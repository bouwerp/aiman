package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
	"github.com/bouwerp/aiman/internal/redact"
)

// Operator write bounds.
const (
	// AWSWriteTimeout bounds one operator write; AWSResetTimeout a reset,
	// which applies the plan and seed again.
	AWSWriteTimeout = 60 * time.Second
	AWSResetTimeout = 5 * time.Minute
	// MaxAWSWriteData caps an s3_put object.
	MaxAWSWriteData = 16 << 20
	// maxAWSItemBytes caps a ddb_put item or ddb_delete key as JSON
	// (DynamoDB's own item limit is 400 KB).
	maxAWSItemBytes = 400 << 10
	// maxAWSMessageBytes caps an sqs_send body (SQS's limit).
	maxAWSMessageBytes = 256 << 10
	maxAWSMessageAttrs = 10
	maxS3KeyBytes      = 1024
)

var (
	ddbTableNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)
	sqsQueueNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,75}(\.fifo)?$`)
	sqsAttrNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)
	sqsIDRe        = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
)

// AWSWriteRequest is one operator write to a ready run's aws service.
// Item and ItemKey are plain JSON (numbers as json.Number); serve types
// them from the table's schema.
type AWSWriteRequest struct {
	Run     string
	Service string
	Op      string

	Bucket      string
	Key         string
	Version     string
	ContentType string
	Data        []byte

	Table   string
	Item    map[string]any
	ItemKey map[string]any

	Queue      string
	Body       string
	Attributes map[string]string
	GroupID    string
	DedupID    string

	// Operator is false for a session caller, which is refused.
	Operator bool
	Actor    domain.EventActor
}

// AWSWriteResult is the harness_aws_write reply. It never holds the values
// written: names, the emulator's ids, and for a reset what was applied.
type AWSWriteResult struct {
	Run     string `json:"run"`
	Service string `json:"service"`
	Op      string `json:"op"`

	Bucket string `json:"bucket,omitempty"`
	Table  string `json:"table,omitempty"`
	Queue  string `json:"queue,omitempty"`

	ETag         string `json:"etag,omitempty"`
	VersionID    string `json:"version_id,omitempty"`
	DeleteMarker bool   `json:"delete_marker,omitempty"`
	Found        *bool  `json:"found,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`

	// Reset: the plan's resources and the calls applied again, the env
	// variables whose value changed (the running app keeps the old one)
	// and the step that failed.
	Resources  int      `json:"resources,omitempty"`
	Calls      int      `json:"calls,omitempty"`
	EnvChanged []string `json:"env_changed,omitempty"`
	FailedStep string   `json:"failed_step,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// AWSWrite runs one operator write (harness.aws_write) through the
// applier container: the request on stdin, signed with the run's test key
// and sent to the edge tap, which logs it as client aiman/operator. It
// records harness.aws_write with hashes of the key and payload, never the
// values.
func (m *Manager) AWSWrite(ctx context.Context, req AWSWriteRequest) (AWSWriteResult, error) {
	if !req.Operator {
		return AWSWriteResult{}, &Error{Code: CodeNeedsOperator, Msg: "only the operator writes to a harness aws service"}
	}
	if !slices.Contains(awsiac.WriteOps, req.Op) {
		return AWSWriteResult{}, paramsError("op must be one of " + strings.Join(awsiac.WriteOps, ", "))
	}
	if err := checkAWSWrite(req); err != nil {
		return AWSWriteResult{}, err
	}
	run, service, c, err := m.awsTarget(req.Run, req.Service)
	if err != nil {
		return AWSWriteResult{}, err
	}
	lr, err := m.readyRunOf(run.ID)
	if err != nil {
		return AWSWriteResult{}, err
	}
	m.Touch(run.ID)
	timeout := AWSWriteTimeout
	if req.Op == awsiac.WriteReset {
		timeout = AWSResetTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lr.awsWrite.Lock()
	defer lr.awsWrite.Unlock()
	lr.mu.Lock()
	p := lr.awsPlan
	var planJSON []byte
	if p != nil {
		planJSON, err = json.Marshal(p)
	}
	lr.mu.Unlock()
	if err != nil {
		return AWSWriteResult{}, err
	}
	if p == nil || p.Service != service {
		return AWSWriteResult{}, fmt.Errorf("run %s has no aws plan for %s", run.ID, service)
	}
	w := awsiac.Write{
		Op: req.Op, Endpoint: p.Endpoint, Region: p.Region,
		Bucket: req.Bucket, Key: req.Key, Version: req.Version, ContentType: req.ContentType, Data: req.Data,
		Table: req.Table, Queue: req.Queue, Body: req.Body, Attributes: req.Attributes, GroupID: req.GroupID, DedupID: req.DedupID,
	}
	res := AWSWriteResult{Run: run.ID, Service: service, Op: req.Op}
	ledger := map[string]any{"run": run.ID, "service": service, "op": req.Op}
	switch req.Op {
	case awsiac.WriteS3Put, awsiac.WriteS3Delete:
		res.Bucket = req.Bucket
		ledger["bucket"] = req.Bucket
		ledger["key_sha256"] = sha256Hex([]byte(req.Key))
		if req.Op == awsiac.WriteS3Put {
			ledger["payload_sha256"] = sha256Hex(req.Data)
			ledger["bytes"] = len(req.Data)
			res.Bytes = len(req.Data)
		}
	case awsiac.WriteDDBPut, awsiac.WriteDDBDelete:
		res.Table = req.Table
		ledger["table"] = req.Table
		typed, err := typeDDBWrite(ctx, c, req, &w)
		if err != nil {
			return AWSWriteResult{}, err
		}
		key, _ := json.Marshal(typed)
		ledger["key_sha256"] = sha256Hex(key)
		if req.Op == awsiac.WriteDDBPut {
			item, _ := json.Marshal(w.Item)
			ledger["payload_sha256"] = sha256Hex(item)
		}
	case awsiac.WriteSQSSend:
		res.Queue = req.Queue
		ledger["queue"] = req.Queue
		ledger["payload_sha256"] = sha256Hex([]byte(req.Body))
		ledger["bytes"] = len(req.Body)
		res.Bytes = len(req.Body)
	case awsiac.WriteReset:
		var copied awsiac.Plan
		if err := json.Unmarshal(planJSON, &copied); err != nil {
			return AWSWriteResult{}, err
		}
		w.Plan = &copied
	}

	out, err := m.runAWSWrite(ctx, run, service, &w)
	if err != nil {
		return AWSWriteResult{}, err
	}
	if f := out.Failed; f != nil {
		return AWSWriteResult{}, awsWriteRefusal(f)
	}
	res.ETag, res.VersionID, res.DeleteMarker, res.Found, res.MessageID = out.ETag, out.VersionID, out.DeleteMarker, out.Found, out.MessageID
	if req.Op == awsiac.WriteReset {
		m.afterReset(lr, run, p, out.Apply, &res)
		ledger["resources"], ledger["calls"] = res.Resources, res.Calls
		if res.FailedStep != "" {
			ledger["failed_step"] = res.FailedStep
		}
	}
	for _, k := range []string{"bucket", "table", "queue"} {
		if v, ok := ledger[k].(string); ok {
			ledger[k], _ = redact.Line(v, nil)
		}
	}
	m.record(run.Session, req.Actor, domain.EventHarnessAWSWrite, ledger)
	res.Bucket, _ = redact.Line(res.Bucket, nil)
	res.Table, _ = redact.Line(res.Table, nil)
	res.Queue, _ = redact.Line(res.Queue, nil)
	return res, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkAWSWrite checks a write's names and sizes before anything runs.
func checkAWSWrite(req AWSWriteRequest) error {
	switch req.Op {
	case awsiac.WriteS3Put, awsiac.WriteS3Delete:
		if !s3BucketNameRe.MatchString(req.Bucket) {
			return paramsError(fmt.Sprintf("bucket %q is not an S3 bucket name", req.Bucket))
		}
		if err := checkS3WriteKey(req.Key); err != nil {
			return err
		}
		if req.Op == awsiac.WriteS3Put {
			if len(req.Data) > MaxAWSWriteData {
				return paramsError(fmt.Sprintf("the object is over %d bytes", MaxAWSWriteData))
			}
			if len(req.ContentType) > 256 || strings.ContainsFunc(req.ContentType, isControl) {
				return paramsError("content_type is one line of at most 256 bytes")
			}
		} else if len(req.Version) > 1024 || strings.ContainsFunc(req.Version, isControl) {
			return paramsError("version is not an S3 version id")
		}
	case awsiac.WriteDDBPut, awsiac.WriteDDBDelete:
		if !ddbTableNameRe.MatchString(req.Table) {
			return paramsError(fmt.Sprintf("table %q is not a DynamoDB table name", req.Table))
		}
		v, what := req.Item, "item"
		if req.Op == awsiac.WriteDDBDelete {
			v, what = req.ItemKey, "key"
		}
		if len(v) == 0 {
			return paramsError(req.Op + " needs " + what + ", a JSON object")
		}
		if b, err := json.Marshal(v); err != nil || len(b) > maxAWSItemBytes {
			return paramsError(fmt.Sprintf("%s is over %d bytes as JSON", what, maxAWSItemBytes))
		}
	case awsiac.WriteSQSSend:
		if !sqsQueueNameRe.MatchString(req.Queue) {
			return paramsError(fmt.Sprintf("queue %q is not an SQS queue name", req.Queue))
		}
		if req.Body == "" || len(req.Body) > maxAWSMessageBytes || !utf8.ValidString(req.Body) {
			return paramsError(fmt.Sprintf("body is UTF-8 text of 1 to %d bytes", maxAWSMessageBytes))
		}
		if len(req.Attributes) > maxAWSMessageAttrs {
			return paramsError(fmt.Sprintf("at most %d message attributes", maxAWSMessageAttrs))
		}
		for k, v := range req.Attributes {
			if !sqsAttrNameRe.MatchString(k) || strings.HasPrefix(strings.ToLower(k), "aws.") || v == "" || len(v) > 4096 {
				return paramsError(fmt.Sprintf("message attribute %q: a name of [A-Za-z0-9_.-] and a value of 1 to 4096 bytes", k))
			}
		}
		for name, v := range map[string]string{"group_id": req.GroupID, "dedup_id": req.DedupID} {
			if v != "" && !sqsIDRe.MatchString(v) {
				return paramsError(name + " is 1 to 128 printable ASCII characters")
			}
		}
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// checkS3WriteKey refuses a key the read relay could not read back: empty,
// over 1024 bytes, not UTF-8, a control character, or a . or .. segment.
func checkS3WriteKey(key string) error {
	switch {
	case key == "" || len(key) > maxS3KeyBytes || !utf8.ValidString(key):
		return paramsError(fmt.Sprintf("key is UTF-8 of 1 to %d bytes", maxS3KeyBytes))
	case strings.ContainsFunc(key, isControl):
		return paramsError("key holds a control character")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return paramsError("key holds a . or .. segment")
		}
	}
	return nil
}

// typeDDBWrite turns a write's plain JSON into AttributeValues, typing the
// key attributes from the table's schema (read through the relay), so "42"
// is a number for a numeric key as ddb_get reads it. It sets w's item or
// key and returns the typed key.
func typeDDBWrite(ctx context.Context, c awsClient, req AWSWriteRequest, w *awsiac.Write) (map[string]any, error) {
	t, err := c.ddbDescribe(ctx, req.Table)
	if err != nil {
		return nil, awsFail(err)
	}
	keys, _ := t.keyNames("")
	src, what := req.ItemKey, "key"
	if req.Op == awsiac.WriteDDBPut {
		src, what = req.Item, "item"
	} else if len(req.ItemKey) != len(keys) {
		return nil, paramsError(fmt.Sprintf("key holds exactly %v", keys))
	}
	key := map[string]any{}
	for _, k := range keys {
		v, ok := src[k]
		if !ok {
			return nil, paramsError(fmt.Sprintf("the %s needs the key attributes %v", what, keys))
		}
		av, err := toAttr(v, t.Attributes[k])
		if err != nil {
			return nil, paramsError(fmt.Sprintf("key %s: %v", k, err))
		}
		key[k] = av
	}
	if req.Op == awsiac.WriteDDBDelete {
		w.ItemKey = key
		return key, nil
	}
	b, err := json.Marshal(req.Item)
	if err != nil {
		return nil, paramsError("item: " + err.Error())
	}
	item, err := awsiac.ItemAttributes(b, "plain")
	if err != nil {
		return nil, paramsError("item: " + err.Error())
	}
	maps.Copy(item, key)
	w.Item = item
	return key, nil
}

// runAWSWrite runs the applier container with w on stdin.
func (m *Manager) runAWSWrite(ctx context.Context, run Run, service string, w *awsiac.Write) (*awsiac.WriteResult, error) {
	in, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	out, exit, err := m.opts.Docker.Compose(ctx, ComposeCall{
		Dir: filepath.Join(m.runDir(run.Session, run.ID), "sandbox"), Project: "harness-" + run.ID,
		Args:  []string{"run", "--rm", "-T", "--no-deps", AWSApplyService(service), "harness", "aws-apply", awsApplyWriteArg},
		Stdin: bytes.NewReader(in),
	})
	if err != nil {
		return nil, err
	}
	reply, perr := parseApplyOutput(out)
	switch {
	case perr != nil:
		line, _ := redact.Line(oneLine(tailLines(out, 3), 300), nil)
		return nil, fmt.Errorf("aws write: applier exit %d: %v: %s", exit, perr, line)
	case reply.Error != "":
		return nil, errors.New("aws write: " + reply.Error)
	case reply.Write == nil:
		return nil, errors.New("aws write: the applier gave no result")
	}
	return reply.Write, nil
}

// awsWriteRefusal turns a refused write into an error: the emulator's 4xx
// is the caller's (invalid_params), anything else the service's.
func awsWriteRefusal(f *awsiac.StepResult) error {
	he := &awsHTTPError{Status: f.Status, Code: f.Code, Message: f.Message}
	if f.Status == 0 {
		line, _ := redact.Line(oneLine(f.Message, 300), nil)
		return fmt.Errorf("aws write %s: %s", f.Operation, line)
	}
	return awsFail(he)
}

// afterReset learns what the plan applied again gave (stream and
// subscription ARNs), writes the manifest and compose file again, and
// fills the reply. A failed step of the apply is reported, not returned:
// the emulator was reset and the run stays ready.
func (m *Manager) afterReset(lr *liveRun, run Run, p *awsiac.Plan, ap *awsiac.ApplyResult, res *AWSWriteResult) {
	res.Resources = len(p.Resources)
	if ap == nil {
		if len(p.Steps) == 0 {
			res.Note = "the plan has no steps; the emulator is empty"
		}
		return
	}
	res.Calls = len(ap.Steps)
	if ap.Failed != nil {
		res.FailedStep = ap.Failed.Label()
		msg, _ := redact.Line(oneLine(strings.TrimSpace(ap.Failed.Code+" "+ap.Failed.Message), 300), nil)
		res.Note = "the plan stopped at " + res.FailedStep + ": " + msg
	}
	lr.mu.Lock()
	for key, attrs := range ap.Learned {
		for k, v := range attrs {
			p.Learn(key, k, v)
		}
	}
	before := lr.spec.AWSAppEnv
	env, err := p.ResolveEnv()
	sandbox := filepath.Join(m.runDir(run.Session, run.ID), "sandbox")
	if err == nil {
		if merr := writeAWSManifest(sandbox, p); merr != nil {
			m.logf("run %s: aws manifest after reset: %v", run.ID, merr)
		}
	}
	lr.mu.Unlock()
	if err != nil {
		m.logf("run %s: aws env after reset: %v", run.ID, err)
		return
	}
	for k, v := range env {
		if before[k] != v {
			res.EnvChanged = append(res.EnvChanged, k)
		}
	}
	slices.Sort(res.EnvChanged)
	if len(res.EnvChanged) == 0 {
		return
	}
	if err := rewriteAWSCompose(sandbox, lr, env); err != nil {
		m.logf("run %s: compose after reset: %v", run.ID, err)
	}
	note := "the running app keeps the old values of " + strings.Join(res.EnvChanged, ", ") + "; checks started now see the new ones"
	if res.Note != "" {
		note = res.Note + "; " + note
	}
	res.Note = note
}
