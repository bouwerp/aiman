package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const (
	maxReplayEvents = 10000
	maxReplayBytes  = 5 << 20
)

// Bundle is one replay fixture written to disk. Empty fields mean the artifact
// was not present; the manifest lists only the files that exist.
type Bundle struct {
	Manifest string `json:"manifest"`
	Events   string `json:"events"`
	Todo     string `json:"todo,omitempty"`
	Handover string `json:"handover,omitempty"`
	Review   string `json:"review,omitempty"`
}

type replayManifest struct {
	SessionID   string            `json:"session_id"`
	Name        string            `json:"name,omitempty"`
	Group       string            `json:"group,omitempty"`
	Events      int               `json:"events"`
	GeneratedAt time.Time         `json:"generated_at"`
	Files       map[string]string `json:"files"`
}

type sessionEventLister interface {
	ListSessionEvents(ctx context.Context, sessionID string, since int64, limit int) ([]domain.SessionEvent, error)
}

// ReplayBundle writes a read-only fixture: the ledger as JSONL, the to-do
// store, the handover file and the review record when they exist. It never
// creates a session, calls a remote, or re-executes anything.
func ReplayBundle(ctx context.Context, repo domain.SessionRepository, sessionID, outDir string) (Bundle, error) {
	if repo == nil {
		return Bundle{}, fmt.Errorf("session repository is required")
	}
	if outDir == "" {
		return Bundle{}, fmt.Errorf("output directory is required")
	}
	sess, err := repo.Get(ctx, sessionID)
	if err != nil {
		return Bundle{}, fmt.Errorf("reading session: %w", err)
	}
	lister, ok := repo.(sessionEventLister)
	if !ok {
		return Bundle{}, fmt.Errorf("session ledger is not available")
	}
	events, err := lister.ListSessionEvents(ctx, sessionID, 0, maxReplayEvents+1)
	if err != nil {
		return Bundle{}, fmt.Errorf("reading session events: %w", err)
	}
	if len(events) > maxReplayEvents {
		return Bundle{}, fmt.Errorf("replay bundle exceeds %d events", maxReplayEvents)
	}
	if size := eventsSize(events); size > maxReplayBytes {
		return Bundle{}, fmt.Errorf("replay bundle exceeds %d bytes", maxReplayBytes)
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return Bundle{}, fmt.Errorf("creating output directory: %w", err)
	}
	bundle, err := writeBundleFiles(sess, events, outDir)
	if err != nil {
		return Bundle{}, err
	}
	manifest := replayManifest{
		SessionID:   sess.ID,
		Name:        sess.Name,
		Group:       sess.Group,
		Events:      len(events),
		GeneratedAt: time.Now().UTC(),
		Files:       bundleFiles(bundle),
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Bundle{}, fmt.Errorf("encoding manifest: %w", err)
	}
	bundle.Manifest = filepath.Join(outDir, "manifest.json")
	if err := os.WriteFile(bundle.Manifest, append(raw, '\n'), 0o600); err != nil {
		return Bundle{}, fmt.Errorf("writing manifest: %w", err)
	}
	return bundle, nil
}

func eventsSize(events []domain.SessionEvent) int {
	total := 0
	for _, ev := range events {
		total += len(ev.Payload) + 64
	}
	return total
}

func writeBundleFiles(sess *domain.Session, events []domain.SessionEvent, outDir string) (Bundle, error) {
	bundle := Bundle{Events: filepath.Join(outDir, "events.jsonl")}
	var lines []byte
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			return Bundle{}, fmt.Errorf("encoding event %d: %w", ev.Seq, err)
		}
		lines = append(lines, raw...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(bundle.Events, lines, 0o600); err != nil {
		return Bundle{}, fmt.Errorf("writing events: %w", err)
	}
	if src := todoPath(sess.ID); src != "" {
		if dst, ok := copyArtifact(src, filepath.Join(outDir, "todo.json")); ok {
			bundle.Todo = dst
		}
	}
	if src := handoverPath(sess); src != "" {
		if dst, ok := copyArtifact(src, filepath.Join(outDir, domain.AimanHandoverFileName)); ok {
			bundle.Handover = dst
		}
	}
	if src := reviewPath(sess.ID); src != "" {
		if dst, ok := copyArtifact(src, filepath.Join(outDir, "review.json")); ok {
			bundle.Review = dst
		}
	}
	return bundle, nil
}

func todoPath(sessionID string) string {
	dir, err := config.GetDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "todos", sessionID+".json")
}

func reviewPath(sessionID string) string {
	dir, err := config.GetDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reviews", sessionID+".json")
}

func handoverPath(sess *domain.Session) string {
	dir := sess.WorkingDirectory
	if dir == "" {
		dir = sess.WorktreePath
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, domain.AimanHandoverFileName)
}

func copyArtifact(src, dst string) (string, bool) {
	info, err := os.Stat(src)
	if err != nil || info.IsDir() {
		return "", false
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", false
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return "", false
	}
	return dst, true
}

func bundleFiles(b Bundle) map[string]string {
	files := map[string]string{"events": filepath.Base(b.Events)}
	if b.Todo != "" {
		files["todo"] = filepath.Base(b.Todo)
	}
	if b.Handover != "" {
		files["handover"] = filepath.Base(b.Handover)
	}
	if b.Review != "" {
		files["review"] = filepath.Base(b.Review)
	}
	return files
}
