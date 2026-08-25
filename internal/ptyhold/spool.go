package ptyhold

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// spoolLimit returns the active-segment cap. SpoolMaxBytes in production;
// AIMAN_PTY_SPOOL_MAX overrides it (documented contract knob, mainly tests).
func spoolLimit() int64 {
	if v := os.Getenv("AIMAN_PTY_SPOOL_MAX"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return int64(n)
		}
	}
	return SpoolMaxBytes
}

// spoolWriter appends PTY output to <dir>/spool, rotating to spool.old when
// the active segment exceeds SpoolMaxBytes. Readers must tolerate either file
// being absent at any moment.
type spoolWriter struct {
	mu   sync.Mutex
	dir  string
	f    *os.File
	size int64
}

func newSpoolWriter(dir string) *spoolWriter {
	return &spoolWriter{dir: dir}
}

func (w *spoolWriter) write(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		f, err := os.OpenFile(filepath.Join(w.dir, SpoolFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return // spooling is best-effort; the live pipe stays authoritative
		}
		if st, serr := f.Stat(); serr == nil {
			w.size = st.Size()
		}
		w.f = f
	}
	if w.size+int64(len(data)) > spoolLimit() {
		w.rotateLocked()
	}
	n, err := w.f.Write(data)
	if err != nil {
		return
	}
	w.size += int64(n)
}

func (w *spoolWriter) rotateLocked() {
	_ = w.f.Close()
	w.f = nil
	_ = os.Remove(filepath.Join(w.dir, SpoolOld))
	_ = os.Rename(filepath.Join(w.dir, SpoolFile), filepath.Join(w.dir, SpoolOld))
}

func (w *spoolWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
}
