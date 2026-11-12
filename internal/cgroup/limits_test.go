package cgroup

import (
	"reflect"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{"", 0, false},
		{"6G", 6 << 30, false},
		{"512m", 512 << 20, false},
		{"1.5GiB", 3 << 29, false},
		{"10KB", 10 << 10, false},
		{"2T", 2 << 40, false},
		{"4096", 4096, false},
		{"infinity", Infinity, false},
		{"max", Infinity, false},
		{"0", 0, true},
		{"-1G", 0, true},
		{"six", 0, true},
		{"6X", 0, true},
		{"G", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for in, want := range map[int64]string{
		6 << 30: "6G", 3 << 29: "1.5G", 512 << 20: "512M", 100: "100", 0: "0", Infinity: "infinity",
	} {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLimitsProperties(t *testing.T) {
	l := Limits{MemoryHigh: 6 << 30, MemoryMax: Infinity, CPUWeight: 100, TasksMax: 4096}
	want := []string{"MemoryHigh=6442450944", "MemoryMax=infinity", "CPUWeight=100", "TasksMax=4096"}
	if got := l.Properties(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Properties = %v, want %v", got, want)
	}
	if got := (Limits{}).Properties(); len(got) != 0 {
		t.Fatalf("zero limits gave %v", got)
	}
}

func TestLimitsMerge(t *testing.T) {
	base := Limits{MemoryHigh: 6 << 30, MemoryMax: 10 << 30, CPUWeight: 100}
	got := base.Merge(Limits{MemoryHigh: 12 << 30})
	if got.MemoryHigh != 12<<30 || got.MemoryMax != 12<<30 || got.CPUWeight != 100 {
		t.Fatalf("Merge raised high without lifting max: %+v", got)
	}
	got = base.Merge(Limits{MemoryMax: Infinity})
	if got.MemoryMax != Infinity || got.MemoryHigh != 6<<30 {
		t.Fatalf("Merge infinity max: %+v", got)
	}
}
