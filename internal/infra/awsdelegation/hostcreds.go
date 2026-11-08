package awsdelegation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrUnavailable means the host credentials file exists but cannot be read.
	ErrUnavailable = errors.New("aws credentials file unavailable")
	// ErrCredentialsWrite means the host credentials file could not be replaced.
	ErrCredentialsWrite = errors.New("aws credentials file not written")
	// ErrInvalidCredentials means the profile name or credentials would not produce
	// a well-formed section. The error never says which value was at fault.
	ErrInvalidCredentials = errors.New("invalid aws profile or credentials")
)

// HostProfile is what the host credentials file says about one section. It never
// carries key values.
type HostProfile struct {
	Name      string
	ExpiresAt time.Time
	HasExpiry bool
	ModTime   time.Time
}

// ReadHostCredentialProfiles parses the credentials file at path. A missing file is
// an empty list. Errors name the path at most, never the file's content.
func ReadHostCredentialProfiles(path string) ([]HostProfile, error) {
	// Stat first: opening a FIFO in this position would block serve forever.
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []HostProfile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: not a regular file", ErrUnavailable, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	profiles := parseHostProfiles(string(body))
	for i := range profiles {
		profiles[i].ModTime = st.ModTime()
	}
	return profiles, nil
}

// parseHostProfiles lists the sections in file order. Only the expiry key's value is
// ever looked at, so no key value can reach a field.
func parseHostProfiles(content string) []HostProfile {
	profiles := []HostProfile{}
	index := map[string]int{}
	current := -1
	for raw := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if name, ok := sectionName(line); ok {
			i, seen := index[name]
			if !seen {
				i = len(profiles)
				index[name] = i
				profiles = append(profiles, HostProfile{Name: name})
			}
			current = i
			continue
		}
		if current >= 0 {
			applyExpiryLine(&profiles[current], line)
		}
	}
	return profiles
}

// sectionName returns the profile a line opens. The match is exact from here on,
// because AWS profile names are case-sensitive.
func sectionName(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return "", false
	}
	return strings.TrimSpace(line[1 : len(line)-1]), true
}

// applyExpiryLine records the expiry when line is a parseable expiry key. A value
// that does not parse leaves the profile without one, as ParseCredentialExpirations does.
func applyExpiryLine(p *HostProfile, line string) {
	key, value, found := strings.Cut(line, "=")
	if !found || !strings.EqualFold(strings.TrimSpace(key), ExpiryKey) {
		return
	}
	exp, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return
	}
	p.ExpiresAt = exp.UTC()
	p.HasExpiry = true
}

// WriteHostCredentials puts one profile's section into the credentials file at path,
// under an exclusive lock on lockPath. The file is replaced by rename, mode 0600. A
// file that exists but cannot be read is left alone, because writing without its
// content would lose every other section.
//
// It splices rather than calling MergeCredentialsIntoConfig: this file belongs to the
// operator and its neighbours must survive byte for byte, while that helper folds
// case (deleting [Phone] when writing phone), normalises line endings, drops a comment
// above the next section and adds blank lines on each rewrite.
func WriteHostCredentials(path, lockPath, profile string, creds *SessionCredentials) error {
	if err := validateHostSection(profile, creds); err != nil {
		return err
	}
	unlock, err := lockHostCredentials(lockPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCredentialsWrite, err)
	}
	defer unlock()

	target := resolveCredentialsPath(path)
	existing, err := readExistingCredentials(target)
	if err != nil {
		return err
	}
	merged := spliceCredentialsSection(existing, profile, FormatCredentialsSection(profile, creds))
	if err := replaceCredentialsFile(target, merged); err != nil {
		return fmt.Errorf("%w: %w", ErrCredentialsWrite, err)
	}
	return nil
}

// validateHostSection refuses what the merge would turn into damage: nil credentials
// delete the section, and a line break or bracket in a value writes extra lines or
// sections into the file.
func validateHostSection(profile string, creds *SessionCredentials) error {
	if creds == nil || creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return ErrInvalidCredentials
	}
	if profile == "" || profile != strings.TrimSpace(profile) || strings.ContainsAny(profile, "[]\r\n") {
		return ErrInvalidCredentials
	}
	for _, v := range []string{creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken} {
		if strings.ContainsAny(v, "\r\n") {
			return ErrInvalidCredentials
		}
	}
	return nil
}

// resolveCredentialsPath follows a symlinked credentials file, so a dotfile manager's
// link keeps pointing at the data. Anything it cannot resolve is written at path.
func resolveCredentialsPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// readExistingCredentials returns the current content, empty for a missing file. It
// stats first for the same reason ReadHostCredentialProfiles does.
func readExistingCredentials(path string) (string, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s: not a regular file", ErrUnavailable, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return string(body), nil
}

// spliceCredentialsSection replaces profile's section in place, or appends block when
// the file has none. Every other line is copied as raw bytes. Repeats of the section
// are removed, because a later repeat would override the fresh keys.
func spliceCredentialsSection(existing, profile, block string) string {
	if strings.TrimSpace(existing) == "" {
		return block
	}
	if strings.Contains(existing, "\r\n") {
		block = strings.ReplaceAll(block, "\n", "\r\n")
	}
	lines := strings.SplitAfter(existing, "\n")
	var out strings.Builder
	placed := false
	for i := 0; i < len(lines); {
		if name, ok := sectionName(lines[i]); !ok || name != profile {
			out.WriteString(lines[i])
			i++
			continue
		}
		if !placed {
			out.WriteString(block)
			placed = true
		}
		i = sectionEnd(lines, i+1)
	}
	if placed {
		return out.String()
	}
	return appendCredentialsBlock(existing, block)
}

// removeCredentialsSection drops every section whose header names profile exactly
// (case preserved) and copies every other byte. Without such a section it returns
// existing unchanged.
func removeCredentialsSection(existing, profile string) string {
	lines := strings.SplitAfter(existing, "\n")
	var out strings.Builder
	removed := false
	for i := 0; i < len(lines); {
		if name, ok := sectionName(lines[i]); !ok || name != profile {
			out.WriteString(lines[i])
			i++
			continue
		}
		removed = true
		i = sectionEnd(lines, i+1)
	}
	if !removed {
		return existing
	}
	return out.String()
}

// sectionEnd returns where the section whose body starts at start stops: before the
// blank and comment lines leading up to the next header, since those sit on that
// header rather than on the section being replaced.
func sectionEnd(lines []string, start int) int {
	end := start
	for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
		end++
	}
	for end > start && isBlankOrComment(lines[end-1]) {
		end--
	}
	return end
}

func isBlankOrComment(line string) bool {
	line = strings.TrimSpace(line)
	return line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";")
}

// appendCredentialsBlock adds block after one blank line, using the file's line ending.
func appendCredentialsBlock(existing, block string) string {
	eol := "\n"
	if strings.Contains(existing, "\r\n") {
		eol = "\r\n"
	}
	if !strings.HasSuffix(existing, "\n") {
		existing += eol
	}
	return existing + eol + block
}

// replaceCredentialsFile writes content to a temp file beside path and renames it
// over path. CreateTemp makes the file at most 0600 before any content goes in; the
// chmod holds the final mode at exactly 0600 whatever the umask took away.
func replaceCredentialsFile(path, content string) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
