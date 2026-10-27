package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/verify"
)

// EnableVerify attaches a real verify.Manager to the harness server. The
// manager's sessions are the harness repository, its terminal is a map of
// stopped ids, and the reviewer is created by FakeCreator.
func (h *Harness) EnableVerify(cfg verify.Config) error {
	store := verify.NewStore(filepath.Join(h.Dir, "reviews"))
	term := &evalTerminal{stopped: map[string]bool{}}
	m := verify.NewManager(cfg, verify.Deps{
		Store:     store,
		Sessions:  evalSessions{h},
		Terminal:  term,
		Todos:     h.srv.TodoAccess(),
		Installed: func(string) bool { return true },
		Poll:      10 * time.Millisecond,
	})
	h.srv.SetVerifier(m)
	h.verifier = m
	h.reviews = store
	h.terminal = term
	return nil
}

// StartVerify starts a review through the socket and returns its first state.
func (h *Harness) StartVerify(sessionID string) (verify.Review, error) {
	resp, err := h.Call("verify.start", map[string]any{"session": sessionID})
	if err != nil {
		return verify.Review{}, err
	}
	return decodeReview(resp.Result)
}

// VerifyStatus reads one review.
func (h *Harness) VerifyStatus(id string) (verify.Review, error) {
	resp, err := h.Call("verify.status", map[string]any{"review": id})
	if err != nil {
		return verify.Review{}, err
	}
	return decodeReview(resp.Result)
}

// WaitReview polls until the review reaches status.
func (h *Harness) WaitReview(id string, status verify.Status) (verify.Review, bool) {
	var got verify.Review
	ok := waitUntil(10*time.Second, func() bool {
		rv, err := h.VerifyStatus(id)
		if err != nil {
			return false
		}
		got = rv
		return rv.Status == status
	})
	return got, ok
}

// WriteReview writes the reviewer's marker file in the review worktree.
func (h *Harness) WriteReview(id, text string) error {
	rv, err := h.VerifyStatus(id)
	if err != nil {
		return err
	}
	dir := filepath.Join(rv.Worktree, rv.Dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".aiman_review.md"), []byte(text), 0o600)
}

// InitGitSession creates a git-backed worktree for a session under dir/id.
func (h *Harness) InitGitSession(id string) (string, error) {
	dir := filepath.Join(h.Dir, "sessions", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	run := func(args ...string) error {
		// #nosec G204 -- the verb is fixed and every path is an argv element.
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Aiman", "GIT_AUTHOR_EMAIL=aiman@localhost", "GIT_COMMITTER_NAME=Aiman", "GIT_COMMITTER_EMAIL=aiman@localhost")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
		return nil
	}
	if err := run("init", "-b", "main"); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar a = 1\n"), 0o600); err != nil {
		return "", err
	}
	if err := run("add", "-A"); err != nil {
		return "", err
	}
	if err := run("commit", "-m", "base"); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

type evalSessions struct{ h *Harness }

func (s evalSessions) Get(ctx context.Context, id string) (domain.Session, error) {
	sess, err := s.h.Repo.Get(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	return *sess, nil
}

func (s evalSessions) CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) {
	sess, err := (FakeCreator{Dir: s.h.Dir}).CreateSession(ctx, cfg)
	if err != nil {
		return domain.Session{}, err
	}
	sess.Role, sess.ParentID = cfg.Role, cfg.ParentID
	if err := s.h.Repo.Save(ctx, sess); err != nil {
		return domain.Session{}, err
	}
	s.h.remote.addSession(sess.TmuxSession, sess.WorkingDirectory)
	return *sess, nil
}

func (s evalSessions) Forget(ctx context.Context, id string) error {
	return s.h.Repo.Delete(ctx, id)
}

type evalTerminal struct {
	mu      sync.Mutex
	stopped map[string]bool
}

func (t *evalTerminal) Alive(_ context.Context, s domain.Session) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.stopped[s.ID]
}

func (t *evalTerminal) Stop(_ context.Context, s domain.Session) error {
	t.mu.Lock()
	t.stopped[s.ID] = true
	t.mu.Unlock()
	return nil
}

func marshalJSON(v any) ([]byte, error)   { return json.Marshal(v) }
func unmarshalJSON(b []byte, v any) error { return json.Unmarshal(b, v) }

func decodeReview(v any) (verify.Review, error) {
	body, ok := v.(map[string]any)
	if !ok {
		return verify.Review{}, fmt.Errorf("decode review: %T", v)
	}
	raw, ok := body["review"]
	if !ok {
		return verify.Review{}, fmt.Errorf("decode review: no review")
	}
	b, err := marshalJSON(raw)
	if err != nil {
		return verify.Review{}, err
	}
	var r verify.Review
	if err := unmarshalJSON(b, &r); err != nil {
		return verify.Review{}, err
	}
	return r, nil
}
