package awsdelegation

import (
	"strings"
	"testing"
)

func TestAssumeRoleFailureExplainsRootCaller(t *testing.T) {
	err := explainAssumeRoleFailure("lab", "arn:aws:iam::120345345908:role/TemporaryDelegatedRole",
		"An error occurred (AccessDenied) when calling the AssumeRole operation: Roles may not be assumed by root accounts.")
	got := err.Error()
	if !strings.Contains(got, "root") || !strings.Contains(got, "lab") {
		t.Fatalf("expected the source profile and root refusal, got %s", got)
	}
	if strings.Contains(got, "trust policy must allow") {
		t.Fatalf("root refusal must not be described as a trust-policy problem, got %s", got)
	}
}

func TestAssumeRoleFailureKeepsTrustHintForOtherDenials(t *testing.T) {
	err := explainAssumeRoleFailure("dev", "arn:aws:iam::120345345908:role/TemporaryDelegatedRole",
		"An error occurred (AccessDenied) when calling the AssumeRole operation: User is not authorized to perform: sts:AssumeRole")
	got := err.Error()
	if !strings.Contains(got, "trust policy") {
		t.Fatalf("other AccessDenied results still need the trust-policy hint, got %s", got)
	}
	if strings.Contains(got, "account root") {
		t.Fatalf("a non-root denial must not be blamed on root, got %s", got)
	}
}
