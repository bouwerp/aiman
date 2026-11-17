package harness

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteJSONConcurrentSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeJSON(path, map[string]int{"n": i}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}
	matches, _ := filepath.Glob(path + ".*.tmp")
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}
