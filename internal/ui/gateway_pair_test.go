package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/infra/config"
	"rsc.io/qr"
)

const samplePairURI = "aimanphone://pair?target=aiman-gateway-devbox:8443&host=devbox&code=ABCDEFGHJK"

const samplePairJSON = `{"target":"aiman-gateway-devbox:8443","host":"devbox","code":"ABCDEFGHJK","expires_at":"2026-11-03T10:05:00Z","uri":"` + samplePairURI + `"}`

func TestParseGatewayPair(t *testing.T) {
	got, err := parseGatewayPair("Warning: noise from the login shell\n" + samplePairJSON + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := gatewayPairResult{
		Target:    "aiman-gateway-devbox:8443",
		Host:      "devbox",
		Code:      "ABCDEFGHJK",
		ExpiresAt: time.Date(2026, 11, 3, 10, 5, 0, 0, time.UTC),
		URI:       samplePairURI,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGatewayPairRejects(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"not json":    "pair: something broke",
		"no uri":      `{"target":"t","host":"h","code":"ABCDEFGHJK","expires_at":"2026-11-03T10:05:00Z"}`,
		"no code":     `{"target":"t","host":"h","expires_at":"2026-11-03T10:05:00Z","uri":"aimanphone://pair"}`,
		"bad expires": `{"target":"t","host":"h","code":"ABCDEFGHJK","expires_at":"soon","uri":"aimanphone://pair"}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseGatewayPair(out)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "ABCDEFGHJK") {
				t.Fatalf("error leaks the code: %v", err)
			}
		})
	}
}

func TestQRHalfBlockSizeIsStable(t *testing.T) {
	lines, err := qrHalfBlock(samplePairURI)
	if err != nil {
		t.Fatalf("qr: %v", err)
	}
	code, err := qr.Encode(samplePairURI, qrLevel)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	width := code.Size + 2*qrQuiet
	if len(lines) != (width+1)/2 {
		t.Fatalf("got %d lines, want %d", len(lines), (width+1)/2)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n != width {
			t.Fatalf("line %d has %d cells, want %d", i, n, width)
		}
	}
	// Small enough for a normal 80x24 terminal beside the text.
	if width > 60 || len(lines) > 30 {
		t.Fatalf("QR too large: %dx%d", width, len(lines))
	}
	again, _ := qrHalfBlock(samplePairURI)
	if strings.Join(again, "\n") != strings.Join(lines, "\n") {
		t.Fatal("render is not deterministic")
	}
}

// TestQRHalfBlockMatchesBitmap decodes the half blocks back into modules and
// compares them with the encoder's bitmap, so a swapped top/bottom glyph
// cannot pass.
func TestQRHalfBlockMatchesBitmap(t *testing.T) {
	lines, err := qrHalfBlock(samplePairURI)
	if err != nil {
		t.Fatalf("qr: %v", err)
	}
	code, _ := qr.Encode(samplePairURI, qrLevel)
	dark := func(x, y int) bool {
		x -= qrQuiet
		y -= qrQuiet
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return false
		}
		return code.Black(x, y)
	}
	for row, l := range lines {
		for x, r := range []rune(l) {
			top, bottom := !dark(x, 2*row), !dark(x, 2*row+1)
			if got := halfBlockRune(top, bottom); got != r {
				t.Fatalf("cell (%d,%d) = %q, want %q", x, row, r, got)
			}
		}
	}
}

type pairCall struct{ host, script string }

// fakePairRunner answers the probe, start and pair scripts by content.
func fakePairRunner(t *testing.T, active, pairOut string, pairErr error) *[]pairCall {
	t.Helper()
	var calls []pairCall
	prev := gatewayScriptRunner
	gatewayScriptRunner = func(_ *config.Config, host, script string) (string, error) {
		calls = append(calls, pairCall{host: host, script: script})
		switch {
		case strings.Contains(script, "---LOGS---"):
			ver := "aiman-gateway v0.24.2"
			if active == "missing" {
				return "DRIVER=none\nACTIVE=inactive\nVERSION=missing\n---LOGS---\n", nil
			}
			return "DRIVER=systemd\nACTIVE=" + active + "\nVERSION=" + ver + "\n---LOGS---\n", nil
		case strings.Contains(script, "pair --json"):
			return pairOut, pairErr
		default:
			return "", nil
		}
	}
	t.Cleanup(func() { gatewayScriptRunner = prev })
	return &calls
}

func pairPage(t *testing.T) (*Model, tea.Cmd) {
	t.Helper()
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	_, cmd := m.handleAgentAPIUpdate(pressRune('p'))
	if m.state != viewStateGatewayPair || cmd == nil {
		t.Fatalf("p: state %v cmd %v", m.state, cmd)
	}
	return m, cmd
}

func withPairClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := gatewayPairNow
	gatewayPairNow = func() time.Time { return now }
	t.Cleanup(func() { gatewayPairNow = prev })
}

func TestAgentAPIKeyPPairsRunningGateway(t *testing.T) {
	calls := fakePairRunner(t, "active", samplePairJSON, nil)
	withPairClock(t, time.Date(2026, 11, 3, 10, 2, 30, 0, time.UTC))
	m, cmd := pairPage(t)
	if out := m.renderGatewayPairView(); !strings.Contains(out, "Pairing") {
		t.Fatalf("loading view:\n%s", out)
	}
	m.routeRemoteServiceMsg(cmd())

	if len(*calls) != 2 {
		t.Fatalf("want probe then pair, got %d calls", len(*calls))
	}
	if (*calls)[0].host != "10.0.1.5" || !strings.Contains((*calls)[1].script, `"$HOME/.local/bin/aiman-gateway" pair --json --name`) {
		t.Fatalf("calls %+v", *calls)
	}
	out := m.renderGatewayPairView()
	for _, want := range []string{"aiman-gateway-devbox:8443", "devbox", "ABCDEFGHJK", "expires in 2:30", "▀"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	m.handleGatewayPairUpdate(pressKey("esc"))
	if m.state != viewStateAgentAPI {
		t.Fatalf("esc: state %v", m.state)
	}
}

func TestAgentAPIKeyPStartsStoppedGateway(t *testing.T) {
	calls := fakePairRunner(t, "inactive", samplePairJSON, nil)
	m, cmd := pairPage(t)
	m.routeRemoteServiceMsg(cmd())
	if len(*calls) != 3 {
		t.Fatalf("want probe, start, pair; got %d calls", len(*calls))
	}
	if !strings.Contains((*calls)[1].script, "systemctl --user start aiman-gateway.service") {
		t.Fatalf("second call is not the gateway start script:\n%s", (*calls)[1].script)
	}
	if m.gatewayPair.err != "" || m.gatewayPair.result.Code == "" {
		t.Fatalf("view %+v", m.gatewayPair)
	}
}

func TestAgentAPIKeyPReportsMissingGateway(t *testing.T) {
	calls := fakePairRunner(t, "missing", "", nil)
	m, cmd := pairPage(t)
	m.routeRemoteServiceMsg(cmd())
	if len(*calls) != 1 {
		t.Fatalf("want only the probe, got %d calls", len(*calls))
	}
	if out := m.renderGatewayPairView(); !strings.Contains(out, "aiman-gateway is not installed on 10.0.1.5") {
		t.Fatalf("view:\n%s", out)
	}
}

func TestAgentAPIKeyPReportsPairFailure(t *testing.T) {
	fakePairRunner(t, "active", "", errors.New("exit status 1"))
	m, cmd := pairPage(t)
	m.routeRemoteServiceMsg(cmd())
	if out := m.renderGatewayPairView(); !strings.Contains(out, "pair failed") {
		t.Fatalf("view:\n%s", out)
	}
}

func TestGatewayPairResultSurvivesLeavingThePage(t *testing.T) {
	calls := fakePairRunner(t, "active", samplePairJSON, nil)
	withPairClock(t, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	m, cmd := pairPage(t)
	m.handleGatewayPairUpdate(pressKey("esc"))
	m.state = viewStateMain
	m.routeRemoteServiceMsg(cmd())
	if m.state != viewStateMain {
		t.Fatalf("result moved the view to %v", m.state)
	}
	m.state = viewStateAgentAPI
	before := len(*calls)
	m.handleAgentAPIUpdate(pressRune('p'))
	if m.gatewayPair.loading || len(*calls) != before {
		t.Fatal("p re-paired although an unexpired code is waiting")
	}
	if m.state != viewStateGatewayPair || !strings.Contains(m.renderGatewayPairView(), "ABCDEFGHJK") {
		t.Fatalf("p did not reopen the code: state %v", m.state)
	}
}

func TestGatewayPairExpiryLine(t *testing.T) {
	exp := time.Date(2026, 11, 3, 10, 5, 0, 0, time.UTC)
	cases := []struct {
		now  time.Time
		want string
	}{
		{exp.Add(-5 * time.Minute), "expires in 5:00"},
		{exp.Add(-61 * time.Second), "expires in 1:01"},
		{exp.Add(-500 * time.Millisecond), "expires in 0:01"},
		{exp, "expired"},
		{exp.Add(time.Minute), "expired"},
	}
	for _, c := range cases {
		if got := gatewayPairExpiry(c.now, exp); !strings.HasPrefix(got, c.want) {
			t.Errorf("at %v: got %q, want prefix %q", c.now, got, c.want)
		}
	}
}

func TestAgentAPIHelpLineListsPair(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	if !strings.Contains(m.renderAgentAPIView(), "p pair phone") {
		t.Fatal("help line does not list p")
	}
}
