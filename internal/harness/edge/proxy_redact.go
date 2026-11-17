package edge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/bouwerp/aiman/internal/redact"
)

// secretHeaders lose their value before a flow is stored.
var secretHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"X-Api-Key":           true,
}

// SecretKey matches a JSON key, form field, query parameter or header name
// whose value is a secret, as the database browser's column rule does.
var SecretKey = regexp.MustCompile(`(?i)password|passwd|secret|token|api[_-]?key`)

// secretPair finds "key": "value" and key=value pairs whose key is a secret
// in text that does not parse as a whole (a body cut at the byte limit).
var secretPair = regexp.MustCompile(`(?i)("[^"]*(?:password|passwd|secret|token|api[_-]?key)[^"]*"\s*:\s*)("(?:[^"\\]|\\.)*"|[^,}\]\s]+)|((?:^|[&?])[^=&\s]*(?:password|passwd|secret|token|api[_-]?key)[^=&\s]*=)[^&\s]*`)

// RedactFlow replaces secrets in f and returns how many it replaced. It is
// safe to run twice: the edge runs it before storing a flow and serve again
// before a flow leaves the host.
func RedactFlow(f *Flow) int {
	n := 0
	var c int
	f.URL, c = redactURL(f.URL)
	n += c
	f.RequestHeaders, c = redactHeaders(f.RequestHeaders)
	n += c
	f.ResponseHeaders, c = redactHeaders(f.ResponseHeaders)
	n += c
	f.RequestBody, c = redactBody(f.RequestBody)
	n += c
	f.ResponseBody, c = redactBody(f.ResponseBody)
	n += c
	f.Error, c = redact.Line(f.Error, nil)
	n += c
	return n
}

func redactHeaders(h http.Header) (http.Header, int) {
	if h == nil {
		return nil, 0
	}
	out := make(http.Header, len(h))
	n := 0
	for k, vs := range h {
		ck := http.CanonicalHeaderKey(k)
		secret := secretHeaders[ck] || SecretKey.MatchString(ck)
		nv := make([]string, len(vs))
		for i, v := range vs {
			switch {
			case secret && v != redact.Mark:
				nv[i] = redact.Mark
				n++
			case secret:
				nv[i] = v
			default:
				var c int
				nv[i], c = redact.Line(v, nil)
				n += c
			}
		}
		out[ck] = nv
	}
	return out, n
}

// redactURL hides userinfo and the values of secret query parameters, then
// runs the line rules over what is left.
func redactURL(raw string) (string, int) {
	n := 0
	u, err := url.Parse(raw)
	if err == nil {
		if u.User != nil {
			u.User = url.User(redact.Mark)
			n++
		}
		if u.RawQuery != "" {
			q, c := redactQuery(u.RawQuery)
			u.RawQuery, n = q, n+c
		}
		raw = u.String()
	}
	out, c := redact.Line(raw, nil)
	return out, n + c
}

func redactQuery(raw string) (string, int) {
	parts := strings.Split(raw, "&")
	n := 0
	for i, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if !ok || v == "" || v == redact.Mark {
			continue
		}
		key, err := url.QueryUnescape(k)
		if err != nil {
			key = k
		}
		if SecretKey.MatchString(key) {
			parts[i] = k + "=" + redact.Mark
			n++
		}
	}
	return strings.Join(parts, "&"), n
}

// redactBody hides the values of secret keys in a JSON body (or, when the
// body does not parse, in key/value pairs found by pattern), then runs the
// line rules over every line.
func redactBody(body string) (string, int) {
	if body == "" {
		return body, 0
	}
	n := 0
	if j, c, ok := redactJSON(body); ok {
		body, n = j, c
	} else {
		body = secretPair.ReplaceAllStringFunc(body, func(m string) string {
			sm := secretPair.FindStringSubmatch(m)
			switch {
			case sm[1] != "":
				if strings.Trim(sm[2], `"`) == redact.Mark {
					return m
				}
				n++
				return sm[1] + `"` + redact.Mark + `"`
			case sm[3] != "":
				if strings.TrimPrefix(m, sm[3]) == redact.Mark || strings.TrimPrefix(m, sm[3]) == "" {
					return m
				}
				n++
				return sm[3] + redact.Mark
			}
			return m
		})
	}
	lines, c := redact.Lines(strings.Split(body, "\n"), nil)
	return strings.Join(lines, "\n"), n + c
}

// redactJSON redacts a body that parses as JSON as a whole and reports
// false otherwise. The output is compact JSON when anything changed.
func redactJSON(body string) (string, int, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", 0, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return "", 0, false
	}
	n := redactValue(v)
	if n == 0 {
		return body, 0, true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", 0, false
	}
	return strings.TrimSuffix(buf.String(), "\n"), n, true
}

func redactValue(v any) int {
	n := 0
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if SecretKey.MatchString(k) {
				if s, ok := val.(string); ok && s == redact.Mark {
					continue
				}
				if val == nil {
					continue
				}
				t[k] = redact.Mark
				n++
				continue
			}
			n += redactValue(val)
		}
	case []any:
		for _, e := range t {
			n += redactValue(e)
		}
	}
	return n
}
