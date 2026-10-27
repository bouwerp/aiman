package skills

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/quality"
)

const kitWT = "/home/user/code/repo"

var kitTaskPath = filepath.Join(kitWT, domain.AimanTaskFileName)

func goChecklist() domain.Checklist {
	return domain.Checklist{Name: "go", Path: "/home/user/code/repo/.aiman/checklists/go.md", Source: "repo", AppliesTo: []string{"*.go"}}
}

func tsChecklist() domain.Checklist {
	return domain.Checklist{Name: "ts", Path: "/home/user/.aiman/checklists/ts.md", Source: "user", AppliesTo: []string{"*.ts"}}
}

func TestPrepareSessionWritesKitIntoIssueTask(t *testing.T) {
	remote := newMockRemote()
	issue := &domain.Issue{Key: "PROJ-1", Summary: "S", Description: "Body."}
	kit := domain.QualityKit{Repo: "o/r", DoneWhen: []string{"A"}, Checklists: []domain.Checklist{goChecklist()}}
	agent := domain.Agent{Name: "Claude Code", Command: "claude"}
	got, err := NewEngine(&config.Config{}).PrepareSession(context.Background(), remote, kitWT, agent, nil, true, issue, nil, kit)
	if err != nil {
		t.Fatal(err)
	}
	task := string(remote.writtenFiles[kitTaskPath])
	dw := strings.Index(task, "## Done when")
	guide := strings.Index(task, "# Working Guidelines")
	desc := strings.Index(task, "Body.")
	if dw < 0 || guide < 0 || desc > dw || dw > guide {
		t.Fatalf("Done when must sit between the description and the guidelines:\n%s", task)
	}
	if !strings.Contains(task, "- "+goChecklist().Path) {
		t.Fatalf("checklist path missing:\n%s", task)
	}
	if got.InitialPrompt != initialPrompt {
		t.Fatalf("issue prompt changed: %q", got.InitialPrompt)
	}
}

func TestPrepareSessionKitOnlyTaskFile(t *testing.T) {
	for _, agent := range []domain.Agent{
		{Name: "Claude Code", Command: "claude"},
		{Name: "Kilo Code", Command: "kilo"},
	} {
		t.Run(agent.Command, func(t *testing.T) {
			remote := newMockRemote()
			kit := domain.QualityKit{Repo: "o/r", DoneWhen: []string{"`make ci` green"}}
			got, err := NewEngine(&config.Config{}).PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, kit)
			if err != nil {
				t.Fatal(err)
			}
			task, ok := remote.writtenFiles[kitTaskPath]
			if !ok {
				t.Fatal("task file not written")
			}
			if !strings.Contains(string(task), "DO NOT COMMIT") || !strings.Contains(string(task), "- [ ] `make ci` green") {
				t.Fatalf("kit-only task file:\n%s", task)
			}
			if got.InitialPrompt != quality.KitPrompt {
				t.Fatalf("prompt %q, want KitPrompt", got.InitialPrompt)
			}
		})
	}
}

func TestRefreshKitUnchangedWritesNothing(t *testing.T) {
	remote := newMockRemote()
	engine := NewEngine(&config.Config{})
	agent := domain.Agent{Name: "Claude Code", Command: "claude"}
	kit := domain.QualityKit{Repo: "o/r", DoneWhen: []string{"A"}, Checklists: []domain.Checklist{goChecklist()}}
	if _, err := engine.PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, kit); err != nil {
		t.Fatal(err)
	}
	remote.writes = map[string]int{}
	if _, err := engine.PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, kit); err != nil {
		t.Fatal(err)
	}
	restart := kit
	restart.DoneWhen = nil
	if _, err := engine.PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, restart); err != nil {
		t.Fatal(err)
	}
	if n := remote.writes[kitTaskPath]; n != 0 {
		t.Fatalf("unchanged kit wrote the task file %d times", n)
	}
}

func TestRefreshKitKeepsDoneWhen(t *testing.T) {
	remote := newMockRemote()
	existing := quality.KitOnlyTaskFile(quality.RenderDoneWhenBlock([]string{"A"}), quality.RenderChecklistBlock([]domain.Checklist{goChecklist()}))
	remote.writtenFiles[kitTaskPath] = []byte(existing)
	kit := domain.QualityKit{Repo: "o/r", Checklists: []domain.Checklist{tsChecklist()}}
	agent := domain.Agent{Name: "Claude Code", Command: "claude"}
	if _, err := NewEngine(&config.Config{}).PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, kit); err != nil {
		t.Fatal(err)
	}
	task := string(remote.writtenFiles[kitTaskPath])
	if !strings.Contains(task, quality.RenderDoneWhenBlock([]string{"A"})) {
		t.Fatalf("Done-when block changed:\n%s", task)
	}
	dw, paths := quality.ParseTaskKit(task)
	if len(dw) != 1 || dw[0] != "A" {
		t.Fatalf("done-when %v", dw)
	}
	if len(paths) != 1 || paths[0] != tsChecklist().Path {
		t.Fatalf("checklists %v, want only ts", paths)
	}
}

func TestPrepareSessionReviewerIgnoresKit(t *testing.T) {
	remote := newMockRemote()
	kit := domain.QualityKit{Repo: "o/r", Role: domain.SessionRoleReviewer, DoneWhen: []string{"A"}, Checklists: []domain.Checklist{goChecklist()}}
	agent := domain.Agent{Name: "Claude Code", Command: "claude"}
	got, err := NewEngine(&config.Config{}).PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, kit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := remote.writtenFiles[kitTaskPath]; ok {
		t.Fatal("reviewer session must not get a kit task file")
	}
	if got.InitialPrompt != "" {
		t.Fatalf("reviewer prompt %q", got.InitialPrompt)
	}
}

func TestPrepareSessionEmptyKitUnchanged(t *testing.T) {
	remote := newMockRemote()
	agent := domain.Agent{Name: "Claude Code", Command: "claude"}
	got, err := NewEngine(&config.Config{}).PrepareSession(context.Background(), remote, kitWT, agent, nil, true, nil, nil, domain.QualityKit{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := remote.writtenFiles[kitTaskPath]; ok {
		t.Fatal("empty kit must not write a task file")
	}
	if got.InitialPrompt != "" {
		t.Fatalf("prompt %q", got.InitialPrompt)
	}
}
