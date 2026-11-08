//go:build acceptance

package ssh

import (
	"context"
	"strings"
	"testing"
)

const (
	gitDenialText = "git@github.com: Permission denied (publickey).\r\n" +
		"fatal: Could not read from remote repository.\n\n" +
		"Please make sure you have the correct access rights\nand the repository exists."
	sshClientDenialText = "u@h: Permission denied (publickey,password)."
)

// F2: a denial the remote git printed is not a dropped transport; the SSH
// client's own denial still is.
func TestRemoteGitAuthDenialClassification(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantDenial    bool
		wantRetriable bool
	}{
		{name: "git denial with fatal line", text: gitDenialText, wantDenial: true},
		{name: "git denial alone", text: "git@github.com: Permission denied (publickey).", wantDenial: true},
		{name: "git denial other host", text: "git@ghe.example.com: Permission denied (publickey).", wantDenial: true},
		// A missing repository prints the fatal line with no denial.
		{name: "git fatal alone", text: "fatal: Could not read from remote repository."},
		{
			name:       "git denial inside Execute's error",
			text:       "remote command failed on u@h: exit status 128\nOutput: " + gitDenialText,
			wantDenial: true,
		},
		{name: "ssh client denial", text: sshClientDenialText, wantRetriable: true},
		{
			name:          "ssh client denial inside Execute's error",
			text:          "remote command failed on u@h: exit status 255\nOutput: " + sshClientDenialText,
			wantRetriable: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRemoteGitAuthDenial(tc.text); got != tc.wantDenial {
				t.Errorf("IsRemoteGitAuthDenial(%q) = %v, want %v", tc.text, got, tc.wantDenial)
			}
			if got := isRetriableSSHTransportError(tc.text); got != tc.wantRetriable {
				t.Errorf("isRetriableSSHTransportError(%q) = %v, want %v", tc.text, got, tc.wantRetriable)
			}
		})
	}
}

// F2: a git denial from the remote command makes Execute call ssh once, with
// no retry and no unmultiplexed fallback, and the error holds git's output.
func TestExecuteDoesNotRetryRemoteGitDenial(t *testing.T) {
	calls := installFakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT", "128")
	t.Setenv("FAKE_SSH_STDERR", gitDenialText)

	_, err := NewManager(Config{Host: "h", User: "u", Root: "/r"}).Execute(context.Background(), "git -C /r/repo fetch origin")
	if err == nil {
		t.Fatal("want an error from a git denial, got nil")
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("want exactly one ssh call, got %d: %q", len(got), got)
	}
	for _, want := range []string{"git@github.com: Permission denied (publickey).", "fatal: Could not read from remote repository."} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should hold the output %q, got %q", want, err.Error())
		}
	}
}
