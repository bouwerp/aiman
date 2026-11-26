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
// lists or opens an existing artifact. Outside a session it allows all.
// Pages a session makes go through `aiman artifact publish`, which hosts them
// on the operator's own Cloudflare site behind Google sign-in.
const artifactGuardScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
input=$(cat)
case "$input" in
*'"action":"read"'*|*'"action": "read"'*|*'"action":"list"'*|*'"action": "list"'*|*'"action":"open"'*|*'"action": "open"'*) exit 0 ;;
esac
echo "In an Aiman session, publish pages with aiman artifact publish (the aiman skill's Artifacts section), not Claude's Artifact tool or any other host. Reading, listing and opening existing claude.ai artifacts is allowed. If aiman artifact publish fails, tell the operator the command you ran and its error." >&2
exit 2
`
