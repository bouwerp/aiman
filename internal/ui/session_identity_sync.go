package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

// identitySyncTimeout bounds every serve call of one sync command.
const identitySyncTimeout = 45 * time.Second

// sessionIdentityRPC calls one session.* method on a remote's agent API.
// Tests replace it.
var sessionIdentityRPC = func(ctx context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
	return usecase.ServeRPC(ctx, dialRemoteSSH(r), method, params)
}

// identityChange is one session.move or session.rename call.
type identityChange struct {
	method string // session.move or session.rename
	id     string
	value  string // the group for a move, the name for a rename
}

func (c identityChange) params() map[string]string {
	if c.method == "session.rename" {
		return map[string]string{"id": c.id, "name": c.value}
	}
	return map[string]string{"id": c.id, "group": c.value}
}

// sessionIdentitySyncMsg reports serve's answers to a group move or rename.
// failed holds one line per call that did not succeed.
type sessionIdentitySyncMsg struct {
	host   string
	failed []string
}

// syncIdentityToServe sends group and name changes to the serve on host.
// Serve's row is what the event stream replays on the next start, so a change
// made only in the laptop DB would be undone. Placeholder and empty ids, and
// hosts with no configured remote, are skipped.
func (m *Model) syncIdentityToServe(host string, changes []identityChange) tea.Cmd {
	remote, ok := remoteForHost(m.cfg, host)
	if !ok {
		return nil
	}
	var todo []identityChange
	for _, c := range changes {
		if c.id == "" || domain.IsEphemeralSessionID(c.id) {
			continue
		}
		todo = append(todo, c)
	}
	if len(todo) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), identitySyncTimeout)
		defer cancel()
		msg := sessionIdentitySyncMsg{host: host}
		for _, c := range todo {
			_, err := sessionIdentityRPC(ctx, remote, c.method, c.params())
			if err == nil || errors.Is(err, usecase.ErrRPCUnsupported) || serveLacksSession(err) {
				continue
			}
			msg.failed = append(msg.failed, fmt.Sprintf("%s %s: %v", c.method, c.id, err))
		}
		return msg
	}
}

// serveLacksSession reports serve answering not_found: it holds no row for
// the session, so it has nothing to replay over the laptop's change.
func serveLacksSession(err error) bool {
	var rpcErr *usecase.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == "not_found"
}

func (m *Model) applyIdentitySync(msg sessionIdentitySyncMsg) tea.Cmd {
	if len(msg.failed) == 0 {
		return nil
	}
	for _, line := range msg.failed {
		m.log("serve on %s: %s", msg.host, line)
	}
	text := fmt.Sprintf("Serve on %s did not save the change; it may revert on restart: %s",
		remoteNameForHost(m.cfg, msg.host), strings.Join(msg.failed, "; "))
	return m.showToast(text, true, 6*time.Second)
}
