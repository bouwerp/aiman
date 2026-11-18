package edge

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestS3Operation(t *testing.T) {
	cases := []struct {
		method, path, query string
		header              map[string]string
		op, bucket, key     string
	}{
		{"GET", "/", "", nil, "ListBuckets", "", ""},
		{"GET", "/b", "list-type=2&prefix=a%2F", nil, "ListObjectsV2", "b", ""},
		{"GET", "/b", "", nil, "ListObjects", "b", ""},
		{"GET", "/b", "versions", nil, "ListObjectVersions", "b", ""},
		{"GET", "/b", "uploads", nil, "ListMultipartUploads", "b", ""},
		{"GET", "/b", "location", nil, "GetBucketLocation", "b", ""},
		{"PUT", "/b", "", nil, "CreateBucket", "b", ""},
		{"PUT", "/b/", "", nil, "CreateBucket", "b", ""},
		{"HEAD", "/b", "", nil, "HeadBucket", "b", ""},
		{"DELETE", "/b", "", nil, "DeleteBucket", "b", ""},
		{"PUT", "/b", "versioning", nil, "PutBucketVersioning", "b", ""},
		{"GET", "/b", "versioning", nil, "GetBucketVersioning", "b", ""},
		{"PUT", "/b", "notification", nil, "PutBucketNotificationConfiguration", "b", ""},
		{"DELETE", "/b", "cors", nil, "DeleteBucketCors", "b", ""},
		{"PUT", "/b", "object-lock", nil, "PutObjectLockConfiguration", "b", ""},
		{"POST", "/b", "delete", nil, "DeleteObjects", "b", ""},
		{"GET", "/b/k", "", nil, "GetObject", "b", "k"},
		{"GET", "/b/dir/a%20b.txt", "", nil, "GetObject", "b", "dir/a b.txt"},
		{"GET", "/b/k", "X-Amz-Signature=abc&X-Amz-Credential=x", nil, "GetObject", "b", "k"},
		{"HEAD", "/b/k", "", nil, "HeadObject", "b", "k"},
		{"PUT", "/b/k", "", nil, "PutObject", "b", "k"},
		{"PUT", "/b/k", "x-id=PutObject", nil, "PutObject", "b", "k"},
		{"PUT", "/b/k", "", map[string]string{"X-Amz-Copy-Source": "/b/j"}, "CopyObject", "b", "k"},
		{"DELETE", "/b/k", "", nil, "DeleteObject", "b", "k"},
		{"DELETE", "/b/k", "versionId=1", nil, "DeleteObject", "b", "k"},
		{"POST", "/b/k", "uploads", nil, "CreateMultipartUpload", "b", "k"},
		{"PUT", "/b/k", "partNumber=1&uploadId=u", nil, "UploadPart", "b", "k"},
		{"PUT", "/b/k", "partNumber=1&uploadId=u", map[string]string{"X-Amz-Copy-Source": "/b/j"}, "UploadPartCopy", "b", "k"},
		{"POST", "/b/k", "uploadId=u", nil, "CompleteMultipartUpload", "b", "k"},
		{"DELETE", "/b/k", "uploadId=u", nil, "AbortMultipartUpload", "b", "k"},
		{"GET", "/b/k", "uploadId=u", nil, "ListParts", "b", "k"},
		{"GET", "/b/k", "tagging", nil, "GetObjectTagging", "b", "k"},
		{"PUT", "/b/k", "tagging", nil, "PutObjectTagging", "b", "k"},
		{"GET", "/b/k", "attributes", nil, "GetObjectAttributes", "b", "k"},
		{"PUT", "/b/k", "retention", nil, "PutObjectRetention", "b", "k"},
		{"GET", "/b/k", "legal-hold", nil, "GetObjectLegalHold", "b", "k"},
		{"POST", "/b/k", "restore", nil, "RestoreObject", "b", "k"},
		{"PATCH", "/b/k", "", nil, "PATCH object", "b", "k"},
	}
	for _, c := range cases {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{}
		for k, v := range c.header {
			h.Set(k, v)
		}
		op, bucket, key := S3Operation(c.method, c.path, q, h)
		if op != c.op || bucket != c.bucket || key != c.key {
			t.Errorf("%s %s?%s = %s %q %q, want %s %q %q", c.method, c.path, c.query, op, bucket, key, c.op, c.bucket, c.key)
		}
	}
}

func newAWSRequest(t *testing.T, method, target, body string, h map[string]string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return r
}

func TestClassifyAWS(t *testing.T) {
	auth := func(svc string) string {
		return "AWS4-HMAC-SHA256 Credential=test/20261118/eu-west-1/" + svc + "/aws4_request, SignedHeaders=host, Signature=abc"
	}
	cases := []struct {
		name               string
		r                  *http.Request
		protocol, svc, op  string
		region, key        string
		presigned          bool
		bucket, queue, top string
	}{
		{"dynamodb json", newAWSRequest(t, "POST", "http://aws:4566/", "{}", map[string]string{"X-Amz-Target": "DynamoDB_20120810.PutItem", "Authorization": auth("dynamodb")}),
			AWSProtocolJSON, "dynamodb", "PutItem", "eu-west-1", "test", false, "", "", ""},
		{"streams json", newAWSRequest(t, "POST", "http://aws:4566/", "{}", map[string]string{"X-Amz-Target": "DynamoDBStreams_20120810.GetRecords"}),
			AWSProtocolJSON, "dynamodbstreams", "GetRecords", "", "", false, "", "", ""},
		{"sqs json", newAWSRequest(t, "POST", "http://aws:4566/", "{}", map[string]string{"X-Amz-Target": "AmazonSQS.SendMessage", "Authorization": auth("sqs")}),
			AWSProtocolJSON, "sqs", "SendMessage", "eu-west-1", "test", false, "", "", ""},
		{"sns query in url", newAWSRequest(t, "GET", "http://aws:4566/?Action=Publish&Version=2010-03-31&TopicArn=arn:aws:sns:us-east-1:000000000000:events", "", nil),
			AWSProtocolQuery, "sns", "Publish", "", "", false, "", "", "events"},
		{"s3 presigned", newAWSRequest(t, "GET", "http://aws:4566/b/k?X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20261118%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=abc", "", nil),
			AWSProtocolREST, "s3", "GetObject", "us-east-1", "AKIAIOSFODNN7EXAMPLE", true, "b", "", ""},
		{"s3 unsigned", newAWSRequest(t, "GET", "http://aws:4566/", "", nil),
			AWSProtocolREST, "s3", "ListBuckets", "", "", false, "", "", ""},
		{"admin", newAWSRequest(t, "GET", "http://aws:4566/_ministack/health", "", nil),
			AWSProtocolAdmin, "admin", "/_ministack/health", "", "", false, "", "", ""},
	}
	for _, c := range cases {
		cl := classifyAWS(c.r)
		if cl.protocol != c.protocol || cl.service != c.svc || cl.operation != c.op || cl.scope.region != c.region ||
			cl.scope.key != c.key || cl.presigned != c.presigned || cl.resource.Bucket != c.bucket || cl.resource.Topic != c.top {
			t.Errorf("%s: got %+v", c.name, cl)
		}
	}
}

func TestJSONParamsTypesValues(t *testing.T) {
	body := `{"TableName":"orders","Item":{"pk":{"S":"secret-customer"},"n":{"N":"42"},"tags":{"SS":["a","b"]},
		"addr":{"M":{"city":{"S":"Paris"},"zip":{"N":"75001"}}},"list":{"L":[{"S":"x"},{"BOOL":true}]},"api_token":{"S":"tok"}},
		"ConditionExpression":"attribute_not_exists(pk)","ExpressionAttributeValues":{":v":{"S":"value1"}}}`
	m, ok := jsonParams([]byte(body))
	if !ok {
		t.Fatal("body did not parse")
	}
	var res AWSResource
	jsonResource(m, &res)
	if res.Table != "orders" {
		t.Errorf("table %q", res.Table)
	}
	typed, _ := typeValues(m).(map[string]any)
	text, cut, _ := finishParams(typed)
	if cut {
		t.Fatal("cut")
	}
	for _, v := range []string{"secret-customer", "42", "Paris", "75001", "value1", `"tok"`, `"x"`} {
		if strings.Contains(text, v) {
			t.Errorf("params keep value %s: %s", v, text)
		}
	}
	for _, want := range []string{`"pk":"S"`, `"n":"N"`, `"tags":"SS"`, `"city":"S"`, `"L":["S","BOOL"]`, `":v":"S"`, `"api_token":"[redacted]"`, "attribute_not_exists(pk)"} {
		if !strings.Contains(text, want) {
			t.Errorf("params miss %s: %s", want, text)
		}
	}
}

func TestJSONResourceBatchAndTransact(t *testing.T) {
	m, _ := jsonParams([]byte(`{"TransactItems":[{"Put":{"TableName":"b","Item":{}}},{"ConditionCheck":{"TableName":"a","Key":{}}},{"Put":{"TableName":"b"}}]}`))
	var res AWSResource
	jsonResource(m, &res)
	if res.Table != "a,b" {
		t.Errorf("transact tables %q", res.Table)
	}
	m, _ = jsonParams([]byte(`{"QueueUrl":"http://aws:4566/000000000000/events","MessageBody":"hello world","MessageAttributes":{"k":{"DataType":"String","StringValue":"v-secret"}}}`))
	res = AWSResource{}
	jsonResource(m, &res)
	if res.Queue != "events" {
		t.Errorf("queue %q", res.Queue)
	}
	text, _, _ := finishParams(typeValues(m).(map[string]any))
	if strings.Contains(text, "hello") || strings.Contains(text, "v-secret") || !strings.Contains(text, `"MessageBody":"<11 bytes>"`) || !strings.Contains(text, `"StringValue":"<8 bytes>"`) {
		t.Errorf("sqs params %s", text)
	}
	m, _ = jsonParams([]byte(`{"StreamArn":"arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026"}`))
	res = AWSResource{}
	jsonResource(m, &res)
	if res.Table != "orders" || res.Stream == "" {
		t.Errorf("stream resource %+v", res)
	}
}

func TestQueryParamsSizesBodies(t *testing.T) {
	q, _ := url.ParseQuery("Action=Publish&TopicArn=arn:aws:sns:us-east-1:000000000000:t&Message=hello&Subject=s1&MessageAttributes.entry.1.Value.StringValue=abc&password=p")
	text, _, n := finishParams(queryParams(q))
	if strings.Contains(text, "hello") || strings.Contains(text, "abc") || !strings.Contains(text, `"Message":"<5 bytes>"`) || !strings.Contains(text, `"password":"[redacted]"`) || n == 0 {
		t.Errorf("query params %s (%d redacted)", text, n)
	}
}

func TestS3ParamsDropPresign(t *testing.T) {
	q, _ := url.ParseQuery("X-Amz-Signature=abc&X-Amz-Credential=c&X-Amz-Security-Token=tok&versionId=v1&prefix=a")
	h := http.Header{"Range": {"bytes=0-9"}, "Authorization": {"AWS4 secret"}, "Content-Type": {"text/plain"}}
	text, _, _ := finishParams(s3Params(q, h))
	if strings.Contains(text, "abc") || strings.Contains(text, "tok") || strings.Contains(text, "Authorization") ||
		!strings.Contains(text, `"versionId":"v1"`) || !strings.Contains(text, `"Range":"bytes=0-9"`) {
		t.Errorf("s3 params %s", text)
	}
}

func TestFinishParamsCuts(t *testing.T) {
	text, cut, _ := finishParams(map[string]any{"ConditionExpression": strings.Repeat("é", 5000)})
	if !cut || len(text) > MaxAWSParamsBytes || !strings.HasPrefix(text, `{"ConditionExpression":"`) {
		t.Errorf("cut %v len %d", cut, len(text))
	}
	if !utf8Valid(text) {
		t.Error("cut inside a rune")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "?") == s }

func TestAWSErrorCode(t *testing.T) {
	cases := []struct {
		h    map[string]string
		body string
		want string
	}{
		{nil, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"x"}`, "ConditionalCheckFailedException"},
		{nil, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>m</Message></Error>`, "NoSuchKey"},
		{nil, `<ErrorResponse><Error><Type>Sender</Type><Code>AWS.SimpleQueueService.NonExistentQueue</Code></Error></ErrorResponse>`, "AWS.SimpleQueueService.NonExistentQueue"},
		{map[string]string{"X-Amzn-Errortype": "ValidationException:http://internal"}, "", "ValidationException"},
		{map[string]string{"X-Amzn-Query-Error": "AWS.SimpleQueueService.NonExistentQueue;Sender"}, "", "AWS.SimpleQueueService.NonExistentQueue"},
		{nil, `<Error><Code><script></Code></Error>`, ""},
		{nil, `not an error`, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		for k, v := range c.h {
			h.Set(k, v)
		}
		if got := awsErrorCode(h, []byte(c.body)); got != c.want {
			t.Errorf("%v %q = %q, want %q", c.h, c.body, got, c.want)
		}
	}
}

func TestComposeService(t *testing.T) {
	for ptr, want := range map[string]string{
		"harness-r1a2-app-1.harness-r1a2_inner.":     "app",
		"harness-r1a2-worker-12.harness-r1a2_inner":  "worker",
		"harness-r1a2-check_0-1.harness-r1a2_inner.": "check_0",
		"aiman-aws-apply-r1a2.harness-r1a2_inner.":   "aiman-aws-apply-r1a2",
		"somehost.": "somehost",
		"harness-r1a2-check_0-run-0f3a9c.harness-r1a2_inner.": "check_0",
		"harness-r1a2-debug-check-1.harness-r1a2_debug":       "debug-check",
	} {
		if got := composeService(ptr); got != want {
			t.Errorf("%s = %q, want %q", ptr, got, want)
		}
	}
}
