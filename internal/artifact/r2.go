// Package artifact publishes session pages to a private R2 bucket that the
// aiman.fyi Worker serves behind Cloudflare Access (docs/specs/artifacts.md).
package artifact

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

var (
	ErrNotFound     = errors.New("artifact: not found")
	ErrPrecondition = errors.New("artifact: precondition failed") // R2 412
	ErrConflict     = errors.New("artifact: conflict")            // expect_version or meta ETag mismatch
	ErrLimits       = errors.New("artifact: over limits")
	ErrInvalid      = errors.New("artifact: invalid")
	ErrNotOwner     = errors.New("artifact: not owner")
)

// Cond makes a PUT conditional. IfNoneMatch sends "If-None-Match: *";
// IfMatch sends "If-Match: <etag>".
type Cond struct {
	IfNoneMatch bool
	IfMatch     string
}

// Bucket is the R2 surface the publisher uses. Get of a missing key is
// ErrNotFound; a failed condition is ErrPrecondition.
type Bucket interface {
	Get(ctx context.Context, key string) (data []byte, etag string, err error)
	Put(ctx context.Context, key string, data []byte, contentType string, c Cond) (etag string, err error)
	Delete(ctx context.Context, keys []string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// R2 is a Bucket over R2's S3 API, path style.
type R2 struct {
	endpoint string
	bucket   string
	signer   awssig.Signer
	hc       *http.Client
	now      func() time.Time
}

// maxReadBytes bounds a GET body and an error body. A version file is at most
// MaxFileBytes; meta.json and listings are far smaller.
const maxReadBytes = MaxFileBytes + 1<<20

// NewR2 is a Bucket over R2's S3 API (path style: <endpoint>/<bucket>/<key>),
// signed with awssig (region "auto", service "s3", X-Amz-Content-Sha256 set).
func NewR2(endpoint, bucket string, creds awssig.Credentials, hc *http.Client) *R2 {
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	return &R2{
		endpoint: strings.TrimRight(endpoint, "/"),
		bucket:   bucket,
		signer:   awssig.Signer{Credentials: creds, Region: "auto", Service: "s3", ContentSHA256: true},
		hc:       hc,
		now:      time.Now,
	}
}

// R2Endpoint is the S3 endpoint of a Cloudflare account.
func R2Endpoint(accountID string) string {
	return "https://" + accountID + ".r2.cloudflarestorage.com"
}

func (r *R2) do(ctx context.Context, method, key, rawQuery string, body []byte, h http.Header) (*http.Response, error) {
	u, err := url.Parse(r.endpoint)
	if err != nil {
		return nil, fmt.Errorf("r2 url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + r.bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath = ""
	u.RawQuery = rawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.URL.Path = u.Path
	req.ContentLength = int64(len(body))
	for k, v := range h {
		req.Header[k] = v
	}
	if err := r.signer.Sign(req, awssig.PayloadHash(body), r.now()); err != nil {
		return nil, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("r2 %s %s: %w", method, key, err)
	}
	return resp, nil
}

// statusErr maps an R2 answer that is not 2xx to an error.
func statusErr(resp *http.Response, method, key string) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	case http.StatusPreconditionFailed:
		return fmt.Errorf("%w: %s", ErrPrecondition, key)
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(msg, &e) == nil && e.Code != "" {
		return fmt.Errorf("r2 %s %s: %d %s: %s", method, key, resp.StatusCode, e.Code, e.Message)
	}
	return fmt.Errorf("r2 %s %s: %s", method, key, resp.Status)
}

func ok(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode < 300 }

// Get reads key and its ETag.
func (r *R2) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := r.do(ctx, http.MethodGet, key, "", nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return nil, "", statusErr(resp, http.MethodGet, key)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("r2 GET %s: %w", key, err)
	}
	return data, resp.Header.Get("ETag"), nil
}

// Put writes key, conditionally when c says so, and returns the new ETag.
func (r *R2) Put(ctx context.Context, key string, data []byte, contentType string, c Cond) (string, error) {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if c.IfNoneMatch {
		h.Set("If-None-Match", "*")
	}
	if c.IfMatch != "" {
		h.Set("If-Match", c.IfMatch)
	}
	resp, err := r.do(ctx, http.MethodPut, key, "", data, h)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return "", statusErr(resp, http.MethodPut, key)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.Header.Get("ETag"), nil
}

// Delete removes keys one by one; a key already gone is not an error.
func (r *R2) Delete(ctx context.Context, keys []string) error {
	for _, k := range keys {
		resp, err := r.do(ctx, http.MethodDelete, k, "", nil, nil)
		if err != nil {
			return err
		}
		if !ok(resp) && resp.StatusCode != http.StatusNotFound {
			err := statusErr(resp, http.MethodDelete, k)
			_ = resp.Body.Close()
			return err
		}
		_ = resp.Body.Close()
	}
	return nil
}

// List returns every key under prefix (ListObjectsV2, paged).
func (r *R2) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := r.do(ctx, http.MethodGet, "", q.Encode(), nil, nil)
		if err != nil {
			return nil, err
		}
		if !ok(resp) {
			err := statusErr(resp, "LIST", prefix)
			_ = resp.Body.Close()
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("r2 LIST %s: %w", prefix, err)
		}
		for _, c := range page.Contents {
			out = append(out, c.Key)
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}
