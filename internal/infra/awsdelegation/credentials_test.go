package awsdelegation

import (
	"context"
	"fmt"
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

func TestGetTemporaryCredentialsFallsBackWhenRootCannotAssumeRole(t *testing.T) {
	var calls [][]string
	prev := runAWS
	t.Cleanup(func() { runAWS = prev })
	runAWS = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if strings.Contains(strings.Join(args, " "), "assume-role") {
			return []byte("An error occurred (AccessDenied) when calling the AssumeRole operation: Roles may not be assumed by root accounts."), fmt.Errorf("exit status 254")
		}
		return []byte(`{"Credentials":{"AccessKeyId":"ASIATEST","SecretAccessKey":"secret","SessionToken":"token","Expiration":"2026-10-01T12:00:00Z"}}`), nil
	}

	creds, err := GetTemporaryCredentials(context.Background(), "lab", CredentialOptions{
		RoleARN:         "arn:aws:iam::120345345908:role/TemporaryDelegatedRole",
		DurationSeconds: 43200,
		SessionPolicy:   `{"Version":"2012-10-17","Statement":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || !creds.RootSession || creds.AccessKeyID != "ASIATEST" {
		t.Fatalf("expected a root session token, got %+v", creds)
	}
	if len(calls) != 2 {
		t.Fatalf("expected assume-role then get-session-token, got %#v", calls)
	}
	tokenArgs := strings.Join(calls[1], " ")
	if !strings.Contains(tokenArgs, "get-session-token") || !strings.Contains(tokenArgs, "--duration-seconds 3600") || strings.Contains(tokenArgs, "--policy") {
		t.Fatalf("root fallback must be a 1h session token with no role policy, got %s", tokenArgs)
	}
}

func TestGetTemporaryCredentialsDoesNotFallBackOnOtherAccessDenied(t *testing.T) {
	var calls int
	prev := runAWS
	t.Cleanup(func() { runAWS = prev })
	runAWS = func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		return []byte("An error occurred (AccessDenied) when calling the AssumeRole operation: User is not authorized to perform: sts:AssumeRole"), fmt.Errorf("exit status 254")
	}

	_, err := GetTemporaryCredentials(context.Background(), "dev", CredentialOptions{
		RoleARN: "arn:aws:iam::120345345908:role/TemporaryDelegatedRole",
	})
	if err == nil || !strings.Contains(err.Error(), "trust policy") {
		t.Fatalf("expected the trust-policy error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("a non-root denial must not call get-session-token, calls=%d", calls)
	}
}
