package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDraftCreatesRichTextDraft(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("drafts.create", `{"ok":true,"draft":{"id":"Dr1"}}`)
	d, err := c.Draft(context.Background(), DraftRequest{Channel: "C1", Text: "line 1\nline 2", Thread: "1.0", Broadcast: true})
	if err != nil || d.ID != "Dr1" || d.Channel != "C1" || d.Thread != "1.0" {
		t.Fatalf("draft %+v %v", d, err)
	}
	fc := f.called("drafts.create")[0]
	if !strings.HasPrefix(fc.ContentType, "application/x-www-form-urlencoded") {
		t.Fatalf("content type %q", fc.ContentType)
	}
	var dest []map[string]any
	if err := json.Unmarshal([]byte(fc.Params["destinations"]), &dest); err != nil || len(dest) != 1 ||
		dest[0]["channel_id"] != "C1" || dest[0]["thread_ts"] != "1.0" || dest[0]["broadcast"] != true {
		t.Fatalf("destinations %q", fc.Params["destinations"])
	}
	if !strings.Contains(fc.Params["blocks"], `"text":"line 1\nline 2"`) || !strings.Contains(fc.Params["blocks"], `"rich_text_section"`) {
		t.Fatalf("blocks %q", fc.Params["blocks"])
	}
	if fc.Params["client_msg_id"] == "" || fc.Params["file_ids"] != "[]" {
		t.Fatalf("params %v", fc.Params)
	}
}

func TestDraftWithoutThreadSendsChannelOnly(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("drafts.create", `{"ok":true,"draft":{"id":"Dr2"}}`)
	if _, err := c.Draft(context.Background(), DraftRequest{Channel: "C1", Text: "x", Broadcast: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.called("drafts.create")[0].Params["destinations"]; got != `[{"channel_id":"C1"}]` {
		t.Fatalf("destinations %q", got)
	}
}

func TestDraftExistsErrorHasHint(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("drafts.create", `{"ok":false,"error":"attached_draft_exists"}`)
	_, err := c.Draft(context.Background(), DraftRequest{Channel: "C1", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "attached_draft_exists") || !strings.Contains(err.Error(), "delete it in Slack") {
		t.Fatalf("err %v", err)
	}
}
