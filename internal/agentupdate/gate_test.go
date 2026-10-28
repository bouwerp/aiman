package agentupdate

import "testing"

func TestGate(t *testing.T) {
	sessions := []LiveSession{
		{Agent: "claude", HookState: "working"},
		{Agent: "claude", HookState: "working"},
		{Agent: "claude", HookState: "idle"},
		{Agent: "codex", HookState: "waiting_input"},
		{Agent: "deepcode", HookState: "idle"},
	}
	cases := []struct {
		name     string
		agent    string
		method   Method
		sessions []LiveSession
		ok       bool
		reason   string
	}{
		{"swap safe busy", "claude", MethodNative, sessions, false, "deferred: 2 sessions busy"},
		{"swap safe one busy", "claude", MethodNative, sessions[1:], false, "deferred: 1 session busy"},
		{"swap safe waiting", "codex", MethodStandalone, sessions, true, ""},
		{"swap safe none", "grok", MethodManaged, sessions, true, ""},
		{"in place running", "deepcode", MethodNPM, sessions, false, "deferred: 1 session running"},
		{"in place none", "kilo", MethodNPM, sessions, true, ""},
		{"in place counts all states", "claude", MethodNPM, sessions, false, "deferred: 3 sessions running"},
		{"unknown method is in place", "claude", MethodUnknown, sessions[2:], false, "deferred: 1 session running"},
		{"no sessions", "claude", MethodNative, nil, true, ""},
	}
	for _, c := range cases {
		ok, reason := Gate(c.agent, c.method, c.sessions)
		if ok != c.ok || reason != c.reason {
			t.Errorf("%s: got (%v, %q) want (%v, %q)", c.name, ok, reason, c.ok, c.reason)
		}
	}
}
