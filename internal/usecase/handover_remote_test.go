package usecase

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestHandoverPromptText(t *testing.T) {
	const want = "Read .aiman_handover.md — it is the handover from the previous agent: the to-do list, notes you have not seen, changed files and the last screen. Continue from it."
	if HandoverPrompt != want {
		t.Fatalf("HandoverPrompt = %q", HandoverPrompt)
	}
}

func TestWriteHandoverOnRemoteDecodesAndDoesNotDelete(t *testing.T) {
	remote := &restartRemote{rpcOut: map[string]string{
		"handover.write": `{"result":{"type":"handover","path":"/w/.aiman_handover.md","written":true,"exists":true}}`,
	}}

	got, err := WriteHandoverOnRemote(context.Background(), remote, "s1", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/w/.aiman_handover.md" || !got.Written || !got.Exists {
		t.Fatalf("result = %+v", got)
	}
	joined := strings.Join(remote.commands, "\n")
	if strings.Contains(joined, "rm -f ") {
		t.Fatalf("WriteHandoverOnRemote deleted files:\n%s", joined)
	}
	params := decodeRPCParams(t, commandContaining(t, remote.commands, "aiman rpc handover.write"))
	if params["session"] != "s1" || params["reason"] != "restart" {
		t.Fatalf("params = %#v", params)
	}
}

func TestPrepareRestartHandoffUsesServeHandover(t *testing.T) {
	remote := &restartRemote{rpcOut: map[string]string{
		"handover.write": `{"result":{"type":"handover","path":"/w/.aiman_handover.md","written":true,"exists":true}}`,
	}}
	s := domain.Session{ID: "s1", TmuxSession: "sess", WorkingDirectory: "/w"}

	mode, note := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", time.Second)
	if mode != HandoffHandover {
		t.Fatalf("mode = %v, note %q", mode, note)
	}
	if note != "" {
		t.Fatalf("note = %q", note)
	}
	joined := strings.Join(remote.commands, "\n")
	if remote.summaryPrompted() {
		t.Fatalf("summary prompt was sent:\n%s", joined)
	}
	rmAt := commandIndex(remote.commands, "rm -f ")
	writeAt := commandIndex(remote.commands, "aiman rpc handover.write")
	if writeAt < 0 || rmAt < 0 || writeAt > rmAt {
		t.Fatalf("summary removed before the handover was accepted:\n%s", joined)
	}
	rm := remote.commands[rmAt]
	for _, path := range []string{
		`"/w/.aiman_session_summary.md"`,
		`"/w/.aiman_session_summary.md.tmp"`,
	} {
		if !strings.Contains(rm, path) {
			t.Fatalf("rm command %q missing %s", rm, path)
		}
	}

	t.Run("exists without a rewrite", func(t *testing.T) {
		remote := &restartRemote{rpcOut: map[string]string{
			"handover.write": `{"result":{"path":"/w/.aiman_handover.md","written":false,"exists":true}}`,
		}}
		mode, _ := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", time.Second)
		if mode != HandoffHandover {
			t.Fatalf("mode = %v", mode)
		}
		joined := strings.Join(remote.commands, "\n")
		if remote.summaryPrompted() {
			t.Fatal("summary prompt was sent for an existing handover")
		}
		if !strings.Contains(joined, `"/w/.aiman_session_summary.md"`) || !strings.Contains(joined, `"/w/.aiman_session_summary.md.tmp"`) {
			t.Fatalf("stale summary was not removed:\n%s", joined)
		}
	})
}

func TestPrepareRestartHandoffFallsBackOnUnknownMethod(t *testing.T) {
	withFastSummaryPoll(t)
	s := domain.Session{ID: "s1", TmuxSession: "sess", WorkingDirectory: "/w"}

	t.Run("file appears", func(t *testing.T) {
		remote := &restartRemote{
			pane:                 "> working",
			sendPromptWritesFile: true,
			rpcOut:               map[string]string{"handover.write": "unknown method"},
		}
		mode, note := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", time.Second)
		if mode != HandoffSummary {
			t.Fatalf("mode = %v, note %q", mode, note)
		}
		if note != "" {
			t.Fatalf("note = %q", note)
		}
		if !remote.summaryPrompted() {
			t.Fatalf("summary flow did not run:\n%s", strings.Join(remote.commands, "\n"))
		}
	})

	t.Run("file never appears", func(t *testing.T) {
		remote := &restartRemote{
			pane:   "> working",
			rpcOut: map[string]string{"handover.write": "unknown method"},
		}
		mode, note := prepareHandoffWithin(t, remote, s, 20*time.Millisecond)
		if mode != HandoffNone {
			t.Fatalf("mode = %v, note %q", mode, note)
		}
		if note != "" {
			t.Fatalf("note = %q", note)
		}
		if !remote.summaryPrompted() {
			t.Fatalf("summary flow did not run:\n%s", strings.Join(remote.commands, "\n"))
		}
	})

	t.Run("capture error is the note", func(t *testing.T) {
		remote := &restartRemote{
			pane:   "> working",
			failOn: "aiman pty input",
			rpcOut: map[string]string{"handover.write": "unknown method"},
		}
		mode, note := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", time.Second)
		if mode != HandoffNone {
			t.Fatalf("mode = %v", mode)
		}
		if !strings.Contains(note, "restart handoff not captured") {
			t.Fatalf("note = %q", note)
		}
	})
}

func TestPrepareRestartHandoffFallsBackOnOtherFailures(t *testing.T) {
	withFastSummaryPoll(t)
	s := domain.Session{ID: "s1", TmuxSession: "sess", WorkingDirectory: "/w"}
	for _, tt := range []struct {
		name string
		out  string
	}{
		{"serve not running", `{"error":{"code":"server_not_running"}}`},
		{"rpc error", `{"error":{"code":"not_found","message":"missing"}}`},
		{"exists false", `{"result":{"type":"handover","path":"/w/.aiman_handover.md","written":true,"exists":false}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := &restartRemote{
				pane:                 "> working",
				sendPromptWritesFile: true,
				rpcOut:               map[string]string{"handover.write": tt.out},
			}
			mode, note := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", time.Second)
			if mode != HandoffSummary {
				t.Fatalf("mode = %v, note %q", mode, note)
			}
			if !remote.summaryPrompted() {
				t.Fatal("summary flow did not run")
			}
		})
	}
}

func TestPrepareRestartHandoffPassesReason(t *testing.T) {
	remote := &restartRemote{rpcOut: map[string]string{
		"handover.write": `{"result":{"path":"/w/.aiman_handover.md","written":true,"exists":true}}`,
	}}
	s := domain.Session{ID: "s1", TmuxSession: "sess"}

	mode, _ := PrepareRestartHandoff(context.Background(), remote, s, "/w", "agent_switch", time.Second)
	if mode != HandoffHandover {
		t.Fatalf("mode = %v", mode)
	}
	params := decodeRPCParams(t, commandContaining(t, remote.commands, "aiman rpc handover.write"))
	if params["reason"] != "agent_switch" || params["session"] != "s1" {
		t.Fatalf("params = %#v", params)
	}
}

func TestReviveIfNeededPointsAtHandover(t *testing.T) {
	remote := &restartRemote{rpcOut: map[string]string{
		"handover.point": "unknown method",
	}}
	s := domain.Session{
		ID: "dead", Backend: domain.BackendPTY, AgentName: "claude",
		AgentSessionID: "conv-42", WorkingDirectory: "/w", TmuxSession: "feat",
	}

	revived, err := ReviveIfNeeded(context.Background(), remote, &s)
	if err != nil || !revived {
		t.Fatalf("revived=%v err=%v", revived, err)
	}
	createAt := commandIndex(remote.commands, "aiman pty create")
	pointAt := commandIndex(remote.commands, "aiman rpc handover.point")
	if createAt < 0 || pointAt < 0 || pointAt < createAt {
		t.Fatalf("pointer not after pty create: create=%d point=%d\n%s", createAt, pointAt, strings.Join(remote.commands, "\n"))
	}
	params := decodeRPCParams(t, remote.commands[pointAt])
	if len(params) != 1 || params["session"] != "dead" {
		t.Fatalf("params = %#v", params)
	}
}

func TestReviveIfNeededNoRevivalNoPointer(t *testing.T) {
	t.Run("live pty", func(t *testing.T) {
		remote := &restartRemote{ptyRunning: true}
		s := domain.Session{ID: "pid", Backend: domain.BackendPTY, AgentName: "claude", AgentSessionID: "conv-42"}
		revived, err := ReviveIfNeeded(context.Background(), remote, &s)
		if err != nil || revived {
			t.Fatalf("revived=%v err=%v", revived, err)
		}
		if commandIndex(remote.commands, "aiman rpc handover.point") >= 0 {
			t.Fatalf("pointer ran for a live pty:\n%s", strings.Join(remote.commands, "\n"))
		}
	})

	t.Run("failed revive", func(t *testing.T) {
		remote := &restartRemote{}
		s := domain.Session{ID: "lost", Backend: domain.BackendPTY, AgentName: "claude"}
		if _, err := ReviveIfNeeded(context.Background(), remote, &s); err == nil {
			t.Fatal("revival without an agent session id must fail")
		}
		if commandIndex(remote.commands, "aiman rpc handover.point") >= 0 || commandIndex(remote.commands, "aiman pty create") >= 0 {
			t.Fatalf("failed revive still touched the runtime:\n%s", strings.Join(remote.commands, "\n"))
		}
	})
}

func withFastSummaryPoll(t *testing.T) {
	t.Helper()
	old := restartSummaryPollInterval
	restartSummaryPollInterval = time.Millisecond
	t.Cleanup(func() { restartSummaryPollInterval = old })
}

func prepareHandoffWithin(t *testing.T, remote *restartRemote, s domain.Session, summaryTimeout time.Duration) (HandoffMode, string) {
	t.Helper()
	type handoff struct {
		mode HandoffMode
		note string
	}
	ch := make(chan handoff, 1)
	go func() {
		mode, note := PrepareRestartHandoff(context.Background(), remote, s, "/w", "restart", summaryTimeout)
		ch <- handoff{mode, note}
	}()
	select {
	case got := <-ch:
		return got.mode, got.note
	case <-time.After(2 * time.Second):
		t.Fatal("PrepareRestartHandoff ignored summaryTimeout")
		return HandoffNone, ""
	}
}

func commandContaining(t *testing.T, cmds []string, sub string) string {
	t.Helper()
	i := commandIndex(cmds, sub)
	if i < 0 {
		t.Fatalf("no command containing %q in:\n%s", sub, strings.Join(cmds, "\n"))
	}
	return cmds[i]
}

func commandIndex(cmds []string, sub string) int {
	for i, cmd := range cmds {
		if strings.Contains(cmd, sub) {
			return i
		}
	}
	return -1
}

func decodeRPCParams(t *testing.T, cmd string) map[string]any {
	t.Helper()
	m := regexp.MustCompile(`printf %s '([^']*)'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("no base64 params in %q", cmd)
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("params %s: %v", raw, err)
	}
	return params
}
