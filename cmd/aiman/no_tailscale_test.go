package main

import (
	"os/exec"
	"strings"
	"testing"
)

// cmd/aiman must stay free of the Tailscale module tree. The phone gateway
// embeds tsnet in a separate binary for that reason: a hello-world tsnet
// binary is already larger than aiman and pulls hundreds of modules.
func TestAimanBinaryDoesNotDependOnTailscaleModule(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/bouwerp/aiman/cmd/aiman")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "tailscale.com") {
			t.Errorf("cmd/aiman must not depend on %s", line)
		}
	}
}
