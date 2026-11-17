package harness

import (
	"strings"
	"testing"
)

// Compose fills $VAR in the YAML from the environment of whoever runs it,
// serve here. Every $ the file or an adapter writes must reach the
// container's shell as written.
func TestComposeEscapesInterpolation(t *testing.T) {
	file := strings.Replace(composeFixture, "run: /out/app serve --addr :8080", `run: /out/app serve --addr ":$PORT" --home "${HOME}"`, 1)
	file = strings.Replace(file, "env: {POSTGRES_PASSWORD: test}", "env: {POSTGRES_PASSWORD: te$t}", 1)
	file = strings.Replace(file, "ready: pg_isready -h db -U postgres", "ready: pg_isready -U $POSTGRES_USER", 1)
	_, doc := buildDebug(t, file, testSpec())
	app := strings.Join(strs(svcOf(t, doc, ServiceApp)["command"]), " ")
	if !strings.Contains(app, `":$$PORT" --home "$${HOME}"`) {
		t.Fatalf("app command %q is not escaped", app)
	}
	db := svcOf(t, doc, "db")
	if env, _ := db["environment"].(map[string]any); env["POSTGRES_PASSWORD"] != "te$$t" {
		t.Fatalf("db env %v is not escaped", db["environment"])
	}
	hc, _ := db["healthcheck"].(map[string]any)
	if got := strings.Join(strs(hc["test"]), " "); !strings.Contains(got, "$$POSTGRES_USER") {
		t.Fatalf("healthcheck %q is not escaped", got)
	}
}
