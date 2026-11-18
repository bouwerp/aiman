package edge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/redact"
)

// AWS wire protocols the classifier tells apart.
const (
	AWSProtocolREST  = "rest-xml"
	AWSProtocolJSON  = "json"
	AWSProtocolQuery = "query"
	AWSProtocolAdmin = "admin"
)

// AWS services a call is attributed to.
const (
	AWSServiceS3              = "s3"
	AWSServiceDynamoDB        = "dynamodb"
	AWSServiceDynamoDBStreams = "dynamodbstreams"
	AWSServiceSQS             = "sqs"
	AWSServiceSNS             = "sns"
)

// AWSResource is what a call acted on. Each field is set only when the
// request names it.
type AWSResource struct {
	Bucket string `json:"bucket,omitempty"`
	Key    string `json:"key,omitempty"`
	Table  string `json:"table,omitempty"`
	Index  string `json:"index,omitempty"`
	Queue  string `json:"queue,omitempty"`
	Topic  string `json:"topic,omitempty"`
	// Stream is a DynamoDB stream ARN.
	Stream string `json:"stream,omitempty"`
}

// awsScope is a SigV4 credential scope: key id/date/region/service/aws4_request.
type awsScope struct {
	key, region, service string
}

// parseScope reads the credential scope from the Authorization header or,
// for a presigned URL, the X-Amz-Credential query parameter.
func parseScope(h http.Header, q url.Values) (awsScope, bool) {
	cred := q.Get("X-Amz-Credential")
	if auth := h.Get("Authorization"); cred == "" && strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		_, rest, ok := strings.Cut(auth, "Credential=")
		if !ok {
			return awsScope{}, false
		}
		cred, _, _ = strings.Cut(rest, ",")
	}
	parts := strings.Split(strings.TrimSpace(cred), "/")
	if len(parts) != 5 || parts[4] != "aws4_request" {
		return awsScope{}, false
	}
	return awsScope{key: parts[0], region: parts[2], service: strings.ToLower(parts[3])}, true
}

// jsonTargets maps an X-Amz-Target prefix to its service.
var jsonTargets = map[string]string{
	"DynamoDB_20120810":        AWSServiceDynamoDB,
	"DynamoDBStreams_20120810": AWSServiceDynamoDBStreams,
	"AmazonSQS":                AWSServiceSQS,
}

// queryVersions maps a query protocol Version to its service, for a request
// with no credential scope.
var queryVersions = map[string]string{
	"2010-03-31": AWSServiceSNS,
	"2012-11-05": AWSServiceSQS,
}

// awsClassified is what the classifier reads from a request before it is
// forwarded: everything but the body.
type awsClassified struct {
	protocol, service, operation string
	resource                     AWSResource
	scope                        awsScope
	scoped                       bool
	presigned                    bool
}

// classifyAWS reads the protocol, service and operation from the request
// line and headers. A JSON or query request names its resource in the body,
// which classifyBody reads once it has passed.
func classifyAWS(r *http.Request) awsClassified {
	q := r.URL.Query()
	c := awsClassified{presigned: q.Get("X-Amz-Signature") != ""}
	c.scope, c.scoped = parseScope(r.Header, q)
	if c.scoped {
		c.service = c.scope.service
	}
	if strings.HasPrefix(r.URL.Path, "/_") {
		c.protocol, c.operation = AWSProtocolAdmin, r.URL.Path
		if c.service == "" {
			c.service = "admin"
		}
		return c
	}
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		prefix, op, _ := strings.Cut(target, ".")
		c.protocol, c.operation = AWSProtocolJSON, op
		if svc, ok := jsonTargets[prefix]; ok {
			c.service = svc
		} else if c.service == "" {
			c.service = strings.ToLower(prefix)
		}
		return c
	}
	if action := q.Get("Action"); action != "" || isForm(r) {
		c.protocol, c.operation = AWSProtocolQuery, action
		if c.service == "" {
			c.service = queryVersions[q.Get("Version")]
		}
		queryResource(q, &c.resource)
		return c
	}
	c.protocol = AWSProtocolREST
	if c.service == "" {
		c.service = AWSServiceS3
	}
	var bucket, key string
	c.operation, bucket, key = S3Operation(r.Method, r.URL.EscapedPath(), q, r.Header)
	c.resource.Bucket, c.resource.Key = bucket, key
	return c
}

func isForm(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded")
}

// s3Sub is one subresource row of the S3 table: the query key that selects
// it and the operation per method.
type s3Sub struct {
	key                     string
	get, put, del, post, hd string
}

// s3BucketSubs are bucket subresources, first match wins.
var s3BucketSubs = []s3Sub{
	{key: "uploads", get: "ListMultipartUploads"},
	{key: "versions", get: "ListObjectVersions"},
	{key: "location", get: "GetBucketLocation"},
	{key: "versioning", get: "GetBucketVersioning", put: "PutBucketVersioning"},
	{key: "notification", get: "GetBucketNotificationConfiguration", put: "PutBucketNotificationConfiguration"},
	{key: "cors", get: "GetBucketCors", put: "PutBucketCors", del: "DeleteBucketCors"},
	{key: "lifecycle", get: "GetBucketLifecycleConfiguration", put: "PutBucketLifecycleConfiguration", del: "DeleteBucketLifecycle"},
	{key: "policy", get: "GetBucketPolicy", put: "PutBucketPolicy", del: "DeleteBucketPolicy"},
	{key: "policyStatus", get: "GetBucketPolicyStatus"},
	{key: "tagging", get: "GetBucketTagging", put: "PutBucketTagging", del: "DeleteBucketTagging"},
	{key: "encryption", get: "GetBucketEncryption", put: "PutBucketEncryption", del: "DeleteBucketEncryption"},
	{key: "object-lock", get: "GetObjectLockConfiguration", put: "PutObjectLockConfiguration"},
	{key: "acl", get: "GetBucketAcl", put: "PutBucketAcl"},
	{key: "website", get: "GetBucketWebsite", put: "PutBucketWebsite", del: "DeleteBucketWebsite"},
	{key: "logging", get: "GetBucketLogging", put: "PutBucketLogging"},
	{key: "ownershipControls", get: "GetBucketOwnershipControls", put: "PutBucketOwnershipControls", del: "DeleteBucketOwnershipControls"},
	{key: "publicAccessBlock", get: "GetPublicAccessBlock", put: "PutPublicAccessBlock", del: "DeletePublicAccessBlock"},
	{key: "replication", get: "GetBucketReplication", put: "PutBucketReplication", del: "DeleteBucketReplication"},
	{key: "accelerate", get: "GetBucketAccelerateConfiguration", put: "PutBucketAccelerateConfiguration"},
	{key: "requestPayment", get: "GetBucketRequestPayment", put: "PutBucketRequestPayment"},
	{key: "delete", post: "DeleteObjects"},
}

// s3ObjectSubs are object subresources, first match wins.
var s3ObjectSubs = []s3Sub{
	{key: "uploads", post: "CreateMultipartUpload"},
	{key: "uploadId", get: "ListParts", put: "UploadPart", del: "AbortMultipartUpload", post: "CompleteMultipartUpload"},
	{key: "tagging", get: "GetObjectTagging", put: "PutObjectTagging", del: "DeleteObjectTagging"},
	{key: "acl", get: "GetObjectAcl", put: "PutObjectAcl"},
	{key: "attributes", get: "GetObjectAttributes"},
	{key: "retention", get: "GetObjectRetention", put: "PutObjectRetention"},
	{key: "legal-hold", get: "GetObjectLegalHold", put: "PutObjectLegalHold"},
	{key: "restore", post: "RestoreObject"},
	{key: "select", post: "SelectObjectContent"},
	{key: "torrent", get: "GetObjectTorrent"},
}

func (s s3Sub) op(method string) string {
	switch method {
	case http.MethodGet:
		return s.get
	case http.MethodPut:
		return s.put
	case http.MethodDelete:
		return s.del
	case http.MethodPost:
		return s.post
	case http.MethodHead:
		return s.hd
	}
	return ""
}

// S3Operation names the S3 REST operation of a path-style request and
// returns its bucket and key: escapedPath is the request's escaped path.
// An unknown shape is "<METHOD> object", "<METHOD> bucket" or "<METHOD>".
func S3Operation(method, escapedPath string, q url.Values, h http.Header) (op, bucket, key string) {
	p := strings.TrimPrefix(escapedPath, "/")
	rawBucket, rawKey, hasKey := strings.Cut(p, "/")
	bucket = unescapePath(rawBucket)
	key = unescapePath(rawKey)
	switch {
	case bucket == "":
		if method == http.MethodGet {
			return "ListBuckets", "", ""
		}
		return method, "", ""
	case !hasKey || key == "":
		return s3BucketOp(method, q), bucket, ""
	default:
		return s3ObjectOp(method, q, h), bucket, key
	}
}

func s3BucketOp(method string, q url.Values) string {
	for _, s := range s3BucketSubs {
		if q.Has(s.key) {
			if op := s.op(method); op != "" {
				return op
			}
		}
	}
	switch method {
	case http.MethodGet:
		if q.Get("list-type") == "2" {
			return "ListObjectsV2"
		}
		return "ListObjects"
	case http.MethodHead:
		return "HeadBucket"
	case http.MethodPut:
		return "CreateBucket"
	case http.MethodDelete:
		return "DeleteBucket"
	case http.MethodPost:
		return "PostObject"
	}
	return method + " bucket"
}

func s3ObjectOp(method string, q url.Values, h http.Header) string {
	copySource := h.Get("X-Amz-Copy-Source") != ""
	if method == http.MethodPut && q.Has("uploadId") && q.Has("partNumber") && copySource {
		return "UploadPartCopy"
	}
	for _, s := range s3ObjectSubs {
		if q.Has(s.key) {
			if op := s.op(method); op != "" {
				return op
			}
		}
	}
	switch method {
	case http.MethodGet:
		return "GetObject"
	case http.MethodHead:
		return "HeadObject"
	case http.MethodPut:
		if copySource {
			return "CopyObject"
		}
		return "PutObject"
	case http.MethodDelete:
		return "DeleteObject"
	}
	return method + " object"
}

func unescapePath(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// lastSegment is a queue URL's or an ARN's last element: the queue or
// topic name.
func lastSegment(s string) string {
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// queryResource reads the queue and topic a query protocol request names.
func queryResource(q url.Values, res *AWSResource) {
	switch {
	case q.Get("QueueUrl") != "":
		res.Queue = lastSegment(q.Get("QueueUrl"))
	case q.Get("QueueName") != "":
		res.Queue = q.Get("QueueName")
	}
	for _, k := range []string{"TopicArn", "TargetArn"} {
		if v := q.Get(k); v != "" && res.Topic == "" {
			res.Topic = lastSegment(v)
		}
	}
	if res.Topic == "" && q.Get("Action") == "CreateTopic" {
		res.Topic = q.Get("Name")
	}
	if v := q.Get("SubscriptionArn"); v != "" && res.Topic == "" {
		// arn:aws:sns:region:account:topic:subscription-id
		if parts := strings.Split(v, ":"); len(parts) >= 7 {
			res.Topic = parts[5]
		}
	}
}

// jsonResource reads the table, index, stream or queue a JSON protocol
// request names.
func jsonResource(body map[string]any, res *AWSResource) {
	str := func(k string) string {
		s, _ := body[k].(string)
		return s
	}
	res.Table = str("TableName")
	if res.Table == "" && str("TableArn") != "" {
		res.Table = lastSegment(str("TableArn"))
	}
	if res.Table == "" && str("ResourceArn") != "" && strings.Contains(str("ResourceArn"), ":table/") {
		res.Table = lastSegment(str("ResourceArn"))
	}
	res.Index = str("IndexName")
	res.Stream = str("StreamArn")
	if res.Stream != "" && res.Table == "" {
		// arn:aws:dynamodb:region:account:table/<table>/stream/<label>
		if _, after, ok := strings.Cut(res.Stream, ":table/"); ok {
			res.Table, _, _ = strings.Cut(after, "/")
		}
	}
	if res.Table == "" {
		res.Table = strings.Join(requestItemTables(body), ",")
	}
	if q := str("QueueUrl"); q != "" {
		res.Queue = lastSegment(q)
	} else if q := str("QueueName"); q != "" {
		res.Queue = q
	}
}

// requestItemTables lists the tables of a batch (RequestItems keys) or a
// transaction (TransactItems[].<Op>.TableName), sorted and unique.
func requestItemTables(body map[string]any) []string {
	seen := map[string]bool{}
	if ri, ok := body["RequestItems"].(map[string]any); ok {
		for k := range ri {
			seen[k] = true
		}
	}
	if ti, ok := body["TransactItems"].([]any); ok {
		for _, it := range ti {
			m, _ := it.(map[string]any)
			for _, op := range m {
				if om, ok := op.(map[string]any); ok {
					if t, ok := om["TableName"].(string); ok && t != "" {
						seen[t] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MaxAWSParamsBytes cuts a call's params.
const MaxAWSParamsBytes = 4 << 10

// awsAttrTypes are DynamoDB AttributeValue tags.
var awsAttrTypes = map[string]bool{"S": true, "N": true, "B": true, "SS": true, "NS": true, "BS": true, "M": true, "L": true, "NULL": true, "BOOL": true}

// sizedFields are message body fields whose value becomes its size.
var sizedFields = map[string]bool{
	"MessageBody": true, "Message": true, "StringValue": true, "BinaryValue": true,
	"StringListValues": true, "BinaryListValues": true, "Subject": true,
}

// sizeOf is the placeholder a message body or attribute value becomes.
func sizeOf(n int) string { return "<" + strconv.Itoa(n) + " bytes>" }

// jsonParams turns a JSON protocol body into its params: every DynamoDB
// attribute value is its type, every message body and message attribute
// value its size. It reports false when the body is not a JSON object.
func jsonParams(body []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// typeValues replaces values in v, in place where it can, and returns the
// result.
func typeValues(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if tag, ok := attrTag(t); ok {
			return attrType(tag, t[tag])
		}
		for k, val := range t {
			if sizedFields[k] {
				t[k] = sizeOfValue(val)
				continue
			}
			t[k] = typeValues(val)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = typeValues(e)
		}
		return t
	}
	return v
}

// attrTag reports whether m is a DynamoDB AttributeValue: one key, a type tag.
func attrTag(m map[string]any) (string, bool) {
	if len(m) != 1 {
		return "", false
	}
	for k := range m {
		return k, awsAttrTypes[k]
	}
	return "", false
}

// attrType is an attribute value shown as its type: a map or a list keeps
// its shape with each member as its type.
func attrType(tag string, val any) any {
	switch tag {
	case "M":
		if m, ok := val.(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, e := range m {
				out[k] = typeValues(e)
			}
			return map[string]any{"M": out}
		}
	case "L":
		if l, ok := val.([]any); ok {
			out := make([]any, len(l))
			for i, e := range l {
				out[i] = typeValues(e)
			}
			return map[string]any{"L": out}
		}
	}
	return tag
}

func sizeOfValue(v any) any {
	switch t := v.(type) {
	case string:
		return sizeOf(len(t))
	case nil:
		return nil
	}
	b, _ := json.Marshal(v)
	return sizeOf(len(b))
}

// queryParamName matches the query protocol fields that carry a message
// body or a message attribute value.
var queryParamName = regexp.MustCompile(`(?:^|\.)(MessageBody|Message|Subject|StringValue|BinaryValue)$`)

// queryParams turns query protocol values into params, message bodies and
// attribute values as sizes.
func queryParams(q url.Values) map[string]any {
	out := make(map[string]any, len(q))
	for k, vs := range q {
		v := strings.Join(vs, ",")
		if queryParamName.MatchString(k) {
			out[k] = sizeOf(len(v))
			continue
		}
		out[k] = v
	}
	return out
}

// s3Headers are the request headers an S3 call's params keep.
var s3Headers = []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "X-Amz-Version-Id", "Content-Type", "Content-Length", "X-Amz-Copy-Source"}

// presignKeys are presigned URL query parameters a call's params leave out.
var presignKeys = map[string]bool{
	"X-Amz-Signature": true, "X-Amz-Credential": true, "X-Amz-Security-Token": true,
	"X-Amz-Algorithm": true, "X-Amz-Date": true, "X-Amz-SignedHeaders": true, "X-Amz-Expires": true,
}

// s3Params keeps an S3 call's query (without presign parameters) and the
// listed headers.
func s3Params(q url.Values, h http.Header) map[string]any {
	out := map[string]any{}
	query := map[string]any{}
	for k, vs := range q {
		if presignKeys[k] {
			continue
		}
		query[k] = strings.Join(vs, ",")
	}
	if len(query) > 0 {
		out["query"] = query
	}
	headers := map[string]any{}
	for _, k := range s3Headers {
		if v := h.Get(k); v != "" {
			headers[k] = v
		}
	}
	if len(headers) > 0 {
		out["headers"] = headers
	}
	return out
}

// finishParams encodes params, redacts secret keys and secrets by pattern,
// and cuts the result at MaxAWSParamsBytes. It returns the params, whether
// they were cut and how many values were redacted.
func finishParams(p map[string]any) (string, bool, int) {
	if len(p) == 0 {
		return "", false, 0
	}
	n := redactValue(p)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return "", false, n
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	out, c := redact.Line(s, nil)
	n += c
	if len(out) <= MaxAWSParamsBytes {
		return out, false, n
	}
	cut := MaxAWSParamsBytes
	for cut > 0 && !utf8Start(out[cut]) {
		cut--
	}
	return out[:cut], true, n
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// awsErrorCode reads the error code of a failed answer: JSON __type (after
// any '#'), XML <Code>, or the X-Amzn-ErrorType and x-amzn-query-error
// headers. head is the first bytes of the body.
func awsErrorCode(h http.Header, head []byte) string {
	if t := h.Get("X-Amzn-Errortype"); t != "" {
		return cleanCode(t)
	}
	if t := h.Get("X-Amzn-Query-Error"); t != "" {
		code, _, _ := strings.Cut(t, ";")
		return cleanCode(code)
	}
	trimmed := bytes.TrimSpace(head)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if m := jsonType.FindSubmatch(trimmed); m != nil {
			return cleanCode(string(m[1]))
		}
		return ""
	}
	if m := xmlCode.FindSubmatch(trimmed); m != nil {
		return cleanCode(string(m[1]))
	}
	return ""
}

var (
	jsonType = regexp.MustCompile(`"__type"\s*:\s*"([^"]{1,256})"`)
	xmlCode  = regexp.MustCompile(`<Code>([^<]{1,256})</Code>`)
	codeOK   = regexp.MustCompile(`^[A-Za-z0-9_.]{1,128}$`)
)

// cleanCode keeps the part after '#' and ':' and only a plain identifier.
func cleanCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "#"); i >= 0 {
		s = s[i+1:]
	}
	s, _, _ = strings.Cut(s, ":")
	if !codeOK.MatchString(s) {
		return ""
	}
	return s
}
