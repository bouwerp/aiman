package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece D (Lists). Do not edit.

const listSchemaJSON = `{"id":"F1","title":"QA","list_metadata":{"schema":[
 {"id":"ColItem","key":"title","name":"Item","type":"text","is_primary_column":true},
 {"id":"ColStatus","key":"status","name":"Status","type":"select","options":{"format":"single_select","choices":[{"value":"todo","label":"To do"},{"value":"done","label":"Done"}]}},
 {"id":"ColOwner","key":"owner","name":"Owner","type":"user"},
 {"id":"ColEst","key":"estimate","name":"Estimate","type":"number"},
 {"id":"ColOK","key":"ok","name":"Checked","type":"checkbox"},
 {"id":"ColDue","key":"due","name":"Due","type":"date"}]}}`

func listFake(t *testing.T) (*fakeSlack, *Client) {
	f, c := newFakeSlack(t)
	f.on("slackLists.items.list", `{"ok":true,"list":`+listSchemaJSON+`,"items":[{"id":"Rec1","list_id":"F1","fields":[
	 {"key":"title","value":"Login fails","column_id":"ColItem"},
	 {"key":"status","value":"todo","select":["todo"],"column_id":"ColStatus"}]}],"response_metadata":{"next_cursor":"c2"}}`)
	f.on("slackLists.items.info", `{"ok":true,"list":`+listSchemaJSON+`,"record":{"id":"Rec1","list_id":"F1","fields":[]}}`)
	f.on("slackLists.items.update", `{"ok":true}`)
	f.on("slackLists.items.create", `{"ok":true,"item":{"id":"Rec9","list_id":"F1","fields":[]}}`)
	return f, c
}

func TestListReadSchemaAndRows(t *testing.T) {
	f, c := listFake(t)
	page, err := c.ListRead(context.Background(), "F1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.List.ID != "F1" || page.List.Title != "QA" || len(page.List.Columns) != 6 || page.NextCursor != "c2" {
		t.Fatalf("list %+v", page)
	}
	st := page.List.Columns[1]
	if st.ID != "ColStatus" || st.Name != "Status" || st.Type != "select" || len(st.Choices) != 2 || st.Choices[1].Label != "Done" || st.Choices[1].Value != "done" {
		t.Fatalf("status column %+v", st)
	}
	if len(page.Rows) != 1 || page.Rows[0].ID != "Rec1" || page.Rows[0].Cells["Item"] != "Login fails" {
		t.Fatalf("rows %+v", page.Rows)
	}
	p := f.called("slackLists.items.list")[0].Params
	if p["list_id"] != "F1" || p["include_list"] != "true" {
		t.Fatalf("items.list params %v", p)
	}
}

func TestListSetMapsNamesLabelsAndTypes(t *testing.T) {
	f, c := listFake(t)
	err := c.ListSet(context.Background(), "F1", "Rec1", map[string]string{
		"Status": "Done", "item": "Fixed", "Estimate": "3", "Checked": "true", "Due": "2026-11-20", "owner": "U1,U2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var cells []map[string]json.RawMessage
	_ = json.Unmarshal([]byte(f.called("slackLists.items.update")[0].Params["cells"]), &cells)
	if len(cells) != 6 {
		t.Fatalf("cells %d: %v", len(cells), cells)
	}
	byCol := map[string]map[string]json.RawMessage{}
	for _, c := range cells {
		var col, row string
		_ = json.Unmarshal(c["column_id"], &col)
		_ = json.Unmarshal(c["row_id"], &row)
		if row != "Rec1" {
			t.Fatalf("row_id %q", row)
		}
		byCol[col] = c
	}
	if s := string(byCol["ColStatus"]["select"]); s != `["done"]` {
		t.Errorf("select %s, want the choice value", s)
	}
	if rt := string(byCol["ColItem"]["rich_text"]); !strings.Contains(rt, `"rich_text_section"`) || !strings.Contains(rt, `"Fixed"`) {
		t.Errorf("text cell %s, want a rich_text block", rt)
	}
	if _, plain := byCol["ColItem"]["text"]; plain {
		t.Error("text cell sent plain text")
	}
	if n := string(byCol["ColEst"]["number"]); n != `[3]` {
		t.Errorf("number %s", n)
	}
	if b := string(byCol["ColOK"]["checkbox"]); b != `true` {
		t.Errorf("checkbox %s", b)
	}
	if d := string(byCol["ColDue"]["date"]); d != `["2026-11-20"]` {
		t.Errorf("date %s", d)
	}
	if u := string(byCol["ColOwner"]["user"]); u != `["U1","U2"]` {
		t.Errorf("user %s", u)
	}
}

func TestListSetRefusesUnknownColumnOrOption(t *testing.T) {
	f, c := listFake(t)
	if err := c.ListSet(context.Background(), "F1", "Rec1", map[string]string{"Nope": "x"}); err == nil {
		t.Fatal("unknown column accepted")
	}
	if err := c.ListSet(context.Background(), "F1", "Rec1", map[string]string{"Status": "Blocked"}); err == nil {
		t.Fatal("unknown option accepted")
	}
	if len(f.called("slackLists.items.update")) != 0 {
		t.Fatal("a refused set reached Slack")
	}
}

func TestListAddUsesInitialFields(t *testing.T) {
	f, c := listFake(t)
	id, err := c.ListAdd(context.Background(), "F1", map[string]string{"Item": "New bug", "Status": "To do"})
	if err != nil || id != "Rec9" {
		t.Fatalf("add %q %v", id, err)
	}
	var fields []map[string]json.RawMessage
	_ = json.Unmarshal([]byte(f.called("slackLists.items.create")[0].Params["initial_fields"]), &fields)
	if len(fields) != 2 {
		t.Fatalf("initial_fields %v", fields)
	}
	for _, fl := range fields {
		var col string
		_ = json.Unmarshal(fl["column_id"], &col)
		if col == "ColStatus" && string(fl["select"]) != `["todo"]` {
			t.Errorf("status %s", fl["select"])
		}
	}
}

func TestListCreateAndUpdate(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("slackLists.create", `{"ok":true,"list_id":"F3"}`)
	f.on("slackLists.update", `{"ok":true}`)
	cols := json.RawMessage(`[{"key":"title","name":"Item","type":"text","is_primary_column":true}]`)
	id, err := c.ListCreate(context.Background(), "QA", cols, false)
	if err != nil || id != "F3" {
		t.Fatalf("create %q %v", id, err)
	}
	p := f.called("slackLists.create")[0].Params
	if p["name"] != "QA" || !strings.Contains(p["schema"], `"is_primary_column":true`) {
		t.Fatalf("create params %v", p)
	}
	name := "QA v2"
	if err := c.ListUpdate(context.Background(), "F3", ListChange{Name: &name}); err != nil {
		t.Fatal(err)
	}
	up := f.called("slackLists.update")[0].Params
	if up["id"] != "F3" || up["name"] != "QA v2" {
		t.Fatalf("update params %v", up)
	}
	if _, ok := up["schema"]; ok {
		t.Fatal("update must not send a schema")
	}
	if err := c.ListUpdate(context.Background(), "F3", ListChange{}); err == nil {
		t.Fatal("empty update accepted")
	}
}
