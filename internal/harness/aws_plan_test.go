package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
	"gopkg.in/yaml.v3"
)

// awsPlanFile is a harness file whose aws service has one stack, read with
// DeclarativeImporter (useDeclared) in place of the CloudFormation import.
const awsPlanFile = `version: 1
app:
  run: exec sleep 600
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1
      stacks:
        - {name: orders, cloudformation: infra/plan.json}
      env:
        ORDERS_TABLE: orders.OrdersTable
        ORDERS_STREAM: orders.OrdersTable.StreamArn
        UPLOAD_BUCKET: orders.outputs.UploadBucketName
        EVENTS_QUEUE_URL: orders.Events.QueueUrl
      function_env: [orders.OrdersFunction]
      seed: .aiman/aws/seed.yaml
scenarios:
  - id: seeded
    services: [aws]
    steps: [{do: look}]
    checks:
      - {name: boto, run: "true", image: "` + MiniStackImage + `"}
  - id: other
    services: [aws]
    aws:
      aws: {seed: .aiman/aws/seed-other.yaml}
`

// awsDeclaredPlan is the stack as Declared JSON: a table with a GSI,
// stream and TTL, a versioned bucket notifying a queue that has a
// dead-letter queue and a policy, a topic with an SQS subscription, an
// output and a function environment.
const awsDeclaredPlan = `{
  "resources": [
    {"logical_id": "DLQ", "type": "AWS::SQS::Queue", "kind": "queue", "queue": {}},
    {"logical_id": "Events", "type": "AWS::SQS::Queue", "kind": "queue", "queue": {
      "attributes": {"RedrivePolicy": [{"lit": "{\"deadLetterTargetArn\":\""}, {"ref": {"stack": "orders", "logical_id": "DLQ", "attr": "Arn"}}, {"lit": "\",\"maxReceiveCount\":3}"}]},
      "policy": [{"lit": "{\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":\"*\",\"Action\":\"sqs:SendMessage\",\"Resource\":\""}, {"ref": {"stack": "orders", "logical_id": "Events", "attr": "Arn"}}, {"lit": "\"}]}"}]}},
    {"logical_id": "Topic", "type": "AWS::SNS::Topic", "kind": "topic", "topic": {}},
    {"logical_id": "Sub", "type": "AWS::SNS::Subscription", "kind": "subscription", "subscription": {
      "topic": [{"ref": {"stack": "orders", "logical_id": "Topic"}}], "protocol": "sqs",
      "endpoint": [{"ref": {"stack": "orders", "logical_id": "Events", "attr": "Arn"}}], "attributes": {"RawMessageDelivery": [{"lit": "true"}]}}},
    {"logical_id": "OrdersTable", "type": "AWS::DynamoDB::Table", "kind": "table", "table": {
      "hash_key": "pk", "attributes": [{"name": "pk", "type": "S"}, {"name": "status", "type": "S"}],
      "gsis": [{"name": "by-status", "hash_key": "status"}], "stream_view_type": "NEW_AND_OLD_IMAGES", "ttl_attribute": "expires"}},
    {"logical_id": "UploadBucket", "type": "AWS::S3::Bucket", "kind": "bucket", "bucket": {
      "versioning": "Enabled",
      "notifications": [{"destination": "queue", "arn": [{"ref": {"stack": "orders", "logical_id": "Events", "attr": "Arn"}}], "events": ["s3:ObjectCreated:*"], "prefix": "uploads/"}]}}
  ],
  "outputs": [{"name": "UploadBucketName", "value": [{"ref": {"stack": "orders", "logical_id": "UploadBucket"}}]}],
  "functions": [{"logical_id": "OrdersFunction", "vars": [
    {"name": "TABLE", "value": [{"ref": {"stack": "orders", "logical_id": "OrdersTable"}}]},
    {"name": "TOPIC_ARN", "value": [{"ref": {"stack": "orders", "logical_id": "Topic"}}]},
    {"name": "AWS_REGION", "value": [{"lit": "us-west-2"}]},
    {"name": "IMPORTED", "value": [{"ref": {"stack": "orders", "logical_id": "NotHere"}}]}
  ]}]
}`

const awsSeedYAML = `objects:
  - {bucket: orders.UploadBucket, from: testdata/s3, prefix: seed/}
items:
  - {table: orders.OrdersTable, file: testdata/items.jsonl}
messages:
  - {queue: orders.DLQ, file: testdata/messages.jsonl}
`

// useDeclared reads cloudformation stacks as Declared JSON for the test.
func useDeclared(t *testing.T) {
	t.Helper()
	t.Cleanup(awsiac.SwapImporter(awsiac.SourceCloudFormation, awsiac.DeclarativeImporter))
}

// awsRepo is a committed repository with the file, the declared stack and
// the seed data.
func awsRepo(t *testing.T, file string, extra map[string]string) string {
	t.Helper()
	dir := testRepo(t, file)
	files := map[string]string{
		"infra/plan.json":            awsDeclaredPlan,
		".aiman/aws/seed.yaml":       awsSeedYAML,
		".aiman/aws/seed-other.yaml": "items: [{table: orders.OrdersTable, file: testdata/items.jsonl}]\n",
		"testdata/s3/a.txt":          "hello",
		"testdata/s3/dir/b.csv":      "x,y\n",
		"testdata/items.jsonl":       `{"pk":"o1","status":"received","n":1}` + "\n" + `{"pk":"o2","status":"new","tags":{"$ss":["a"]}}` + "\n",
		"testdata/messages.jsonl":    "first\n" + `{"body":"second","attributes":{"kind":"seed"}}` + "\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, p, body)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "aws")
	return dir
}

func TestAWSPlanFromFile(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, awsPlanFile, nil)
	tm := newTestManager(t)
	sha, plans, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "seeded"})
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" || len(plans) != 1 {
		t.Fatalf("sha %q plans %+v", sha, plans)
	}
	v := plans[0]
	if v.Service != "aws" || v.Scenario != "seeded" || v.Endpoint != "http://aws:4566" || len(v.Resources) != 6 {
		t.Fatalf("view %+v", v)
	}
	bucket := ""
	for _, r := range v.Resources {
		if r.LogicalID == "UploadBucket" {
			bucket = r.Physical
		}
	}
	if !strings.HasPrefix(bucket, "orders-uploadbucket-") {
		t.Fatalf("bucket %q", bucket)
	}
	if v.Env["UPLOAD_BUCKET"] != bucket || !strings.HasPrefix(v.Env["ORDERS_TABLE"], "orders-OrdersTable-") ||
		!strings.HasPrefix(v.Env["ORDERS_STREAM"], "(set when applied: ") || !strings.HasPrefix(v.Env["EVENTS_QUEUE_URL"], "http://aws:4566/000000000000/orders-Events-") {
		t.Fatalf("env %v", v.Env)
	}
	if v.Env["TOPIC_ARN"] == "" || v.Env["AWS_REGION"] != "" || v.Env["IMPORTED"] != "" {
		t.Fatalf("function env %v", v.Env)
	}
	if len(v.Warnings) != 1 || v.Warnings[0].Attribute != "Environment.Variables.IMPORTED" {
		t.Fatalf("warnings %v", v.Warnings)
	}
	if v.Seed == nil || len(v.Steps) == 0 || v.Steps[len(v.Steps)-1].Operation != "SendMessageBatch" {
		t.Fatalf("steps %+v", v.Steps)
	}
	// Without a scenario every aws service is planned without overrides.
	_, all, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo})
	if err != nil || len(all) != 1 || all[0].Scenario != "" {
		t.Fatalf("all %v %+v", err, all)
	}
	// The scenario's seed replaces the service's.
	_, other, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "other"})
	if err != nil || len(other[0].Seed.Items) != 1 || len(other[0].Seed.Objects) != 0 {
		t.Fatalf("other %v %+v", err, other)
	}
	if entries, _ := os.ReadDir(tm.dir); len(entries) != 0 {
		t.Fatalf("scratch left in %s: %v", tm.dir, entries)
	}
}

func TestAWSPlanRefusals(t *testing.T) {
	useDeclared(t)
	cases := map[string]struct {
		file  string
		extra map[string]string
		want  string
	}{
		"env names a missing resource": {
			file: strings.Replace(awsPlanFile, "orders.OrdersTable.StreamArn", "orders.Gone", 1),
			want: "aws.env ORDERS_STREAM: orders.Gone",
		},
		"env names a missing attribute": {
			file: strings.Replace(awsPlanFile, "orders.Events.QueueUrl", "orders.Events.StreamArn", 1),
			want: `no attribute "StreamArn"`,
		},
		"env names a missing output": {
			file: strings.Replace(awsPlanFile, "orders.outputs.UploadBucketName", "orders.outputs.Nope", 1),
			want: "has no output Nope",
		},
		"function_env names no function": {
			file: strings.Replace(awsPlanFile, "orders.OrdersFunction", "orders.Other", 1),
			want: "no Lambda or SAM function Other",
		},
		"seed names a missing resource": {
			file:  awsPlanFile,
			extra: map[string]string{".aiman/aws/seed.yaml": "items: [{table: orders.Nope, file: testdata/items.jsonl}]\n"},
			want:  "orders.Nope",
		},
		"seed file missing": {
			file:  awsPlanFile,
			extra: map[string]string{".aiman/aws/seed.yaml": "items: [{table: orders.OrdersTable, file: testdata/none.jsonl}]\n"},
			want:  "testdata/none.jsonl",
		},
		"stack file missing": {
			file: strings.Replace(awsPlanFile, "infra/plan.json", "infra/none.json", 1),
			want: "aws.stacks[0] orders",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			repo := awsRepo(t, c.file, c.extra)
			tm := newTestManager(t)
			_, _, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "seeded"})
			var he *Error
			if !errors.As(err, &he) || he.Code != CodeInvalid || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %s with %q", err, CodeInvalid, c.want)
			}
		})
	}
}

// TestAWSPlanNoImporter plans a stack whose format has no importer with a
// warning, never an error.
func TestAWSPlanNoImporter(t *testing.T) {
	t.Cleanup(awsiac.SwapImporter(awsiac.SourceTerraform, nil))
	file := strings.NewReplacer(
		"{name: orders, cloudformation: infra/plan.json}", "{name: orders, terraform: infra}",
		"      env:\n        ORDERS_TABLE: orders.OrdersTable\n        ORDERS_STREAM: orders.OrdersTable.StreamArn\n        UPLOAD_BUCKET: orders.outputs.UploadBucketName\n        EVENTS_QUEUE_URL: orders.Events.QueueUrl\n      function_env: [orders.OrdersFunction]\n      seed: .aiman/aws/seed.yaml\n", "",
	).Replace(awsPlanFile)
	repo := awsRepo(t, strings.Replace(file, "    aws:\n      aws: {seed: .aiman/aws/seed-other.yaml}\n", "", 1), nil)
	tm := newTestManager(t)
	_, plans, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "seeded"})
	if err != nil || len(plans[0].Resources) != 0 || len(plans[0].Warnings) != 1 || !strings.Contains(plans[0].Warnings[0].Message, "cannot import terraform yet") {
		t.Fatalf("%v %+v", err, plans)
	}
}

// TestAWSUpAppliesPlan runs the start with a fake applier: the plan goes on
// stdin, the learned stream ARN reaches the manifest and the app's env, and
// run.json names the resources.
func TestAWSUpAppliesPlan(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, awsPlanFile, nil)
	tm := newTestManager(t, func(o *Options) { o.Images = &recordingVetter{} })
	stream := "arn:aws:dynamodb:eu-west-1:000000000000:table/x/stream/2026-11-18T00:00:00.000"
	tm.docker.answer = func(args []string) (string, int, bool) {
		if len(args) > 4 && args[0] == "run" && args[len(args)-1] == AWSApplyService("aws") {
			b, _ := json.Marshal(awsApplyReply{Result: &awsiac.ApplyResult{Learned: map[string]map[string]string{"orders.OrdersTable": {awsiac.AttrStreamARN: stream}}}})
			return "Creating ...\n" + AWSApplyMarker + string(b) + "\n", 0, true
		}
		return "", 0, false
	}
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "seeded", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if run.AWS == nil || len(run.AWS.Resources) != 6 {
		t.Fatalf("run.aws at up %+v", run.AWS)
	}
	run = tm.Wait(context.Background(), run.ID, 10*time.Second)
	if run.State != StateReady {
		t.Fatalf("%s %s %s", run.State, run.FailedStep, run.Error)
	}
	if tm.docker.called("run --rm -T --no-deps aws-emu-apply") != 1 {
		t.Fatalf("calls %v", tm.docker.calls)
	}
	var sent awsiac.Plan
	if err := json.Unmarshal([]byte(tm.docker.stdin[len(tm.docker.stdin)-1]), &sent); err != nil || len(sent.Steps) == 0 || sent.Seed == nil {
		t.Fatalf("stdin plan %v %+v", err, sent)
	}
	sandbox := filepath.Join(tm.dir, "s1", run.ID, "sandbox")
	b, err := os.ReadFile(filepath.Join(sandbox, AWSResourcesFile))
	if err != nil {
		t.Fatal(err)
	}
	var m awsiac.Manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Env["ORDERS_STREAM"] != stream || m.Outputs["orders"]["UploadBucketName"] == "" {
		t.Fatalf("manifest %v %s", err, b)
	}
	compose, err := os.ReadFile(filepath.Join(sandbox, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "ORDERS_STREAM: "+stream) || !strings.Contains(string(compose), "AIMAN_AWS_RESOURCES: /aiman-aws/resources.json") {
		t.Fatalf("compose has no plan env:\n%s", compose)
	}
	if !slices.Contains(tm.rec.kinds(), domain.EventHarnessAWSApplied) {
		t.Fatal("no harness.aws_applied row")
	}
	// The aws step runs after the services and before the app.
	order := strings.Join(tm.docker.calls, "\n")
	if i, j := strings.Index(order, "aws-emu-apply"), strings.Index(order, "up -d app"); i < 0 || j < i || strings.Index(order, "aws-emu") > i {
		t.Fatalf("order:\n%s", order)
	}
}

func TestAWSUpApplyFailure(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, awsPlanFile, nil)
	tm := newTestManager(t, func(o *Options) { o.Images = &recordingVetter{} })
	tm.docker.answer = func(args []string) (string, int, bool) {
		if len(args) > 4 && args[0] == "run" && args[len(args)-1] == AWSApplyService("aws") {
			f := awsiac.StepResult{N: 7, Resource: "orders.OrdersTable", Service: "dynamodb", Operation: "CreateTable", Status: 400, Code: "ValidationException", Message: "bad key"}
			b, _ := json.Marshal(awsApplyReply{Result: &awsiac.ApplyResult{Steps: []awsiac.StepResult{f}, Failed: &f}})
			return AWSApplyMarker + string(b) + "\n", 0, true
		}
		return "", 0, false
	}
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "seeded", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 10*time.Second)
	if run.State != StateFailed || run.FailedStep != "aws 7: orders.OrdersTable CreateTable" || !strings.Contains(run.Error, "ValidationException bad key") {
		t.Fatalf("%s %q %q", run.State, run.FailedStep, run.Error)
	}
}

func TestAWSUpRefusesBadReference(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, strings.Replace(awsPlanFile, "orders.OrdersTable.StreamArn", "orders.Gone", 1), nil)
	tm := newTestManager(t, func(o *Options) { o.Images = &recordingVetter{} })
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "seeded", Actor: domain.EventActorUser})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("err %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(tm.dir, "s1")); len(entries) != 0 {
		t.Fatalf("run dir left: %v", entries)
	}
}

func TestAWSComposeApplier(t *testing.T) {
	f, err := Parse([]byte(awsPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	spec := awsSpec()
	spec.Scenario = "seeded"
	spec.CheckImages = map[string]string{CheckService(0): MiniStackImage}
	spec.AWSAppEnv = map[string]string{"ORDERS_TABLE": "t1", "AWS_REGION": "nope"}
	p, err := Compose(f, spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(p.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	ap := svcOf(t, doc, AWSApplyService("aws"))
	if ap["image"] != spec.EdgeImage || ap["read_only"] != true || ap["user"] != fmt.Sprintf("%d:%d", spec.UID, spec.GID) ||
		!slices.Equal(strs(ap["profiles"]), []string{profileAWSApply}) || !slices.Contains(strs(ap["volumes"]), spec.Src+":/src:ro") ||
		ap["environment"] != nil || !slices.Equal(strs(ap["networks"]), []string{NetworkInner}) || !slices.Equal(strs(ap["cap_drop"]), []string{"ALL"}) {
		t.Fatalf("applier %v", ap)
	}
	env := func(svc string) map[string]any {
		m, _ := svcOf(t, doc, svc)["environment"].(map[string]any)
		return m
	}
	if app := env(ServiceApp); app["ORDERS_TABLE"] != "t1" || app["AWS_REGION"] != "eu-west-1" || app["AIMAN_AWS_RESOURCES"] != AWSMount+"/resources.json" {
		t.Fatalf("app env %v", app)
	}
	if env(CheckService(0))["ORDERS_TABLE"] != "t1" {
		t.Fatal("the check lacks the plan env")
	}
	if _, err := Parse([]byte(strings.Replace(awsPlanFile, "services:\n  aws:", "services:\n  x-emu-apply:\n    image: postgres:16\n  aws:", 1))); err == nil {
		t.Fatal("a service named like an applier was accepted")
	}
}
