package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// autoUpdateRetryAfter is how long to wait before trying a remote again after an
// update attempt. An update that failed will most likely fail the same way a
// minute later, and a probe arrives every few seconds.
const autoUpdateRetryAfter = 30 * time.Minute

// maybeAutoUpdateServe updates a remote's serve when it is running an older
// release than this client.
//
// Nearly every fix to the runtime lives in serve and the holder rather than in
// the TUI, so a remote left behind quietly loses them: previews render from an
// old renderer, prompts are delivered by an old code path, activity is never
// published. Noticing that is not something a user should have to do by hand
// when the client already knows both version numbers.
//
// Deliberately narrow:
//
//   - serve only. The trigger daemon runs autonomous work on a schedule, and
//     restarting it unasked could interrupt a run.
//   - running only. A stopped or missing serve is an install decision, not an
//     update.
//   - strictly older only, and only when both sides report a real release. See
//     remotesvc.Outdated for why a dev build or a newer remote is left alone.
//   - once per remote per interval, so a repeatedly failing update does not
//     retry on every probe.
func (m *Model) maybeAutoUpdateServe(d domain.Daemon) tea.Cmd {
	if !m.cfg.AutoUpdateRemotes() {
		return nil
	}
	if d.Kind != string(remotesvc.KindServe) || d.Status != domain.DaemonStatusRunning {
		return nil
	}
	remote, local, outdated := remotesvc.Outdated(d.Version, m.version)
	if !outdated {
		return nil
	}
	if m.serveUpdateAt == nil {
		m.serveUpdateAt = map[string]time.Time{}
	}
	if last, tried := m.serveUpdateAt[d.RemoteHost]; tried && time.Since(last) < autoUpdateRetryAfter {
		return nil
	}
	m.serveUpdateAt[d.RemoteHost] = time.Now()

	m.logPersistent("auto-updating serve on %s: %s -> %s", d.RemoteHost, remote, local)
	return tea.Batch(
		m.showToast("updating agent API on "+d.RemoteHost+" ("+remote.String()+" → "+local.String()+")", false, 8*time.Second),
		remoteServiceOpCmd(m.cfg, d.RemoteHost, remotesvc.KindServe, "update", true),
	)
}
