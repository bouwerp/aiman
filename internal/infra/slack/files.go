package slack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// MaxFileRead caps a FileRead download in bytes.
const MaxFileRead = 10 << 20

// File is a Slack file's metadata from files.info.
type File struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Mimetype   string `json:"mimetype"`
	Size       int64  `json:"size"`
	URLPrivate string `json:"url_private"`
}

// UploadRequest is one file to share. Channel is a channel id; Thread, Comment
// and Title are optional.
type UploadRequest struct {
	Filename string
	Data     []byte
	Channel  string
	Thread   string
	Comment  string
	Title    string
}

// FileInfo returns a file's metadata.
func (c *Client) FileInfo(ctx context.Context, id string) (File, error) {
	var out struct {
		File File `json:"file"`
	}
	err := c.get(ctx, "files.info", url.Values{"file": {id}}, &out)
	return out.File, err
}

// FileRead looks up a file and writes its bytes to w, downloaded from
// url_private with the user token. A file over MaxFileRead is refused and
// nothing is written.
func (c *Client) FileRead(ctx context.Context, id string, w io.Writer) (File, error) {
	f, err := c.FileInfo(ctx, id)
	if err != nil {
		return File{}, err
	}
	if f.URLPrivate == "" {
		return f, fmt.Errorf("slack file %s has no url_private", id)
	}
	if f.Size > MaxFileRead {
		return f, fmt.Errorf("slack file %s is %d bytes, over the %d byte limit", id, f.Size, MaxFileRead)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URLPrivate, nil)
	if err != nil {
		return f, fmt.Errorf("slack file %s: %w", id, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return f, fmt.Errorf("slack file %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return f, fmt.Errorf("slack file %s: download http %d", id, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFileRead+1))
	if err != nil {
		return f, fmt.Errorf("slack file %s: %w", id, err)
	}
	if len(data) > MaxFileRead {
		return f, fmt.Errorf("slack file %s is over the %d byte limit", id, MaxFileRead)
	}
	if _, err := w.Write(data); err != nil {
		return f, err
	}
	return f, nil
}

// Upload shares r's bytes through Slack's external upload and returns the
// file id: files.getUploadURLExternal, a POST of the bytes to the returned
// URL, then files.completeUploadExternal. The token goes only to the API.
func (c *Client) Upload(ctx context.Context, r UploadRequest) (string, error) {
	if r.Filename == "" {
		return "", fmt.Errorf("slack upload needs a filename")
	}
	if len(r.Data) == 0 {
		return "", fmt.Errorf("slack upload of %s: file is empty", r.Filename)
	}
	var got struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	v := url.Values{"filename": {r.Filename}, "length": {strconv.Itoa(len(r.Data))}}
	if err := c.callForm(ctx, "files.getUploadURLExternal", v, &got); err != nil {
		return "", err
	}
	if got.UploadURL == "" || got.FileID == "" {
		return "", fmt.Errorf("slack files.getUploadURLExternal: no upload_url or file_id")
	}
	if err := c.putBytes(ctx, got.UploadURL, r.Data); err != nil {
		return "", err
	}
	entry := map[string]string{"id": got.FileID}
	if r.Title != "" {
		entry["title"] = r.Title
	}
	req := map[string]any{"files": []map[string]string{entry}}
	if r.Channel != "" {
		req["channel_id"] = r.Channel
	}
	if r.Thread != "" {
		req["thread_ts"] = r.Thread
	}
	if r.Comment != "" {
		req["initial_comment"] = r.Comment
	}
	var done struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := c.call(ctx, "files.completeUploadExternal", req, &done); err != nil {
		return "", err
	}
	if len(done.Files) > 0 && done.Files[0].ID != "" {
		return done.Files[0].ID, nil
	}
	return got.FileID, nil
}

// putBytes POSTs data to Slack's upload URL, which needs no token.
func (c *Client) putBytes(ctx context.Context, target string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("slack upload: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("slack upload: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack upload: http %d", resp.StatusCode)
	}
	return nil
}
