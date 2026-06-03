package awsdelegation

import (
	"fmt"
	"strings"
)

// ProfileSectionHeader returns the INI section header AWS CLI expects for a named profile.
func ProfileSectionHeader(profileName string) string {
	p := strings.TrimSpace(profileName)
	if p == "default" {
		return "[default]"
	}
	return fmt.Sprintf("[profile %s]", p)
}

// FormatProfileSection returns the body lines (no section header) for a delegated-role profile.
// region is optional; when non-empty it is written as "region = <value>".
func FormatProfileSection(roleARN, sourceProfile, region string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("role_arn = %s\n", strings.TrimSpace(roleARN)))
	if s := strings.TrimSpace(sourceProfile); s != "" {
		b.WriteString(fmt.Sprintf("source_profile = %s\n", s))
	}
	if r := strings.TrimSpace(region); r != "" {
		b.WriteString(fmt.Sprintf("region = %s\n", r))
	}
	return strings.TrimRight(b.String(), "\n")
}

// MergeProfileIntoConfig replaces or inserts a [profile NAME] block in an AWS shared config file.
// When roleARN is empty but region is set, it writes a region-only profile block so sync-mode
// callers can manage the shared default region without a role_arn/source_profile stanza.
// If both roleARN and region are empty, it removes an existing block for that profile only
// (other content is preserved). Trailing newline is ensured.
func MergeProfileIntoConfig(existing string, profileName, roleARN, sourceProfile, region string) string {
	name := strings.TrimSpace(profileName)
	header := ProfileSectionHeader(name)
	return mergeSection(existing, name, header, aimanHeader, func() string {
		if strings.TrimSpace(roleARN) == "" {
			if r := strings.TrimSpace(region); r != "" {
				return fmt.Sprintf("region = %s", r)
			}
			return ""
		}
		return FormatProfileSection(roleARN, sourceProfile, region)
	})
}

// MergeCredentialsIntoConfig replaces or inserts a [NAME] block in an AWS shared credentials file.
// If creds is nil, it removes an existing block for that profile name.
func MergeCredentialsIntoConfig(existing string, profileName string, creds *SessionCredentials) string {
	name := strings.TrimSpace(profileName)
	if name == "" {
		name = "default"
	}
	header := fmt.Sprintf("[%s]", name)
	return mergeSection(existing, name, header, aimanHeader, func() string {
		return FormatCredentialsSection(name, creds)
	})
}

func mergeSection(existing, name, header, topHeader string, bodyFn func() string) string {
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	without := stripSection(existing, header)

	body := bodyFn()
	if body == "" || name == "" {
		return finalizeConfig(without)
	}

	var block string
	if strings.HasPrefix(body, "[") {
		block = body + "\n"
	} else {
		block = header + "\n" + body + "\n"
	}

	if strings.TrimSpace(without) == "" {
		return finalizeConfig(topHeader + block)
	}
	if !strings.HasSuffix(strings.TrimRight(without, "\n"), "\n") {
		without += "\n"
	}
	return finalizeConfig(without + "\n" + block)
}

func renameSection(existing, oldHeader, newHeader, newName string) (string, error) {
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	if strings.EqualFold(strings.TrimSpace(oldHeader), strings.TrimSpace(newHeader)) {
		return existing, nil
	}
	if hasSection(existing, newHeader) {
		return "", fmt.Errorf("profile %q already exists", strings.TrimSpace(newName))
	}

	body, found := extractSectionBody(existing, oldHeader)
	if !found {
		return existing, nil
	}

	without := stripSection(existing, oldHeader)
	block := newHeader + "\n"
	if strings.TrimRight(body, "\n") != "" {
		block += strings.TrimRight(body, "\n") + "\n"
	}

	if strings.TrimSpace(without) == "" {
		return block, nil
	}
	if !strings.HasSuffix(strings.TrimRight(without, "\n"), "\n") {
		without += "\n"
	}
	return without + "\n" + block, nil
}

const aimanHeader = "# aiman: delegated profile below\n"

func finalizeConfig(s string) string {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// stripSection removes the section with the given header (until the next [ or EOF).
func stripSection(content, header string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.EqualFold(line, header) {
			for i++; i < len(lines); i++ {
				next := strings.TrimSpace(lines[i])
				if strings.HasPrefix(next, "[") {
					i--
					break
				}
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

func hasSection(content, header string) bool {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), header) {
			return true
		}
	}
	return false
}

func extractSectionBody(content, header string) (string, bool) {
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.EqualFold(strings.TrimSpace(lines[i]), header) {
			continue
		}
		start := i + 1
		end := len(lines)
		for j := start; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				end = j
				break
			}
		}
		return strings.Join(lines[start:end], "\n"), true
	}
	return "", false
}
