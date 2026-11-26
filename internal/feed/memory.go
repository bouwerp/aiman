package feed

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memory is the agent's running notes, a file serve owns
// (~/.aiman/feed/memory.md). Each line is one bullet:
//
//   - [m12] 2026-11-20T10:00:00Z the note
//
// A first line "next: <n>" keeps ids from being reused after a removal.
type Memory struct {
	Path string
	mu   sync.Mutex
}

// NewMemory is the memory file at path.
func NewMemory(path string) *Memory { return &Memory{Path: path} }

// Bullets reads the file, oldest first. A missing file has none.
func (m *Memory) Bullets() ([]MemoryBullet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bs, _, err := m.load()
	return bs, err
}

func (m *Memory) load() ([]MemoryBullet, int, error) {
	f, err := os.Open(m.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	var out []MemoryBullet
	next := 1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), MaxMemoryBytes*2)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "next: "); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > next {
				next = n
			}
			continue
		}
		rest, ok := strings.CutPrefix(line, "- [")
		if !ok {
			continue
		}
		id, rest, ok := strings.Cut(rest, "] ")
		if !ok {
			continue
		}
		at, text, _ := strings.Cut(rest, " ")
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			continue
		}
		out = append(out, MemoryBullet{ID: id, Text: text, At: t})
		if n, err := strconv.Atoi(strings.TrimPrefix(id, "m")); err == nil && n >= next {
			next = n + 1
		}
	}
	return out, next, sc.Err()
}

func (m *Memory) save(bs []MemoryBullet, next int) error {
	render := func(bs []MemoryBullet) string {
		var b strings.Builder
		fmt.Fprintf(&b, "next: %d\n", next)
		for _, x := range bs {
			fmt.Fprintf(&b, "- [%s] %s %s\n", x.ID, x.At.UTC().Format(time.RFC3339), x.Text)
		}
		return b.String()
	}
	body := render(bs)
	for len(body) > MaxMemoryBytes && len(bs) > 0 {
		bs = bs[1:]
		body = render(bs)
	}
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o700); err != nil {
		return err
	}
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}

// Apply applies at most MaxMemoryOps ops: add cuts its text to one line of
// MaxMemoryBullet bytes, remove drops a bullet by id. The oldest bullets go
// when the file passes MaxMemoryBytes. It returns the ops applied.
func (m *Memory) Apply(ops []MemoryOp, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bs, next, err := m.load()
	if err != nil {
		return 0, err
	}
	if len(ops) > MaxMemoryOps {
		ops = ops[:MaxMemoryOps]
	}
	n := 0
	for _, op := range ops {
		switch op.Op {
		case "add":
			text := cutBytes(oneLine(op.Text), MaxMemoryBullet)
			if text == "" {
				continue
			}
			bs = append(bs, MemoryBullet{ID: "m" + strconv.Itoa(next), Text: text, At: now})
			next++
			n++
		case "remove":
			if i := bulletIndex(bs, op.ID); i >= 0 {
				bs = append(bs[:i], bs[i+1:]...)
				n++
			}
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, m.save(bs, next)
}

// Forget removes one bullet; false when there is no such bullet.
func (m *Memory) Forget(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bs, next, err := m.load()
	if err != nil {
		return false, err
	}
	i := bulletIndex(bs, id)
	if i < 0 {
		return false, nil
	}
	return true, m.save(append(bs[:i], bs[i+1:]...), next)
}

func bulletIndex(bs []MemoryBullet, id string) int {
	for i, b := range bs {
		if b.ID == id {
			return i
		}
	}
	return -1
}
