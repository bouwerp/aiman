//go:build e2e

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
)

// TestAWSFixturePlan plans the aws fixture's SAM and Terraform stacks
// through the real importers, with no Docker: both stacks' resources, the
// scenario overrides, the seed and the one warning the Resources view
// shows.
func TestAWSFixturePlan(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := fixtures["aws"].write(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	m, err := harness.NewManager(harness.Options{
		Dir: filepath.Join(t.TempDir(), "h"), Docker: harness.ExecDocker{},
		AppImage: func(string, string) (string, error) { return "python:3.13-slim", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	for _, tc := range []struct {
		scenario, stage, env string
		seeded               bool
	}{
		{"upload", "e2e", "e2e", true},
		{"staging", "test", "test", false},
	} {
		_, views, err := m.AWSPlans(context.Background(), harness.AWSPlanRequest{Repo: repo, Scenario: tc.scenario})
		if err != nil || len(views) != 1 {
			t.Fatalf("%s: %v %+v", tc.scenario, err, views)
		}
		v := views[0]
		var got []string
		for _, r := range v.Resources {
			got = append(got, r.Stack+"."+r.LogicalID)
		}
		want := "orders.OrdersTable orders.CountersTable orders.UploadQueue orders.UploadBucket orders.NotifyQueue " +
			"orders.EventsTopic orders.EventsSubscription shared.aws_dynamodb_table.customers " +
			"shared.module.jobs.aws_sqs_queue.dlq shared.module.jobs.aws_sqs_queue.main"
		if strings.Join(got, " ") != want {
			t.Errorf("%s resources %v", tc.scenario, got)
		}
		env := v.Env
		if env["STAGE"] != tc.stage || env["COUNTERS_TABLE"] != "orders-"+tc.stage+"-counters" ||
			env["CUSTOMERS_TABLE"] != "shared-"+tc.env+"-customers" ||
			env["JOBS_QUEUE_URL"] != "http://aws:4566/000000000000/shared-"+tc.env+"-jobs" ||
			!strings.HasPrefix(env["ORDERS_TABLE"], "orders-OrdersTable-") ||
			!strings.HasPrefix(env["UPLOAD_BUCKET"], "orders-uploadbucket-") ||
			!strings.HasPrefix(env["EVENTS_TOPIC"], "arn:aws:sns:eu-west-1:000000000000:orders-EventsTopic-") ||
			!strings.HasPrefix(env["NOTIFY_QUEUE_URL"], "http://aws:4566/000000000000/orders-NotifyQueue-") {
			t.Errorf("%s env %v", tc.scenario, env)
		}
		if _, ok := env["SHARED_VPC"]; ok {
			t.Errorf("%s: SHARED_VPC set", tc.scenario)
		}
		if len(v.Warnings) != 1 || v.Warnings[0].Resource != "OrdersFunction" || !strings.Contains(v.Warnings[0].Message, "shared-vpc-id") {
			t.Errorf("%s warnings %+v", tc.scenario, v.Warnings)
		}
		seeded := v.Seed != nil && (len(v.Seed.Objects)+len(v.Seed.Items)+len(v.Seed.Messages)) > 0
		if seeded != tc.seeded {
			t.Errorf("%s seed %+v", tc.scenario, v.Seed)
		}
	}
}
