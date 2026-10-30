package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestImageFromGoMod(t *testing.T) {
	got, err := Image("testdata/sandbox/go", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "golang:1.26" {
		t.Fatalf("Image(go.mod) = %q, want golang:1.26", got)
	}
}

func TestImageFromNode(t *testing.T) {
	cases := []struct {
		dir, want string
	}{
		{"testdata/sandbox/node-nvmrc", "node:20"},
		{"testdata/sandbox/node-engines", "node:22"},
		{"testdata/sandbox/node-lts", "node:lts"},
	}
	for _, tc := range cases {
		got, err := Image(tc.dir, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.dir, err)
		}
		if got != tc.want {
			t.Errorf("%s: Image = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestImageOverride(t *testing.T) {
	got, err := Image("testdata/sandbox/go", "custom:tag")
	if err != nil || got != "custom:tag" {
		t.Fatalf("override = %q (%v), want custom:tag", got, err)
	}
	got, err = Image(t.TempDir(), "node:21")
	if err != nil || got != "node:21" {
		t.Fatalf("override without manifest = %q (%v)", got, err)
	}
}

func TestImageUnknown(t *testing.T) {
	_, err := Image(t.TempDir(), "")
	if err == nil {
		t.Fatal("empty src: want error")
	}
}

func TestComposeHardening(t *testing.T) {
	sb := Sandbox{
		ID:      "rev1",
		Dir:     "/reviews/rev1/sandbox",
		Src:     "/reviews/rev1/src",
		Out:     "/reviews/rev1/out",
		Image:   "golang:1.26",
		Network: "none",
		Ports:   []PortMap{{Host: 2345, Container: 2345}},
	}
	raw, err := Compose(sb, 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "privileged") {
		t.Fatalf("compose mentions privileged:\n%s", text)
	}
	if strings.Contains(text, "network_mode: host") || strings.Contains(text, "network_mode: \"host\"") {
		t.Fatalf("host network:\n%s", text)
	}
	if strings.Contains(text, "docker.sock") {
		t.Fatalf("docker.sock mounted:\n%s", text)
	}

	var doc composeDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse compose: %v\n%s", err, text)
	}
	svc, ok := doc.Services["sandbox"]
	if !ok {
		t.Fatalf("no sandbox service:\n%s", text)
	}
	if svc.Image != "golang:1.26" || svc.WorkingDir != "/src" {
		t.Fatalf("image %q working_dir %q", svc.Image, svc.WorkingDir)
	}
	if svc.User != "1000:2000" {
		t.Fatalf("user %q, want 1000:2000", svc.User)
	}
	if !svc.ReadOnly {
		t.Fatal("read_only is not true")
	}
	if svc.Privileged {
		t.Fatal("privileged is set")
	}
	if svc.NetworkMode != "none" {
		t.Fatalf("network_mode %q, want none", svc.NetworkMode)
	}
	if !reflect.DeepEqual(svc.CapDrop, []string{"ALL"}) {
		t.Fatalf("cap_drop %v", svc.CapDrop)
	}
	if !reflect.DeepEqual(svc.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("security_opt %v", svc.SecurityOpt)
	}
	if !reflect.DeepEqual(svc.Tmpfs, []string{"/tmp"}) {
		t.Fatalf("tmpfs %v", svc.Tmpfs)
	}
	wantEnv := map[string]string{
		"HOME": "/tmp", "GOMODCACHE": "/cache/gomod", "GOCACHE": "/cache/gocache",
		"npm_config_cache": "/cache/npm",
	}
	if !reflect.DeepEqual(svc.Environment, wantEnv) {
		t.Fatalf("environment %v, want %v", svc.Environment, wantEnv)
	}
	if svc.MemLimit != "2g" || svc.CPUs != 2 || svc.PidsLimit != 256 {
		t.Fatalf("limits mem=%q cpus=%v pids=%d", svc.MemLimit, svc.CPUs, svc.PidsLimit)
	}
	if !reflect.DeepEqual(svc.Ports, []string{"127.0.0.1:2345:2345"}) {
		t.Fatalf("ports %v", svc.Ports)
	}
	gotSrc, gotOut := false, false
	for _, v := range svc.Volumes {
		host, _, _ := strings.Cut(v, ":")
		switch {
		case strings.Contains(v, "docker.sock"):
			t.Fatalf("docker.sock volume %q", v)
		case host == sb.Src:
			gotSrc = true
		case host == sb.Out:
			gotOut = true
		case strings.HasPrefix(host, "/"):
			t.Fatalf("unexpected host path volume %q", v)
		}
	}
	if !gotSrc || !gotOut {
		t.Fatalf("src/out mounts missing: %v", svc.Volumes)
	}
	for _, name := range []string{"rev1-gomod", "rev1-gocache", "rev1-npm"} {
		if _, ok := doc.Volumes[name]; !ok {
			t.Errorf("named volume %s missing: %v", name, doc.Volumes)
		}
	}
	if doc.Name != "rev1" {
		t.Fatalf("compose project name %q, want the review id", doc.Name)
	}
}

func TestComposeProjectNameIsolatesReviews(t *testing.T) {
	names := map[string]bool{}
	for _, id := range []string{"rev-a", "rev-b"} {
		raw, err := Compose(Sandbox{ID: id, Src: "/src", Out: "/out", Image: "golang:1.26"}, 1000, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var doc composeDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Name != id {
			t.Fatalf("id %s: name %q", id, doc.Name)
		}
		if names[doc.Name] {
			t.Fatalf("shared compose project %q", doc.Name)
		}
		names[doc.Name] = true
	}
}

// composeDoc is the subset TestComposeHardening reads back from the generated YAML.
type composeDoc struct {
	Name     string                       `yaml:"name"`
	Services map[string]composeServiceDoc `yaml:"services"`
	Volumes  map[string]any               `yaml:"volumes"`
}

type composeServiceDoc struct {
	Image       string            `yaml:"image"`
	WorkingDir  string            `yaml:"working_dir"`
	User        string            `yaml:"user"`
	ReadOnly    bool              `yaml:"read_only"`
	Privileged  bool              `yaml:"privileged"`
	NetworkMode string            `yaml:"network_mode"`
	CapDrop     []string          `yaml:"cap_drop"`
	SecurityOpt []string          `yaml:"security_opt"`
	Tmpfs       []string          `yaml:"tmpfs"`
	Environment map[string]string `yaml:"environment"`
	Volumes     []string          `yaml:"volumes"`
	MemLimit    string            `yaml:"mem_limit"`
	CPUs        float64           `yaml:"cpus"`
	PidsLimit   int               `yaml:"pids_limit"`
	Ports       []string          `yaml:"ports"`
}

func TestExportExcludesGit(t *testing.T) {
	repo := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "secret.txt"), []byte("unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q")
	git.Dir = repo
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	add := exec.Command("git", "add", "hello.txt")
	add.Dir = repo
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	run := func(ctx context.Context, dir, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if err := Export(context.Background(), run, repo, dst); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil || string(body) != "staged\n" {
		t.Fatalf("hello.txt = %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git present in export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("unstaged file exported: %v", err)
	}
}

func TestRunnerArgs(t *testing.T) {
	dir := t.TempDir()
	sb := Sandbox{
		ID: "rid", Dir: dir, Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"),
		Image: "golang:1.26",
	}
	var got []string
	r := Runner{
		Docker: func(_ context.Context, _ string, args ...string) (string, int, error) {
			got = append([]string(nil), args...)
			return "fail\n", 3, nil
		},
		UID: 1000, GID: 1000,
	}
	out, exit, err := r.Run(context.Background(), sb, []string{"go", "test", "./..."}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if exit != 3 || out != "fail\n" {
		t.Fatalf("Run = %q exit %d, want fail / 3", out, exit)
	}
	want := []string{"compose", "-f", filepath.Join(dir, "compose.yaml"), "run", "--rm", "sandbox", "go", "test", "./..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args %q, want %q", got, want)
	}
}
