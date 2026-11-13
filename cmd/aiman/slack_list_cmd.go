package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func init() {
	registerSlack("list", slackCommand{
		run:   runSlackList,
		write: slackWriteSubcommands("set", "add", "create", "update"),
		usage: `read <F…> [--format json|csv|markdown] [--schema] [--limit n] [--cursor c] [--raw]
       aiman slack list set <F…> <Rec…> --set Col=Value …
       aiman slack list add <F…> --set Col=Value …
       aiman slack list create --name N --columns-file f.json [--todo]
       aiman slack list update <F…> [--name N] [--description D] [--todo on|off]`,
	})
}

func runSlackList(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackFail(errOut, "slack_params", "usage: aiman slack list <read|set|add|create|update> …")
	}
	switch args[0] {
	case "read":
		return runSlackListRead(ctx, args[1:], out, errOut)
	case "set":
		return runSlackListSet(ctx, args[1:], out, errOut)
	case "add":
		return runSlackListAdd(ctx, args[1:], out, errOut)
	case "create":
		return runSlackListCreate(ctx, args[1:], out, errOut)
	case "update":
		return runSlackListUpdate(ctx, args[1:], out, errOut)
	default:
		return slackFail(errOut, "slack_params", "unknown list subcommand "+args[0])
	}
}

// slackSetFlags pulls every --set Col=Value (or --set=Col=Value) out of
// args, since slackFlags keeps only the last value of a repeated flag.
func slackSetFlags(args []string) (map[string]string, []string, error) {
	set := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var pair string
		switch {
		case a == "--set":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--set needs Col=Value")
			}
			pair = args[i+1]
			i++
		case strings.HasPrefix(a, "--set="):
			pair = strings.TrimPrefix(a, "--set=")
		default:
			rest = append(rest, a)
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, nil, fmt.Errorf("--set %q is not Col=Value", pair)
		}
		set[strings.TrimSpace(k)] = v
	}
	return set, rest, nil
}

func runSlackListRead(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, rest := slackFlags(args, "schema")
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "list read takes one list id")
	}
	format := flags["format"]
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" && format != "markdown" {
		return slackFail(errOut, "slack_params", "format must be json, csv or markdown")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	page, err := c.ListRead(ctx, rest[0], flags["cursor"], atoi(flags["limit"]))
	if err != nil {
		return slackAPI(errOut, err)
	}
	if flags["schema"] != "" {
		return slackWrite(out, page.List)
	}
	switch format {
	case "csv":
		return writeListCSV(out, page)
	case "markdown":
		return writeListMarkdown(out, page)
	}
	return slackWrite(out, map[string]any{"list": page.List, "rows": nonNil(page.Rows), "next_cursor": page.NextCursor})
}

// listTable is a header of column names (after "id") and one string row
// per record.
func listTable(page slack.ListPage) ([]string, [][]string) {
	header := []string{"id"}
	for _, col := range page.List.Columns {
		header = append(header, col.Name)
	}
	rows := make([][]string, 0, len(page.Rows))
	for _, r := range page.Rows {
		line := []string{r.ID}
		for _, col := range page.List.Columns {
			line = append(line, cellString(r.Cells[col.Name]))
		}
		rows = append(rows, line)
	}
	return header, rows
}

func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func writeListCSV(out io.Writer, page slack.ListPage) error {
	header, rows := listTable(page)
	w := csv.NewWriter(out)
	if err := w.Write(header); err != nil {
		return err
	}
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func writeListMarkdown(out io.Writer, page slack.ListPage) error {
	header, rows := listTable(page)
	esc := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
	}
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("|")
		for _, c := range cells {
			b.WriteString(" " + esc(c) + " |")
		}
		b.WriteString("\n")
	}
	line(header)
	b.WriteString("|" + strings.Repeat(" --- |", len(header)) + "\n")
	for _, r := range rows {
		line(r)
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func runSlackListSet(ctx context.Context, args []string, out, errOut io.Writer) error {
	set, rest, err := slackSetFlags(args)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if len(rest) != 2 || len(set) == 0 {
		return slackFail(errOut, "slack_params", "usage: aiman slack list set <list id> <row id> --set Col=Value ...")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	if err := c.ListSet(ctx, rest[0], rest[1], set); err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"ok": true})
}

func runSlackListAdd(ctx context.Context, args []string, out, errOut io.Writer) error {
	set, rest, err := slackSetFlags(args)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "usage: aiman slack list add <list id> --set Col=Value ...")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	id, err := c.ListAdd(ctx, rest[0], set)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"id": id})
}

func runSlackListCreate(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, rest := slackFlags(args, "todo")
	if len(rest) != 0 || flags["name"] == "" || flags["columns-file"] == "" {
		return slackFail(errOut, "slack_params", "usage: aiman slack list create --name N --columns-file f.json [--todo]")
	}
	schema, err := os.ReadFile(flags["columns-file"])
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if !json.Valid(schema) {
		return slackFail(errOut, "slack_params", "columns file is not valid JSON")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	id, err := c.ListCreate(ctx, flags["name"], json.RawMessage(schema), flags["todo"] != "")
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"id": id})
}

func runSlackListUpdate(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, rest := slackFlags(args)
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "usage: aiman slack list update <list id> [--name N] [--description D] [--todo on|off]")
	}
	var ch slack.ListChange
	if v, ok := flags["name"]; ok {
		ch.Name = &v
	}
	if v, ok := flags["description"]; ok {
		ch.Description = &v
	}
	if v, ok := flags["todo"]; ok {
		var on bool
		switch v {
		case "on":
			on = true
		case "off":
		default:
			return slackFail(errOut, "slack_params", "--todo takes on or off")
		}
		ch.Todo = &on
	}
	if ch.Name == nil && ch.Description == nil && ch.Todo == nil {
		return slackFail(errOut, "slack_params", "list update needs --name, --description or --todo")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	if err := c.ListUpdate(ctx, rest[0], ch); err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"ok": true})
}
