//go:build slackparity_c

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece C (canvases, files, upload). Do not edit.

func markdownOf(t *testing.T, raw string) string {
	t.Helper()
	var dc struct {
		Type     string `json:"type"`
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(raw), &dc); err != nil || dc.Type != "markdown" {
		t.Fatalf("document_content %q", raw)
	}
	return dc.Markdown
}

func TestCanvasCreateStandaloneAndInChannel(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("canvases.create", `{"ok":true,"canvas_id":"F1"}`)
	f.on("conversations.canvases.create", `{"ok":true,"canvas_id":"F2"}`)
	id, err := c.CanvasCreate(context.Background(), "Plan", "# Plan\n- a\n", "")
	if err != nil || id != "F1" {
		t.Fatalf("create %q %v", id, err)
	}
	p := f.called("canvases.create")[0].Params
	if p["title"] != "Plan" || markdownOf(t, p["document_content"]) != "# Plan\n- a\n" {
		t.Fatalf("canvases.create params %v", p)
	}
	id, err = c.CanvasCreate(context.Background(), "Tab", "x", "C1")
	if err != nil || id != "F2" || f.called("conversations.canvases.create")[0].Params["channel_id"] != "C1" {
		t.Fatalf("channel canvas %q %v", id, err)
	}
}

func TestCanvasReadReturnsMarkdownAndSections(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("canvases.getContent", `{"ok":true,"content":"# Plan\n\n## Risks\n"}`)
	f.on("canvases.sections.lookup", `{"ok":true,"sections":[{"id":"temp:C:abc"}]}`)
	cv, err := c.CanvasRead(context.Background(), "F1", []string{"h2"}, "Risks")
	if err != nil {
		t.Fatal(err)
	}
	if cv.ID != "F1" || cv.Markdown != "# Plan\n\n## Risks\n" || len(cv.Sections) != 1 || cv.Sections[0] != "temp:C:abc" {
		t.Fatalf("canvas %+v", cv)
	}
	var crit struct {
		SectionTypes []string `json:"section_types"`
		ContainsText string   `json:"contains_text"`
	}
	_ = json.Unmarshal([]byte(f.called("canvases.sections.lookup")[0].Params["criteria"]), &crit)
	if strings.Join(crit.SectionTypes, ",") != "h2" || crit.ContainsText != "Risks" {
		t.Fatalf("criteria %+v", crit)
	}
	if _, err := c.CanvasRead(context.Background(), "F1", nil, ""); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(f.called("canvases.sections.lookup")[1].Params["criteria"]), &crit)
	if strings.Join(crit.SectionTypes, ",") != "any_header" {
		t.Fatalf("default criteria %+v, want any_header", crit)
	}
}

func TestCanvasEditMapsOperations(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("canvases.edit", `{"ok":true}`)
	cases := []struct {
		edit    CanvasEdit
		op      string
		section string
	}{
		{CanvasEdit{Op: "append", Markdown: "a"}, "insert_at_end", ""},
		{CanvasEdit{Op: "prepend", Markdown: "a"}, "insert_at_start", ""},
		{CanvasEdit{Op: "after", Section: "s1", Markdown: "a"}, "insert_after", "s1"},
		{CanvasEdit{Op: "before", Section: "s1", Markdown: "a"}, "insert_before", "s1"},
		{CanvasEdit{Op: "replace", Section: "s1", Markdown: "a"}, "replace", "s1"},
		{CanvasEdit{Op: "replace", All: true, Markdown: "a"}, "replace", ""},
		{CanvasEdit{Op: "delete", Section: "s1"}, "delete", "s1"},
		{CanvasEdit{Op: "rename", Markdown: "New title"}, "rename", ""},
	}
	for i, tc := range cases {
		if err := c.CanvasEdit(context.Background(), "F1", tc.edit); err != nil {
			t.Fatalf("%+v: %v", tc.edit, err)
		}
		var changes []map[string]any
		_ = json.Unmarshal([]byte(f.called("canvases.edit")[i].Params["changes"]), &changes)
		if len(changes) != 1 || changes[0]["operation"] != tc.op {
			t.Fatalf("%+v sent %v, want one %s", tc.edit, changes, tc.op)
		}
		sec, _ := changes[0]["section_id"].(string)
		if sec != tc.section {
			t.Fatalf("%+v section %q, want %q", tc.edit, sec, tc.section)
		}
		if tc.op == "rename" {
			if _, ok := changes[0]["title_content"]; !ok {
				t.Fatalf("rename without title_content: %v", changes[0])
			}
		}
	}
}

func TestCanvasEditRefusesWholeCanvasReplaceWithoutAll(t *testing.T) {
	f, c := newFakeSlack(t)
	for _, e := range []CanvasEdit{
		{Op: "replace", Markdown: "x"},
		{Op: "delete"},
		{Op: "after", Markdown: "x"},
		{Op: "bogus", Markdown: "x"},
	} {
		if err := c.CanvasEdit(context.Background(), "F1", e); err == nil {
			t.Fatalf("%+v accepted", e)
		}
	}
	if len(f.called("canvases.edit")) != 0 {
		t.Fatal("a refused edit reached Slack")
	}
}

func TestFileReadDownloadsWithToken(t *testing.T) {
	f, c := newFakeSlack(t)
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxp-test" {
			t.Errorf("download auth %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, "file body")
	}))
	t.Cleanup(dl.Close)
	f.on("files.info", `{"ok":true,"file":{"id":"F5","name":"notes.txt","mimetype":"text/plain","size":9,"url_private":"`+dl.URL+`/f"}}`)
	var buf bytes.Buffer
	info, err := c.FileRead(context.Background(), "F5", &buf)
	if err != nil || info.Name != "notes.txt" || buf.String() != "file body" {
		t.Fatalf("file %+v %q %v", info, buf.String(), err)
	}
	if fc := f.called("files.info")[0]; fc.Params["file"] != "F5" {
		t.Fatalf("files.info %+v", fc)
	}
}

func TestUploadRunsBothSteps(t *testing.T) {
	f, c := newFakeSlack(t)
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
	}))
	t.Cleanup(up.Close)
	f.on("files.getUploadURLExternal", `{"ok":true,"upload_url":"`+up.URL+`/u","file_id":"F7"}`)
	f.on("files.completeUploadExternal", `{"ok":true,"files":[{"id":"F7","title":"Report"}]}`)
	id, err := c.Upload(context.Background(), UploadRequest{
		Filename: "r.txt", Data: []byte("hello"), Channel: "C1", Thread: "1.0", Comment: "see this", Title: "Report",
	})
	if err != nil || id != "F7" || string(got) != "hello" {
		t.Fatalf("upload %q %v body %q", id, err, got)
	}
	g := f.called("files.getUploadURLExternal")[0]
	if !strings.HasPrefix(g.ContentType, "application/x-www-form-urlencoded") || g.Params["filename"] != "r.txt" || g.Params["length"] != "5" {
		t.Fatalf("getUploadURLExternal %+v", g)
	}
	p := f.called("files.completeUploadExternal")[0].Params
	var files []map[string]string
	_ = json.Unmarshal([]byte(p["files"]), &files)
	if len(files) != 1 || files[0]["id"] != "F7" || files[0]["title"] != "Report" || p["channel_id"] != "C1" || p["thread_ts"] != "1.0" || p["initial_comment"] != "see this" {
		t.Fatalf("complete params %v", p)
	}
}
