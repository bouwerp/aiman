package awsiac

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// unsignedPayload is the payload hash of a streamed S3 upload.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// signV4 signs req with AWS Signature Version 4 for the run's fixed key.
// payloadHash is the hex sha256 of the body, or unsignedPayload. The path
// of req.URL must already be URI-encoded in RawPath (awsPath); S3 signs it
// as sent, which is what the other services see for "/" too.
func signV4(req *http.Request, service, region, key, secret, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.URL.Host
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "content-md5" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
			values[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method, path, canonicalQuery(req.URL.Query()), canonHeaders.String(), signed, payloadHash,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+key+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// awsEscape is SigV4's URI encoding: every byte but the unreserved ones.
func awsEscape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k, false)+"="+awsEscape(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// awsPath encodes an S3 path, /<bucket>[/<key>], keeping the slashes.
func awsPath(bucket, key string) (raw, plain string) {
	plain = "/" + bucket
	raw = "/" + awsEscape(bucket, false)
	if key != "" {
		plain += "/" + key
		raw += "/" + awsEscape(key, true)
	}
	return raw, plain
}
