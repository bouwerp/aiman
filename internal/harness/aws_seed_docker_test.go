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

// awsSeedCheck reads every resource through the env names the run maps and
// the manifest, with boto3 and nothing in code but the variable names.
const awsSeedCheck = `
import boto3, json, os
m = json.load(open(os.environ["AIMAN_AWS_RESOURCES"]))
ddb, s3, sqs, sns = (boto3.client(n) for n in ("dynamodb", "s3", "sqs", "sns"))
t = os.environ["ORDERS_TABLE"]
d = ddb.describe_table(TableName=t)["Table"]
print("TABLE", t, d["LatestStreamArn"] == os.environ["ORDERS_STREAM"], [i["IndexName"] for i in d["GlobalSecondaryIndexes"]])
print("TTL", ddb.describe_time_to_live(TableName=t)["TimeToLiveDescription"]["AttributeName"])
print("ITEM", ddb.get_item(TableName=t, Key={"pk": {"S": "o1"}})["Item"]["status"]["S"])
q = ddb.query(TableName=t, IndexName="by-status", KeyConditionExpression="#s = :s", ExpressionAttributeNames={"#s": "status"}, ExpressionAttributeValues={":s": {"S": "new"}})
print("GSI", [i["pk"]["S"] for i in q["Items"]])
b = os.environ["UPLOAD_BUCKET"]
print("OBJECTS", sorted(o["Key"] for o in s3.list_objects_v2(Bucket=b)["Contents"]), s3.get_object(Bucket=b, Key="seed/a.txt")["Body"].read().decode())
print("VERSIONING", s3.get_bucket_versioning(Bucket=b).get("Status"))
dlq = [r for r in m["resources"] if r["logical_id"] == "DLQ"][0]["attributes"]["url"]
msgs = sqs.receive_message(QueueUrl=dlq, MaxNumberOfMessages=10, MessageAttributeNames=["All"])["Messages"]
print("MESSAGES", sorted(x["Body"] for x in msgs))
events = os.environ["EVENTS_QUEUE_URL"]
sns.publish(TopicArn=os.environ["TOPIC_ARN"], Message="via-topic")
s3.put_object(Bucket=b, Key="uploads/new.txt", Body=b"x")
got = []
for _ in range(10):
    got += [x["Body"] for x in sqs.receive_message(QueueUrl=events, MaxNumberOfMessages=10, WaitTimeSeconds=1).get("Messages", [])]
    if any("via-topic" == g for g in got) and any("uploads/new.txt" in g for g in got):
        break
print("EVENTS", any(g == "via-topic" for g in got), any("uploads/new.txt" in g for g in got))
print("SUB", [r for r in m["resources"] if r["logical_id"] == "Sub"][0]["attributes"]["arn"].startswith(os.environ["TOPIC_ARN"] + ":"))
`

// TestAWSDockerSeed brings a run up for real: the applier creates a plan
// built by hand (DeclarativeImporter) through the edge tap, seeds objects,
// items and messages, and a client in the MiniStack image finds everything
// through the env names and the manifest.
func TestAWSDockerSeed(t *testing.T) {
	needDocker(t)
	useDeclared(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, MiniStackImage)
	repo := awsRepo(t, awsPlanFile, nil)
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
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "seeded", Actor: domain.EventActorUser})
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
	if run.AWS == nil || len(run.AWS.Resources) != 6 {
		t.Fatalf("run.aws %+v", run.AWS)
	}
	sandbox := filepath.Join(m.opts.Dir, "s1", run.ID, "sandbox")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", filepath.Join(sandbox, "compose.yaml"),
		"run", "--rm", "--no-deps", "--entrypoint", "python3", CheckService(0), "-c", awsSeedCheck)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("client: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"TABLE orders-OrdersTable-", "True ['by-status']", "TTL expires", "ITEM received", "GSI ['o2']",
		"OBJECTS ['seed/a.txt', 'seed/dir/b.csv'] hello", "VERSIONING Enabled",
		"MESSAGES ['first', 'second']", "EVENTS True True", "SUB True",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The app container has the same names.
	appEnv, err := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", filepath.Join(sandbox, "compose.yaml"),
		"exec", "-T", ServiceApp, "sh", "-c", "echo $ORDERS_TABLE $TABLE; cat $AIMAN_AWS_RESOURCES | head -c 1").CombinedOutput()
	if f := strings.Fields(string(appEnv)); err != nil || len(f) != 3 || f[0] != f[1] || !strings.HasPrefix(f[0], "orders-OrdersTable-") || f[2] != "{" {
		t.Fatalf("app env: %v %q", err, appEnv)
	}
	if err := m.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
}
