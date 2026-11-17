package main

import (
	"strings"
	"testing"
)

const harnessDebugJSON = `{"type":"harness_debug","debug":{"run":"r1","holder":{"client_name":"Visual Studio Code","remote":"172.20.0.1:5000","since":"2026-11-17T12:00:00Z","connections":1},"launch":{"lang":"go","adapter":"dlv dap","arguments":{"program":"/out/app"},"source_root":"/src"},"host_port":41900,"refused":2}}`

func TestHarnessCmdDebug(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.debug": {harnessDebugJSON}}}
	out, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "debug")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Visual Studio Code", "41900", "dlv dap", "refused 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if f.calls[0].params["op"] != "status" || f.calls[0].params["session"] != "s1" {
		t.Fatalf("params %v", f.calls[0].params)
	}

	f = &fakeHarness{t: t, answers: map[string][]any{"harness.debug": {harnessDebugJSON}}}
	if _, _, err := runHarnessTest(t, f, nil, "debug", "take", "--run", "r1"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].params["op"] != "take" || f.calls[0].params["run"] != "r1" {
		t.Fatalf("params %v", f.calls[0].params)
	}
	if _, _, err := runHarnessTest(t, &fakeHarness{t: t}, nil, "debug", "pause", "--run", "r1"); err == nil {
		t.Fatal("an unknown op was accepted")
	}

	f = &fakeHarness{t: t, answers: map[string][]any{"harness.up": {runJSON("starting")}}}
	if _, _, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund", "--debug"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].params["debug"] != true {
		t.Fatalf("up params %v", f.calls[0].params)
	}
}
