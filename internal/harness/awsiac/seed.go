package awsiac

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Seed kinds, the SeedStep kinds.
const (
	SeedKindObjects  = "objects"
	SeedKindItems    = "items"
	SeedKindMessages = "messages"
)

// Seed bounds: what one apply loads at most.
const (
	MaxSeedObjects     = 5000
	MaxSeedObjectBytes = 256 << 20
	MaxSeedItems       = 100_000
	MaxSeedMessages    = 10_000
	// MaxSeedTargets bounds the distinct buckets, tables and queues
	// (each) a seed file fills.
	MaxSeedTargets = 50
	// MaxSeedFileBytes bounds the seed file itself.
	MaxSeedFileBytes = 1 << 20
	// MaxSeedLineBytes bounds one item or message line.
	MaxSeedLineBytes = 400 << 10
)

// SeedFile is the seed YAML as written: objects copied from a directory of
// the repository into a bucket, items from a JSON-lines file into a table,
// messages from a JSON-lines file onto a queue. Targets are a reference
// (<stack>.<logical id>) or a literal physical name.
type SeedFile struct {
	Objects  []SeedObjectsEntry  `yaml:"objects"`
	Items    []SeedItemsEntry    `yaml:"items"`
	Messages []SeedMessagesEntry `yaml:"messages"`
}

// SeedObjectsEntry copies every regular file under From into Bucket, keyed
// by its path relative to From after Prefix. ContentType is auto (by
// extension, the default) or a literal type.
type SeedObjectsEntry struct {
	Bucket      string `yaml:"bucket"`
	From        string `yaml:"from"`
	Prefix      string `yaml:"prefix"`
	ContentType string `yaml:"content_type"`
}

// SeedItemsEntry writes one item per line of File into Table. Format is
// plain (JSON mapped to attribute values, the default) or dynamodb
// (AttributeValue JSON).
type SeedItemsEntry struct {
	Table  string `yaml:"table"`
	File   string `yaml:"file"`
	Format string `yaml:"format"`
}

// SeedMessagesEntry sends one message per line of File to Queue: the line
// is the body, or an object {body, attributes, group_id, dedup_id}.
type SeedMessagesEntry struct {
	Queue string `yaml:"queue"`
	File  string `yaml:"file"`
}

// Item formats.
const (
	FormatPlain    = "plain"
	FormatDynamoDB = "dynamodb"
)

// ParseSeed reads a seed file strictly: unknown keys, aliases, paths that
// leave the repository and unknown formats are refused.
func ParseSeed(b []byte) (*SeedFile, error) {
	if len(b) > MaxSeedFileBytes {
		return nil, fmt.Errorf("the seed file is over %d bytes", MaxSeedFileBytes)
	}
	if bytes.Contains(b, []byte("*")) {
		var probe yaml.Node
		if err := yaml.Unmarshal(b, &probe); err == nil && hasAlias(&probe) {
			return nil, errors.New("the seed file uses YAML aliases, which are not allowed")
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var sf SeedFile
	if err := dec.Decode(&sf); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the seed file: %w", err)
	}
	var errs []error
	for i := range sf.Objects {
		e := &sf.Objects[i]
		at := fmt.Sprintf("objects[%d]", i)
		errs = append(errs, needTarget(at, "bucket", e.Bucket), checkRepoPath(at+".from", e.From))
		if e.ContentType == "" {
			e.ContentType = "auto"
		}
		if strings.ContainsAny(e.ContentType, "\r\n") {
			errs = append(errs, fmt.Errorf("%s.content_type is not a type", at))
		}
		if strings.HasPrefix(e.Prefix, "/") {
			errs = append(errs, fmt.Errorf("%s.prefix %q must not start with /", at, e.Prefix))
		}
	}
	for i := range sf.Items {
		e := &sf.Items[i]
		at := fmt.Sprintf("items[%d]", i)
		errs = append(errs, needTarget(at, "table", e.Table), checkRepoPath(at+".file", e.File))
		switch e.Format {
		case "":
			e.Format = FormatPlain
		case FormatPlain, FormatDynamoDB:
		default:
			errs = append(errs, fmt.Errorf("%s.format %q is not plain or dynamodb", at, e.Format))
		}
	}
	for i := range sf.Messages {
		e := &sf.Messages[i]
		at := fmt.Sprintf("messages[%d]", i)
		errs = append(errs, needTarget(at, "queue", e.Queue), checkRepoPath(at+".file", e.File))
	}
	return &sf, errors.Join(errs...)
}

func hasAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if hasAlias(c) {
			return true
		}
	}
	return false
}

func needTarget(at, field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s.%s is required", at, field)
	}
	return nil
}

// checkRepoPath refuses an empty path, an absolute one and one that leaves
// the repository.
func checkRepoPath(at, p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%s is required", at)
	case strings.HasPrefix(p, "/") || strings.Contains(p, "\\"):
		return fmt.Errorf("%s %q must be a relative repository path", at, p)
	}
	if c := path.Clean(p); c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("%s %q leaves the repository", at, p)
	}
	return nil
}

// TargetFunc reads a seed target: a reference into the plan (isRef), or a
// literal physical name (isRef false).
type TargetFunc func(target string) (ref Ref, isRef bool, err error)

// Seed is a seed file bound to the plan: every target resolved to a
// planned resource. Build fills Resource, Physical and the queue fields.
type Seed struct {
	File     string         `json:"file"`
	Objects  []SeedObjects  `json:"objects,omitempty"`
	Items    []SeedItems    `json:"items,omitempty"`
	Messages []SeedMessages `json:"messages,omitempty"`

	targets map[string]targetSpec
}

type targetSpec struct {
	ref   *Ref
	label string
}

// SeedObjects is an objects entry with its bucket.
type SeedObjects struct {
	Target      string `json:"target"`
	Resource    string `json:"resource,omitempty"`
	Physical    string `json:"physical,omitempty"`
	From        string `json:"from"`
	Prefix      string `json:"prefix,omitempty"`
	ContentType string `json:"content_type"`
}

// SeedItems is an items entry with its table.
type SeedItems struct {
	Target   string `json:"target"`
	Resource string `json:"resource,omitempty"`
	Physical string `json:"physical,omitempty"`
	File     string `json:"file"`
	Format   string `json:"format"`
}

// SeedMessages is a messages entry with its queue.
type SeedMessages struct {
	Target   string `json:"target"`
	Resource string `json:"resource,omitempty"`
	Physical string `json:"physical,omitempty"`
	URL      string `json:"url,omitempty"`
	Fifo     bool   `json:"fifo,omitempty"`
	File     string `json:"file"`
}

// SetSeed binds a parsed seed file named file to the plan; Build resolves
// its targets.
func (p *Plan) SetSeed(file string, sf *SeedFile, target TargetFunc) error {
	s := &Seed{File: file, targets: map[string]targetSpec{}}
	var errs []error
	bind := func(at, t string) {
		ref, isRef, err := target(t)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", at, err))
		case isRef:
			if ref.Output != "" || ref.Attr != "" {
				errs = append(errs, fmt.Errorf("%s: %q must name a resource, <stack>.<logical id>", at, t))
				return
			}
			s.targets[t] = targetSpec{ref: &ref, label: t}
		default:
			s.targets[t] = targetSpec{label: t}
		}
	}
	for i, e := range sf.Objects {
		bind(fmt.Sprintf("seed objects[%d].bucket", i), e.Bucket)
		s.Objects = append(s.Objects, SeedObjects{Target: e.Bucket, From: e.From, Prefix: e.Prefix, ContentType: e.ContentType})
	}
	for i, e := range sf.Items {
		bind(fmt.Sprintf("seed items[%d].table", i), e.Table)
		s.Items = append(s.Items, SeedItems{Target: e.Table, File: e.File, Format: e.Format})
	}
	for i, e := range sf.Messages {
		bind(fmt.Sprintf("seed messages[%d].queue", i), e.Queue)
		s.Messages = append(s.Messages, SeedMessages{Target: e.Queue, File: e.File})
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	p.Seed = s
	p.built = false
	return nil
}

// checkSeed resolves the seed's targets against the built resources: a
// target that names a skipped resource, no resource, or one of the wrong
// kind is an error, as are more distinct targets than MaxSeedTargets.
func (p *Plan) checkSeed() error {
	s := p.Seed
	if s == nil {
		return nil
	}
	var errs []error
	find := func(at, target string, kind Kind) *Resource {
		spec := s.targets[target]
		var r *Resource
		if spec.ref != nil {
			r = p.Resource(Key(spec.ref.Stack, spec.ref.LogicalID))
			if r == nil {
				errs = append(errs, fmt.Errorf("seed %s %q is not a planned resource (skipped, not emulated or not in the stack)", at, target))
				return nil
			}
		} else {
			for _, x := range p.Resources {
				if x.Kind == kind && x.Physical == target {
					r = x
				}
			}
			if r == nil {
				errs = append(errs, fmt.Errorf("seed %s %q names no planned %s", at, target, kind))
				return nil
			}
		}
		if r.Kind != kind {
			errs = append(errs, fmt.Errorf("seed %s %q is a %s, not a %s", at, target, r.Kind, kind))
			return nil
		}
		return r
	}
	distinct := map[Kind]map[string]bool{KindBucket: {}, KindTable: {}, KindQueue: {}}
	for i := range s.Objects {
		if r := find(fmt.Sprintf("objects[%d]", i), s.Objects[i].Target, KindBucket); r != nil {
			s.Objects[i].Resource, s.Objects[i].Physical = r.Key(), r.Physical
			distinct[KindBucket][r.Key()] = true
		}
	}
	for i := range s.Items {
		if r := find(fmt.Sprintf("items[%d]", i), s.Items[i].Target, KindTable); r != nil {
			s.Items[i].Resource, s.Items[i].Physical = r.Key(), r.Physical
			distinct[KindTable][r.Key()] = true
		}
	}
	for i := range s.Messages {
		if r := find(fmt.Sprintf("messages[%d]", i), s.Messages[i].Target, KindQueue); r != nil {
			m := &s.Messages[i]
			m.Resource, m.Physical, m.URL, m.Fifo = r.Key(), r.Physical, r.Attrs[AttrURL], r.Queue.Fifo
			distinct[KindQueue][r.Key()] = true
		}
	}
	for _, k := range []Kind{KindBucket, KindTable, KindQueue} {
		if n := len(distinct[k]); n > MaxSeedTargets {
			errs = append(errs, fmt.Errorf("the seed fills %d %ss; at most %d", n, k, MaxSeedTargets))
		}
	}
	return errors.Join(errs...)
}

// ItemAttributes turns one item line into DynamoDB AttributeValue JSON.
// Plain JSON maps by type (string S, number N, bool BOOL, null NULL, array
// L, object M); {"$b64": "…"} is B and {"$ss"|"$ns"|"$bs": […]} are sets.
// The dynamodb format is AttributeValue JSON already and is checked only
// for being an object.
func ItemAttributes(line []byte, format string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value on the line")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("an item must be a JSON object")
	}
	if format == FormatDynamoDB {
		return obj, nil
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		av, err := attributeValue(v)
		if err != nil {
			return nil, fmt.Errorf("attribute %s: %w", k, err)
		}
		out[k] = av
	}
	return out, nil
}

func attributeValue(v any) (map[string]any, error) {
	switch t := v.(type) {
	case nil:
		return map[string]any{"NULL": true}, nil
	case bool:
		return map[string]any{"BOOL": t}, nil
	case string:
		return map[string]any{"S": t}, nil
	case json.Number:
		return map[string]any{"N": t.String()}, nil
	case []any:
		l := make([]any, 0, len(t))
		for i, e := range t {
			av, err := attributeValue(e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			l = append(l, av)
		}
		return map[string]any{"L": l}, nil
	case map[string]any:
		if len(t) == 1 {
			for k, e := range t {
				if av, ok, err := markedValue(k, e); ok || err != nil {
					return av, err
				}
			}
		}
		m := make(map[string]any, len(t))
		for k, e := range t {
			av, err := attributeValue(e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			m[k] = av
		}
		return map[string]any{"M": m}, nil
	}
	return nil, fmt.Errorf("unsupported JSON value %T", v)
}

// markedValue reads the typed markers $b64, $ss, $ns and $bs.
func markedValue(k string, e any) (map[string]any, bool, error) {
	switch k {
	case "$b64":
		s, ok := e.(string)
		if !ok {
			return nil, true, errors.New("$b64 takes a base64 string")
		}
		if _, err := base64.StdEncoding.DecodeString(s); err != nil {
			return nil, true, fmt.Errorf("$b64: %w", err)
		}
		return map[string]any{"B": s}, true, nil
	case "$ss", "$ns", "$bs":
		arr, ok := e.([]any)
		if !ok || len(arr) == 0 {
			return nil, true, fmt.Errorf("%s takes a non-empty array", k)
		}
		set := make([]string, 0, len(arr))
		for _, x := range arr {
			var s string
			switch y := x.(type) {
			case string:
				s = y
			case json.Number:
				s = y.String()
			default:
				return nil, true, fmt.Errorf("%s holds a %T", k, x)
			}
			if k == "$ns" {
				if _, err := json.Number(s).Float64(); err != nil {
					return nil, true, fmt.Errorf("$ns holds %q, not a number", s)
				}
			}
			if k == "$bs" {
				if _, err := base64.StdEncoding.DecodeString(s); err != nil {
					return nil, true, fmt.Errorf("$bs: %w", err)
				}
			}
			set = append(set, s)
		}
		return map[string]any{strings.ToUpper(k[1:]): set}, true, nil
	}
	return nil, false, nil
}

// SeedMessage is one message line read.
type SeedMessage struct {
	Body       string            `json:"body"`
	Attributes map[string]string `json:"attributes,omitempty"`
	GroupID    string            `json:"group_id,omitempty"`
	DedupID    string            `json:"dedup_id,omitempty"`
}

// ParseMessageLine reads a message line: an object with a string body is
// the structured form; any other line is the body as it is.
func ParseMessageLine(line []byte) (SeedMessage, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var probe map[string]json.RawMessage
		if json.Unmarshal(trimmed, &probe) == nil {
			if _, ok := probe["body"]; ok {
				dec := json.NewDecoder(bytes.NewReader(trimmed))
				dec.DisallowUnknownFields()
				var m SeedMessage
				if err := dec.Decode(&m); err != nil {
					return SeedMessage{}, fmt.Errorf("message: %w", err)
				}
				return m, nil
			}
		}
	}
	return SeedMessage{Body: string(line)}, nil
}
