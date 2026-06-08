package usecase

import (
	"context"
	"strings"
	"testing"
)

func TestJoinPrompt(t *testing.T) {
	cases := []struct {
		name       string
		base, user string
		want       string
	}{
		{"both present", "base prompt", "extra", "base prompt extra"},
		{"empty base", "", "extra", "extra"},
		{"empty user", "base prompt", "", "base prompt"},
		{"trims whitespace", "  base  ", "  extra  ", "base extra"},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := joinPrompt(c.base, c.user); got != c.want {
				t.Fatalf("joinPrompt(%q, %q) = %q, want %q", c.base, c.user, got, c.want)
			}
		})
	}
}

// fakePromptDeliverer records WriteFile and Execute calls for testing the
// initial-prompt delivery path.
type fakePromptDeliverer struct {
	writePath    string
	writeContent []byte
	writeErr     error
	execCmds     []string
}

func (f *fakePromptDeliverer) WriteFile(_ context.Context, path string, content []byte) error {
	f.writePath = path
	f.writeContent = content
	return f.writeErr
}

func (f *fakePromptDeliverer) Execute(_ context.Context, cmd string) (string, error) {
	f.execCmds = append(f.execCmds, cmd)
	return "", nil
}

func TestSendKeysScriptReadsPromptFromFile(t *testing.T) {
	// The script must source the prompt from the file via command substitution
	// so its contents are never interpolated into the command text.
	script := sendKeysScript("my-session", "/tmp/aiman-prompt-abc")
	if !strings.Contains(script, `-l -- "$(cat `) {
		t.Fatalf("script does not read prompt via cat: %q", script)
	}
	if !strings.Contains(script, "/tmp/aiman-prompt-abc") {
		t.Fatalf("script does not reference prompt path: %q", script)
	}
	if !strings.Contains(script, "rm -f") {
		t.Fatalf("script does not clean up the prompt file: %q", script)
	}
}

func TestDetachCommandEscapesSingleQuotes(t *testing.T) {
	got := detachCommand("echo 'hi'")
	want := `nohup bash -c 'echo '\''hi'\''' >/dev/null 2>&1 &`
	if got != want {
		t.Fatalf("detachCommand single-quote escaping wrong:\n got: %s\nwant: %s", got, want)
	}
}

func TestDeliverInitialPromptRoutesPromptThroughFile(t *testing.T) {
	// A prompt containing shell metacharacters must never appear in the executed
	// command — it is written to a file and read back via cat.
	malicious := "do the thing $(touch /tmp/pwned) `id`"
	f := &fakePromptDeliverer{}
	deliverInitialPrompt(context.Background(), f, "sess", "abc-123", malicious)

	if string(f.writeContent) != malicious {
		t.Fatalf("prompt not written verbatim to file: %q", f.writeContent)
	}
	if f.writePath != "/tmp/aiman-prompt-abc-123" {
		t.Fatalf("unexpected prompt path: %q", f.writePath)
	}
	if len(f.execCmds) != 1 {
		t.Fatalf("expected exactly one Execute call, got %d", len(f.execCmds))
	}
	for _, frag := range []string{"$(touch /tmp/pwned)", "`id`", "do the thing"} {
		if strings.Contains(f.execCmds[0], frag) {
			t.Fatalf("prompt content leaked into command (%q): %s", frag, f.execCmds[0])
		}
	}
}

func TestDeliverInitialPromptSkipsEmpty(t *testing.T) {
	f := &fakePromptDeliverer{}
	deliverInitialPrompt(context.Background(), f, "sess", "abc-123", "")
	if f.writePath != "" || len(f.execCmds) != 0 {
		t.Fatalf("empty prompt should not write or execute anything")
	}
}

func TestDeliverInitialPromptSkipsExecuteOnWriteError(t *testing.T) {
	f := &fakePromptDeliverer{writeErr: context.DeadlineExceeded}
	deliverInitialPrompt(context.Background(), f, "sess", "abc-123", "hello")
	if len(f.execCmds) != 0 {
		t.Fatalf("must not send-keys when the prompt file failed to write")
	}
}

func TestAWSRoleSessionName(t *testing.T) {
	if got := awsRoleSessionName("12345678-90ab-cdef"); got != "session-12345678" {
		t.Fatalf("unexpected aws role session name: %q", got)
	}
	if got := awsRoleSessionName(""); got != "session" {
		t.Fatalf("unexpected empty-session fallback: %q", got)
	}
}

func TestSharedSessionAWSEnv(t *testing.T) {
	env := sharedSessionAWSEnv("eu-west-1")
	if got := env["AWS_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_REGION: %q", got)
	}
	if got := env["AWS_DEFAULT_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_DEFAULT_REGION: %q", got)
	}
	if _, ok := env["AWS_SHARED_CREDENTIALS_FILE"]; ok {
		t.Fatal("did not expect AWS_SHARED_CREDENTIALS_FILE in shared session env")
	}
	if _, ok := env["AWS_CONFIG_FILE"]; ok {
		t.Fatal("did not expect AWS_CONFIG_FILE in shared session env")
	}
}
