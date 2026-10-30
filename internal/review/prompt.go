package review

// Prompt is the initial prompt of a review session. The agent runs in an
// aiman-owned scratch directory, never in the PR worktree.
const Prompt = "You are reviewing a pull request in this worktree. " +
	"The pull request is checked out at the path in .aiman_review/meta.json (worktree); read it there. " +
	"Follow the aiman skill's Review mode section. " +
	"Start with .aiman_review/meta.json, .aiman_review/files.txt and .aiman_review/diff.patch. " +
	"Do not read the PR description, commit messages or gh pr view until aiman review reveal succeeds. " +
	"The PR's changes are staged on purpose so editors mark them; never commit, stash or reset in this worktree. " +
	"Never approve the PR and never push to it. " +
	"Run PR code (tests, builds, scripts) only with aiman review run, which uses a sandbox; never run it directly on this host."
