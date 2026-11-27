package agenthook

import "path/filepath"

// ArtifactGuardMarker identifies the generated artifact guard script.
const ArtifactGuardMarker = "artifact-guard.sh"

// artifactGuardMatcher matches Claude Code's built-in Artifact tool, which
// publishes pages to claude.ai.
const artifactGuardMatcher = "Artifact"

func artifactGuardPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", ArtifactGuardMarker)
}

// artifactGuardScript refuses Claude's Artifact tool inside an Aiman session
// (exit 2, the reason on stderr for the agent) unless the call only reads,
// lists or opens an existing artifact, updates an existing claude.ai artifact
// (a claude.ai url), or declares runtime capabilities. Those two cover pages
// that need claude.ai features aiman pages lack: a page database, asking
// Claude, browser storage, loading their own files. Delete is always refused.
// Outside a session it allows all.
// Pages a session makes go through `aiman artifact publish`, which hosts them
// on the operator's own Cloudflare site behind Google sign-in.
const artifactGuardScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
input=$(cat)
case "$input" in
*'"action":"delete"'*|*'"action": "delete"'*) ;;
*'"action":"read"'*|*'"action": "read"'*|*'"action":"list"'*|*'"action": "list"'*|*'"action":"open"'*|*'"action": "open"'*) exit 0 ;;
*'"url":"https://claude.ai/'*|*'"url": "https://claude.ai/'*) exit 0 ;;
*'"capabilities":{"'*|*'"capabilities": {"'*) exit 0 ;;
esac
echo "In an Aiman session, publish pages with aiman artifact publish (the aiman skill's Artifacts section), not Claude's Artifact tool or any other host. Claude's Artifact tool is allowed only to read, list or open claude.ai artifacts, to update an existing claude.ai artifact (pass its url), or for a page that declares capabilities (page database, asking Claude, storage). If aiman artifact publish fails, tell the operator the command you ran and its error." >&2
exit 2
`
