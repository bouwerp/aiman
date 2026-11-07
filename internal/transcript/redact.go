package transcript

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// replacements within one process, so ids still pair. Keys, numbers, booleans,
// structure and key order are kept. Empty strings stay empty. Fixtures under
// testdata are passed through it. A line that is not one JSON value returns
// nil: nothing unredacted comes out.
func Redact(line []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var out bytes.Buffer
	if err := redactValue(dec, &out, false); err != nil {
		return nil
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil
	}
	return out.Bytes()
}

// redactValue copies one value from dec to out. keep says the value sits
// directly under an enum key, which spares a string and nothing nested deeper.
// It walks tokens, because decoding into a map would lose the key order.
func redactValue(dec *json.Decoder, out *bytes.Buffer, keep bool) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return redactObject(dec, out)
		}
		return redactArray(dec, out)
	case string:
		if !keep {
			t = redactString(t)
		}
		return writeJSON(out, t)
	case json.Number:
		out.WriteString(t.String())
		return nil
	default:
		return writeJSON(out, t)
	}
}

func redactObject(dec *json.Decoder, out *bytes.Buffer) error {
	out.WriteByte('{')
	for i := 0; dec.More(); i++ {
		if i > 0 {
			out.WriteByte(',')
		}
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if err := writeJSON(out, key); err != nil {
			return err
		}
		out.WriteByte(':')
		if err := redactValue(dec, out, enumKeys[key]); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	_, err := dec.Token() // the closing brace
	return err
}

func redactArray(dec *json.Decoder, out *bytes.Buffer) error {
	out.WriteByte('[')
	for i := 0; dec.More(); i++ {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := redactValue(dec, out, false); err != nil {
			return err
		}
	}
	out.WriteByte(']')
	_, err := dec.Token() // the closing bracket
	return err
}

func writeJSON(out *bytes.Buffer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	out.Write(raw)
	return nil
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
