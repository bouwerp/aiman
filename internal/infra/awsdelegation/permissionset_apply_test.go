package awsdelegation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeAWSOnPath puts an aws script on PATH that writes its arguments to a file
// and then runs body.
func fakeAWSOnPath(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil { // #nosec G306 -- test script must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestExecAWSCLIStripsDuplicateProfile(t *testing.T) {
	argsFile := fakeAWSOnPath(t, `echo '{"ok":true}'`)
	out, err := ExecAWSCLI{Profile: "acme-admin"}.Run(context.Background(), "--profile", "acme-admin", "sso-admin", "list-instances")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"ok":true}` {
		t.Errorf("stdout = %q", out)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "--profile\nacme-admin\n--output\njson\nsso-admin\nlist-instances\n"
	if string(got) != want {
		t.Errorf("aws argv =\n%s\nwant\n%s", got, want)
	}
}

func TestExecAWSCLIKeepsAnotherProfile(t *testing.T) {
	got := ExecAWSCLI{Profile: "a"}.stripProfile([]string{"--profile", "b", "sts", "get-caller-identity"})
	if strings.Join(got, " ") != "--profile b sts get-caller-identity" {
		t.Errorf("stripProfile = %v, want a different profile kept", got)
	}
}

func TestExecAWSCLIStdoutOnlyOnSuccess(t *testing.T) {
	fakeAWSOnPath(t, `echo 'warning: something' >&2; echo '{"Account":"1"}'`)
	out, err := ExecAWSCLI{Profile: "p"}.Run(context.Background(), "sts", "get-caller-identity")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"Account":"1"}` {
		t.Errorf("stdout = %q, want the JSON without the stderr warning", out)
	}
}

func TestExecAWSCLIErrorFormat(t *testing.T) {
	for name, body := range map[string]string{
		"stderr first": `echo 'partial' ; printf 'An error occurred (AccessDeniedException) when calling X: no\nsecond\n' >&2; exit 254`,
		"stdout else":  `printf 'An error occurred (AccessDeniedException) when calling X: no\nsecond\n'; exit 254`,
	} {
		t.Run(name, func(t *testing.T) {
			fakeAWSOnPath(t, body)
			_, err := ExecAWSCLI{Profile: "p"}.Run(context.Background(), "--region", "eu-west-1", "sso-admin", "list-instances")
			want := "aws sso-admin list-instances: An error occurred (AccessDeniedException) when calling X: no"
			if err == nil || err.Error() != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
	t.Run("silent failure", func(t *testing.T) {
		fakeAWSOnPath(t, `exit 3`)
		_, err := ExecAWSCLI{Profile: "p"}.Run(context.Background(), "sts", "get-caller-identity")
		if err == nil || !strings.HasPrefix(err.Error(), "aws sts get-caller-identity: exit status 3") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestApplyEmptyRequestIDIsAnError(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{}
	cli.handle = func(args []string) (string, error) {
		if op(args) == "sso-admin create-account-assignment" {
			return `{"AccountAssignmentCreationStatus":{"Status":"IN_PROGRESS"}}`, nil
		}
		return w.handle(args)
	}
	_, err := Apply(context.Background(), cli, fastSpec(t), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "RequestId") {
		t.Fatalf("err = %v, want one naming the missing RequestId", err)
	}
	if n := len(cli.callsOf("sso-admin describe-account-assignment-creation-status")); n != 0 {
		t.Errorf("polled %d times on an empty request id", n)
	}
}

func TestApplyUnreadableConfigWarns(t *testing.T) {
	w := defaultWorld()
	cli := &scriptedAWS{handle: w.handle}
	spec := fastSpec(t)
	spec.AWSConfigPath = filepath.Join(t.TempDir(), "missing")
	var log strings.Builder
	if _, err := Apply(context.Background(), cli, spec, &log); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.HasPrefix(log.String(), "warning: cannot read sso_region") {
		t.Errorf("log does not start with the region warning:\n%s", log.String())
	}
}

func TestRunErrorKeepsUnknownOptionsLine(t *testing.T) {
	stderr := "\nusage: aws [options] <command> <subcommand> [<subcommand> ...] [parameters]\n" +
		"To see help text, you can run:\n\n  aws help\n\nUnknown options: --region-scope, ALL_REGIONS\n"
	err := runError([]string{"--profile", "p", "sso-admin", "list-instances", "--region-scope", "ALL_REGIONS"}, "", stderr, errors.New("exit status 252"))
	if got, want := err.Error(), "aws sso-admin list-instances: Unknown options: --region-scope, ALL_REGIONS"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
	if !isUnknownRegionScope(err) {
		t.Error("isUnknownRegionScope = false for the CLI's unknown-option answer")
	}
}

func TestPartitionHome(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:iam::123456789012:user/admin":        awsPartitionHome,
		"arn:aws-cn:iam::123456789012:user/admin":     "",
		"arn:aws-us-gov:iam::123456789012:user/admin": "",
		"": "",
	} {
		if got, _ := partitionHome(arn); got != want {
			t.Errorf("partitionHome(%q) = %q, want %q", arn, got, want)
		}
	}
}
