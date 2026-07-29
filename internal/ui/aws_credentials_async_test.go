package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func busyCredsModel() AWSCredentialsModel {
	del := &config.AWSDelegation{Profile: "prod", SourceProfile: "prod", SyncCredentials: true}
	m := NewAWSCredentialsModel(&config.Config{}, nil)
	m.entries = []awsHostEntry{
		{key: "k1", userAtHost: "ubuntu@worker", remoteProfile: "prod", status: awsCredStatusChecking, del: del},
		{key: "k2", userAtHost: "ubuntu@worker", remoteProfile: "dev", status: awsCredStatusValid, del: del},
	}
	m.renewing = map[string]bool{"k1": true}
	return m
}

func TestAWSCredentialsBusy(t *testing.T) {
	if !busyCredsModel().Busy() {
		t.Fatal("a model with a renewal in flight must report busy")
	}

	checking := NewAWSCredentialsModel(&config.Config{}, nil)
	checking.entries = []awsHostEntry{{key: "k1", status: awsCredStatusChecking}}
	if !checking.Busy() {
		t.Fatal("a model with an outstanding check must report busy")
	}

	idle := NewAWSCredentialsModel(&config.Config{}, nil)
	idle.entries = []awsHostEntry{{key: "k1", status: awsCredStatusValid}}
	if idle.Busy() {
		t.Fatal("a settled model must not report busy")
	}

	if NewAWSCredentialsModel(&config.Config{}, nil).Busy() {
		t.Fatal("an empty model must not report busy")
	}
}

func TestAWSCredentialsViewShowsInFlightWork(t *testing.T) {
	view := busyCredsModel().View()
	if !strings.Contains(view, "in flight") {
		t.Fatalf("a busy model must say work is in flight, got:\n%s", view)
	}
	if !strings.Contains(view, "background") {
		t.Fatalf("a busy model must say the work continues in the background, got:\n%s", view)
	}

	idle := NewAWSCredentialsModel(&config.Config{}, nil)
	idle.entries = []awsHostEntry{{key: "k1", status: awsCredStatusValid}}
	if strings.Contains(idle.View(), "in flight") {
		t.Fatal("a settled model must not claim work is in flight")
	}
}

func TestAWSCredentialsViewShowsExternalRefresh(t *testing.T) {
	m := NewAWSCredentialsModel(&config.Config{}, nil)
	m.entries = []awsHostEntry{{key: "k1", status: awsCredStatusValid}}
	m.externalRefresh = true
	if !strings.Contains(m.View(), "shift+R") {
		t.Fatalf("a dashboard-triggered refresh must be visible on this page, got:\n%s", m.View())
	}
}

// Renewals continue while the user is on another screen: the results must still reach
// the credentials model rather than being dropped by the view-state dispatch.
func TestAWSCredResultRoutedWhileOffPage(t *testing.T) {
	m := &Model{state: viewStateMain, awsCredentials: busyCredsModel()}

	updated, _ := m.Update(awsCredRenewResultMsg{key: "k1"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if model.state != viewStateMain {
		t.Fatalf("routing a credential message must not change the view, got %v", model.state)
	}
	if model.awsCredentials.renewing["k1"] {
		t.Fatal("the renewal should have been marked finished even though the page was not open")
	}
}

func TestAWSCredCheckResultRoutedWhileOffPage(t *testing.T) {
	m := &Model{state: viewStateMain, awsCredentials: busyCredsModel()}

	updated, _ := m.Update(awsCredCheckResultMsg{key: "k1", status: awsCredStatusValid})
	model := updated.(*Model)
	if got := model.awsCredentials.entries[0].status; got != awsCredStatusValid {
		t.Fatalf("check result did not reach the model off-page: status %v", got)
	}
}

func TestEnterAWSCredentialsPreservesInFlightWork(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateMenu, awsCredentials: busyCredsModel()}

	cmd := m.enterAWSCredentials()
	if m.state != viewStateAWSCredentials {
		t.Fatalf("expected to be on the credentials page, got %v", m.state)
	}
	if len(m.awsCredentials.entries) != 2 || !m.awsCredentials.renewing["k1"] {
		t.Fatal("re-entering the page must not discard in-flight work")
	}
	if cmd != nil {
		t.Fatal("must not kick off a fresh scan while work is in flight")
	}
}

func TestEnterAWSCredentialsRescansWhenIdle(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateMenu}

	cmd := m.enterAWSCredentials()
	if m.state != viewStateAWSCredentials {
		t.Fatalf("expected to be on the credentials page, got %v", m.state)
	}
	if cmd == nil {
		t.Fatal("an idle page must start a fresh scan on entry")
	}
}

func TestEnterAWSCredentialsSurfacesDashboardRefresh(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateMenu, awsCredRefreshing: true}
	m.enterAWSCredentials()
	if !m.awsCredentials.externalRefresh {
		t.Fatal("an in-flight dashboard refresh must be shown on the credentials page")
	}
}

func TestAWSCredentialsRefreshingExcludesPlainChecks(t *testing.T) {
	checking := NewAWSCredentialsModel(&config.Config{}, nil)
	checking.entries = []awsHostEntry{{key: "k1", status: awsCredStatusChecking}}
	if !checking.Busy() {
		t.Fatal("an outstanding check is still busy work")
	}
	if checking.Refreshing() {
		t.Fatal("a routine check must not be reported as a refresh")
	}
	if !busyCredsModel().Refreshing() {
		t.Fatal("a renewal in flight must be reported as a refresh")
	}
}

func TestDashboardBannerShowsRefreshWithNoExpiryWarning(t *testing.T) {
	m := &Model{awsCredentials: busyCredsModel()}
	got := m.renderAWSCredExpiryBanner()
	if !strings.Contains(got, "Refreshing") || !strings.Contains(got, "background") {
		t.Fatalf("expected an in-progress banner while a page-started refresh runs, got %q", got)
	}
}

func TestDashboardBannerSilentWhenIdleAndHealthy(t *testing.T) {
	m := &Model{}
	if got := m.renderAWSCredExpiryBanner(); got != "" {
		t.Fatalf("expected no banner when idle with nothing near expiry, got %q", got)
	}
}

// A refresh that finishes while the user is elsewhere should say so, since the page they
// started it from is not on screen to show the result.
func TestOffPageRefreshCompletionIsAnnounced(t *testing.T) {
	m := &Model{state: viewStateMain, cfg: &config.Config{}, awsCredentials: busyCredsModel()}

	updated, cmd := m.Update(awsCredRenewResultMsg{key: "k1"})
	model := updated.(*Model)
	if cmd == nil {
		t.Fatal("expected commands (toast + expiry re-poll) on completion")
	}
	if !strings.Contains(model.snapshotToast, "refresh") {
		t.Fatalf("expected a completion toast, got %q", model.snapshotToast)
	}
	if model.snapshotToastError {
		t.Fatalf("a successful refresh must not toast as an error: %q", model.snapshotToast)
	}
}

func TestOffPageRefreshFailureIsAnnouncedAsError(t *testing.T) {
	m := &Model{state: viewStateMain, cfg: &config.Config{}, awsCredentials: busyCredsModel()}

	updated, _ := m.Update(awsCredRenewResultMsg{key: "k1", err: context.DeadlineExceeded})
	model := updated.(*Model)
	if !model.snapshotToastError {
		t.Fatalf("a failed refresh must toast as an error, got %q", model.snapshotToast)
	}
}

func TestOnPageRefreshCompletionDoesNotToast(t *testing.T) {
	m := &Model{state: viewStateAWSCredentials, cfg: &config.Config{}, awsCredentials: busyCredsModel()}

	updated, _ := m.Update(awsCredRenewResultMsg{key: "k1"})
	if got := updated.(*Model).snapshotToast; got != "" {
		t.Fatalf("the page itself shows the result; no toast expected, got %q", got)
	}
}

func TestRefreshFailureCounterResetsPerWave(t *testing.T) {
	m := busyCredsModel()
	m.refreshFailures = 3

	// Starting a new wave with shift+R clears the previous wave's tally.
	m.renewing = map[string]bool{}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if got := updated.(AWSCredentialsModel).refreshFailures; got != 0 {
		t.Fatalf("expected the failure tally to reset when a refresh starts, got %d", got)
	}
}
