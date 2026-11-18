package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

// KindAWS marks the read relay in front of an AWS emulator. It rebuilds
// every request from the parts it checked (FilterAWSRequest), admits only
// the S3 and DynamoDB reads listed below, and signs the rebuilt request with
// the emulator's test key, so the published port can read the app's data
// and never change it.
const KindAWS = "aws"

// AWS relay bounds.
const (
	// AWSMaxRequestBytes caps a DynamoDB request body.
	AWSMaxRequestBytes = 64 << 10
	// AWSMaxResponseBytes caps every answer but an S3 object body; a larger
	// one is refused whole.
	AWSMaxResponseBytes = 32 << 20
	// DefaultAWSDownloadMax caps one S3 object body (harness.aws.download_max).
	DefaultAWSDownloadMax = 64 << 20
	awsMaxHeaderBytes     = 16 << 10
	awsIdle               = 60 * time.Second
	awsCallTimeout        = 60 * time.Second
)

// awsRelayAccessKey and awsRelaySecret are the emulator's public test key,
// the only credentials a run has (harness.AWSAccessKeyID).
const (
	awsRelayAccessKey = "test"
	awsRelaySecret    = "test" //nolint:gosec // G101: the emulator's fixed public test key
)

var awsRegionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)

// S3ReadQuery are the only query keys an S3 read may carry.
var S3ReadQuery = []string{
	"list-type", "prefix", "delimiter", "continuation-token", "start-after", "max-keys", "encoding-type",
	"fetch-owner", "versions", "key-marker", "version-id-marker", "versionId", "location", "versioning",
	"notification", "cors", "lifecycle", "tagging", "policy", "encryption", "object-lock", "uploads",
	"partNumber", "attributes",
}

// S3ReadHeaders are the only request headers an S3 read keeps.
var S3ReadHeaders = []string{"Range", "If-Match", "If-None-Match", "X-Amz-Object-Attributes", "X-Amz-Max-Parts"}

// DynamoDBReadTargets are the only X-Amz-Target values the relay passes.
// ExecuteStatement and BatchExecuteStatement are left out: PartiQL writes.
var DynamoDBReadTargets = []string{
	"DynamoDB_20120810.ListTables",
	"DynamoDB_20120810.DescribeTable",
	"DynamoDB_20120810.DescribeTimeToLive",
	"DynamoDB_20120810.DescribeContinuousBackups",
	"DynamoDB_20120810.ListTagsOfResource",
	"DynamoDB_20120810.GetItem",
	"DynamoDB_20120810.BatchGetItem",
	"DynamoDB_20120810.TransactGetItems",
	"DynamoDB_20120810.Query",
	"DynamoDB_20120810.Scan",
	"DynamoDBStreams_20120810.ListStreams",
	"DynamoDBStreams_20120810.DescribeStream",
	"DynamoDBStreams_20120810.GetShardIterator",
	"DynamoDBStreams_20120810.GetRecords",
}

// awsOverrideHeaders would let a front end treat a request as another
// method; the relay refuses a request that carries one rather than drop it
// silently.
var awsOverrideHeaders = []string{"X-Http-Method-Override", "X-Http-Method", "X-Method-Override"}

// s3BucketRe is S3's bucket naming rule; it keeps "/_ministack" and every
// other admin path out.
var s3BucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// AWSRefusal is a request the read relay does not admit.
type AWSRefusal struct{ Reason string }

func (e *AWSRefusal) Error() string {
	return "aiman edge: the AWS read relay refused this request: " + e.Reason
}

func refuse(format string, args ...any) error {
	return &AWSRefusal{Reason: fmt.Sprintf(format, args...)}
}

// AWSRead is a request the relay admits, rebuilt from the parts it checked:
// nothing else of the client's request reaches the emulator.
type AWSRead struct {
	// Service is the signing name: s3 or dynamodb (Streams included).
	Service string
	Method  string
	// Path is the decoded path; the signer encodes it.
	Path  string
	Query url.Values
	// Header holds only the headers the relay keeps.
	Header http.Header
	// Body is the re-encoded JSON of a DynamoDB call; empty for S3.
	Body []byte
	// Object is an S3 GetObject or HeadObject, whose body streams up to the
	// download cap rather than the answer cap.
	Object bool
}

// FilterAWSRequest checks one request (its body already read, at most
// AWSMaxRequestBytes) and returns it rebuilt, or an *AWSRefusal.
func FilterAWSRequest(req *http.Request, body []byte) (*AWSRead, error) {
	for _, h := range awsOverrideHeaders {
		if _, ok := req.Header[h]; ok {
			return nil, refuse("method override header %s", h)
		}
	}
	if req.Header.Get("Upgrade") != "" || headerHasToken(req.Header, "Connection", "upgrade") {
		return nil, refuse("protocol upgrades are not relayed")
	}
	if strings.HasPrefix(req.URL.Path, "/_") {
		return nil, refuse("emulator admin paths are not relayed")
	}
	if req.URL.Query().Has("X-Amz-Signature") || req.URL.Query().Has("X-Amz-Credential") || req.URL.Query().Has("X-Amz-Algorithm") {
		return nil, refuse("presigned requests are not relayed; the relay signs every request itself")
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		return filterS3(req, body)
	case http.MethodPost:
		return filterDynamoDB(req, body)
	}
	return nil, refuse("method %s; the relay reads only (S3 GET and HEAD, DynamoDB POST of a read)", req.Method)
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func filterS3(req *http.Request, body []byte) (*AWSRead, error) {
	if len(body) > 0 || req.ContentLength > 0 || len(req.TransferEncoding) > 0 {
		return nil, refuse("an S3 read carries no body")
	}
	read := &AWSRead{Service: "s3", Method: req.Method, Query: url.Values{}, Header: http.Header{}}
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return nil, refuse("query does not parse")
	}
	for k, v := range q {
		if !slices.Contains(S3ReadQuery, k) {
			return nil, refuse("query key %q", k)
		}
		if len(v) != 1 {
			return nil, refuse("query key %q given %d times", k, len(v))
		}
		read.Query[k] = v
	}
	for _, h := range S3ReadHeaders {
		vals := req.Header.Values(h)
		if len(vals) > 1 {
			return nil, refuse("header %s given %d times", h, len(vals))
		}
		if len(vals) == 1 {
			read.Header.Set(h, vals[0])
		}
	}
	path := req.URL.Path
	if path == "" || path == "/" {
		read.Path = "/"
		return read, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, refuse("path must start with /")
	}
	bucket, key, hasKey := strings.Cut(path[1:], "/")
	if !s3BucketRe.MatchString(bucket) {
		return nil, refuse("%q is not a bucket name", bucket)
	}
	if !hasKey || key == "" {
		read.Path = "/" + bucket
		return read, nil
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return nil, refuse("object keys with . or .. segments are not relayed")
		}
	}
	if strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, refuse("object keys with control characters are not relayed")
	}
	read.Path = "/" + bucket + "/" + key
	read.Object = true
	for k := range read.Query {
		if k != "versionId" && k != "partNumber" {
			read.Object = false
		}
	}
	return read, nil
}

func filterDynamoDB(req *http.Request, body []byte) (*AWSRead, error) {
	if req.URL.Path != "/" {
		return nil, refuse("a DynamoDB read posts to /")
	}
	if req.URL.RawQuery != "" {
		return nil, refuse("a DynamoDB read has no query string")
	}
	targets := req.Header.Values("X-Amz-Target")
	if len(targets) != 1 {
		return nil, refuse("a POST needs exactly one X-Amz-Target, not %d", len(targets))
	}
	target := targets[0]
	if !slices.Contains(DynamoDBReadTargets, target) {
		return nil, refuse("X-Amz-Target %q is not a read the relay admits", target)
	}
	if enc := req.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, refuse("Content-Encoding %q", enc)
	}
	if len(body) > AWSMaxRequestBytes {
		return nil, refuse("body over %d bytes", AWSMaxRequestBytes)
	}
	v, err := DecodeStrictJSON(body)
	if err != nil {
		return nil, refuse("body: %v", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, refuse("body must be a JSON object")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, refuse("body: %v", err)
	}
	h := http.Header{}
	h.Set("X-Amz-Target", target)
	h.Set("Content-Type", "application/x-amz-json-1.0")
	return &AWSRead{Service: "dynamodb", Method: http.MethodPost, Path: "/", Query: url.Values{}, Header: h, Body: out}, nil
}

// maxJSONDepth bounds nesting in a DynamoDB body.
const maxJSONDepth = 64

// DecodeStrictJSON decodes one JSON value, refusing a duplicate object key
// at any depth, nesting past 64 levels and trailing data. Numbers stay
// json.Number, so re-encoding keeps them as sent.
func DecodeStrictJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

func strictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("nested too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		m := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, _ := kt.(string)
			if _, dup := m[k]; dup {
				return nil, fmt.Errorf("duplicate key %q", k)
			}
			v, err := strictValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return m, nil
	case '[':
		a := []any{}
		for dec.More() {
			v, err := strictValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return a, nil
	}
	return nil, fmt.Errorf("unexpected %v", d)
}

// Request builds the signed request to the emulator at target for region.
func (a *AWSRead) Request(ctx context.Context, target, region string, now time.Time) (*http.Request, error) {
	u := &url.URL{Scheme: "http", Host: target, Path: a.Path, RawQuery: a.Query.Encode()}
	var body io.Reader = http.NoBody
	if len(a.Body) > 0 {
		body = bytes.NewReader(a.Body)
	}
	req, err := http.NewRequestWithContext(ctx, a.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	// NewRequest parsed the URL again; put back the decoded path so the
	// signer encodes it the one way it signs.
	req.URL.Path, req.URL.RawPath = a.Path, ""
	for k, v := range a.Header {
		req.Header[k] = append([]string(nil), v...)
	}
	s := awssig.Signer{
		Credentials: awssig.Credentials{AccessKeyID: awsRelayAccessKey, SecretAccessKey: awsRelaySecret},
		Region:      region, Service: a.Service, ContentSHA256: a.Service == "s3",
	}
	hash := awssig.EmptyPayload
	if len(a.Body) > 0 {
		hash = awssig.PayloadHash(a.Body)
	}
	if err := s.Sign(req, hash, now); err != nil {
		return nil, err
	}
	return req, nil
}

// awsHopHeaders are answer headers the relay does not pass on.
var awsHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Connection", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Content-Length", "Set-Cookie",
}

// handleAWS serves HTTP/1.1 on one client connection, answering each
// request from the emulator when FilterAWSRequest admits it.
func (r *relay) handleAWS(client net.Conn) {
	cr := &capReader{r: client}
	br := bufio.NewReader(cr)
	upstream := &http.Client{
		Timeout: awsCallTimeout,
		Transport: &http.Transport{
			Proxy:              nil,
			DisableKeepAlives:  true,
			DisableCompression: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: r.dialTimeout}
				return d.DialContext(ctx, network, r.spec.Target)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer upstream.CloseIdleConnections()
	for {
		cr.n = awsMaxHeaderBytes + AWSMaxRequestBytes + 1
		_ = client.SetReadDeadline(time.Now().Add(awsIdle))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		resp := r.awsAnswer(req, upstream)
		_ = req.Body.Close()
		resp.Request = req
		resp.Close = resp.Close || req.Close
		_ = client.SetWriteDeadline(time.Now().Add(awsCallTimeout))
		err = resp.Write(client)
		_ = resp.Body.Close()
		if err != nil || resp.Close {
			return
		}
	}
}

// awsAnswer filters one request and forwards it, or answers the refusal.
func (r *relay) awsAnswer(req *http.Request, upstream *http.Client) *http.Response {
	body, err := io.ReadAll(io.LimitReader(req.Body, AWSMaxRequestBytes+1))
	if err != nil {
		return awsText(http.StatusBadRequest, "aiman edge: read request: "+err.Error(), true)
	}
	if len(body) > AWSMaxRequestBytes {
		r.refused.Add(1)
		return awsText(http.StatusRequestEntityTooLarge, "aiman edge: request body over "+strconv.Itoa(AWSMaxRequestBytes)+" bytes", true)
	}
	read, err := FilterAWSRequest(req, body)
	if err != nil {
		r.refused.Add(1)
		return awsText(http.StatusForbidden, err.Error(), false)
	}
	ctx, cancel := context.WithTimeout(r.ctx, awsCallTimeout)
	up, err := read.Request(ctx, r.spec.Target, r.spec.Region, time.Now())
	if err != nil {
		cancel()
		return awsText(http.StatusBadGateway, "aiman edge: "+err.Error(), true)
	}
	resp, err := upstream.Do(up)
	if err != nil {
		cancel()
		r.failed.Add(1)
		r.setErr(err)
		return awsText(http.StatusBadGateway, "aiman edge: the AWS emulator did not answer", true)
	}
	header := resp.Header.Clone()
	for _, h := range awsHopHeaders {
		header.Del(h)
	}
	out := &http.Response{StatusCode: resp.StatusCode, ProtoMajor: 1, ProtoMinor: 1, Header: header}
	if req.Method == http.MethodHead {
		_ = resp.Body.Close()
		cancel()
		out.ContentLength = resp.ContentLength
		out.Body = http.NoBody
		return out
	}
	if read.Object && resp.StatusCode/100 == 2 {
		limit := r.spec.DownloadMax
		if limit <= 0 {
			limit = DefaultAWSDownloadMax
		}
		if resp.ContentLength > limit {
			_ = resp.Body.Close()
			cancel()
			return awsText(http.StatusBadGateway, fmt.Sprintf("aiman edge: the object is %d bytes, over the download cap of %d (harness.aws.download_max)", resp.ContentLength, limit), true)
		}
		// The body streams; an answer with no length is cut at the cap and
		// the connection closed, so a client sees a short read.
		out.ContentLength = resp.ContentLength
		out.Body = &cancelBody{ReadCloser: struct {
			io.Reader
			io.Closer
		}{io.LimitReader(resp.Body, limit), resp.Body}, cancel: cancel}
		out.Close = resp.ContentLength < 0
		return out
	}
	defer cancel()
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, AWSMaxResponseBytes+1))
	if err != nil {
		return awsText(http.StatusBadGateway, "aiman edge: read the emulator's answer: "+err.Error(), true)
	}
	if len(data) > AWSMaxResponseBytes {
		return awsText(http.StatusBadGateway, "aiman edge: the emulator's answer is over "+strconv.Itoa(AWSMaxResponseBytes)+" bytes", true)
	}
	out.ContentLength = int64(len(data))
	out.Body = io.NopCloser(bytes.NewReader(data))
	return out
}

// cancelBody ends the upstream call's context when the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func awsText(status int, msg string, closeConn bool) *http.Response {
	body := []byte(msg + "\n")
	return &http.Response{
		StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Close: closeConn,
	}
}
