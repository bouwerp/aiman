//go:build acceptance

package apicompat_test

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/apicompat"
)

// baseSpec mirrors the shape of api/openapi.yaml: methods are the enum of
// RpcRequest.method and StreamMethod, event types the enum of
// SessionEvent.type, error codes the enum of RpcError.code. RpcParams and
// PairRequest are request schemas.
const baseSpec = `openapi: 3.1.0
info:
  title: aiman gateway
  version: 0.3.0
paths:
  /v1/rpc:
    post:
      operationId: rpc
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/RpcRequest"
      responses:
        "200":
          description: ok
components:
  schemas:
    RpcError:
      type: object
      required: [code, message]
      properties:
        code:
          type: string
          enum:
            - invalid_params
            - needs_operator
            - session_scope
        message:
          type: string
    RpcRequest:
      type: object
      required: [method]
      properties:
        id:
          type: string
        method:
          type: string
          enum:
            - ping
            - session.list
            - host.stats
        params:
          $ref: "#/components/schemas/RpcParams"
    StreamMethod:
      type: string
      enum:
        - pty.attach
        - session.events
    RpcParams:
      type: object
      additionalProperties: true
      properties:
        id:
          type: string
        session:
          type: string
        force:
          type: boolean
    PairRequest:
      type: object
      required: [code]
      properties:
        code:
          type: string
        name:
          type: string
    SessionEvent:
      type: object
      required: [type]
      properties:
        type:
          type: string
          enum:
            - session_activity
            - todo_changed
            - notify
    HostStatsResult:
      type: object
      required: [type]
      properties:
        type:
          type: string
        cpu_percent:
          type: number
        memory_bytes:
          type: integer
`

// variant applies one textual edit to baseSpec and fails when it does not apply.
func variant(t *testing.T, old, new string) []byte {
	t.Helper()
	if !strings.Contains(baseSpec, old) {
		t.Fatalf("fixture edit does not apply: %q", old)
	}
	return []byte(strings.Replace(baseSpec, old, new, 1))
}

func TestP3CompareSpecsIdenticalIsCompatible(t *testing.T) {
	if got := apicompat.CompareSpecs([]byte(baseSpec), []byte(baseSpec)); len(got) != 0 {
		t.Fatalf("identical specs gave problems: %q", got)
	}
}

func TestP3CompareSpecsFindsBreaks(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		mention  string
	}{
		{"removed method", "            - host.stats\n", "", "host.stats"},
		{"removed stream method", "        - session.events\n", "", "session.events"},
		{"removed event type", "            - todo_changed\n", "", "todo_changed"},
		{"removed error code", "            - needs_operator\n", "", "needs_operator"},
		{
			"removed schema",
			"    HostStatsResult:\n      type: object\n      required: [type]\n      properties:\n        type:\n          type: string\n        cpu_percent:\n          type: number\n        memory_bytes:\n          type: integer\n",
			"",
			"HostStatsResult",
		},
		{"removed result property", "        cpu_percent:\n          type: number\n", "", "cpu_percent"},
		{"removed request property", "        force:\n          type: boolean\n", "", "force"},
		{
			"request property newly required",
			"    RpcParams:\n      type: object\n",
			"    RpcParams:\n      type: object\n      required: [session]\n",
			"session",
		},
		{
			"existing request property joins required",
			"      required: [code]\n      properties:\n        code:\n          type: string\n        name:",
			"      required: [code, name]\n      properties:\n        code:\n          type: string\n        name:",
			"name",
		},
		{
			"new required request property",
			"        force:\n          type: boolean\n",
			"        force:\n          type: boolean\n        nonce:\n          type: string\n      required: [nonce]\n",
			"nonce",
		},
		{"info.version backwards", "  version: 0.3.0\n", "  version: 0.2.9\n", "version"},
		{"result property type changed", "        cpu_percent:\n          type: number\n", "        cpu_percent:\n          type: string\n", "cpu_percent"},
		{"request property type changed", "        force:\n          type: boolean\n", "        force:\n          type: string\n", "force"},
		{
			"$ref retargeted",
			"        params:\n          $ref: \"#/components/schemas/RpcParams\"\n",
			"        params:\n          $ref: \"#/components/schemas/PairRequest\"\n",
			"params",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := apicompat.CompareSpecs([]byte(baseSpec), variant(t, c.old, c.new))
			if len(got) == 0 {
				t.Fatal("no problem reported")
			}
			if joined := strings.Join(got, "\n"); !strings.Contains(joined, c.mention) {
				t.Fatalf("problems do not mention %q:\n%s", c.mention, joined)
			}
		})
	}
}

func TestP3CompareSpecsAllowsAdditions(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
	}{
		{"added method", "            - host.stats\n", "            - host.stats\n            - host.version\n            - host.update\n"},
		{"added stream method", "        - session.events\n", "        - session.events\n        - session.tail\n"},
		{"added event type", "            - notify\n", "            - notify\n            - transcript\n"},
		{"added error code", "            - session_scope\n", "            - session_scope\n            - host_busy\n"},
		{"added optional request property", "        force:\n          type: boolean\n", "        force:\n          type: boolean\n        discard:\n          type: boolean\n"},
		{"added result property", "        memory_bytes:\n          type: integer\n", "        memory_bytes:\n          type: integer\n        load:\n          type: number\n"},
		{
			// A new result schema may list required fields of its own.
			"added result schema",
			"    HostStatsResult:\n",
			"    HostVersionResult:\n      type: object\n      required: [type, version, protocol]\n      properties:\n        type:\n          type: string\n        version:\n          type: string\n        protocol:\n          type: integer\n    HostStatsResult:\n",
		},
		{"info.version forwards", "  version: 0.3.0\n", "  version: 0.4.0\n"},
		{"description edit", "  title: aiman gateway\n", "  title: aiman gateway\n  description: more words\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := apicompat.CompareSpecs([]byte(baseSpec), variant(t, c.old, c.new)); len(got) != 0 {
				t.Fatalf("addition reported as a problem: %q", got)
			}
		})
	}
}

func TestP3CompareSpecsRealSpecAgainstItself(t *testing.T) {
	raw, err := exec.Command("git", "-C", "../..", "show", "HEAD:api/openapi.yaml").Output()
	if err != nil {
		t.Skipf("cannot read api/openapi.yaml at HEAD: %v", err)
	}
	if got := apicompat.CompareSpecs(raw, raw); len(got) != 0 {
		t.Fatalf("the real spec is incompatible with itself: %q", got)
	}
}

// allowlistSrc mirrors internal/gateway/allowlist.go: three []string sets in
// one var block, plus gatewayMethods, which is not a device set.
func allowlistSrc(read, write, destructive []string) []byte {
	q := func(list []string) string {
		parts := make([]string, len(list))
		for i, m := range list {
			parts[i] = `"` + m + `"`
		}
		return strings.Join(parts, ", ")
	}
	return []byte(`package gateway

var (
	// deviceReadMethods only read state.
	deviceReadMethods = []string{
		` + q(read) + `,
	}
	// deviceWriteMethods change state.
	deviceWriteMethods = []string{
		` + q(write) + `,
	}
	// deviceDestructiveMethods need allow_destructive.
	deviceDestructiveMethods = []string{
		` + q(destructive) + `,
	}
	// gatewayMethods are answered by the gateway itself, not serve.
	gatewayMethods = []string{"push.register", "push.unregister"}
)

var deviceAllowed = toSet(deviceReadMethods, deviceWriteMethods)

func toSet(lists ...[]string) map[string]bool { return nil }
`)
}

var (
	baseRead        = []string{"ping", "session.list", "host.stats", "aws.status"}
	baseWrite       = []string{"session.prompt", "todo.add", "push.register"}
	baseDestructive = []string{"pty.input", "session.create", "aws.login_start"}
)

func without(list []string, drop string) []string {
	var out []string
	for _, m := range list {
		if m != drop {
			out = append(out, m)
		}
	}
	return out
}

func with(list []string, add string) []string {
	return append(append([]string(nil), list...), add)
}

func TestP3CompareAllowlists(t *testing.T) {
	old := allowlistSrc(baseRead, baseWrite, baseDestructive)

	if got := apicompat.CompareAllowlists(old, old); len(got) != 0 {
		t.Fatalf("identical allowlists gave problems: %q", got)
	}

	breaks := []struct {
		name    string
		new     []byte
		mention string
	}{
		{"read method dropped", allowlistSrc(without(baseRead, "host.stats"), baseWrite, baseDestructive), "host.stats"},
		{"write method dropped", allowlistSrc(baseRead, without(baseWrite, "todo.add"), baseDestructive), "todo.add"},
		{"destructive method dropped", allowlistSrc(baseRead, baseWrite, without(baseDestructive, "aws.login_start")), "aws.login_start"},
		{"read method moved to destructive", allowlistSrc(without(baseRead, "aws.status"), baseWrite, with(baseDestructive, "aws.status")), "aws.status"},
		{"write method moved to destructive", allowlistSrc(baseRead, without(baseWrite, "session.prompt"), with(baseDestructive, "session.prompt")), "session.prompt"},
	}
	for _, c := range breaks {
		t.Run(c.name, func(t *testing.T) {
			got := apicompat.CompareAllowlists(old, c.new)
			if len(got) == 0 {
				t.Fatal("no problem reported")
			}
			if joined := strings.Join(got, "\n"); !strings.Contains(joined, c.mention) {
				t.Fatalf("problems do not mention %q:\n%s", c.mention, joined)
			}
		})
	}

	fine := []struct {
		name string
		new  []byte
	}{
		{"read method added", allowlistSrc(with(baseRead, "host.version"), baseWrite, baseDestructive)},
		{"write method added", allowlistSrc(baseRead, with(baseWrite, "session.rename"), baseDestructive)},
		{"destructive method added", allowlistSrc(baseRead, baseWrite, with(baseDestructive, "host.update"))},
	}
	for _, c := range fine {
		t.Run(c.name, func(t *testing.T) {
			if got := apicompat.CompareAllowlists(old, c.new); len(got) != 0 {
				t.Fatalf("addition reported as a problem: %q", got)
			}
		})
	}
}

func TestP3CompareAllowlistsRealFileAgainstItself(t *testing.T) {
	raw, err := exec.Command("git", "-C", "../..", "show", "HEAD:internal/gateway/allowlist.go").Output()
	if err != nil {
		t.Skipf("cannot read allowlist.go at HEAD: %v", err)
	}
	if got := apicompat.CompareAllowlists(raw, raw); len(got) != 0 {
		t.Fatalf("the real allowlist is incompatible with itself: %q", got)
	}
}

const holderOld = `package ptyhold

type Spec struct {
	ID      string            ` + "`json:\"id\"`" + `
	Name    string            ` + "`json:\"name,omitempty\"`" + `
	Cols    int               ` + "`json:\"cols\"`" + `
	Env     map[string]string ` + "`json:\"env,omitempty\"`" + `
	private string
}

type Meta struct {
	ID       string ` + "`json:\"id\"`" + `
	ParentID string ` + "`json:\"parent_id,omitempty\"`" + `
}

type Other struct {
	Gone string ` + "`json:\"gone\"`" + `
}
`

var holderTypes = []string{"Spec", "Meta"}

func editHolder(t *testing.T, old, new string) []byte {
	t.Helper()
	if !strings.Contains(holderOld, old) {
		t.Fatalf("fixture edit does not apply: %q", old)
	}
	return []byte(strings.Replace(holderOld, old, new, 1))
}

func TestP3CompareJSONNames(t *testing.T) {
	if got := apicompat.CompareJSONNames([]byte(holderOld), []byte(holderOld), holderTypes); len(got) != 0 {
		t.Fatalf("identical sources gave problems: %q", got)
	}

	breaks := []struct {
		name     string
		old, new string
		mention  string
	}{
		{"field removed", "\tName    string            `json:\"name,omitempty\"`\n", "", "name"},
		{"json name renamed", "`json:\"parent_id,omitempty\"`", "`json:\"parent,omitempty\"`", "parent_id"},
		{"type changed", "\tCols    int               `json:\"cols\"`", "\tCols    string            `json:\"cols\"`", "cols"},
		{"map type changed", "\tEnv     map[string]string `json:\"env,omitempty\"`", "\tEnv     []string          `json:\"env,omitempty\"`", "env"},
	}
	for _, c := range breaks {
		t.Run(c.name, func(t *testing.T) {
			got := apicompat.CompareJSONNames([]byte(holderOld), editHolder(t, c.old, c.new), holderTypes)
			if len(got) == 0 {
				t.Fatal("no problem reported")
			}
			if joined := strings.Join(got, "\n"); !strings.Contains(joined, c.mention) {
				t.Fatalf("problems do not mention %q:\n%s", c.mention, joined)
			}
		})
	}

	fine := []struct {
		name     string
		old, new string
	}{
		{"field added", "\tprivate string\n", "\tprivate string\n\tRole    string `json:\"role,omitempty\"`\n"},
		{"go field renamed, json name kept", "\tParentID string `json:\"parent_id,omitempty\"`", "\tParent string `json:\"parent_id,omitempty\"`"},
		{"unlisted struct changed", "\tGone string `json:\"gone\"`\n", ""},
		{"untagged unexported field removed", "\tprivate string\n", ""},
	}
	for _, c := range fine {
		t.Run(c.name, func(t *testing.T) {
			if got := apicompat.CompareJSONNames([]byte(holderOld), editHolder(t, c.old, c.new), holderTypes); len(got) != 0 {
				t.Fatalf("compatible change reported as a problem: %q", got)
			}
		})
	}
}

func TestP3CompareJSONNamesMissingStructIsAProblem(t *testing.T) {
	newSrc := editHolder(t, "type Meta struct {", "type MetaV2 struct {")
	got := apicompat.CompareJSONNames([]byte(holderOld), newSrc, holderTypes)
	if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), "Meta") {
		t.Fatalf("a dropped struct must be reported, got %q", got)
	}
}

func hasTags(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", "../..", "tag", "--list", "v*").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", "../.."}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestP3PreviousTagAndShowAtTag(t *testing.T) {
	if !hasTags(t) {
		if os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatal("no release tags in CI; check out with fetch-depth: 0")
		}
		t.Skip("this checkout has no release tags (fetch with tags to run this test)")
	}
	tag, ok := apicompat.PreviousTag("../..")
	if !ok {
		t.Fatal("PreviousTag found no tag in a checkout that has tags")
	}
	release := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	if !release.MatchString(tag) {
		t.Fatalf("PreviousTag = %q, want vX.Y.Z", tag)
	}

	// Never HEAD's own tag.
	atHead := strings.Fields(git(t, "tag", "--points-at", "HEAD"))
	if slices.Contains(atHead, tag) {
		t.Fatalf("PreviousTag = %q, which points at HEAD", tag)
	}
	// The newest release tag by version order that is not at HEAD.
	var want string
	for _, c := range strings.Fields(git(t, "tag", "--list", "v*.*.*", "--sort=-v:refname")) {
		if release.MatchString(c) && !slices.Contains(atHead, c) {
			want = c
			break
		}
	}
	if tag != want {
		t.Fatalf("PreviousTag = %q, want %q (newest release tag not at HEAD)", tag, want)
	}

	spec, err := apicompat.ShowAtTag("../..", tag, "api/openapi.yaml")
	if err != nil {
		t.Fatalf("ShowAtTag(%s, api/openapi.yaml): %v", tag, err)
	}
	if len(spec) == 0 || !strings.HasPrefix(string(spec), "openapi:") {
		t.Fatalf("ShowAtTag returned %d bytes not starting with openapi:", len(spec))
	}

	if _, err := apicompat.ShowAtTag("../..", tag, "no/such/file.yaml"); err == nil {
		t.Fatal("ShowAtTag of a missing path returned no error")
	}
}

// TestP3PreviousTagSkipsHeadTag builds a repository whose HEAD carries the
// newest tag and checks PreviousTag returns the release before it, picked by
// version order rather than by name or date.
func TestP3PreviousTagSkipsHeadTag(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	commit := func(msg string) { run("commit", "-q", "--allow-empty", "-m", msg) }
	commit("one")
	run("tag", "v0.9.0")
	commit("two")
	run("tag", "v0.10.0")
	run("tag", "v0.10.0-rc1")
	commit("three")
	run("tag", "v0.11.0")

	tag, ok := apicompat.PreviousTag(dir)
	if !ok || tag != "v0.10.0" {
		t.Fatalf("PreviousTag = %q, %v; want v0.10.0 (HEAD's v0.11.0 skipped, v0.10.0 > v0.9.0)", tag, ok)
	}

	commit("four")
	tag, ok = apicompat.PreviousTag(dir)
	if !ok || tag != "v0.11.0" {
		t.Fatalf("untagged HEAD: PreviousTag = %q, %v; want v0.11.0", tag, ok)
	}
}

func TestP3PreviousTagOutsideRepo(t *testing.T) {
	if _, ok := apicompat.PreviousTag(t.TempDir()); ok {
		t.Fatal("PreviousTag reported a tag outside a git repository")
	}
}
