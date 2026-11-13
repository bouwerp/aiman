package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func init() {
	registerSlack("canvas", slackCommand{run: runSlackCanvas, write: slackWriteSubcommands("create", "edit"), usage: `read <F…> [--sections h1,h2] [--contains text] [--raw]
       aiman slack canvas create --title T (--text t | --text-file f) [--channel C…]
       aiman slack canvas edit <F…> --op append|prepend|after|before|replace|delete|rename [--section id] [--all] (--text t | --text-file f)`})
	registerSlack("file", slackCommand{run: runSlackFile,
		usage: "read <F…> [--out path]\n\n  File ids are in the files of history and thread messages."})
	registerSlack("upload", slackCommand{run: runSlackUpload, write: slackAlwaysWrite,
		usage: "<path> --channel <name|C…> [--thread <ts|link>] [--comment text] [--title T]" + slackLinkHelp})
}

func runSlackCanvas(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackFail(errOut, "slack_params", "canvas requires create, read or edit")
	}
	switch args[0] {
	case "create":
		return runSlackCanvasCreate(ctx, args[1:], in, out, errOut)
	case "read":
		return runSlackCanvasRead(ctx, args[1:], out, errOut)
	case "edit":
		return runSlackCanvasEdit(ctx, args[1:], in, out, errOut)
	}
	return slackFail(errOut, "slack_params", "canvas requires create, read or edit, not "+args[0])
}

func runSlackCanvasCreate(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	if flags["title"] == "" {
		return slackFail(errOut, "slack_params", "canvas create requires --title")
	}
	text, err := slackText(flags, in)
	if err != nil {
		return slackFail(errOut, "slack_params", "canvas create requires --text or --text-file: "+err.Error())
	}
	var (
		c       *slack.Client
		channel string
	)
	if flags["channel"] != "" {
		c, channel, err = slackChannel(ctx, flags, errOut)
	} else {
		c, err = slackClientOrFail(ctx, errOut)
	}
	if err != nil {
		return err
	}
	id, err := c.CanvasCreate(ctx, flags["title"], text, channel)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]string{"canvas_id": id})
}

func runSlackCanvasRead(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, rest := slackFlags(args)
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "canvas read requires one canvas id")
	}
	var types []string
	if s := flags["sections"]; s != "" {
		types = strings.Split(s, ",")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	cv, err := c.CanvasRead(ctx, rest[0], types, flags["contains"])
	if err != nil {
		return slackAPI(errOut, err)
	}
	cv.Sections = nonNil(cv.Sections)
	return slackWrite(out, cv)
}

func runSlackCanvasEdit(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, rest := slackFlags(args, "all")
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "canvas edit requires one canvas id")
	}
	e := slack.CanvasEdit{Op: flags["op"], Section: flags["section"], All: flags["all"] != ""}
	if e.Op != "delete" {
		text, err := slackText(flags, in)
		if err != nil {
			return slackFail(errOut, "slack_params", "canvas edit "+e.Op+" requires --text or --text-file: "+err.Error())
		}
		e.Markdown = text
	}
	if _, err := e.Change(); err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	if err := c.CanvasEdit(ctx, rest[0], e); err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]bool{"ok": true})
}

// runSlackFile handles file read: with --out the bytes go to that path,
// otherwise a text file is printed inline as content and binary is refused.
func runSlackFile(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] != "read" {
		return slackFail(errOut, "slack_params", "file requires read")
	}
	flags, rest := slackFlags(args[1:])
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "file read requires one file id")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	f, err := c.FileRead(ctx, rest[0], &buf)
	if err != nil {
		return slackAPI(errOut, err)
	}
	if path := flags["out"]; path != "" {
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return slackFail(errOut, "slack_file", err.Error())
		}
		return slackWrite(out, f)
	}
	if !isText(buf.Bytes()) {
		return slackFail(errOut, "slack_params", "file "+f.ID+" is binary ("+f.Mimetype+"); pass --out path")
	}
	return slackWrite(out, struct {
		slack.File
		Content string `json:"content"`
	}{f, buf.String()})
}

// isText reports whether b is UTF-8 with no NUL byte.
func isText(b []byte) bool {
	return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
}

func runSlackUpload(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, rest := slackFlags(args)
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "upload requires one file path")
	}
	data, err := os.ReadFile(rest[0])
	if err != nil {
		return slackFail(errOut, "slack_file", err.Error())
	}
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	id, err := c.Upload(ctx, slack.UploadRequest{
		Filename: filepath.Base(rest[0]), Data: data, Channel: channel,
		Thread: flags["thread"], Comment: flags["comment"], Title: flags["title"],
	})
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]string{"file_id": id})
}
