package cgroup

import "testing"

func TestOverrides(t *testing.T) {
	o := &Overrides{Dir: t.TempDir()}
	if got := o.Get("s1"); got != (Limits{}) {
		t.Fatalf("empty store gave %+v", got)
	}
	if _, err := o.Set("s1", Limits{MemoryHigh: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	got, err := o.Set("s1", Limits{MemoryMax: 16 << 30})
	if err != nil || got != (Limits{MemoryHigh: 12 << 30, MemoryMax: 16 << 30}) {
		t.Fatalf("second Set = %+v, %v", got, err)
	}
	if o.Get("s1") != got {
		t.Fatalf("Get = %+v", o.Get("s1"))
	}
	o.Remove("s1")
	if o.Get("s1") != (Limits{}) {
		t.Fatal("Remove left the override")
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if _, err := o.Set(bad, Limits{MemoryHigh: 1}); err == nil {
			t.Errorf("Set(%q) was accepted", bad)
		}
	}
	var none *Overrides
	if none.Get("s1") != (Limits{}) {
		t.Fatal("nil store")
	}
}
