package console

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

// AWSWritePath takes the operator's writes to a run's aws service and its
// reset (harness.aws_write). The console calls serve as the operator; the
// page asks the tester to confirm first.
const AWSWritePath = "/api/aws/write"

// ConsoleAWSPutMax caps an object uploaded through the console: the call
// travels to serve as one line of the laptop's RPC stream, which holds 8
// MiB, so a larger object is written with `aiman harness aws write` on the
// host.
const ConsoleAWSPutMax = 5 << 20

func init() {
	apiRoutes[AWSWritePath] = apiRoute{http.MethodPost, (*Server).handleAWSWrite}
	// base64 of ConsoleAWSPutMax plus the other fields.
	routeMaxBody[AWSWritePath] = 8 << 20
}

// handleAWSWrite answers POST /api/aws/write {run, service?, op, …}: the
// fields of harness.aws_write, passed on for the shown run. Item and key
// pass as written, so numbers keep their digits.
func (s *Server) handleAWSWrite(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run         string            `json:"run"`
		Service     string            `json:"service"`
		Op          string            `json:"op"`
		Bucket      string            `json:"bucket"`
		Key         json.RawMessage   `json:"key"`
		Version     string            `json:"version"`
		ContentType string            `json:"content_type"`
		Data        string            `json:"data"`
		Table       string            `json:"table"`
		Item        json.RawMessage   `json:"item"`
		Queue       string            `json:"queue"`
		Body        string            `json:"body"`
		Attributes  map[string]string `json:"attributes"`
		GroupID     string            `json:"group_id"`
		DedupID     string            `json:"dedup_id"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	op := strings.TrimSpace(b.Op)
	if !slices.Contains(awsiac.WriteOps, op) {
		badParams(w, "op is one of "+strings.Join(awsiac.WriteOps, ", "))
		return
	}
	if b.Data != "" {
		n, err := base64.StdEncoding.DecodeString(b.Data)
		switch {
		case err != nil:
			badParams(w, "data must be base64")
			return
		case len(n) > ConsoleAWSPutMax:
			badParams(w, fmt.Sprintf("the console uploads at most %d bytes; write a larger object with aiman harness aws write s3-put on the host", ConsoleAWSPutMax))
			return
		}
	}
	if !s.sameRun(w, b.Run) {
		return
	}
	params := map[string]any{"run": b.Run, "op": op}
	for k, v := range map[string]string{
		"service": strings.TrimSpace(b.Service), "bucket": b.Bucket, "version": b.Version, "content_type": b.ContentType,
		"data": b.Data, "table": b.Table, "queue": b.Queue, "body": b.Body, "group_id": b.GroupID, "dedup_id": b.DedupID,
	} {
		if v != "" {
			params[k] = v
		}
	}
	if len(b.Key) > 0 {
		params["key"] = b.Key
	}
	if len(b.Item) > 0 {
		params["item"] = b.Item
	}
	if len(b.Attributes) > 0 {
		params["attributes"] = b.Attributes
	}
	s.relay(w, r, "harness.aws_write", params)
}
