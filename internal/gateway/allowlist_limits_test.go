package gateway

import (
	"os"
	"strings"
	"testing"
)

// limits.get reports what each provider has left. It stays off device tokens
// until the phone has a reason to read it.
func TestLimitsGetIsOffTheDeviceAllowlist(t *testing.T) {
	raw, err := os.ReadFile("allowlist.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "limits.get") {
		t.Fatal("limits.get is on the device allowlist")
	}
}
