package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

const harnessAWSWriteUsageText = `Usage (the operator's, from a terminal outside a session):
       aiman harness aws write s3-put <bucket>/<key> (--file F | --text T) [--content-type T]
       aiman harness aws write s3-delete <bucket>/<key> [--version V]
       aiman harness aws write ddb-put <table> ('<item JSON>' | --file F)
       aiman harness aws write ddb-delete <table> '<key JSON>'
       aiman harness aws write sqs-send <queue> (--text T | --file F) [--attr NAME=VALUE ...] [--group G] [--dedup D]
       aiman harness aws reset
Every command takes --session NAME (default $AIMAN_ID) or --run ID, --service NAME and --json.
`

func init() {
	harnessAWSCommands["write"] = harnessAWSWrite
	harnessAWSCommands["reset"] = func(h harnessIO, args []string) error {
		return harnessAWSWrite(h, append([]string{awsiac.WriteReset}, args...))
	}
}

// awsAttrs collects repeated --attr NAME=VALUE flags.
type awsAttrs map[string]string

func (a awsAttrs) String() string { return "" }

func (a awsAttrs) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("--attr takes NAME=VALUE, not %q", s)
	}
	a[k] = v
	return nil
}

// awsWriteFlags are the flags of `aiman harness aws write`.
type awsWriteFlags struct {
	fs                               *flag.FlagSet
	session, run, service            string
	file, text, contentType, version string
	group, dedup                     string
	attrs                            awsAttrs
	json                             bool
	pos                              []string
	fileSet, textSet                 bool
}

func newAWSWriteFlags() *awsWriteFlags {
	f := &awsWriteFlags{fs: flag.NewFlagSet("harness aws write", flag.ContinueOnError), attrs: awsAttrs{}}
	f.fs.SetOutput(io.Discard)
	f.fs.StringVar(&f.session, "session", "", "")
	f.fs.StringVar(&f.run, "run", "", "")
	f.fs.StringVar(&f.service, "service", "", "")
	f.fs.StringVar(&f.file, "file", "", "")
	f.fs.StringVar(&f.text, "text", "", "")
	f.fs.StringVar(&f.contentType, "content-type", "", "")
	f.fs.StringVar(&f.version, "version", "", "")
	f.fs.StringVar(&f.group, "group", "", "")
	f.fs.StringVar(&f.dedup, "dedup", "", "")
	f.fs.Var(f.attrs, "attr", "")
	f.fs.BoolVar(&f.json, "json", false, "")
	return f
}

func harnessAWSWriteUsage(w io.Writer) error {
	_, _ = io.WriteString(w, harnessAWSWriteUsageText)
	return errUsage
}

// parse reads flags anywhere among the positional arguments.
func (f *awsWriteFlags) parse(h harnessIO, args []string) error {
	for {
		if err := f.fs.Parse(args); err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
			return harnessAWSWriteUsage(h.errOut)
		}
		rest := f.fs.Args()
		if len(rest) == 0 {
			break
		}
		f.pos = append(f.pos, rest[0])
		args = rest[1:]
	}
	f.fs.Visit(func(fl *flag.Flag) {
		f.fileSet = f.fileSet || fl.Name == "file"
		f.textSet = f.textSet || fl.Name == "text"
	})
	if f.session == "" {
		f.session = strings.TrimSpace(h.getenv("AIMAN_ID"))
	}
	if f.session == "" && f.run == "" {
		_, _ = fmt.Fprintln(h.errOut, "name the run with --run ID, or the session with --session NAME")
		return harnessAWSWriteUsage(h.errOut)
	}
	return nil
}

// content is the --file or --text payload; exactly one is required.
func (f *awsWriteFlags) content(h harnessIO) ([]byte, error) {
	switch {
	case f.fileSet == f.textSet:
		_, _ = fmt.Fprintln(h.errOut, "give exactly one of --file F and --text T")
		return nil, harnessAWSWriteUsage(h.errOut)
	case f.textSet:
		return []byte(f.text), nil
	}
	if f.file == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, harness.MaxAWSWriteData+1))
	}
	return os.ReadFile(f.file)
}

// jsonObject decodes a JSON object keeping numbers as written.
func jsonObject(b []byte, what string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("%s must be a JSON object: %v", what, err)
	}
	return m, nil
}

// harnessAWSWrite is `aiman harness aws write <op> …`: the operator's
// writes to a run's aws service and its reset (harness.aws_write).
func harnessAWSWrite(h harnessIO, args []string) error {
	if len(args) == 0 {
		return harnessAWSWriteUsage(h.errOut)
	}
	op := strings.ReplaceAll(args[0], "-", "_")
	f := newAWSWriteFlags()
	if err := f.parse(h, args[1:]); err != nil {
		return err
	}
	params := map[string]any{"op": op}
	if f.run != "" {
		params["run"] = f.run
	} else {
		params["session"] = f.session
	}
	if f.service != "" {
		params["service"] = f.service
	}
	switch op {
	case awsiac.WriteS3Put, awsiac.WriteS3Delete:
		if len(f.pos) != 1 {
			return harnessAWSWriteUsage(h.errOut)
		}
		params["bucket"], params["key"] = splitBucketKey(f.pos[0])
		if op == awsiac.WriteS3Delete {
			if f.version != "" {
				params["version"] = f.version
			}
			break
		}
		data, err := f.content(h)
		if err != nil {
			return err
		}
		params["data"] = base64.StdEncoding.EncodeToString(data)
		if f.contentType != "" {
			params["content_type"] = f.contentType
		}
	case awsiac.WriteDDBPut, awsiac.WriteDDBDelete:
		want := 2
		if op == awsiac.WriteDDBPut && f.fileSet {
			want = 1
		}
		if len(f.pos) != want {
			return harnessAWSWriteUsage(h.errOut)
		}
		params["table"] = f.pos[0]
		var raw []byte
		if want == 2 {
			raw = []byte(f.pos[1])
		} else {
			b, err := f.content(h)
			if err != nil {
				return err
			}
			raw = b
		}
		what := "the item"
		if op == awsiac.WriteDDBDelete {
			what = "the key"
		}
		m, err := jsonObject(raw, what)
		if err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
			return harnessAWSWriteUsage(h.errOut)
		}
		if op == awsiac.WriteDDBPut {
			params["item"] = m
		} else {
			params["key"] = m
		}
	case awsiac.WriteSQSSend:
		if len(f.pos) != 1 {
			return harnessAWSWriteUsage(h.errOut)
		}
		body, err := f.content(h)
		if err != nil {
			return err
		}
		params["queue"], params["body"] = f.pos[0], string(body)
		if len(f.attrs) > 0 {
			params["attributes"] = map[string]string(f.attrs)
		}
		if f.group != "" {
			params["group_id"] = f.group
		}
		if f.dedup != "" {
			params["dedup_id"] = f.dedup
		}
	case awsiac.WriteReset:
		if len(f.pos) != 0 {
			return harnessAWSWriteUsage(h.errOut)
		}
	default:
		return harnessAWSWriteUsage(h.errOut)
	}
	raw, err := harnessCallOut(h, harnessArgs{json: f.json}, "harness.aws_write", params)
	if err != nil {
		return err
	}
	if f.json {
		return printHarnessJSON(h.out, raw)
	}
	var res harness.AWSWriteResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	printAWSWrite(h.out, res)
	return nil
}

func printAWSWrite(w io.Writer, r harness.AWSWriteResult) {
	switch r.Op {
	case awsiac.WriteS3Put:
		_, _ = fmt.Fprintf(w, "put %d bytes in %s (etag %s", r.Bytes, r.Bucket, r.ETag)
		if r.VersionID != "" {
			_, _ = fmt.Fprintf(w, ", version %s", r.VersionID)
		}
		_, _ = fmt.Fprintln(w, ")")
	case awsiac.WriteS3Delete:
		_, _ = fmt.Fprintf(w, "deleted from %s", r.Bucket)
		if r.DeleteMarker {
			_, _ = fmt.Fprintf(w, " (delete marker %s)", r.VersionID)
		}
		_, _ = fmt.Fprintln(w)
	case awsiac.WriteDDBPut:
		_, _ = fmt.Fprintf(w, "put an item in %s\n", r.Table)
	case awsiac.WriteDDBDelete:
		if r.Found != nil && !*r.Found {
			_, _ = fmt.Fprintf(w, "no such item in %s\n", r.Table)
			return
		}
		_, _ = fmt.Fprintf(w, "deleted an item from %s\n", r.Table)
	case awsiac.WriteSQSSend:
		_, _ = fmt.Fprintf(w, "sent message %s to %s\n", r.MessageID, r.Queue)
	case awsiac.WriteReset:
		_, _ = fmt.Fprintf(w, "reset %s: %d resources, %d calls applied again\n", r.Service, r.Resources, r.Calls)
	}
	if r.Note != "" {
		_, _ = fmt.Fprintln(w, r.Note)
	}
}
