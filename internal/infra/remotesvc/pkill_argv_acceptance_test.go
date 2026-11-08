//go:build acceptance

package remotesvc

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestServePkillSparesHostUpdateRunner pins that the serve pkill pattern, which
// leftoverCleanup runs while the host update script restarts serve, cannot
// match the detached runner that is running that very script.
func TestServePkillSparesHostUpdateRunner(t *testing.T) {
	var pats []string
	for _, line := range strings.Split(leftoverCleanup(KindServe), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pkill") {
			continue
		}
		start, end := strings.Index(line, "'"), strings.LastIndex(line, "'")
		if start < 0 || end <= start {
			t.Fatalf("cannot read pkill pattern from %q", line)
		}
		pats = append(pats, line[start+1:end])
	}
	if len(pats) == 0 {
		t.Fatal("leftoverCleanup(KindServe) has no pkill line")
	}
	if !slices.Contains(pats, KindServe.procPattern()) {
		t.Fatalf("leftoverCleanup pkill patterns %q do not include procPattern %q", pats, KindServe.procPattern())
	}
	runner := []string{
		"bash /home/x/.aiman/tmp/host-update-v0.35.0.wrapper.sh",
		"nohup bash /home/x/.aiman/tmp/host-update-v0.35.0.wrapper.sh",
		"bash /home/x/.aiman/tmp/host-update-v0.35.0.sh",
	}
	serve := []string{
		"aiman serve",
		"/home/x/.local/bin/aiman serve",
	}
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("pkill pattern %q: %v", p, err)
		}
		for _, cmd := range runner {
			if re.MatchString(cmd) {
				t.Errorf("pkill pattern %q matches the update runner %q", p, cmd)
			}
		}
		for _, cmd := range serve {
			if !re.MatchString(cmd) {
				t.Errorf("pkill pattern %q does not match serve %q", p, cmd)
			}
		}
	}
}
