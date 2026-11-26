package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const feedUsage = `Usage: aiman feed status [--json]
       aiman feed actions [--state a,b] [--limit N] [--json]
       aiman feed events [--batch N] [--digest] [--limit N] [--json]
       aiman feed memory [--json]
       aiman feed act <id> approve [--option N] [--text T]
       aiman feed act <id> dismiss [--reason R] [--until T] [--note N]
`

var feedDismissReasons = map[string]bool{
	"not_relevant": true, "handled": true, "later": true,
	"mute_thread": true, "mute_sender": true, "mute_channel": true,
}

// feedBoolFlags take no value.
var feedBoolFlags = map[string]bool{"json": true, "digest": true}

// parseFeedFlags splits args into flags and positionals; a value flag takes
// the next argument whatever it starts with.
func parseFeedFlags(args []string) (map[string]string, []string, error) {
	flags := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if feedBoolFlags[key] {
			flags[key] = "1"
			continue
		}
		switch key {
		case "state", "limit", "batch", "option", "text", "reason", "until", "note":
		default:
			return nil, nil, fmt.Errorf("unknown flag %s", a)
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("%s needs a value", a)
		}
		flags[key] = args[i+1]
		i++
	}
	return flags, rest, nil
}

func feedUsageErr(msg string) error {
	if msg != "" {
		fmt.Fprintln(os.Stderr, "aiman feed:", msg)
	}
	fmt.Fprint(os.Stderr, feedUsage)
	return errUsage
}

// runFeed is `aiman feed`: reads and decisions through serve's feed.* RPCs.
func runFeed(args []string, out io.Writer) error {
	if len(args) == 0 {
		return feedUsageErr("")
	}
	sub := args[0]
	flags, rest, err := parseFeedFlags(args[1:])
	if err != nil {
		return feedUsageErr(err.Error())
	}
	intFlag := func(k string) (int, bool, error) {
		v, ok := flags[k]
		if !ok {
			return 0, false, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, true, fmt.Errorf("--%s must be a positive number", k)
		}
		return n, true, nil
	}
	params := map[string]any{}
	var method string
	switch sub {
	case "status", "memory":
		if len(rest) != 0 {
			return feedUsageErr("")
		}
		method = "feed." + sub
	case "actions":
		if len(rest) != 0 {
			return feedUsageErr("")
		}
		method = "feed.actions"
		if v := flags["state"]; v != "" {
			params["state"] = strings.Split(v, ",")
		}
		n, ok, err := intFlag("limit")
		if err != nil {
			return feedUsageErr(err.Error())
		}
		if ok {
			params["limit"] = n
		}
	case "events":
		if len(rest) != 0 {
			return feedUsageErr("")
		}
		method = "feed.events"
		n, ok, err := intFlag("batch")
		if err != nil {
			return feedUsageErr(err.Error())
		}
		if ok {
			params["batch"] = n
		}
		if flags["digest"] == "1" {
			params["digest"] = true
		}
		if !ok && flags["digest"] != "1" {
			return feedUsageErr("events needs --batch or --digest")
		}
		if l, ok, err := intFlag("limit"); err != nil {
			return feedUsageErr(err.Error())
		} else if ok {
			params["limit"] = l
		}
	case "act":
		return runFeedAct(rest, flags, out)
	default:
		return feedUsageErr("unknown subcommand " + sub)
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError("server_not_running", err.Error())
		return err
	}
	res, err := callOverview(sock, method, params)
	if err != nil {
		return err
	}
	if flags["json"] == "1" {
		return writeJSON(res)
	}
	return printFeed(out, sub, res)
}

func runFeedAct(rest []string, flags map[string]string, out io.Writer) error {
	if len(rest) != 2 {
		return feedUsageErr("act needs <id> and approve or dismiss")
	}
	id, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || id < 1 {
		return feedUsageErr("action id must be a positive number")
	}
	op := rest[1]
	params := map[string]any{"id": id, "op": op}
	option := 0
	switch op {
	case "approve":
		option = 1
		if v, ok := flags["option"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return feedUsageErr("--option is a number from 1")
			}
			option = n
		}
		params["option"] = option - 1
	case "dismiss":
		reason := flags["reason"]
		if reason == "" {
			reason = string(domain.FeedNotRelevant)
		}
		if !feedDismissReasons[reason] {
			return feedUsageErr("unknown --reason " + reason)
		}
		params["reason"] = reason
		if v := flags["until"]; v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return feedUsageErr("--until is an RFC 3339 time")
			}
			params["until"] = t.UTC().Format(time.RFC3339)
		}
		if v := flags["note"]; v != "" {
			params["note"] = v
		}
	default:
		return feedUsageErr("act op is approve or dismiss")
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError("server_not_running", err.Error())
		return err
	}
	if text, ok := flags["text"]; ok && op == "approve" {
		edited, err := feedEditedParams(sock, id, option-1, text)
		if err != nil {
			return err
		}
		params["params"] = edited
	}
	res, err := callOverview(sock, "feed.act", params)
	if err != nil {
		return err
	}
	if flags["json"] == "1" {
		return writeJSON(res)
	}
	return printFeed(out, "act", res)
}

// feedEditedParams reads the action and returns option's params with text
// replaced, keeping the other params.
func feedEditedParams(sock string, id int64, option int, text string) (map[string]any, error) {
	res, err := callOverview(sock, "feed.actions", map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	var got struct {
		Actions []domain.FeedAction `json:"actions"`
	}
	if err := decodeInto(res, &got); err != nil {
		return nil, err
	}
	if len(got.Actions) != 1 {
		return nil, fmt.Errorf("no feed action %d", id)
	}
	opts := got.Actions[0].Options
	if option < 0 || option >= len(opts) {
		return nil, fmt.Errorf("action %d has %d options, not %d", id, len(opts), option+1)
	}
	p := map[string]any{}
	if len(opts[option].Params) > 0 {
		if err := json.Unmarshal(opts[option].Params, &p); err != nil {
			return nil, errors.New("option params are not an object")
		}
	}
	p["text"] = text
	return p, nil
}

func printFeed(out io.Writer, sub string, res any) error {
	switch sub {
	case "actions":
		var r struct {
			Actions []domain.FeedAction `json:"actions"`
		}
		if err := decodeInto(res, &r); err != nil {
			return err
		}
		for _, a := range r.Actions {
			printFeedAction(out, a)
		}
		if len(r.Actions) == 0 {
			fmt.Fprintln(out, "no actions")
		}
	case "act":
		var r struct {
			Action *domain.FeedAction `json:"action"`
		}
		if err := decodeInto(res, &r); err != nil {
			return err
		}
		if r.Action != nil {
			printFeedAction(out, *r.Action)
			if r.Action.Result != "" {
				fmt.Fprintf(out, "    result: %s\n", r.Action.Result)
			}
		}
	case "events":
		var r struct {
			Events []domain.FeedEvent `json:"events"`
		}
		if err := decodeInto(res, &r); err != nil {
			return err
		}
		for _, e := range r.Events {
			fmt.Fprintf(out, "%d  %s  %s\n", e.ID, e.Source, e.Title)
		}
		if len(r.Events) == 0 {
			fmt.Fprintln(out, "no events")
		}
	default:
		return writeJSON(res)
	}
	return nil
}

func printFeedAction(out io.Writer, a domain.FeedAction) {
	fmt.Fprintf(out, "#%d  %s  %s  %s  [%s]  %s\n", a.ID, a.Priority, a.Kind, feedActionSource(a), a.State, a.Rationale)
	for i, o := range a.Options {
		fmt.Fprintf(out, "    %d. %s\n", i+1, o.Label)
	}
}

// feedActionSource names the first event the action answers, from its
// trace, else "-".
func feedActionSource(a domain.FeedAction) string {
	for _, t := range a.Trace {
		if t.Kind == "event" && t.Role == "answers" {
			return "event " + t.Ref
		}
	}
	return "-"
}
