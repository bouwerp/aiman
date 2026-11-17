package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func appFile(t *testing.T, app string) *File {
	t.Helper()
	f, err := Parse([]byte("version: 1\napp:\n" + app + "scenarios:\n  - id: s\n"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGoDebugLaunch(t *testing.T) {
	cases := []struct {
		name, app, prog string
		args            []string
		bad             bool
	}{
		{"plain", "  run: /out/app serve --addr :8080\n  debug: {lang: go}\n", "/out/app", []string{"serve", "--addr", ":8080"}, false},
		{"exec and relative", "  run: exec ./bin/app --x=1 'two words'\n  debug: {lang: go}\n", "/src/bin/app", []string{"--x=1", "two words"}, false},
		{"explicit program", "  run: cd x && ./app\n  debug: {lang: go, program: /out/app, args: [a, b]}\n", "/out/app", []string{"a", "b"}, false},
		{"shell list", "  run: cd x && ./app\n  debug: {lang: go}\n", "", nil, true},
		{"expansion", "  run: /out/app --port $PORT\n  debug: {lang: go}\n", "", nil, true},
		{"assignment", "  run: PORT=1 /out/app\n  debug: {lang: go}\n", "", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := (&GoDebug{}).Launch(appFile(t, c.app))
			if c.bad {
				var he *Error
				if !errors.As(err, &he) || he.Code != CodeInvalid || !strings.Contains(err.Error(), "app.debug.program") {
					t.Fatalf("err = %v, want harness_invalid naming app.debug.program", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			a := l.Arguments
			if a["program"] != c.prog || !slices.Equal(a["args"].([]string), c.args) || a["mode"] != "exec" || a["request"] != "launch" {
				t.Fatalf("launch = %+v", a)
			}
			if l.SourceRoot != "/src" || l.Lang != "go" {
				t.Fatalf("launch = %+v", l)
			}
		})
	}
}

func TestDebugAdapterFor(t *testing.T) {
	adapters := []DebugAdapter{&GoDebug{}}
	if _, err := DebugAdapterFor(appFile(t, "  run: /out/app\n"), adapters); err == nil {
		t.Fatal("a file without app.debug got an adapter")
	}
	_, err := DebugAdapterFor(appFile(t, "  run: node x.js\n  debug: {lang: node}\n"), adapters)
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeDebugUnsupported || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("node: %v", err)
	}
	a, err := DebugAdapterFor(appFile(t, "  run: /out/app\n  debug: {lang: go}\n"), adapters)
	if err != nil || a.Lang() != "go" {
		t.Fatalf("go: %v %v", a, err)
	}
}

func TestGoEditorConfig(t *testing.T) {
	l, err := (&GoDebug{}).Launch(appFile(t, "  run: /out/app serve\n  debug: {lang: go}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := EditorConfig(l, 52013)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["port"] != 52013 || cfg["host"] != "127.0.0.1" || cfg["program"] != "/out/app" || cfg["request"] != "launch" {
		t.Fatalf("editor config %+v", cfg)
	}
	sub := cfg["substitutePath"].([]map[string]string)
	if sub[0]["to"] != "/src" || sub[0]["from"] != "${workspaceFolder}" {
		t.Fatalf("substitutePath %+v", sub)
	}
}
