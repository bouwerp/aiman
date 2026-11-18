package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
)

// HarnessAWSTimeout bounds one harness.aws read through the edge relay.
const HarnessAWSTimeout = harness.AWSTimeout

// harnessAWSParams are harness.aws's parameters. Key is an S3 object key
// (a string) for the s3 ops and the item's key attributes (an object) for
// ddb_get.
type harnessAWSParams struct {
	Session   string               `json:"session"`
	Run       string               `json:"run"`
	Service   string               `json:"service"`
	Op        string               `json:"op"`
	Bucket    string               `json:"bucket"`
	Key       json.RawMessage      `json:"key"`
	Prefix    string               `json:"prefix"`
	Delimiter *string              `json:"delimiter"`
	Token     string               `json:"token"`
	Version   string               `json:"version"`
	Offset    int64                `json:"offset"`
	Limit     int                  `json:"limit"`
	Table     string               `json:"table"`
	Index     string               `json:"index"`
	PK        *harness.AWSKeyValue `json:"pk"`
	SK        *harness.AWSSortKey  `json:"sk"`
	Forward   *bool                `json:"forward"`
	Start     string               `json:"start"`
}

// handleHarnessAWS answers harness.aws {run|session, service?, op, …}: the
// S3 and DynamoDB reads of a ready run's aws service, through the edge's
// read-only relay. Any in-scope caller may read; nothing is recorded.
func (s *Server) handleHarnessAWS(ctx context.Context, req Request) Response {
	var p harnessAWSParams
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
	if strings.TrimSpace(p.Op) == "" {
		return errResp(req.ID, CodeInvalidParams, "op is required: one of "+strings.Join(harness.AWSReadOps, ", "))
	}
	r := harness.AWSReadRequest{
		Service: strings.TrimSpace(p.Service), Op: strings.TrimSpace(p.Op),
		Bucket: p.Bucket, Prefix: p.Prefix, Delimiter: p.Delimiter, Token: p.Token, Version: p.Version,
		Offset: p.Offset, Limit: p.Limit, Table: p.Table, Index: p.Index, PK: p.PK, SK: p.SK,
		Forward: p.Forward, Start: p.Start,
	}
	if k := bytes.TrimSpace(p.Key); len(k) > 0 && !bytes.Equal(k, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(k))
		dec.UseNumber()
		var err error
		if k[0] == '{' {
			err = dec.Decode(&r.ItemKey)
		} else {
			err = dec.Decode(&r.Key)
		}
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, "key must be an S3 key string or a DynamoDB key object: "+err.Error())
		}
	}
	run, fail := s.harnessRun(ctx, req, harnessParams{Session: p.Session, Run: p.Run}, false)
	if fail != nil {
		return *fail
	}
	m := s.harness.d.Manager
	m.Touch(run.ID)
	r.Run = run.ID
	// m.AWS bounds itself by HarnessAWSTimeout.
	res, err := m.AWS(ctx, r)
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	return Response{ID: req.ID, Result: harnessAWSReply{Type: "harness_aws", AWSResult: res}}
}

// harnessAWSReply is the harness_aws result: the type beside the result's
// fields.
type harnessAWSReply struct {
	Type string `json:"type"`
	harness.AWSResult
}
