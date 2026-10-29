package aimanskill

import _ "embed"

// Text is the aiman skill installed as SKILL.md.
//
//go:embed SKILL.md
var Text string

// TemplateName is the design page skeleton's file name beside SKILL.md.
const TemplateName = "design-template.md"

// Template is the design page skeleton the skill's Design first section uses.
//
//go:embed design-template.md
var Template string
