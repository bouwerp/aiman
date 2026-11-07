//go:build acceptance

package gateway

import (
	"slices"
	"testing"
)

// TestDeviceMayCallTranscript: a paired phone reads transcripts with any
// device token. The three methods only read, so they sit in the read tier.
func TestDeviceMayCallTranscript(t *testing.T) {
	for _, m := range []string{"session.transcript", "session.transcript_item", "session.process_output"} {
		if !slices.Contains(deviceReadMethods, m) {
			t.Errorf("%s is not in deviceReadMethods", m)
		}
		if !deviceMayCall(m, false) {
			t.Errorf("a device without allow_destructive may not call %s", m)
		}
		if slices.Contains(deviceWriteMethods, m) || slices.Contains(deviceDestructiveMethods, m) {
			t.Errorf("%s is listed outside the read tier", m)
		}
	}
}
