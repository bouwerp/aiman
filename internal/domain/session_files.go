package domain

const (
	AimanTaskFileName           = ".aiman_task.md"
	AimanPromptFileName         = ".aiman_prompt"
	AimanSessionSummaryFileName = ".aiman_session_summary.md"
	AimanContextFileName        = ".aiman_context.md"
	// AimanSessionIDFileName is the worktree marker Muse hooks read. Muse
	// strips AIMAN_ID from the hook environment, so the id cannot travel
	// as a variable.
	AimanSessionIDFileName = ".aiman_session_id"
	AimanGeneratedFileGlob = ".aiman_*"
)
