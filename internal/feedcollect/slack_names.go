package feedcollect

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// SlackUsers looks up a member by id (users.info). A SlackAPI that also
// implements it gives events the sender's name and turns <@U…> mentions in
// bodies into @Name.
type SlackUsers interface {
	User(ctx context.Context, id string) (slack.User, error)
}

const (
	slackMethodUsers = "users"
	// slackNameKeep is how long a looked-up name is used before it is read
	// again; slackNameRetry is how long a failed lookup waits.
	slackNameKeep  = 24 * time.Hour
	slackNameRetry = 10 * time.Minute
)

type slackName struct {
	name string
	at   time.Time
}

var slackMentionRe = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]*)?>`)

// isSlackUserID reports whether id names a member (U… or W…), not a bot.
func isSlackUserID(id string) bool {
	return len(id) > 1 && (id[0] == 'U' || id[0] == 'W')
}

// names sets each event's ActorName and rewrites member mentions in its body
// and title, looking up ids it has not cached inside the users budget. An id
// out of budget or failing stays as it is.
func (s *slackSource) names(p *slackPoll) error {
	users, ok := s.o.API.(SlackUsers)
	if !ok {
		return nil
	}
	if s.userNames == nil {
		s.userNames = map[string]slackName{}
	}
	for i := range p.events {
		ev := &p.events[i]
		if isSlackUserID(ev.Actor) {
			name, err := s.userName(p, users, ev.Actor)
			if err != nil {
				return err
			}
			ev.ActorName = name
		}
		for _, field := range []*string{&ev.Body, &ev.Title} {
			var ferr error
			*field = slackMentionRe.ReplaceAllStringFunc(*field, func(m string) string {
				id := slackMentionRe.FindStringSubmatch(m)[1]
				name, err := s.userName(p, users, id)
				if err != nil && ferr == nil {
					ferr = err
				}
				if name == "" {
					return m
				}
				return "@" + name
			})
			if ferr != nil {
				return ferr
			}
		}
	}
	return nil
}

// userName answers id's name from the cache, else from users.info while the
// budget lasts. Only a context error is returned: any other failure is
// remembered for slackNameRetry and answers "".
func (s *slackSource) userName(p *slackPoll, users SlackUsers, id string) (string, error) {
	now := s.o.Now()
	if c, ok := s.userNames[id]; ok {
		keep := slackNameKeep
		if c.name == "" {
			keep = slackNameRetry
		}
		if now.Sub(c.at) < keep {
			return c.name, nil
		}
	}
	var u slack.User
	ok, err := s.do(p, slackMethodUsers, func() error {
		var err error
		u, err = users.User(p.ctx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		s.userNames[id] = slackName{at: now}
		return "", nil
	}
	if !ok {
		return "", nil // out of budget: the next poll tries again
	}
	name := strings.TrimSpace(u.RealName)
	if name == "" {
		name = strings.TrimSpace(u.Name)
	}
	s.userNames[id] = slackName{name: oneLine(name), at: now}
	return s.userNames[id].name, nil
}
