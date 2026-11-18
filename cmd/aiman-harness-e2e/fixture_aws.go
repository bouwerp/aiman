//go:build e2e

package main

import (
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// The aws fixture: a MiniStack service planned from a SAM template and a
// Terraform stack with a local module, both read through the real
// importers, a seed file, and a Python app on the stdlib that signs its
// own S3, DynamoDB, SQS and SNS calls, so the run needs no package
// download.
func init() {
	fixtures["aws"] = fixture{
		scenario: "upload",
		images:   []string{harnesstest.PythonImage, harness.MiniStackImage},
		write:    files(awsFixtureFiles),
	}
	allowedImages = append(allowedImages, harness.MiniStackImage)
}

// awsFixtureFiles is the fixture repository, shared with the plan test.
var awsFixtureFiles = map[string]string{
	".aiman/harness.yaml":            awsHarnessFile,
	"infra/sam/template.yaml":        awsSAMTemplate,
	"infra/tf/main.tf":               awsTFMain,
	"infra/tf/modules/queue/main.tf": awsTFQueueModule,
	"infra/tf/terraform.tfvars":      "retention = 600\n",
	"infra/tf/env/test.tfvars":       "retention = 1200\n",
	".aiman/aws/seed.yaml":           awsFixtureSeed,
	".aiman/aws/seed-empty.yaml":     "objects: []\n",
	"testdata/s3/readme.txt":         "hello from the seed\nsecond line\n",
	"testdata/s3/notes/orders.csv":   "id,status\no1,received\no2,new\n",
	"testdata/ddb/orders.jsonl":      `{"pk":"o1","status":"received","total":12.5}` + "\n" + `{"pk":"o2","status":"new","tags":{"$ss":["gift"]},"api_key":"k-123"}` + "\n",
	"testdata/ddb/customers.jsonl":   `{"id":"c1","name":"Ada","password":"hunter2"}` + "\n",
	"testdata/sqs/jobs.jsonl":        `{"job":"reindex"}` + "\n",
	"app.py":                         awsFixtureApp,
}

// awsHarnessFile names both stacks. The upload scenario overrides the SAM
// stage and the Terraform env; the staging scenario keeps the file's
// values and replaces the seed. OrdersFunction's SHARED_VPC reads an
// export no stack provides, so the plan carries one warning.
const awsHarnessFile = `version: 1
app:
  image: ` + harnesstest.PythonImage + `
  run: exec python3 -u /src/app.py
  ports: [{name: web, container: 8080}]
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1
      stacks:
        - {name: orders, cloudformation: infra/sam/template.yaml, parameters: {Stage: test}}
        - name: shared
          terraform: infra/tf
          var_files: [infra/tf/env/test.tfvars]
          vars: {env: test}
      env:
        UPLOAD_BUCKET: orders.outputs.UploadBucketName
        UPLOADS_QUEUE_URL: orders.UploadQueue
        NOTIFY_QUEUE_URL: orders.outputs.NotifyQueueUrl
        CUSTOMERS_TABLE: shared.aws_dynamodb_table.customers
        JOBS_QUEUE_URL: shared.outputs.jobs_queue_url
      function_env: [orders.OrdersFunction]
      seed: .aiman/aws/seed.yaml
scenarios:
  - id: upload
    title: Uploads land in S3 and DynamoDB
    services: [aws]
    aws:
      aws:
        parameters: {orders: {Stage: e2e}}
        vars: {shared: {env: e2e}}
    steps:
      - do: Open the AWS tab
        expect: Resources lists both stacks' tables, bucket, queues and topic with their env variables, and one warning
      - do: Open the web link and press Upload
        expect: Calls shows PutObject and PutItem with 200, and the upload queue holds the bucket's notification
      - do: Query the by-status index for new
        expect: The uploaded order is there, and the stream has its INSERT
  - id: staging
    title: The file's own stage and an empty seed
    services: [aws]
    aws:
      aws:
        seed: .aiman/aws/seed-empty.yaml
    steps:
      - do: Open the AWS tab
        expect: The counters table is orders-test-counters and nothing is seeded
`

// awsSAMTemplate is the orders stack: a table with a GSI and a stream, a
// SimpleTable named from the stage, a bucket that notifies a queue, a
// topic with an SQS subscription, and a function whose environment the
// app takes.
const awsSAMTemplate = `AWSTemplateFormatVersion: "2010-09-09"
Transform: AWS::Serverless-2016-10-31
Parameters:
  Stage:
    Type: String
    Default: dev
    AllowedValues: [dev, test, e2e]
Globals:
  Function:
    Runtime: python3.13
    Environment:
      Variables:
        STAGE: !Ref Stage
Resources:
  OrdersTable:
    Type: AWS::DynamoDB::Table
    Properties:
      BillingMode: PAY_PER_REQUEST
      KeySchema: [{AttributeName: pk, KeyType: HASH}]
      AttributeDefinitions:
        - {AttributeName: pk, AttributeType: S}
        - {AttributeName: status, AttributeType: S}
      GlobalSecondaryIndexes:
        - IndexName: by-status
          KeySchema: [{AttributeName: status, KeyType: HASH}]
          Projection: {ProjectionType: ALL}
      StreamSpecification: {StreamViewType: NEW_AND_OLD_IMAGES}
  CountersTable:
    Type: AWS::Serverless::SimpleTable
    Properties:
      TableName: !Sub "${AWS::StackName}-${Stage}-counters"
  UploadQueue:
    Type: AWS::SQS::Queue
  UploadQueuePolicy:
    Type: AWS::SQS::QueuePolicy
    Properties:
      Queues: [!Ref UploadQueue]
      PolicyDocument:
        Statement:
          - {Effect: Allow, Principal: {Service: s3.amazonaws.com}, Action: sqs:SendMessage, Resource: !GetAtt UploadQueue.Arn}
  UploadBucket:
    Type: AWS::S3::Bucket
    DependsOn: UploadQueuePolicy
    Properties:
      VersioningConfiguration: {Status: Enabled}
      NotificationConfiguration:
        QueueConfigurations:
          - Event: s3:ObjectCreated:*
            Queue: !GetAtt UploadQueue.Arn
            Filter:
              S3Key:
                Rules: [{Name: prefix, Value: uploads/}]
  NotifyQueue:
    Type: AWS::SQS::Queue
  NotifyQueuePolicy:
    Type: AWS::SQS::QueuePolicy
    Properties:
      Queues: [!Ref NotifyQueue]
      PolicyDocument:
        Statement:
          - {Effect: Allow, Principal: {Service: sns.amazonaws.com}, Action: sqs:SendMessage, Resource: !GetAtt NotifyQueue.Arn}
  EventsTopic:
    Type: AWS::SNS::Topic
  EventsSubscription:
    Type: AWS::SNS::Subscription
    Properties:
      TopicArn: !Ref EventsTopic
      Protocol: sqs
      Endpoint: !GetAtt NotifyQueue.Arn
      RawMessageDelivery: true
  OrdersFunction:
    Type: AWS::Serverless::Function
    Properties:
      Handler: app.handler
      CodeUri: src/
      Environment:
        Variables:
          ORDERS_TABLE: !Ref OrdersTable
          COUNTERS_TABLE: !Ref CountersTable
          EVENTS_TOPIC: !Ref EventsTopic
          SHARED_VPC: !ImportValue shared-vpc-id
      Events:
        Uploads:
          Type: SQS
          Properties:
            Queue: !GetAtt UploadQueue.Arn
Outputs:
  UploadBucketName:
    Value: !Ref UploadBucket
  NotifyQueueUrl:
    Value: !Ref NotifyQueue
`

// awsTFMain is the shared stack: a customers table named from var.env and
// a jobs queue with a dead-letter queue from a local module.
const awsTFMain = `variable "env" {
  type    = string
  default = "dev"
}

variable "retention" {
  type = number
}

locals {
  prefix = "shared-${var.env}"
}

resource "aws_dynamodb_table" "customers" {
  name         = "${local.prefix}-customers"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }
}

module "jobs" {
  source    = "./modules/queue"
  name      = "${local.prefix}-jobs"
  retention = var.retention
}

output "jobs_queue_url" {
  value = module.jobs.url
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

output "url" {
  value = aws_sqs_queue.main.url
}
`

const awsFixtureSeed = `objects:
  - {bucket: orders.UploadBucket, from: testdata/s3, prefix: seed/}
items:
  - {table: orders.OrdersTable, file: testdata/ddb/orders.jsonl}
  - {table: shared.aws_dynamodb_table.customers, file: testdata/ddb/customers.jsonl}
messages:
  - {queue: shared.module.jobs.aws_sqs_queue.main, file: testdata/sqs/jobs.jsonl}
`

// awsFixtureApp answers on 8080. /upload puts an object and an item;
// /notified reads the bucket's notification from the upload queue;
// /publish publishes to the topic and reads it from the subscribed queue;
// /wrong-region, /foreign-key and /real-aws make the calls the AWS tab's
// hints name.
const awsFixtureApp = `import datetime, hashlib, hmac, http.server, itertools, json, os, threading, time
import urllib.error, urllib.parse, urllib.request

EP = os.environ["AWS_ENDPOINT_URL"]
REGION = os.environ["AWS_REGION"]
KEY, SECRET = os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"]
BUCKET, TABLE = os.environ["UPLOAD_BUCKET"], os.environ["ORDERS_TABLE"]
COUNT = itertools.count(1)
LOCK = threading.Lock()


def q(s):
    return urllib.parse.quote(s, safe="-_.~")


def call(method, path, service, query=None, headers=None, body=b"", region=REGION, key=KEY, secret=SECRET):
    query, headers = query or {}, dict(headers or {})
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    payload = hashlib.sha256(body).hexdigest()
    h = {k.lower(): v for k, v in headers.items()}
    h.update({"host": urllib.parse.urlsplit(EP).netloc, "x-amz-date": now, "x-amz-content-sha256": payload})
    names = sorted(h)
    cq = "&".join(q(k) + "=" + q(v) for k, v in sorted(query.items()))
    upath = urllib.parse.quote(path, safe="/-_.~")
    creq = "\n".join([method, upath, cq, "".join(n + ":" + h[n].strip() + "\n" for n in names), ";".join(names), payload])
    scope = "/".join([now[:8], region, service, "aws4_request"])
    sts = "\n".join(["AWS4-HMAC-SHA256", now, scope, hashlib.sha256(creq.encode()).hexdigest()])
    k = ("AWS4" + secret).encode()
    for part in (now[:8], region, service, "aws4_request"):
        k = hmac.new(k, part.encode(), hashlib.sha256).digest()
    h["authorization"] = "AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s" % (
        key, scope, ";".join(names), hmac.new(k, sts.encode(), hashlib.sha256).hexdigest())
    del h["host"]
    req = urllib.request.Request(EP + upath + ("?" + cq if cq else ""), data=body if method in ("PUT", "POST") else None, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def ddb(op, doc):
    return call("POST", "/", "dynamodb", headers={"content-type": "application/x-amz-json-1.0",
                "x-amz-target": "DynamoDB_20120810." + op}, body=json.dumps(doc).encode())


def sqs(op, doc):
    return call("POST", "/", "sqs", headers={"content-type": "application/x-amz-json-1.0",
                "x-amz-target": "AmazonSQS." + op}, body=json.dumps(doc).encode())


def receive(url, want):
    # Notifications arrive a moment after the call that causes them.
    for _ in range(10):
        s, b = sqs("ReceiveMessage", {"QueueUrl": url, "MaxNumberOfMessages": 10, "WaitTimeSeconds": 1})
        bodies = [m["Body"] for m in json.loads(b or b"{}").get("Messages", [])] if s == 200 else []
        if any(want in x for x in bodies):
            return True
        time.sleep(0.3)
    return False


def upload():
    with LOCK:
        n = next(COUNT)
    key = "uploads/order-u%d.txt" % n
    s1, _ = call("PUT", "/%s/%s" % (BUCKET, key), "s3", headers={"content-type": "text/plain"}, body=("order u%d\n" % n).encode())
    s2, _ = ddb("PutItem", {"TableName": TABLE, "Item": {"pk": {"S": "u%d" % n}, "status": {"S": "new"}, "file": {"S": key}}})
    return "uploaded %s: PutObject %d, PutItem %d\n" % (key, s1, s2)


def notified():
    return "upload notification: %s\n" % receive(os.environ["UPLOADS_QUEUE_URL"], "uploads/order-u")


def publish():
    msg = "order event %d" % time.time_ns()
    body = urllib.parse.urlencode({"Action": "Publish", "TopicArn": os.environ["EVENTS_TOPIC"], "Message": msg}).encode()
    s, _ = call("POST", "/", "sns", headers={"content-type": "application/x-www-form-urlencoded"}, body=body)
    return "Publish %d, delivered to the subscribed queue: %s\n" % (s, receive(os.environ["NOTIFY_QUEUE_URL"], msg))


def wrong_region():
    s, _ = call("GET", "/" + BUCKET, "s3", query={"list-type": "2"}, region="us-east-1")
    return "ListObjectsV2 signed for us-east-1: %d\n" % s


def foreign_key():
    s, _ = call("GET", "/", "s3", key="FOREIGNKEYFORTEST", secret="not-the-test-secret")
    return "ListBuckets with another access key: %d\n" % s


def real_aws():
    try:
        with urllib.request.urlopen("https://s3.amazonaws.com/", timeout=10) as r:
            return "real AWS answered %d\n" % r.status
    except Exception as e:
        return "real AWS refused: %s\n" % e


def env():
    names = ("ORDERS_TABLE", "COUNTERS_TABLE", "EVENTS_TOPIC", "STAGE", "SHARED_VPC", "UPLOAD_BUCKET",
             "UPLOADS_QUEUE_URL", "NOTIFY_QUEUE_URL", "CUSTOMERS_TABLE", "JOBS_QUEUE_URL")
    return "".join("%s=%s\n" % (n, os.environ.get(n, "<unset>")) for n in names)


ROUTES = {"/upload": upload, "/notified": notified, "/publish": publish, "/env": env,
          "/wrong-region": wrong_region, "/foreign-key": foreign_key, "/real-aws": real_aws}


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        fn = ROUTES.get(self.path)
        if fn:
            text, code = fn(), 200
        elif self.path == "/":
            text, code = "aws fixture: " + " ".join(ROUTES) + "\n", 200
        else:
            text, code = "not found\n", 404
        b = text.encode()
        self.send_response(code)
        self.send_header("content-type", "text/plain; charset=utf-8")
        self.send_header("content-length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)


http.server.ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
`
