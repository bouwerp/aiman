package debuglog

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

// DefaultPath is used when debug logging is not opted in via --debug.
// Existing TUI traces (session create/restart) already write here.
const DefaultPath = "/tmp/aiman-debug.log"

var (
	mu      sync.Mutex
	enabled bool
	path    = DefaultPath
	file    *os.File
)

func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
	enabled = false
	path = DefaultPath
}

// SetPath changes where Write sends lines when debug is not Enabled.
func SetPath(p string) {
	mu.Lock()
	defer mu.Unlock()
	if p != "" {
		path = p
	}
}

// Enable appends debug output to path (0600) for the rest of the process.
func Enable(p string) error {
	if p == "" {
		p = DefaultPath
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	file = f
	path = p
	enabled = true
	return nil
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
}

func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Writer returns the open debug file when Enable has been called, else nil.
func Writer() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return nil
	}
	return file
}

// Write appends line to the current debug file. When Enable has not been
// called this still writes to Path (default /tmp/aiman-debug.log) so existing
// TUI traces keep a post-mortem dump.
func Write(line string) error {
	mu.Lock()
	f := file
	p := path
	mu.Unlock()

	if f != nil {
		_, err := f.WriteString(line)
		return err
	}
	df, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = df.WriteString(line)
	_ = df.Close()
	return err
}
