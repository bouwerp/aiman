package agenthook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/fsutil"
	"github.com/bouwerp/aiman/internal/shellx"
)

// statusLineMarker identifies a wrapped command. Detection looks for it in the
// command text, never at a path, so a moved script or home still counts.
const statusLineMarker = "aiman-claude-statusline-wrapper"

const statusLineKey = "statusLine"

func statusLineScriptPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", "claude-statusline.sh")
}

func statusLineSavedPath(home string) string {
	return filepath.Join(home, ".aiman", "limits", "claude-statusline.original.json")
}

// statusLineScript copies stdin to the recorder in the background and to
// stdout for the original command. stdin stays in a shell variable and never
// reaches a temp file: the working directory, transcript path, session id and
// cost it carries must not reach disk.
func statusLineScript(aimanBin string) string {
	return `#!/bin/sh
bin=` + shellx.Quote(aimanBin) + `
# The trailing x keeps trailing newlines through the substitution.
input=$(cat; printf x)
input=${input%x}
if [ -x "$bin" ]; then
  (printf '%s' "$input" | "$bin" limits record-claude) >/dev/null 2>&1 &
fi
printf '%s' "$input"
`
}

// WriteStatusLineWrapper writes the wrapper script, leaving it alone when it is
// already current, and returns its path.
func WriteStatusLineWrapper(home, aimanBin string) (string, error) {
	path := statusLineScriptPath(home)
	if _, err := ensureHookScript(path, statusLineScript(aimanBin)); err != nil {
		return "", err
	}
	return path, nil
}

// wrappedStatusLineCommand is what Claude Code runs. The original sits inline
// in a brace group, so it keeps running in whichever shell Claude Code itself
// uses and needs no quoting. The pipeline's status is the original's, and a
// missing script still lets the original paint. The newline before the closing
// brace keeps a trailing comment or heredoc in the original valid.
func wrappedStatusLineCommand(home, original string) string {
	head := ": " + statusLineMarker + "; sh " + shellx.Quote(statusLineScriptPath(home))
	// An empty brace group is a syntax error, so a blank original gets no pipe.
	if strings.TrimSpace(original) == "" {
		return head + " >/dev/null"
	}
	return head + " | { " + original + "\n}"
}

// saveOriginalStatusLine stores what restoring needs, at 0600 in a 0700 directory.
func saveOriginalStatusLine(home string, saved map[string]any) error {
	path := statusLineSavedPath(home)
	body, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if _, err := fsutil.WriteIfChanged(path, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// WrapStatusLine points root's status line at the wrapper, changing only its
// command, after saving the original object. A command that is already wrapped
// changes nothing and writes nothing, so a serve restart cannot make the status
// line flicker. Any other command is the user's and becomes the new original.
func WrapStatusLine(home string, root map[string]any) (bool, error) {
	raw, present := root[statusLineKey]
	if !present {
		if err := saveOriginalStatusLine(home, map[string]any{"existed": false}); err != nil {
			return false, err
		}
		root[statusLineKey] = map[string]any{"type": "command", "command": wrappedStatusLineCommand(home, "")}
		return true, nil
	}
	line, ok := raw.(map[string]any)
	if !ok {
		return false, fmt.Errorf("%s in Claude settings is %T, not an object", statusLineKey, raw)
	}
	command, _ := line["command"].(string)
	if strings.Contains(command, statusLineMarker) {
		return false, nil
	}
	if err := saveOriginalStatusLine(home, map[string]any{"existed": true, statusLineKey: line}); err != nil {
		return false, err
	}
	line["command"] = wrappedStatusLineCommand(home, command)
	return true, nil
}

// UnwrapStatusLine restores the saved status line. It runs on every serve start
// with the wrapper off, so a command that is not wrapped changes nothing and
// nothing is restored over the user's current settings. When the saved file is
// missing or corrupt it returns an error and the wrapped line keeps working.
//
// The saved file stays: settings are written after this returns, so deleting it
// here could strand a wrapped settings file. The next wrap overwrites it.
func UnwrapStatusLine(home string, root map[string]any) (bool, error) {
	line, ok := root[statusLineKey].(map[string]any)
	if !ok {
		return false, nil
	}
	command, _ := line["command"].(string)
	if !strings.Contains(command, statusLineMarker) {
		return false, nil
	}
	saved, err := readOriginalStatusLine(home)
	if err != nil {
		return false, err
	}
	if saved == nil {
		delete(root, statusLineKey)
		return true, nil
	}
	root[statusLineKey] = saved
	return true, nil
}

// readOriginalStatusLine returns the saved object, or nil when no status line
// existed before wrapping.
func readOriginalStatusLine(home string) (map[string]any, error) {
	path := statusLineSavedPath(home)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var saved struct {
		Existed    *bool          `json:"existed"`
		StatusLine map[string]any `json:"statusLine"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if saved.Existed == nil {
		return nil, errors.New(path + ": no existed field")
	}
	if !*saved.Existed {
		return nil, nil
	}
	if saved.StatusLine == nil {
		return nil, errors.New(path + ": no saved status line")
	}
	return saved.StatusLine, nil
}
