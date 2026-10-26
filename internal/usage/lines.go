package usage

import (
	"bufio"
	"errors"
	"io"
)

// readLines calls fn for each complete '\n'-terminated line (without the '\n') and
// returns the bytes consumed through the last complete line. A trailing partial
// line is not consumed. Lines of any length are accepted.
// line is valid only for the duration of fn.
func readLines(r io.Reader, fn func(line []byte) error) (int64, error) {
	br := bufio.NewReader(r)
	var pending []byte
	var consumed int64
	for {
		frag, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			pending = append(pending, frag...)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return consumed, err
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		line, n := joinLine(pending, frag)
		pending = nil
		consumed += n
		if err := fn(line); err != nil {
			return consumed, err
		}
	}
}

// joinLine strips the trailing newline. n counts that newline.
func joinLine(pending, frag []byte) ([]byte, int64) {
	n := int64(len(pending) + len(frag))
	if len(frag) > 0 && frag[len(frag)-1] == '\n' {
		frag = frag[:len(frag)-1]
	}
	if len(pending) == 0 {
		return frag, n
	}
	line := make([]byte, 0, len(pending)+len(frag))
	line = append(line, pending...)
	line = append(line, frag...)
	return line, n
}
