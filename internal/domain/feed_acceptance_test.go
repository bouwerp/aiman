package domain_test

import (
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFeedOptionEffectiveKind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option domain.FeedOption
		action domain.FeedActionKind
		want   domain.FeedActionKind
	}{
		{"empty takes the action's kind", domain.FeedOption{Label: "a"}, domain.FeedSlackDraft, domain.FeedSlackDraft},
		{"own kind wins", domain.FeedOption{Label: "b", Kind: domain.FeedTodoAdd}, domain.FeedSlackDraft, domain.FeedTodoAdd},
		{"own phase 2 kind wins", domain.FeedOption{Label: "c", Kind: domain.FeedSlackReply}, domain.FeedRead, domain.FeedSlackReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.option.EffectiveKind(tc.action); got != tc.want {
				t.Fatalf("EffectiveKind(%s) = %s, want %s", tc.action, got, tc.want)
			}
		})
	}
}

func TestFeedPhase1KindsAreTheSafeKinds(t *testing.T) {
	phase1 := domain.FeedPhase1Kinds()
	want := []domain.FeedActionKind{domain.FeedSlackDraft, domain.FeedTodoAdd, domain.FeedRead}
	if len(phase1) != len(want) {
		t.Fatalf("FeedPhase1Kinds() = %v, want %v", phase1, want)
	}
	for _, k := range want {
		if !slices.Contains(phase1, k) {
			t.Errorf("FeedPhase1Kinds() = %v, missing %s", phase1, k)
		}
	}
	for _, tc := range []struct {
		kind domain.FeedActionKind
		safe bool
	}{
		{domain.FeedSlackDraft, true},
		{domain.FeedTodoAdd, true},
		{domain.FeedRead, true},
		{domain.FeedSteerSession, false},
		{domain.FeedReviewPR, false},
		{domain.FeedSlackReply, false},
		{domain.FeedStartSession, false},
		{"", false},
		{"rm_rf", false},
	} {
		if got := domain.FeedSafeKind(tc.kind); got != tc.safe {
			t.Errorf("FeedSafeKind(%q) = %v, want %v", tc.kind, got, tc.safe)
		}
	}
}

func TestFeedThreadKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"slack thread reply", domain.FeedThreadKeySlack("C123", "1700000000.000100", "1700000050.000200"), "slack:C123/1700000000.000100"},
		{"slack top-level message", domain.FeedThreadKeySlack("D9", "", "1700000050.000200"), "slack:D9/1700000050.000200"},
		{"github pull request", domain.FeedThreadKeyGitHub("bouwerp/aiman", 217, "998877"), "gh:bouwerp/aiman#217"},
		{"github without a number", domain.FeedThreadKeyGitHub("bouwerp/aiman", 0, "998877"), "gh:998877"},
		{"github without a repo", domain.FeedThreadKeyGitHub("", 5, "998877"), "gh:998877"},
		{"aiman session", domain.FeedThreadKeySession("sess-1"), "aiman:sess-1"},
		{"drive file", domain.FeedThreadKeyDrive("1AbC"), "drive:1AbC"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
