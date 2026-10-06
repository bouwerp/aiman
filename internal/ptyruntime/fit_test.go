package ptyruntime

import "testing"

func TestFitsClient(t *testing.T) {
	if !FitsClient(`"$HOME/.aiman/hooks/deepcode-launch.sh"`) {
		t.Fatal("deep code launcher")
	}
	if FitsClient("muse --trust-workspace --yolo") {
		t.Fatal("muse")
	}
}
