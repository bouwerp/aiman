package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/review"
)

type reviewDebugParams struct {
	Session string `json:"session"`
	Lang    string `json:"lang"`
	Target  string `json:"target"`
}

// handleReviewDebug starts a long-running debug process in the review sandbox
// and records the host-published ports as the session's tunnels.
func (s *Server) handleReviewDebug(ctx context.Context, req Request) Response {
	var p reviewDebugParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	lang := strings.ToLower(strings.TrimSpace(p.Lang))
	if lang != "go" && lang != "node" && lang != "react" {
		return errResp(req.ID, CodeInvalidParams, "lang must be go, node or react")
	}
	inner, err := review.SandboxDebugCommand(lang, p.Target)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	raw, _ := json.Marshal(reviewCall{Session: p.Session})
	req.Params = raw
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	if s.reviewRunner.Docker == nil {
		return errResp(req.ID, CodeInvalidParams, "review sandbox not configured")
	}
	if isNilPTY(s.pty) {
		return errResp(req.ID, CodeInvalidParams, "pty runtime is not configured")
	}
	sb, err := s.sandboxFor(t.sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	ports, err := probeDebugPorts(lang)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	sb.Ports = review.PortMaps(lang, ports)
	composePath, err := s.reviewRunner.WriteCompose(sb)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	order := s.readingOrder(t.sess.ID)
	if wt := strings.TrimSpace(t.sess.WorktreePath); wt != "" {
		if err := review.WriteLaunchJSON(wt, lang, ports, order); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	if err := s.startDebugPTY(t.sess, sb, composePath, inner); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	t.sess.Tunnels = debugTunnels(lang, ports)
	if s.repo != nil {
		if err := s.repo.Save(ctx, &t.sess); err != nil {
			s.stopDebugPTY(t.sess.ID)
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "review_debug", "session": sessionInfo(t.sess, req.Caller),
	}}
}

func (s *Server) startDebugPTY(sess domain.Session, sb review.Sandbox, composePath, inner string) error {
	cmd := "docker compose -f " + quoteArg(composePath) +
		" run --rm --service-ports sandbox sh -c " + quoteArg(inner)
	id := debugPTYID(sess.ID)
	_ = s.pty.Kill(id)
	_ = s.pty.Forget(id)
	_, err := s.pty.Create(ptyruntime.Spec{
		ID:       id,
		Name:     "debug",
		Dir:      sb.Dir,
		Command:  cmd,
		ParentID: sess.ID,
	})
	return err
}

func debugPTYID(sessionID string) string {
	return "debug-" + sessionID
}

func (s *Server) stopDebugPTY(sessionID string) {
	if s.pty == nil {
		return
	}
	id := debugPTYID(sessionID)
	if _, err := s.pty.Get(id); err != nil {
		return
	}
	_ = s.pty.Kill(id)
	_ = s.pty.Forget(id)
}

func (s *Server) readingOrder(id string) []string {
	path := filepath.Join(s.reviewScratch(id), reviewFilesDir, "reading-order.txt")
	raw, err := os.ReadFile(path) //nolint:gosec // path is under the review scratch
	if err != nil {
		return nil
	}
	return review.ReadingOrder(raw)
}

func debugTunnels(lang string, ports review.DebugPorts) []domain.Tunnel {
	out := []domain.Tunnel{{LocalPort: ports.Debug, RemotePort: ports.Debug}}
	if lang == "react" {
		out = append(out,
			domain.Tunnel{LocalPort: ports.App, RemotePort: ports.App},
			domain.Tunnel{LocalPort: ports.DevTools, RemotePort: ports.DevTools},
		)
	}
	return out
}

func probeDebugPorts(lang string) (review.DebugPorts, error) {
	debug, err := freeLocalPort()
	if err != nil {
		return review.DebugPorts{}, err
	}
	p := review.DebugPorts{Debug: debug}
	if lang != "react" {
		return p, nil
	}
	app, err := freeLocalPort()
	if err != nil {
		return review.DebugPorts{}, err
	}
	dt, err := freeLocalPortPreferred(reviewDevToolsHost)
	if err != nil {
		return review.DebugPorts{}, err
	}
	p.App, p.DevTools = app, dt
	return p, nil
}

const reviewDevToolsHost = 8097

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("free port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func freeLocalPortPreferred(port int) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return freeLocalPort()
	}
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}
