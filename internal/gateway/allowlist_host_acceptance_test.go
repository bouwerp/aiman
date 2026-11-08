package gateway

import (
	"slices"
	"testing"
)

// host.version only reads; host.update restarts serve and the gateway, so it
// needs allow_destructive.
func TestDeviceHostUpdateMethods(t *testing.T) {
	if !slices.Contains(deviceReadMethods, "host.version") {
		t.Error("host.version should be a device read method")
	}
	if !slices.Contains(deviceDestructiveMethods, "host.update") {
		t.Error("host.update should be a device destructive method")
	}
	if slices.Contains(deviceReadMethods, "host.update") || slices.Contains(deviceWriteMethods, "host.update") {
		t.Error("host.update must be destructive only")
	}
	tests := []struct {
		method      string
		destructive bool
		want        bool
	}{
		{"host.version", false, true},
		{"host.update", false, false},
		{"host.update", true, true},
	}
	for _, tt := range tests {
		if got := deviceMayCall(tt.method, tt.destructive); got != tt.want {
			t.Errorf("deviceMayCall(%q, %v) = %v, want %v", tt.method, tt.destructive, got, tt.want)
		}
	}
}
