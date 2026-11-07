package transcript

import (
	"bufio"
	"errors"
	"io"
)

// readLines calls fn for each complete '\n'-terminated line, without the '\n'.
// A trailing partial line is not delivered. Lines of any length are accepted.
// line is valid only for the duration of fn.
//
// It keeps the behaviour of internal/usage's unexported readLines.
func readLines(r io.Reader, fn func(line []byte) error) error {
	br := bufio.NewReader(r)
	var pending []byte
	for {
		frag, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			pending = append(pending, frag...)
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line := frag[:len(frag)-1]
		if len(pending) > 0 {
			line = append(pending, line...)
			pending = nil
		}
		if err := fn(line); err != nil {
			return err
		}
	}
}
