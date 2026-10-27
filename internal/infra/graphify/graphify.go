// Package graphify builds the remote shell snippets that detect the graphify
// CLI and refresh a worktree's code graph in the background.
package graphify

import (
	"context"
	"fmt"
	"strings"
)

// pathPrefix puts the usual user-local install dirs on PATH; non-login SSH
// shells often lack them.
const pathPrefix = "export PATH=\"$HOME/.local/bin:$HOME/bin:$HOME/.cargo/bin:/usr/local/bin:$PATH\"; "

// PathPrefix is the PATH export other remote tool probes share with the
// graphify probe.
const PathPrefix = pathPrefix

// TaskFileLine is the .aiman_task.md guardrail shown when graphify is present.
const TaskFileLine = "- **Code graph.** `graphify` is installed and a graph of this repository is being built in the background into `graphify-out/`. For codebase questions run `graphify query \"<question>\"` first; use `graphify path \"A\" \"B\"` for relationships and `graphify explain \"X\"` for one concept.\n"

const probeMarker = "GRAPHIFY=1"

// ProbeCommand prints GRAPHIFY=1 when graphify is on PATH.
func ProbeCommand() string {
	return pathPrefix + "command -v graphify >/dev/null 2>&1 && echo " + probeMarker + " || true"
}

// Available reports whether graphify is installed where ex runs.
func Available(ctx context.Context, ex interface {
	Execute(context.Context, string) (string, error)
}) bool {
	out, err := ex.Execute(ctx, ProbeCommand())
	return err == nil && strings.Contains(out, probeMarker)
}

// BackgroundUpdateScript runs `nice graphify update .` in worktree, detached,
// and returns at once. It does nothing unless graphify is installed and
// worktree is a git checkout. graphify-out/ is added to the repository's
// info/exclude when nothing ignores it yet, so the graph never shows up in
// git status; the log goes to the worktree's git dir.
func BackgroundUpdateScript(worktree string) string {
	// The redirections sit on the backgrounded group: the forked subshell
	// would otherwise hold the caller's stdout open until graphify exits.
	return pathPrefix + fmt.Sprintf(`wt=%s
command -v graphify >/dev/null 2>&1 || exit 0
git -C "$wt" rev-parse >/dev/null 2>&1 || exit 0
if ! git -C "$wt" check-ignore -q graphify-out/ 2>/dev/null; then
  ex="$(cd "$wt" && cd "$(git rev-parse --git-common-dir)" && pwd)/info/exclude"
  mkdir -p "$(dirname "$ex")"
  grep -qxF /graphify-out/ "$ex" 2>/dev/null || printf '%%s\n' /graphify-out/ >>"$ex"
fi
log="$(git -C "$wt" rev-parse --absolute-git-dir)/aiman-graphify.log"
(cd "$wt" && exec nohup nice graphify update .) >"$log" 2>&1 </dev/null &
`, shellQuote(worktree))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
