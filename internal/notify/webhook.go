package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// WebhookSink posts notification text as plain text to a webhook URL.
type WebhookSink struct {
	URL    string
	HTTP   *http.Client
	client *http.Client
}

func (w *WebhookSink) Name() string { return "webhook" }

func (w *WebhookSink) httpClient() *http.Client {
	if w.client != nil {
		return w.client
	}
	if w.HTTP != nil {
		return w.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (w *WebhookSink) Send(ctx context.Context, n domain.Notification) error {
	body := strings.NewReader(n.Text)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", "aiman: "+domain.EventLabel(n.Kind))
	req.Header.Set("Tags", string(n.Kind))
	req.Header.Set("X-Aiman-Session", n.SessionID)
	req.Header.Set("X-Aiman-Kind", string(n.Kind))
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}
