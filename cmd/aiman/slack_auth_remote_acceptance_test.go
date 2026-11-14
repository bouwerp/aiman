//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// Acceptance tests for `aiman slack auth login --remote <name>`: the laptop
// forwards the OAuth redirect port to the host over one ssh process, the host
// runs its own `aiman slack auth login`, and the laptop opens the consent URL
// the host prints. The token stays on the host. Do not edit.

const remoteConsentURL = "https://slack.com/oauth/v2/authorize?client_id=1234.5678&state=s1&redirect_uri=http%3A%2F%2F127.0.0.1%3A43127%2Fslack%2Fcallback"

// fakeSSH is an ssh on PATH. Each call gets a directory under $SSH_LOG
// (call-01, call-02, …) with its argv, one per line, in args. A remote command
// containing "slack auth app" stores stdin in stdin and prints {"ok":true}.
// One containing "slack auth login" acts on $SSH_LOGIN: ok prints the consent
// URL to stderr and the result to stdout, evil prints a foreign URL first,
// fail prints a host error to stderr and exits 2.
const fakeSSH = `#!/bin/sh
n=$(ls "$SSH_LOG" | wc -l)
d="$SSH_LOG/call-$(printf '%02d' $((n + 1)))"
mkdir -p "$d"
for a in "$@"; do printf '%s\n' "$a" >> "$d/args"; last="$a"; done
case "$last" in
*"slack auth app"*)
	cat > "$d/stdin"
	printf '{"ok":true}\n'
	;;
*"slack auth login"*)
	case "$SSH_LOGIN" in
	fail)
		printf '{"error":{"code":"slack_unconfigured","message":"slack client id is not set"}}\n' >&2
		exit 2
		;;
	evil)
		printf 'Open this URL and approve Aiman:\nhttps://evil.example/phish\n' >&2
		;;
	esac
	printf 'Open this URL and approve Aiman:\n%s\nCould not open a browser. Open the URL yourself.\n' "$SSH_URL" >&2
	printf '{\n  "ok": true,\n  "team": "realfi",\n  "user_id": "U9"\n}\n'
	;;
*)
	echo "unexpected remote command: $last" >&2
	exit 99
	;;
esac
`

type remoteRig struct {
	log    string
	tok    string
	listen string
	opened *[]string
}

// newRemoteRig writes a laptop config with remote regent0 (code@regent0.lan),
// puts the fake ssh first on PATH, points slack.ListenAddr at a free port and
// records every URL handed to the browser.
func newRemoteRig(t *testing.T, login string) remoteRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "remotes:\n  - name: regent0\n    host: regent0.lan\n    user: code\n    root: /home/code/repos\n"
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := remoteRig{log: t.TempDir(), tok: filepath.Join(home, "tok")}
	t.Setenv("SSH_LOG", r.log)
	t.Setenv("SSH_LOGIN", login)
	t.Setenv("SSH_URL", remoteConsentURL)
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", r.tok)
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.listen = ln.Addr().String()
	_ = ln.Close()
	prevListen, prevOpen := slack.ListenAddr, slack.BrowserOpen
	slack.ListenAddr = r.listen
	var mu sync.Mutex
	opened := []string{}
	r.opened = &opened
	slack.BrowserOpen = func(raw string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, raw)
		return nil
	}
	t.Cleanup(func() {
		slack.ListenAddr = prevListen
		slack.BrowserOpen = prevOpen
	})
	return r
}

type sshCall struct {
	args  []string
	stdin string
}

func (r remoteRig) calls(t *testing.T) []sshCall {
	t.Helper()
	entries, err := os.ReadDir(r.log)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	calls := []sshCall{}
	for _, n := range names {
		raw, _ := os.ReadFile(filepath.Join(r.log, n, "args"))
		in, _ := os.ReadFile(filepath.Join(r.log, n, "stdin"))
		calls = append(calls, sshCall{args: strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"), stdin: string(in)})
	}
	return calls
}

func runRemoteLogin(args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), append([]string{"auth", "login"}, args...), strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestSlackAuthLoginRemoteForwardsAndOpensTheHostsURL(t *testing.T) {
	r := newRemoteRig(t, "ok")
	out, errOut, err := runRemoteLogin("--remote", "regent0", "--timeout", "3m")
	if err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, errOut)
	}
	calls := r.calls(t)
	if len(calls) != 1 {
		t.Fatalf("ssh calls = %d, want 1: %+v", len(calls), calls)
	}
	args := calls[0].args
	if !hasPair(args, "-L", r.listen+":127.0.0.1:43127") {
		t.Fatalf("ssh argv %q lacks -L %s:127.0.0.1:43127", args, r.listen)
	}
	if !hasPair(args, "-o", "ExitOnForwardFailure=yes") {
		t.Fatalf("ssh argv %q lacks -o ExitOnForwardFailure=yes", args)
	}
	if !slices.Contains(args, "code@regent0.lan") {
		t.Fatalf("ssh argv %q does not target code@regent0.lan", args)
	}
	remote := args[len(args)-1]
	if !strings.Contains(remote, "aiman slack auth login") || !strings.Contains(remote, "--timeout 3m") {
		t.Fatalf("remote command = %q", remote)
	}
	if strings.Contains(remote, "--remote") {
		t.Fatalf("remote command must not recurse: %q", remote)
	}
	if got := *r.opened; len(got) != 1 || got[0] != remoteConsentURL {
		t.Fatalf("browser opened %q, want only the host's consent URL", got)
	}
	if !strings.Contains(errOut, remoteConsentURL) {
		t.Fatalf("stderr %q should show the consent URL", errOut)
	}
	var res map[string]any
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
		t.Fatalf("stdout %q is not JSON: %v", out, jerr)
	}
	if res["ok"] != true || res["user_id"] != "U9" || res["team"] != "realfi" {
		t.Fatalf("result %v", res)
	}
	if _, serr := os.Stat(r.tok); !os.IsNotExist(serr) {
		t.Fatalf("laptop token file exists (%v); the token belongs to the host", serr)
	}
}

func TestSlackAuthLoginRemoteSendsTheClientIDOnStdin(t *testing.T) {
	r := newRemoteRig(t, "ok")
	if _, errOut, err := runRemoteLogin("--remote", "regent0", "--client-id", "1234.5678"); err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, errOut)
	}
	calls := r.calls(t)
	if len(calls) != 2 {
		t.Fatalf("ssh calls = %d, want auth app then auth login", len(calls))
	}
	app, login := calls[0], calls[1]
	if !strings.Contains(app.args[len(app.args)-1], "aiman slack auth app") {
		t.Fatalf("first remote command = %q", app.args[len(app.args)-1])
	}
	if strings.TrimSpace(app.stdin) != "1234.5678" {
		t.Fatalf("auth app stdin = %q", app.stdin)
	}
	for _, c := range calls {
		for _, a := range c.args {
			if strings.Contains(a, "1234.5678") {
				t.Fatalf("client id on ssh argv: %q", c.args)
			}
		}
	}
	if !strings.Contains(login.args[len(login.args)-1], "aiman slack auth login") {
		t.Fatalf("second remote command = %q", login.args[len(login.args)-1])
	}
}

func TestSlackAuthLoginRemoteOpensOnlySlackURLs(t *testing.T) {
	r := newRemoteRig(t, "evil")
	if _, errOut, err := runRemoteLogin("--remote", "regent0"); err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, errOut)
	}
	for _, u := range *r.opened {
		if !strings.HasPrefix(u, "https://slack.com/oauth/v2/authorize?") {
			t.Fatalf("browser opened %q", u)
		}
	}
	if len(*r.opened) != 1 {
		t.Fatalf("browser opened %q, want the Slack URL once", *r.opened)
	}
}

func TestSlackAuthLoginRemoteHostFailure(t *testing.T) {
	r := newRemoteRig(t, "fail")
	_, errOut, err := runRemoteLogin("--remote", "regent0")
	if err == nil {
		t.Fatal("a failed host login must fail")
	}
	if !strings.Contains(errOut, "slack client id is not set") {
		t.Fatalf("stderr %q should carry the host's error", errOut)
	}
	if len(*r.opened) != 0 {
		t.Fatalf("browser opened %q", *r.opened)
	}
	if n := len(r.calls(t)); n != 1 {
		t.Fatalf("ssh ran %d times, want the one host login", n)
	}
}

func TestSlackAuthLoginRemoteUnknownRemote(t *testing.T) {
	r := newRemoteRig(t, "ok")
	_, errOut, err := runRemoteLogin("--remote", "nope")
	if err == nil || !strings.Contains(errOut, "slack_params") || !strings.Contains(errOut, "nope") {
		t.Fatalf("err %v stderr %q, want slack_params naming nope", err, errOut)
	}
	if n := len(r.calls(t)); n != 0 {
		t.Fatalf("ssh ran %d times", n)
	}
}

func TestSlackAuthLoginRemoteBusyPort(t *testing.T) {
	r := newRemoteRig(t, "ok")
	ln, err := net.Listen("tcp", r.listen)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, errOut, err := runRemoteLogin("--remote", "regent0")
	_, port, _ := net.SplitHostPort(r.listen)
	if err == nil || !strings.Contains(errOut, "slack_params") || !strings.Contains(errOut, port) {
		t.Fatalf("err %v stderr %q, want slack_params naming port %s", err, errOut, port)
	}
	if n := len(r.calls(t)); n != 0 {
		t.Fatalf("ssh ran %d times", n)
	}
}

// Without --remote, --client-id saves the id before the local login.
func TestSlackAuthLoginClientIDLocally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxp-local","scope":"`+strings.Join(slack.UserScopes, ",")+`"},"team":{"name":"Acme"}}`)
	}))
	defer srv.Close()
	prevBase, prevListen, prevRedirect, prevOpen := slack.APIBase, slack.ListenAddr, slack.RedirectURL, slack.BrowserOpen
	slack.APIBase = srv.URL + "/api/"
	slack.ListenAddr = "127.0.0.1:0"
	slack.RedirectURL = ""
	slack.BrowserOpen = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		if u.Query().Get("client_id") != "1234.5678" {
			t.Errorf("authorize URL client_id = %q", u.Query().Get("client_id"))
		}
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=c1&state=" + url.QueryEscape(u.Query().Get("state")))
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	t.Cleanup(func() {
		slack.APIBase, slack.ListenAddr, slack.RedirectURL, slack.BrowserOpen = prevBase, prevListen, prevRedirect, prevOpen
	})
	tok := filepath.Join(t.TempDir(), "tok")
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", tok)
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	if _, errOut, err := runRemoteLogin("--client-id", "1234.5678", "--timeout", "5s"); err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, errOut)
	}
	raw, err := os.ReadFile(tok)
	if err != nil || !strings.Contains(string(raw), "1234.5678") || !strings.Contains(string(raw), "xoxp-local") {
		t.Fatalf("token file %v: missing client id or token", err)
	}
}
