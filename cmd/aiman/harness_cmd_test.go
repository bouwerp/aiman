package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type harnessCall struct {
	method string
	params map[string]any
}

// fakeHarness answers each method from a queue; an entry that is an error is
// returned as the call's error.
type fakeHarness struct {
	t       *testing.T
	calls   []harnessCall
	answers map[string][]any
}

func (f *fakeHarness) call(method string, params map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, harnessCall{method, params})
	q := f.answers[method]
	if len(q) == 0 {
		f.t.Fatalf("unexpected call %s %v", method, params)
	}
	a := q[0]
	if len(q) > 1 {
		f.answers[method] = q[1:]
	}
	if err, ok := a.(error); ok {
		return nil, err
	}
	return json.RawMessage(a.(string)), nil
}

func runHarnessTest(t *testing.T, f *fakeHarness, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := harnessMain(args, harnessIO{
		getenv: func(k string) string { return env[k] },
		call:   f.call,
		out:    &out,
		errOut: &errOut,
		sleep:  func(time.Duration) {},
	})
	return out.String(), errOut.String(), err
}

const harnessRunJSON = `{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3d4e5f6a7b8","dirty":false,"source":"file","network":"offline","state":"%s","ports":[{"name":"web","service":"app","host_port":41873}],"steps":[{"n":1,"do":"Open {{web}}/orders/42","expect":"Refunded"}],"checks":[{"name":"refund row","run":"x","exit":0}]}}`

func runJSON(state string) string { return strings.Replace(harnessRunJSON, "%s", state, 1) }

func TestHarnessCmdUpWaitPollsStatus(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.up":     {runJSON("starting")},
		"harness.status": {runJSON("starting"), runJSON("ready")},
	}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund", "--dirty", "--force", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	up := f.calls[0]
	if up.method != "harness.up" || up.params["session"] != "s1" || up.params["scenario"] != "refund" ||
		up.params["dirty"] != true || up.params["force"] != true || up.params["wait"] != true {
		t.Fatalf("up call %+v", up)
	}
	if len(f.calls) != 3 || f.calls[2].params["run"] != "r1" {
		t.Fatalf("calls %+v", f.calls)
	}
	for _, want := range []string{"r1", "ready", "web", "41873", "a1b2c3d4e5f6", "Open {{web}}/orders/42"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestHarnessCmdUpFailedRunExitsNonZero(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.up": {runJSON("failed")}}}
	_, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund")
	if err == nil {
		t.Fatal("a failed run exited zero")
	}
}

func TestHarnessCmdSessionFlagAndJSON(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.status": {runJSON("ready")}}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "status", "--session", "other", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].params["session"] != "other" {
		t.Fatalf("params %v", f.calls[0].params)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["type"] != "harness_run" {
		t.Fatalf("--json output %q: %v", out, err)
	}
}

func TestHarnessCmdNeedsSessionOrRun(t *testing.T) {
	f := &fakeHarness{t: t}
	_, _, err := runHarnessTest(t, f, nil, "status")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want usage", err)
	}
	f = &fakeHarness{t: t, answers: map[string][]any{"harness.down": {runJSON("stopped")}}}
	if _, _, err := runHarnessTest(t, f, nil, "down", "--run", "r1"); err != nil {
		t.Fatal(err)
	}
	if p := f.calls[0].params; p["run"] != "r1" || p["session"] != nil {
		t.Fatalf("params %v", p)
	}
}

func TestHarnessCmdVerdict(t *testing.T) {
	ans := `{"type":"harness_verdict","verdict":{"run":"r1","step":2,"result":"fail","note":"n"},"todo":"t9"}`
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.verdict": {ans, ans}}}
	out, _, err := runHarnessTest(t, f, nil, "verdict", "2", "fail", "--note", "UI says Refunded", "--run", "r1")
	if err != nil {
		t.Fatal(err)
	}
	p := f.calls[0].params
	if p["step"] != 2 || p["result"] != "fail" || p["note"] != "UI says Refunded" || p["run"] != "r1" {
		t.Fatalf("params %v", p)
	}
	if !strings.Contains(out, "t9") {
		t.Fatalf("output lacks the to-do: %q", out)
	}
	if _, _, err := runHarnessTest(t, f, nil, "verdict", "all", "pass", "--run", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.calls[1].params["step"]; ok {
		t.Fatalf("all sent a step: %v", f.calls[1].params)
	}
	for _, bad := range [][]string{{"verdict", "0", "pass"}, {"verdict", "x", "pass"}, {"verdict", "1", "maybe"}, {"verdict", "1"}} {
		if _, _, err := runHarnessTest(t, &fakeHarness{t: t}, nil, append(bad, "--run", "r1")...); !errors.Is(err, errUsage) {
			t.Errorf("%v: err = %v, want usage", bad, err)
		}
	}
}

func TestHarnessCmdSurfacesRefusal(t *testing.T) {
	refusal := &harnessRPCError{Code: "needs_operator", Message: "only the operator records a harness verdict"}
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.verdict": {refusal}}}
	_, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "verdict", "1", "pass")
	if err == nil {
		t.Fatal("refusal exited zero")
	}
	if !strings.Contains(errOut, "needs_operator") || !strings.Contains(errOut, "only the operator") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestHarnessCmdRefusalDataPrinted(t *testing.T) {
	refusal := &harnessRPCError{Code: "harness_dirty", Message: "tree is dirty", Data: json.RawMessage(`{"paths":["a.go","b.go"]}`)}
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.up": {refusal}}}
	_, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund")
	if err == nil || !strings.Contains(errOut, "a.go") {
		t.Fatalf("err %v stderr %q", err, errOut)
	}
	f = &fakeHarness{t: t, answers: map[string][]any{"harness.up": {refusal}}}
	_, errOut, _ = runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund", "--json")
	var v struct {
		Error struct {
			Code string          `json:"code"`
			Data json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(errOut), &v); err != nil || v.Error.Code != "harness_dirty" || !strings.Contains(string(v.Error.Data), "b.go") {
		t.Fatalf("--json stderr %q: %v", errOut, err)
	}
}

func TestHarnessCmdCheckLogsListResult(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.check":  {`{"type":"harness_check","run":"r1","checks":[{"name":"refund row","run":"x","exit":0},{"name":"ledger","run":"y","exit":3,"output":"boom"}]}`},
		"harness.logs":   {`{"type":"harness_logs","run":"r1","service":"db","lines":["l1","l2"],"redacted":0}`},
		"harness.list":   {`{"type":"harness_list","session":"s1","source":"file","sha":"abc","scenarios":[{"id":"refund","title":"Refund","steps":[{"do":"a"}],"checks":[{"name":"c","run":"x"}]}],"runs":[]}`},
		"harness.result": {`{"type":"harness_result","run":{"id":"r1","scenario":"refund","state":"stopped","result":"fail","verdicts":[{"step":1,"result":"fail","note":"n"}],"steps":[],"checks":[],"ports":[]}}`},
	}}
	env := map[string]string{"AIMAN_ID": "s1"}
	out, _, err := runHarnessTest(t, f, env, "check", "ledger")
	if err == nil || !strings.Contains(out, "boom") || !strings.Contains(out, "exit 3") {
		t.Fatalf("check: err %v out %q", err, out)
	}
	if f.calls[0].params["name"] != "ledger" {
		t.Fatalf("check params %v", f.calls[0].params)
	}
	out, _, err = runHarnessTest(t, f, env, "logs", "db", "--lines", "50")
	if err != nil || out != "l1\nl2\n" || f.calls[1].params["lines"] != 50 || f.calls[1].params["service"] != "db" {
		t.Fatalf("logs: err %v out %q params %v", err, out, f.calls[1].params)
	}
	out, _, err = runHarnessTest(t, f, env, "list")
	if err != nil || !strings.Contains(out, "refund") || !strings.Contains(out, "Refund") {
		t.Fatalf("list: err %v out %q", err, out)
	}
	out, _, err = runHarnessTest(t, f, env, "result", "r1")
	if err != nil || !strings.Contains(out, "fail") || f.calls[3].params["run"] != "r1" {
		t.Fatalf("result: err %v out %q params %v", err, out, f.calls[3].params)
	}
}

func TestHarnessCmdUnknownSubcommand(t *testing.T) {
	if _, _, err := runHarnessTest(t, &fakeHarness{t: t}, nil, "nope"); !errors.Is(err, errUsage) {
		t.Fatalf("err = %v", err)
	}
}
