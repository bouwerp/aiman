package domain

// Checklist is a domain checklist file the session agent works through
// before it finishes.
type Checklist struct {
	Name      string // file base name without .md
	Path      string // absolute path on the session host
	Source    string // "repo" | "user"
	AppliesTo []string
}

// QualityKit is the per-session content written into .aiman_task.md: the
// conditions that define done and the checklists that apply.
type QualityKit struct {
	Repo       string      // owner/name; carried for launch overrides even when the kit is empty
	Role       SessionRole // "" is an ordinary session
	DoneWhen   []string    // nil at restart means "leave the existing block"
	Checklists []Checklist
}

// Empty reports whether the kit has no conditions and no checklists.
func (k QualityKit) Empty() bool {
	return len(k.DoneWhen) == 0 && len(k.Checklists) == 0
}
