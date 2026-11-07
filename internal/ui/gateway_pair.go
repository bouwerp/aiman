package ui

import (
	"context"
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

// gatewayPairTimeoutMarker is printed by the pair script when the pair command
// outlives gatewayPairTimeoutSeconds.
const gatewayPairTimeoutMarker = "AIMAN_GATEWAY_PAIR_TIMEOUT"

// gatewayPairTimeoutSeconds bounds the pair command. A real pair only writes a
// file; a binary that does not know `pair` starts a server and never returns.
const gatewayPairTimeoutSeconds = 30

// gatewayPairMinVersion is the first release with the `pair` subcommand.
var gatewayPairMinVersion = remotesvc.Version{Major: 0, Minor: 25, Patch: 0}

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

var (
	errGatewayMissing     = errors.New("aiman-gateway is not installed")
	errGatewayPairTimeout = errors.New("aiman-gateway pair did not answer; update the gateway (u on its Daemons row)")
)

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
	// updatedTo is the release the gateway was updated to before pairing, or
	// empty when it was already current.
	updatedTo string
	result    gatewayPairResult
	qr        []string
	err       string
	// tickQueued is set while a countdown tick is pending, so reopening the
	// page never starts a second tick chain.
	tickQueued bool
}

type gatewayPairMsg struct {
	host      string
	started   bool
	updatedTo string
	result    gatewayPairResult
	err       error
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
// failing pair cannot put a code into an SSH error message. timeout reports 124,
// or 137 after -k; busybox's reports the 143 of its SIGTERM. A host without
// timeout runs the pair unbounded, with the version gate as its only guard.
func gatewayPairScript() string {
	pair := fmt.Sprintf(`"$HOME/.local/bin/aiman-gateway" pair --json --name '%s'`, gatewayPairDevice)
	return fmt.Sprintf(`bin="$HOME/.local/bin/aiman-gateway"
if [ ! -x "$bin" ]; then echo %s; exit 0; fi
if command -v timeout >/dev/null 2>&1; then
  out=$(timeout -k 5 %d %s) || {
    rc=$?
    case "$rc" in 124|137|143) echo %s; exit 0 ;; esac
    exit "$rc"
  }
else
  out=$(%s) || exit $?
fi
printf '%%s\n' "$out"
`, gatewayMissingMarker, gatewayPairTimeoutSeconds, pair, gatewayPairTimeoutMarker, pair)
}

// gatewayNeedsUpdate reports whether a probed version may lack `pair`. A version
// with no release number is updated too, since nothing says it has the command.
func gatewayNeedsUpdate(version string) bool {
	if version == "missing" {
		return false
	}
	v, ok := remotesvc.ParseVersion(version)
	return !ok || v.Less(gatewayPairMinVersion)
}

func probeGateway(cfg *config.Config, host string) (domain.Daemon, error) {
	out, err := gatewayScriptRunner(cfg, host, remotesvc.ProbeScript(remotesvc.KindGateway))
	if err != nil {
		return domain.Daemon{}, fmt.Errorf("probe gateway: %w", err)
	}
	d := remotesvc.ParseProbe(remotesvc.KindGateway, host, out)
	if d.Version == "missing" {
		return d, errGatewayMissing
	}
	return d, nil
}

// updateGateway installs this client's release (the latest for a local build)
// and probes again, because the binary that answers may still be too old to pair.
// Unparseable text after an update still goes on to the pair, which the script
// bounds. The returned string is the release for the page.
func updateGateway(cfg *config.Config, host, version string) (domain.Daemon, string, error) {
	tag, err := remotesvc.InstallTag(context.Background(), version)
	if err != nil {
		return domain.Daemon{}, "", fmt.Errorf("update gateway: %w", err)
	}
	if _, err := gatewayScriptRunner(cfg, host, remotesvc.UpdateScript(remotesvc.KindGateway, tag)); err != nil {
		return domain.Daemon{}, "", fmt.Errorf("update gateway: %w", err)
	}
	d, err := probeGateway(cfg, host)
	if err != nil {
		return d, "", err
	}
	v, ok := remotesvc.ParseVersion(d.Version)
	if !ok {
		return d, tag, nil
	}
	if v.Less(gatewayPairMinVersion) {
		return d, "", fmt.Errorf("aiman-gateway on %s is still %s after updating; pairing needs %s or newer",
			host, v, gatewayPairMinVersion)
	}
	return d, v.String(), nil
}

// prepareGateway leaves a gateway that knows `pair` running on host and records
// on msg what it had to do.
func prepareGateway(cfg *config.Config, host, version string, msg *gatewayPairMsg) error {
	d, err := probeGateway(cfg, host)
	if err != nil {
		return err
	}
	wasRunning := d.Status == domain.DaemonStatusRunning
	if gatewayNeedsUpdate(d.Version) {
		d, msg.updatedTo, err = updateGateway(cfg, host, version)
		if err != nil {
			return err
		}
	}
	if d.Status != domain.DaemonStatusRunning {
		if _, err := gatewayScriptRunner(cfg, host, remotesvc.StartScript(remotesvc.KindGateway)); err != nil {
			return fmt.Errorf("start gateway: %w", err)
		}
	}
	msg.started = !wasRunning
	return nil
}

func mintGatewayPair(cfg *config.Config, host string) (gatewayPairResult, error) {
	out, err := gatewayScriptRunner(cfg, host, gatewayPairScript())
	switch {
	case err != nil:
		return gatewayPairResult{}, fmt.Errorf("pair failed: %w", err)
	case strings.Contains(out, gatewayMissingMarker):
		return gatewayPairResult{}, errGatewayMissing
	case strings.Contains(out, gatewayPairTimeoutMarker):
		return gatewayPairResult{}, errGatewayPairTimeout
	}
	res, err := parseGatewayPair(out)
	if err != nil {
		return gatewayPairResult{}, fmt.Errorf("pair failed: %w", err)
	}
	return res, nil
}

// gatewayPairCmd brings the gateway on host up to a release that knows `pair`,
// starts it when it is not running, then mints a pairing code.
func gatewayPairCmd(cfg *config.Config, host, version string) tea.Cmd {
	return func() tea.Msg {
		msg := gatewayPairMsg{host: host}
		if msg.err = prepareGateway(cfg, host, version, &msg); msg.err != nil {
			return msg
		}
		msg.result, msg.err = mintGatewayPair(cfg, host)
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
	return gatewayPairCmd(m.cfg, d.RemoteHost, m.version)
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
	v.updatedTo = msg.updatedTo
	if msg.err == nil {
		v.qr, msg.err = qrHalfBlock(msg.result.URI)
	}
	if msg.err != nil {
		v.err = gatewayPairError(msg.host, m.version, msg.err)
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

func gatewayPairError(host, version string, err error) string {
	if errors.Is(err, errGatewayMissing) {
		missing := "aiman-gateway is not installed on " + host + "."
		if tag, ok := remotesvc.ReleaseTag(version); ok {
			return missing + " Install it with:\n" + remotesvc.KindGateway.InstallPipeAt(tag)
		}
		return missing + " Install it from the phone gateway row on the daemons tab."
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

// notes says what pairing did to the gateway on the way.
func (v *gatewayPairView) notes() []string {
	var notes []string
	if v.updatedTo != "" {
		notes = append(notes, "Updated aiman-gateway to "+v.updatedTo+" on "+v.host+".")
	}
	if v.started {
		notes = append(notes, "Gateway started.")
	}
	return notes
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
		for _, note := range v.notes() {
			b.WriteString("\n  " + dimStyle.Render(note) + "\n")
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
		for _, note := range v.notes() {
			info = append(info, "", dimStyle.Render(note))
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
