package agenthook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	deepcodeMaxProjectCode = 64
	deepcodeProjectHashLen = 16

	// DeepcodeLauncher is the shell token aiman runs instead of the bare
	// deepcode binary. The script is installed next to the hook reporter and
	// expands $HOME when the session shell starts it.
	DeepcodeLauncher = `"$HOME/.aiman/hooks/deepcode-launch.sh"`
)

// DeepcodeLaunchScript watches Deep Code's per-project session index and then
// runs the CLI. Deep Code has no hook events; the index is where conversation
// id, title, and status (including ask_permission and waiting_for_user) land.
// The CLI stays in the foreground so this process can still report session end.
const DeepcodeLaunchScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || { deepcode "$@"; exit $?; }
[ -n "${AIMAN_ID:-}" ] || { deepcode "$@"; exit $?; }
bin="${AIMAN_BIN_PATH:-}"
if [ -z "$bin" ] || [ ! -x "$bin" ]; then
  if command -v aiman >/dev/null 2>&1; then
    bin=aiman
  elif [ -x "$HOME/.local/bin/aiman" ]; then
    bin="$HOME/.local/bin/aiman"
  else
    deepcode "$@"
    exit $?
  fi
fi
resume=""
prev=""
for arg in "$@"; do
  case "$prev" in
    --resume|-r) resume=$arg ;;
  esac
  case "$arg" in
    --resume=*|-r=*) resume=${arg#*=} ;;
  esac
  prev=$arg
done
case "$resume" in
  *[!A-Za-z0-9-]*) resume="" ;;
esac
dir=$(pwd)
if [ -n "$resume" ]; then
  "$bin" session deepcode-watch --dir "$dir" --resume "$resume" >/dev/null 2>&1 &
else
  "$bin" session deepcode-watch --dir "$dir" >/dev/null 2>&1 &
fi
wpid=$!
deepcode "$@"
status=$?
kill "$wpid" 2>/dev/null || true
wait "$wpid" 2>/dev/null || true
exit "$status"
`

// DeepcodeAsk is one pending permission prompt from sessions-index.json.
type DeepcodeAsk struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Scopes  []string `json:"scopes"`
}

// DeepcodeEntry is one conversation in a project's session index.
type DeepcodeEntry struct {
	ID         string
	Summary    string
	Status     string
	FailReason string
	UpdateTime time.Time
	Ask        []DeepcodeAsk
}

// DeepcodeWatch polls one project index until ctx is cancelled, which the
// launch script does when the CLI exits.
type DeepcodeWatch struct {
	Home     string
	Dir      string
	ResumeID string
	Interval time.Duration
	Now      func() time.Time
	Emit     func(Report)
	// IgnoreEnv lets tests run without AIMAN_ENV. The launch script only
	// starts the watcher when that variable is set.
	IgnoreEnv bool
}

// DeepcodeProjectCode is the directory name under ~/.deepcode/projects.
// It follows Deep Code's getProjectCode: slash-separated paths stay as the
// legacy encoding until they exceed 64 characters, then a hashed suffix.
func DeepcodeProjectCode(projectRoot string) string {
	legacy := strings.NewReplacer("\\", "-", "/", "-", ":", "").Replace(projectRoot)
	if len(legacy) <= deepcodeMaxProjectCode {
		return legacy
	}
	normalized := filepath.Clean(projectRoot)
	sum := sha256.Sum256([]byte(normalized))
	hash := hex.EncodeToString(sum[:])[:deepcodeProjectHashLen]
	prefixLimit := deepcodeMaxProjectCode - deepcodeProjectHashLen - 1
	prefix := sanitizeProjectPart(filepath.Base(normalized))
	prefix = trimRunes(prefix, prefixLimit)
	prefix = strings.TrimRight(prefix, "-.")
	if prefix == "" {
		prefix = "project"
	}
	return prefix + "-" + hash
}

func sanitizeProjectPart(value string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		safe := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !safe {
			r = '-'
		}
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "-.")
}

func trimRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// DeepcodeChooseEntry picks the conversation this process should report.
// An explicit resume id wins. Otherwise only an entry touched at or after
// launch counts, so an old project history is not treated as this session.
func DeepcodeChooseEntry(entries []DeepcodeEntry, resumeID string, started time.Time) (DeepcodeEntry, bool) {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID != "" {
		for _, e := range entries {
			if e.ID == resumeID {
				return e, true
			}
		}
		return DeepcodeEntry{}, false
	}
	var best DeepcodeEntry
	found := false
	for _, e := range entries {
		if e.UpdateTime.IsZero() || e.UpdateTime.Before(started) {
			continue
		}
		if !found || e.UpdateTime.After(best.UpdateTime) {
			best = e
			found = true
		}
	}
	return best, found
}

// DeepcodeReport maps one index entry onto the hook report aiman already stores.
func DeepcodeReport(entry DeepcodeEntry, projectDir string, seq int64) Report {
	state, message := deepcodeState(entry)
	path := ""
	if id := strings.TrimSpace(entry.ID); id != "" && strings.TrimSpace(projectDir) != "" {
		path = filepath.Join(projectDir, id+".jsonl")
	}
	return Report{
		Native:  Native{ID: strings.TrimSpace(entry.ID), Path: path},
		State:   state,
		Source:  SourceLifecycle,
		Message: message,
		Title:   strings.TrimSpace(entry.Summary),
		Seq:     seq,
	}
}

func deepcodeState(entry DeepcodeEntry) (domain.AgentState, string) {
	switch strings.TrimSpace(entry.Status) {
	case "processing", "pending":
		return domain.AgentStateWorking, ""
	case "waiting_for_user", "ask_permission", "permission_denied":
		msg := deepcodeAskMessage(entry.Ask)
		if msg == "" {
			msg = strings.TrimSpace(entry.Status)
		}
		return domain.AgentStateWaitingInput, msg
	case "failed":
		return domain.AgentStateErrored, strings.TrimSpace(entry.FailReason)
	case "completed", "interrupted":
		return domain.AgentStateIdle, ""
	default:
		return "", ""
	}
}

func deepcodeAskMessage(asks []DeepcodeAsk) string {
	if len(asks) == 0 {
		return ""
	}
	a := asks[0]
	msg := strings.TrimSpace(a.Name)
	if cmd := strings.TrimSpace(a.Command); cmd != "" {
		if msg != "" {
			msg += ": "
		}
		msg += cmd
	}
	if len(a.Scopes) > 0 {
		msg = strings.TrimSpace(msg + " (" + strings.Join(a.Scopes, ", ") + ")")
	}
	return msg
}

// WatchDeepcode polls the project index until ctx is cancelled, then reports
// the process as ended. Cancellation is the CLI exiting, not a turn boundary.
func WatchDeepcode(ctx context.Context, cfg DeepcodeWatch) error {
	if !cfg.IgnoreEnv && (os.Getenv("AIMAN_ENV") != "1" || strings.TrimSpace(os.Getenv("AIMAN_ID")) == "") {
		return nil
	}
	home, dir, err := deepcodeWatchPaths(cfg)
	if err != nil {
		return err
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	var seq int64
	var last string
	var current DeepcodeEntry
	projectDir := filepath.Join(home, ".deepcode", "projects", DeepcodeProjectCode(dir))
	emit := func(r Report) {
		if cfg.Emit != nil {
			cfg.Emit(r)
		}
	}
	poll := func() {
		entry, ok := readDeepcodeEntry(projectDir, cfg.ResumeID, started)
		if !ok {
			return
		}
		current = entry
		seq++
		r := DeepcodeReport(entry, projectDir, seq)
		sig := r.ID + "\x00" + string(r.State) + "\x00" + r.Message + "\x00" + r.Title
		if sig == last {
			seq--
			return
		}
		last = sig
		emit(r)
	}
	poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			poll()
			seq++
			end := DeepcodeReport(current, projectDir, seq)
			end.Ended = true
			end.State = domain.AgentStateIdle
			end.Source = SourceSessionEnd
			emit(end)
			return nil
		case <-ticker.C:
			poll()
		}
	}
}

func deepcodeWatchPaths(cfg DeepcodeWatch) (string, string, error) {
	home := strings.TrimSpace(cfg.Home)
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		home = h
	}
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		d, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		dir = d
	}
	return home, dir, nil
}

func readDeepcodeEntry(projectDir, resumeID string, started time.Time) (DeepcodeEntry, bool) {
	raw, err := os.ReadFile(filepath.Join(projectDir, "sessions-index.json"))
	if err != nil {
		return DeepcodeEntry{}, false
	}
	entries, err := parseDeepcodeIndex(raw)
	if err != nil {
		return DeepcodeEntry{}, false
	}
	return DeepcodeChooseEntry(entries, resumeID, started)
}

func parseDeepcodeIndex(raw []byte) ([]DeepcodeEntry, error) {
	var doc struct {
		Entries []struct {
			ID         string        `json:"id"`
			Summary    string        `json:"summary"`
			Status     string        `json:"status"`
			FailReason string        `json:"failReason"`
			UpdateTime string        `json:"updateTime"`
			Ask        []DeepcodeAsk `json:"askPermissions"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := make([]DeepcodeEntry, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			continue
		}
		updated, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(e.UpdateTime))
		out = append(out, DeepcodeEntry{
			ID:         id,
			Summary:    e.Summary,
			Status:     e.Status,
			FailReason: e.FailReason,
			UpdateTime: updated.UTC(),
			Ask:        e.Ask,
		})
	}
	return out, nil
}

// DeepcodeCommand replaces a bare deepcode binary with the launch script and
// leaves model env assignments in front, where the shell applies them.
func DeepcodeCommand(command string) string {
	prefix, argv := splitCommand(command)
	if len(argv) == 0 {
		return command
	}
	base := strings.ToLower(strings.Trim(argv[0], `"'`))
	if base != "deepcode" && !strings.Contains(base, "deepcode-launch.sh") {
		return command
	}
	argv[0] = DeepcodeLauncher
	out := argv[0] + restArgs(argv[1:])
	if prefix == "" {
		return out
	}
	return prefix + " " + out
}
