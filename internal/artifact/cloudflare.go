package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CloudflareBase is the Cloudflare REST API v4 root.
const CloudflareBase = "https://api.cloudflare.com/client/v4"

// CloudflareAPI calls the Cloudflare REST API v4 with a bearer token. The
// token is never logged and never part of an error.
type CloudflareAPI struct {
	Base  string // "https://api.cloudflare.com/client/v4"
	Token string // never logged, never in an error
	HTTP  *http.Client
}

// CloudflareError is a failed API call: the HTTP status and Cloudflare's own
// error messages.
type CloudflareError struct {
	Method   string
	Path     string
	Status   int
	Messages []string
}

func (e *CloudflareError) Error() string {
	msg := strings.Join(e.Messages, "; ")
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("cloudflare %s %s: %d %s", e.Method, e.Path, e.Status, msg)
}

// IsCloudflareNotFound reports whether err is a 404 from the API.
func IsCloudflareNotFound(err error) bool {
	var ce *CloudflareError
	return errors.As(err, &ce) && ce.Status == http.StatusNotFound
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfMessage     `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// maxCloudflareReply bounds one API answer.
const maxCloudflareReply = 8 << 20

// call sends body (JSON-encoded unless it is a *multipartBody) and decodes the
// result into out (nil: ignored). path is relative to Base and may carry a
// query.
func (c *CloudflareAPI) call(ctx context.Context, method, path string, body, out any) error {
	var (
		rd    io.Reader
		ctype string
	)
	switch b := body.(type) {
	case nil:
	case *multipartBody:
		rd, ctype = bytes.NewReader(b.data), b.contentType
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(raw), "application/json"
	}
	base := c.Base
	if base == "" {
		base = CloudflareBase
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	shown := path
	if i := strings.IndexByte(shown, '?'); i >= 0 {
		shown = shown[:i]
	}
	resp, err := hc.Do(req)
	if err != nil {
		// net/http errors carry the URL, never the header.
		return fmt.Errorf("cloudflare %s %s: %w", method, shown, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudflareReply))
	if err != nil {
		return fmt.Errorf("cloudflare %s %s: %w", method, shown, err)
	}
	var env cfEnvelope
	jsonErr := json.Unmarshal(raw, &env)
	if resp.StatusCode/100 != 2 || jsonErr != nil || !env.Success {
		ce := &CloudflareError{Method: method, Path: shown, Status: resp.StatusCode}
		for _, m := range env.Errors {
			ce.Messages = append(ce.Messages, fmt.Sprintf("%s (%d)", m.Message, m.Code))
		}
		if len(ce.Messages) == 0 && jsonErr != nil {
			ce.Messages = append(ce.Messages, "answer is not JSON")
		}
		c.scrub(ce)
		return ce
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("cloudflare %s %s: decode result: %w", method, shown, err)
		}
	}
	return nil
}

// scrub removes the token from messages, in case an API echoes it.
func (c *CloudflareAPI) scrub(ce *CloudflareError) {
	if c.Token == "" {
		return
	}
	for i, m := range ce.Messages {
		ce.Messages[i] = strings.ReplaceAll(m, c.Token, "[redacted]")
	}
}

type multipartBody struct {
	data        []byte
	contentType string
}
