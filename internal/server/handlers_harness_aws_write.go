package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

// HarnessAWSWriteTimeout bounds one harness.aws_write; HarnessAWSResetTimeout
// a reset, which applies the plan and seed again. The client waits for the
// longer.
const (
	HarnessAWSWriteTimeout = harness.AWSWriteTimeout
	HarnessAWSResetTimeout = harness.AWSResetTimeout
)

// harnessAWSWriteParams are harness.aws_write's parameters. Key is an S3
// object key (a string) for the s3 ops and the item's key attributes (an
// object) for ddb_delete; data is the object, base64.
type harnessAWSWriteParams struct {
	Session     string            `json:"session"`
	Run         string            `json:"run"`
	Service     string            `json:"service"`
	Op          string            `json:"op"`
	Bucket      string            `json:"bucket"`
	Key         json.RawMessage   `json:"key"`
	Version     string            `json:"version"`
	ContentType string            `json:"content_type"`
	Data        string            `json:"data"`
	Table       string            `json:"table"`
	Item        map[string]any    `json:"item"`
	Queue       string            `json:"queue"`
	Body        string            `json:"body"`
	Attributes  map[string]string `json:"attributes"`
	GroupID     string            `json:"group_id"`
	DedupID     string            `json:"dedup_id"`
}

// handleHarnessAWSWrite answers harness.aws_write {run|session, service?,
// op, …}: the operator's write to a ready run's aws service, or a reset.
// A session caller is refused before the params or the run are looked at.
func (s *Server) handleHarnessAWSWrite(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator writes to a harness aws service; the harness console and `aiman harness aws write` from a terminal outside a session are the operator's")
	}
	var p harnessAWSWriteParams
	if len(req.Params) > 0 {
		dec := json.NewDecoder(bytes.NewReader(req.Params))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		p.Session = req.Caller
	}
	op := strings.TrimSpace(p.Op)
	if op == "" {
		return errResp(req.ID, CodeInvalidParams, "op is required: one of "+strings.Join(awsiac.WriteOps, ", "))
	}
	w := harness.AWSWriteRequest{
		Service: strings.TrimSpace(p.Service), Op: op,
		Bucket: p.Bucket, Version: p.Version, ContentType: p.ContentType,
		Table: p.Table, Item: p.Item,
		Queue: p.Queue, Body: p.Body, Attributes: p.Attributes, GroupID: p.GroupID, DedupID: p.DedupID,
		Operator: true, Actor: s.harnessActor(ctx, req),
	}
	if p.Data != "" {
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, "data must be base64: "+err.Error())
		}
		w.Data = data
	}
	if k := bytes.TrimSpace(p.Key); len(k) > 0 && !bytes.Equal(k, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(k))
		dec.UseNumber()
		var err error
		if k[0] == '{' {
			err = dec.Decode(&w.ItemKey)
		} else {
			err = dec.Decode(&w.Key)
		}
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, "key must be an S3 key string or a DynamoDB key object: "+err.Error())
		}
	}
	run, fail := s.harnessRun(ctx, req, harnessParams{Session: p.Session, Run: p.Run}, false)
	if fail != nil {
		return *fail
	}
	w.Run = run.ID
	// AWSWrite bounds itself by HarnessAWSWriteTimeout, or the reset's.
	res, err := s.harness.d.Manager.AWSWrite(ctx, w)
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	return Response{ID: req.ID, Result: harnessAWSWriteReply{Type: "harness_aws_write", AWSWriteResult: res}}
}

// harnessAWSWriteReply is the harness_aws_write result: the type beside the
// result's fields.
type harnessAWSWriteReply struct {
	Type string `json:"type"`
	harness.AWSWriteResult
}
