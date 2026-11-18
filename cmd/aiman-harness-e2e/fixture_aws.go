//go:build e2e

package main

import (
	"context"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// The aws fixture: a MiniStack service whose plan is written by hand
// (awsiac.DeclarativeImporter reads infra/plan.json in place of a
// CloudFormation template), a seed file, and a Python app on the stdlib
// that signs its own S3 and DynamoDB calls, so the run needs no package
// download.
func init() {
	fixtures["aws"] = fixture{
		scenario: "upload",
		images:   []string{harnesstest.PythonImage, harness.MiniStackImage},
		write: func(ctx context.Context, repo string) error {
			// One driver serves one fixture, so the swap is never put back.
			awsiac.SwapImporter(awsiac.SourceCloudFormation, awsiac.DeclarativeImporter)
			return files(map[string]string{
				".aiman/harness.yaml":          awsHarnessFile,
				"infra/plan.json":              awsFixturePlan,
				".aiman/aws/seed.yaml":         awsFixtureSeed,
				"testdata/s3/readme.txt":       "hello from the seed\nsecond line\n",
				"testdata/s3/notes/orders.csv": "id,status\no1,received\no2,new\n",
				"testdata/ddb/orders.jsonl":    `{"pk":"o1","status":"received","total":12.5}` + "\n" + `{"pk":"o2","status":"new","tags":{"$ss":["gift"]},"api_key":"k-123"}` + "\n",
				"app.py":                       awsFixtureApp,
			})(ctx, repo)
		},
	}
	allowedImages = append(allowedImages, harness.MiniStackImage)
}

const awsHarnessFile = `version: 1
app:
  image: ` + harnesstest.PythonImage + `
  run: exec python3 -u /src/app.py
  ports: [{name: web, container: 8080}]
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb]
      region: eu-west-1
      stacks:
        - {name: orders, cloudformation: infra/plan.json}
      env:
        ORDERS_TABLE: orders.OrdersTable
        UPLOAD_BUCKET: orders.UploadBucket
      function_env: [orders.OrdersFunction]
      seed: .aiman/aws/seed.yaml
scenarios:
  - id: upload
    title: Uploads land in S3 and DynamoDB
    services: [aws]
    steps:
      - do: Open the AWS tab
        expect: Resources lists the table and the bucket with their env variables
      - do: Open the web link and press Upload
        expect: Calls shows PutObject and PutItem with 200
      - do: Query the by-status index for new
        expect: The uploaded order is there, and the stream has its INSERT
`

// awsFixturePlan is a table with a GSI and a stream, and a versioned
// bucket. OrdersFunction's IMPORTED names a resource no stack has, so the
// plan carries one warning.
const awsFixturePlan = `{
  "resources": [
    {"logical_id": "OrdersTable", "type": "AWS::DynamoDB::Table", "kind": "table", "table": {
      "hash_key": "pk", "attributes": [{"name": "pk", "type": "S"}, {"name": "status", "type": "S"}],
      "gsis": [{"name": "by-status", "hash_key": "status"}], "stream_view_type": "NEW_AND_OLD_IMAGES"}},
    {"logical_id": "UploadBucket", "type": "AWS::S3::Bucket", "kind": "bucket", "bucket": {"versioning": "Enabled"}}
  ],
  "functions": [{"logical_id": "OrdersFunction", "vars": [
    {"name": "TABLE", "value": [{"ref": {"stack": "orders", "logical_id": "OrdersTable"}}]},
    {"name": "IMPORTED", "value": [{"ref": {"stack": "orders", "logical_id": "NotHere"}}]}
  ]}]
}`

const awsFixtureSeed = `objects:
  - {bucket: orders.UploadBucket, from: testdata/s3, prefix: seed/}
items:
  - {table: orders.OrdersTable, file: testdata/ddb/orders.jsonl}
`

// awsFixtureApp answers on 8080. /upload puts an object and an item;
// /wrong-region, /foreign-key and /real-aws make the calls the AWS tab's
// hints name.
const awsFixtureApp = `import datetime, hashlib, hmac, http.server, itertools, json, os, threading
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


def upload():
    with LOCK:
        n = next(COUNT)
    key = "uploads/order-u%d.txt" % n
    s1, _ = call("PUT", "/%s/%s" % (BUCKET, key), "s3", headers={"content-type": "text/plain"}, body=("order u%d\n" % n).encode())
    s2, _ = ddb("PutItem", {"TableName": TABLE, "Item": {"pk": {"S": "u%d" % n}, "status": {"S": "new"}, "file": {"S": key}}})
    return "uploaded %s: PutObject %d, PutItem %d\n" % (key, s1, s2)


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


ROUTES = {"/upload": upload, "/wrong-region": wrong_region, "/foreign-key": foreign_key, "/real-aws": real_aws}


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
