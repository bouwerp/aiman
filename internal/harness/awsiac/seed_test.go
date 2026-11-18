package awsiac

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeedParse(t *testing.T) {
	sf, err := ParseSeed([]byte(`
objects: [{bucket: orders.UploadBucket, from: testdata/s3}]
items: [{table: orders.OrdersTable, file: testdata/ddb.jsonl}]
messages: [{queue: q, file: m.jsonl}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if sf.Objects[0].ContentType != "auto" || sf.Items[0].Format != FormatPlain {
		t.Fatalf("defaults %+v", sf)
	}
	for name, bad := range map[string]string{
		"unknown key":   "objects: [{bucket: b, from: d, extra: 1}]",
		"absolute":      "items: [{table: t, file: /etc/passwd}]",
		"escapes":       "messages: [{queue: q, file: ../x}]",
		"no target":     "items: [{file: a}]",
		"bad format":    "items: [{table: t, file: a, format: csv}]",
		"no from":       "objects: [{bucket: b}]",
		"alias":         "a: &x {table: t, file: f}\nitems: [*x]",
		"slash prefix":  "objects: [{bucket: b, from: d, prefix: /x}]",
		"not a mapping": "items: 3",
	} {
		if _, err := ParseSeed([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseSeed([]byte(strings.Repeat("#", MaxSeedFileBytes+1))); err == nil {
		t.Error("an oversized seed file was accepted")
	}
}

func TestSeedTargets(t *testing.T) {
	p := samplePlan(t)
	sampleSeed(t, p)
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	s := p.Seed
	if s.Objects[0].Resource != "orders.UploadBucket" || s.Items[0].Resource != "orders.OrdersTable" || s.Messages[0].URL == "" {
		t.Fatalf("bound %+v", s)
	}
	var seedOps []string
	for _, st := range p.Steps {
		if st.Seed != nil {
			seedOps = append(seedOps, st.Operation)
		}
	}
	if strings.Join(seedOps, ",") != "PutObject,BatchWriteItem,SendMessageBatch" {
		t.Fatalf("seed steps %v", seedOps)
	}
	for name, yml := range map[string]string{
		"skipped":    "items: [{table: orders.Gone, file: f}]",
		"wrong kind": "items: [{table: orders.Events, file: f}]",
		"no literal": "objects: [{bucket: nope, from: d}]",
		"attribute":  "objects: [{bucket: orders.UploadBucket.Arn, from: d}]",
	} {
		p := samplePlan(t)
		sf, err := ParseSeed([]byte(yml))
		if err != nil {
			t.Fatal(err)
		}
		target := func(s string) (Ref, bool, error) {
			if strings.HasSuffix(s, ".Arn") {
				return Ref{Stack: "orders", LogicalID: "UploadBucket", Attr: "Arn"}, true, nil
			}
			return testTargets(s)
		}
		if err := p.SetSeed("seed.yaml", sf, target); err != nil {
			continue
		}
		if err := p.Build(); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestSeedItemAttributes(t *testing.T) {
	got, err := ItemAttributes([]byte(`{"s":"a","n":12.50,"big":123456789012345678901,"b":true,"z":null,"l":[1,"x"],"m":{"k":"v"},
		"bin":{"$b64":"aGk="},"ss":{"$ss":["a","b"]},"ns":{"$ns":[1,"2"]},"bs":{"$bs":["aGk="]}}`), FormatPlain)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	for _, want := range []string{
		`"s":{"S":"a"}`, `"n":{"N":"12.50"}`, `"big":{"N":"123456789012345678901"}`, `"b":{"BOOL":true}`, `"z":{"NULL":true}`,
		`"l":{"L":[{"N":"1"},{"S":"x"}]}`, `"m":{"M":{"k":{"S":"v"}}}`, `"bin":{"B":"aGk="}`,
		`"ss":{"SS":["a","b"]}`, `"ns":{"NS":["1","2"]}`, `"bs":{"BS":["aGk="]}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	for _, bad := range []string{`[1]`, `"x"`, `{"a":{"$b64":"!!"}}`, `{"a":{"$ss":[]}}`, `{"a":{"$ns":["x"]}}`, `{"a":1} {"b":2}`, `nope`} {
		if _, err := ItemAttributes([]byte(bad), FormatPlain); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	raw, err := ItemAttributes([]byte(`{"pk":{"S":"a"}}`), FormatDynamoDB)
	if err != nil || raw["pk"].(map[string]any)["S"] != "a" {
		t.Fatalf("dynamodb format %v %v", raw, err)
	}
}

func TestSeedMessageLine(t *testing.T) {
	m, err := ParseMessageLine([]byte(`{"body":"b","attributes":{"k":"v"},"group_id":"g","dedup_id":"d"}`))
	if err != nil || m.Body != "b" || m.Attributes["k"] != "v" || m.GroupID != "g" || m.DedupID != "d" {
		t.Fatalf("%+v %v", m, err)
	}
	if m, _ := ParseMessageLine([]byte(`{"order":1}`)); m.Body != `{"order":1}` {
		t.Fatalf("plain JSON body %+v", m)
	}
	if m, _ := ParseMessageLine([]byte(`hello`)); m.Body != "hello" {
		t.Fatalf("text body %+v", m)
	}
	if _, err := ParseMessageLine([]byte(`{"body":"b","x":1}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
