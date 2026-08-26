package remotesvc

import "testing"

func TestParseVersionFromProbeText(t *testing.T) {
	cases := map[string]Version{
		"aiman v0.19.10 (built 2026-08-26T09:32:04Z)": {0, 19, 10},
		"v1.2.3":                          {1, 2, 3},
		"aiman 0.19.4":                    {0, 19, 4},
		"aiman-trigger v0.20.0 (built x)": {0, 20, 0},
	}
	for in, want := range cases {
		got, ok := ParseVersion(in)
		if !ok {
			t.Errorf("ParseVersion(%q) reported no version", in)
			continue
		}
		if got != want {
			t.Errorf("ParseVersion(%q) = %v, want %v", in, got, want)
		}
	}
	// Anything without a release number must report false, never a zero version:
	// treating "dev" as v0.0.0 would make every remote look newer than the
	// client, or the client look ancient.
	for _, in := range []string{"", "dev", "missing", "aiman dev (built x)", "no numbers here"} {
		if v, ok := ParseVersion(in); ok {
			t.Errorf("ParseVersion(%q) = %v, want no version", in, v)
		}
	}
}

// The patch position is compared numerically, so v0.19.10 is newer than v0.19.9
// rather than sorting before it as a string would.
func TestVersionLessIsNumeric(t *testing.T) {
	older, _ := ParseVersion("v0.19.9")
	newer, _ := ParseVersion("v0.19.10")
	if !older.Less(newer) {
		t.Errorf("%v should be older than %v", older, newer)
	}
	if newer.Less(older) {
		t.Errorf("%v should not be older than %v", newer, older)
	}
	same, _ := ParseVersion("v0.19.10")
	if newer.Less(same) || same.Less(newer) {
		t.Error("equal versions are neither older nor newer")
	}
	// Minor and major dominate the patch.
	a, _ := ParseVersion("v0.20.0")
	if !newer.Less(a) {
		t.Errorf("%v should be older than %v", newer, a)
	}
	b, _ := ParseVersion("v1.0.0")
	if !a.Less(b) {
		t.Errorf("%v should be older than %v", a, b)
	}
}

func TestOutdatedIsConservative(t *testing.T) {
	cases := []struct {
		name          string
		remote, local string
		want          bool
	}{
		{"remote behind", "aiman v0.19.4 (built x)", "v0.19.10", true},
		{"same version", "aiman v0.19.10", "v0.19.10", false},
		// Never downgrade someone's host to match an older laptop.
		{"remote ahead", "aiman v0.20.0", "v0.19.10", false},
		// A locally-built client has no release to offer.
		{"local is a dev build", "aiman v0.19.4", "dev", false},
		// No aiman on the remote is an install, not an update.
		{"remote missing", "missing", "v0.19.10", false},
		{"probe returned nothing", "", "v0.19.10", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, got := Outdated(tc.remote, tc.local); got != tc.want {
				t.Errorf("Outdated(%q, %q) = %v, want %v", tc.remote, tc.local, got, tc.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	v, _ := ParseVersion("aiman v0.19.10 (built x)")
	if v.String() != "v0.19.10" {
		t.Errorf("String() = %q", v.String())
	}
}
