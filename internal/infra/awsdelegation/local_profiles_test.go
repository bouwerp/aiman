package awsdelegation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAWSSectionFile_config(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	content := `[default]
region = us-east-1

[profile work]
region = eu-west-1

[profile personal]
`
	if err := osWrite(p, content); err != nil {
		t.Fatal(err)
	}
	var got []string
	add := func(s string) { got = append(got, s) }
	if err := parseAWSSectionFile(p, add, true); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"default": true, "work": true, "personal": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected %q", s)
		}
	}
}

func TestParseAWSSectionFile_credentials(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials")
	content := `[default]
key = x

[legacy]
key = y
`
	if err := osWrite(p, content); err != nil {
		t.Fatal(err)
	}
	var got []string
	add := func(s string) { got = append(got, s) }
	if err := parseAWSSectionFile(p, add, false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func osWrite(path, s string) error {
	return os.WriteFile(path, []byte(s), 0600)
}
