//go:build acceptance

package gateway

import "testing"

// TestDeviceMayCallAWS: reading AWS status and polling a login are reads;
// starting a login mints a host session, so it needs allow_destructive.
func TestDeviceMayCallAWS(t *testing.T) {
	tests := []struct {
		method      string
		destructive bool
		want        bool
	}{
		{"aws.status", false, true},
		{"aws.login_status", false, true},
		{"aws.login_start", false, false},
		{"aws.login_start", true, true},
	}
	for _, tt := range tests {
		if got := deviceMayCall(tt.method, tt.destructive); got != tt.want {
			t.Errorf("deviceMayCall(%q, %v) = %v, want %v", tt.method, tt.destructive, got, tt.want)
		}
	}
	for _, m := range []string{"aws.status", "aws.login_status"} {
		if !deviceAllowed[m] {
			t.Errorf("%s should be a device read method", m)
		}
	}
	if deviceAllowed["aws.login_start"] || !deviceDestructive["aws.login_start"] {
		t.Errorf("aws.login_start must be destructive only")
	}
}
