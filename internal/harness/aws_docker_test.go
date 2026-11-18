package harness

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// dockerAWSFile has a check in the MiniStack image, which ships Python and
// boto3, so the test needs no other image or package download. A check gets
// the app's env and mounts; the test runs it with python3 as entrypoint,
// since MiniStack's own entrypoint starts the emulator.
const dockerAWSFile = `version: 1
app:
  run: exec sleep 600
services:
  aws:
    aws: {kind: ministack, services: [s3, dynamodb], region: eu-west-1}
scenarios:
  - id: aws
    services: [aws]
    checks:
      - {name: boto, run: "true", image: "` + MiniStackImage + `"}
`

// awsClientScript uses boto3 with no endpoint, region or key in code: it
// reads them from the environment and config file the run provides.
const awsClientScript = `
import boto3
s3 = boto3.client("s3")
s3.create_bucket(Bucket="uploads", CreateBucketConfiguration={"LocationConstraint": "eu-west-1"})
s3.put_object(Bucket="uploads", Key="a/b.txt", Body=b"hello")
print("S3", [b["Name"] for b in s3.list_buckets()["Buckets"]], s3.get_object(Bucket="uploads", Key="a/b.txt")["Body"].read().decode())
ddb = boto3.client("dynamodb")
ddb.create_table(TableName="orders", KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}],
    AttributeDefinitions=[{"AttributeName": "pk", "AttributeType": "S"}], BillingMode="PAY_PER_REQUEST")
ddb.put_item(TableName="orders", Item={"pk": {"S": "o1"}, "status": {"S": "received"}})
arn = ddb.describe_table(TableName="orders")["Table"]["TableArn"]
print("DDB", ddb.list_tables()["TableNames"], ddb.get_item(TableName="orders", Key={"pk": {"S": "o1"}})["Item"]["status"]["S"], arn)
`

// realAWSScript asks real S3 through the proxy, as an app that ignores
// AWS_ENDPOINT_URL would.
const realAWSScript = `
import urllib.request, urllib.error
try:
    urllib.request.urlopen("https://s3.amazonaws.com/", timeout=20)
    print("REACHED")
except urllib.error.HTTPError as e:
    print("STATUS", e.code, e.read().decode())
`

func awsRun(t *testing.T, p *ProxySpec) *dockerRun {
	t.Helper()
	ensureImage(t, MiniStackImage)
	return upRun(t, dockerAWSFile, "aws", func(s *RunSpec) {
		s.Proxy = p
		s.CheckImages = map[string]string{CheckService(0): MiniStackImage}
	})
}

// python runs a script in the check container, which has the app's env.
func (r *dockerRun) python(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return r.compose(ctx, "run", "--rm", "--no-deps", "--entrypoint", "python3", CheckService(0), "-c", script)
}

// TestAWSDockerEndpoint starts MiniStack in a run: a client reaches S3 and
// DynamoDB through the edge tap with the fixed test key and nothing in
// code; real AWS is refused by the proxy; the emulator has no route out.
func TestAWSDockerEndpoint(t *testing.T) {
	needDocker(t)
	r := awsRun(t, &ProxySpec{})
	out, err := r.python(awsClientScript)
	if err != nil || !strings.Contains(out, "S3 ['uploads'] hello") ||
		!strings.Contains(out, "DDB ['orders'] received arn:aws:dynamodb:eu-west-1:"+AWSAccountID+":table/orders") {
		t.Fatalf("client through the tap: %v:\n%s", err, out)
	}
	// The name the client uses is the edge's address, not the emulator's.
	out, err = r.python(`import socket;print("ADDR", socket.gethostbyname("aws"), socket.gethostbyname("edge"))`)
	_, addrs, _ := strings.Cut(out, "ADDR")
	if f := strings.Fields(addrs); err != nil || len(f) != 2 || f[0] != f[1] || !r.spec.InnerSubnet.Contains(netip.MustParseAddr(f[0])) {
		t.Fatalf("aws resolves to %q: %v", out, err)
	}
	if out, err := r.exec(ServiceApp, "cat "+AWSMount+"/credentials && touch "+AWSMount+"/x"); err == nil || !strings.Contains(out, "aws_access_key_id = test") {
		t.Fatalf("app reads or writes %s: %v: %s", AWSMount, err, out)
	}
	// A key other than test is refused with signatures on.
	bad := `import boto3;boto3.client("s3",aws_access_key_id="other",aws_secret_access_key="x").list_buckets()`
	if out, err := r.python(bad); err == nil || !strings.Contains(out, "InvalidAccessKeyId") {
		t.Fatalf("another key: %v: %s", err, out)
	}
	if out, err := r.python(realAWSScript); err != nil || !strings.Contains(out, "STATUS 502 "+edge.BlockedAWSMessage) {
		t.Fatalf("real S3 from an offline run: %v: %s", err, out)
	}
	if f := findFlow(r.capture(), "s3.amazonaws.com"); f == nil || f.Outcome != edge.OutcomeRefused {
		t.Fatalf("real S3 flow = %+v", f)
	}
	noRoute := `import urllib.request;urllib.request.urlopen("http://1.1.1.1/",timeout=3)`
	if out, err := r.exec(AWSEmuService("aws"), "python -c '"+noRoute+"'"); err == nil {
		t.Fatalf("the emulator reached the internet: %s", out)
	}
	if out, err := r.exec(AWSEmuService("aws"), "env"); err != nil || strings.Contains(strings.ToLower(out), "proxy") || strings.Contains(out, "AWS_") {
		t.Fatalf("emulator env: %v:\n%s", err, out)
	}
}

// TestAWSDockerOnlineBlocksRealAWS refuses real AWS in an online run too,
// where the proxy would otherwise forward.
func TestAWSDockerOnlineBlocksRealAWS(t *testing.T) {
	needDocker(t)
	if c, err := net.DialTimeout("tcp", "s3.amazonaws.com:443", 5*time.Second); err != nil {
		t.Skipf("no internet from this host: %v", err)
	} else {
		_ = c.Close()
	}
	roots, err := FindUpstreamRoots()
	if err != nil {
		t.Skip(err)
	}
	r := awsRun(t, &ProxySpec{Online: true, UpstreamRoots: roots})
	if out, err := r.python(realAWSScript); err != nil || !strings.Contains(out, "STATUS 502 "+edge.BlockedAWSMessage) {
		t.Fatalf("real S3 from an online run: %v: %s", err, out)
	}
	if f := findFlow(r.capture(), "s3.amazonaws.com"); f == nil || f.Outcome != edge.OutcomeRefused {
		t.Fatalf("real S3 flow = %+v", f)
	}
}
