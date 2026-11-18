package awsiac

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Operator write operations (harness.aws_write). Serve checks and types
// each request; the applier only sends it.
const (
	WriteS3Put     = "s3_put"
	WriteS3Delete  = "s3_delete"
	WriteDDBPut    = "ddb_put"
	WriteDDBDelete = "ddb_delete"
	WriteSQSSend   = "sqs_send"
	WriteReset     = "reset"
)

// WriteOps lists the operator write operations.
var WriteOps = []string{WriteS3Put, WriteS3Delete, WriteDDBPut, WriteDDBDelete, WriteSQSSend, WriteReset}

// WriteAgent is the User-Agent of an operator write; the edge tap logs it
// as client aiman/operator. The plan a reset applies again keeps UserAgent.
const WriteAgent = "aiman-harness-aws-write/1"

// ResetPath is MiniStack's admin endpoint that wipes every service's state.
const ResetPath = "/_ministack/reset"

// Write is one operator write as serve hands it to the applier: names and
// values are already checked, items and keys are AttributeValue JSON.
type Write struct {
	Op       string `json:"op"`
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`

	Bucket      string `json:"bucket,omitempty"`
	Key         string `json:"key,omitempty"`
	Version     string `json:"version,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data,omitempty"`

	Table   string         `json:"table,omitempty"`
	Item    map[string]any `json:"item,omitempty"`
	ItemKey map[string]any `json:"item_key,omitempty"`

	Queue      string            `json:"queue,omitempty"`
	Body       string            `json:"body,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	GroupID    string            `json:"group_id,omitempty"`
	DedupID    string            `json:"dedup_id,omitempty"`

	// Plan is applied again after a reset; nil or no steps applies nothing.
	Plan *Plan `json:"plan,omitempty"`
}

// WriteResult is what the emulator answered. Values are never echoed: an
// S3 write gives its ETag and version id, a delete whether something was
// there, a message its id.
type WriteResult struct {
	Op           string       `json:"op"`
	Status       int          `json:"status,omitempty"`
	ETag         string       `json:"etag,omitempty"`
	VersionID    string       `json:"version_id,omitempty"`
	DeleteMarker bool         `json:"delete_marker,omitempty"`
	Found        *bool        `json:"found,omitempty"`
	MessageID    string       `json:"message_id,omitempty"`
	Failed       *StepResult  `json:"failed,omitempty"`
	Apply        *ApplyResult `json:"apply,omitempty"`
}

// Write sends one operator write. A refused call sets Failed and returns
// its error; a reset whose plan fails returns the reset's result with the
// apply's.
func (a *Applier) Write(ctx context.Context, w *Write) (*WriteResult, error) {
	a.defaults(w.Endpoint)
	if a.Agent == "" {
		a.Agent = WriteAgent
	}
	res := &WriteResult{Op: w.Op}
	sr := StepResult{N: 1, Service: writeService(w.Op), Operation: w.Op}
	fail := func(err error) (*WriteResult, error) {
		if sr.Message == "" {
			sr.Message = err.Error()
		}
		res.Status = sr.Status
		res.Failed = &sr
		return res, err
	}
	var err error
	switch w.Op {
	case WriteS3Put:
		sr.Operation = "PutObject"
		c := call{service: "s3", method: "PUT", bucket: w.Bucket, key: w.Key, header: map[string]string{"Content-Type": w.ContentType}}
		if c.header["Content-Type"] == "" {
			c.header["Content-Type"] = "application/octet-stream"
		}
		_, err = a.send(ctx, w.Region, c, bytes.NewReader(w.Data), int64(len(w.Data)), hashHex(w.Data), &sr)
		if err == nil {
			res.ETag = strings.Trim(sr.header.Get("ETag"), `"`)
			res.VersionID = sr.header.Get("X-Amz-Version-Id")
		}
	case WriteS3Delete:
		sr.Operation = "DeleteObject"
		c := call{service: "s3", method: "DELETE", bucket: w.Bucket, key: w.Key}
		if w.Version != "" {
			c.query = url.Values{"versionId": {w.Version}}
		}
		_, err = a.do(ctx, w.Region, c, &sr)
		if err == nil {
			res.VersionID = sr.header.Get("X-Amz-Version-Id")
			res.DeleteMarker = sr.header.Get("X-Amz-Delete-Marker") == "true"
		}
	case WriteDDBPut:
		sr.Operation = "PutItem"
		err = a.jsonWrite(ctx, w.Region, "dynamodb", dynamoTarget+"PutItem", map[string]any{"TableName": w.Table, "Item": w.Item}, &sr, nil)
	case WriteDDBDelete:
		sr.Operation = "DeleteItem"
		var out struct {
			Attributes map[string]any `json:"Attributes"`
		}
		err = a.jsonWrite(ctx, w.Region, "dynamodb", dynamoTarget+"DeleteItem",
			map[string]any{"TableName": w.Table, "Key": w.ItemKey, "ReturnValues": "ALL_OLD"}, &sr, &out)
		if err == nil {
			found := len(out.Attributes) > 0
			res.Found = &found
		}
	case WriteSQSSend:
		sr.Operation = "SendMessage"
		res.MessageID, err = a.sendMessage(ctx, w, &sr)
	case WriteReset:
		sr.Operation = ResetPath
		_, err = a.do(ctx, w.Region, call{service: "admin", method: "POST", path: ResetPath, unsigned: true}, &sr)
		if err == nil && w.Plan != nil && len(w.Plan.Steps) > 0 {
			res.Status = sr.Status
			agent := a.Agent
			a.Agent = UserAgent
			res.Apply, err = a.Apply(ctx, w.Plan)
			a.Agent = agent
			return res, err
		}
	default:
		err = fmt.Errorf("unknown write op %q", w.Op)
	}
	if err != nil {
		return fail(err)
	}
	res.Status = sr.Status
	return res, nil
}

func writeService(op string) string {
	switch {
	case strings.HasPrefix(op, "s3_"):
		return "s3"
	case strings.HasPrefix(op, "ddb_"):
		return "dynamodb"
	case strings.HasPrefix(op, "sqs_"):
		return "sqs"
	}
	return "admin"
}

// jsonWrite sends one JSON-protocol call and decodes the answer into out
// (nil ignores it).
func (a *Applier) jsonWrite(ctx context.Context, region, service, target string, in any, sr *StepResult, out any) error {
	c, err := jsonCall(service, target, in)
	if err != nil {
		return err
	}
	body, err := a.do(ctx, region, c, sr)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// sendMessage sends w's message, finding the queue's URL by name first. A
// FIFO queue gets the group "operator" and a deduplication id from the body
// and the clock unless w names them.
func (a *Applier) sendMessage(ctx context.Context, w *Write, sr *StepResult) (string, error) {
	queueURL := w.Queue
	if !strings.HasPrefix(queueURL, "http://") && !strings.HasPrefix(queueURL, "https://") {
		var out struct {
			URL string `json:"QueueUrl"`
		}
		if err := a.jsonWrite(ctx, w.Region, "sqs", sqsTarget+"GetQueueUrl", map[string]string{"QueueName": w.Queue}, sr, &out); err != nil {
			return "", err
		}
		if out.URL == "" {
			return "", errors.New("GetQueueUrl gave no URL")
		}
		queueURL = out.URL
	}
	in := map[string]any{"QueueUrl": queueURL, "MessageBody": w.Body}
	if len(w.Attributes) > 0 {
		attrs := map[string]any{}
		for k, v := range w.Attributes {
			attrs[k] = map[string]string{"DataType": "String", "StringValue": v}
		}
		in["MessageAttributes"] = attrs
	}
	group, dedup := w.GroupID, w.DedupID
	if strings.HasSuffix(path.Base(queueURL), ".fifo") {
		if group == "" {
			group = "operator"
		}
		if dedup == "" {
			sum := sha256.Sum256([]byte(w.Body + "\x00" + strconv.FormatInt(a.Now().UnixNano(), 10)))
			dedup = hex.EncodeToString(sum[:16])
		}
	}
	if group != "" {
		in["MessageGroupId"] = group
	}
	if dedup != "" {
		in["MessageDeduplicationId"] = dedup
	}
	var out struct {
		ID string `json:"MessageId"`
	}
	if err := a.jsonWrite(ctx, w.Region, "sqs", sqsTarget+"SendMessage", in, sr, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}
