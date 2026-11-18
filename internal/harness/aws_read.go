package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/harness/awssig"
	"github.com/bouwerp/aiman/internal/harness/edge"
	"github.com/bouwerp/aiman/internal/redact"
)

// harness.aws read operations. Every one reads through the aws service's
// edge read relay, which admits only reads and signs them itself.
const (
	AWSOpResources   = "resources"
	AWSOpS3Buckets   = "s3_buckets"
	AWSOpS3Objects   = "s3_objects"
	AWSOpS3Versions  = "s3_versions"
	AWSOpS3Object    = "s3_object"
	AWSOpS3Download  = "s3_download"
	AWSOpDDBTables   = "ddb_tables"
	AWSOpDDBDescribe = "ddb_describe"
	AWSOpDDBScan     = "ddb_scan"
	AWSOpDDBQuery    = "ddb_query"
	AWSOpDDBGet      = "ddb_get"
	AWSOpDDBStream   = "ddb_stream"
)

// AWSReadOps lists the harness.aws operations.
var AWSReadOps = []string{
	AWSOpResources, AWSOpS3Buckets, AWSOpS3Objects, AWSOpS3Versions, AWSOpS3Object, AWSOpS3Download,
	AWSOpDDBTables, AWSOpDDBDescribe, AWSOpDDBScan, AWSOpDDBQuery, AWSOpDDBGet, AWSOpDDBStream,
}

// AWS read bounds.
const (
	// AWSTimeout bounds one harness.aws call.
	AWSTimeout = 20 * time.Second
	// MaxS3Objects caps an s3_objects page; DefaultS3Objects is its default.
	MaxS3Objects     = 1000
	DefaultS3Objects = 1000
	// MaxS3ObjectPage caps an s3_object text page; MaxS3DownloadPage an
	// s3_download page.
	MaxS3ObjectPage   = 256 << 10
	MaxS3DownloadPage = 1 << 20
	// MaxDDBItems caps the items of a scan, query or stream read; it is
	// also the default.
	MaxDDBItems = 200
	// awsMaxBuckets caps the buckets s3_buckets describes.
	awsMaxBuckets = 100
	// awsMaxTables caps a table listing.
	awsMaxTables = 1000
	// awsMaxCellBytes cuts each string; awsMaxReplyBytes stops adding
	// items or records.
	awsMaxCellBytes  = 4 << 10
	awsMaxReplyBytes = 1 << 20
	// awsBinarySniff is how much of an object decides whether it is text.
	awsBinarySniff = 8 << 10
	// awsStreamPages bounds the GetRecords calls per shard.
	awsStreamPages = 20
	// awsMaxShards bounds the shards one ddb_stream read walks.
	awsMaxShards = 16
)

// awsSecretAttr names an attribute whose every value is redacted.
var awsSecretAttr = regexp.MustCompile(`(?i)password|passwd|secret|token|api_key`)

var s3BucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// AWSReadRequest is one harness.aws call against a ready run.
type AWSReadRequest struct {
	Run     string
	Service string
	Op      string
	// S3.
	Bucket    string
	Key       string
	Prefix    string
	Delimiter *string
	Token     string
	Version   string
	Offset    int64
	Limit     int
	// DynamoDB.
	Table   string
	Index   string
	PK      *AWSKeyValue
	SK      *AWSSortKey
	Forward *bool
	Start   string
	// ItemKey is ddb_get's key as plain JSON.
	ItemKey map[string]any
}

// AWSKeyValue is a query's partition key: the attribute and its plain JSON
// value.
type AWSKeyValue struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// AWSSortKey is a query's sort-key condition: op is =, <, <=, >, >=,
// begins_with (one value) or between (two).
type AWSSortKey struct {
	Op     string `json:"op"`
	Values []any  `json:"values"`
}

// AWSResult is the harness_aws reply; only the fields of its op are set.
// Every name, string and text page has passed redaction.
type AWSResult struct {
	Run     string `json:"run"`
	Service string `json:"service"`
	Op      string `json:"op"`

	Resources json.RawMessage `json:"resources,omitempty"`
	Note      string          `json:"note,omitempty"`

	Buckets   []S3Bucket       `json:"buckets,omitempty"`
	Objects   []S3ObjectEntry  `json:"objects,omitempty"`
	Prefixes  []string         `json:"prefixes,omitempty"`
	NextToken string           `json:"next_token,omitempty"`
	Versions  []S3VersionEntry `json:"versions,omitempty"`
	Object    *S3ObjectPage    `json:"object,omitempty"`
	Download  *S3DownloadPage  `json:"download,omitempty"`

	Tables  []string          `json:"tables,omitempty"`
	Table   *DDBTableInfo     `json:"table,omitempty"`
	Items   []map[string]any  `json:"items,omitempty"`
	Item    map[string]any    `json:"item,omitempty"`
	Found   *bool             `json:"found,omitempty"`
	LastKey string            `json:"last_key,omitempty"`
	Records []DDBStreamRecord `json:"records,omitempty"`

	Truncated          bool     `json:"truncated"`
	Redacted           int      `json:"redacted"`
	RedactedAttributes []string `json:"redacted_attributes,omitempty"`
}

// S3Bucket is one bucket of s3_buckets.
type S3Bucket struct {
	Name          string   `json:"name"`
	Created       string   `json:"created,omitempty"`
	Versioning    string   `json:"versioning,omitempty"`
	Notifications []string `json:"notifications,omitempty"`
}

// S3ObjectEntry is one key of s3_objects.
type S3ObjectEntry struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	ETag     string `json:"etag,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// S3VersionEntry is a version or delete marker of one key.
type S3VersionEntry struct {
	Version      string `json:"version"`
	Latest       bool   `json:"latest"`
	DeleteMarker bool   `json:"delete_marker,omitempty"`
	Size         int64  `json:"size,omitempty"`
	ETag         string `json:"etag,omitempty"`
	Modified     string `json:"modified,omitempty"`
}

// S3ObjectPage is s3_object: the head and a text page starting at Offset.
// Next is the offset after the bytes Data came from; Binary is set, with no
// data, when the first 8 KiB holds a NUL.
type S3ObjectPage struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Version     string            `json:"version,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Size        int64             `json:"size"`
	ETag        string            `json:"etag,omitempty"`
	Modified    string            `json:"modified,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Lock        *S3Lock           `json:"lock,omitempty"`
	Offset      int64             `json:"offset"`
	Next        int64             `json:"next"`
	EOF         bool              `json:"eof"`
	Binary      bool              `json:"binary"`
	Data        string            `json:"data"`
}

// S3Lock is an object's Object Lock state.
type S3Lock struct {
	Mode       string `json:"mode,omitempty"`
	RetainUtil string `json:"retain_until,omitempty"`
	LegalHold  string `json:"legal_hold,omitempty"`
}

// S3DownloadPage is s3_download: Data is base64 of the bytes from Offset.
// Capped is set when the object is larger than the download cap, which
// bounds every page.
type S3DownloadPage struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
	Next   int64  `json:"next"`
	EOF    bool   `json:"eof"`
	Capped bool   `json:"capped,omitempty"`
	Max    int64  `json:"max"`
	Data   string `json:"data"`
}

// DDBTableInfo is ddb_describe.
type DDBTableInfo struct {
	Name       string            `json:"name"`
	Status     string            `json:"status,omitempty"`
	KeySchema  []DDBKey          `json:"key_schema"`
	Attributes map[string]string `json:"attributes"`
	GSIs       []DDBIndex        `json:"gsis,omitempty"`
	LSIs       []DDBIndex        `json:"lsis,omitempty"`
	StreamARN  string            `json:"stream_arn,omitempty"`
	StreamView string            `json:"stream_view,omitempty"`
	TTL        string            `json:"ttl_attribute,omitempty"`
	TTLStatus  string            `json:"ttl_status,omitempty"`
	ItemCount  int64             `json:"item_count"`
	SizeBytes  int64             `json:"size_bytes"`
}

// DDBKey is one key schema element: HASH or RANGE.
type DDBKey struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// DDBIndex is a global or local secondary index.
type DDBIndex struct {
	Name       string   `json:"name"`
	KeySchema  []DDBKey `json:"key_schema"`
	Projection string   `json:"projection,omitempty"`
}

// DDBStreamRecord is one stream record of ddb_stream.
type DDBStreamRecord struct {
	Event    string         `json:"event"`
	Sequence string         `json:"sequence"`
	Time     string         `json:"time,omitempty"`
	Keys     map[string]any `json:"keys,omitempty"`
	Old      map[string]any `json:"old,omitempty"`
	New      map[string]any `json:"new,omitempty"`
}

// awsClient reads one aws service through its edge relay.
type awsClient struct {
	base string
	http *http.Client
}

// awsTarget finds a ready run's aws service and the client for its relay.
// An empty service names the run's only aws service.
func (m *Manager) awsTarget(id, service string) (Run, string, awsClient, error) {
	lr, err := m.readyRunOf(id)
	if err != nil {
		return Run{}, "", awsClient{}, err
	}
	run := lr.snapshot()
	if len(run.AWSServices) == 0 {
		return Run{}, "", awsClient{}, paramsError(fmt.Sprintf("run %s starts no aws service", id))
	}
	if service == "" {
		service = run.AWSServices[0]
	}
	if !slices.Contains(run.AWSServices, service) {
		return Run{}, "", awsClient{}, paramsError(fmt.Sprintf("%q is not an aws service of run %s; it has %v", service, id, run.AWSServices))
	}
	port := 0
	for _, p := range run.Ports {
		if p.Name == service {
			port = p.HostPort
		}
	}
	if port == 0 {
		return Run{}, "", awsClient{}, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("run %s publishes no read relay for %s", id, service)}
	}
	hc := m.opts.AWSHTTP
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true}}
	}
	return run, service, awsClient{base: "http://127.0.0.1:" + strconv.Itoa(port), http: hc}, nil
}

// awsHTTPError is the emulator's (or the relay's) refusal: a 4xx is the
// caller's (invalid_params), anything else the service's.
type awsHTTPError struct {
	Status  int
	Code    string
	Message string
}

func (e *awsHTTPError) Error() string {
	msg := e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	line, _ := redact.Line(oneLine(msg, 300), nil)
	return fmt.Sprintf("aws: HTTP %d %s", e.Status, line)
}

func awsFail(err error) error {
	var he *awsHTTPError
	if errors.As(err, &he) && he.Status/100 == 4 {
		return paramsError(he.Error())
	}
	return err
}

// s3 sends a GET or HEAD for path (decoded) and query through the relay
// and returns the answer; a non-2xx answer is an *awsHTTPError. hdr adds
// request headers.
func (c awsClient) s3(ctx context.Context, method, path string, q url.Values, hdr map[string]string) (*http.Response, error) {
	u := c.base + awssig.EscapePath(path)
	if len(q) > 0 {
		u += "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aws relay: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		if xml.Unmarshal(body, &e) != nil {
			e.Message = strings.TrimSpace(string(body))
		}
		return nil, &awsHTTPError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return resp, nil
}

// s3XML GETs path and query and decodes the XML answer into v.
func (c awsClient) s3XML(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.s3(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, edge.AWSMaxResponseBytes+1))
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(body, v); err != nil {
		return fmt.Errorf("aws: S3 answer: %w", err)
	}
	return nil
}

// ddb posts one DynamoDB (or Streams) read and decodes the answer into v.
func (c awsClient) ddb(ctx context.Context, target string, in any, v any) error {
	if !slices.Contains(edge.DynamoDBReadTargets, target) {
		return fmt.Errorf("aws: %s is not a read", target)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Target", target)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("aws relay: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, edge.AWSMaxResponseBytes+1))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
			Msg     string `json:"Message"`
		}
		if json.Unmarshal(out, &e) != nil {
			e.Message = strings.TrimSpace(string(out))
		}
		if e.Message == "" {
			e.Message = e.Msg
		}
		code := e.Type
		if i := strings.LastIndex(code, "#"); i >= 0 {
			code = code[i+1:]
		}
		return &awsHTTPError{Status: resp.StatusCode, Code: code, Message: e.Message}
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("aws: DynamoDB answer: %w", err)
	}
	return nil
}

// AWS answers one harness.aws read.
func (m *Manager) AWS(ctx context.Context, req AWSReadRequest) (AWSResult, error) {
	if !slices.Contains(AWSReadOps, req.Op) {
		return AWSResult{}, paramsError(fmt.Sprintf("op must be one of %s", strings.Join(AWSReadOps, ", ")))
	}
	run, service, c, err := m.awsTarget(req.Run, req.Service)
	if err != nil {
		return AWSResult{}, err
	}
	res := AWSResult{Run: run.ID, Service: service, Op: req.Op}
	ctx, cancel := context.WithTimeout(ctx, AWSTimeout)
	defer cancel()
	switch req.Op {
	case AWSOpResources:
		err = m.awsResources(run, &res)
	case AWSOpS3Buckets:
		err = c.s3Buckets(ctx, &res)
	case AWSOpS3Objects:
		err = c.s3Objects(ctx, req, &res)
	case AWSOpS3Versions:
		err = c.s3Versions(ctx, req, &res)
	case AWSOpS3Object:
		err = c.s3Object(ctx, req, &res)
	case AWSOpS3Download:
		err = c.s3Download(ctx, req, m.awsDownloadMax(), &res)
	case AWSOpDDBTables:
		err = c.ddbTables(ctx, &res)
	case AWSOpDDBDescribe:
		var t *DDBTableInfo
		t, err = c.ddbDescribe(ctx, req.Table)
		if err == nil {
			res.Table = t
			res.Table.Name = redactName(&res, t.Name)
		}
	case AWSOpDDBScan, AWSOpDDBQuery:
		err = c.ddbRead(ctx, req, &res)
	case AWSOpDDBGet:
		err = c.ddbGet(ctx, req, &res)
	case AWSOpDDBStream:
		err = c.ddbStream(ctx, req, &res)
	}
	if err != nil {
		return AWSResult{}, awsFail(err)
	}
	return res, nil
}

func (m *Manager) awsDownloadMax() int64 {
	if m.opts.AWSDownloadMax > 0 {
		return m.opts.AWSDownloadMax
	}
	return edge.DefaultAWSDownloadMax
}

// awsResources answers the manifest the plan wrote (resources.json beside
// the config and credentials files), as written; a run with no applied plan
// answers none and says so.
func (m *Manager) awsResources(run Run, res *AWSResult) error {
	path := filepath.Join(m.runDir(run.Session, run.ID), "sandbox", AWSResourcesFile)
	b, err := readCapped(path, awsMaxReplyBytes)
	if errors.Is(err, os.ErrNotExist) {
		res.Note = "no plan has been applied to this run, so there is no manifest"
		return nil
	}
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return errors.New("aws: resources.json is not JSON")
	}
	res.Resources = b
	return nil
}

// AWSResourcesFile is the manifest's Project.Files path, bound at
// AWSMount/resources.json.
const AWSResourcesFile = awsFilesDir + "/resources.json"

func readCapped(path string, n int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a path under the run's own directory
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("%s is over %d bytes", filepath.Base(path), n)
	}
	return b, nil
}

// redactName passes one name through redaction, counting what it masked.
func redactName(res *AWSResult, s string) string {
	out, n := redact.Line(s, nil)
	res.Redacted += n
	return out
}

func checkBucket(b string) error {
	if !s3BucketNameRe.MatchString(b) {
		return paramsError("bucket must be an S3 bucket name")
	}
	return nil
}

func checkKey(k string) error {
	if k == "" || len(k) > 1024 {
		return paramsError("key must be 1-1024 bytes")
	}
	return nil
}

func (c awsClient) s3Buckets(ctx context.Context, res *AWSResult) error {
	var out struct {
		Buckets []struct {
			Name         string `xml:"Name"`
			CreationDate string `xml:"CreationDate"`
		} `xml:"Buckets>Bucket"`
	}
	if err := c.s3XML(ctx, "/", nil, &out); err != nil {
		return err
	}
	res.Buckets = []S3Bucket{}
	for i, b := range out.Buckets {
		if i >= awsMaxBuckets {
			res.Truncated = true
			break
		}
		entry := S3Bucket{Name: b.Name, Created: b.CreationDate}
		var v struct {
			Status string `xml:"Status"`
		}
		if c.s3XML(ctx, "/"+b.Name, url.Values{"versioning": {""}}, &v) == nil {
			entry.Versioning = v.Status
		}
		var n struct {
			Queues  []string  `xml:"QueueConfiguration>Queue"`
			Topics  []string  `xml:"TopicConfiguration>Topic"`
			Lambdas []string  `xml:"CloudFunctionConfiguration>CloudFunction"`
			Bridge  *struct{} `xml:"EventBridgeConfiguration"`
		}
		if c.s3XML(ctx, "/"+b.Name, url.Values{"notification": {""}}, &n) == nil {
			entry.Notifications = append(append(append(entry.Notifications, n.Queues...), n.Topics...), n.Lambdas...)
			if n.Bridge != nil {
				entry.Notifications = append(entry.Notifications, "eventbridge")
			}
		}
		entry.Name = redactName(res, entry.Name)
		for j, d := range entry.Notifications {
			entry.Notifications[j] = redactName(res, d)
		}
		res.Buckets = append(res.Buckets, entry)
	}
	return nil
}

func (c awsClient) s3Objects(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	if err := checkBucket(req.Bucket); err != nil {
		return err
	}
	if req.Limit < 0 || req.Limit > MaxS3Objects {
		return paramsError(fmt.Sprintf("limit must be 1-%d", MaxS3Objects))
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultS3Objects
	}
	delim := "/"
	if req.Delimiter != nil {
		delim = *req.Delimiter
	}
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(limit)}}
	if req.Prefix != "" {
		q.Set("prefix", req.Prefix)
	}
	if delim != "" {
		q.Set("delimiter", delim)
	}
	if req.Token != "" {
		q.Set("continuation-token", req.Token)
	}
	var out struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			ETag         string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		Prefixes    []string `xml:"CommonPrefixes>Prefix"`
		IsTruncated bool     `xml:"IsTruncated"`
		Next        string   `xml:"NextContinuationToken"`
	}
	if err := c.s3XML(ctx, "/"+req.Bucket, q, &out); err != nil {
		return err
	}
	res.Objects = []S3ObjectEntry{}
	for _, o := range out.Contents {
		res.Objects = append(res.Objects, S3ObjectEntry{Key: redactName(res, o.Key), Size: o.Size, ETag: strings.Trim(o.ETag, `"`), Modified: o.LastModified})
	}
	for _, p := range out.Prefixes {
		res.Prefixes = append(res.Prefixes, redactName(res, p))
	}
	if out.IsTruncated {
		res.NextToken = out.Next
	}
	return nil
}

func (c awsClient) s3Versions(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	if err := checkBucket(req.Bucket); err != nil {
		return err
	}
	if err := checkKey(req.Key); err != nil {
		return err
	}
	type ver struct {
		Key          string `xml:"Key"`
		VersionID    string `xml:"VersionId"`
		IsLatest     bool   `xml:"IsLatest"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	}
	var out struct {
		Versions []ver `xml:"Version"`
		Markers  []ver `xml:"DeleteMarker"`
		Trunc    bool  `xml:"IsTruncated"`
	}
	if err := c.s3XML(ctx, "/"+req.Bucket, url.Values{"versions": {""}, "prefix": {req.Key}}, &out); err != nil {
		return err
	}
	res.Versions = []S3VersionEntry{}
	for _, v := range out.Versions {
		if v.Key == req.Key {
			res.Versions = append(res.Versions, S3VersionEntry{Version: v.VersionID, Latest: v.IsLatest, Size: v.Size, ETag: strings.Trim(v.ETag, `"`), Modified: v.LastModified})
		}
	}
	for _, v := range out.Markers {
		if v.Key == req.Key {
			res.Versions = append(res.Versions, S3VersionEntry{Version: v.VersionID, Latest: v.IsLatest, DeleteMarker: true, Modified: v.LastModified})
		}
	}
	sort.SliceStable(res.Versions, func(i, j int) bool { return res.Versions[i].Modified > res.Versions[j].Modified })
	res.Truncated = out.Trunc
	return nil
}

// s3Head reads an object's head: size, type, metadata, lock state.
func (c awsClient) s3Head(ctx context.Context, bucket, key, version string, res *AWSResult) (*S3ObjectPage, error) {
	q := url.Values{}
	if version != "" {
		q.Set("versionId", version)
	}
	resp, err := c.s3(ctx, http.MethodHead, "/"+bucket+"/"+key, q, nil)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	h := resp.Header
	p := &S3ObjectPage{
		Bucket: bucket, Key: redactName(res, key), Version: h.Get("X-Amz-Version-Id"), ContentType: h.Get("Content-Type"),
		Size: resp.ContentLength, ETag: strings.Trim(h.Get("ETag"), `"`), Modified: h.Get("Last-Modified"),
	}
	for name, vals := range h {
		if meta, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok && len(vals) > 0 {
			if p.Metadata == nil {
				p.Metadata = map[string]string{}
			}
			v := vals[0]
			if awsSecretAttr.MatchString(meta) {
				v = redact.Mark
				res.Redacted++
			} else {
				v = cutCell(redactName(res, v))
			}
			p.Metadata[meta] = v
		}
	}
	if mode, until, hold := h.Get("X-Amz-Object-Lock-Mode"), h.Get("X-Amz-Object-Lock-Retain-Until-Date"), h.Get("X-Amz-Object-Lock-Legal-Hold"); mode != "" || until != "" || hold != "" {
		p.Lock = &S3Lock{Mode: mode, RetainUtil: until, LegalHold: hold}
	}
	return p, nil
}

// s3Range GETs bytes [off, off+n) of an object.
func (c awsClient) s3Range(ctx context.Context, bucket, key, version string, off, n int64) ([]byte, error) {
	q := url.Values{}
	if version != "" {
		q.Set("versionId", version)
	}
	resp, err := c.s3(ctx, http.MethodGet, "/"+bucket+"/"+key, q, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", off, off+n-1)})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, n))
}

func (c awsClient) s3Object(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	if err := checkBucket(req.Bucket); err != nil {
		return err
	}
	if err := checkKey(req.Key); err != nil {
		return err
	}
	if req.Offset < 0 || req.Limit < 0 || req.Limit > MaxS3ObjectPage {
		return paramsError(fmt.Sprintf("offset must not be negative and limit must be 1-%d", MaxS3ObjectPage))
	}
	limit := int64(req.Limit)
	if limit == 0 {
		limit = MaxS3ObjectPage
	}
	p, err := c.s3Head(ctx, req.Bucket, req.Key, req.Version, res)
	if err != nil {
		return err
	}
	var tags struct {
		Tags []struct {
			Key   string `xml:"Key"`
			Value string `xml:"Value"`
		} `xml:"TagSet>Tag"`
	}
	tq := url.Values{"tagging": {""}}
	if req.Version != "" {
		tq.Set("versionId", req.Version)
	}
	if c.s3XML(ctx, "/"+req.Bucket+"/"+req.Key, tq, &tags) == nil {
		for _, t := range tags.Tags {
			if p.Tags == nil {
				p.Tags = map[string]string{}
			}
			v := t.Value
			if awsSecretAttr.MatchString(t.Key) {
				v = redact.Mark
				res.Redacted++
			}
			p.Tags[redactName(res, t.Key)] = redactName(res, v)
		}
	}
	res.Object = p
	p.Offset, p.Next = req.Offset, req.Offset
	if p.Size == 0 || req.Offset >= p.Size {
		p.EOF = true
		return nil
	}
	sniff, err := c.s3Range(ctx, req.Bucket, req.Key, req.Version, 0, min(awsBinarySniff, p.Size))
	if err != nil {
		return err
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		p.Binary = true
		return nil
	}
	n := min(limit, p.Size-req.Offset)
	page, err := c.s3Range(ctx, req.Bucket, req.Key, req.Version, req.Offset, n)
	if err != nil {
		return err
	}
	used := len(page)
	if req.Offset+int64(used) < p.Size {
		// Cut on a rune boundary so pages join.
		for i := used - 1; i >= 0 && i >= used-utf8.UTFMax; i-- {
			if utf8.RuneStart(page[i]) {
				if !utf8.FullRune(page[i:used]) {
					used = i
				}
				break
			}
		}
	}
	lines, red := redact.Lines(strings.Split(string(page[:used]), "\n"), nil)
	res.Redacted += red
	p.Data = strings.Join(lines, "\n")
	p.Next = req.Offset + int64(used)
	p.EOF = p.Next >= p.Size
	return nil
}

func (c awsClient) s3Download(ctx context.Context, req AWSReadRequest, max int64, res *AWSResult) error {
	if err := checkBucket(req.Bucket); err != nil {
		return err
	}
	if err := checkKey(req.Key); err != nil {
		return err
	}
	if req.Offset < 0 || req.Limit < 0 || req.Limit > MaxS3DownloadPage {
		return paramsError(fmt.Sprintf("offset must not be negative and limit must be 1-%d", MaxS3DownloadPage))
	}
	limit := int64(req.Limit)
	if limit == 0 {
		limit = MaxS3DownloadPage
	}
	if req.Offset+limit > max {
		limit = max - req.Offset
	}
	if limit <= 0 {
		return paramsError(fmt.Sprintf("offset %d is past the download cap of %d bytes (harness.aws.download_max)", req.Offset, max))
	}
	p, err := c.s3Head(ctx, req.Bucket, req.Key, req.Version, res)
	if err != nil {
		return err
	}
	d := &S3DownloadPage{Bucket: req.Bucket, Key: p.Key, Size: p.Size, Offset: req.Offset, Next: req.Offset, Max: max, Capped: p.Size > max}
	res.Download = d
	end := min(p.Size, max)
	if req.Offset >= end {
		d.EOF = true
		return nil
	}
	n := min(limit, end-req.Offset)
	page, err := c.s3Range(ctx, req.Bucket, req.Key, req.Version, req.Offset, n)
	if err != nil {
		return err
	}
	d.Data = base64.StdEncoding.EncodeToString(page)
	d.Next = req.Offset + int64(len(page))
	d.EOF = d.Next >= end
	return nil
}

func (c awsClient) ddbTables(ctx context.Context, res *AWSResult) error {
	res.Tables = []string{}
	start := ""
	for {
		in := map[string]any{"Limit": 100}
		if start != "" {
			in["ExclusiveStartTableName"] = start
		}
		var out struct {
			TableNames []string `json:"TableNames"`
			Last       string   `json:"LastEvaluatedTableName"`
		}
		if err := c.ddb(ctx, "DynamoDB_20120810.ListTables", in, &out); err != nil {
			return err
		}
		for _, t := range out.TableNames {
			res.Tables = append(res.Tables, redactName(res, t))
		}
		if out.Last == "" {
			return nil
		}
		if len(res.Tables) >= awsMaxTables {
			res.Truncated = true
			return nil
		}
		start = out.Last
	}
}

type rawKeyElem struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"`
}

type rawIndex struct {
	IndexName  string       `json:"IndexName"`
	KeySchema  []rawKeyElem `json:"KeySchema"`
	Projection struct {
		ProjectionType string `json:"ProjectionType"`
	} `json:"Projection"`
}

type rawTable struct {
	Table struct {
		TableName   string       `json:"TableName"`
		TableStatus string       `json:"TableStatus"`
		KeySchema   []rawKeyElem `json:"KeySchema"`
		Attrs       []struct {
			Name string `json:"AttributeName"`
			Type string `json:"AttributeType"`
		} `json:"AttributeDefinitions"`
		GSIs       []rawIndex `json:"GlobalSecondaryIndexes"`
		LSIs       []rawIndex `json:"LocalSecondaryIndexes"`
		StreamArn  string     `json:"LatestStreamArn"`
		StreamSpec *struct {
			Enabled bool   `json:"StreamEnabled"`
			View    string `json:"StreamViewType"`
		} `json:"StreamSpecification"`
		ItemCount      json.Number `json:"ItemCount"`
		TableSizeBytes json.Number `json:"TableSizeBytes"`
	} `json:"Table"`
}

func keysOf(in []rawKeyElem) []DDBKey {
	out := make([]DDBKey, 0, len(in))
	for _, k := range in {
		out = append(out, DDBKey{Name: k.AttributeName, Type: k.KeyType})
	}
	return out
}

func indexesOf(in []rawIndex) []DDBIndex {
	var out []DDBIndex
	for _, i := range in {
		out = append(out, DDBIndex{Name: i.IndexName, KeySchema: keysOf(i.KeySchema), Projection: i.Projection.ProjectionType})
	}
	return out
}

func checkTable(t string) error {
	if t == "" || len(t) > 1024 {
		return paramsError("table is required")
	}
	return nil
}

func (c awsClient) ddbDescribe(ctx context.Context, table string) (*DDBTableInfo, error) {
	if err := checkTable(table); err != nil {
		return nil, err
	}
	var out rawTable
	if err := c.ddb(ctx, "DynamoDB_20120810.DescribeTable", map[string]any{"TableName": table}, &out); err != nil {
		return nil, err
	}
	t := out.Table
	info := &DDBTableInfo{
		Name: t.TableName, Status: t.TableStatus, KeySchema: keysOf(t.KeySchema), Attributes: map[string]string{},
		GSIs: indexesOf(t.GSIs), LSIs: indexesOf(t.LSIs), StreamARN: t.StreamArn,
	}
	info.ItemCount, _ = t.ItemCount.Int64()
	info.SizeBytes, _ = t.TableSizeBytes.Int64()
	for _, a := range t.Attrs {
		info.Attributes[a.Name] = a.Type
	}
	if t.StreamSpec != nil && t.StreamSpec.Enabled {
		info.StreamView = t.StreamSpec.View
	}
	var ttl struct {
		D struct {
			Status string `json:"TimeToLiveStatus"`
			Attr   string `json:"AttributeName"`
		} `json:"TimeToLiveDescription"`
	}
	if c.ddb(ctx, "DynamoDB_20120810.DescribeTimeToLive", map[string]any{"TableName": table}, &ttl) == nil {
		info.TTL, info.TTLStatus = ttl.D.Attr, ttl.D.Status
	}
	return info, nil
}

// keyNames is the key schema of the table or the index: partition key
// first, then the sort key if any.
func (t *DDBTableInfo) keyNames(index string) ([]string, error) {
	schema := t.KeySchema
	if index != "" {
		found := false
		for _, i := range slices.Concat(t.GSIs, t.LSIs) {
			if i.Name == index {
				schema, found = i.KeySchema, true
			}
		}
		if !found {
			return nil, paramsError(fmt.Sprintf("table %s has no index %q", t.Name, index))
		}
	}
	var hash, rng string
	for _, k := range schema {
		switch k.Type {
		case "HASH":
			hash = k.Name
		case "RANGE":
			rng = k.Name
		}
	}
	if rng == "" {
		return []string{hash}, nil
	}
	return []string{hash, rng}, nil
}

func checkItemLimit(n int) (int, error) {
	if n < 0 || n > MaxDDBItems {
		return 0, paramsError(fmt.Sprintf("limit must be 1-%d", MaxDDBItems))
	}
	if n == 0 {
		return MaxDDBItems, nil
	}
	return n, nil
}

// ddbRead runs a Scan or a Query.
func (c awsClient) ddbRead(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	limit, err := checkItemLimit(req.Limit)
	if err != nil {
		return err
	}
	if err := checkTable(req.Table); err != nil {
		return err
	}
	in := map[string]any{"TableName": req.Table, "Limit": limit}
	if req.Index != "" {
		in["IndexName"] = req.Index
	}
	if req.Start != "" {
		key, err := decodeLastKey(req.Start)
		if err != nil {
			return err
		}
		in["ExclusiveStartKey"] = key
	}
	target := "DynamoDB_20120810.Scan"
	if req.Op == AWSOpDDBQuery {
		target = "DynamoDB_20120810.Query"
		if err := c.queryInput(ctx, req, in); err != nil {
			return err
		}
	}
	var out struct {
		Items []map[string]any `json:"Items"`
		Last  map[string]any   `json:"LastEvaluatedKey"`
	}
	if err := c.ddb(ctx, target, in, &out); err != nil {
		return err
	}
	res.Items = []map[string]any{}
	size := 0
	for _, raw := range out.Items {
		item := showItem(res, raw)
		b, _ := json.Marshal(item)
		size += len(b)
		if size > awsMaxReplyBytes {
			res.Truncated = true
			break
		}
		res.Items = append(res.Items, item)
	}
	if len(out.Last) > 0 && !res.Truncated {
		res.LastKey = encodeLastKey(out.Last)
	}
	return nil
}

var sortOps = map[string]string{"=": "=", "<": "<", "<=": "<=", ">": ">", ">=": ">="}

// queryInput fills a Query's key condition from pk and sk, typing each
// value from the table's attribute definitions.
func (c awsClient) queryInput(ctx context.Context, req AWSReadRequest, in map[string]any) error {
	if req.PK == nil {
		return paramsError("ddb_query needs pk {name, value}")
	}
	t, err := c.ddbDescribe(ctx, req.Table)
	if err != nil {
		return err
	}
	keys, err := t.keyNames(req.Index)
	if err != nil {
		return err
	}
	if req.PK.Name != keys[0] {
		return paramsError(fmt.Sprintf("pk name must be the partition key %q", keys[0]))
	}
	pv, err := toAttr(req.PK.Value, t.Attributes[keys[0]])
	if err != nil {
		return paramsError("pk value: " + err.Error())
	}
	names := map[string]any{"#pk": keys[0]}
	values := map[string]any{":pk": pv}
	cond := "#pk = :pk"
	if req.SK != nil {
		if len(keys) < 2 {
			return paramsError("this table or index has no sort key")
		}
		typ := t.Attributes[keys[1]]
		names["#sk"] = keys[1]
		want := 1
		if req.SK.Op == "between" {
			want = 2
		}
		if len(req.SK.Values) != want {
			return paramsError(fmt.Sprintf("sk op %q takes %d value(s)", req.SK.Op, want))
		}
		for i, v := range req.SK.Values {
			av, err := toAttr(v, typ)
			if err != nil {
				return paramsError("sk value: " + err.Error())
			}
			values[":sk"+strconv.Itoa(i)] = av
		}
		switch req.SK.Op {
		case "begins_with":
			cond += " AND begins_with(#sk, :sk0)"
		case "between":
			cond += " AND #sk BETWEEN :sk0 AND :sk1"
		default:
			op, ok := sortOps[req.SK.Op]
			if !ok {
				return paramsError("sk op must be one of = < <= > >= begins_with between")
			}
			cond += " AND #sk " + op + " :sk0"
		}
	}
	in["KeyConditionExpression"] = cond
	in["ExpressionAttributeNames"] = names
	in["ExpressionAttributeValues"] = values
	if req.Forward != nil {
		in["ScanIndexForward"] = *req.Forward
	}
	return nil
}

func (c awsClient) ddbGet(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	if len(req.ItemKey) == 0 {
		return paramsError("ddb_get needs key, the item's key attributes as JSON")
	}
	t, err := c.ddbDescribe(ctx, req.Table)
	if err != nil {
		return err
	}
	keys, _ := t.keyNames("")
	key := map[string]any{}
	for _, k := range keys {
		v, ok := req.ItemKey[k]
		if !ok {
			return paramsError(fmt.Sprintf("key needs %v", keys))
		}
		av, err := toAttr(v, t.Attributes[k])
		if err != nil {
			return paramsError(fmt.Sprintf("key %s: %v", k, err))
		}
		key[k] = av
	}
	if len(req.ItemKey) != len(keys) {
		return paramsError(fmt.Sprintf("key holds exactly %v", keys))
	}
	var out struct {
		Item map[string]any `json:"Item"`
	}
	if err := c.ddb(ctx, "DynamoDB_20120810.GetItem", map[string]any{"TableName": req.Table, "Key": key, "ConsistentRead": true}, &out); err != nil {
		return err
	}
	found := out.Item != nil
	res.Found = &found
	if found {
		res.Item = showItem(res, out.Item)
	}
	return nil
}

func (c awsClient) ddbStream(ctx context.Context, req AWSReadRequest, res *AWSResult) error {
	limit, err := checkItemLimit(req.Limit)
	if err != nil {
		return err
	}
	t, err := c.ddbDescribe(ctx, req.Table)
	if err != nil {
		return err
	}
	if t.StreamARN == "" {
		return paramsError(fmt.Sprintf("table %s has no stream", req.Table))
	}
	var desc struct {
		D struct {
			Shards []struct {
				ID string `json:"ShardId"`
			} `json:"Shards"`
		} `json:"StreamDescription"`
	}
	if err := c.ddb(ctx, "DynamoDBStreams_20120810.DescribeStream", map[string]any{"StreamArn": t.StreamARN}, &desc); err != nil {
		return err
	}
	var all []DDBStreamRecord
	for i, sh := range desc.D.Shards {
		if i >= awsMaxShards {
			res.Truncated = true
			break
		}
		var it struct {
			Iterator string `json:"ShardIterator"`
		}
		in := map[string]any{"StreamArn": t.StreamARN, "ShardId": sh.ID, "ShardIteratorType": "TRIM_HORIZON"}
		if err := c.ddb(ctx, "DynamoDBStreams_20120810.GetShardIterator", in, &it); err != nil {
			return err
		}
		iter := it.Iterator
		for page := 0; iter != "" && page < awsStreamPages; page++ {
			var out struct {
				Records []struct {
					Event string `json:"eventName"`
					D     struct {
						Seq     string         `json:"SequenceNumber"`
						Created json.Number    `json:"ApproximateCreationDateTime"`
						Keys    map[string]any `json:"Keys"`
						Old     map[string]any `json:"OldImage"`
						New     map[string]any `json:"NewImage"`
					} `json:"dynamodb"`
				} `json:"Records"`
				Next string `json:"NextShardIterator"`
			}
			if err := c.ddb(ctx, "DynamoDBStreams_20120810.GetRecords", map[string]any{"ShardIterator": iter, "Limit": 1000}, &out); err != nil {
				return err
			}
			for _, r := range out.Records {
				rec := DDBStreamRecord{Event: r.Event, Sequence: r.D.Seq}
				if f, err := r.D.Created.Float64(); err == nil && f > 0 {
					rec.Time = time.Unix(int64(f), 0).UTC().Format(time.RFC3339)
				}
				if r.D.Keys != nil {
					rec.Keys = showItem(res, r.D.Keys)
				}
				if r.D.Old != nil {
					rec.Old = showItem(res, r.D.Old)
				}
				if r.D.New != nil {
					rec.New = showItem(res, r.D.New)
				}
				all = append(all, rec)
			}
			if len(out.Records) == 0 || out.Next == iter {
				break
			}
			iter = out.Next
		}
	}
	// Newest first: sequence numbers are decimal strings of growing length.
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].Sequence, all[j].Sequence
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return a > b
	})
	if len(all) > limit {
		all, res.Truncated = all[:limit], true
	}
	res.Records = []DDBStreamRecord{}
	size := 0
	for _, r := range all {
		b, _ := json.Marshal(r)
		size += len(b)
		if size > awsMaxReplyBytes {
			res.Truncated = true
			break
		}
		res.Records = append(res.Records, r)
	}
	return nil
}

// encodeLastKey makes a LastEvaluatedKey an opaque paging token, kept as
// the emulator sent it so paging never depends on what redaction shows.
func encodeLastKey(k map[string]any) string {
	b, _ := json.Marshal(k)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeLastKey(s string) (map[string]any, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 16<<10 {
		return nil, paramsError("start must be a last_key from an earlier answer")
	}
	v, err := edge.DecodeStrictJSON(b)
	m, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, paramsError("start must be a last_key from an earlier answer")
	}
	return m, nil
}

// toAttr types a plain JSON key value as the attribute type typ (S, N or
// B; an unknown type takes the value's own: string S, number N). A typed
// marker {"$b64": …} is B.
func toAttr(v any, typ string) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		if b, ok := m["$b64"].(string); ok && len(m) == 1 {
			v, typ = b, "B"
		} else {
			return nil, errors.New("a key value is a string, a number or {\"$b64\": …}")
		}
	}
	switch typ {
	case "S":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("want a string")
		}
		return map[string]any{"S": s}, nil
	case "N":
		switch n := v.(type) {
		case json.Number:
			return map[string]any{"N": n.String()}, nil
		case float64:
			return map[string]any{"N": strconv.FormatFloat(n, 'f', -1, 64)}, nil
		case string:
			if _, err := strconv.ParseFloat(n, 64); err != nil {
				return nil, errors.New("want a number")
			}
			return map[string]any{"N": n}, nil
		}
		return nil, errors.New("want a number")
	case "B":
		s, ok := v.(string)
		if _, err := base64.StdEncoding.DecodeString(s); !ok || err != nil {
			return nil, errors.New("want base64")
		}
		return map[string]any{"B": s}, nil
	}
	switch x := v.(type) {
	case string:
		return map[string]any{"S": x}, nil
	case json.Number, float64:
		return toAttr(x, "N")
	}
	return nil, errors.New("want a string or a number")
}

// showItem turns an item's AttributeValues into plain JSON: S a string, N a
// number, BOOL, NULL null, L an array, M an object, B {"$b64"}, sets
// {"$ss"}, {"$ns"}, {"$bs"}. Strings are redacted and cut at 4 KiB; every
// attribute whose name looks secret is [redacted].
func showItem(res *AWSResult, item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for name, av := range item {
		out[name] = showAttr(res, name, av)
	}
	return out
}

func showAttr(res *AWSResult, name string, raw any) any {
	if awsSecretAttr.MatchString(name) {
		res.Redacted++
		if !slices.Contains(res.RedactedAttributes, name) {
			res.RedactedAttributes = append(res.RedactedAttributes, name)
		}
		return redact.Mark
	}
	av, ok := raw.(map[string]any)
	if !ok || len(av) != 1 {
		return nil
	}
	for typ, v := range av {
		switch typ {
		case "S":
			s, _ := v.(string)
			return showString(res, s)
		case "N":
			if s, ok := v.(string); ok {
				return json.Number(s)
			}
			return v
		case "BOOL":
			return v
		case "NULL":
			return nil
		case "B":
			return map[string]any{"$b64": v}
		case "SS":
			return map[string]any{"$ss": showStrings(res, v)}
		case "NS":
			list, _ := v.([]any)
			ns := make([]any, 0, len(list))
			for _, x := range list {
				if s, ok := x.(string); ok {
					ns = append(ns, json.Number(s))
				}
			}
			return map[string]any{"$ns": ns}
		case "BS":
			return map[string]any{"$bs": v}
		case "L":
			list, _ := v.([]any)
			out := make([]any, 0, len(list))
			for _, x := range list {
				out = append(out, showAttr(res, "", x))
			}
			return out
		case "M":
			m, _ := v.(map[string]any)
			return showItem(res, m)
		}
	}
	return nil
}

func showStrings(res *AWSResult, v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, showString(res, s))
		}
	}
	return out
}

func showString(res *AWSResult, s string) string {
	lines, n := redact.Lines(strings.Split(s, "\n"), nil)
	res.Redacted += n
	return cutCell(strings.Join(lines, "\n"))
}
