package agenthook

// WriteStatusLineWrapper writes ~/.aiman/hooks/claude-statusline.sh, which
// records through aimanBin in the background and always runs the original
// command. It returns the script's path. Stub until P4.
func WriteStatusLineWrapper(_, _ string) (string, error) { return "", nil }

// WrapStatusLine points the statusLine command in Claude settings at the wrapper
// and saves the whole original object under home. It reports whether root
// changed; an already wrapped command changes nothing. Stub until P4.
func WrapStatusLine(_ string, _ map[string]any) (bool, error) { return false, nil }

// UnwrapStatusLine restores the saved object. Stub until P4.
func UnwrapStatusLine(_ string, _ map[string]any) (bool, error) { return false, nil }
