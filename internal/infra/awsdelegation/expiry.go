package awsdelegation

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ExpiryKey is the credentials-file key that records when a set of temporary tokens
// stops working. It is the de-facto convention used by other credential tools
// (aws-vault, granted); the AWS CLI ignores keys it does not know, so writing it is
// inert for anything that only reads the access key, secret and session token.
const ExpiryKey = "x_security_token_expires"

// credentialsPayloadMarker separates the mtime line from the file body in the single
// remote command ReadCredentialExpirations runs, so one round trip yields both.
const credentialsPayloadMarker = "---aiman-credentials---"

// RemoteCredentialExpiry is what a remote's ~/.aws/credentials tells us about when its
// profiles expire.
type RemoteCredentialExpiry struct {
	// Expirations maps profile name → recorded expiry, for profiles written by a
	// version of aiman that records it.
	Expirations map[string]time.Time
	// FileModTime is the mtime of ~/.aws/credentials, used to approximate the expiry of
	// profiles pushed before aiman started recording it. Zero when the file is absent.
	FileModTime time.Time
}

// For returns the expiry of a profile. approx is true when the value was derived from
// the file mtime plus durationSeconds rather than read from the file. ok is false when
// there is nothing to base an answer on.
func (e RemoteCredentialExpiry) For(profile string, durationSeconds int) (at time.Time, approx bool, ok bool) {
	if exp, found := e.Expirations[profile]; found && !exp.IsZero() {
		return exp, false, true
	}
	if e.FileModTime.IsZero() {
		return time.Time{}, false, false
	}
	d := durationSeconds
	if d <= 0 {
		d = DefaultDurationSeconds
	}
	return e.FileModTime.Add(time.Duration(d) * time.Second), true, true
}

// ReadCredentialExpirations reads the remote ~/.aws/credentials and reports what it says
// about profile expiry. A missing file is not an error: the result is simply empty.
func ReadCredentialExpirations(ctx context.Context, r RemoteRunner) (RemoteCredentialExpiry, error) {
	// One round trip: mtime on the first line (GNU stat, then BSD stat), then the body.
	cmd := fmt.Sprintf(
		`f="$HOME/.aws/credentials"; stat -c %%Y "$f" 2>/dev/null || stat -f %%m "$f" 2>/dev/null || true; `+
			`printf '%%s\n' %q; cat "$f" 2>/dev/null || true`, credentialsPayloadMarker)
	out, err := r.Execute(ctx, cmd)
	if err != nil && !strings.Contains(out, credentialsPayloadMarker) {
		return RemoteCredentialExpiry{}, fmt.Errorf("%w: %v", ErrSSHFailure, err)
	}

	head, body, found := strings.Cut(out, credentialsPayloadMarker)
	if !found {
		// Output we cannot interpret — treat as no information rather than guessing.
		return RemoteCredentialExpiry{}, nil
	}

	info := RemoteCredentialExpiry{Expirations: ParseCredentialExpirations(body)}
	if secs, convErr := strconv.ParseInt(strings.TrimSpace(head), 10, 64); convErr == nil && secs > 0 {
		info.FileModTime = time.Unix(secs, 0).UTC()
	}
	return info, nil
}

// ParseCredentialExpirations extracts profile → expiry pairs from the INI body of an AWS
// shared credentials file. Sections without a parseable expiry key are omitted.
func ParseCredentialExpirations(content string) map[string]time.Time {
	out := map[string]time.Time{}
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), ExpiryKey) {
			continue
		}
		exp, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err != nil {
			continue
		}
		out[section] = exp.UTC()
	}
	return out
}
