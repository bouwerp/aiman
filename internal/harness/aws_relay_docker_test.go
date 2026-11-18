package harness

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

// relaySeedScript writes through the tap, as the app would, the data the
// read relay test reads back.
const relaySeedScript = `
import boto3
s3 = boto3.client("s3")
s3.create_bucket(Bucket="uploads", CreateBucketConfiguration={"LocationConstraint": "eu-west-1"})
s3.put_object(Bucket="uploads", Key="in/a b.txt", Body=b"hello relay")
ddb = boto3.client("dynamodb")
ddb.create_table(TableName="orders", KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}],
    AttributeDefinitions=[{"AttributeName": "pk", "AttributeType": "S"}], BillingMode="PAY_PER_REQUEST")
ddb.put_item(TableName="orders", Item={"pk": {"S": "o1"}, "status": {"S": "received"}, "password": {"S": "hunter2"}})
print("SEEDED")
`

// relayCheckScript reports, through the tap, what the relay must not have
// changed.
const relayCheckScript = `
import boto3
s3 = boto3.client("s3")
keys = sorted(o["Key"] for o in s3.list_objects_v2(Bucket="uploads").get("Contents", []))
ddb = boto3.client("dynamodb")
items = ddb.scan(TableName="orders")["Items"]
print("STATE", keys, len(items), sorted(ddb.list_tables()["TableNames"]))
`

// TestAWSDockerRelayReadOnly reads seeded data through the published read
// relay with serve's own client, and proves that writes, PartiQL, SQS and
// the emulator's admin paths sent to the published port never change the
// emulator, signed with the test key or not.
func TestAWSDockerRelayReadOnly(t *testing.T) {
	needDocker(t)
	r := awsRun(t, &ProxySpec{})
	if out, err := r.python(relaySeedScript); err != nil || !strings.Contains(out, "SEEDED") {
		t.Fatalf("seed: %v: %s", err, out)
	}
	var port int
	for _, rel := range r.project.Relays {
		if rel.Kind == RelayKindAWS {
			port = rel.Port
		}
	}
	if port == 0 {
		t.Fatalf("no aws relay in %+v", r.project.Relays)
	}
	addr := r.hostPort(port)
	c := awsClient{base: "http://" + addr, http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}}
	ctx := context.Background()

	var res AWSResult
	if err := c.s3Buckets(ctx, &res); err != nil || len(res.Buckets) != 1 || res.Buckets[0].Name != "uploads" {
		t.Fatalf("buckets %+v: %v", res.Buckets, err)
	}
	res = AWSResult{}
	if err := c.s3Objects(ctx, AWSReadRequest{Bucket: "uploads", Prefix: "in/"}, &res); err != nil || len(res.Objects) != 1 || res.Objects[0].Key != "in/a b.txt" {
		t.Fatalf("objects %+v: %v", res.Objects, err)
	}
	res = AWSResult{}
	if err := c.s3Object(ctx, AWSReadRequest{Bucket: "uploads", Key: "in/a b.txt"}, &res); err != nil || res.Object.Data != "hello relay" || !res.Object.EOF {
		t.Fatalf("object %+v: %v", res.Object, err)
	}
	res = AWSResult{}
	if err := c.s3Download(ctx, AWSReadRequest{Bucket: "uploads", Key: "in/a b.txt", Offset: 6}, 1<<20, &res); err != nil || res.Download.Data != "cmVsYXk=" {
		t.Fatalf("download %+v: %v", res.Download, err)
	}
	res = AWSResult{}
	if err := c.ddbTables(ctx, &res); err != nil || len(res.Tables) != 1 || res.Tables[0] != "orders" {
		t.Fatalf("tables %+v: %v", res.Tables, err)
	}
	res = AWSResult{}
	if err := c.ddbRead(ctx, AWSReadRequest{Op: AWSOpDDBScan, Table: "orders"}, &res); err != nil || len(res.Items) != 1 ||
		res.Items[0]["status"] != "received" || res.Items[0]["password"] != "[redacted]" {
		t.Fatalf("scan %+v: %v", res.Items, err)
	}
	res = AWSResult{}
	if err := c.ddbGet(ctx, AWSReadRequest{Table: "orders", ItemKey: map[string]any{"pk": "o1"}}, &res); err != nil || res.Found == nil || !*res.Found {
		t.Fatalf("get %+v: %v", res, err)
	}
	// Reads see the app's account: the table's ARN names it.
	var desc struct {
		Table struct {
			TableArn string `json:"TableArn"`
		} `json:"Table"`
	}
	if err := c.ddb(ctx, "DynamoDB_20120810.DescribeTable", map[string]any{"TableName": "orders"}, &desc); err != nil ||
		!strings.Contains(desc.Table.TableArn, ":"+AWSAccountID+":table/orders") {
		t.Fatalf("describe arn %q: %v", desc.Table.TableArn, err)
	}

	// Writes through the published port, signed with the test key as an
	// SDK pointed at it would sign them.
	signer := func(service string) awssig.Signer {
		return awssig.Signer{Credentials: awssig.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, Region: "eu-west-1", Service: service, ContentSHA256: service == "s3"}
	}
	send := func(method, path, target, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		service := "s3"
		if target != "" {
			req.Header.Set("X-Amz-Target", target)
			req.Header.Set("Content-Type", "application/x-amz-json-1.0")
			service = "dynamodb"
		}
		_ = signer(service).Sign(req, awssig.PayloadHash([]byte(body)), time.Now())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, w := range []struct{ method, path, target, body string }{
		{"PUT", "/uploads/evil.txt", "", "evil"},
		{"DELETE", "/uploads/in/a%20b.txt", "", ""},
		{"POST", "/uploads?delete", "", `<Delete><Object><Key>in/a b.txt</Key></Object></Delete>`},
		{"PUT", "/evil-bucket", "", ""},
		{"POST", "/", "DynamoDB_20120810.PutItem", `{"TableName":"orders","Item":{"pk":{"S":"evil"}}}`},
		{"POST", "/", "DynamoDB_20120810.DeleteTable", `{"TableName":"orders"}`},
		{"POST", "/", "DynamoDB_20120810.ExecuteStatement", `{"Statement":"INSERT INTO orders VALUE {'pk':'evil'}"}`},
		{"POST", "/", "AmazonSQS.SendMessage", `{"QueueUrl":"q","MessageBody":"m"}`},
		{"POST", "/_ministack/reset", "", ""},
		{"GET", "/_ministack/config", "", ""},
	} {
		if code, out := send(w.method, w.path, w.target, w.body); code != http.StatusForbidden {
			t.Errorf("%s %s %s: %d %s", w.method, w.path, w.target, code, out)
		}
	}
	// The emulator is as seeded.
	out, err := r.python(relayCheckScript)
	if err != nil || !strings.Contains(out, "STATE ['in/a b.txt'] 1 ['orders']") {
		t.Fatalf("state after the writes: %v: %s", err, out)
	}
}
