package remotesvc

import (
	"regexp"
	"strconv"
)

// semverRe finds the first vMAJOR.MINOR.PATCH in a string. The probe reports a
// whole version line ("aiman v0.19.10 (built 2026-08-26T09:32:04Z)"), and
// `aiman --version` is free to change its wording around the number.
var semverRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// Version is a parsed release number.
type Version struct{ Major, Minor, Patch int }

// ParseVersion pulls a release number out of arbitrary version text. It reports
// false for anything without one — "dev", "missing", an empty probe — which
// callers must treat as "do not compare", never as zero.
func ParseVersion(s string) (Version, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	patch, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return Version{}, false
	}
	return Version{Major: major, Minor: minor, Patch: patch}, true
}

// Less reports whether v is an earlier release than other.
func (v Version) Less(other Version) bool {
	switch {
	case v.Major != other.Major:
		return v.Major < other.Major
	case v.Minor != other.Minor:
		return v.Minor < other.Minor
	default:
		return v.Patch < other.Patch
	}
}

// String renders the version as it is written.
func (v Version) String() string {
	return "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Outdated reports whether a remote is running an older release than this
// client, and is therefore worth updating.
//
// Deliberately conservative: it answers false unless both sides carry a real
// release number and the remote's is strictly older. That rules out the cases
// where updating would be wrong or meaningless —
//
//   - a remote that is *newer* than the client (never downgrade someone else's
//     host to match an older laptop),
//   - a locally-built client ("dev"), which has no release to offer,
//   - a remote with no aiman installed, where the action is an install, not an
//     update,
//   - a probe that returned nothing.
func Outdated(remoteVersion, localVersion string) (Version, Version, bool) {
	remote, rok := ParseVersion(remoteVersion)
	local, lok := ParseVersion(localVersion)
	if !rok || !lok {
		return remote, local, false
	}
	return remote, local, remote.Less(local)
}
