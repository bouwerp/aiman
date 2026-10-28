package usecase

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestTmuxNameForCreateUsesBranchNotDisplayName(t *testing.T) {
	got := tmuxNameForCreate("wtb-1925-auth", "spike")
	want := domain.SanitizeTmuxSessionName("wtb-1925-auth")
	if got != want {
		t.Fatalf("tmuxNameForCreate = %q, want branch-derived %q (not the display name)", got, want)
	}
	if got == domain.SanitizeTmuxSessionName("spike") && want != got {
		t.Fatal("display name must not become the tmux session")
	}
}

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

// logBootWait is the only evidence a slow boot leaves behind: without it, a
// timed-out boot-wait and a normal one are indistinguishable from serve.log.
func TestLogBootWaitReportsReasonAndAttempts(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	logBootWait("sess-1", "some pane noise\nAIMAN_BOOTWAIT:timeout:15\n")

	got := buf.String()
	for _, want := range []string{"session=sess-1", "reason=timeout", "attempts=15"} {
		if !strings.Contains(got, want) {
			t.Errorf("log output missing %q, got: %s", want, got)
		}
	}
}

func TestLogBootWaitToleratesMissingMarker(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	logBootWait("sess-1", "no marker in this output")

	if buf.Len() != 0 {
		t.Errorf("expected no log line without a marker, got: %s", buf.String())
	}
}

func TestSharedSessionAWSEnv(t *testing.T) {
	withLocalAWSProfiles(t, []string{"prod"})

	env := SharedSessionAWSEnv("prod", "eu-west-1")
	if got := env["AWS_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_REGION: %q", got)
	}
	if got := env["AWS_DEFAULT_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_DEFAULT_REGION: %q", got)
	}
	if got := env["AWS_PROFILE"]; got != "prod" {
		t.Fatalf("expected AWS_PROFILE=prod, got %q", got)
	}
	if _, ok := env["AWS_SHARED_CREDENTIALS_FILE"]; ok {
		t.Fatal("did not expect AWS_SHARED_CREDENTIALS_FILE in shared session env")
	}
	if _, ok := env["AWS_CONFIG_FILE"]; ok {
		t.Fatal("did not expect AWS_CONFIG_FILE in shared session env")
	}
}

// withLocalAWSProfiles stubs the local ~/.aws profile lookup so tests never depend
// on the machine's real AWS configuration.
func withLocalAWSProfiles(t *testing.T, names []string) {
	t.Helper()
	prev := localAWSProfileNames
	localAWSProfileNames = func() ([]string, error) { return names, nil }
	t.Cleanup(func() { localAWSProfileNames = prev })
}

func TestSharedSessionAWSEnvDropsLegacyScopedProfile(t *testing.T) {
	withLocalAWSProfiles(t, []string{"prod", "dev"})

	env := SharedSessionAWSEnv("aiman-58f485ff", "eu-west-1")
	if got, ok := env["AWS_PROFILE"]; ok {
		t.Fatalf("legacy aiman-* profile must not be exported, got AWS_PROFILE=%q", got)
	}
	if got := env["AWS_REGION"]; got != "eu-west-1" {
		t.Fatalf("region must survive profile sanitisation, got %q", got)
	}
}

func TestSharedSessionAWSEnvDropsUnknownProfile(t *testing.T) {
	withLocalAWSProfiles(t, []string{"prod", "dev"})

	env := SharedSessionAWSEnv("staging", "eu-west-1")
	if got, ok := env["AWS_PROFILE"]; ok {
		t.Fatalf("profile absent from ~/.aws must not be exported, got AWS_PROFILE=%q", got)
	}
}

func TestSharedSessionAWSEnvKeepsProfileWhenLookupFails(t *testing.T) {
	prev := localAWSProfileNames
	localAWSProfileNames = func() ([]string, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { localAWSProfileNames = prev })

	env := SharedSessionAWSEnv("prod", "eu-west-1")
	if got := env["AWS_PROFILE"]; got != "prod" {
		t.Fatalf("expected AWS_PROFILE=prod when the profile list is unavailable, got %q", got)
	}
}

func TestSanitizeSessionAWSProfile(t *testing.T) {
	known := []string{"default", "dev", "prod"}
	cases := []struct {
		name    string
		profile string
		known   []string
		want    string
	}{
		{"known profile passes through", "prod", known, "prod"},
		{"whitespace trimmed", "  dev  ", known, "dev"},
		{"empty stays empty", "", known, ""},
		{"legacy scoped profile dropped", "aiman-58f485ff", known, ""},
		{"legacy scoped profile dropped even if present locally", "aiman-58f485ff", []string{"aiman-58f485ff"}, ""},
		{"unknown profile dropped", "staging", known, ""},
		{"unknown profile kept when list is empty", "staging", nil, "staging"},
		{"legacy profile dropped when list is empty", "aiman-58f485ff", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeSessionAWSProfile(tc.profile, tc.known); got != tc.want {
				t.Fatalf("SanitizeSessionAWSProfile(%q, %v) = %q, want %q", tc.profile, tc.known, got, tc.want)
			}
		})
	}
}

func TestSharedSessionAWSEnvNoProfile(t *testing.T) {
	env := SharedSessionAWSEnv("", "eu-west-1")
	if _, ok := env["AWS_PROFILE"]; ok {
		t.Fatal("did not expect AWS_PROFILE when profile name is empty")
	}
}

// rootOnlyRemote answers GetRoot and nothing else; any other call panics on
// the nil embedded interface, which proves a code path stayed off the remote.
type rootOnlyRemote struct {
	domain.RemoteExecutor
	root string
}

func (r rootOnlyRemote) GetRoot() string { return r.root }

func fakeRoot(dir string) domain.RemoteExecutor { return rootOnlyRemote{root: dir} }

func TestSessionBaseBranch(t *testing.T) {
	cases := []struct{ configured, detected, want string }{
		{"", "origin/main", "origin/main"},
		{"dev", "origin/main", "dev"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := sessionBaseBranch(c.configured, c.detected); got != c.want {
			t.Errorf("sessionBaseBranch(%q, %q) = %q, want %q", c.configured, c.detected, got, c.want)
		}
	}
}

func TestResolveWorktreeAdHoc(t *testing.T) {
	wt, err := (&FlowManager{}).resolveWorktree(context.Background(), fakeRoot("/r"), domain.SessionConfig{AdHoc: true}, "adhoc-x")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Path != "/r/adhoc-x" {
		t.Fatalf("Path = %q, want /r/adhoc-x", wt.Path)
	}
}

func TestShouldBootstrap(t *testing.T) {
	repo := domain.Repo{Name: "owner/name"}
	cases := []struct {
		name string
		cfg  domain.SessionConfig
		want bool
	}{
		{"new worktree", domain.SessionConfig{Repo: repo}, true},
		{"existing branch", domain.SessionConfig{Repo: repo, ExistingBranch: true}, true},
		{"attach existing", domain.SessionConfig{Repo: repo, AttachExisting: true}, false},
		{"reuse workspace", domain.SessionConfig{Repo: repo, ReuseWorkspace: true}, false},
		{"ad hoc", domain.SessionConfig{Repo: repo, AdHoc: true}, false},
		{"prepared worktree", domain.SessionConfig{Repo: repo, PreparedWorktree: "/w/review"}, false},
		{"no repository", domain.SessionConfig{Repo: domain.Repo{Name: "No Repository"}}, false},
		{"empty repo", domain.SessionConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldBootstrap(tc.cfg); got != tc.want {
				t.Fatalf("shouldBootstrap = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBootstrapPromptNote(t *testing.T) {
	got := bootstrapPromptNote("make deps", "/r/.git/worktrees/x/aiman-setup.log")
	want := "Worktree setup (`make deps`) is running in the background; its log is /r/.git/worktrees/x/aiman-setup.log. Check it has finished before building or running tests."
	if got != want {
		t.Fatalf("bootstrapPromptNote =\n%q\nwant\n%q", got, want)
	}
}
