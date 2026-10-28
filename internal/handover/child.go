package handover

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// File names inside a handover child's scratch directory. The brief and the
// mechanical render are inputs; handover.md and memories.json are outputs, and
// the done marker is written last so its presence means the outputs are final.
const (
	BriefFile      = "brief.md"
	MechanicalFile = "mechanical.md"
	HandoverFile   = "handover.md"
	MemoriesFile   = "memories.json"
	DoneFile       = "done"
)

// MaxChildHandoverBytes caps the handover a child may produce.
const MaxChildHandoverBytes = 64 << 10

// MaxChildMemoriesBytes caps the memories.json a child may produce.
const MaxChildMemoriesBytes = 256 << 10

// BriefInput is what a handover child is told. All paths are absolute because
// the child's working directory is not guaranteed to be Dir.
type BriefInput struct {
	Dir        string // scratch directory holding the brief, inputs and outputs
	Transcript string // agent transcript (Session.AgentSessionPath); "" when unknown
	Worktree   string // worker worktree, read-only, may already be removed
	Reason     string // "archive" | "forget" | "restart" | "agent_switch" | "pre_compact"
}

// childMemoriesSchema is the shape of memories.json.
const childMemoriesSchema = `{
  "candidates": [
    {
      "type": "decision | pitfall | convention | fact",
      "title": "short title",
      "abstract": "one sentence",
      "body": "the detail a future session needs"
    }
  ]
}`

// ChildBrief is the prompt file for a handover child agent.
func ChildBrief(in BriefInput) string {
	path := func(name string) string { return filepath.Join(in.Dir, name) }
	transcript := "No transcript was recorded for this session; rely on the mechanical handover and the worktree."
	if in.Transcript != "" {
		transcript = "Agent transcript: " + in.Transcript
	}

	var b strings.Builder
	b.WriteString("# Handover brief\n\n")
	b.WriteString("You write the handover for another agent's coding session. ")
	b.WriteString("A new agent will continue that session from your handover, so record what was decided and why, not only what changed.\n\n")

	b.WriteString("## Inputs\n\n")
	fmt.Fprintf(&b, "- Reason: %s\n", in.Reason)
	fmt.Fprintf(&b, "- This brief: %s\n", path(BriefFile))
	fmt.Fprintf(&b, "- Mechanical handover (pane tail, to-dos, notes, diffstat): %s\n", path(MechanicalFile))
	fmt.Fprintf(&b, "- %s\n", transcript)
	fmt.Fprintf(&b, "- Worker worktree: %s (read-only; it may be gone)\n\n", in.Worktree)

	b.WriteString("## Outputs\n\n")
	fmt.Fprintf(&b, "1. Write the handover to %s. It must contain each of these headings on its own line, in this order:\n\n", path(HandoverFile))
	for _, h := range RequiredSections {
		fmt.Fprintf(&b, "   %s\n", h)
	}
	fmt.Fprintf(&b, "\n   Keep error and test output verbatim. Keep the file under %d KiB.\n\n", MaxChildHandoverBytes>>10)
	fmt.Fprintf(&b, "2. Write memory candidates to %s: at most %d durable notes a future session on this repository needs. ", path(MemoriesFile), domain.MaxMemoryCandidates)
	b.WriteString("Each type is decision, pitfall, convention or fact. Do not include a status report. ")
	b.WriteString("Use an empty candidates array when nothing is worth keeping. The JSON shape is:\n\n")
	b.WriteString("```json\n" + childMemoriesSchema + "\n```\n\n")
	fmt.Fprintf(&b, "3. Write the done file last: create %s only after both files above are complete.\n\n", path(DoneFile))

	b.WriteString("## Rules\n\n")
	b.WriteString("- Do not edit code or any file outside the outputs above. Treat the worktree as read-only.\n")
	b.WriteString("- Do not commit and do not push.\n")
	b.WriteString("- Do not change aiman to-dos, send steering notes, or start child sessions.\n")
	return b.String()
}

// ReadChildFile reads at most limit+1 bytes of path, enough for a parser to
// see that the file is over limit without loading all of it.
func ReadChildFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is inside the run's scratch directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, int64(limit)+1))
}

// ParseChildHandover validates a child's handover.md and returns its text.
func ParseChildHandover(raw []byte) (string, error) {
	if len(raw) > MaxChildHandoverBytes {
		return "", fmt.Errorf("handover is %d bytes, limit %d", len(raw), MaxChildHandoverBytes)
	}
	md := string(raw)
	if strings.TrimSpace(md) == "" {
		return "", fmt.Errorf("handover is empty")
	}
	if missing := MissingSections(md); len(missing) > 0 {
		return "", fmt.Errorf("handover is missing %s", strings.Join(missing, ", "))
	}
	return md, nil
}

// ParseChildCandidates reads a child's memories.json. Invalid JSON, or a file
// over MaxChildMemoriesBytes, yields nil.
func ParseChildCandidates(raw []byte) []domain.MemoryCandidate {
	if len(raw) > MaxChildMemoriesBytes {
		return nil
	}
	var result struct {
		Candidates []domain.MemoryCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return domain.FilterMemoryCandidates(result.Candidates)
}
