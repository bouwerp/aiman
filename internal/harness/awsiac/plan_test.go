package awsiac

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNamesGenerated(t *testing.T) {
	cases := []struct {
		kind   Kind
		src    Source
		stack  string
		id     string
		prefix string
		fifo   bool
		want   string
	}{
		{KindQueue, SourceCloudFormation, "orders", "UploadQueue", "", false, "orders-UploadQueue-"},
		{KindBucket, SourceCloudFormation, "orders", "UploadBucket", "", false, "orders-uploadbucket-"},
		{KindQueue, SourceTerraform, "shared", "module.q.aws_sqs_queue.dlq", "", false, "shared-module-q-aws_sqs_queue-dlq-"},
		{KindBucket, SourceTerraform, "shared", "aws_s3_bucket.raw_data", "", false, "shared-aws-s3-bucket-raw-data-"},
		{KindQueue, SourceTerraform, "shared", `aws_sqs_queue.q["a b"]`, "", false, "shared-aws_sqs_queue-q-a-b-"},
		{KindTopic, SourceCloudFormation, "orders", "Events", "", true, "orders-Events-"},
		{KindQueue, SourceTerraform, "shared", "aws_sqs_queue.p", "jobs-", false, "jobs-"},
	}
	for _, c := range cases {
		got := GeneratedName(c.kind, c.src, c.stack, c.id, c.prefix, c.fifo)
		suffix := NameSuffix(c.stack, c.id)
		if !strings.HasPrefix(got, c.want) || !strings.Contains(got, suffix) {
			t.Errorf("%s %s: got %q, want prefix %q and suffix %q", c.kind, c.id, got, c.want, suffix)
		}
		if c.fifo != strings.HasSuffix(got, ".fifo") {
			t.Errorf("%s: fifo suffix in %q", c.id, got)
		}
		if err := CheckName(c.kind, got, c.fifo); err != nil {
			t.Errorf("generated %q is not valid: %v", got, err)
		}
		if again := GeneratedName(c.kind, c.src, c.stack, c.id, c.prefix, c.fifo); again != got {
			t.Errorf("unstable: %q then %q", got, again)
		}
	}
	if s := NameSuffix("orders", "UploadQueue"); len(s) != 12 || strings.ToLower(s) != s || strings.ContainsAny(s, "=") {
		t.Fatalf("suffix %q", s)
	}
	if NameSuffix("a", "b") == NameSuffix("b", "a") {
		t.Fatal("suffix ignores the stack")
	}
}

func TestNamesLimits(t *testing.T) {
	long := strings.Repeat("VeryLongLogicalId", 30)
	for _, k := range []Kind{KindBucket, KindTable, KindQueue, KindTopic} {
		for _, fifo := range []bool{false, true} {
			if fifo && (k == KindBucket || k == KindTable) {
				continue
			}
			got := GeneratedName(k, SourceCloudFormation, "stack", long, "", fifo)
			if len(got) > MaxName(k) {
				t.Errorf("%s fifo=%v: %d characters, limit %d", k, fifo, len(got), MaxName(k))
			}
			if !strings.Contains(got, NameSuffix("stack", long)) {
				t.Errorf("%s: the cut took the suffix: %q", k, got)
			}
			if err := CheckName(k, got, fifo); err != nil {
				t.Errorf("%s: %v", k, err)
			}
			pre := GeneratedName(k, SourceTerraform, "stack", "aws_x.y", strings.Repeat("p", 400), fifo)
			if len(pre) > MaxName(k) || !strings.HasSuffix(strings.TrimSuffix(pre, ".fifo"), NameSuffix("stack", "aws_x.y")) {
				t.Errorf("%s prefix: %q", k, pre)
			}
		}
	}
	if len(GeneratedName(KindBucket, SourceCloudFormation, "s", long, "", false)) != MaxBucketName {
		t.Error("a long bucket name is not cut to 63")
	}
	for _, bad := range []struct {
		k    Kind
		name string
		fifo bool
	}{
		{KindBucket, "Upper", false}, {KindBucket, "a..b", false}, {KindTable, "ab", false},
		{KindQueue, "q.fifo", false}, {KindQueue, "q", true}, {KindTopic, "has space", false},
		{KindQueue, strings.Repeat("q", 81), false},
	} {
		if CheckName(bad.k, bad.name, bad.fifo) == nil {
			t.Errorf("%s %q accepted", bad.k, bad.name)
		}
	}
}

func TestPlanOrder(t *testing.T) {
	p := samplePlan(t)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, s := range p.Steps {
		ops = append(ops, s.Resource+" "+s.Operation)
	}
	want := []string{
		"orders.DLQ CreateQueue", "orders.Events CreateQueue", "orders.Topic CreateTopic", "shared.aws_sqs_queue.jobs CreateQueue",
		"orders.Events SetQueueAttributes", "orders.Sub Subscribe",
		"orders.OrdersTable CreateTable", "orders.OrdersTable UpdateTimeToLive",
		"orders.UploadBucket CreateBucket", "orders.UploadBucket PutBucketTagging", "orders.UploadBucket PutBucketVersioning",
		"orders.UploadBucket PutBucketNotificationConfiguration",
	}
	if strings.Join(ops, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(ops, "\n"), strings.Join(want, "\n"))
	}
	for i, s := range p.Steps {
		if s.N != i+1 {
			t.Fatalf("step numbers %+v", p.Steps)
		}
	}
	if len(p.Warnings) != 0 {
		t.Fatalf("warnings %v", p.Warnings)
	}
	q := p.Resource("orders.Events")
	if q.Attrs[AttrURL] != "http://aws:4566/000000000000/"+q.Physical || q.Attrs[AttrARN] != "arn:aws:sqs:eu-west-1:000000000000:"+q.Physical {
		t.Fatalf("queue attrs %v", q.Attrs)
	}
	if j := p.Resource("shared.aws_sqs_queue.jobs"); !strings.HasSuffix(j.Physical, ".fifo") || !j.Generated {
		t.Fatalf("fifo queue %+v", j)
	}
	c, err := p.resourceCall(p.Steps[1], q)
	if err != nil {
		t.Fatal(err)
	}
	var in struct{ Attributes map[string]string }
	if err := json.Unmarshal(c.body, &in); err != nil || !strings.Contains(in.Attributes["RedrivePolicy"], p.Resource("orders.DLQ").Attrs[AttrARN]) {
		t.Fatalf("redrive %s: %v", c.body, err)
	}
}

func TestPlanCycle(t *testing.T) {
	p := New("aws", "us-east-1", "http://aws:4566", []string{"sqs"})
	for _, pair := range [][2]string{{"A", "B"}, {"B", "A"}} {
		_ = p.Add(&Resource{Stack: "s", LogicalID: pair[0], Kind: KindQueue, Queue: &Queue{Attributes: map[string]Value{"RedrivePolicy": AttrOf("s", pair[1], "Arn")}}})
	}
	if err := p.Build(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
}

// TestPlanUnresolved skips a resource whose identity does not resolve and
// drops a property that does not, each with a warning naming stack,
// resource and attribute.
func TestPlanUnresolved(t *testing.T) {
	p := New("aws", "us-east-1", "http://aws:4566", []string{"sqs", "sns", "s3", "dynamodb"})
	add := func(r *Resource) {
		if err := p.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	add(&Resource{Stack: "s", LogicalID: "Q", Kind: KindQueue, Queue: &Queue{Attributes: map[string]Value{
		"RedrivePolicy": AttrOf("s", "Gone", "Arn"), "DelaySeconds": Lit("5"),
	}}})
	add(&Resource{Stack: "s", LogicalID: "Sub", Kind: KindSubscription, Subscription: &Subscription{Topic: RefOf("s", "Gone"), Protocol: "sqs", Endpoint: AttrOf("s", "Q", "Arn")}})
	add(&Resource{Stack: "s", LogicalID: "Mail", Kind: KindSubscription, Subscription: &Subscription{Topic: Lit("x"), Protocol: "email", Endpoint: Lit("a@b")}})
	add(&Resource{Stack: "s", LogicalID: "B", Kind: KindBucket, Name: "Bad_Name", Bucket: &Bucket{}})
	add(&Resource{Stack: "s", LogicalID: "B2", Kind: KindBucket, Bucket: &Bucket{Policy: AttrOf("s", "Q", "NoSuchAttr")}})
	add(&Resource{Stack: "s", LogicalID: "T", Kind: KindTable, Table: &Table{HashKey: "pk"}})
	p.AddOutput(Output{Stack: "s", Name: "Lost", Value: RefOf("s", "B")})
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range p.Warnings {
		got[w.Resource+" "+w.Attribute] = w.String()
	}
	for _, want := range []string{"Q RedrivePolicy", "Sub TopicArn", "Mail Protocol", "B name", "B2 Policy", "T KeySchema", "outputs.Lost "} {
		if got[want] == "" {
			t.Errorf("no warning %q in %v", want, got)
		}
	}
	if !strings.Contains(got["Q RedrivePolicy"], "s.Q RedrivePolicy: s.Gone is not a planned resource") {
		t.Errorf("warning text %q", got["Q RedrivePolicy"])
	}
	if p.Resource("s.Sub") != nil || p.Resource("s.Mail") != nil || p.Resource("s.B") != nil || p.Resource("s.T") != nil {
		t.Fatal("a resource that should be skipped is planned")
	}
	q := p.Resource("s.Q")
	if _, ok := q.Queue.Attributes["RedrivePolicy"]; ok || q.Queue.Attributes["DelaySeconds"] == nil {
		t.Fatalf("queue attributes %v", q.Queue.Attributes)
	}
	if p.Resource("s.B2").Bucket.Policy != nil {
		t.Fatal("policy kept")
	}
}

func TestPlanServiceNotLoaded(t *testing.T) {
	p := New("aws", "us-east-1", "http://aws:4566", []string{"s3"})
	_ = p.Add(&Resource{Stack: "s", LogicalID: "Q", Kind: KindQueue, Queue: &Queue{}})
	if err := p.Build(); err != nil || p.Resource("s.Q") != nil || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "does not load sqs") {
		t.Fatalf("%v %v", err, p.Warnings)
	}
}

func TestPlanAddRefuses(t *testing.T) {
	p := New("aws", "us-east-1", "http://aws:4566", nil)
	for _, r := range []*Resource{
		{LogicalID: "x", Kind: KindQueue, Queue: &Queue{}},
		{Stack: "s", LogicalID: "x", Kind: KindQueue, Topic: &Topic{}},
		{Stack: "s", LogicalID: "x", Kind: KindQueue, Queue: &Queue{}, Topic: &Topic{}},
	} {
		if p.Add(r) == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	_ = p.Add(&Resource{Stack: "s", LogicalID: "x", Kind: KindQueue, Queue: &Queue{}})
	if p.Add(&Resource{Stack: "s", LogicalID: "x", Kind: KindQueue, Queue: &Queue{}}) == nil {
		t.Error("planned twice")
	}
}

func TestEnvRefs(t *testing.T) {
	p := samplePlan(t)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	err := p.BuildEnv(EnvRequest{
		Env: map[string]Ref{
			"ORDERS_TABLE":  {Stack: "orders", LogicalID: "OrdersTable"},
			"ORDERS_STREAM": {Stack: "orders", LogicalID: "OrdersTable", Attr: "StreamArn"},
			"UPLOAD_BUCKET": {Stack: "orders", Output: "UploadBucketName"},
			"JOBS_URL":      {Stack: "shared", LogicalID: "aws_sqs_queue.jobs", Attr: "url"},
			"TABLE":         {Stack: "orders", LogicalID: "OrdersTable", Attr: "Arn"},
		},
		Functions: []Ref{{Stack: "orders", LogicalID: "Fn"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolveEnv(); !errors.Is(err, ErrPending) {
		t.Fatalf("stream before apply: %v", err)
	}
	p.Learn("orders.OrdersTable", AttrStreamARN, "arn:aws:dynamodb:eu-west-1:000000000000:table/orders-test/stream/2026")
	env, err := p.ResolveEnv()
	if err != nil {
		t.Fatal(err)
	}
	q := p.Resource("orders.Events")
	want := map[string]string{
		"ORDERS_TABLE":  "orders-test",
		"ORDERS_STREAM": "arn:aws:dynamodb:eu-west-1:000000000000:table/orders-test/stream/2026",
		"UPLOAD_BUCKET": p.Resource("orders.UploadBucket").Physical,
		"JOBS_URL":      p.Resource("shared.aws_sqs_queue.jobs").Attrs[AttrURL],
		"TABLE":         "arn:aws:dynamodb:eu-west-1:000000000000:table/orders-test", // env wins over the function
		"QUEUE_URL":     q.Attrs[AttrURL],
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["GONE"]; ok {
		t.Error("an unresolved function variable is set")
	}
	var warned, noted bool
	for _, w := range p.Warnings {
		warned = warned || w.Attribute == "Environment.Variables.GONE"
	}
	for _, w := range p.Notes {
		noted = noted || w.Attribute == "Environment.Variables.TABLE"
	}
	if !warned || !noted {
		t.Fatalf("warnings %v notes %v", p.Warnings, p.Notes)
	}
	for _, bad := range []Ref{
		{Stack: "orders", LogicalID: "Missing"},
		{Stack: "orders", LogicalID: "Events", Attr: "StreamArn"},
		{Stack: "orders", LogicalID: "UploadQueue", Attr: "arn"},
		{Stack: "orders", Output: "Nope"},
		{Stack: "orders", LogicalID: "DLQ", Attr: "arn"}, // a Terraform name on a CloudFormation resource
	} {
		if err := p.BuildEnv(EnvRequest{Env: map[string]Ref{"X": bad}}); err == nil {
			t.Errorf("%s resolved", bad)
		}
	}
	if err := p.BuildEnv(EnvRequest{Functions: []Ref{{Stack: "orders", LogicalID: "NoFn"}}}); err == nil {
		t.Error("an unknown function resolved")
	}
}

func TestFunctionEnvDuplicates(t *testing.T) {
	p := New("aws", "us-east-1", "http://aws:4566", []string{"sqs"})
	p.AddFunctionEnv(FunctionEnv{Stack: "s", LogicalID: "A", Vars: []EnvVar{{Name: "X", Value: Lit("1")}, {Name: "S", Value: Lit("secret"), Sensitive: true}}})
	p.AddFunctionEnv(FunctionEnv{Stack: "s", LogicalID: "B", Vars: []EnvVar{{Name: "X", Value: Lit("2")}}})
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	if err := p.BuildEnv(EnvRequest{Functions: []Ref{{Stack: "s", LogicalID: "A"}, {Stack: "s", LogicalID: "B"}}}); err != nil {
		t.Fatal(err)
	}
	env, _ := p.ResolveEnv()
	if env["X"] != "2" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "s.A") {
		t.Fatalf("env %v warnings %v", env, p.Warnings)
	}
	if m := p.Manifest(); len(m.Sensitive) != 1 || m.Sensitive[0] != "S" {
		t.Fatalf("sensitive %v", m.Sensitive)
	}
}

func TestManifest(t *testing.T) {
	p := samplePlan(t)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	if err := p.BuildEnv(EnvRequest{Env: map[string]Ref{"B": {Stack: "orders", Output: "UploadBucketName"}}}); err != nil {
		t.Fatal(err)
	}
	m := p.Manifest()
	if _, ok := m.Outputs["orders"]["StreamArn"]; ok {
		t.Fatal("a pending output is in the manifest before the apply")
	}
	p.Learn("orders.OrdersTable", AttrStreamARN, "arn:aws:dynamodb:eu-west-1:000000000000:table/orders-test/stream/L1")
	p.Learn("orders.Sub", AttrARN, "arn:sub")
	m = p.Manifest()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Manifest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Resources) != len(p.Resources) || back.Outputs["orders"]["StreamArn"] == "" || back.Env["B"] == "" || back.Endpoint != "http://aws:4566" {
		t.Fatalf("manifest %s", b)
	}
	for _, r := range back.Resources {
		if r.LogicalID == "OrdersTable" && (r.Attributes[AttrStreamLabel] != "L1" || r.Generated || r.Physical != "orders-test") {
			t.Fatalf("table %+v", r)
		}
		if r.LogicalID == "Events" && (!r.Generated || r.Attributes[AttrURL] == "") {
			t.Fatalf("queue %+v", r)
		}
	}
}

// TestPlanJSONRoundTrip sends the plan to the applier as JSON, as serve
// does, and builds the same requests on the other side.
func TestPlanJSONRoundTrip(t *testing.T) {
	p := samplePlan(t)
	sampleSeed(t, p)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var q Plan
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	for _, st := range p.Steps {
		if st.Seed != nil {
			continue
		}
		c1, err1 := p.resourceCall(st, p.Resource(st.Resource))
		c2, err2 := q.resourceCall(st, q.Resource(st.Resource))
		if err1 != nil || err2 != nil || !bytes.Equal(c1.body, c2.body) {
			t.Fatalf("%s: %v %v\n%s\n%s", st.Operation, err1, err2, c1.body, c2.body)
		}
	}
	if q.Seed == nil || q.Seed.Messages[0].URL == "" || q.Seed.Items[0].Physical != "orders-test" {
		t.Fatalf("seed %+v", q.Seed)
	}
}

func TestJSONValue(t *testing.T) {
	v, err := JSON(map[string]any{"a": Concat(Lit(`q"`), AttrOf("s", "Q", "Arn")), "n": 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); got != `{"a":"q\"${s.Q.Arn}","n":3}` {
		t.Fatalf("got %s", got)
	}
}

func TestDeclarativeImporter(t *testing.T) {
	fsys := fstest.MapFS{"infra/plan.json": {Data: []byte(`{"resources":[{"logical_id":"Q","kind":"queue","type":"AWS::SQS::Queue","queue":{}}],
		"outputs":[{"name":"QueueUrl","value":[{"ref":{"stack":"x","logical_id":"Q"}}]}]}`)}}
	p := New("aws", "us-east-1", "http://aws:4566", []string{"sqs"})
	if err := DeclarativeImporter(context.Background(), StackSource{Name: "app", Path: "infra/plan.json", Root: fsys}, p); err != nil {
		t.Fatal(err)
	}
	if p.Resource("app.Q") == nil || p.Outputs[0].Stack != "app" {
		t.Fatalf("%+v %+v", p.Resources, p.Outputs)
	}
	bad := fstest.MapFS{"p.json": {Data: []byte(`{"resources":[],"extra":1}`)}}
	if DeclarativeImporter(context.Background(), StackSource{Name: "app", Path: "p.json", Root: bad}, p) == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestErrorOf(t *testing.T) {
	for in, want := range map[string]string{
		`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceInUseException","message":"Table already exists"}`: "ResourceInUseException",
		`<?xml version="1.0"?><Error><Code>BucketAlreadyOwnedByYou</Code><Message>x</Message></Error>`:          "BucketAlreadyOwnedByYou",
		`<ErrorResponse><Error><Code>InvalidParameter</Code><Message>y</Message></Error></ErrorResponse>`:       "InvalidParameter",
	} {
		if code, _ := errorOf([]byte(in)); code != want {
			t.Errorf("%s: %q", in, code)
		}
	}
}

func TestSeedFilesRefused(t *testing.T) {
	dir := seedTree(t)
	if err := os.Symlink("/etc/passwd", dir+"/seed/link.jsonl"); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &Applier{Root: root}
	if err := a.lines("seed/link.jsonl", func(int, []byte) error { return nil }); err == nil {
		t.Fatal("a symlink was read")
	}
	if err := a.lines("../outside", func(int, []byte) error { return nil }); err == nil {
		t.Fatal("a path outside the root was read")
	}
	if err := a.lines("seed/msgs.jsonl", func(int, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
