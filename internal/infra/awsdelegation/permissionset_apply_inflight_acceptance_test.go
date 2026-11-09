//go:build acceptance

package awsdelegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// inFlightMeta is what describe-account-assignment-creation-status reports for
// one in-flight request beyond its status: the target, permission set and
// principal that list-account-assignment-creation-status leaves out.
type inFlightMeta struct {
	target, permissionSet, principal string
	statuses                         []string
	calls                            int
}

// describeWithTargets wraps a world so that status polls carry the request's
// TargetId, PermissionSetArn and PrincipalId, as the real API does.
func describeWithTargets(w *world, meta map[string]*inFlightMeta) func(args []string) (string, error) {
	return func(args []string) (string, error) {
		if op(args) != "sso-admin describe-account-assignment-creation-status" {
			return w.handle(args)
		}
		id := flagValue(args, "--account-assignment-creation-request-id")
		m, ok := meta[id]
		if !ok {
			return "", &unknownRequest{id: id}
		}
		st := m.statuses[len(m.statuses)-1]
		if m.calls < len(m.statuses) {
			st = m.statuses[m.calls]
		}
		m.calls++
		b, err := json.Marshal(map[string]any{"AccountAssignmentCreationStatus": map[string]string{
			"RequestId": id, "Status": st, "TargetId": m.target, "TargetType": "AWS_ACCOUNT",
			"PermissionSetArn": m.permissionSet, "PrincipalType": "USER", "PrincipalId": m.principal,
		}})
		return string(b), err
	}
}

type unknownRequest struct{ id string }

func (u *unknownRequest) Error() string {
	return "An error occurred (ResourceNotFoundException) when calling the DescribeAccountAssignmentCreationStatus operation: " + u.id
}

func TestApplyInFlightCreateMustMatchOurAssignment(t *testing.T) {
	w := defaultWorld()
	ours := withExisting(w)
	w.assignments = nil
	w.createConflict = true
	// Two creates are in flight in this instance. The newer one (req-new) is
	// another principal's assignment for another account; ours is req-ours.
	w.inFlight = []string{"req-ours", "req-new"}
	meta := map[string]*inFlightMeta{
		"req-ours": {target: "123456789012", permissionSet: ours, principal: "u-1", statuses: []string{"IN_PROGRESS", "SUCCEEDED"}},
		"req-new":  {target: "999999999999", permissionSet: ours, principal: "u-other", statuses: []string{"IN_PROGRESS"}},
	}
	cli := &scriptedAWS{handle: describeWithTargets(w, meta)}
	res, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.CreatedAssignment {
		t.Error("CreatedAssignment = false after waiting for our own in-flight create")
	}
	if meta["req-ours"].calls < 2 {
		t.Errorf("req-ours was polled %d times, want it followed to SUCCEEDED", meta["req-ours"].calls)
	}
	if meta["req-new"].calls > 1 {
		t.Errorf("req-new (another principal's request) was polled %d times, want at most one look to read its target", meta["req-new"].calls)
	}
}

func TestApplyInFlightCreatesOfOthersAreNotOurs(t *testing.T) {
	w := defaultWorld()
	ours := withExisting(w)
	w.assignments = nil
	w.createConflict = true
	w.inFlight = []string{"req-other"}
	meta := map[string]*inFlightMeta{
		"req-other": {target: "123456789012", permissionSet: ours, principal: "u-other", statuses: []string{"IN_PROGRESS"}},
	}
	cli := &scriptedAWS{handle: describeWithTargets(w, meta)}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "ConflictException") {
		t.Fatalf("err = %v, want the conflict: the only in-flight create belongs to another principal", err)
	}
	if n := len(cli.callsOf("sso-admin provision-permission-set")); n != 0 {
		t.Errorf("provision-permission-set called %d times although our assignment never appeared", n)
	}
}
