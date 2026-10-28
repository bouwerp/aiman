package agenthook

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// requireNodeTS skips unless node can run a .mts file with type annotations
// (node 22.6+ with --experimental-strip-types). Pi loads extensions through
// jiti, which accepts the same erasable TypeScript.
func requireNodeTS(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	probe := filepath.Join(t.TempDir(), "probe.mts")
	if err := os.WriteFile(probe, []byte("const n: number = 1; console.log(n);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--experimental-strip-types", "--no-warnings", probe).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skipf("node cannot strip TypeScript types: %v %s", err, out)
	}
	return node
}

// piHarness drives the extension the way Pi 1.4 does: a default factory that
// receives ExtensionAPI, then (event, ctx) handlers. ctx.sessionManager is the
// only source of the session id and file.
const piHarness = `const mod = await import(process.argv[2]);
const handlers = {};
mod.default({ on: (ev, h) => { (handlers[ev] ||= []).push(h); return () => {}; } });
let idle = true;
const ctx = {
  sessionManager: {
    getSessionId: () => "01a18182-808d-7087-a9b4-a7a8665626b3",
    getSessionFile: () => "/home/u/.pi/agent/sessions/--w--/2026_01a18182.jsonl",
    getSessionName: () => undefined,
  },
  isIdle: () => idle,
};
const fire = (ev, e) => { for (const h of handlers[ev] || []) h({ type: ev, ...e }, ctx); };
fire("session_start", { reason: "startup" });
idle = false;
fire("agent_start", {});
fire("agent_end", { messages: [] });
fire("ui_prompt_start", { reason: "ui_prompt", kind: "confirm", title: "Allow rm?" });
fire("ui_prompt_end", { reason: "ui_prompt", kind: "confirm", title: "Allow rm?" });
idle = true;
fire("agent_settled", { aborted: false });
fire("session_shutdown", { reason: "new" });
fire("session_shutdown", { reason: "quit" });
`

func runPiExtension(t *testing.T) []Report {
	t.Helper()
	node := requireNodeTS(t)
	dir := t.TempDir()
	ext := filepath.Join(dir, "aiman-native-session.mts")
	if err := os.WriteFile(ext, []byte(piExtension), 0o600); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(piHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	// One file per report: the children run concurrently, so a shared log
	// would interleave their bodies.
	reportDir := filepath.Join(dir, "reports")
	if err := os.Mkdir(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-aiman")
	script := "#!/bin/sh\ncat > \"$(mktemp '" + reportDir + "/r.XXXXXX')\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext)
	cmd.Env = append(os.Environ(), "AIMAN_ENV=1", "AIMAN_ID=s1", "AIMAN_BIN_PATH="+bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: %v\n%s", err, out)
	}
	files, err := filepath.Glob(filepath.Join(reportDir, "r.*"))
	if err != nil {
		t.Fatal(err)
	}
	var reports []Report
	for _, path := range files {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: test temp dir
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, ExtractReport(raw))
	}
	// Each report is its own child process, so they can land out of order.
	sort.Slice(reports, func(i, j int) bool { return reports[i].Seq < reports[j].Seq })
	return reports
}

func TestPiExtensionReportsLifecycleThroughExtensionAPI(t *testing.T) {
	reports := runPiExtension(t)
	var states []string
	for _, r := range reports {
		s := string(r.State)
		if r.Ended {
			s += "+ended"
		}
		states = append(states, s)
		if r.ID != "01a18182-808d-7087-a9b4-a7a8665626b3" {
			t.Errorf("report without the Pi session id: %+v", r)
		}
		if !strings.HasSuffix(r.Path, ".jsonl") {
			t.Errorf("report without the session file: %+v", r)
		}
	}
	// agent_end is not final in Pi 1.4 (retries and queued work follow it), so
	// idle comes only from agent_settled. A session replaced by /new is not an end.
	want := []string{
		string(domain.AgentStateIdle),
		string(domain.AgentStateWorking),
		string(domain.AgentStateWaitingInput),
		string(domain.AgentStateWorking),
		string(domain.AgentStateIdle),
		string(domain.AgentStateIdle) + "+ended",
	}
	if strings.Join(states, ",") != strings.Join(want, ",") {
		t.Fatalf("states %v, want %v", states, want)
	}
	if reports[2].Message != "Allow rm?" {
		t.Errorf("waiting_input message %q", reports[2].Message)
	}
}

// Serve drops a report whose seq is below the last one it applied. A counter
// that restarts at 1 in every Pi process would be ignored after a relaunch or
// /reload until it caught up with the previous run.
func TestPiExtensionSeqOutlivesTheProcess(t *testing.T) {
	first := runPiExtension(t)
	second := runPiExtension(t)
	last := first[len(first)-1].Seq
	if second[0].Seq <= last {
		t.Fatalf("second run seq %d is not after first run %d", second[0].Seq, last)
	}
}

func TestPiExtensionHandlesSpawnFailure(t *testing.T) {
	if !strings.Contains(piExtension, `.on("error"`) {
		t.Fatal("an unhandled spawn error (aiman missing from PATH) would crash Pi")
	}
}

func TestKiloPluginSeqIsTimeBased(t *testing.T) {
	if !strings.Contains(kiloPlugin, "Date.now()") {
		t.Fatal("kilo plugin seq restarts at 1 per process, so serve drops reports after a relaunch")
	}
}
