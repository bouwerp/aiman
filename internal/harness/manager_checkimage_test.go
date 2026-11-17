package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/domain"
)

const checkImageHarnessFile = `version: 1
app:
  run: exec httpd -f -p 8080
  ports: [{name: web, container: 8080}]
scenarios:
  - id: plain
    checks:
      - {name: unit, run: "go test ./..."}
      - {name: fake, run: "python3 -m pytest", image: "node:24"}
`

func TestCheckImage(t *testing.T) {
	t.Run("validate", func(t *testing.T) {
		f, err := Parse([]byte(checkImageHarnessFile))
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Scenarios[0].Checks[1].Image; got != "node:24" {
			t.Fatalf("checks[1].image = %q", got)
		}
		_, err = Parse([]byte(strings.Replace(checkImageHarnessFile, `image: "node:24"`, `image: "node:24 --privileged"`, 1)))
		if err == nil || !strings.Contains(err.Error(), "checks[1].image") {
			t.Fatalf("a bad check image parsed: %v", err)
		}
	})

	tm := newTestManager(t)
	tm.docker.images = newFakeDocker("golang:1.25", EdgeImage, "node:24").images
	nodeID := tm.docker.images["node:24"]
	tm.docker.answer = func(args []string) (string, int, bool) {
		if strings.Join(args, " ") == "run --rm --no-deps "+CheckService(1)+" sh -c python3 -m pytest" {
			return "fake ran\n", 0, true
		}
		return "", 0, false
	}
	repo := testRepo(t, checkImageHarnessFile)
	run := tm.upReady(t, repo, "plain")
	if run.Images[CheckService(1)] != nodeID || run.Images[CheckService(0)] != "" {
		t.Fatalf("images = %v; want only the fake check's image, by ID", run.Images)
	}
	if run.Checks[1].Image != "node:24" || run.Checks[0].Image != "" {
		t.Fatalf("checks = %+v", run.Checks)
	}

	raw, err := os.ReadFile(filepath.Join(tm.runDir(run.Session, run.ID), "sandbox", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc composeFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	svc, ok := doc.Services[CheckService(1)]
	if !ok {
		t.Fatalf("compose has no %s service:\n%s", CheckService(1), raw)
	}
	app := doc.Services[ServiceApp]
	if svc.Image != nodeID || svc.PullPolicy != pullNever || !slices.Equal(svc.Profiles, []string{checksProfile}) {
		t.Fatalf("check service = %+v", svc)
	}
	if !slices.Equal(svc.Volumes, app.Volumes) || svc.User != app.User || !svc.ReadOnly || svc.MemLimit != app.MemLimit || svc.NetworkMode != "" {
		t.Fatalf("check service %+v does not keep the app's mounts, user and limits (%+v)", svc, app)
	}
	if nets, _ := svc.Networks.([]any); len(nets) != 1 || nets[0] != NetworkInner {
		t.Fatalf("check networks = %v; want only the inner network", svc.Networks)
	}
	if len(svc.Command) != 0 {
		t.Fatalf("check service command = %v; harness.check passes it", svc.Command)
	}
	if _, ok := doc.Services[CheckService(0)]; ok {
		t.Fatal("a check without an image got its own service")
	}

	results, err := tm.Check(context.Background(), run.ID, "", domain.EventActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if tm.docker.called("run --rm --no-deps app sh -c go test ./...") != 1 || tm.docker.called("run --rm --no-deps "+CheckService(1)+" ") != 1 {
		t.Fatalf("calls = %q", tm.docker.calls)
	}
	if !strings.Contains(results[1].Output, "fake ran") || results[1].Image != "node:24" {
		t.Fatalf("results = %+v", results)
	}
}

func TestCheckImageVetted(t *testing.T) {
	t.Run("asked for with the others", func(t *testing.T) {
		v := &recordingVetter{}
		tm := newTestManager(t, func(o *Options) { o.Images = v })
		repo := testRepo(t, checkImageHarnessFile)
		run := tm.upReady(t, repo, "plain")
		i := slices.IndexFunc(v.reqs, func(r ImageRequest) bool { return r.Ref == "node:24" })
		if i < 0 || v.reqs[i].Builtin || v.reqs[i].Session != "s1" || !v.reqs[i].Operator {
			t.Fatalf("requests = %+v; want node:24 asked for, not builtin", v.reqs)
		}
		if run.Images[CheckService(1)] == "" {
			t.Fatalf("images = %v", run.Images)
		}
	})
	t.Run("pending refuses up", func(t *testing.T) {
		v := &recordingVetter{pending: "node:24"}
		tm := newTestManager(t, func(o *Options) { o.Images = v })
		repo := testRepo(t, checkImageHarnessFile)
		_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"})
		var he *Error
		if !errors.As(err, &he) || he.Code != CodeImagePending || he.Image != "node:24" {
			t.Fatalf("err = %v, want harness_image_pending for the check image", err)
		}
		if !slices.ContainsFunc(v.reqs, func(r ImageRequest) bool { return r.Ref == EdgeImage }) {
			t.Fatal("a pending check image stopped the other images from being asked for")
		}
		if runs, _ := tm.Runs("s1"); len(runs) != 0 {
			t.Fatalf("a refused run left %d run records", len(runs))
		}
	})
	t.Run("not allowed without vetting", func(t *testing.T) {
		tm := newTestManager(t)
		tm.docker.images = newFakeDocker("golang:1.25", EdgeImage, "python:3.13").images
		repo := testRepo(t, strings.Replace(checkImageHarnessFile, "node:24", "python:3.13", 1))
		_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"})
		var he *Error
		if !errors.As(err, &he) || he.Code != CodeImageMissing || he.Image != "python:3.13" {
			t.Fatalf("err = %v, want harness_image_missing for the check image", err)
		}
	})
}
