package harness

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// ProjectLabel is the label Docker Compose sets on every container of a
// project; its value for a harness run is ProjectPrefix plus the run id.
const ProjectLabel = "com.docker.compose.project"

// ProjectPrefix starts the compose project name of every harness run.
const ProjectPrefix = "harness-"

// rssListEvery is how long a container listing stands. Cgroup files are read
// on every call; Docker is asked only this often, or sooner when a listed
// container's cgroup is gone.
const rssListEvery = 15 * time.Second

// Container is one running container of a harness compose project. PID is
// its init process on the host, 0 when Docker did not report one.
type Container struct {
	ID      string
	Project string
	PID     int
}

// RunMemory is the memory one run's containers hold.
type RunMemory struct {
	Run        string `json:"run"`
	Project    string `json:"project"`
	HeldBytes  int64  `json:"held_bytes"`
	Containers int    `json:"containers"`
}

// Memory is the memory every harness run holds, by run. Held is anon plus
// shmem of each container's cgroup (cgroup.Usage.Held): page cache, which the
// kernel reclaims freely, never counts. Source is "cgroup" when every value
// came from a cgroup file, "stats" when every one came from docker stats, and
// "mixed" otherwise; it is empty with no containers.
type Memory struct {
	HeldBytes int64       `json:"held_bytes"`
	Runs      []RunMemory `json:"runs,omitempty"`
	Source    string      `json:"source,omitempty"`
}

// ContainerSource is what RSSReader asks Docker: the running harness
// containers, and the memory of containers whose cgroup cannot be read.
type ContainerSource interface {
	HarnessContainers(ctx context.Context) ([]Container, error)
	ContainerMemory(ctx context.Context, ids []string) (map[string]int64, error)
}

// RSSReader sums the memory harness containers hold. Containers run under
// docker.service, outside the user slice and the session scopes, so neither
// the slice's memory.pressure nor its memory.current sees them.
//
// It lists containers by ProjectLabel at most every rssListEvery (two docker
// CLI calls, ps and inspect, to learn each container's PID), maps each PID to
// its cgroup through /proc/<pid>/cgroup, and reads that cgroup's memory files
// on every call. That costs a few file reads per tick, needs no knowledge of
// the engine's cgroup driver or layout, and catches containers a previous
// serve left behind, which the manager no longer lists. docker stats, which
// samples for about two seconds per call, runs only for containers whose
// cgroup cannot be read (a hidden /proc, a remote or rootless engine), and its
// answer stands as long as the listing does.
type RSSReader struct {
	Docker ContainerSource
	// CgroupOf maps a PID to its cgroup directory; nil is cgroup.Of.
	CgroupOf func(pid int) string
	// ReadHeld reads a cgroup's held memory; nil is cgroup.ReadHeld.
	ReadHeld func(dir string) (int64, bool)
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu       sync.Mutex
	list     []Container
	dirs     map[string]string
	listedAt time.Time
	stats    map[string]int64
}

// Read returns the memory every harness run holds now. An error from Docker
// fails the call; a container whose memory cannot be read at all is left out.
func (r *RSSReader) Read(ctx context.Context) (Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.dirs == nil || now.Sub(r.listedAt) >= rssListEvery {
		if err := r.relist(ctx, now); err != nil {
			return Memory{}, err
		}
	}
	held := map[string]int64{}
	fromCgroup, fromStats := 0, 0
	var missing []string
	for _, c := range r.list {
		if dir := r.dirs[c.ID]; dir != "" {
			if v, ok := r.readHeld(dir); ok {
				held[c.ID] = v
				fromCgroup++
				continue
			}
			// The cgroup is gone: the container stopped. List again next time.
			r.listedAt = time.Time{}
			continue
		}
		missing = append(missing, c.ID)
	}
	if len(missing) > 0 {
		if r.stats == nil {
			stats, err := r.Docker.ContainerMemory(ctx, missing)
			if err != nil {
				// Not again until the next listing: docker stats is slow.
				r.stats = map[string]int64{}
				return Memory{}, err
			}
			r.stats = stats
		}
		for _, id := range missing {
			if v, ok := r.stats[id]; ok {
				held[id] = v
				fromStats++
			}
		}
	}
	m := GroupByProject(r.list, held)
	switch {
	case fromCgroup > 0 && fromStats > 0:
		m.Source = "mixed"
	case fromStats > 0:
		m.Source = "stats"
	case fromCgroup > 0:
		m.Source = "cgroup"
	}
	return m, nil
}

// relist asks Docker for the harness containers and maps each to its cgroup.
func (r *RSSReader) relist(ctx context.Context, now time.Time) error {
	list, err := r.Docker.HarnessContainers(ctx)
	if err != nil {
		return err
	}
	cgroupOf := r.CgroupOf
	if cgroupOf == nil {
		cgroupOf = cgroup.Of
	}
	dirs := make(map[string]string, len(list))
	for _, c := range list {
		if c.PID > 0 {
			dirs[c.ID] = cgroupOf(c.PID)
		} else {
			dirs[c.ID] = ""
		}
	}
	r.list, r.dirs, r.listedAt, r.stats = list, dirs, now, nil
	return nil
}

func (r *RSSReader) readHeld(dir string) (int64, bool) {
	if r.ReadHeld != nil {
		return r.ReadHeld(dir)
	}
	return cgroup.ReadHeld(dir)
}

func (r *RSSReader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// GroupByProject sums held (bytes by container id) by compose project.
// Containers of projects outside ProjectPrefix, and containers with no value
// in held, are left out. Runs are sorted by run id.
func GroupByProject(list []Container, held map[string]int64) Memory {
	byProject := map[string]*RunMemory{}
	var m Memory
	for _, c := range list {
		run, ok := strings.CutPrefix(c.Project, ProjectPrefix)
		if !ok || run == "" {
			continue
		}
		v, ok := held[c.ID]
		if !ok {
			continue
		}
		rm := byProject[c.Project]
		if rm == nil {
			rm = &RunMemory{Run: run, Project: c.Project}
			byProject[c.Project] = rm
		}
		rm.HeldBytes += v
		rm.Containers++
		m.HeldBytes += v
	}
	for _, rm := range byProject {
		m.Runs = append(m.Runs, *rm)
	}
	sort.Slice(m.Runs, func(i, j int) bool { return m.Runs[i].Run < m.Runs[j].Run })
	return m
}

// HarnessContainers lists the running containers whose compose project starts
// with ProjectPrefix, with their PIDs: docker ps filtered by ProjectLabel, then
// one docker inspect over the ids.
func (ExecDocker) HarnessContainers(ctx context.Context) ([]Container, error) {
	out, err := dockerOut(ctx, "ps", "--no-trunc", "--filter", "label="+ProjectLabel,
		"--format", `{{.ID}}	{{.Label "`+ProjectLabel+`"}}`)
	if err != nil {
		return nil, err
	}
	var list []Container
	for _, line := range strings.Split(out, "\n") {
		id, project, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || !strings.HasPrefix(project, ProjectPrefix) {
			continue
		}
		list = append(list, Container{ID: id, Project: project})
	}
	if len(list) == 0 {
		return nil, nil
	}
	args := []string{"inspect", "--format", "{{.Id}}\t{{.State.Pid}}"}
	for _, c := range list {
		args = append(args, c.ID)
	}
	// A container that stopped between ps and inspect fails inspect for
	// itself only; the others are still printed, so the output is read
	// whatever the exit status.
	out, _ = dockerOut(ctx, args...)
	pids := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		id, pid, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			pids[id] = n
		}
	}
	for i := range list {
		list[i].PID = pids[list[i].ID]
	}
	return list, nil
}

// ContainerMemory is docker stats' memory use of each container in ids, in
// bytes.
func (ExecDocker) ContainerMemory(ctx context.Context, ids []string) (map[string]int64, error) {
	args := append([]string{"stats", "--no-stream", "--no-trunc", "--format", "{{.ID}}\t{{.MemUsage}}"}, ids...)
	out, err := dockerOut(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseStatsMemory(out), nil
}

// parseStatsMemory reads "<id>\t<used> / <limit>" lines.
func parseStatsMemory(out string) map[string]int64 {
	got := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		id, usage, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		used, _, _ := strings.Cut(usage, "/")
		if v, ok := parseDockerSize(strings.TrimSpace(used)); ok {
			got[id] = v
		}
	}
	return got
}

// parseDockerSize reads a size as docker stats prints it: "12.5MiB", "1.2GiB",
// "512B", or the decimal "3.4MB".
func parseDockerSize(s string) (int64, bool) {
	units := []struct {
		suffix string
		mult   float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		num, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
		if err != nil || f < 0 {
			return 0, false
		}
		return int64(f * u.mult), true
	}
	return 0, false
}

// dockerOut runs the docker CLI and returns its standard output.
func dockerOut(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: aiman's own docker arguments
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
