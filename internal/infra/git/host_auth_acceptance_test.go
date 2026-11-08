package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	gitDenialOutput = "git@github.com: Permission denied (publickey).\r\n" +
		"fatal: Could not read from remote repository.\n\n" +
		"Please make sure you have the correct access rights\nand the repository exists."
	hostAuthPrefix = "the host cannot authenticate to GitHub: give it its own credentials"
)

// sshShapedError builds the error ssh.Manager.Execute returns for a failed
// remote command.
func sshShapedError(output string) error {
	return fmt.Errorf("remote command failed on u@h: %w\nOutput: %s", errors.New("exit status 128"), output)
}

// F2: an error that is not a git auth denial passes through untouched.
func TestWrapRemoteErrorLeavesPlainErrors(t *testing.T) {
	for _, plain := range []error{
		errors.New("disk full"),
		sshShapedError("fatal: couldn't find remote ref feature-x"),
		sshShapedError("u@h: Permission denied (publickey,password)."),
		// A missing repository prints the fatal line with no denial.
		sshShapedError("fatal: Could not read from remote repository."),
		nil,
	} {
		got := WrapRemoteError(plain)
		if got != plain { //nolint:errorlint // identity is the contract
			t.Errorf("WrapRemoteError(%q) = %v, want the same error unchanged", plain, got)
		}
		if errors.Is(got, ErrHostAuthDenied) {
			t.Errorf("WrapRemoteError(%q) should not be ErrHostAuthDenied", plain)
		}
	}
}

// F2: a git auth denial becomes ErrHostAuthDenied with the operator's fix
// first, and still wraps the original error.
func TestWrapRemoteErrorMarksGitDenial(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "git denial", err: sshShapedError(gitDenialOutput)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WrapRemoteError(tc.err)
			if got == nil {
				t.Fatal("WrapRemoteError returned nil for a denial")
			}
			if !errors.Is(got, ErrHostAuthDenied) {
				t.Errorf("want errors.Is(err, ErrHostAuthDenied), got %v", got)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("want the result to wrap the original error, got %v", got)
			}
			if !strings.Contains(got.Error(), "give it its own credentials") {
				t.Errorf("message should contain %q, got %q", "give it its own credentials", got.Error())
			}
			if !strings.HasPrefix(got.Error(), hostAuthPrefix) {
				t.Errorf("message should start with %q, got %q", hostAuthPrefix, got.Error())
			}
		})
	}
}

// fetchDeniedRemote is a healthy existing repository whose git fetch is
// refused by GitHub.
type fetchDeniedRemote struct {
	mockRemote
	fetches int
}

func (r *fetchDeniedRemote) Execute(ctx context.Context, cmd string) (string, error) {
	if strings.Contains(cmd, " fetch ") || strings.HasSuffix(cmd, " fetch") {
		r.fetches++
		return gitDenialOutput, sshShapedError(gitDenialOutput)
	}
	return r.mockRemote.Execute(ctx, cmd)
}

// F2: SetupRemoteWorktree reports a host that cannot reach GitHub as
// ErrHostAuthDenied.
func TestSetupRemoteWorktreeFetchDeniedIsHostAuth(t *testing.T) {
	const url = "git@github.com:owner/repo.git"
	remote := &fetchDeniedRemote{mockRemote: mockRemote{
		root:    "/r",
		dirs:    map[string]bool{"/r/repo": true},
		outputs: map[string]string{"git -C '/r/repo' remote get-url origin 2>/dev/null": url},
	}}
	_, err := NewManager(nil).SetupRemoteWorktree(context.Background(), remote,
		domain.Repo{Name: "owner/repo", URL: url}, "feature-x", "")
	if remote.fetches == 0 {
		t.Fatalf("the fake remote never saw a git fetch; err = %v", err)
	}
	if err == nil {
		t.Fatal("want an error when the fetch is denied, got nil")
	}
	if !errors.Is(err, ErrHostAuthDenied) {
		t.Fatalf("want errors.Is(err, ErrHostAuthDenied), got %v", err)
	}
}
