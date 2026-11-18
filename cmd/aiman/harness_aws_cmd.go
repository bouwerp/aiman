package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/harness"
)

const harnessAWSUsageText = `Usage: aiman harness aws calls [--after N] [--limit N] [--run ID] [--json]
       aiman harness aws plan [<scenario>] [--json]
       aiman harness aws resources [--run ID] [--json]
       aiman harness aws s3 ls [<bucket>[/<prefix>]] [--limit N] [--token T] [--run ID] [--json]
       aiman harness aws s3 versions <bucket>/<key> [--run ID] [--json]
       aiman harness aws s3 cat <bucket>/<key> [--version V] [--offset N] [--run ID] [--json]
       aiman harness aws s3 get <bucket>/<key> [--version V] [--out FILE] [--run ID]
       aiman harness aws ddb tables [--run ID] [--json]
       aiman harness aws ddb describe <table> [--run ID] [--json]
       aiman harness aws ddb scan <table> [--index I] [--limit N] [--start TOKEN] [--run ID] [--json]
       aiman harness aws ddb query <table> --pk NAME=VALUE [--sk 'OP V1 [V2]'] [--index I] [--desc] [--limit N] [--start TOKEN] [--run ID] [--json]
       aiman harness aws ddb get <table> '<key JSON>' [--run ID] [--json]
       aiman harness aws ddb stream <table> [--limit N] [--run ID] [--json]
Every command takes --session NAME (default $AIMAN_ID) or --run ID, and --service NAME when a run has more than one aws service.
`

// harnessAWSCommands are the `aiman harness aws <name>` subcommands,
// keyed by name and given the arguments after it; each piece registers its
// own in init.
var harnessAWSCommands = map[string]func(h harnessIO, args []string) error{}

func init() {
	harnessAWSCommands["resources"] = harnessAWSResources
	harnessAWSCommands["s3"] = harnessAWSS3
	harnessAWSCommands["ddb"] = harnessAWSDDB
}

// harnessAWSMain is `aiman harness aws …`: the reads of a run's aws service
// through serve.
func harnessAWSMain(args []string, h harnessIO) error {
	if len(args) == 0 {
		return harnessAWSUsage(h.errOut)
	}
	cmd, ok := harnessAWSCommands[args[0]]
	if !ok {
		return harnessAWSUsage(h.errOut)
	}
	return cmd(h, args[1:])
}

func harnessAWSUsage(w io.Writer) error {
	_, _ = io.WriteString(w, harnessAWSUsageText)
	return errUsage
}

// awsFlags are the flags every harness aws read takes, plus the op's own.
type awsFlags struct {
	fs                                *flag.FlagSet
	session, run, service             string
	token, version, out, index, start string
	pk, sk                            string
	limit                             int
	offset                            int64
	json, desc                        bool
	pos                               []string
}

func newAWSFlags() *awsFlags {
	f := &awsFlags{fs: flag.NewFlagSet("harness aws", flag.ContinueOnError)}
	f.fs.SetOutput(io.Discard)
	f.fs.StringVar(&f.session, "session", "", "")
	f.fs.StringVar(&f.run, "run", "", "")
	f.fs.StringVar(&f.service, "service", "", "")
	f.fs.StringVar(&f.token, "token", "", "")
	f.fs.StringVar(&f.version, "version", "", "")
	f.fs.StringVar(&f.out, "out", "", "")
	f.fs.StringVar(&f.index, "index", "", "")
	f.fs.StringVar(&f.start, "start", "", "")
	f.fs.StringVar(&f.pk, "pk", "", "")
	f.fs.StringVar(&f.sk, "sk", "", "")
	f.fs.IntVar(&f.limit, "limit", 0, "")
	f.fs.Int64Var(&f.offset, "offset", 0, "")
	f.fs.BoolVar(&f.json, "json", false, "")
	f.fs.BoolVar(&f.desc, "desc", false, "")
	return f
}

// parse reads flags anywhere among the positional arguments.
func (f *awsFlags) parse(h harnessIO, args []string, minPos, maxPos int) error {
	for {
		if err := f.fs.Parse(args); err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
			return harnessAWSUsage(h.errOut)
		}
		rest := f.fs.Args()
		if len(rest) == 0 {
			break
		}
		f.pos = append(f.pos, rest[0])
		args = rest[1:]
	}
	if len(f.pos) < minPos || len(f.pos) > maxPos {
		return harnessAWSUsage(h.errOut)
	}
	if f.session == "" {
		f.session = strings.TrimSpace(h.getenv("AIMAN_ID"))
	}
	if f.session == "" && f.run == "" {
		_, _ = fmt.Fprintln(h.errOut, "name the session with --session NAME (or set AIMAN_ID), or the run with --run ID")
		return harnessAWSUsage(h.errOut)
	}
	return nil
}

func (f *awsFlags) params(op string) map[string]any {
	p := map[string]any{"op": op}
	if f.run != "" {
		p["run"] = f.run
	} else {
		p["session"] = f.session
	}
	if f.service != "" {
		p["service"] = f.service
	}
	if f.limit > 0 {
		p["limit"] = f.limit
	}
	return p
}

// call sends harness.aws and decodes the reply, printing a refusal.
func (f *awsFlags) call(h harnessIO, params map[string]any) (json.RawMessage, *harness.AWSResult, error) {
	raw, err := harnessCallOut(h, harnessArgs{json: f.json}, "harness.aws", params)
	if err != nil {
		return nil, nil, err
	}
	var res harness.AWSResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, err
	}
	return raw, &res, nil
}

// splitBucketKey reads bucket/key (key may hold more slashes).
func splitBucketKey(s string) (bucket, key string) {
	bucket, key, _ = strings.Cut(strings.TrimPrefix(s, "s3://"), "/")
	return bucket, key
}

func harnessAWSResources(h harnessIO, args []string) error {
	f := newAWSFlags()
	if err := f.parse(h, args, 0, 0); err != nil {
		return err
	}
	raw, res, err := f.call(h, f.params(harness.AWSOpResources))
	if err != nil {
		return err
	}
	if f.json || res.Resources != nil {
		if !f.json {
			return printHarnessJSON(h.out, res.Resources)
		}
		return printHarnessJSON(h.out, raw)
	}
	_, _ = fmt.Fprintln(h.out, res.Note)
	return nil
}

func harnessAWSS3(h harnessIO, args []string) error {
	if len(args) == 0 {
		return harnessAWSUsage(h.errOut)
	}
	f := newAWSFlags()
	sub := args[0]
	switch sub {
	case "ls":
		if err := f.parse(h, args[1:], 0, 1); err != nil {
			return err
		}
		if len(f.pos) == 0 {
			return f.s3Buckets(h)
		}
		return f.s3List(h)
	case "versions":
		if err := f.parse(h, args[1:], 1, 1); err != nil {
			return err
		}
		return f.s3Versions(h)
	case "cat":
		if err := f.parse(h, args[1:], 1, 1); err != nil {
			return err
		}
		return f.s3Cat(h)
	case "get":
		if err := f.parse(h, args[1:], 1, 1); err != nil {
			return err
		}
		return f.s3Get(h)
	}
	return harnessAWSUsage(h.errOut)
}

func (f *awsFlags) s3Buckets(h harnessIO) error {
	raw, res, err := f.call(h, f.params(harness.AWSOpS3Buckets))
	if err != nil || f.json {
		return jsonOr(h, raw, err, f.json)
	}
	tw := tabwriter.NewWriter(h.out, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BUCKET\tCREATED\tVERSIONING\tNOTIFIES")
	for _, b := range res.Buckets {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Name, b.Created, dash(b.Versioning), dash(strings.Join(b.Notifications, ",")))
	}
	return tw.Flush()
}

func (f *awsFlags) s3List(h harnessIO) error {
	bucket, prefix := splitBucketKey(f.pos[0])
	p := f.params(harness.AWSOpS3Objects)
	p["bucket"], p["prefix"] = bucket, prefix
	if f.token != "" {
		p["token"] = f.token
	}
	raw, res, err := f.call(h, p)
	if err != nil || f.json {
		return jsonOr(h, raw, err, f.json)
	}
	tw := tabwriter.NewWriter(h.out, 0, 2, 2, ' ', 0)
	for _, pre := range res.Prefixes {
		_, _ = fmt.Fprintf(tw, "PRE\t\t%s\n", pre)
	}
	for _, o := range res.Objects {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", o.Modified, o.Size, o.Key)
	}
	_ = tw.Flush()
	if res.NextToken != "" {
		_, _ = fmt.Fprintf(h.errOut, "(more: --token %s)\n", res.NextToken)
	}
	return nil
}

func (f *awsFlags) s3Versions(h harnessIO) error {
	bucket, key := splitBucketKey(f.pos[0])
	p := f.params(harness.AWSOpS3Versions)
	p["bucket"], p["key"] = bucket, key
	raw, res, err := f.call(h, p)
	if err != nil || f.json {
		return jsonOr(h, raw, err, f.json)
	}
	tw := tabwriter.NewWriter(h.out, 0, 2, 2, ' ', 0)
	for _, v := range res.Versions {
		kind := "version"
		if v.DeleteMarker {
			kind = "delete marker"
		}
		latest := ""
		if v.Latest {
			latest = "latest"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", v.Version, v.Modified, v.Size, kind, latest)
	}
	return tw.Flush()
}

func (f *awsFlags) s3Cat(h harnessIO) error {
	bucket, key := splitBucketKey(f.pos[0])
	p := f.params(harness.AWSOpS3Object)
	p["bucket"], p["key"], p["offset"] = bucket, key, f.offset
	if f.version != "" {
		p["version"] = f.version
	}
	raw, res, err := f.call(h, p)
	if err != nil || f.json {
		return jsonOr(h, raw, err, f.json)
	}
	o := res.Object
	if o.Binary {
		_, _ = fmt.Fprintf(h.errOut, "(%s/%s is binary, %d bytes; use aws s3 get)\n", o.Bucket, o.Key, o.Size)
		return nil
	}
	_, _ = io.WriteString(h.out, o.Data)
	if !o.EOF {
		_, _ = fmt.Fprintf(h.errOut, "\n(more: --offset %d of %d bytes)\n", o.Next, o.Size)
	}
	return nil
}

// s3Get downloads an object in pages, up to the download cap.
func (f *awsFlags) s3Get(h harnessIO) error {
	bucket, key := splitBucketKey(f.pos[0])
	out := h.out
	if f.out != "" {
		file, err := os.OpenFile(f.out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: the operator's own output path
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	var offset int64
	for {
		p := f.params(harness.AWSOpS3Download)
		p["bucket"], p["key"], p["offset"], p["limit"] = bucket, key, offset, harness.MaxS3DownloadPage
		if f.version != "" {
			p["version"] = f.version
		}
		_, res, err := f.call(h, p)
		if err != nil {
			return err
		}
		d := res.Download
		data, err := base64.StdEncoding.DecodeString(d.Data)
		if err != nil {
			return fmt.Errorf("download page at %d: %w", offset, err)
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
		if d.EOF || d.Next <= offset {
			if d.Capped {
				_, _ = fmt.Fprintf(h.errOut, "(stopped at the download cap: %d of %d bytes; harness.aws.download_max)\n", d.Next, d.Size)
				return cliExit{code: 1}
			}
			return nil
		}
		offset = d.Next
	}
}

func harnessAWSDDB(h harnessIO, args []string) error {
	if len(args) == 0 {
		return harnessAWSUsage(h.errOut)
	}
	f := newAWSFlags()
	sub := args[0]
	var p map[string]any
	switch sub {
	case "tables":
		if err := f.parse(h, args[1:], 0, 0); err != nil {
			return err
		}
		p = f.params(harness.AWSOpDDBTables)
	case "describe", "scan", "stream":
		if err := f.parse(h, args[1:], 1, 1); err != nil {
			return err
		}
		p = f.params("ddb_" + sub)
		p["table"] = f.pos[0]
	case "query":
		if err := f.parse(h, args[1:], 1, 1); err != nil {
			return err
		}
		p = f.params(harness.AWSOpDDBQuery)
		p["table"] = f.pos[0]
		if err := f.queryParams(p); err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
			return harnessAWSUsage(h.errOut)
		}
	case "get":
		if err := f.parse(h, args[1:], 2, 2); err != nil {
			return err
		}
		p = f.params(harness.AWSOpDDBGet)
		p["table"] = f.pos[0]
		var key map[string]any
		if err := json.Unmarshal([]byte(f.pos[1]), &key); err != nil {
			_, _ = fmt.Fprintln(h.errOut, "the key is a JSON object of the key attributes, like '{\"pk\":\"o-1\"}'")
			return harnessAWSUsage(h.errOut)
		}
		p["key"] = key
	default:
		return harnessAWSUsage(h.errOut)
	}
	if f.index != "" {
		p["index"] = f.index
	}
	if f.start != "" {
		p["start"] = f.start
	}
	raw, res, err := f.call(h, p)
	if err != nil || f.json {
		return jsonOr(h, raw, err, f.json)
	}
	printDDBResult(h, res)
	return nil
}

// queryParams reads --pk NAME=VALUE and --sk 'OP V1 [V2]'. A value that
// parses as JSON (a number, a quoted string) is sent as that; anything else
// is a string.
func (f *awsFlags) queryParams(p map[string]any) error {
	name, val, ok := strings.Cut(f.pk, "=")
	if !ok || name == "" {
		return errors.New("--pk NAME=VALUE is required")
	}
	p["pk"] = map[string]any{"name": name, "value": cliValue(val)}
	if f.sk != "" {
		parts := strings.Fields(f.sk)
		if len(parts) < 2 {
			return errors.New("--sk is 'OP V1' or 'between V1 V2'")
		}
		values := make([]any, 0, len(parts)-1)
		for _, v := range parts[1:] {
			values = append(values, cliValue(v))
		}
		p["sk"] = map[string]any{"op": parts[0], "values": values}
	}
	if f.desc {
		p["forward"] = false
	}
	return nil
}

func cliValue(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		switch v.(type) {
		case float64, string:
			return v
		}
	}
	return s
}

func printDDBResult(h harnessIO, res *harness.AWSResult) {
	switch {
	case res.Tables != nil:
		for _, t := range res.Tables {
			_, _ = fmt.Fprintln(h.out, t)
		}
	case res.Table != nil:
		_ = printHarnessJSONValue(h.out, res.Table)
	case res.Found != nil:
		if !*res.Found {
			_, _ = fmt.Fprintln(h.errOut, "(no such item)")
			return
		}
		_ = printHarnessJSONValue(h.out, res.Item)
	case res.Records != nil:
		for _, r := range res.Records {
			b, _ := json.Marshal(r)
			_, _ = fmt.Fprintln(h.out, string(b))
		}
	default:
		for _, it := range res.Items {
			b, _ := json.Marshal(it)
			_, _ = fmt.Fprintln(h.out, string(b))
		}
	}
	var notes []string
	if res.Items != nil {
		notes = append(notes, fmt.Sprintf("%d items", len(res.Items)))
	}
	if res.LastKey != "" {
		notes = append(notes, "more: --start "+res.LastKey)
	}
	if res.Truncated {
		notes = append(notes, "truncated")
	}
	if len(res.RedactedAttributes) > 0 {
		sort.Strings(res.RedactedAttributes)
		notes = append(notes, "redacted attributes: "+strings.Join(res.RedactedAttributes, ", "))
	}
	if res.Redacted > 0 {
		notes = append(notes, fmt.Sprintf("%d values redacted", res.Redacted))
	}
	if len(notes) > 0 {
		_, _ = fmt.Fprintf(h.errOut, "(%s)\n", strings.Join(notes, "; "))
	}
}

func printHarnessJSONValue(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// jsonOr prints raw as JSON when asked, or passes err through.
func jsonOr(h harnessIO, raw json.RawMessage, err error, asJSON bool) error {
	if err != nil {
		return err
	}
	if asJSON {
		return printHarnessJSON(h.out, raw)
	}
	return nil
}
