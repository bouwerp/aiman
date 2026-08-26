package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

type recordingServeConfigWriter struct {
	commands []string
	path     string
	content  []byte
}

func (w *recordingServeConfigWriter) Execute(_ context.Context, command string) (string, error) {
	w.commands = append(w.commands, command)
	return "", nil
}

func (w *recordingServeConfigWriter) WriteFile(_ context.Context, path string, content []byte) error {
	w.path = path
	w.content = content
	return nil
}

func TestDaemonListHasServeAndTriggerPerRemote(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	items := m.daemonList.Items()
	if len(items) != 4 {
		t.Fatalf("len=%d want 4 (2 remotes × serve+trigger)", len(items))
	}
	first, ok := items[0].(daemonItem)
	if !ok || first.daemon.Kind != string(remotesvc.KindServe) {
		t.Fatalf("first %+v", items[0])
	}
	if first.Title() != "agent API  ·  10.0.1.5" {
		t.Fatalf("title %q", first.Title())
	}
	second := items[1].(daemonItem)
	if second.daemon.Kind != string(remotesvc.KindTrigger) {
		t.Fatalf("second kind %s", second.daemon.Kind)
	}
}

func TestStoreDaemonRoundTrip(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.storeDaemon(domain.Daemon{
		RemoteHost: "10.0.1.5",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusRunning,
		SocketOK:   true,
		Driver:     "systemd",
		Version:    "aiman v0.10.1",
	})
	m.applyRemoteFilter()
	got := m.daemons[domain.DaemonKey("10.0.1.5", string(remotesvc.KindServe))]
	if got.Status != domain.DaemonStatusRunning || !got.SocketOK {
		t.Fatalf("%+v", got)
	}
	if desc := m.daemonList.Items()[0].(daemonItem).Description(); desc != "RUNNING  aiman v0.10.1  socket  systemd" {
		t.Fatalf("desc %q", desc)
	}
}

func TestStoppedServeDescriptionTellsOperatorToInstall(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	desc := m.daemonList.Items()[0].(daemonItem).Description()
	if !strings.Contains(desc, "press i to install") {
		t.Fatalf("desc %q", desc)
	}
}

func TestFailedSystemdServeDescriptionTellsOperatorToRestart(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.storeDaemon(domain.Daemon{
		RemoteHost: "10.0.1.5",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusError,
		Driver:     "systemd",
	})
	m.applyRemoteFilter()
	desc := m.daemonList.Items()[0].(daemonItem).Description()
	if strings.Contains(desc, "press i to install") {
		t.Fatalf("installed unit must not say install: %q", desc)
	}
	if !strings.Contains(desc, "press s to restart") {
		t.Fatalf("desc %q", desc)
	}
}

func TestWriteServeConfigSyncsJiraSettingsWithPrivatePermissions(t *testing.T) {
	writer := &recordingServeConfigWriter{}
	cfg := &config.Config{
		Integrations: config.Integrations{Jira: config.JiraConfig{
			URL:      "https://jira.example.test",
			Email:    "user@example.test",
			APIToken: "test-token",
		}},
		Remotes: []config.Remote{{Host: "remote.example.test", Root: "/repos"}},
	}

	if err := writeServeConfig(context.Background(), cfg, writer); err != nil {
		t.Fatalf("writeServeConfig: %v", err)
	}
	if writer.path != ".aiman/config.yaml" {
		t.Fatalf("path %q", writer.path)
	}
	if !strings.Contains(string(writer.content), "https://jira.example.test") {
		t.Fatalf("Jira settings were not synced: %s", writer.content)
	}
	if strings.Contains(string(writer.content), "remote.example.test") || strings.Contains(string(writer.content), "/repos") {
		t.Fatalf("local remote settings must not be copied: %s", writer.content)
	}
	joined := strings.Join(writer.commands, "\n")
	if !strings.Contains(joined, "install -d -m 700") || !strings.Contains(joined, "chmod 600") {
		t.Fatalf("remote config must be private: %s", joined)
	}
}
