// Package kit holds the artifact theme kit that publish copies into every
// version under kit/.
package kit

import "embed"

// FS holds the kit files at their published paths (relative to kit/).
//
//go:embed theme.css kit.js
var FS embed.FS
