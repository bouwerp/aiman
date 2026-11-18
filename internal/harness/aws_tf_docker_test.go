package harness

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// awsTFFile is a harness file whose aws service reads a Terraform
// directory: a root module and a local queue module, with a scenario that
// overrides a variable.
const awsTFFile = `version: 1
app:
  run: exec sleep 600
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1
      stacks:
        - name: shared
          terraform: infra/tf
          var_files: [infra/tf/env/test.tfvars]
      env:
        ORDERS_TABLE: shared.aws_dynamodb_table.orders
        ORDERS_STREAM: shared.aws_dynamodb_table.orders.stream_arn
        EVENTS_QUEUE_URL: shared.outputs.events_queue_url
        TOPIC_ARN: shared.aws_sns_topic.events.arn
        UPLOAD_BUCKET: shared.aws_s3_bucket.uploads.bucket
      function_env: [shared.aws_lambda_function.worker]
scenarios:
  - id: tf
    services: [aws]
    steps: [{do: look}]
    aws:
      aws:
        vars: {shared: {env: e2e}}
    checks:
      - {name: boto, run: "true", image: "` + MiniStackImage + `"}
`

const awsTFMain = `variable "env" {
  type    = string
  default = "dev"
}

variable "retention" {
  type = number
}

locals {
  prefix = "orders-${var.env}"
}

resource "aws_dynamodb_table" "orders" {
  name         = "${local.prefix}-orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "status"
    type = "S"
  }

  global_secondary_index {
    name            = "by-status"
    hash_key        = "status"
    projection_type = "ALL"
  }

  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  ttl {
    attribute_name = "expires"
    enabled        = true
  }
}

module "events" {
  source    = "./modules/queue"
  name      = "${local.prefix}-events"
  retention = var.retention
}

resource "aws_sns_topic" "events" {
  name = "${local.prefix}-topic"
}

resource "aws_sns_topic_subscription" "events" {
  topic_arn            = aws_sns_topic.events.arn
  protocol             = "sqs"
  endpoint             = module.events.arn
  raw_message_delivery = true
}

resource "aws_s3_bucket" "uploads" {
  bucket_prefix = "uploads-"
}

resource "aws_s3_bucket_versioning" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_notification" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  queue {
    queue_arn     = module.events.arn
    events        = ["s3:ObjectCreated:*"]
    filter_prefix = "uploads/"
  }
}

resource "aws_lambda_function" "worker" {
  function_name = "worker"
  environment {
    variables = {
      WORKER_TABLE = aws_dynamodb_table.orders.name
      WORKER_ENV   = var.env
    }
  }
}

output "events_queue_url" {
  value = module.events.url
}

output "dlq_url" {
  value = module.events.dlq_url
}
`

const awsTFQueueModule = `variable "name" {
  type = string
}

variable "retention" {
  type = number
}

resource "aws_sqs_queue" "dlq" {
  name = "${var.name}-dlq"
}

resource "aws_sqs_queue" "main" {
  name                      = var.name
  message_retention_seconds = var.retention
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 2
  })
}

resource "aws_sqs_queue_policy" "main" {
  queue_url = aws_sqs_queue.main.id
  policy = jsonencode({
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.main.arn
    }]
  })
}

output "arn" {
  value = aws_sqs_queue.main.arn
}

output "url" {
  value = aws_sqs_queue.main.url
}

output "dlq_url" {
  value = aws_sqs_queue.dlq.url
}
`

func awsTFRepo(t *testing.T) string {
	t.Helper()
	return awsRepo(t, awsTFFile, map[string]string{
		"infra/tf/main.tf":                 awsTFMain,
		"infra/tf/modules/queue/main.tf":   awsTFQueueModule,
		"infra/tf/terraform.tfvars":        "retention = 600\n",
		"infra/tf/env/test.tfvars":         "retention = 1200\n",
		"infra/tf/modules/queue/README.md": "a local module\n",
	})
}

// TestAWSPlanTerraform plans the Terraform stack from the commit without a
// run: the scenario's var override names the resources, the var file wins
// over terraform.tfvars, and function_env reads the Lambda's environment.
func TestAWSPlanTerraform(t *testing.T) {
	repo := awsTFRepo(t)
	tm := newTestManager(t)
	_, plans, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "tf"})
	if err != nil {
		t.Fatal(err)
	}
	v := plans[0]
	if len(v.Warnings) != 0 || len(v.Resources) != 6 {
		t.Fatalf("resources %d warnings %v", len(v.Resources), v.Warnings)
	}
	want := map[string]string{
		"ORDERS_TABLE":     "orders-e2e-orders",
		"EVENTS_QUEUE_URL": "http://aws:4566/000000000000/orders-e2e-events",
		"TOPIC_ARN":        "arn:aws:sns:eu-west-1:000000000000:orders-e2e-topic",
		"WORKER_TABLE":     "orders-e2e-orders",
		"WORKER_ENV":       "e2e",
	}
	for k, w := range want {
		if v.Env[k] != w {
			t.Errorf("env %s = %q, want %q", k, v.Env[k], w)
		}
	}
	if !strings.HasPrefix(v.Env["UPLOAD_BUCKET"], "uploads-") || !strings.HasPrefix(v.Env["ORDERS_STREAM"], "(set when applied") {
		t.Errorf("env %v", v.Env)
	}
	for _, r := range v.Resources {
		if r.LogicalID == "module.events.aws_sqs_queue.main" && !strings.Contains(r.Type, "aws_sqs_queue") {
			t.Errorf("resource %+v", r)
		}
	}
}

// awsTFCheck uses only the env names and the manifest.
const awsTFCheck = `
import boto3, json, os
m = json.load(open(os.environ["AIMAN_AWS_RESOURCES"]))
ddb, s3, sqs, sns = (boto3.client(n) for n in ("dynamodb", "s3", "sqs", "sns"))
t = os.environ["ORDERS_TABLE"]
d = ddb.describe_table(TableName=t)["Table"]
print("TABLE", t, d["LatestStreamArn"] == os.environ["ORDERS_STREAM"], [i["IndexName"] for i in d["GlobalSecondaryIndexes"]], os.environ["WORKER_TABLE"] == t)
print("TTL", ddb.describe_time_to_live(TableName=t)["TimeToLiveDescription"]["AttributeName"])
q = os.environ["EVENTS_QUEUE_URL"]
a = sqs.get_queue_attributes(QueueUrl=q, AttributeNames=["All"])["Attributes"]
print("QUEUE", a["MessageRetentionPeriod"], "orders-e2e-events-dlq" in a["RedrivePolicy"], "sqs:SendMessage" in a.get("Policy", ""))
b = os.environ["UPLOAD_BUCKET"]
print("VERSIONING", s3.get_bucket_versioning(Bucket=b).get("Status"))
sns.publish(TopicArn=os.environ["TOPIC_ARN"], Message="via-topic")
s3.put_object(Bucket=b, Key="uploads/new.txt", Body=b"x")
got = []
for _ in range(10):
    got += [x["Body"] for x in sqs.receive_message(QueueUrl=q, MaxNumberOfMessages=10, WaitTimeSeconds=1).get("Messages", [])]
    if "via-topic" in got and any("uploads/new.txt" in g for g in got):
        break
print("EVENTS", "via-topic" in got, any("uploads/new.txt" in g for g in got))
print("MANIFEST", sorted(r["logical_id"] for r in m["resources"]))
`

// TestAWSDockerTFImport brings a run up from a Terraform directory: a
// table with a GSI, stream and TTL, a queue module with a dead-letter
// queue and a policy, a topic with an SQS subscription and a versioned
// bucket that notifies the queue. An SNS publish and an upload reach the
// queue, and the app finds everything through the env the file maps.
func TestAWSDockerTFImport(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, MiniStackImage)
	repo := awsTFRepo(t)
	m, err := NewManager(Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages: []string{"busybox:*", MiniStackImage},
		Docker:        ExecDocker{},
		AppImage:      func(string, string) (string, error) { return dockerTestImage, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "tf", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	project := "harness-" + run.ID
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", project, "--profile", "*", "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	run = m.Wait(context.Background(), run.ID, 4*time.Minute)
	if run.State != StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	if run.AWS == nil || len(run.AWS.Resources) != 6 || len(run.AWS.Warnings) != 0 {
		t.Fatalf("run.aws %+v", run.AWS)
	}
	sandbox := filepath.Join(m.opts.Dir, "s1", run.ID, "sandbox")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", filepath.Join(sandbox, "compose.yaml"),
		"run", "--rm", "--no-deps", "--entrypoint", "python3", CheckService(0), "-c", awsTFCheck)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("client: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"TABLE orders-e2e-orders True ['by-status'] True", "TTL expires",
		"QUEUE 1200 True True", "VERSIONING Enabled", "EVENTS True True",
		"'module.events.aws_sqs_queue.dlq'", "'aws_sns_topic_subscription.events'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if err := m.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
}
