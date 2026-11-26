package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CFObjects is a Bucket over Cloudflare's R2 REST object API, authorised by
// a bearer token. That API ignores If-None-Match and If-Match, so Put
// checks the condition itself with a read first. The check races with other
// writers; Publisher's per-artifact lock keeps serve's own writes apart.
type CFObjects struct {
	base  string // .../accounts/<account>/r2/buckets/<bucket>/objects
	token TokenSource
	hc    *http.Client
}

// cfMissingKey is the REST API's error code for a key that does not exist.
const cfMissingKey = 10007

// NewCFObjects is a Bucket over the REST object API:
// <base>/accounts/<account>/r2/buckets/<bucket>/objects/<key>.
// It emulates Cond: IfNoneMatch reads the key first and refuses with
// ErrPrecondition when it exists; IfMatch reads the key's ETag first and
// refuses on a mismatch. Get returns the ETag header without quotes
// normalised to the quoted form "<hex>". List pages with ?prefix=&cursor=.
func NewCFObjects(base, account, bucket string, token TokenSource, hc *http.Client) *CFObjects {
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	return &CFObjects{
		base:  strings.TrimRight(base, "/") + "/accounts/" + url.PathEscape(account) + "/r2/buckets/" + url.PathEscape(bucket) + "/objects",
		token: token,
		hc:    hc,
	}
}

// keyURL escapes each element of key and keeps its slashes.
func (c *CFObjects) keyURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return c.base + "/" + strings.Join(parts, "/")
}

func (c *CFObjects) do(ctx context.Context, method, u string, body []byte, contentType string) (*http.Response, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// url.Error carries the URL only; the token is in a header.
		return nil, fmt.Errorf("r2 rest %s: %w", method, err)
	}
	return resp, nil
}

// restEnvelope is the REST API's JSON answer.
type restEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// cfStatusErr maps an answer that is not 2xx to an error with Cloudflare's
// message.
func cfStatusErr(resp *http.Response, method, key string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var env restEnvelope
	_ = json.Unmarshal(body, &env)
	if resp.StatusCode == http.StatusNotFound || (len(env.Errors) > 0 && env.Errors[0].Code == cfMissingKey) {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %s", ErrPrecondition, key)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("r2 rest %s %s: %s: %d %s", method, key, resp.Status, env.Errors[0].Code, env.Errors[0].Message)
	}
	return fmt.Errorf("r2 rest %s %s: %s", method, key, resp.Status)
}

// normalETag turns W/"x", "x" and x into "x".
func normalETag(e string) string {
	e = strings.TrimSpace(e)
	e = strings.TrimPrefix(e, "W/")
	e = strings.Trim(e, `"`)
	if e == "" {
		return ""
	}
	return `"` + e + `"`
}

// Get reads key and its ETag in the quoted form.
func (c *CFObjects) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.keyURL(key), nil, "")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return nil, "", cfStatusErr(resp, http.MethodGet, key)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("r2 rest GET %s: %w", key, err)
	}
	return data, normalETag(resp.Header.Get("ETag")), nil
}

// check emulates c against the key's current state.
func (c *CFObjects) check(ctx context.Context, key string, cond Cond) error {
	if !cond.IfNoneMatch && cond.IfMatch == "" {
		return nil
	}
	_, etag, err := c.Get(ctx, key)
	switch {
	case err == nil && cond.IfNoneMatch:
		return fmt.Errorf("%w: %s exists", ErrPrecondition, key)
	case err == nil && normalETag(cond.IfMatch) != etag:
		return fmt.Errorf("%w: %s changed", ErrPrecondition, key)
	case err == nil:
		return nil
	case isNotFound(err) && cond.IfMatch != "":
		return fmt.Errorf("%w: %s is gone", ErrPrecondition, key)
	case isNotFound(err):
		return nil
	}
	return err
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Put writes key after checking cond and returns the new ETag, quoted.
func (c *CFObjects) Put(ctx context.Context, key string, data []byte, contentType string, cond Cond) (string, error) {
	if err := c.check(ctx, key, cond); err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodPut, c.keyURL(key), data, contentType)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return "", cfStatusErr(resp, http.MethodPut, key)
	}
	var out struct {
		restEnvelope
		Result struct {
			ETag string `json:"etag"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", fmt.Errorf("r2 rest PUT %s: answer: %w", key, err)
	}
	if !out.Success {
		return "", fmt.Errorf("r2 rest PUT %s: not successful", key)
	}
	return normalETag(out.Result.ETag), nil
}

// Delete removes keys one by one; a key already gone is not an error.
func (c *CFObjects) Delete(ctx context.Context, keys []string) error {
	for _, k := range keys {
		resp, err := c.do(ctx, http.MethodDelete, c.keyURL(k), nil, "")
		if err != nil {
			return err
		}
		if !ok(resp) {
			err := cfStatusErr(resp, http.MethodDelete, k)
			_ = resp.Body.Close()
			if isNotFound(err) {
				continue
			}
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}
	return nil
}

// List returns every key under prefix, following result_info.cursor while
// is_truncated.
func (c *CFObjects) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	cursor := ""
	for {
		q := url.Values{"prefix": {prefix}, "per_page": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp, err := c.do(ctx, http.MethodGet, c.base+"?"+q.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		if !ok(resp) {
			err := cfStatusErr(resp, "LIST", prefix)
			_ = resp.Body.Close()
			return nil, err
		}
		var page struct {
			restEnvelope
			Result []struct {
				Key string `json:"key"`
			} `json:"result"`
			ResultInfo struct {
				Cursor      string `json:"cursor"`
				IsTruncated bool   `json:"is_truncated"`
			} `json:"result_info"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("r2 rest LIST %s: %w", prefix, err)
		}
		for _, r := range page.Result {
			out = append(out, r.Key)
		}
		if !page.ResultInfo.IsTruncated || page.ResultInfo.Cursor == "" {
			return out, nil
		}
		cursor = page.ResultInfo.Cursor
	}
}
