package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	GroupUngrouped = "ungrouped"
)

var groupNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)

const maxSessionNameRunes = 120

func ValidateSessionName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid session name %q: must not be empty or padded", name)
	}
	if utf8.RuneCountInString(name) > maxSessionNameRunes {
		return fmt.Errorf("invalid session name %q: exceeds %d characters", name, maxSessionNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid session name %q: contains a control character", name)
		}
	}
	return nil
}

// ValidateGroupName accepts the identifier format used for sidebar groups.
func ValidateGroupName(name string) error {
	if !groupNameRe.MatchString(name) {
		return fmt.Errorf("invalid group name %q: must match %s", name, groupNameRe.String())
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

// ContextGroup is the group a session's shared-context notes live under: its
// group, or empty for ungrouped. Ungrouped holds unrelated sessions, so it is
// never a shared namespace.
func ContextGroup(group string) string {
	g := strings.TrimSpace(group)
	if strings.EqualFold(g, GroupUngrouped) {
		return ""
	}
	return g
}

// NormalizeGroupName hyphenates spaces and accepts ungrouped. Empty becomes ungrouped.
func NormalizeGroupName(name string) (string, error) {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), "-")
	if name == "" || strings.EqualFold(name, GroupUngrouped) {
		return GroupUngrouped, nil
	}
	if err := ValidateGroupName(name); err != nil {
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

// AssignSessionName picks a unique name. Quick mode uses q1, q2, … and ignores
// preferred. Otherwise it is preferred, normally the branch, suffixed if taken.
func AssignSessionName(existing []Session, preferred string, quick bool) (string, error) {
	if quick {
		return nextQuickName(existing), nil
	}
	if ValidateSessionName(preferred) == nil {
		return nextSuffixed(existing, preferred), nil
	}
	return nextSuffixed(existing, "session"), nil
}

// AssignSessionGroup returns the group a new session starts in: the one the
// creator named, else ungrouped. The user files sessions by moving them.
func AssignSessionGroup(explicit string) string {
	if g := strings.TrimSpace(explicit); g != "" {
		return g
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
