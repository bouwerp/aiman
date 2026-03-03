package mutagen

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/bouwerp/aiman/internal/domain"
)

type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) StartSync(ctx context.Context, localPath, remotePath string) error {
	// mutagen sync create --name <id> localPath remotePath
	return nil
}

func (e *Engine) StopSync(ctx context.Context) error {
	return nil
}

func (e *Engine) GetStatus(ctx context.Context) (string, error) {
	return "", nil
}

type mutagenSession struct {
	Identifier string `json:"identifier"`
	Alpha      struct {
		URL string `json:"url"`
	} `json:"alpha"`
	Beta struct {
		URL string `json:"url"`
	} `json:"beta"`
	Status string `json:"status"`
}

func (e *Engine) ListSyncSessions(ctx context.Context) ([]domain.SyncSession, error) {
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "list", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list mutagen sessions: %w, output: %s", err, string(output))
	}

	var sessions []mutagenSession
	if err := json.Unmarshal(output, &sessions); err != nil {
		return nil, fmt.Errorf("failed to parse mutagen output: %w", err)
	}

	results := make([]domain.SyncSession, len(sessions))
	for i, s := range sessions {
		results[i] = domain.SyncSession{
			ID:         s.Identifier,
			LocalPath:  s.Alpha.URL,
			RemotePath: s.Beta.URL,
			Status:     s.Status,
		}
	}

	return results, nil
}
