package edge

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// bodyCapture keeps the first limit bytes written to it and counts all.
type bodyCapture struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	total int64
}

func newBodyCapture(limit int) *bodyCapture { return &bodyCapture{limit: limit} }

func (c *bodyCapture) write(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(b))
	if room := c.limit - len(c.buf); room > 0 {
		c.buf = append(c.buf, b[:min(room, len(b))]...)
	}
}

// snapshot is the kept bytes, the total and whether the total is more.
func (c *bodyCapture) snapshot() ([]byte, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf...), c.total, c.total > int64(len(c.buf))
}

// captureReader copies what passes through into c and calls done once, at
// EOF, on a read error or on Close.
type captureReader struct {
	rc   io.ReadCloser
	c    *bodyCapture
	done func(error)
	once sync.Once
}

func (r *captureReader) Read(b []byte) (int, error) {
	n, err := r.rc.Read(b)
	if n > 0 {
		r.c.write(b[:n])
	}
	if err != nil {
		r.end(err)
	}
	return n, err
}

func (r *captureReader) Close() error {
	err := r.rc.Close()
	r.end(nil)
	return err
}

func (r *captureReader) end(err error) {
	if r.done != nil {
		r.once.Do(func() { r.done(err) })
	}
}

// bodyText turns a captured body into text: decoded when gzip or deflate
// (as far as the kept bytes go, at most limit bytes), empty with binary set
// when it is not UTF-8 text.
func bodyText(raw []byte, encoding string, limit int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var zr io.Reader
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "gzip", "x-gzip":
		if r, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			zr = r
		}
	case "deflate":
		zr = flate.NewReader(bytes.NewReader(raw))
	case "", "identity":
	default:
		return "", true
	}
	if zr != nil {
		// A body cut at the limit ends in an unexpected EOF; what decoded
		// before it is kept.
		decoded, _ := io.ReadAll(io.LimitReader(zr, int64(limit)))
		raw = decoded
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", true
	}
	// A cut can split the last rune; drop the partial rune, not the body.
	for i := 0; i < utf8.UTFMax && len(raw) > 0 && !utf8.Valid(raw); i++ {
		raw = raw[:len(raw)-1]
	}
	if !utf8.Valid(raw) {
		return "", true
	}
	return string(raw), false
}
