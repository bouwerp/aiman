package review

// Prompt is the initial prompt of a review session.
const Prompt = "You are reviewing a pull request in this worktree. Follow the aiman skill's Review mode section. " +
	"Start with .aiman_review/meta.json, .aiman_review/files.txt and .aiman_review/diff.patch. " +
	"Do not read the PR description, commit messages or gh pr view until aiman review reveal succeeds. " +
	"The PR's changes are staged on purpose so editors mark them; never commit, stash or reset in this worktree. " +
	"Never approve the PR and never push to it."
