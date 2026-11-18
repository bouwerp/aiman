package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// awsTapScript makes the calls TestAWSDockerTapLogs looks for: a PutObject,
// a presigned GET as signed and with its signature changed, and a
// TransactWriteItems that fails its condition.
const awsTapScript = `
import boto3, re, urllib.request, urllib.error
from botocore.config import Config
s3 = boto3.client("s3", config=Config(signature_version="s3v4"))
s3.create_bucket(Bucket="uploads", CreateBucketConfiguration={"LocationConstraint": "eu-west-1"})
s3.put_object(Bucket="uploads", Key="a/b.txt", Body=b"tap-body-secret")
url = s3.generate_presigned_url("get_object", Params={"Bucket": "uploads", "Key": "a/b.txt"}, ExpiresIn=300)
print("GOOD", urllib.request.urlopen(url).status)
sig = re.search(r"X-Amz-Signature=([0-9a-f]+)", url).group(1)
bad = url.replace(sig, sig[:-1] + ("0" if sig[-1] != "0" else "1"))
try:
    urllib.request.urlopen(bad)
    print("BAD 200")
except urllib.error.HTTPError as e:
    print("BAD", e.code)
ddb = boto3.client("dynamodb")
ddb.create_table(TableName="orders", KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}],
    AttributeDefinitions=[{"AttributeName": "pk", "AttributeType": "S"}], BillingMode="PAY_PER_REQUEST")
ddb.put_item(TableName="orders", Item={"pk": {"S": "item-value-secret"}})
try:
    ddb.transact_write_items(TransactItems=[{"Put": {"TableName": "orders", "Item": {"pk": {"S": "item-value-secret"}},
        "ConditionExpression": "attribute_not_exists(pk)"}}])
    print("TX ok")
except Exception as e:
    print("TX", e.response["Error"]["Code"])
`

// awsCalls reads the tap's call log over the run's control socket.
func (r *dockerRun) awsCalls() []edge.AWSCall {
	r.t.Helper()
	tok, err := os.ReadFile(filepath.Join(r.spec.ControlDir, ControlTokenName))
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := edge.Client{Socket: filepath.Join(r.spec.ControlDir, ControlSocketName), Token: string(tok)}
	var all []edge.AWSCall
	after := int64(0)
	for {
		calls, info, err := c.AWSCalls(ctx, after, edge.MaxAWSCallsLimit)
		if err != nil {
			r.t.Fatalf("aws calls: %v", err)
		}
		all = append(all, calls...)
		if !info.More {
			return all
		}
		after = info.Next
	}
}

// TestAWSDockerTapLogs runs boto3 against MiniStack through the tap and
// finds each call in the call log with its operation, status and error
// code, and no object body or item value.
func TestAWSDockerTapLogs(t *testing.T) {
	needDocker(t)
	r := awsRun(t, &ProxySpec{})
	out, err := r.python(awsTapScript)
	if err != nil || !strings.Contains(out, "GOOD 200") || !strings.Contains(out, "BAD 403") || !strings.Contains(out, "TX TransactionCanceledException") {
		t.Fatalf("client through the tap: %v:\n%s", err, out)
	}
	calls := r.awsCalls()
	defer func() {
		if t.Failed() {
			for _, c := range calls {
				t.Logf("%d %s %s %s %d %q %+v presigned=%v client=%s params=%s", c.ID, c.Service, c.Method, c.Operation, c.Status, c.ErrorCode, c.Resource, c.Presigned, c.Client, c.Params)
			}
		}
	}()
	find := func(op string, presigned bool, status int) *edge.AWSCall {
		for i := range calls {
			c := &calls[i]
			if c.Operation == op && c.Presigned == presigned && c.Status == status {
				return c
			}
		}
		return nil
	}
	put := find("PutObject", false, 200)
	if put == nil || put.Service != "s3" || put.Resource.Bucket != "uploads" || put.Resource.Key != "a/b.txt" ||
		put.RequestBytes == 0 || put.AccessKey != "test" || put.Region != "eu-west-1" || put.Client != "check_0" {
		t.Errorf("PutObject = %+v", put)
	}
	if c := find("GetObject", true, 200); c == nil {
		t.Errorf("presigned GetObject 200 missing")
	}
	if c := find("GetObject", true, 403); c == nil || c.ErrorCode == "" {
		t.Errorf("tampered presigned GetObject = %+v", c)
	}
	tx := find("TransactWriteItems", false, 400)
	if tx == nil || tx.Service != "dynamodb" || tx.ErrorCode != "TransactionCanceledException" || tx.Resource.Table != "orders" ||
		!strings.Contains(tx.Params, `"pk":"S"`) {
		t.Errorf("TransactWriteItems = %+v", tx)
	}
	if c := find("CreateTable", false, 200); c == nil || c.Resource.Table != "orders" {
		t.Errorf("CreateTable = %+v", c)
	}
	for _, c := range calls {
		for _, secret := range []string{"tap-body-secret", "item-value-secret"} {
			if strings.Contains(c.Params, secret) || strings.Contains(c.Error, secret) || strings.Contains(c.Resource.Key, secret) {
				t.Errorf("call %d %s keeps %q: %s", c.ID, c.Operation, secret, c.Params)
			}
		}
	}
}
