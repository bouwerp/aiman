package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListColumn is one typed column of a Slack List. ID is the Col… id the
// write methods take; Key is the stable key; Name is what people see.
type ListColumn struct {
	ID      string       `json:"id"`
	Key     string       `json:"key"`
	Name    string       `json:"name"`
	Type    string       `json:"type"`
	Choices []ListChoice `json:"choices,omitempty"`
}

// ListChoice is one option of a select column. Writes send Value.
type ListChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ListMeta is a List's id, title and schema.
type ListMeta struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Columns []ListColumn `json:"columns"`
}

// ListRow is one record; Cells is keyed by column name.
type ListRow struct {
	ID    string         `json:"id"`
	Cells map[string]any `json:"cells"`
}

// ListPage is one page of a List's rows with its schema.
type ListPage struct {
	List       ListMeta  `json:"list"`
	Rows       []ListRow `json:"rows"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// ListChange is what list update may change. Columns are not among them:
// slackLists.update cannot change a schema.
type ListChange struct {
	Name        *string
	Description *string
	Todo        *bool
}

// wireList is the list object Slack returns with include_list and from
// slackLists.items.info.
type wireList struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Metadata struct {
		Schema []struct {
			ID      string `json:"id"`
			Key     string `json:"key"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Options struct {
				Choices []ListChoice `json:"choices"`
			} `json:"options"`
		} `json:"schema"`
	} `json:"list_metadata"`
}

func (w *wireList) meta() ListMeta {
	m := ListMeta{ID: w.ID, Title: w.Title, Columns: []ListColumn{}}
	if m.Title == "" {
		m.Title = w.Name
	}
	for _, s := range w.Metadata.Schema {
		m.Columns = append(m.Columns, ListColumn{ID: s.ID, Key: s.Key, Name: s.Name, Type: s.Type, Choices: s.Options.Choices})
	}
	return m
}

type wireField struct {
	Key      string          `json:"key"`
	ColumnID string          `json:"column_id"`
	Value    json.RawMessage `json:"value"`
	Text     string          `json:"text"`
	Select   []string        `json:"select"`
	User     []string        `json:"user"`
	Date     []string        `json:"date"`
	Number   []float64       `json:"number"`
	Checkbox *bool           `json:"checkbox"`
}

// cell is the plain value of a field: value when Slack sent one, else the
// first typed form present.
func (f wireField) cell() any {
	if len(f.Value) > 0 && string(f.Value) != "null" {
		var v any
		if json.Unmarshal(f.Value, &v) == nil {
			return v
		}
	}
	switch {
	case f.Text != "":
		return f.Text
	case f.Select != nil:
		return strings.Join(f.Select, ",")
	case f.User != nil:
		return strings.Join(f.User, ",")
	case f.Date != nil:
		return strings.Join(f.Date, ",")
	case len(f.Number) == 1:
		return f.Number[0]
	case f.Checkbox != nil:
		return *f.Checkbox
	}
	return nil
}

type wireRecord struct {
	ID     string      `json:"id"`
	Fields []wireField `json:"fields"`
}

func (r wireRecord) row(m ListMeta) ListRow {
	row := ListRow{ID: r.ID, Cells: map[string]any{}}
	for _, f := range r.Fields {
		name := f.Key
		for _, col := range m.Columns {
			if (f.ColumnID != "" && col.ID == f.ColumnID) || (f.ColumnID == "" && col.Key == f.Key) {
				name = col.Name
				break
			}
		}
		row.Cells[name] = f.cell()
	}
	return row
}

// ListRead returns one page of rows with the List's schema. The schema comes
// from include_list; a response without it takes the schema from
// slackLists.items.info of the first row.
func (c *Client) ListRead(ctx context.Context, id, cursor string, limit int) (ListPage, error) {
	v := url.Values{"list_id": {id}, "include_list": {"true"}}
	setIf(v, "cursor", cursor)
	setLimit(v, limit, 100)
	var out struct {
		List  *wireList    `json:"list"`
		Items []wireRecord `json:"items"`
		cursorMeta
	}
	if err := c.callForm(ctx, "slackLists.items.list", v, &out); err != nil {
		return ListPage{}, err
	}
	var meta ListMeta
	switch {
	case out.List != nil:
		meta = out.List.meta()
	case len(out.Items) > 0:
		m, err := c.listSchema(ctx, id, out.Items[0].ID)
		if err != nil {
			return ListPage{}, err
		}
		meta = m
	default:
		meta = ListMeta{ID: id, Columns: []ListColumn{}}
	}
	if meta.ID == "" {
		meta.ID = id
	}
	page := ListPage{List: meta, Rows: []ListRow{}, NextCursor: out.Meta.NextCursor}
	for _, r := range out.Items {
		page.Rows = append(page.Rows, r.row(meta))
	}
	return page, nil
}

// listSchema reads the schema through slackLists.items.info of one row.
func (c *Client) listSchema(ctx context.Context, id, row string) (ListMeta, error) {
	var out struct {
		List *wireList `json:"list"`
	}
	if err := c.callForm(ctx, "slackLists.items.info", url.Values{"list_id": {id}, "id": {row}}, &out); err != nil {
		return ListMeta{}, err
	}
	if out.List == nil {
		return ListMeta{}, fmt.Errorf("slack slackLists.items.info: no list schema for %s", id)
	}
	return out.List.meta(), nil
}

// ListSet changes cells of one row. Keys are column names (any case) or
// column keys; every key and select option is checked before any write.
func (c *Client) ListSet(ctx context.Context, id, row string, set map[string]string) error {
	if len(set) == 0 {
		return errors.New("slack list set: nothing to set")
	}
	meta, err := c.listSchema(ctx, id, row)
	if err != nil {
		return err
	}
	cells, err := mapCells(meta, set)
	if err != nil {
		return err
	}
	for _, cell := range cells {
		cell["row_id"] = row
	}
	return c.call(ctx, "slackLists.items.update", map[string]any{"list_id": id, "cells": cells}, nil)
}

// ListAdd creates a row with the given cells and returns its id. The schema
// is read from the List's first row, so an empty List takes no fields.
func (c *Client) ListAdd(ctx context.Context, id string, set map[string]string) (string, error) {
	payload := map[string]any{"list_id": id}
	if len(set) > 0 {
		page, err := c.ListRead(ctx, id, "", 1)
		if err != nil {
			return "", err
		}
		if len(page.List.Columns) == 0 {
			return "", fmt.Errorf("slack list add: cannot read the schema of %s", id)
		}
		fields, err := mapCells(page.List, set)
		if err != nil {
			return "", err
		}
		payload["initial_fields"] = fields
	}
	var out struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := c.call(ctx, "slackLists.items.create", payload, &out); err != nil {
		return "", err
	}
	return out.Item.ID, nil
}

// ListCreate creates a List with schema (a JSON array of columns) and
// returns its id.
func (c *Client) ListCreate(ctx context.Context, name string, schema json.RawMessage, todo bool) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("slack list create: name is required")
	}
	payload := map[string]any{"name": name}
	if len(schema) > 0 {
		if !json.Valid(schema) {
			return "", errors.New("slack list create: schema is not valid JSON")
		}
		payload["schema"] = schema
	}
	if todo {
		payload["todo_mode"] = true
	}
	var out struct {
		ListID string `json:"list_id"`
	}
	if err := c.call(ctx, "slackLists.create", payload, &out); err != nil {
		return "", err
	}
	return out.ListID, nil
}

// ListUpdate changes a List's name, description or todo mode; it sends
// only the fields set and never a schema.
func (c *Client) ListUpdate(ctx context.Context, id string, ch ListChange) error {
	payload := map[string]any{"id": id}
	if ch.Name != nil {
		payload["name"] = *ch.Name
	}
	if ch.Description != nil {
		payload["description_blocks"] = []any{richTextBlock(*ch.Description)}
	}
	if ch.Todo != nil {
		payload["todo_mode"] = *ch.Todo
	}
	if len(payload) == 1 {
		return errors.New("slack list update: nothing to change")
	}
	return c.call(ctx, "slackLists.update", payload, nil)
}

func richTextBlock(text string) map[string]any {
	return map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":     "rich_text_section",
			"elements": []any{map[string]any{"type": "text", "text": text}},
		}},
	}
}

// mapCells turns name=value pairs into cells keyed by column_id with the
// value in its column type's form.
func mapCells(meta ListMeta, set map[string]string) ([]map[string]any, error) {
	cells := make([]map[string]any, 0, len(set))
	for key, val := range set {
		col, ok := findColumn(meta.Columns, key)
		if !ok {
			return nil, fmt.Errorf("slack list: no column %q", key)
		}
		cell, err := cellValue(col, val)
		if err != nil {
			return nil, err
		}
		cell["column_id"] = col.ID
		cells = append(cells, cell)
	}
	return cells, nil
}

func findColumn(cols []ListColumn, key string) (ListColumn, bool) {
	for _, col := range cols {
		if strings.EqualFold(col.Name, key) {
			return col, true
		}
	}
	for _, col := range cols {
		if col.Key == key {
			return col, true
		}
	}
	return ListColumn{}, false
}

func cellValue(col ListColumn, val string) (map[string]any, error) {
	switch col.Type {
	case "text":
		return map[string]any{"rich_text": []any{richTextBlock(val)}}, nil
	case "select":
		if v, ok := choiceValue(col.Choices, strings.TrimSpace(val)); ok {
			return map[string]any{"select": []string{v}}, nil
		}
		var values []string
		for _, part := range splitList(val) {
			v, ok := choiceValue(col.Choices, part)
			if !ok {
				return nil, fmt.Errorf("slack list: column %q has no option %q", col.Name, part)
			}
			values = append(values, v)
		}
		return map[string]any{"select": values}, nil
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return nil, fmt.Errorf("slack list: column %q takes a number, not %q", col.Name, val)
		}
		return map[string]any{"number": []float64{n}}, nil
	case "checkbox":
		b, err := strconv.ParseBool(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("slack list: column %q takes true or false, not %q", col.Name, val)
		}
		return map[string]any{"checkbox": b}, nil
	case "date":
		d := strings.TrimSpace(val)
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return nil, fmt.Errorf("slack list: column %q takes YYYY-MM-DD, not %q", col.Name, val)
		}
		return map[string]any{"date": []string{d}}, nil
	case "user":
		users := splitList(val)
		if len(users) == 0 {
			return nil, fmt.Errorf("slack list: column %q takes user ids", col.Name)
		}
		return map[string]any{"user": users}, nil
	default:
		return nil, fmt.Errorf("slack list: column %q has type %q, which list set cannot write", col.Name, col.Type)
	}
}

func choiceValue(choices []ListChoice, s string) (string, bool) {
	for _, ch := range choices {
		if strings.EqualFold(ch.Label, s) || strings.EqualFold(ch.Value, s) {
			return ch.Value, true
		}
	}
	return "", false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
