package domain

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	GroupUngrouped = "ungrouped"
	GroupQuick     = "quick"
)

var sessionNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)

func ValidateSessionName(name string) error {
	if !sessionNameRe.MatchString(name) {
		return fmt.Errorf("invalid session name %q: must match %s", name, sessionNameRe.String())
	}
	return nil
}

// GroupLabel is the sidebar bucket for a session: empty persists as ungrouped.
func GroupLabel(group string) string {
	g := strings.TrimSpace(group)
	if g == "" {
		return GroupUngrouped
	}
	return g
}

// NormalizeGroupName hyphenates spaces and accepts ungrouped. Empty becomes ungrouped.
func NormalizeGroupName(name string) (string, error) {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), "-")
	if name == "" || strings.EqualFold(name, GroupUngrouped) {
		return GroupUngrouped, nil
	}
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	return name, nil
}

func NameTaken(existing []Session, name string) bool {
	want := strings.ToLower(name)
	for _, s := range existing {
		if strings.ToLower(s.Name) == want {
			return true
		}
	}
	return false
}

// AssignSessionName picks a unique name. Quick mode uses q1, q2, … and
// ignores preferred. Otherwise impl is tried first, then preferred, then
// suffixed variants.
func AssignSessionName(existing []Session, preferred string, quick bool) (string, error) {
	if quick {
		return nextQuickName(existing), nil
	}
	if !NameTaken(existing, "impl") {
		return "impl", nil
	}
	if preferred != "" {
		if err := ValidateSessionName(preferred); err == nil && !NameTaken(existing, preferred) {
			return preferred, nil
		}
		if err := ValidateSessionName(preferred); err == nil {
			return nextSuffixed(existing, preferred), nil
		}
	}
	return nextSuffixed(existing, "impl"), nil
}

func AssignSessionGroup(explicit, issueKey, repoName string, quick bool) string {
	if quick {
		return GroupQuick
	}
	if g := strings.TrimSpace(explicit); g != "" {
		return g
	}
	if issueKey != "" {
		return issueKey
	}
	if repoName != "" {
		if i := strings.LastIndex(repoName, "/"); i >= 0 && i+1 < len(repoName) {
			return repoName[i+1:]
		}
		return repoName
	}
	return GroupUngrouped
}

func nextQuickName(existing []Session) string {
	for n := 1; ; n++ {
		cand := fmt.Sprintf("q%d", n)
		if !NameTaken(existing, cand) {
			return cand
		}
	}
}

func nextSuffixed(existing []Session, base string) string {
	if err := ValidateSessionName(base); err == nil && !NameTaken(existing, base) {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		if err := ValidateSessionName(cand); err != nil {
			cand = fmt.Sprintf("s%d", n)
		}
		if !NameTaken(existing, cand) {
			return cand
		}
	}
}
