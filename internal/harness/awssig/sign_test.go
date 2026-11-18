package awssig

import (
	"bufio"
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The vectors under testdata/aws4_testsuite are AWS's Signature Version 4
// test suite as botocore ships it (Apache-2.0, LICENSE and NOTICE beside
// them). Left out: the cases for non-S3 path normalisation and double
// encoding (normalize-path, get-utf8), multi-line header values, and
// session tokens, none of which the harness signs.
var vectorCreds = Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"} //nolint:gosec // G101: AWS's published example key

func TestSigV4TestSuite(t *testing.T) {
	entries, err := os.ReadDir("testdata/aws4_testsuite")
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if err != nil || len(dirs) < 15 {
		t.Fatalf("vectors: %v, %d", err, len(dirs))
	}
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	for _, name := range dirs {
		dir := filepath.Join("testdata/aws4_testsuite", name)
		t.Run(name, func(t *testing.T) {
			raw := read(t, filepath.Join(dir, name+".req"))
			req, body := parseVector(t, raw)
			hash := PayloadHash(body)
			creq, _ := CanonicalRequest(req.Method, EscapePath(req.URL.Path), req.URL.RawQuery, req.Host, req.Header, hash)
			if want := read(t, filepath.Join(dir, name+".creq")); creq != want {
				t.Fatalf("canonical request\n got: %q\nwant: %q", creq, want)
			}
			s := Signer{Credentials: vectorCreds, Region: "us-east-1", Service: "service"}
			if err := s.Sign(req, hash, when); err != nil {
				t.Fatal(err)
			}
			if got, want := req.Header.Get(HeaderAuthorization), read(t, filepath.Join(dir, name+".authz")); got != want {
				t.Fatalf("authorization\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parseVector reads a suite .req file: a request line, Name:value header
// lines (a name may repeat), a blank line and the body.
func parseVector(t *testing.T, raw string) (*http.Request, []byte) {
	t.Helper()
	head, body, _ := strings.Cut(raw, "\n\n")
	sc := bufio.NewScanner(strings.NewReader(head))
	sc.Scan()
	parts := strings.SplitN(sc.Text(), " ", 3)
	if len(parts) != 3 {
		t.Fatalf("request line %q", sc.Text())
	}
	target := parts[1]
	p, q, _ := strings.Cut(target, "?")
	u := &url.URL{Path: p, RawQuery: q}
	req := &http.Request{Method: parts[0], URL: u, Header: http.Header{}}
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			t.Fatalf("header line %q", sc.Text())
		}
		if strings.EqualFold(k, "host") {
			req.Host = v
			continue
		}
		req.Header[k] = append(req.Header[k], v)
	}
	return req, []byte(body)
}

// TestSignS3KnownAnswer pins a path-style S3 GET with a key that needs
// encoding, the content hash header and a query, so a change to the
// encoding rules shows.
func TestSignS3KnownAnswer(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://aws-emu:4566/my-bucket/a%20b/c+d%C3%A9.txt?versionId=v%2B1", nil)
	req.Header.Set("Range", "bytes=0-9")
	s := Signer{Credentials: Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, Region: "eu-west-1", Service: "s3", ContentSHA256: true}
	if err := s.Sign(req, EmptyPayload, time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.RequestURI(); got != "/my-bucket/a%20b/c%2Bd%C3%A9.txt?versionId=v%2B1" {
		t.Fatalf("request uri %q", got)
	}
	creq, signed := CanonicalRequest(req.Method, req.URL.RawPath, req.URL.RawQuery, req.URL.Host, req.Header, EmptyPayload)
	wantCreq := "GET\n/my-bucket/a%20b/c%2Bd%C3%A9.txt\nversionId=v%2B1\n" +
		"host:aws-emu:4566\nrange:bytes=0-9\nx-amz-content-sha256:" + EmptyPayload + "\nx-amz-date:20261118T093000Z\n\n" +
		"host;range;x-amz-content-sha256;x-amz-date\n" + EmptyPayload
	if creq != wantCreq || signed != "host;range;x-amz-content-sha256;x-amz-date" {
		t.Fatalf("canonical request\n%s", creq)
	}
	auth := req.Header.Get(HeaderAuthorization)
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=test/20261118/eu-west-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=") {
		t.Fatalf("authorization %q", auth)
	}
	// Signing again at the same time gives the same signature.
	again := req.Clone(req.Context())
	if err := s.Sign(again, EmptyPayload, time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)); err != nil || again.Header.Get(HeaderAuthorization) != auth {
		t.Fatalf("not deterministic: %v", err)
	}
}

func TestSignRefusesMissingFacts(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	for _, s := range []Signer{{}, {Credentials: Credentials{AccessKeyID: "a", SecretAccessKey: "b"}, Region: "r"}} {
		if s.Sign(req, EmptyPayload, time.Now()) == nil {
			t.Fatalf("%+v signed", s)
		}
	}
	s := Signer{Credentials: Credentials{AccessKeyID: "a", SecretAccessKey: "b"}, Region: "r", Service: "s"}
	if s.Sign(req, "", time.Now()) == nil {
		t.Fatal("empty payload hash signed")
	}
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"a-b_c.d~e": "a-b_c.d~e", "a b": "a%20b", "a/b": "a%2Fb", "é": "%C3%A9", "+*": "%2B%2A",
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
	if EscapePath("") != "/" || EscapePath("/a b/c") != "/a%20b/c" {
		t.Fatal("EscapePath")
	}
	if !bytes.Equal([]byte(PayloadHash(nil)), []byte(EmptyPayload)) {
		t.Fatal("PayloadHash(nil)")
	}
}
