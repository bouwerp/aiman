package harness

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeContainers is RSSReader's Docker.
type fakeContainers struct {
	list      []Container
	stats     map[string]int64
	lists     int
	statCalls int
}

func (f *fakeContainers) HarnessContainers(context.Context) ([]Container, error) {
	f.lists++
	return append([]Container(nil), f.list...), nil
}

func (f *fakeContainers) ContainerMemory(_ context.Context, ids []string) (map[string]int64, error) {
	f.statCalls++
	out := map[string]int64{}
	for _, id := range ids {
		if v, ok := f.stats[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// writeCgroup makes a cgroup directory holding anon+shmem bytes and a larger
// memory.current, so a reader that counted page cache would be caught.
func writeCgroup(t *testing.T, dir string, anon, shmem int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": strconv.FormatInt(anon+shmem+(1<<30), 10) + "\n",
		"memory.stat":    "anon " + strconv.FormatInt(anon, 10) + "\nfile 1073741824\nshmem " + strconv.FormatInt(shmem, 10) + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHarnessRSSByProject(t *testing.T) {
	root := t.TempDir()
	const mib = int64(1) << 20
	dirs := map[int]string{}
	cg := func(pid int, anon, shmem int64) {
		dir := filepath.Join(root, "docker-"+strconv.Itoa(pid)+".scope")
		writeCgroup(t, dir, anon, shmem)
		dirs[pid] = dir
	}
	cg(101, 300*mib, 10*mib) // r1 app
	cg(102, 200*mib, 0)      // r1 postgres
	cg(103, 20*mib, 0)       // r1 edge
	cg(201, 500*mib, 5*mib)  // r2 app
	cg(301, 900*mib, 0)      // another project's container

	fake := &fakeContainers{
		list: []Container{
			{ID: "c101", Project: "harness-r1", PID: 101},
			{ID: "c102", Project: "harness-r1", PID: 102},
			{ID: "c103", Project: "harness-r1", PID: 103},
			{ID: "c201", Project: "harness-r2", PID: 201},
			{ID: "c202", Project: "harness-r2"}, // no PID: docker stats answers
			{ID: "c301", Project: "wtb-db", PID: 301},
		},
		stats: map[string]int64{"c202": 40 * mib},
	}
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	r := &RSSReader{
		Docker:   fake,
		CgroupOf: func(pid int) string { return dirs[pid] },
		Now:      func() time.Time { return now },
	}

	m, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []RunMemory{
		{Run: "r1", Project: "harness-r1", HeldBytes: 530 * mib, Containers: 3},
		{Run: "r2", Project: "harness-r2", HeldBytes: 545 * mib, Containers: 2},
	}
	if len(m.Runs) != len(want) {
		t.Fatalf("runs %+v, want %+v", m.Runs, want)
	}
	for i := range want {
		if m.Runs[i] != want[i] {
			t.Errorf("run %d: %+v, want %+v", i, m.Runs[i], want[i])
		}
	}
	if m.HeldBytes != 1075*mib || m.Source != "mixed" {
		t.Fatalf("total %d source %q, want %d mixed", m.HeldBytes, m.Source, 1075*mib)
	}

	// Inside the listing's lifetime Docker is not asked again: cgroup files
	// are read afresh, the docker stats answer stands.
	writeCgroup(t, dirs[101], 400*mib, 10*mib)
	now = now.Add(5 * time.Second)
	m, err = r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.lists != 1 || fake.statCalls != 1 {
		t.Fatalf("docker asked %d lists and %d stats, want 1 and 1", fake.lists, fake.statCalls)
	}
	if m.Runs[0].HeldBytes != 630*mib {
		t.Fatalf("r1 %d, want the fresh cgroup read %d", m.Runs[0].HeldBytes, 630*mib)
	}

	// r2's app stops: its cgroup goes, it is left out, and the next read
	// lists again at once.
	if err := os.RemoveAll(dirs[201]); err != nil {
		t.Fatal(err)
	}
	fake.list = fake.list[:3]
	now = now.Add(5 * time.Second)
	m, err = r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Runs) != 2 || m.Runs[1].HeldBytes != 40*mib {
		t.Fatalf("runs %+v, want r2 with only its stats container", m.Runs)
	}
	now = now.Add(time.Second)
	m, err = r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.lists != 2 || len(m.Runs) != 1 || m.Runs[0].Run != "r1" || m.Source != "cgroup" {
		t.Fatalf("after relist: %d lists, %+v source %q, want 2 lists and r1 alone from cgroups", fake.lists, m.Runs, m.Source)
	}

	// The listing expires after rssListEvery.
	now = now.Add(rssListEvery)
	if _, err := r.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.lists != 3 {
		t.Fatalf("%d lists, want 3 once the listing expired", fake.lists)
	}

	// No harness containers: nothing, and no source.
	fake.list = nil
	now = now.Add(rssListEvery)
	m, err = r.Read(context.Background())
	if err != nil || m.HeldBytes != 0 || len(m.Runs) != 0 || m.Source != "" {
		t.Fatalf("empty host: %+v, %v", m, err)
	}
}

func TestParseStatsMemory(t *testing.T) {
	got := parseStatsMemory("aaa\t12.5MiB / 2GiB\nbbb\t1.5GiB / 2GiB\nccc\t512B / 1GiB\nddd\t3.4MB / 1GB\nbad line\n")
	want := map[string]int64{"aaa": 13107200, "bbb": 1610612736, "ccc": 512, "ddd": 3400000}
	for id, v := range want {
		if got[id] != v {
			t.Errorf("%s: %d, want %d", id, got[id], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
}
