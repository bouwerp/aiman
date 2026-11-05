package limits

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func museEnv(t *testing.T, out string, runErr error) Env {
	env := testEnv(t)
	env.Run = func(_ context.Context, name string, args ...string) (string, int, error) {
		if name != "muse" || !reflect.DeepEqual(args, []string{"usage", "--json"}) {
			t.Errorf("ran %s %v", name, args)
		}
		return out, 0, runErr
	}
	return env
}

func TestMuseReadsAllowance(t *testing.T) {
	r := NewMuseSource(museEnv(t, fixture(t, "muse-usage.json"), nil)).Read(context.Background())
	if r.State != StateOK || r.Plan != "Muse Code High Usage" {
		t.Fatalf("reading %+v", r)
	}
	if len(r.Windows) != 2 || r.Windows[0].Label != "5h" || r.Windows[1].Label != "week" || r.Windows[1].UsedPercent != 30 {
		t.Fatalf("windows %+v", r.Windows)
	}
	if !r.Windows[1].ResetsAt.Equal(time.UnixMilli(1794182400000)) || !r.ObservedAt.Equal(time.UnixMilli(1793912799911)) {
		t.Fatalf("times %+v observed %v", r.Windows[1], r.ObservedAt)
	}
}

func TestMuseNullAllowanceIsUnknownWithItsHint(t *testing.T) {
	r := NewMuseSource(museEnv(t, fixture(t, "muse-null.json"), nil)).Read(context.Background())
	if r.State != StateUnknown || r.Reason == "" || len(r.Windows) != 0 {
		t.Fatalf("reading %+v", r)
	}
}

func TestMuseMissingAndGarbled(t *testing.T) {
	if r := NewMuseSource(museEnv(t, "", exec.ErrNotFound)).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("not installed: %+v", r)
	}
	if r := NewMuseSource(museEnv(t, "{nope", nil)).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("garbled: %+v", r)
	}
}
