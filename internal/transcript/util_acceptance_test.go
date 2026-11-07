//go:build acceptance

package transcript_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/transcript"
)

func TestCursor(t *testing.T) {
	urlSafe := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	cases := []transcript.Cursor{
		{FileID: "uuid:3f1e-ab/+=?&", Kind: transcript.PositionOffset, Position: 9123456},
		{FileID: "sha256:00ff", Kind: transcript.PositionOrdinal, Position: 42},
		{FileID: "x", Kind: transcript.PositionOffset, Position: 0},
	}
	encoded := map[string]bool{}
	for _, c := range cases {
		s := transcript.EncodeCursor(c)
		if !urlSafe.MatchString(s) || url.QueryEscape(s) != s {
			t.Errorf("EncodeCursor(%+v) = %q, not URL-safe", c, s)
		}
		encoded[s] = true
		got, err := transcript.DecodeCursor(s)
		if err != nil || got != c {
			t.Errorf("DecodeCursor(%q) = %+v %v, want %+v", s, got, err, c)
		}
	}
	if len(encoded) != len(cases) {
		t.Error("distinct cursors encode to the same string")
	}
	off := transcript.Cursor{FileID: "f", Kind: transcript.PositionOffset, Position: 7}
	ord := transcript.Cursor{FileID: "f", Kind: transcript.PositionOrdinal, Position: 7}
	if transcript.EncodeCursor(off) == transcript.EncodeCursor(ord) {
		t.Error("offset and ordinal cursors at the same position encode alike")
	}

	raw, err := json.Marshal(struct{ C transcript.Cursor }{ord})
	if err != nil || string(raw) != `{"C":"`+transcript.EncodeCursor(ord)+`"}` {
		t.Errorf("cursor JSON = %s %v, want an opaque string", raw, err)
	}
	var back struct{ C transcript.Cursor }
	if err := json.Unmarshal(raw, &back); err != nil || back.C != ord {
		t.Errorf("cursor JSON round trip = %+v %v", back.C, err)
	}

	for _, bad := range []string{"", "!!!", "not a cursor", "e30", "eyJ4IjoxfQ"} {
		if _, err := transcript.DecodeCursor(bad); err == nil {
			t.Errorf("DecodeCursor(%q) accepted bad input", bad)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		{"hello", 10, "hello", false},
		{"hello", 5, "hello", false},
		{"hello world", 5, "hello", true},
		{"héllo", 2, "h", true},
		{"héllo", 3, "hé", true},
		{"abc", 0, "", true},
	}
	for _, c := range cases {
		got, cut := transcript.Truncate(c.in, c.max)
		if got != c.want || cut != c.cut || !utf8.ValidString(got) {
			t.Errorf("Truncate(%q, %d) = %q %v, want %q %v", c.in, c.max, got, cut, c.want, c.cut)
		}
	}

	_, ix, _ := newIndex(t, "A 1 "+strings.Repeat("x", 500)+"\nA 2 short\n", transcript.IndexOptions{MaxFieldBytes: 100})
	p := mustPage(t, ix, nil, 0)
	if len(p.Items) != 2 {
		t.Fatalf("items = %q", ids(p.Items))
	}
	if len(p.Items[0].Text) > 100 || p.Items[0].Text == "" || !p.Items[0].Truncated {
		t.Errorf("long item: %d bytes truncated=%v, want <=100 and true", len(p.Items[0].Text), p.Items[0].Truncated)
	}
	if p.Items[1].Text != "short" || p.Items[1].Truncated {
		t.Errorf("short item = %+v", p.Items[1])
	}

	_, ix, _ = newIndex(t, "A 1 "+strings.Repeat("y", 70<<10)+"\n", transcript.IndexOptions{})
	if it, ok := ix.Item("1"); !ok || len(it.Text) > transcript.DefaultMaxFieldBytes || !it.Truncated {
		t.Errorf("default cap: %d bytes truncated=%v ok=%v", len(it.Text), it.Truncated, ok)
	}
}

func TestLocate(t *testing.T) {
	home := t.TempDir()
	const cwd, id = "/home/code/repos/my.app", "9f3c-1"
	want := map[string]string{
		"claude": filepath.Join(home, ".claude/projects/-home-code-repos-my-app", id+".jsonl"),
		"codex":  filepath.Join(home, ".codex/sessions/2026/11/07/rollout-2026-11-07T10-00-00-"+id+".jsonl"),
		"pi":     filepath.Join(home, ".pi/agent/sessions/--home-code-repos-my.app--/1730970000000_"+id+".jsonl"),
		"grok":   filepath.Join(home, ".grok/sessions/%2Fhome%2Fcode%2Frepos%2Fmy.app", id, "updates.jsonl"),
	}
	for _, p := range want {
		writeFile(t, p, "{}\n")
	}
	writeFile(t, filepath.Join(home, ".codex/sessions/2026/11/06/rollout-2026-11-06T09-00-00-other.jsonl"), "{}\n")

	for agent, p := range want {
		got, kind, err := transcript.Locate(home, agent, id, "", cwd)
		if err != nil || got != p || kind != agent {
			t.Errorf("Locate(%s) = %q %q %v, want %q %q", agent, got, kind, err, p, agent)
		}
	}

	explicit := filepath.Join(home, "elsewhere", "x.jsonl")
	writeFile(t, explicit, "{}\n")
	if got, kind, err := transcript.Locate(home, "claude", id, explicit, cwd); err != nil || got != explicit || kind != "claude" {
		t.Errorf("explicit path: %q %q %v, want %q claude", got, kind, err, explicit)
	}

	for _, c := range [][2]string{{"claude", "nope"}, {"codex", "nope"}, {"kilo", id}} {
		if _, _, err := transcript.Locate(home, c[0], c[1], "", cwd); !errors.Is(err, transcript.ErrNoTranscript) {
			t.Errorf("Locate(%s, %s) err = %v, want ErrNoTranscript", c[0], c[1], err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	record := `{"type":"user","uuid":"u-1","message":"hi"}` + "\n"
	files := map[string]string{
		"a": `{"type":"last-prompt","leafUuid":"l","sessionId":"s"}` + "\n" + record,
		"b": `{"type":"ai-title","title":"t"}` + "\n" + record + `{"type":"assistant","uuid":"u-2"}` + "\n",
		"c": `{"type":"last-prompt","leafUuid":"l","sessionId":"s"}` + "\n" + `{"type":"user","uuid":"u-9","message":"hi"}` + "\n",
		"d": "A 1 one\n",
		"e": "A 2 two\n",
	}
	fp := map[string]string{}
	for name, content := range files {
		p := filepath.Join(dir, name)
		writeFile(t, p, content)
		got, err := transcript.Fingerprint(p)
		if err != nil || got == "" {
			t.Fatalf("Fingerprint(%s) = %q %v", name, got, err)
		}
		fp[name] = got
	}
	if fp["a"] != fp["b"] {
		t.Error("header lines changed the id of the same first record")
	}
	if fp["a"] == fp["c"] || fp["d"] == fp["e"] || fp["a"] == fp["d"] {
		t.Errorf("different first records share an id: %v", fp)
	}
	appendFile(t, filepath.Join(dir, "a"), `{"type":"assistant","uuid":"u-3"}`+"\n")
	if got, _ := transcript.Fingerprint(filepath.Join(dir, "a")); got != fp["a"] {
		t.Error("append changed the id")
	}
	if _, err := transcript.Fingerprint(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file gave no error")
	}
}

func TestRedact(t *testing.T) {
	lines := []string{
		`{"type":"assistant","uuid":"abc-123","sessionId":"secret-session","cwd":"/home/alice/project","n":3,"ok":true,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01XYZ","name":"Bash","input":{"command":"cat /etc/passwd"}}]}}`,
		`{"type":"user","parentUuid":"abc-123","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01XYZ","content":"root:x:0:0","is_error":false}]}}`,
		`{"method":"session/update","params":{"sessionId":"secret-session","update":{"sessionUpdate":"agent_message_chunk","status":"completed","kind":"execute","state":"done","subtype":"init","content":{"type":"text","text":"hello there"}}}}`,
	}
	secrets := []string{"abc-123", "secret-session", "/home/alice", "alice", "toolu_01XYZ", "cat /etc/passwd", "passwd", "root:x:0:0", "hello there"}
	out := make([]map[string]any, len(lines))
	for i, l := range lines {
		r := transcript.Redact([]byte(l))
		for _, s := range secrets {
			if strings.Contains(string(r), s) {
				t.Errorf("line %d: %q survived redaction", i, s)
			}
		}
		if err := json.Unmarshal(r, &out[i]); err != nil {
			t.Fatalf("line %d: redacted output is not JSON: %v (%q)", i, err, r)
		}
	}
	if len(out[0]) != 6 || out[0]["n"] != float64(3) || out[0]["ok"] != true {
		t.Errorf("structure, numbers or booleans changed: %v", out[0])
	}
	if out[0]["type"] != "assistant" || out[0]["uuid"] == "" || out[0]["uuid"] == nil {
		t.Errorf("line 0 type/uuid = %v %v", out[0]["type"], out[0]["uuid"])
	}
	use := out[0]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	res := out[1]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if use["type"] != "tool_use" || use["name"] != "Bash" || out[0]["message"].(map[string]any)["role"] != "assistant" {
		t.Errorf("enum keys not kept: %v", use)
	}
	if out[1]["parentUuid"] != out[0]["uuid"] || res["tool_use_id"] != use["id"] {
		t.Error("shared ids no longer pair after redaction")
	}
	upd := out[2]["params"].(map[string]any)["update"].(map[string]any)
	if out[2]["method"] != "session/update" || upd["sessionUpdate"] != "agent_message_chunk" ||
		upd["status"] != "completed" || upd["kind"] != "execute" || upd["state"] != "done" || upd["subtype"] != "init" {
		t.Errorf("grok enum keys not kept: %v", out[2])
	}
	if out[2]["params"].(map[string]any)["sessionId"] != out[0]["sessionId"] {
		t.Error("same session id redacted differently across lines")
	}
}
