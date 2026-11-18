// Package awssig signs HTTP requests with AWS Signature Version 4 for fixed
// credentials. The harness uses it with the emulator's public test key only:
// the edge's read relay re-signs every request it admits, and the applier
// signs the calls it sends through the tap.
//
// The canonical path is encoded once and never normalised, which is S3's
// rule; the other services aiman calls post to "/", where both rules agree.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Algorithm is the SigV4 algorithm name.
const Algorithm = "AWS4-HMAC-SHA256"

// Payload hashes a caller may pass to Sign.
const (
	// UnsignedPayload signs no body; S3 accepts it.
	UnsignedPayload = "UNSIGNED-PAYLOAD"
	// EmptyPayload is the hex SHA-256 of no bytes.
	EmptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Header names Sign writes.
const (
	HeaderDate          = "X-Amz-Date"
	HeaderContentSHA256 = "X-Amz-Content-Sha256"
	HeaderAuthorization = "Authorization"
	timeFormat          = "20060102T150405Z"
	dateFormat          = "20060102"
)

// Credentials are an access key pair. The harness holds only the emulator's
// test key.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Signer signs requests for one region and service.
type Signer struct {
	Credentials Credentials
	Region      string
	Service     string
	// ContentSHA256 also sets X-Amz-Content-Sha256 to the payload hash,
	// which S3 requires.
	ContentSHA256 bool
}

// unsignedHeaders are left out of the signature: a proxy or the transport
// may change them.
var unsignedHeaders = []string{"authorization", "user-agent", "expect", "x-amzn-trace-id", "content-length", "connection"}

// PayloadHash is the hex SHA-256 of body.
func PayloadHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Sign sets X-Amz-Date (and X-Amz-Content-Sha256 when s.ContentSHA256) and
// Authorization on req for time t. payloadHash is PayloadHash of the body,
// EmptyPayload or UnsignedPayload. req.Host, else req.URL.Host, is the
// signed host. req.URL.RawPath is set to the path Sign encoded, so the
// request line carries exactly the signed path.
func (s Signer) Sign(req *http.Request, payloadHash string, t time.Time) error {
	if s.Credentials.AccessKeyID == "" || s.Credentials.SecretAccessKey == "" || s.Region == "" || s.Service == "" {
		return errors.New("awssig: credentials, region and service are required")
	}
	if payloadHash == "" {
		return errors.New("awssig: payload hash is required")
	}
	t = t.UTC()
	req.Header.Del(HeaderAuthorization)
	req.Header.Set(HeaderDate, t.Format(timeFormat))
	if s.ContentSHA256 {
		req.Header.Set(HeaderContentSHA256, payloadHash)
	}
	path := EscapePath(req.URL.Path)
	req.URL.RawPath = path
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	creq, signed := CanonicalRequest(req.Method, path, req.URL.RawQuery, host, req.Header, payloadHash)
	scope := t.Format(dateFormat) + "/" + s.Region + "/" + s.Service + "/aws4_request"
	sts := StringToSign(t, scope, creq)
	key := SigningKey(s.Credentials.SecretAccessKey, t.Format(dateFormat), s.Region, s.Service)
	sig := hex.EncodeToString(hmacSHA256(key, []byte(sts)))
	req.Header.Set(HeaderAuthorization, Algorithm+" Credential="+s.Credentials.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
	return nil
}

// CanonicalRequest builds the canonical request from an encoded path, a raw
// query, the host and the headers, and returns it with the signed header
// list.
func CanonicalRequest(method, path, rawQuery, host string, h http.Header, payloadHash string) (creq, signed string) {
	type kv struct{ k, v string }
	headers := []kv{{"host", canonicalValue([]string{host})}}
	for name, vals := range h {
		n := strings.ToLower(name)
		if n == "host" || slices.Contains(unsignedHeaders, n) {
			continue
		}
		headers = append(headers, kv{n, canonicalValue(vals)})
	}
	slices.SortFunc(headers, func(a, b kv) int { return strings.Compare(a.k, b.k) })
	var ch strings.Builder
	names := make([]string, 0, len(headers))
	for _, x := range headers {
		ch.WriteString(x.k + ":" + x.v + "\n")
		names = append(names, x.k)
	}
	signed = strings.Join(names, ";")
	if path == "" {
		path = "/"
	}
	creq = strings.Join([]string{method, path, CanonicalQuery(rawQuery), ch.String(), signed, payloadHash}, "\n")
	return creq, signed
}

// canonicalValue trims each value, collapses inner runs of spaces and joins
// several values with commas.
func canonicalValue(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(out, ",")
}

// CanonicalQuery decodes a raw query and re-encodes it with SigV4's rules:
// every name and value encoded strictly, pairs sorted by name then value,
// an empty value kept as "name=".
func CanonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		pairs = append(pairs, kv{Escape(k), Escape(v)})
	}
	slices.SortFunc(pairs, func(a, b kv) int {
		if c := strings.Compare(a.k, b.k); c != 0 {
			return c
		}
		return strings.Compare(a.v, b.v)
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// StringToSign is the SigV4 string to sign for a canonical request.
func StringToSign(t time.Time, scope, creq string) string {
	sum := sha256.Sum256([]byte(creq))
	return Algorithm + "\n" + t.UTC().Format(timeFormat) + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

// SigningKey derives the day's key for region and service.
func SigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// Escape percent-encodes every byte except A-Z, a-z, 0-9, '-', '.', '_'
// and '~', with upper-case hex: SigV4's URI encoding.
func Escape(s string) string { return escape(s, false) }

// EscapePath is Escape that keeps '/'; an empty path is "/".
func EscapePath(p string) string {
	if p == "" {
		return "/"
	}
	return escape(p, true)
}

func escape(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || (slash && c == '/') {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}
