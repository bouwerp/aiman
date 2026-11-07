package transcript

import (
	"fmt"
	"strings"
)

// toolCall starts a running tool call keyed by toolCallId.
func (g *grokNormaliser) toolCall(l *grokLine) []Delta {
	id := l.upd.ToolCallID
	if id == "" {
		return nil
	}
	if it, ok := g.calls[id]; ok {
		applyGrokTool(it.ToolCall, &l.upd)
		return g.update(l, it)
	}
	return g.add(l, g.newCall(l))
}

// newCall stores a running tool call for the line's toolCallId.
func (g *grokNormaliser) newCall(l *grokLine) *Item {
	id := l.upd.ToolCallID
	it := &Item{ID: "tool:" + id, Kind: KindToolCall, At: l.at, ToolCall: &ToolCall{ToolID: id, Status: ToolRunning}}
	applyGrokTool(it.ToolCall, &l.upd)
	g.calls[id] = it
	return it
}

// toolUpdate moves a tool call's status and result. An update for a call
// the file never started adds the call.
func (g *grokNormaliser) toolUpdate(l *grokLine) []Delta {
	if l.upd.ToolCallID == "" {
		return nil
	}
	it, known := g.calls[l.upd.ToolCallID]
	if !known {
		it = g.newCall(l)
	}
	tc := it.ToolCall
	if tc.Name == "" || tc.Input == "" {
		applyGrokTool(tc, &l.upd)
	}
	if res := l.upd.contentText(); res != "" {
		tc.Result = res
	} else if out := rawJSON(l.upd.RawOutput); out != "" {
		tc.Result = out
	}
	if st := grokToolStatus(l.upd.status()); st != "" && tc.Status == ToolRunning {
		tc.Status = st
		if !it.At.IsZero() && l.at.After(it.At) {
			tc.DurationMS = l.at.Sub(it.At).Milliseconds()
		}
	}
	if !known {
		return g.add(l, it)
	}
	return g.update(l, it)
}

// applyGrokTool fills the name, summary and input a call does not have yet.
func applyGrokTool(tc *ToolCall, u *grokUpdate) {
	if tc.Name == "" {
		tc.Name = u.ToolMeta.Tool.Name
	}
	if tc.Name == "" {
		tc.Name = u.Title
	}
	if tc.Summary == "" {
		tc.Summary = u.Title
	}
	if tc.Input == "" {
		tc.Input = rawJSON(u.RawInput)
	}
}

// grokToolStatus maps an ACP tool status to a terminal ToolCall status, or
// "" while the call is still pending or in progress.
func grokToolStatus(s string) string {
	switch strings.ToLower(s) {
	case "completed":
		return ToolOK
	case "failed":
		return ToolError
	case "cancelled", "canceled", "rejected", "denied":
		return ToolDenied
	}
	return ""
}

// hookExecution reports failed hook runs; a hook whose runs all succeeded
// gives nothing. Only the hook's name, event and error reach the text.
func (g *grokNormaliser) hookExecution(l *grokLine) []Delta {
	var failed []string
	for _, r := range l.upd.Runs {
		if r.Status.Status == "success" {
			continue
		}
		msg := r.Name + ": " + r.Status.Status
		if r.Status.Error != "" {
			msg += ": " + r.Status.Error
		}
		failed = append(failed, msg)
	}
	if len(failed) == 0 {
		return nil
	}
	text := "Hook " + l.upd.EventName + " failed: " + strings.Join(failed, "; ")
	return g.system(l, text)
}

// grokCompactionText is the system text for a compaction update, or "".
func grokCompactionText(u *grokUpdate) string {
	switch u.SessionUpdate {
	case "auto_compact_started":
		return fmt.Sprintf("Compacting context (%.0f%% of %.0f tokens)", u.Percentage, u.ContextWindow)
	case "auto_compact_completed":
		return fmt.Sprintf("Context compacted: %.0f → %.0f tokens", u.TokensBefore, u.TokensAfter)
	case "auto_compact_cancelled":
		return "Context compaction cancelled"
	case "compaction_checkpoint":
		return "Compaction checkpoint saved"
	}
	return ""
}
