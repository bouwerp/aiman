package edge

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

// relayEmulator answers like the emulator would, after checking each
// request's signature against the test key; it records what reached it.
type relayEmulator struct {
	mu   sync.Mutex
	seen []string
	bad  []string
	big  int
}

func (f *relayEmulator) log() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...), append([]string(nil), f.bad...)
}

func (f *relayEmulator) start(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entry := r.Method + " " + r.URL.RequestURI() + " " + r.Header.Get("X-Amz-Target") + " " + string(body)
		f.mu.Lock()
		f.seen = append(f.seen, entry)
		if err := verifySig(r, body); err != nil {
			f.bad = append(f.bad, entry+": "+err.Error())
		}
		big := f.big
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/big/obj":
			w.Header().Set("Content-Length", fmt.Sprint(big))
			_, _ = w.Write(make([]byte, big))
		case strings.HasPrefix(r.URL.Path, "/bk1/"):
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("X-Amz-Meta-Owner", "me")
			w.Header().Set("Set-Cookie", "x=y")
			_, _ = w.Write([]byte("hello object"))
		default:
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return ln.Addr().String()
}

// verifySig recomputes the signature as an AUTH=true emulator does.
func verifySig(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=test/") || !strings.Contains(auth, "/eu-west-1/") {
		return fmt.Errorf("authorization %q", auth)
	}
	date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return err
	}
	service := "dynamodb"
	if r.Method != http.MethodPost {
		service = "s3"
	}
	hash := awssig.PayloadHash(body)
	if service == "s3" {
		if r.Header.Get("X-Amz-Content-Sha256") != awssig.EmptyPayload {
			return errors.New("no content hash")
		}
		hash = awssig.EmptyPayload
	}
	_, signed, _ := strings.Cut(auth, "SignedHeaders=")
	signed, _, _ = strings.Cut(signed, ",")
	h := http.Header{}
	for _, name := range strings.Split(signed, ";") {
		if name != "host" {
			h[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
		}
	}
	creq, _ := awssig.CanonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, h, hash)
	scope := date.Format("20060102") + "/eu-west-1/" + service + "/aws4_request"
	key := awssig.SigningKey("test", date.Format("20060102"), "eu-west-1", service)
	mac := hmacHex(key, awssig.StringToSign(date, scope, creq))
	if !strings.HasSuffix(auth, "Signature="+mac) {
		return errors.New("signature mismatch")
	}
	return nil
}

func hmacHex(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func TestFilterAWSRequestAdmits(t *testing.T) {
	for _, c := range []struct {
		method, target, body string
		header               map[string]string
		object               bool
	}{
		{method: "GET", target: "/"},
		{method: "GET", target: "/my-bucket?list-type=2&prefix=a%2Fb&delimiter=%2F&max-keys=5"},
		{method: "GET", target: "/my-bucket?versions&prefix=k"},
		{method: "GET", target: "/my-bucket/dir/key.txt", header: map[string]string{"Range": "bytes=0-9"}, object: true},
		{method: "HEAD", target: "/my-bucket/dir/key.txt?versionId=v1", object: true},
		{method: "GET", target: "/my-bucket/key?tagging"},
		{method: "POST", target: "/", body: `{"TableName":"t","Key":{"pk":{"S":"a"}}}`, header: map[string]string{"X-Amz-Target": "DynamoDB_20120810.GetItem"}},
		{method: "POST", target: "/", body: `{"StreamArn":"arn","Limit":5}`, header: map[string]string{"X-Amz-Target": "DynamoDBStreams_20120810.DescribeStream"}},
	} {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		req.Header.Set("X-Amz-Security-Token", "x")
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE")
		read, err := FilterAWSRequest(req, []byte(c.body))
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.target, err)
		}
		if read.Object != c.object {
			t.Errorf("%s %s: object %v", c.method, c.target, read.Object)
		}
		if read.Header.Get("Authorization") != "" || read.Header.Get("X-Amz-Security-Token") != "" {
			t.Errorf("%s %s kept a client header: %v", c.method, c.target, read.Header)
		}
	}
}

func TestFilterAWSRequestRefuses(t *testing.T) {
	ddb := func(target string) map[string]string { return map[string]string{"X-Amz-Target": target} }
	for name, c := range map[string]struct {
		method, target, body string
		header               map[string]string
		multi                map[string][]string
	}{
		"put object":         {method: "PUT", target: "/bk1/k", body: "data"},
		"delete object":      {method: "DELETE", target: "/bk1/k"},
		"post multi-delete":  {method: "POST", target: "/bk1?delete", body: "<Delete/>"},
		"patch":              {method: "PATCH", target: "/bk1/k"},
		"options":            {method: "OPTIONS", target: "/"},
		"override header":    {method: "GET", target: "/bk1/k", header: map[string]string{"X-HTTP-Method-Override": "PUT"}},
		"override header 2":  {method: "POST", target: "/", header: map[string]string{"X-Method-Override": "DELETE", "X-Amz-Target": "DynamoDB_20120810.GetItem"}, body: `{}`},
		"put item":           {method: "POST", target: "/", body: `{"TableName":"t"}`, header: ddb("DynamoDB_20120810.PutItem")},
		"partiql":            {method: "POST", target: "/", body: `{"Statement":"DELETE FROM t"}`, header: ddb("DynamoDB_20120810.ExecuteStatement")},
		"batch partiql":      {method: "POST", target: "/", body: `{}`, header: ddb("DynamoDB_20120810.BatchExecuteStatement")},
		"transact write":     {method: "POST", target: "/", body: `{}`, header: ddb("DynamoDB_20120810.TransactWriteItems")},
		"target lower case":  {method: "POST", target: "/", body: `{}`, header: ddb("dynamodb_20120810.GetItem")},
		"target two values":  {method: "POST", target: "/", body: `{}`, header: ddb("DynamoDB_20120810.GetItem, DynamoDB_20120810.PutItem")},
		"target suffix":      {method: "POST", target: "/", body: `{}`, header: ddb("DynamoDB_20120810.GetItemX")},
		"target version":     {method: "POST", target: "/", body: `{}`, header: ddb("DynamoDB_20111205.PutItem")},
		"target duplicate":   {method: "POST", target: "/", body: `{}`, multi: map[string][]string{"X-Amz-Target": {"DynamoDB_20120810.GetItem", "DynamoDB_20120810.DeleteItem"}}},
		"no target":          {method: "POST", target: "/", body: `Action=SendMessage&QueueUrl=q`},
		"sqs json":           {method: "POST", target: "/", body: `{"QueueUrl":"q"}`, header: ddb("AmazonSQS.SendMessage")},
		"query action post":  {method: "POST", target: "/?Action=DeleteTable", body: `{}`, header: ddb("DynamoDB_20120810.GetItem")},
		"query action get":   {method: "GET", target: "/?Action=ListQueues"},
		"query action sns":   {method: "GET", target: "/?Action=Publish&TopicArn=x&Message=m"},
		"query x-id":         {method: "GET", target: "/bk1/k?x-id=PutObject"},
		"query acl":          {method: "GET", target: "/bk1?acl"},
		"query dup":          {method: "GET", target: "/bk1?prefix=a&prefix=b"},
		"duplicate json key": {method: "POST", target: "/", body: `{"TableName":"a","TableName":"b"}`, header: ddb("DynamoDB_20120810.Scan")},
		"nested dup key":     {method: "POST", target: "/", body: `{"Key":{"pk":{"S":"a"},"pk":{"S":"b"}}}`, header: ddb("DynamoDB_20120810.GetItem")},
		"json array":         {method: "POST", target: "/", body: `[]`, header: ddb("DynamoDB_20120810.Scan")},
		"json trailing":      {method: "POST", target: "/", body: `{}{}`, header: ddb("DynamoDB_20120810.Scan")},
		"gzip body":          {method: "POST", target: "/", body: `{}`, header: map[string]string{"X-Amz-Target": "DynamoDB_20120810.Scan", "Content-Encoding": "gzip"}},
		"ddb other path":     {method: "POST", target: "/x", body: `{}`, header: ddb("DynamoDB_20120810.Scan")},
		"admin reset":        {method: "POST", target: "/_ministack/reset"},
		"admin config":       {method: "GET", target: "/_ministack/config"},
		"admin health":       {method: "GET", target: "/_ministack/health"},
		"localstack":         {method: "GET", target: "/_localstack/health"},
		"encoded admin":      {method: "GET", target: "/%5Fministack/config"},
		"dot dot":            {method: "GET", target: "/bk1/a/../../_ministack/config"},
		"bad bucket":         {method: "GET", target: "/B_1/k"},
		"presigned":          {method: "GET", target: "/bk1/k?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=00&X-Amz-Credential=test"},
		"get with body":      {method: "GET", target: "/bk1/k", body: "x"},
		"websocket":          {method: "GET", target: "/", header: map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}},
		"range twice":        {method: "GET", target: "/bk1/k", multi: map[string][]string{"Range": {"bytes=0-1", "bytes=2-3"}}},
		"control char key":   {method: "GET", target: "/bk1/a%0Ab"},
	} {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		for k, v := range c.multi {
			req.Header[k] = v
		}
		_, err := FilterAWSRequest(req, []byte(c.body))
		var ref *AWSRefusal
		if !errors.As(err, &ref) {
			t.Errorf("%s: admitted (%v)", name, err)
		}
	}
}

func TestFilterAWSRequestRebuildsJSON(t *testing.T) {
	body := `{ "TableName" : "t", "Limit": 10.50, "ExclusiveStartKey": {"pk": {"N": "1e3"}} }`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.Scan")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Amz-Meta-Extra", "1")
	read, err := FilterAWSRequest(req, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Body) != `{"ExclusiveStartKey":{"pk":{"N":"1e3"}},"Limit":10.50,"TableName":"t"}` {
		t.Fatalf("body %s", read.Body)
	}
	if len(read.Header) != 2 || read.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
		t.Fatalf("headers %v", read.Header)
	}
}

// TestAWSRelayReadOnly drives a real KindAWS relay: reads reach the
// emulator re-signed with the test key, writes and smuggling attempts
// never reach it, and object bodies stop at the download cap.
func TestAWSRelayReadOnly(t *testing.T) {
	emu := &relayEmulator{}
	target := emu.start(t)
	e := newTestEdge(t)
	if _, err := e.Add(RelaySpec{Name: "aws", Target: target, Kind: KindAWS}); err == nil {
		t.Fatal("aws relay without a region was accepted")
	}
	st, err := e.Add(RelaySpec{Name: "aws", Target: target, Kind: KindAWS, Region: "eu-west-1", DownloadMax: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + st.Listen
	do := func(method, path, body string, hdr map[string]string) (int, http.Header, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if body == "" {
			req.Body = nil
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	code, hdr, body := do("GET", "/bk1/a%20b.txt", "", map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/x", "Range": "bytes=0-4"})
	if code != 200 || body != "hello object" || hdr.Get("Etag") != `"abc"` || hdr.Get("X-Amz-Meta-Owner") != "me" || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("get object: %d %v %q", code, hdr, body)
	}
	if code, hdr, _ := do("HEAD", "/bk1/k", "", nil); code != 200 || hdr.Get("Content-Length") != "12" {
		t.Fatalf("head: %d %v", code, hdr)
	}
	if code, _, body := do("POST", "/", `{"TableName":"t"}`, map[string]string{"X-Amz-Target": "DynamoDB_20120810.Scan"}); code != 200 || body != `{"ok":true}` {
		t.Fatalf("scan: %d %s", code, body)
	}
	for _, c := range []struct{ method, path, body, target string }{
		{"PUT", "/bk1/k", "evil", ""},
		{"DELETE", "/bk1/k", "", ""},
		{"POST", "/", `{"TableName":"t","Item":{}}`, "DynamoDB_20120810.PutItem"},
		{"POST", "/", `{"Statement":"UPDATE t SET a=1"}`, "DynamoDB_20120810.ExecuteStatement"},
		{"POST", "/", `{"QueueUrl":"q","MessageBody":"m"}`, "AmazonSQS.SendMessage"},
		{"POST", "/_ministack/reset", "", ""},
		{"GET", "/_ministack/config", "", ""},
	} {
		hdr := map[string]string{}
		if c.target != "" {
			hdr["X-Amz-Target"] = c.target
		}
		if code, _, out := do(c.method, c.path, c.body, hdr); code != http.StatusForbidden || !strings.Contains(out, "refused") {
			t.Fatalf("%s %s %s: %d %s", c.method, c.path, c.target, code, out)
		}
	}
	// A method override header on a GET is refused, not dropped into a GET.
	if code, _, _ := do("GET", "/bk1/k", "", map[string]string{"X-HTTP-Method-Override": "DELETE"}); code != http.StatusForbidden {
		t.Fatalf("override: %d", code)
	}
	// Two X-Amz-Target lines in one raw request.
	raw := "POST / HTTP/1.1\r\nHost: x\r\nX-Amz-Target: DynamoDB_20120810.GetItem\r\nx-amz-target: DynamoDB_20120810.DeleteItem\r\nContent-Length: 2\r\n\r\n{}"
	if code := rawStatus(t, st.Listen, raw); code != http.StatusForbidden {
		t.Fatalf("duplicate target: %d", code)
	}
	// Object over the cap.
	emu.mu.Lock()
	emu.big = 2 << 10
	emu.mu.Unlock()
	if code, _, out := do("GET", "/big/obj", "", nil); code != http.StatusBadGateway || !strings.Contains(out, "download cap") {
		t.Fatalf("big object: %d %s", code, out)
	}
	seen, bad := emu.log()
	if len(bad) > 0 {
		t.Fatalf("bad signatures: %v", bad)
	}
	for _, s := range seen {
		if strings.HasPrefix(s, "PUT") || strings.HasPrefix(s, "DELETE") || strings.Contains(s, "PutItem") || strings.Contains(s, "ExecuteStatement") ||
			strings.Contains(s, "SendMessage") || strings.Contains(s, "_ministack") || strings.Contains(s, "DeleteItem") || strings.Contains(s, "evil") {
			t.Fatalf("the emulator saw %q", s)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("emulator saw %v, want 3 reads and the big object", seen)
	}
	if !strings.HasPrefix(seen[0], "GET /bk1/a%20b.txt ") {
		t.Fatalf("first read %q", seen[0])
	}
}

func rawStatus(t *testing.T, addr, raw string) int {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
