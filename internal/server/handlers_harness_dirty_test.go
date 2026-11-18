package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
)

// TestHarnessListDirty reads the harness file from the working tree with
// dirty, so a session checks a file it has not committed, and still reads
// the commit without it.
func TestHarnessListDirty(t *testing.T) {
	srv, dir := harnessServer(t, harnessSetup{})
	type listResult struct {
		Dirty      bool   `json:"dirty"`
		DiffSHA256 string `json:"diff_sha256"`
		Scenarios  []any  `json:"scenarios"`
		FileError  *struct {
			Code string `json:"code"`
		} `json:"file_error"`
	}
	path := filepath.Join(dir, harness.FilePath)
	if err := os.WriteFile(path, []byte(serverHarnessFile+"  - id: extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var list listResult
	rpcOK(t, srv, "s1", "harness.list", map[string]any{"session": "s1"}, &list)
	if list.Dirty || len(list.Scenarios) != 2 {
		t.Fatalf("committed list = %+v", list)
	}
	list = listResult{}
	rpcOK(t, srv, "s1", "harness.list", map[string]any{"session": "s1", "dirty": true}, &list)
	if !list.Dirty || list.DiffSHA256 == "" || len(list.Scenarios) != 3 || list.FileError != nil {
		t.Fatalf("dirty list = %+v", list)
	}
	if err := os.WriteFile(path, []byte(serverHarnessFile+"bogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list = listResult{}
	rpcOK(t, srv, "s1", "harness.list", map[string]any{"session": "s1", "dirty": true}, &list)
	if list.FileError == nil || list.FileError.Code != harness.CodeInvalid {
		t.Fatalf("broken dirty list = %+v", list)
	}
}
