package awsiac

import (
	"os"
	"path/filepath"
	"testing"
)

// samplePlan is a plan built by hand, as an importer would: a FIFO queue
// with a dead-letter queue, a topic with a policy and an SQS subscription,
// a table with a GSI, an LSI, a stream and TTL, and a versioned bucket that
// notifies the queue.
func samplePlan(t *testing.T) *Plan {
	t.Helper()
	p := New("aws", "eu-west-1", "http://aws:4566", []string{"s3", "dynamodb", "sqs", "sns"})
	pol, err := JSON(map[string]any{"Statement": []any{map[string]any{
		"Effect": "Allow", "Principal": "*", "Action": "sqs:SendMessage", "Resource": AttrOf("orders", "Events", "Arn"),
		"Condition": map[string]any{"ArnLike": map[string]any{"aws:SourceArn": []any{RefOf("orders", "Topic"), Lit("arn:aws:s3:::*")}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	redrive, err := JSON(map[string]any{"deadLetterTargetArn": AttrOf("orders", "DLQ", "Arn"), "maxReceiveCount": 3})
	if err != nil {
		t.Fatal(err)
	}
	add := func(r *Resource) {
		t.Helper()
		if err := p.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	add(&Resource{Stack: "orders", LogicalID: "Events", Type: "AWS::SQS::Queue", Kind: KindQueue, Queue: &Queue{
		Attributes: map[string]Value{"RedrivePolicy": redrive, "VisibilityTimeout": Lit("30")}, Policy: pol,
	}})
	add(&Resource{Stack: "orders", LogicalID: "DLQ", Type: "AWS::SQS::Queue", Kind: KindQueue, Queue: &Queue{}})
	add(&Resource{Stack: "orders", LogicalID: "Topic", Type: "AWS::SNS::Topic", Kind: KindTopic, Topic: &Topic{Tags: map[string]string{"team": "a"}}})
	add(&Resource{Stack: "orders", LogicalID: "Sub", Type: "AWS::SNS::Subscription", Kind: KindSubscription, Subscription: &Subscription{
		Topic: RefOf("orders", "Topic"), Protocol: "sqs", Endpoint: AttrOf("orders", "Events", "Arn"),
		Attributes: map[string]Value{"RawMessageDelivery": Lit("true")},
	}})
	add(&Resource{Stack: "orders", LogicalID: "OrdersTable", Type: "AWS::DynamoDB::Table", Kind: KindTable, Name: "orders-test", Table: &Table{
		HashKey: "pk", RangeKey: "sk",
		Attributes:     []AttributeDef{{"pk", "S"}, {"sk", "S"}, {"status", "S"}, {"at", "N"}},
		GSIs:           []Index{{Name: "by-status", HashKey: "status"}},
		LSIs:           []Index{{Name: "by-at", RangeKey: "at", Projection: "KEYS_ONLY"}},
		StreamViewType: "NEW_AND_OLD_IMAGES", TTLAttribute: "expires",
	}})
	add(&Resource{Stack: "orders", LogicalID: "UploadBucket", Type: "AWS::S3::Bucket", Kind: KindBucket, Bucket: &Bucket{
		Versioning:    "Enabled",
		Notifications: []Notification{{Destination: KindQueue, ARN: AttrOf("orders", "Events", "Arn"), Events: []string{"s3:ObjectCreated:*"}, Prefix: "in/"}},
		Tags:          map[string]string{"team": "a"},
	}})
	add(&Resource{Stack: "shared", LogicalID: "aws_sqs_queue.jobs", Type: "aws_sqs_queue", Source: SourceTerraform, Kind: KindQueue, Queue: &Queue{Fifo: true}})
	p.AddOutput(Output{Stack: "orders", Name: "UploadBucketName", Value: RefOf("orders", "UploadBucket")})
	p.AddOutput(Output{Stack: "orders", Name: "StreamArn", Value: AttrOf("orders", "OrdersTable", "StreamArn")})
	p.AddFunctionEnv(FunctionEnv{Stack: "orders", LogicalID: "Fn", Vars: []EnvVar{
		{Name: "TABLE", Value: RefOf("orders", "OrdersTable")},
		{Name: "QUEUE_URL", Value: RefOf("orders", "Events")},
		{Name: "GONE", Value: RefOf("orders", "Missing")},
	}})
	return p
}

// seedTree writes a repository tree with seed data and returns its root.
func seedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("seed/s3/a.txt", "hello")
	write("seed/s3/sub/b.json", `{"x":1}`)
	write("seed/items.jsonl", `{"pk":"o1","sk":"a","status":"new","at":1,"tags":{"$ss":["x","y"]},"blob":{"$b64":"aGk="}}
{"pk":"o2","sk":"a","status":"done","at":2,"n":null,"l":[1,"a",true]}
`)
	write("seed/msgs.jsonl", `plain body
{"body":"structured","attributes":{"kind":"test"}}
`)
	return dir
}

func sampleSeed(t *testing.T, p *Plan) {
	t.Helper()
	sf, err := ParseSeed([]byte(`
objects:
  - {bucket: orders.UploadBucket, from: seed/s3, prefix: seed/}
items:
  - {table: orders-test, file: seed/items.jsonl}
messages:
  - {queue: orders.Events, file: seed/msgs.jsonl}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetSeed(".aiman/aws/seed.yaml", sf, testTargets); err != nil {
		t.Fatal(err)
	}
}

// testTargets reads <stack>.<id> as a reference when stack is orders or
// shared.
func testTargets(s string) (Ref, bool, error) {
	for _, st := range []string{"orders.", "shared."} {
		if rest, ok := cutPrefix(s, st); ok {
			return Ref{Stack: st[:len(st)-1], LogicalID: rest}, true, nil
		}
	}
	return Ref{}, false, nil
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}
