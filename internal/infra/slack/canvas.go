package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Canvas is a canvas read as one markdown string, plus the section ids that
// matched the lookup criteria. Section ids are what CanvasEdit targets.
type Canvas struct {
	ID       string   `json:"id"`
	Markdown string   `json:"markdown"`
	Sections []string `json:"sections"`
}

// CanvasEdit is one change to a canvas. Op is append, prepend, after, before,
// replace, delete or rename. after, before and delete need Section; replace
// needs Section unless All replaces the whole canvas. rename sets the title
// from Markdown.
type CanvasEdit struct {
	Op       string
	Section  string
	All      bool
	Markdown string
}

// defaultSectionTypes is what CanvasRead looks up when no type is given.
var defaultSectionTypes = []string{"any_header"}

func markdownContent(md string) map[string]string {
	return map[string]string{"type": "markdown", "markdown": md}
}

// CanvasCreate creates a canvas and returns its id. With channel set it is
// that channel's canvas (conversations.canvases.create), else a standalone one.
func (c *Client) CanvasCreate(ctx context.Context, title, markdown, channel string) (string, error) {
	req := map[string]any{"document_content": markdownContent(markdown)}
	method := "canvases.create"
	if channel != "" {
		method = "conversations.canvases.create"
		req["channel_id"] = channel
	}
	if title != "" {
		req["title"] = title
	}
	var out struct {
		CanvasID string `json:"canvas_id"`
	}
	if err := c.call(ctx, method, req, &out); err != nil {
		return "", err
	}
	return out.CanvasID, nil
}

// CanvasRead returns a canvas's markdown and the ids of its sections matching
// sectionTypes (default any_header) and contains.
func (c *Client) CanvasRead(ctx context.Context, id string, sectionTypes []string, contains string) (Canvas, error) {
	var content struct {
		Content string `json:"content"`
	}
	if err := c.callForm(ctx, "canvases.getContent", url.Values{"canvas_id": {id}}, &content); err != nil {
		return Canvas{}, err
	}
	if len(sectionTypes) == 0 {
		sectionTypes = defaultSectionTypes
	}
	crit := struct {
		SectionTypes []string `json:"section_types"`
		ContainsText string   `json:"contains_text,omitempty"`
	}{sectionTypes, contains}
	raw, err := json.Marshal(crit)
	if err != nil {
		return Canvas{}, fmt.Errorf("slack canvases.sections.lookup: %w", err)
	}
	var lookup struct {
		Sections []struct {
			ID string `json:"id"`
		} `json:"sections"`
	}
	v := url.Values{"canvas_id": {id}, "criteria": {string(raw)}}
	if err := c.callForm(ctx, "canvases.sections.lookup", v, &lookup); err != nil {
		return Canvas{}, err
	}
	secs := make([]string, 0, len(lookup.Sections))
	for _, s := range lookup.Sections {
		secs = append(secs, s.ID)
	}
	return Canvas{ID: id, Markdown: content.Content, Sections: secs}, nil
}

// canvasOps maps CanvasEdit.Op to the canvases.edit operation.
var canvasOps = map[string]string{
	"append":  "insert_at_end",
	"prepend": "insert_at_start",
	"after":   "insert_after",
	"before":  "insert_before",
	"replace": "replace",
	"delete":  "delete",
	"rename":  "rename",
}

// Change builds the single canvases.edit change for e, or says why e is
// refused. It calls nothing, so a bad edit never reaches Slack.
func (e CanvasEdit) Change() (map[string]any, error) {
	op, ok := canvasOps[e.Op]
	if !ok {
		return nil, fmt.Errorf("unknown canvas edit op %q (append, prepend, after, before, replace, delete, rename)", e.Op)
	}
	change := map[string]any{"operation": op}
	switch e.Op {
	case "after", "before", "delete":
		if e.Section == "" {
			return nil, fmt.Errorf("canvas edit %s needs a section", e.Op)
		}
	case "replace":
		if e.Section == "" && !e.All {
			return nil, fmt.Errorf("canvas edit replace needs a section, or all to replace the whole canvas")
		}
		if e.Section != "" && e.All {
			return nil, fmt.Errorf("canvas edit replace takes a section or all, not both")
		}
	}
	if e.Section != "" && e.Op != "append" && e.Op != "prepend" && e.Op != "rename" {
		change["section_id"] = e.Section
	}
	switch e.Op {
	case "delete":
	case "rename":
		if e.Markdown == "" {
			return nil, fmt.Errorf("canvas edit rename needs a title")
		}
		change["title_content"] = markdownContent(e.Markdown)
	default:
		if e.Markdown == "" {
			return nil, fmt.Errorf("canvas edit %s needs text", e.Op)
		}
		change["document_content"] = markdownContent(e.Markdown)
	}
	return change, nil
}

// CanvasEdit applies one change to canvas id.
func (c *Client) CanvasEdit(ctx context.Context, id string, e CanvasEdit) error {
	change, err := e.Change()
	if err != nil {
		return err
	}
	return c.call(ctx, "canvases.edit", map[string]any{
		"canvas_id": id,
		"changes":   []map[string]any{change},
	}, nil)
}
