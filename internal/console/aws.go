package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
)

// AWS tab (P10 A7): the console reads a run's emulated S3 and DynamoDB and
// the AWS call log. Every read goes to serve as the operator, and serve reads
// the emulator through the edge's read-only relay. This file registers no
// write route: operator writes (POST /api/aws/write) register their own
// route and fill the page's #aws-write area.

// AWS API paths.
const (
	AWSPath      = "/api/aws"
	AWSCallsPath = "/api/aws/calls"
)

// Call log read bounds, as serve's harness.aws_calls.
const (
	DefaultAWSCallsLimit = 50
	MaxAWSCallsLimit     = 200
)

// awsSortOps are the sort-key conditions harness.aws takes.
var awsSortOps = []string{"=", "<", "<=", ">", ">=", "begins_with", "between"}

var awsServiceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func init() {
	apiRoutes[AWSPath] = apiRoute{http.MethodGet, (*Server).handleAWS}
	apiRoutes[AWSCallsPath] = apiRoute{http.MethodGet, (*Server).handleAWSCalls}
	staticFiles["/aws.js"] = [2]string{"web/aws.js", "text/javascript; charset=utf-8"}
	staticFiles["/aws.css"] = [2]string{"web/aws.css", "text/css; charset=utf-8"}
}

// awsRunParams reads run and service, checks the run is the one shown, and
// returns serve's params; false once it has answered.
func (s *Server) awsRunParams(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	q := r.URL.Query()
	params := map[string]any{}
	if svc := strings.TrimSpace(q.Get("service")); svc != "" {
		if !awsServiceRe.MatchString(svc) {
			badParams(w, "service must be a service name")
			return nil, false
		}
		params["service"] = svc
	}
	if !s.sameRun(w, q.Get("run")) {
		return nil, false
	}
	params["run"] = s.currentRun()
	return params, true
}

// handleAWS answers GET /api/aws?run&service&op&…: one harness.aws read.
// S3: bucket, key, prefix, delimiter, token, version, offset, limit.
// DynamoDB: table, index, limit, start, forward, pk_name and pk_value, sk_op
// and sk_value (twice for between), and key (ddb_get's key as a JSON
// object). Serve checks the values; the console checks shape only.
func (s *Server) handleAWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	op := q.Get("op")
	if !slices.Contains(harness.AWSReadOps, op) {
		badParams(w, "op must be one of "+strings.Join(harness.AWSReadOps, ", "))
		return
	}
	params := map[string]any{"op": op}
	for _, k := range []string{"bucket", "prefix", "token", "version", "table", "index", "start"} {
		if v := q.Get(k); v != "" {
			params[k] = v
		}
	}
	if q.Has("delimiter") {
		params["delimiter"] = q.Get("delimiter")
	}
	for _, k := range []string{"offset", "limit"} {
		v := q.Get(k)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || (k == "limit" && n == 0) {
			badParams(w, k+" must be a number (limit above 0)")
			return
		}
		params[k] = n
	}
	if v := q.Get("forward"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badParams(w, "forward is true or false")
			return
		}
		params["forward"] = b
	}
	if !awsKeyParams(w, q.Get("key"), op, params) || !awsQueryParams(w, q, params) {
		return
	}
	base, ok := s.awsRunParams(w, r)
	if !ok {
		return
	}
	for k, v := range base {
		params[k] = v
	}
	s.relay(w, r, "harness.aws", params)
}

// awsKeyParams sets key: an S3 object key for the s3 ops, and ddb_get's
// item key, which arrives as a JSON object.
func awsKeyParams(w http.ResponseWriter, key, op string, params map[string]any) bool {
	if key == "" {
		return true
	}
	if op != harness.AWSOpDDBGet {
		params["key"] = key
		return true
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(key)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || len(obj) == 0 || dec.More() {
		badParams(w, "key must be the item's key attributes as a JSON object")
		return false
	}
	params["key"] = obj
	return true
}

// awsQueryParams sets ddb_query's pk and sk from pk_name, pk_value, sk_op
// and sk_value.
func awsQueryParams(w http.ResponseWriter, q map[string][]string, params map[string]any) bool {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if name := strings.TrimSpace(get("pk_name")); name != "" {
		params["pk"] = map[string]any{"name": name, "value": get("pk_value")}
	}
	op := get("sk_op")
	vals := q["sk_value"]
	if op == "" {
		if len(vals) > 0 {
			badParams(w, "sk_value needs sk_op")
			return false
		}
		return true
	}
	want := 1
	if op == "between" {
		want = 2
	}
	if !slices.Contains(awsSortOps, op) || len(vals) != want {
		badParams(w, "sk_op is one of "+strings.Join(awsSortOps, " ")+" with one sk_value (two for between)")
		return false
	}
	values := make([]any, len(vals))
	for i, v := range vals {
		values[i] = v
	}
	params["sk"] = map[string]any{"op": op, "values": values}
	return true
}

// handleAWSCalls relays harness.aws_calls for the shown run: the AWS calls
// with an ID above after.
func (s *Server) handleAWSCalls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := map[string]any{"limit": DefaultAWSCallsLimit}
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badParams(w, "after must be a call id (0 or more)")
			return
		}
		params["after_id"] = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			badParams(w, "limit must be a positive number")
			return
		}
		params["limit"] = min(n, MaxAWSCallsLimit)
	}
	if !s.sameRun(w, q.Get("run")) {
		return
	}
	params["run"] = s.currentRun()
	s.relay(w, r, "harness.aws_calls", params)
}
