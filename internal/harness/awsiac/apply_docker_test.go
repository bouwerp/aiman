package awsiac

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// miniStack is the pinned emulator image; harness.MiniStackImage is the
// same pin (this package cannot import harness).
const miniStack = "ministackorg/ministack:1.5.41@sha256:aef90fec1a4581c5fa7b4f79d347898f8475111bac95bbab3736f4a44f5f8573"

// startMiniStack runs the emulator with signatures on and returns its
// endpoint on the host.
func startMiniStack(t *testing.T) string {
	t.Helper()
	if os.Getenv("AIMAN_TEST_DOCKER") != "1" {
		t.Skip("set AIMAN_TEST_DOCKER=1 to run Docker tests")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	name := "aiman-awsiac-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:"+strconv.Itoa(port)+":4566",
		"-e", "SERVICES=s3,dynamodb,dynamodbstreams,sqs,sns", "-e", "AUTH=true", "-e", "MINISTACK_REGION=eu-west-1",
		miniStack).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(endpoint + "/_ministack/health"); err == nil { //nolint:noctx // a test poll
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return endpoint
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("MiniStack did not become healthy")
	return ""
}

// TestApplyDockerMiniStack applies the sample plan and seed against a real
// MiniStack with signatures on, then checks the learned stream ARN and the
// seeded objects, items and messages with signed reads.
func TestApplyDockerMiniStack(t *testing.T) {
	endpoint := startMiniStack(t)
	p := samplePlan(t)
	sampleSeed(t, p)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(seedTree(t))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &Applier{Endpoint: endpoint, Key: "test", Secret: "test", Root: root}
	res, err := a.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("apply: %v\n%+v", err, res)
	}
	if arn := p.Resource("orders.OrdersTable").Attrs[AttrStreamARN]; !strings.Contains(arn, ":table/orders-test/stream/") {
		t.Fatalf("stream arn %q", arn)
	}
	counts := map[string]int{}
	for _, s := range res.Steps {
		counts[s.Operation] += s.Count
	}
	if counts["PutObject"] != 2 || counts["BatchWriteItem"] != 2 || counts["SendMessageBatch"] != 2 {
		t.Fatalf("seed counts %v", counts)
	}
	check := &Applier{Endpoint: endpoint, Key: "test", Secret: "test"}
	check.Client, check.Now = &http.Client{}, time.Now
	var sr StepResult
	get := func(c call) string {
		t.Helper()
		b, err := check.do(context.Background(), p.Region, c, &sr)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		return string(b)
	}
	bucket := p.Resource("orders.UploadBucket").Physical
	if body := get(s3Call("GET", bucket, "seed/sub/b.json", "", nil, nil)); body != `{"x":1}` {
		t.Fatalf("object %q", body)
	}
	if v := get(s3Call("GET", bucket, "", "versioning", nil, nil)); !strings.Contains(v, "Enabled") {
		t.Fatalf("versioning %s", v)
	}
	c, _ := jsonCall("dynamodb", dynamoTarget+"GetItem", map[string]any{"TableName": "orders-test", "Key": map[string]any{"pk": map[string]string{"S": "o1"}, "sk": map[string]string{"S": "a"}}})
	if item := get(c); !strings.Contains(item, `"SS"`) || !strings.Contains(item, `"status": {"S": "new"}`) && !strings.Contains(item, `"status":{"S":"new"}`) {
		t.Fatalf("item %s", item)
	}
	c, _ = jsonCall("dynamodb", dynamoTarget+"DescribeTimeToLive", map[string]any{"TableName": "orders-test"})
	if ttl := get(c); !strings.Contains(ttl, "expires") {
		t.Fatalf("ttl %s", ttl)
	}
	c, _ = jsonCall("sqs", sqsTarget+"ReceiveMessage", map[string]any{"QueueUrl": endpoint + "/000000000000/" + p.Resource("orders.Events").Physical, "MaxNumberOfMessages": 10})
	if msgs := get(c); !strings.Contains(msgs, "plain body") || !strings.Contains(msgs, "structured") {
		t.Fatalf("messages %s", msgs)
	}
}
