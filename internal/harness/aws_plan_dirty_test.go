package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAWSPlanDirty builds the plan from the working tree with Dirty: the
// harness file and the stack it reads, both uncommitted, and still from the
// commit without it.
func TestAWSPlanDirty(t *testing.T) {
	useDeclared(t)
	repo := awsRepo(t, awsPlanFile, nil)
	file := strings.Replace(awsPlanFile, "region: eu-west-1", "region: us-west-2", 1)
	if err := os.WriteFile(filepath.Join(repo, FilePath), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	stack := strings.Replace(awsDeclaredPlan, `"resources": [`, `"resources": [
    {"logical_id": "Extra", "type": "AWS::SQS::Queue", "kind": "queue", "queue": {}},`, 1)
	if err := os.WriteFile(filepath.Join(repo, "infra/plan.json"), []byte(stack), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := newTestManager(t)
	_, committed, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "seeded"})
	if err != nil {
		t.Fatal(err)
	}
	if v := committed[0]; v.Region != "eu-west-1" || len(v.Resources) != 6 {
		t.Fatalf("committed plan: region %s, %d resources", v.Region, len(v.Resources))
	}
	_, dirty, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "seeded", Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	if v := dirty[0]; v.Region != "us-west-2" || len(v.Resources) != 7 {
		t.Fatalf("dirty plan: region %s, %d resources", v.Region, len(v.Resources))
	}
}
