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

// awsCFNCheck finds every resource of the SAM template through the names
// the run maps (env and function_env), with nothing in code but the
// variable names.
const awsCFNCheck = `
import boto3, json, os
ddb, s3, sqs, sns = (boto3.client(n) for n in ("dynamodb", "s3", "sqs", "sns"))
t = os.environ["RECORDS_TABLE"]
d = ddb.describe_table(TableName=t)["Table"]
print("TABLE", t, [k["AttributeName"] for k in d["KeySchema"]], os.environ["STAGE"])
ddb.put_item(TableName=t, Item={"key": {"S": "k1"}})
b = os.environ["UPLOADS_BUCKET"]
print("VERSIONING", s3.get_bucket_versioning(Bucket=b).get("Status"))
q = os.environ["UPLOADS_QUEUE_URL"]
s3.put_object(Bucket=b, Key="in/a.txt", Body=b"x")
sns.publish(TopicArn=os.environ["EVENTS_TOPIC"], Message="via-topic")
got = []
for _ in range(10):
    got += [m["Body"] for m in sqs.receive_message(QueueUrl=q, MaxNumberOfMessages=10, WaitTimeSeconds=1).get("Messages", [])]
    if any("in/a.txt" in g for g in got) and any("via-topic" in g for g in got):
        break
print("EVENTS", any("in/a.txt" in g for g in got), any("via-topic" in g for g in got))
m = json.load(open(os.environ["AIMAN_AWS_RESOURCES"]))
print("MANIFEST", sorted(r["logical_id"] for r in m["resources"]))
`

// TestAWSDockerCFNImport brings up a run whose aws service reads a SAM
// template: the importer plans it, the applier creates it through the
// tap, and a client finds the table, bucket, queue and topic through the
// env the harness file maps, with the scenario's parameter override.
func TestAWSDockerCFNImport(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, MiniStackImage)
	repo := awsRepo(t, awsCFNFile, map[string]string{"infra/template.yaml": awsSAMTemplate})
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
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "sam", Actor: domain.EventActorUser})
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
	if run.AWS == nil || len(run.AWS.Resources) != 5 || len(run.AWS.Warnings) != 0 {
		t.Fatalf("run.aws %+v", run.AWS)
	}
	sandbox := filepath.Join(m.opts.Dir, "s1", run.ID, "sandbox")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", filepath.Join(sandbox, "compose.yaml"),
		"run", "--rm", "--no-deps", "--entrypoint", "python3", CheckService(0), "-c", awsCFNCheck)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("client: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"TABLE records-e2e ['key'] e2e", "VERSIONING Enabled", "EVENTS True True",
		"MANIFEST ['Events', 'Events/Subscription[0]', 'Records', 'UploadQueue', 'Uploads']",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	appEnv, err := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", filepath.Join(sandbox, "compose.yaml"),
		"exec", "-T", ServiceApp, "sh", "-c", "echo $RECORDS_TABLE $UPLOADS_BUCKET").CombinedOutput()
	if f := strings.Fields(string(appEnv)); err != nil || len(f) != 2 || f[0] != "records-e2e" || !strings.HasPrefix(f[1], "app-uploads-") {
		t.Fatalf("app env: %v %q", err, appEnv)
	}
	if err := m.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
}
