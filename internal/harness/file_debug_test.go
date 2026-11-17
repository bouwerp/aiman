package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func parseErrs(t *testing.T, src string) []string {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		return nil
	}
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("err = %v, want harness_invalid", err)
	}
	return he.Errors
}

func TestParseDebugSpec(t *testing.T) {
	src := `version: 1
app:
  image: oven/bun:1.3-slim
  run: cd api && node dist/index.js
  debug:
    lang: node
    entry: api/dist/index.js
    cwd: api
    runtime: node
    runtime_args: ["--enable-source-maps", "--import", "tsx"]
scenarios:
  - id: s
    checks:
      - name: unit
        run: npx vitest run
        debug:
          lang: node
          run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism
      - name: snowflake
        run: npx vitest run tests/snowflake
        debug: {lang: python, wrap_env: [PYTHON_BIN]}
`
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.App.Image != "oven/bun:1.3-slim" {
		t.Fatalf("app.image = %q", f.App.Image)
	}
	d := f.App.Debug
	if d.Lang != "node" || d.Entry != "api/dist/index.js" || d.Cwd != "api" || d.Runtime != "node" ||
		!slices.Equal(d.RuntimeArgs, []string{"--enable-source-maps", "--import", "tsx"}) {
		t.Fatalf("app.debug = %+v", d)
	}
	cd := f.Scenarios[0].Checks[0].Debug
	if cd == nil || cd.Lang != "node" || !strings.Contains(cd.Run, "$AIMAN_INSPECT") {
		t.Fatalf("checks[0].debug = %+v", cd)
	}
	if w := f.Scenarios[0].Checks[1].Debug; w == nil || !slices.Equal(w.WrapEnv, []string{"PYTHON_BIN"}) {
		t.Fatalf("checks[1].debug = %+v", w)
	}
	// Strict parsing reaches the new blocks too.
	if errs := parseErrs(t, strings.Replace(src, "cwd: api", "cwd: api\n    bogus: 1", 1)); errs == nil {
		t.Fatal("an unknown key under app.debug parsed")
	}
}

func TestParseDebugSpecRefusals(t *testing.T) {
	base := "version: 1\napp:\n  run: node dist/a.js\n%s\nscenarios:\n  - id: s\n"
	cases := []struct{ name, app, want string }{
		{"node needs entry or run", "  debug: {lang: node}", "app.debug: node needs entry or run"},
		{"absolute entry", "  debug: {lang: node, entry: /src/a.js}", "app.debug.entry"},
		{"entry escapes", "  debug: {lang: node, entry: ../a.js}", "app.debug.entry"},
		{"absolute cwd", "  debug: {lang: node, entry: a.js, cwd: /x}", "app.debug.cwd"},
		{"tsx runtime", "  debug: {lang: node, entry: a.ts, runtime: node_modules/.bin/tsx}", "runtime_args"},
		{"runtime shell", "  debug: {lang: node, entry: a.js, runtime: 'node;rm'}", "app.debug.runtime"},
		{"run without inspect", "  debug: {lang: node, run: node --inspect a.js}", "$AIMAN_INSPECT"},
		{"run inspect twice", "  debug: {lang: node, run: node --inspect=$AIMAN_INSPECT --x=$AIMAN_INSPECT a.js}", "once"},
		{"run and entry", "  debug: {lang: node, entry: a.js, run: node --inspect=$AIMAN_INSPECT a.js}", "not both"},
		{"program for node", "  debug: {lang: node, entry: a.js, program: /out/app}", "app.debug.program"},
		{"entry for go", "  debug: {lang: go, entry: main.go}", "app.debug.entry"},
		{"wrap_env for node", "  debug: {lang: node, entry: a.js, wrap_env: [X]}", "wrap_env"},
		{"wrap_env name", "  debug: {lang: python, wrap_env: ['A B']}", "wrap_env"},
		{"bad app image", "  image: 'a b'", "app.image"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := parseErrs(t, strings.Replace(base, "%s", c.app, 1))
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, c.want) }) {
				t.Fatalf("errors %q do not mention %q", errs, c.want)
			}
		})
	}
}

func TestDebugRefusesWatch(t *testing.T) {
	base := "version: 1\napp:\n  run: node a.js\n%s\nscenarios:\n  - id: s\n"
	for _, app := range []string{
		"  debug: {lang: node, run: node --watch --inspect=$AIMAN_INSPECT a.js}",
		"  debug: {lang: bun, run: bun --hot --inspect=$AIMAN_INSPECT a.ts}",
		"  debug: {lang: node, run: node --inspect=0.0.0.0:9229 --x=$AIMAN_INSPECT a.js}",
		"  debug: {lang: node, entry: a.js, runtime_args: [--watch]}",
		"  debug: {lang: node, entry: a.js, runtime_args: ['--watch-path=src']}",
		"  debug: {lang: node, entry: a.js, runtime_args: ['--inspect=0.0.0.0']}",
	} {
		errs := parseErrs(t, strings.Replace(base, "%s", app, 1))
		if !slices.ContainsFunc(errs, func(e string) bool {
			return strings.Contains(e, "--watch") || strings.Contains(e, "--hot") || strings.Contains(e, "0.0.0.0")
		}) {
			t.Fatalf("%s: errors %q do not refuse the flag", app, errs)
		}
	}
	// A check's debug command is held to the same rules.
	check := "version: 1\napp:\n  run: x\nscenarios:\n  - id: s\n    checks:\n      - name: c\n        run: x\n        debug: {lang: node, run: node --watch --inspect-brk=$AIMAN_INSPECT t.js}\n"
	if errs := parseErrs(t, check); !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "checks[0].debug") }) {
		t.Fatalf("check debug errors = %q", errs)
	}
}

func TestDebugUnsupportedLang(t *testing.T) {
	adapters := []DebugAdapter{&GoDebug{}, &NodeDebug{}}
	for _, lang := range []string{"bun", "python"} {
		f := appFile(t, "  run: x\n  debug: {lang: "+lang+", run: x --inspect=$AIMAN_INSPECT}\n")
		_, err := DebugAdapterFor(f, adapters)
		var he *Error
		if !errors.As(err, &he) || he.Code != CodeDebugUnsupported || he.Data()["lang"] != lang {
			t.Fatalf("%s: err = %v (%+v), want harness_debug_unsupported with data.lang", lang, err, he)
		}
		if !strings.Contains(err.Error(), "go, node") {
			t.Fatalf("%s: message %q does not list the supported languages", lang, err)
		}
	}
	if a, err := DebugAdapterFor(appFile(t, "  run: x\n  debug: {lang: node, entry: a.js}\n"), adapters); err != nil || a.Lang() != "node" {
		t.Fatalf("node adapter = %v, %v", a, err)
	}
}
