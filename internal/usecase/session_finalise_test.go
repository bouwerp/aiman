package usecase

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// slowRemote answers every command after a fixed delay, recording what it saw.
type slowRemote struct {
	discovererRemote
	delay time.Duration
	mu    sync.Mutex
	seen  []string
	reply map[string]string
}

func (r *slowRemote) Execute(ctx context.Context, cmd string) (string, error) {
	r.mu.Lock()
	r.seen = append(r.seen, cmd)
	reply := r.reply[cmd]
	r.mu.Unlock()

	select {
	case <-time.After(r.delay):
		return reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *slowRemote) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// The trust commands and model lookup each boot an agent CLI on the remote.
// Sequentially they measured ~31s; they must overlap.
func TestFinaliseSessionRunsConcurrently(t *testing.T) {
	remote := &slowRemote{delay: 300 * time.Millisecond}
	fm := &FlowManager{}
	session := &domain.Session{ID: "s1"}
	cfg := domain.SessionConfig{Agent: &domain.Agent{Name: "Claude Code"}}

	start := time.Now()
	fm.finaliseSessionBestEffort(context.Background(), remote, session, cfg, "/repos/app@x")
	elapsed := time.Since(start)

	cmds := remote.commands()
	if len(cmds) < 5 {
		t.Fatalf("expected the trust commands and the model lookup, got %d: %v", len(cmds), cmds)
	}
	// Five commands at 300ms each is 1.5s sequentially; concurrently well under.
	if elapsed > time.Second {
		t.Errorf("finalisation took %s — the steps are not overlapping", elapsed.Round(time.Millisecond))
	}
}

// A step that never returns must not hold session creation open.
func TestFinaliseSessionIsBounded(t *testing.T) {
	remote := &slowRemote{delay: time.Hour}
	fm := &FlowManager{}
	session := &domain.Session{ID: "s1"}
	cfg := domain.SessionConfig{Agent: &domain.Agent{Name: "Claude Code"}}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		fm.finaliseSessionBestEffort(context.Background(), remote, session, cfg, "/repos/app@x")
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > bestEffortFinaliseTimeout+2*time.Second {
			t.Errorf("returned after %s, want it bounded by %s", elapsed, bestEffortFinaliseTimeout)
		}
	case <-time.After(bestEffortFinaliseTimeout + 5*time.Second):
		t.Fatal("finalisation did not return; a hung agent CLI can block session creation")
	}
}

// The model is decoration: losing it must not lose the session.
func TestFinaliseSessionLeavesModelEmptyOnTimeout(t *testing.T) {
	remote := &slowRemote{delay: time.Hour}
	fm := &FlowManager{}
	session := &domain.Session{ID: "s1", AgentModel: ""}
	cfg := domain.SessionConfig{Agent: &domain.Agent{Name: "Claude Code"}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	fm.finaliseSessionBestEffort(ctx, remote, session, cfg, "/repos/app@x")

	if session.AgentModel != "" {
		t.Errorf("AgentModel = %q, want it left empty when detection times out", session.AgentModel)
	}
}

func TestFinaliseSessionRecordsDetectedModel(t *testing.T) {
	modelCmd := `printenv ANTHROPIC_MODEL 2>/dev/null || claude config get model 2>/dev/null || echo ""`
	remote := &slowRemote{reply: map[string]string{modelCmd: "claude-opus-5\n"}}
	fm := &FlowManager{}
	session := &domain.Session{ID: "s1"}
	cfg := domain.SessionConfig{Agent: &domain.Agent{Name: "Claude Code"}}

	fm.finaliseSessionBestEffort(context.Background(), remote, session, cfg, "/repos/app@x")

	if session.AgentModel != "claude-opus-5" {
		t.Errorf("AgentModel = %q, want claude-opus-5", session.AgentModel)
	}
}

func TestFinaliseSessionTrustsTheWorkingDirectory(t *testing.T) {
	remote := &slowRemote{}
	fm := &FlowManager{}
	fm.finaliseSessionBestEffort(context.Background(), remote, &domain.Session{ID: "s"},
		domain.SessionConfig{}, "/repos/app@x")

	joined := strings.Join(remote.commands(), "\n")
	if !strings.Contains(joined, "safe.directory") {
		t.Errorf("expected git safe.directory to be set, got:\n%s", joined)
	}
	if !strings.Contains(joined, "/repos/app@x") {
		t.Errorf("expected the working directory to be used, got:\n%s", joined)
	}
}

func TestFinaliseSessionNilSafe(t *testing.T) {
	fm := &FlowManager{}
	fm.finaliseSessionBestEffort(context.Background(), nil, &domain.Session{}, domain.SessionConfig{}, "/x")
	fm.finaliseSessionBestEffort(context.Background(), &slowRemote{}, nil, domain.SessionConfig{}, "/x")
}
