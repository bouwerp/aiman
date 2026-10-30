package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Sandbox is one review's Docker Compose project. Dir holds compose.yaml;
// Src is the exported tree mounted at /src; Out is mounted at /out.
type Sandbox struct {
	ID, Dir, Src, Out, Image, Network string
	Ports                             []PortMap
}

// PortMap publishes a container port on 127.0.0.1.
type PortMap struct {
	Host, Container int
}

const (
	sandboxMemory = "2g"
	sandboxCPUs   = 2.0
	sandboxPIDs   = 256
)

var (
	goVersionRe = regexp.MustCompile(`(?m)^go\s+([0-9]+)\.([0-9]+)`)
	nodeMajorRe = regexp.MustCompile(`([0-9]+)`)
	// ErrNoLanguage is returned by Image when src has no go.mod, package.json, or override.
	ErrNoLanguage = errors.New("review sandbox: no go.mod or package.json, and no image override")
)

// Image picks the container image for src. override wins. A go.mod `go`
// line becomes golang:<major.minor>. Node uses .nvmrc, else engines.node,
// else node:lts. Neither go.mod nor package.json without an override is an
// error.
func Image(src, override string) (string, error) {
	if v := strings.TrimSpace(override); v != "" {
		return v, nil
	}
	if img, err := imageFromGoMod(src); err != nil {
		return "", err
	} else if img != "" {
		return img, nil
	}
	if img, err := imageFromNode(src); err != nil {
		return "", err
	} else if img != "" {
		return img, nil
	}
	return "", ErrNoLanguage
}

func imageFromGoMod(src string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(src, "go.mod")) //nolint:gosec // src is the review export
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	m := goVersionRe.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("review sandbox: go.mod has no go version")
	}
	return "golang:" + string(m[1]) + "." + string(m[2]), nil
}

func imageFromNode(src string) (string, error) {
	pkg := filepath.Join(src, "package.json")
	if _, err := os.Stat(pkg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if raw, err := os.ReadFile(filepath.Join(src, ".nvmrc")); err == nil { //nolint:gosec // src is the review export
		if v := nodeTag(string(raw)); v != "" {
			return "node:" + v, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	engines, err := packageEnginesNode(pkg)
	if err != nil {
		return "", err
	}
	if v := nodeTag(engines); v != "" {
		return "node:" + v, nil
	}
	return "node:lts", nil
}

func packageEnginesNode(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is src/package.json
	if err != nil {
		return "", err
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("review sandbox: package.json: %w", err)
	}
	return pkg.Engines.Node, nil
}

func nodeTag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if f := strings.Fields(s); len(f) > 0 {
		s = f[0]
	}
	m := nodeMajorRe.FindString(s)
	return m
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
}

type composeService struct {
	Image       string            `yaml:"image"`
	WorkingDir  string            `yaml:"working_dir"`
	User        string            `yaml:"user"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	SecurityOpt []string          `yaml:"security_opt"`
	Tmpfs       []string          `yaml:"tmpfs"`
	Environment map[string]string `yaml:"environment"`
	Volumes     []string          `yaml:"volumes"`
	MemLimit    string            `yaml:"mem_limit"`
	CPUs        float64           `yaml:"cpus"`
	PidsLimit   int               `yaml:"pids_limit"`
	Ports       []string          `yaml:"ports,omitempty"`
	NetworkMode string            `yaml:"network_mode,omitempty"`
}

// Compose marshals aiman's sandbox compose file. It is never taken from the PR.
func Compose(sb Sandbox, uid, gid int) ([]byte, error) {
	gomod, gocache, npm := sb.ID+"-gomod", sb.ID+"-gocache", sb.ID+"-npm"
	svc := composeService{
		Image:       sb.Image,
		WorkingDir:  "/src",
		User:        fmt.Sprintf("%d:%d", uid, gid),
		ReadOnly:    true,
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Tmpfs:       []string{"/tmp"},
		Environment: map[string]string{
			"HOME":             "/tmp",
			"GOMODCACHE":       "/cache/gomod",
			"GOCACHE":          "/cache/gocache",
			"npm_config_cache": "/cache/npm",
		},
		Volumes: []string{
			sb.Src + ":/src",
			sb.Out + ":/out",
			gomod + ":/cache/gomod",
			gocache + ":/cache/gocache",
			npm + ":/cache/npm",
		},
		MemLimit:  sandboxMemory,
		CPUs:      sandboxCPUs,
		PidsLimit: sandboxPIDs,
	}
	if sb.Network == "none" {
		svc.NetworkMode = "none"
	}
	for _, p := range sb.Ports {
		svc.Ports = append(svc.Ports, fmt.Sprintf("127.0.0.1:%d:%d", p.Host, p.Container))
	}
	doc := composeFile{
		Name:     sb.ID,
		Services: map[string]composeService{"sandbox": svc},
		Volumes: map[string]map[string]any{
			gomod:   {},
			gocache: {},
			npm:     {},
		},
	}
	return yaml.Marshal(doc)
}

// Export writes the staged tree at worktree into dst with no .git, using
// git checkout-index. dst is removed first.
func Export(ctx context.Context, run func(ctx context.Context, dir, name string, args ...string) (string, error), worktree, dst string) error {
	if run == nil {
		return errors.New("review export: run is nil")
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	prefix := dst
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	_, err := run(ctx, worktree, "git", "checkout-index", "-a", "--prefix="+prefix)
	return err
}

// DirRun runs name in dir and returns stdout. Export uses it when the
// caller has no remote executor.
func DirRun(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = execWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Runner runs commands in a review sandbox through Docker Compose.
type Runner struct {
	Docker func(ctx context.Context, dir string, args ...string) (string, int, error)
	UID    int
	GID    int
}

// Run writes compose.yaml and runs cmd in the sandbox service. timeout 0
// leaves ctx as the only deadline. The command's exit code is returned even
// when non-zero; Docker itself failing is an error.
func (r Runner) Run(ctx context.Context, sb Sandbox, cmd []string, timeout time.Duration) (string, int, error) {
	if r.Docker == nil {
		return "", 0, errors.New("review sandbox: docker not configured")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	composePath, err := r.WriteCompose(sb)
	if err != nil {
		return "", 0, err
	}
	args := []string{"compose", "-f", composePath, "run", "--rm", "sandbox"}
	args = append(args, cmd...)
	return r.Docker(ctx, sb.Dir, args...)
}

// WriteCompose writes compose.yaml for sb and returns its path.
func (r Runner) WriteCompose(sb Sandbox) (string, error) {
	if err := os.MkdirAll(sb.Dir, 0o700); err != nil {
		return "", err
	}
	uid, gid := r.UID, r.GID
	if uid == 0 && gid == 0 {
		uid, gid = os.Getuid(), os.Getgid()
	}
	raw, err := Compose(sb, uid, gid)
	if err != nil {
		return "", err
	}
	path := filepath.Join(sb.Dir, "compose.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Down stops the sandbox compose project and removes its volumes.
func (r Runner) Down(ctx context.Context, sb Sandbox) error {
	if r.Docker == nil {
		return errors.New("review sandbox: docker not configured")
	}
	_, _, err := r.Docker(ctx, sb.Dir, "compose", "-f", filepath.Join(sb.Dir, "compose.yaml"), "down", "-v")
	return err
}

// DockerExec runs `docker` with args in dir. A process exit is returned as
// (output, code, nil); a failure to start docker is an error.
func DockerExec(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.WaitDelay = execWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := stdout.String()
	if err == nil {
		return out, 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if stderr.Len() > 0 {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += stderr.String()
		}
		return out, ee.ExitCode(), nil
	}
	return out, 0, fmt.Errorf("docker: %w: %s", err, strings.TrimSpace(stderr.String()))
}
