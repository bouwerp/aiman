package harness

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHarnessRSSReadsContainerCgroups starts one labelled container holding
// 40 MiB of tmpfs and reads it through the docker CLI and its cgroup.
func TestHarnessRSSReadsContainerCgroups(t *testing.T) {
	needDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := "p7rss" + strconv.FormatInt(time.Now().UnixNano(), 36)
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", //nolint:gosec // G204: test arguments
		"--label", ProjectLabel+"="+ProjectPrefix+run, "--memory", "256m",
		EdgeImage, "sh", "-c", "dd if=/dev/zero of=/dev/shm/fill bs=1048576 count=40 && sleep 120").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	var m Memory
	deadline := time.Now().Add(30 * time.Second)
	for {
		// A fresh reader each round, so the listing is never cached.
		r := &RSSReader{Docker: ExecDocker{}}
		m, err = r.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := find(m, run); got != nil && got.HeldBytes >= 40<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never held 40 MiB: %+v", run, m)
		}
		time.Sleep(time.Second)
	}
	got := find(m, run)
	if got.Containers != 1 || got.Project != ProjectPrefix+run || m.Source != "cgroup" {
		t.Fatalf("run %+v source %q, want one container read from its cgroup", got, m.Source)
	}
	stats, err := ExecDocker{}.ContainerMemory(ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if stats[id] < 40<<20 {
		t.Fatalf("docker stats %d bytes, want at least 40 MiB", stats[id])
	}
}

func find(m Memory, run string) *RunMemory {
	for i := range m.Runs {
		if m.Runs[i].Run == run {
			return &m.Runs[i]
		}
	}
	return nil
}
