package edge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeEmulator answers like MiniStack for the few calls the tests send.
func fakeEmulator(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case r.Header.Get("X-Amz-Target") == "DynamoDB_20120810.TransactWriteItems":
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","Message":"Item {pk: secret} failed"}`))
		case r.Header.Get("X-Amz-Target") != "":
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Query().Get("X-Amz-Signature") == "bad":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>SignatureDoesNotMatch</Code></Error>`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/b/"):
			_, _ = w.Write([]byte("object bytes that must not be logged"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startTap(t *testing.T, target string, opts ...AWSTapOption) (*AWSTap, string) {
	t.Helper()
	tap, err := NewAWSTap(target, opts...)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = tap.Serve(ln) }()
	t.Cleanup(func() { _ = tap.Close() })
	return tap, "http://" + ln.Addr().String()
}

func waitCalls(t *testing.T, tap *AWSTap, want int) []AWSCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls, _ := tap.Calls(0, MaxAWSCallsLimit)
		done := 0
		for _, c := range calls {
			if !c.Pending {
				done++
			}
		}
		if done >= want {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d finished calls, have %+v", want, calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sendAWS(t *testing.T, method, u, body string, h map[string]string) int {
	t.Helper()
	r, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "aws:4566"
	for k, v := range h {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func testAuth(svc string) string {
	return "AWS4-HMAC-SHA256 Credential=test/20261118/eu-west-1/" + svc + "/aws4_request, SignedHeaders=host, Signature=abc"
}

// TestAWSTapLogsCalls records service, operation, resource, status and
// error code of S3, DynamoDB, SQS and SNS calls with no body, item value or
// message body in the log.
func TestAWSTapLogsCalls(t *testing.T) {
	emu := fakeEmulator(t)
	resolver := func(_ context.Context, addr string) ([]string, error) {
		if addr == "127.0.0.1" {
			return []string{"harness-r1-app-1.harness-r1_inner."}, nil
		}
		return nil, errors.New("no name")
	}
	tap, u := startTap(t, emu.Listener.Addr().String(), WithAWSTapName("aws"), WithAWSTapResolver(resolver))
	sendAWS(t, "PUT", u+"/b/dir/report.txt", "object body secret-bytes", map[string]string{"Authorization": testAuth("s3"), "Content-Type": "text/plain"})
	sendAWS(t, "GET", u+"/b/dir/report.txt?X-Amz-Credential=test%2F20261118%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=bad", "", nil)
	sendAWS(t, "POST", u+"/", `{"TransactItems":[{"Put":{"TableName":"orders","Item":{"pk":{"S":"cust-123"}},"ConditionExpression":"attribute_not_exists(pk)"}}]}`,
		map[string]string{"X-Amz-Target": "DynamoDB_20120810.TransactWriteItems", "Authorization": testAuth("dynamodb")})
	sendAWS(t, "POST", u+"/", `{"QueueUrl":"http://aws:4566/000000000000/events","MessageBody":"order shipped"}`,
		map[string]string{"X-Amz-Target": "AmazonSQS.SendMessage", "Authorization": strings.Replace(testAuth("sqs"), "test/", "AKIAIOSFODNN7EXAMPLE/", 1)})
	sendAWS(t, "POST", u+"/", "Action=Publish&Version=2010-03-31&TopicArn=arn%3Aaws%3Asns%3Aeu-west-1%3A000000000000%3Aevents&Message=hello+world",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Authorization": testAuth("sns")})
	sendAWS(t, "POST", u+"/_ministack/config", "{}", nil)
	sendAWS(t, "GET", u+"/", "", map[string]string{"User-Agent": "Go-http-client " + AWSApplierAgent, "Authorization": testAuth("s3")})

	calls := waitCalls(t, tap, 7)
	type want struct {
		svc, op string
		status  int
		code    string
		res     AWSResource
	}
	wants := []want{
		{"s3", "PutObject", 200, "", AWSResource{Bucket: "b", Key: "dir/report.txt"}},
		{"s3", "GetObject", 403, "SignatureDoesNotMatch", AWSResource{Bucket: "b", Key: "dir/report.txt"}},
		{"dynamodb", "TransactWriteItems", 400, "TransactionCanceledException", AWSResource{Table: "orders"}},
		{"sqs", "SendMessage", 200, "", AWSResource{Queue: "events"}},
		{"sns", "Publish", 200, "", AWSResource{Topic: "events"}},
		{"admin", "/_ministack/config", 403, CodeAimanRefused, AWSResource{}},
		{"s3", "ListBuckets", 200, "", AWSResource{}},
	}
	for i, w := range wants {
		c := calls[i]
		if c.Service != w.svc || c.Operation != w.op || c.Status != w.status || c.ErrorCode != w.code || c.Resource != w.res {
			t.Errorf("call %d: %s %s %d %q %+v, want %+v", i, c.Service, c.Operation, c.Status, c.ErrorCode, c.Resource, w)
		}
	}
	if calls[0].Client != "app" || calls[6].Client != AWSClientAiman {
		t.Errorf("clients %q %q", calls[0].Client, calls[6].Client)
	}
	if calls[0].RequestBytes != int64(len("object body secret-bytes")) || calls[0].Region != "eu-west-1" || calls[0].AccessKey != "test" {
		t.Errorf("put call %+v", calls[0])
	}
	if !calls[1].Presigned || calls[1].Region != "us-east-1" || calls[1].ResponseBytes == 0 {
		t.Errorf("presigned call %+v", calls[1])
	}
	if !calls[3].ForeignKey || calls[3].AccessKey != "" {
		t.Errorf("foreign key call %+v", calls[3])
	}
	if !strings.Contains(calls[2].Params, `"pk":"S"`) || !strings.Contains(calls[4].Params, `"Message":"<11 bytes>"`) || !strings.Contains(calls[3].Params, `"MessageBody":"<13 bytes>"`) {
		t.Errorf("params: %s | %s | %s", calls[2].Params, calls[3].Params, calls[4].Params)
	}
	for _, c := range calls {
		for _, secret := range []string{"secret-bytes", "object bytes", "cust-123", "order shipped", "hello world", "AKIAIOSFODNN7EXAMPLE", `"bad"`} {
			if strings.Contains(c.Params, secret) || strings.Contains(c.Error, secret) {
				t.Errorf("call %d %s keeps %q: %s", c.ID, c.Operation, secret, c.Params)
			}
		}
	}
}

// TestAWSTapEmulatorDown records a call the emulator never answered.
func TestAWSTapEmulatorDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := ln.Addr().String()
	_ = ln.Close()
	tap, u := startTap(t, target, WithAWSTapResolver(func(context.Context, string) ([]string, error) { return nil, errors.New("none") }))
	if code := sendAWS(t, "GET", u+"/", "", nil); code != http.StatusBadGateway {
		t.Fatalf("status %d", code)
	}
	calls := waitCalls(t, tap, 1)
	if calls[0].Status != http.StatusBadGateway || calls[0].Error == "" {
		t.Fatalf("call %+v", calls[0])
	}
}

// TestAWSTapExpectContinue records the final status of an upload that
// waited for 100 Continue, not the 100.
func TestAWSTapExpectContinue(t *testing.T) {
	emu := fakeEmulator(t)
	tap, u := startTap(t, emu.Listener.Addr().String(), WithAWSTapResolver(func(context.Context, string) ([]string, error) { return nil, errors.New("none") }))
	if code := sendAWS(t, "PUT", u+"/b/k", strings.Repeat("x", 4096), map[string]string{"Expect": "100-continue", "Authorization": testAuth("s3")}); code != 200 {
		t.Fatalf("status %d", code)
	}
	calls := waitCalls(t, tap, 1)
	if calls[0].Status != 200 || calls[0].Operation != "PutObject" || calls[0].RequestBytes != 4096 {
		t.Fatalf("call %+v", calls[0])
	}
}

// TestAWSCallStoreBounds keeps the newest MaxAWSCalls calls, pages reads
// and holds the cursor on a young pending call.
func TestAWSCallStoreBounds(t *testing.T) {
	var s awsCallStore
	now := time.Now()
	for range MaxAWSCalls + 10 {
		s.add(&AWSCall{Start: now})
	}
	calls, info := s.read(0, 0, now)
	if len(calls) != DefaultAWSCallsLimit || info.Total != MaxAWSCalls+10 || info.Dropped != 10 || !info.More || calls[0].ID != 11 {
		t.Fatalf("first page: %d calls, %+v, first %d", len(calls), info, calls[0].ID)
	}
	calls, _ = s.read(0, 10_000, now)
	if len(calls) != MaxAWSCallsLimit {
		t.Fatalf("limit cap: %d", len(calls))
	}
	p := &AWSCall{Start: now, Pending: true}
	s.add(p)
	_, info = s.read(MaxAWSCalls+9, 10, now)
	if info.Next != MaxAWSCalls+10 {
		t.Fatalf("pending hold: next %d", info.Next)
	}
	_, info = s.read(MaxAWSCalls+9, 10, now.Add(2*pendingHold))
	if info.Next != p.ID {
		t.Fatalf("old pending: next %d", info.Next)
	}
}

// TestAWSCallsControlOp reads the call log over the control socket, and
// answers unavailable on an edge with no tap.
func TestAWSCallsControlOp(t *testing.T) {
	e := newTestEdge(t)
	sock := startControl(t, e)
	c := Client{Socket: sock, Token: testToken}
	if _, _, err := c.AWSCalls(context.Background(), 0, 10); err == nil || !strings.Contains(err.Error(), CodeUnavailable) {
		t.Fatalf("no tap: %v", err)
	}
	emu := fakeEmulator(t)
	tap, u := startTap(t, emu.Listener.Addr().String(), WithAWSTapName("aws"),
		WithAWSTapResolver(func(context.Context, string) ([]string, error) { return nil, errors.New("none") }))
	e.SetAWSTap(tap)
	sendAWS(t, "GET", u+"/", "", nil)
	waitCalls(t, tap, 1)
	calls, info, err := c.AWSCalls(context.Background(), 0, 10)
	if err != nil || len(calls) != 1 || info.Service != "aws" || calls[0].Operation != "ListBuckets" || calls[0].Client != "127.0.0.1" {
		t.Fatalf("calls %+v %+v %v", calls, info, err)
	}
}

func TestRedactAWSCall(t *testing.T) {
	c := AWSCall{
		Params:    `{"password":"hunter2","Key":"AKIAIOSFODNN7EXAMPLE"}`,
		Resource:  AWSResource{Key: "keys/AKIAIOSFODNN7EXAMPLE.txt"},
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
	}
	n := RedactAWSCall(&c)
	if n < 3 || strings.Contains(c.Params, "hunter2") || strings.Contains(c.Params+c.Resource.Key, "AKIAIOSFODNN7EXAMPLE") || c.AccessKey != "" || !c.ForeignKey {
		t.Fatalf("redacted %d: %+v", n, c)
	}
	if again := RedactAWSCall(&c); again != 0 {
		t.Fatalf("second pass replaced %d", again)
	}
}
