package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// fakeMiniStack answers the reads harness.aws makes, as the emulator would
// behind the relay, and records each request.
type fakeMiniStack struct {
	mu   sync.Mutex
	seen []string
}

const fakeObject = "hello é world\nsecond line AKIAIOSFODNN7EXAMPLE\n"

func (f *fakeMiniStack) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		target := r.Header.Get("X-Amz-Target")
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI()+" "+target)
		f.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test/") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		switch {
		case target != "":
			f.ddb(t, w, target, body)
		case r.URL.Path == "/":
			_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets><Bucket><Name>uploads</Name><CreationDate>2026-11-18T00:00:00Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`)
		case r.URL.Path == "/uploads" && q.Has("versioning"):
			_, _ = io.WriteString(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case r.URL.Path == "/uploads" && q.Has("notification"):
			_, _ = io.WriteString(w, `<NotificationConfiguration><QueueConfiguration><Queue>arn:aws:sqs:eu-west-1:000000000000:events</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`)
		case r.URL.Path == "/uploads" && q.Has("versions"):
			_, _ = io.WriteString(w, `<ListVersionsResult><Version><Key>a.txt</Key><VersionId>v2</VersionId><IsLatest>true</IsLatest><LastModified>2026-11-18T02:00:00Z</LastModified><Size>3</Size></Version><Version><Key>a.txt</Key><VersionId>v1</VersionId><IsLatest>false</IsLatest><LastModified>2026-11-18T01:00:00Z</LastModified><Size>2</Size></Version><Version><Key>a.txt.bak</Key><VersionId>x</VersionId></Version></ListVersionsResult>`)
		case r.URL.Path == "/uploads":
			if q.Get("list-type") != "2" || q.Get("delimiter") != "/" || q.Get("prefix") != "in/" {
				http.Error(w, "bad list "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `<ListBucketResult><Contents><Key>in/a.txt</Key><Size>48</Size><ETag>"e1"</ETag><LastModified>2026-11-18T00:00:00Z</LastModified></Contents><CommonPrefixes><Prefix>in/sub/</Prefix></CommonPrefixes><IsTruncated>true</IsTruncated><NextContinuationToken>tok2</NextContinuationToken></ListBucketResult>`)
		case r.URL.Path == "/uploads/missing.txt":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
		case r.URL.Path == "/uploads/in/a.txt" && q.Has("tagging"):
			_, _ = io.WriteString(w, `<Tagging><TagSet><Tag><Key>team</Key><Value>orders</Value></Tag><Tag><Key>api_key</Key><Value>k</Value></Tag></TagSet></Tagging>`)
		case strings.HasPrefix(r.URL.Path, "/uploads/"):
			data := fakeObject
			if r.URL.Path == "/uploads/bin" {
				data = "\x00\x01binary"
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("ETag", `"e1"`)
			w.Header().Set("X-Amz-Meta-Owner", "me")
			w.Header().Set("X-Amz-Meta-Secret-Token", "s3cr3t")
			w.Header().Set("X-Amz-Object-Lock-Mode", "GOVERNANCE")
			if rg := r.Header.Get("Range"); rg != "" && r.Method == http.MethodGet {
				var a, b int
				_, _ = fmt.Sscanf(rg, "bytes=%d-%d", &a, &b)
				b = min(b, len(data)-1)
				w.Header().Set("Content-Length", fmt.Sprint(b-a+1))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, data[a:b+1])
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, data)
			}
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	})
}

func (f *fakeMiniStack) ddb(t *testing.T, w http.ResponseWriter, target string, body []byte) {
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	item := `{"pk":{"S":"o-1"},"sk":{"N":"7"},"note":{"S":"x AKIAIOSFODNN7EXAMPLE"},"password":{"S":"hunter2"},"data":{"B":"AAE="},"tags":{"SS":["a","b"]},"nested":{"M":{"api_key":{"S":"k"},"n":{"L":[{"N":"1.5"},{"BOOL":true},{"NULL":true}]}}}}`
	switch target {
	case "DynamoDB_20120810.ListTables":
		_, _ = io.WriteString(w, `{"TableNames":["orders","users"]}`)
	case "DynamoDB_20120810.DescribeTable":
		_, _ = io.WriteString(w, `{"Table":{"TableName":"orders","TableStatus":"ACTIVE","KeySchema":[{"AttributeName":"pk","KeyType":"HASH"},{"AttributeName":"sk","KeyType":"RANGE"}],
			"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"},{"AttributeName":"sk","AttributeType":"N"},{"AttributeName":"status","AttributeType":"S"}],
			"GlobalSecondaryIndexes":[{"IndexName":"by-status","KeySchema":[{"AttributeName":"status","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}],
			"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"NEW_AND_OLD_IMAGES"},"LatestStreamArn":"arn:aws:dynamodb:eu-west-1:000000000000:table/orders/stream/1",
			"ItemCount":3,"TableSizeBytes":120}}`)
	case "DynamoDB_20120810.DescribeTimeToLive":
		_, _ = io.WriteString(w, `{"TimeToLiveDescription":{"TimeToLiveStatus":"ENABLED","AttributeName":"expires"}}`)
	case "DynamoDB_20120810.Scan":
		_, _ = io.WriteString(w, `{"Items":[`+item+`],"LastEvaluatedKey":{"pk":{"S":"o-1"},"sk":{"N":"7"}}}`)
	case "DynamoDB_20120810.Query":
		want := `{"ExpressionAttributeNames":{"#pk":"pk","#sk":"sk"},"ExpressionAttributeValues":{":pk":{"S":"o-1"},":sk0":{"N":"5"},":sk1":{"N":"9"}},"KeyConditionExpression":"#pk = :pk AND #sk BETWEEN :sk0 AND :sk1","Limit":10,"ScanIndexForward":false,"TableName":"orders"}`
		if string(body) != want {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"__type":"com.amazonaws.dynamodb.v20120810#ValidationException","message":"bad query `+strings.ReplaceAll(string(body), `"`, `'`)+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"Items":[`+item+`]}`)
	case "DynamoDB_20120810.GetItem":
		if k, _ := in["Key"].(map[string]any); fmt.Sprint(k) != "map[pk:map[S:o-1] sk:map[N:7]]" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `{"Item":`+item+`}`)
	case "DynamoDBStreams_20120810.DescribeStream":
		_, _ = io.WriteString(w, `{"StreamDescription":{"Shards":[{"ShardId":"s1"}]}}`)
	case "DynamoDBStreams_20120810.GetShardIterator":
		_, _ = io.WriteString(w, `{"ShardIterator":"it1"}`)
	case "DynamoDBStreams_20120810.GetRecords":
		if in["ShardIterator"] != "it1" {
			_, _ = io.WriteString(w, `{"Records":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"Records":[
			{"eventName":"INSERT","dynamodb":{"SequenceNumber":"100","ApproximateCreationDateTime":1763424000,"Keys":{"pk":{"S":"o-1"}},"NewImage":{"pk":{"S":"o-1"},"token":{"S":"t"}}}},
			{"eventName":"MODIFY","dynamodb":{"SequenceNumber":"200","Keys":{"pk":{"S":"o-1"}},"OldImage":{"pk":{"S":"o-1"}},"NewImage":{"pk":{"S":"o-1"},"status":{"S":"done"}}}}],"NextShardIterator":"it2"}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"__type":"UnknownOperationException"}`)
	}
}

// awsReadSetup starts a ready run with an aws service whose relay is a real
// edge KindAWS relay in front of fakeMiniStack.
func awsReadSetup(t *testing.T, downloadMax int64) (*testManager, Run, *fakeMiniStack) {
	t.Helper()
	emu := &fakeMiniStack{}
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(emu.handler(t))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	loop := netip.MustParseAddr("127.0.0.1")
	e, err := edge.New(edge.Config{Outer: loop, Token: []byte("0123456789abcdef0123456789abcdef"),
		LocalAddrs: func() ([]netip.Addr, error) { return []netip.Addr{loop}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	st, err := e.Add(edge.RelaySpec{Name: "aws", Target: ln.Addr().String(), Kind: edge.KindAWS, Region: "eu-west-1", DownloadMax: downloadMax})
	if err != nil {
		t.Fatal(err)
	}
	tm := newTestManager(t, func(o *Options) {
		o.Images = &recordingVetter{}
		o.AWSHTTP = &http.Client{Transport: redirect{to: st.Listen}}
		o.AWSDownloadMax = downloadMax
	})
	run := tm.upReady(t, testRepo(t, awsManagerFile), "aws")
	if !slices.ContainsFunc(run.Ports, func(p RunPort) bool { return p.Name == "aws" }) {
		t.Fatalf("run publishes no aws relay: %+v", run.Ports)
	}
	return tm, run, emu
}

func TestAWSReadS3(t *testing.T) {
	tm, run, emu := awsReadSetup(t, 1<<20)
	ctx := context.Background()
	read := func(req AWSReadRequest) AWSResult {
		t.Helper()
		req.Run = run.ID
		res, err := tm.AWS(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return res
	}
	b := read(AWSReadRequest{Op: AWSOpS3Buckets})
	if len(b.Buckets) != 1 || b.Buckets[0].Name != "uploads" || b.Buckets[0].Versioning != "Enabled" ||
		!slices.Equal(b.Buckets[0].Notifications, []string{"arn:aws:sqs:eu-west-1:000000000000:events"}) || b.Service != "aws" {
		t.Fatalf("buckets %+v", b)
	}
	o := read(AWSReadRequest{Op: AWSOpS3Objects, Bucket: "uploads", Prefix: "in/", Limit: 10})
	if len(o.Objects) != 1 || o.Objects[0].Key != "in/a.txt" || o.Objects[0].Size != 48 || o.Objects[0].ETag != "e1" ||
		!slices.Equal(o.Prefixes, []string{"in/sub/"}) || o.NextToken != "tok2" {
		t.Fatalf("objects %+v", o)
	}
	v := read(AWSReadRequest{Op: AWSOpS3Versions, Bucket: "uploads", Key: "a.txt"})
	if len(v.Versions) != 2 || v.Versions[0].Version != "v2" || !v.Versions[0].Latest {
		t.Fatalf("versions %+v", v.Versions)
	}
	p := read(AWSReadRequest{Op: AWSOpS3Object, Bucket: "uploads", Key: "in/a.txt", Limit: 7})
	obj := p.Object
	// "hello é" is 8 bytes; a 7-byte page stops before the split rune.
	if obj == nil || obj.Data != "hello " || obj.Next != 6 || obj.EOF || obj.Size != int64(len(fakeObject)) ||
		obj.Metadata["owner"] != "me" || obj.Metadata["secret-token"] != "[redacted]" || obj.Tags["team"] != "orders" ||
		obj.Tags["api_key"] != "[redacted]" || obj.Lock == nil || obj.Lock.Mode != "GOVERNANCE" || obj.ContentType != "text/plain" {
		t.Fatalf("object %+v", obj)
	}
	rest := read(AWSReadRequest{Op: AWSOpS3Object, Bucket: "uploads", Key: "in/a.txt", Offset: 6})
	if !strings.HasPrefix(rest.Object.Data, "é world") || !rest.Object.EOF || strings.Contains(rest.Object.Data, "AKIAIOSFODNN7EXAMPLE") || rest.Redacted == 0 {
		t.Fatalf("rest %+v", rest.Object)
	}
	bin := read(AWSReadRequest{Op: AWSOpS3Object, Bucket: "uploads", Key: "bin"})
	if !bin.Object.Binary || bin.Object.Data != "" {
		t.Fatalf("binary %+v", bin.Object)
	}
	d := read(AWSReadRequest{Op: AWSOpS3Download, Bucket: "uploads", Key: "in/a.txt", Offset: 0, Limit: 5})
	if raw, _ := base64.StdEncoding.DecodeString(d.Download.Data); string(raw) != "hello" || d.Download.Next != 5 || d.Download.EOF {
		t.Fatalf("download %+v", d.Download)
	}
	all := read(AWSReadRequest{Op: AWSOpS3Download, Bucket: "uploads", Key: "in/a.txt"})
	if raw, _ := base64.StdEncoding.DecodeString(all.Download.Data); string(raw) != fakeObject || !all.Download.EOF {
		t.Fatalf("whole download %+v", all.Download)
	}
	for name, req := range map[string]AWSReadRequest{
		"missing key":     {Op: AWSOpS3Object, Bucket: "uploads", Key: "missing.txt"},
		"bad bucket":      {Op: AWSOpS3Objects, Bucket: "_ministack"},
		"no key":          {Op: AWSOpS3Object, Bucket: "uploads"},
		"limit":           {Op: AWSOpS3Objects, Bucket: "uploads", Limit: 1001},
		"page":            {Op: AWSOpS3Download, Bucket: "uploads", Key: "k", Limit: MaxS3DownloadPage + 1},
		"past cap":        {Op: AWSOpS3Download, Bucket: "uploads", Key: "k", Offset: 1 << 20},
		"unknown op":      {Op: "s3_put"},
		"unknown service": {Op: AWSOpS3Buckets, Service: "db"},
	} {
		req.Run = run.ID
		if _, err := tm.AWS(ctx, req); !errors.Is(err, ErrParams) {
			t.Errorf("%s: %v", name, err)
		}
	}
	emu.mu.Lock()
	defer emu.mu.Unlock()
	for _, s := range emu.seen {
		if !strings.HasPrefix(s, "GET ") && !strings.HasPrefix(s, "HEAD ") {
			t.Fatalf("the emulator saw %q", s)
		}
	}
}

func TestAWSReadDynamoDB(t *testing.T) {
	tm, run, emu := awsReadSetup(t, 0)
	ctx := context.Background()
	read := func(req AWSReadRequest) AWSResult {
		t.Helper()
		req.Run = run.ID
		res, err := tm.AWS(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return res
	}
	if r := read(AWSReadRequest{Op: AWSOpDDBTables}); !slices.Equal(r.Tables, []string{"orders", "users"}) {
		t.Fatalf("tables %+v", r.Tables)
	}
	d := read(AWSReadRequest{Op: AWSOpDDBDescribe, Table: "orders"}).Table
	if d == nil || len(d.KeySchema) != 2 || d.Attributes["sk"] != "N" || len(d.GSIs) != 1 || d.GSIs[0].Name != "by-status" ||
		d.StreamView != "NEW_AND_OLD_IMAGES" || d.TTL != "expires" || d.ItemCount != 3 {
		t.Fatalf("describe %+v", d)
	}
	s := read(AWSReadRequest{Op: AWSOpDDBScan, Table: "orders", Limit: 5})
	if len(s.Items) != 1 || s.LastKey == "" {
		t.Fatalf("scan %+v", s)
	}
	item := s.Items[0]
	enc, _ := json.Marshal(item)
	want := `{"data":{"$b64":"AAE="},"nested":{"api_key":"[redacted]","n":[1.5,true,null]},"note":"x [redacted]","password":"[redacted]","pk":"o-1","sk":7,"tags":{"$ss":["a","b"]}}`
	if string(enc) != want {
		t.Fatalf("item\n got %s\nwant %s", enc, want)
	}
	if !slices.Contains(s.RedactedAttributes, "password") || !slices.Contains(s.RedactedAttributes, "api_key") {
		t.Fatalf("redacted attributes %v", s.RedactedAttributes)
	}
	// The paging token round-trips into ExclusiveStartKey.
	read(AWSReadRequest{Op: AWSOpDDBScan, Table: "orders", Start: s.LastKey})
	fwd := false
	q := read(AWSReadRequest{Op: AWSOpDDBQuery, Table: "orders", Limit: 10, Forward: &fwd,
		PK: &AWSKeyValue{Name: "pk", Value: "o-1"}, SK: &AWSSortKey{Op: "between", Values: []any{json.Number("5"), "9"}}})
	if len(q.Items) != 1 {
		t.Fatalf("query %+v", q)
	}
	g := read(AWSReadRequest{Op: AWSOpDDBGet, Table: "orders", ItemKey: map[string]any{"pk": "o-1", "sk": json.Number("7")}})
	if g.Found == nil || !*g.Found || g.Item["pk"] != "o-1" {
		t.Fatalf("get %+v", g)
	}
	miss := read(AWSReadRequest{Op: AWSOpDDBGet, Table: "orders", ItemKey: map[string]any{"pk": "o-2", "sk": json.Number("7")}})
	if miss.Found == nil || *miss.Found || miss.Item != nil {
		t.Fatalf("miss %+v", miss)
	}
	st := read(AWSReadRequest{Op: AWSOpDDBStream, Table: "orders", Limit: 1})
	if len(st.Records) != 1 || st.Records[0].Event != "MODIFY" || !st.Truncated || st.Records[0].New["status"] != "done" {
		t.Fatalf("stream %+v", st)
	}
	for name, req := range map[string]AWSReadRequest{
		"query no pk":    {Op: AWSOpDDBQuery, Table: "orders"},
		"query wrong pk": {Op: AWSOpDDBQuery, Table: "orders", PK: &AWSKeyValue{Name: "sk", Value: "x"}},
		"query pk type":  {Op: AWSOpDDBQuery, Table: "orders", PK: &AWSKeyValue{Name: "pk", Value: json.Number("1")}},
		"query bad op":   {Op: AWSOpDDBQuery, Table: "orders", PK: &AWSKeyValue{Name: "pk", Value: "o"}, SK: &AWSSortKey{Op: "!=", Values: []any{"1"}}},
		"query index":    {Op: AWSOpDDBQuery, Table: "orders", Index: "nope", PK: &AWSKeyValue{Name: "pk", Value: "o"}},
		"emulator 400":   {Op: AWSOpDDBQuery, Table: "orders", PK: &AWSKeyValue{Name: "pk", Value: "o-9"}},
		"get partial":    {Op: AWSOpDDBGet, Table: "orders", ItemKey: map[string]any{"pk": "o-1"}},
		"get extra":      {Op: AWSOpDDBGet, Table: "orders", ItemKey: map[string]any{"pk": "o-1", "sk": json.Number("7"), "x": "y"}},
		"bad start":      {Op: AWSOpDDBScan, Table: "orders", Start: "!!"},
		"limit":          {Op: AWSOpDDBScan, Table: "orders", Limit: 201},
		"no table":       {Op: AWSOpDDBScan},
	} {
		req.Run = run.ID
		if _, err := tm.AWS(ctx, req); !errors.Is(err, ErrParams) {
			t.Errorf("%s: %v", name, err)
		}
	}
	emu.mu.Lock()
	defer emu.mu.Unlock()
	for _, s := range emu.seen {
		target := s[strings.LastIndex(s, " ")+1:]
		if !slices.Contains(edge.DynamoDBReadTargets, target) {
			t.Fatalf("the emulator saw %q", s)
		}
	}
}

func TestAWSReadResources(t *testing.T) {
	tm, run, _ := awsReadSetup(t, 0)
	res, err := tm.AWS(context.Background(), AWSReadRequest{Run: run.ID, Op: AWSOpResources})
	if err != nil || res.Resources != nil || res.Note == "" {
		t.Fatalf("no manifest: %+v %v", res, err)
	}
	path := filepath.Join(tm.runDir(run.Session, run.ID), "sandbox", AWSResourcesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte(`{"resources":[{"stack":"orders"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = tm.AWS(context.Background(), AWSReadRequest{Run: run.ID, Op: AWSOpResources})
	if err != nil || string(res.Resources) != `{"resources":[{"stack":"orders"}]}` {
		t.Fatalf("manifest: %s %v", res.Resources, err)
	}
}

func TestAWSReadNoAWSService(t *testing.T) {
	tm := newTestManager(t)
	run := tm.upReady(t, testRepo(t, testHarnessFile), "plain")
	if _, err := tm.AWS(context.Background(), AWSReadRequest{Run: run.ID, Op: AWSOpS3Buckets}); !errors.Is(err, ErrParams) {
		t.Fatalf("err %v", err)
	}
	if _, err := tm.AWS(context.Background(), AWSReadRequest{Run: "nope", Op: AWSOpS3Buckets}); err == nil {
		t.Fatal("unknown run read")
	}
}
