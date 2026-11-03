package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"rsc.io/qr"
)

// gatewayPairDevice is the device name the pairing code is minted for. The
// phone sends its own name when it redeems the code.
const gatewayPairDevice = "phone"

// gatewayMissingMarker is printed by the pair script when the gateway binary
// is absent, so a missing binary is told apart from a failed pair.
const gatewayMissingMarker = "AIMAN_GATEWAY_MISSING"

const (
	// qrLevel L keeps a pairing URI at 37 modules. A screen is not damaged
	// the way print is, so the extra recovery of M buys nothing here.
	qrLevel = qr.L
	// qrQuiet is the light border in modules on each side. Two modules keep
	// the code inside a 24-row terminal; phone scanners accept it.
	qrQuiet = 2
)

// gatewayScriptRunner runs one script on a remote under remotesvc.OpTimeout.
// Tests replace it.
var gatewayScriptRunner = runRemoteScript

// gatewayPairNow is the clock of the expiry countdown. Tests replace it.
var gatewayPairNow = time.Now

var errGatewayMissing = errors.New("aiman-gateway is not installed")

// gatewayPairResult is the output of `aiman-gateway pair --json`.
type gatewayPairResult struct {
	Target    string    `json:"target"`
	Host      string    `json:"host"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	URI       string    `json:"uri"`
}

// gatewayPairView is the pairing page opened with p on the Agent API page.
// It outlives the page so a result that lands after the user left can be
// reopened with p.
type gatewayPairView struct {
	host    string
	loading bool
	started bool
	result  gatewayPairResult
	qr      []string
	err     string
	// tickQueued is set while a countdown tick is pending, so reopening the
	// page never starts a second tick chain.
	tickQueued bool
}

type gatewayPairMsg struct {
	host    string
	started bool
	result  gatewayPairResult
	err     error
}

type gatewayPairTickMsg struct{}

// parseGatewayPair reads the last JSON object line of the pair output. Errors
// never quote the output, which holds the code.
func parseGatewayPair(out string) (gatewayPairResult, error) {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var res gatewayPairResult
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			return gatewayPairResult{}, errors.New("pair output is not valid JSON")
		}
		if res.Code == "" || res.URI == "" || res.Target == "" || res.ExpiresAt.IsZero() {
			return gatewayPairResult{}, errors.New("pair output lacks target, code, expires_at or uri")
		}
		return res, nil
	}
	return gatewayPairResult{}, errors.New("pair printed no JSON")
}

// gatewayPairScript mints a code. The JSON is printed only on success, so a
// failing pair cannot put a code into an SSH error message.
func gatewayPairScript() string {
	return fmt.Sprintf(`bin="$HOME/.local/bin/aiman-gateway"
if [ ! -x "$bin" ]; then echo %s; exit 0; fi
out=$("$HOME/.local/bin/aiman-gateway" pair --json --name '%s') || exit $?
printf '%%s\n' "$out"
`, gatewayMissingMarker, gatewayPairDevice)
}

// gatewayPairCmd starts the gateway service on host when it is not running,
// then mints a pairing code.
func gatewayPairCmd(cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		msg := gatewayPairMsg{host: host}
		out, err := gatewayScriptRunner(cfg, host, remotesvc.ProbeScript(remotesvc.KindGateway))
		if err != nil {
			msg.err = fmt.Errorf("probe gateway: %w", err)
			return msg
		}
		d := remotesvc.ParseProbe(remotesvc.KindGateway, host, out)
		if d.Version == "missing" {
			msg.err = errGatewayMissing
			return msg
		}
		if d.Status != domain.DaemonStatusRunning {
			if _, err := gatewayScriptRunner(cfg, host, remotesvc.StartScript(remotesvc.KindGateway)); err != nil {
				msg.err = fmt.Errorf("start gateway: %w", err)
				return msg
			}
			msg.started = true
		}
		out, err = gatewayScriptRunner(cfg, host, gatewayPairScript())
		switch {
		case err != nil:
			msg.err = fmt.Errorf("pair failed: %w", err)
		case strings.Contains(out, gatewayMissingMarker):
			msg.err = errGatewayMissing
		default:
			msg.result, msg.err = parseGatewayPair(out)
			if msg.err != nil {
				msg.err = fmt.Errorf("pair failed: %w", msg.err)
			}
		}
		return msg
	}
}

func gatewayPairTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return gatewayPairTickMsg{} })
}

// enterGatewayPair opens the pairing page for the selected remote. An
// unexpired code or a pair still running for that remote is reopened rather
// than minting another.
func (m *Model) enterGatewayPair() tea.Cmd {
	d, ok := m.selectedAgentAPIDaemon()
	if !ok {
		return nil
	}
	if v := m.gatewayPair; v != nil && v.host == d.RemoteHost && v.reusable(gatewayPairNow()) {
		m.state = viewStateGatewayPair
		return m.queueGatewayPairTick()
	}
	m.gatewayPair = &gatewayPairView{host: d.RemoteHost, loading: true}
	m.state = viewStateGatewayPair
	return gatewayPairCmd(m.cfg, d.RemoteHost)
}

func (v *gatewayPairView) reusable(now time.Time) bool {
	if v.loading {
		return true
	}
	return v.err == "" && v.result.Code != "" && now.Before(v.result.ExpiresAt)
}

func (m *Model) queueGatewayPairTick() tea.Cmd {
	v := m.gatewayPair
	if v == nil || v.tickQueued || v.loading || v.result.Code == "" {
		return nil
	}
	v.tickQueued = true
	return gatewayPairTickCmd()
}

// routeGatewayPairMsg handles pair results and ticks on every view: a pair
// started on the Agent API page can finish after the user moved on.
func (m *Model) routeGatewayPairMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case gatewayPairMsg:
		return m.applyGatewayPair(msg)
	case gatewayPairTickMsg:
		v := m.gatewayPair
		if v == nil {
			return m, nil
		}
		v.tickQueued = false
		if m.state != viewStateGatewayPair || !gatewayPairNow().Before(v.result.ExpiresAt) {
			return m, nil
		}
		return m, m.queueGatewayPairTick()
	}
	return m, nil
}

func (m *Model) applyGatewayPair(msg gatewayPairMsg) (tea.Model, tea.Cmd) {
	v := m.gatewayPair
	if v == nil || v.host != msg.host || !v.loading {
		return m, nil
	}
	v.loading = false
	v.started = msg.started
	if msg.err == nil {
		v.qr, msg.err = qrHalfBlock(msg.result.URI)
	}
	if msg.err != nil {
		v.err = gatewayPairError(msg.host, msg.err)
		if m.state != viewStateGatewayPair {
			return m, m.showToast("⚠️  phone pairing on "+msg.host+" failed", true, 6*time.Second)
		}
		return m, nil
	}
	v.result = msg.result
	if m.state != viewStateGatewayPair {
		return m, m.showToast("✓  pairing code ready for "+msg.host+" (p on Agent API)", false, 6*time.Second)
	}
	return m, m.queueGatewayPairTick()
}

func gatewayPairError(host string, err error) string {
	if errors.Is(err, errGatewayMissing) {
		return "aiman-gateway is not installed on " + host + ". Install it with:\n" +
			remotesvc.KindGateway.InstallPipe()
	}
	return err.Error()
}

func (m *Model) handleGatewayPairUpdate(msg tea.Msg) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return
	}
	switch km.String() {
	case "esc", "q":
		m.state = viewStateAgentAPI
	}
}

// gatewayPairExpiry is the countdown line, rounded up to whole seconds so it
// reads 0:01 until the code has actually expired.
func gatewayPairExpiry(now, expires time.Time) string {
	left := expires.Sub(now)
	if left <= 0 {
		return "expired: esc, then p for a new code"
	}
	secs := int((left + time.Second - 1) / time.Second)
	return fmt.Sprintf("expires in %d:%02d", secs/60, secs%60)
}

func (m *Model) renderGatewayPairView() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	badStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	codeStyle := lipgloss.NewStyle().Bold(true)

	v := m.gatewayPair
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Pair a phone") + "\n\n")
	switch {
	case v == nil:
	case v.loading:
		b.WriteString("  Pairing with the gateway on " + v.host + "…\n")
	case v.err != "":
		for _, line := range strings.Split(v.err, "\n") {
			b.WriteString("  " + badStyle.Render(line) + "\n")
		}
	default:
		info := []string{
			"Scan with aiman-phone.",
			"",
			dimStyle.Render("Target"), v.result.Target,
			"",
			dimStyle.Render("Host"), v.result.Host,
			"",
			dimStyle.Render("Code"), codeStyle.Render(v.result.Code),
			"",
			gatewayPairExpiry(gatewayPairNow(), v.result.ExpiresAt),
		}
		if v.started {
			info = append(info, "", dimStyle.Render("Gateway started."))
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, renderPairQR(v.qr), "   ", strings.Join(info, "\n")) + "\n")
	}
	b.WriteString("\n  " + dimStyle.Render("esc back") + "\n")
	return docStyle.Render(b.String())
}

// renderPairQR paints light modules white on black whatever the terminal
// theme, since scanners expect dark modules on a light field.
func renderPairQR(lines []string) string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("0"))
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  " + style.Render(l) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// qrHalfBlock encodes text and draws two module rows per text row. Light
// modules are drawn, dark ones are blank, with a qrQuiet border.
func qrHalfBlock(text string) ([]string, error) {
	code, err := qr.Encode(text, qrLevel)
	if err != nil {
		return nil, fmt.Errorf("encode QR: %w", err)
	}
	width := code.Size + 2*qrQuiet
	light := func(x, y int) bool {
		x -= qrQuiet
		y -= qrQuiet
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return true
		}
		return !code.Black(x, y)
	}
	lines := make([]string, 0, (width+1)/2)
	for y := 0; y < width; y += 2 {
		var row strings.Builder
		for x := range width {
			// light reports the row past an odd-sized grid as quiet zone.
			row.WriteRune(halfBlockRune(light(x, y), light(x, y+1)))
		}
		lines = append(lines, row.String())
	}
	return lines, nil
}

func halfBlockRune(topLight, bottomLight bool) rune {
	switch {
	case topLight && bottomLight:
		return '█'
	case topLight:
		return '▀'
	case bottomLight:
		return '▄'
	default:
		return ' '
	}
}
