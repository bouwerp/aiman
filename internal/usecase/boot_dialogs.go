package usecase

import (
	"fmt"
	"strings"

	infraAgent "github.com/bouwerp/aiman/internal/infra/agent"
)

// captureSGRFilter removes SGR from the JSON `aiman pty capture` prints, where
// ESC is the six characters \u001b. Captures are styled and agents change style
// inside a phrase (Codex's "? for shortcuts" and "Press enter to continue"), so
// a pattern would otherwise miss the visible text.
const captureSGRFilter = `sed 's/\\u001b\[[0-9;:]*m//g'`

// bootUpdateDialogBranch is the boot-wait shell fragment that skips a startup
// update dialog. It reads only the bottom rows of the screen, where a live
// dialog sits, and presses the dialog's skip key; never Enter, which installs.
// After a dismissal it sets upd=1 and restarts the loop, since the agent may
// still show a trust dialog or need time to draw its composer.
//
// The patterns are single-quoted for the remote shell and run through grep -E
// against the JSON that `aiman pty capture` prints with its SGR removed
// (captureSGRFilter), so they must not contain characters JSON escapes.
func bootUpdateDialogBranch(sessionID string) string {
	if len(infraAgent.UpdateDialogs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tail=$(aiman pty capture %q --lines %d 2>/dev/null | %s || true); ",
		sessionID, infraAgent.UpdateDialogTailLines, captureSGRFilter)
	for _, d := range infraAgent.UpdateDialogs {
		fmt.Fprintf(&b, "if printf '%%s' \"$tail\" | grep -qE %s; then "+
			"aiman pty input %q --key %s >/dev/null 2>&1; upd=1; sleep 2; attempt=$((attempt+1)); continue; "+
			"fi; ", shellQuote(d.Re), sessionID, d.Key)
	}
	return b.String()
}
