package awsdelegation

import (
	"context"
	"strings"
	"testing"
)

func TestAssumeRoleUsesRegionalSTSEndpoint(t *testing.T) {
	var got string
	prev := runAWS
	t.Cleanup(func() { runAWS = prev })
	runAWS = func(_ context.Context, args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"Credentials":{"AccessKeyId":"A","SecretAccessKey":"S","SessionToken":"T"}}`), nil
	}
	if _, err := GetTemporaryCredentials(context.Background(), "lab", CredentialOptions{
		RoleARN: "arn:aws:iam::120345345908:role/TemporaryDelegatedRole",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--region us-east-1 --endpoint-url https://sts.us-east-1.amazonaws.com") {
		t.Fatalf("assume-role must use the regional STS endpoint, got %s", got)
	}
}

func TestRegionalSTSArgsSkipsOtherPartitions(t *testing.T) {
	if a := regionalSTSArgs("arn:aws-cn:iam::1:role/R"); a != nil {
		t.Fatalf("aws-cn role got %v", a)
	}
}
