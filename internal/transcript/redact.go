package transcript

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// enumKeys hold protocol vocabulary, not content, so their values stay.
var enumKeys = map[string]bool{
	"type":          true,
	"role":          true,
	"name":          true,
	"status":        true,
	"subtype":       true,
	"sessionUpdate": true,
	"kind":          true,
	"state":         true,
	"method":        true,
}

// redactSalt keys the replacements for one process, so a replacement cannot be
// checked against a guess at the original.
var redactSalt = sync.OnceValue(func() []byte {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		panic(fmt.Sprintf("transcript redact: no randomness: %v", err))
	}
	return salt
})

// Redact returns one JSON line with every string value replaced, except the
// values of the enum-like keys type, role, name, status, subtype,
// sessionUpdate, kind, state and method. Equal strings map to equal
// replacements within one process, so ids still pair. Keys, numbers, booleans
// and structure are kept; object keys come back sorted. Empty strings stay
// empty. Fixtures under testdata are passed through it. A line that is not
// JSON returns nil: nothing unredacted comes out.
func Redact(line []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return nil
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactString(t)
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
	case map[string]any:
		for k, val := range t {
			if _, isString := val.(string); isString && enumKeys[k] {
				continue
			}
			t[k] = redactValue(val)
		}
	}
	return v
}

func redactString(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.New()
	h.Write(redactSalt())
	h.Write([]byte(s))
	return "r" + hex.EncodeToString(h.Sum(nil)[:8])
}

// RedactFile writes src to dst with each line through Redact, for building
// fixtures. Lines that are not JSON are dropped and a trailing partial line is
// not copied. Redact the files of one fixture in one process: ids pair only
// within a process. File names and object keys are not redacted, so a fixture
// still needs reading before it is committed.
func RedactFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // G304: a developer names the file
	if err != nil {
		return fmt.Errorf("redact: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst) //nolint:gosec // G304: a developer names the file
	if err != nil {
		return fmt.Errorf("redact: %w", err)
	}
	w := bufio.NewWriter(out)
	err = readLines(in, func(line []byte) error {
		r := Redact(line)
		if r == nil {
			return nil
		}
		_, werr := w.Write(append(r, '\n'))
		return werr
	})
	if err == nil {
		err = w.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("redact: %w", err)
	}
	return nil
}
