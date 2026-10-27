package notify

import (
	"context"
	"strings"
	"sync"

	"github.com/bouwerp/aiman/internal/domain"
	slackclient "github.com/bouwerp/aiman/internal/infra/slack"
)

// SlackSink posts notifications to a Slack channel as the token owner.
type SlackSink struct {
	Client  *slackclient.Client
	Channel string

	mu    sync.RWMutex
	cid   string
	resol error
}

func (s *SlackSink) Name() string { return "slack" }

func slackEscape(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func (s *SlackSink) resolve(ctx context.Context) (string, error) {
	s.mu.RLock()
	cid := s.cid
	err := s.resol
	s.mu.RUnlock()
	if cid != "" {
		return cid, nil
	}
	if err != nil && s.cid == "" {
		// allow retry on next send if resolution failed before? tests say "retried on the next send if resolution failed"
		// but easier to just try again
	}

	// try resolve
	cid2, err2 := s.Client.ResolveChannel(ctx, s.Channel)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err2 != nil {
		s.resol = err2
		return "", err2
	}
	s.cid = cid2
	s.resol = nil
	return s.cid, nil
}

func (s *SlackSink) Send(ctx context.Context, n domain.Notification) error {
	ch, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	text := slackEscape(n.Text)
	_, err = s.Client.Post(ctx, slackclient.Post{Channel: ch, Text: text})
	return err
}
