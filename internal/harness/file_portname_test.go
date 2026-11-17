package harness

import (
	"errors"
	"strings"
	"testing"
)

// An app port and a service share the edge's relay namespace, so a port may
// not take a service's name.
func TestRejectsPortNamedLikeService(t *testing.T) {
	_, err := Parse([]byte(`version: 1
app:
  run: serve
  ports: [{name: db, container: 8080}]
services:
  db: {image: "postgres:16", database: {kind: postgres}}
scenarios:
  - id: s
    services: [db]
`))
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("err = %v, want %s", err, CodeInvalid)
	}
	if !strings.Contains(strings.Join(he.Errors, "\n"), `app.ports[0]: name "db" is also a service`) {
		t.Fatalf("errors = %q", he.Errors)
	}
}
