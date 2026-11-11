package remotesvc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Kind string

const (
	KindServe   Kind = "serve"
	KindTrigger Kind = "trigger"
	KindGateway Kind = "gateway"

	// OpTimeout covers remote install/update (curl the GitHub binary) plus
	// systemctl. The default SSH call deadline is too short for that.
	OpTimeout = 3 * time.Minute
)

func (k Kind) Unit() string {
	switch k {
	case KindTrigger:
		return "aiman-trigger"
	case KindGateway:
		return "aiman-gateway"
	default:
		return "aiman-serve"
	}
}

func (k Kind) Binary() string {
	switch k {
	case KindTrigger:
		return "aiman-trigger"
	case KindGateway:
		return "aiman-gateway"
	default:
		return "aiman"
	}
}

func (k Kind) ExecLine() string {
	switch k {
	case KindTrigger:
		return "%h/.local/bin/aiman-trigger"
	case KindGateway:
		return "%h/.local/bin/aiman-gateway"
	default:
		return "%h/.local/bin/aiman serve"
	}
}

func (k Kind) LogFile() string {
	switch k {
	case KindTrigger:
		return "$HOME/.aiman/trigger.log"
	case KindGateway:
		return "$HOME/.aiman/gateway.log"
	default:
		return "$HOME/.aiman/serve.log"
	}
}

func (k Kind) PidFile() string {
	switch k {
	case KindTrigger:
		return "$HOME/.aiman/trigger.pid"
	case KindGateway:
		return "$HOME/.aiman/gateway.pid"
	default:
		return "$HOME/.aiman/serve.pid"
	}
}

// procPattern is the pgrep -f and pkill -f pattern for k's process. The
// gateway's is anchored to the executable, so it cannot match another command
// that names the binary, such as an installer's cp or mv.
func (k Kind) procPattern() string {
	switch k {
	case KindTrigger:
		return "[a]iman-trigger"
	case KindGateway:
		return "^([^ ]*/)?aiman-gateway( |$)"
	default:
		return "[a]iman serve"
	}
}

// killMode returns the systemd KillMode for the unit.
//
// serve spawns detached PTY holder processes that are meant to outlive it —
// surviving a serve restart or update is their entire purpose. Setsid is not
// enough for that: it moves a process to a new session, so it escapes
// process-group signals, but cgroup membership is inherited across fork and
// cannot be shed. Under systemd's default KillMode=control-group, stopping
// serve therefore SIGTERMs every holder in its cgroup, killing the running
// agents with it. KillMode=process signals only the main process and leaves the
// rest of the cgroup alone, which is exactly the contract the holders assume.
//
// The trigger daemon and the phone gateway own no such processes; the default
// group cleanup is the right behaviour for their children.
func (k Kind) killMode() string {
	if k == KindServe {
		return "KillMode=process\n"
	}
	return ""
}

func UnitFile(k Kind) string {
	return fmt.Sprintf(`[Unit]
Description=Aiman %s
After=default.target
StartLimitIntervalSec=120
StartLimitBurst=20

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
%sWorkingDirectory=%%h
Environment=HOME=%%h

[Install]
WantedBy=default.target
`, k, k.ExecLine(), k.killMode())
}

// userRuntimeEnv makes `systemctl --user` work over SSH: non-interactive
// sessions have no XDG_RUNTIME_DIR or session bus unless linger is on.
func userRuntimeEnv() string {
	return `export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR}/bus}"
`
}

// InstallEnableScript installs release tag of k when it is missing, then
// enables and starts its unit.
func InstallEnableScript(k Kind, tag string) string {
	unit := k.Unit()
	fallback := nohupStart(k)
	return "set -e\n" + userRuntimeEnv() + fmt.Sprintf(`mkdir -p "$HOME/.aiman" "$HOME/.config/systemd/user" "$HOME/.local/bin"
if ! command -v %s >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/%s" ]; then
  %s
fi
export PATH="$HOME/.local/bin:$PATH"
if command -v systemctl >/dev/null 2>&1; then
  loginctl enable-linger "$(id -un)" >/dev/null 2>&1 || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -d "/run/user/$(id -u)" ] && break
    sleep 0.3
  done
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR}/bus}"
  if systemctl --user show-environment >/dev/null 2>&1; then
    cat > "$HOME/.config/systemd/user/%s.service" <<'UNIT'
%s
UNIT
    systemctl --user daemon-reload
    %s
    systemctl --user enable --now %s.service
  else
    %s
  fi
else
  %s
fi
`, k.Binary(), k.Binary(), k.InstallPipeAt(tag), unit, UnitFile(k), leftoverCleanup(k), unit, fallback, fallback)
}

// StartScript (re)starts k. Whenever user systemd answers, k runs under its
// unit, which is written (or rewritten) first, so a host whose daemon was
// started with nohup moves to systemd at its next start or update. The nohup
// supervisor is only for hosts without user systemd.
func StartScript(k Kind) string {
	unit := k.Unit()
	return "set -e\n" + userRuntimeEnv() + fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
mkdir -p "$HOME/.aiman" "$HOME/.config/systemd/user"
if command -v systemctl >/dev/null 2>&1; then
  loginctl enable-linger "$(id -un)" >/dev/null 2>&1 || true
fi
if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  cat > "$HOME/.config/systemd/user/%s.service" <<'UNIT'
%s
UNIT
  systemctl --user daemon-reload
  %s
  systemctl --user start %s.service
  systemctl --user enable %s.service >/dev/null 2>&1 || true
else
  %s
fi
`, unit, UnitFile(k), leftoverCleanup(k), unit, unit, nohupStart(k))
}

// leftoverCleanup resets systemd start-limit, stops the user unit, and kills
// any leftover process holding the socket lock so the next start can bind.
func leftoverCleanup(k Kind) string {
	unit := k.Unit()
	pid := k.PidFile()
	return fmt.Sprintf(`systemctl --user reset-failed %s.service >/dev/null 2>&1 || true
systemctl --user stop %s.service >/dev/null 2>&1 || true
if [ -f %s ]; then
  kill "$(cat %s)" 2>/dev/null || true
  rm -f %s
fi
pkill -f '%s' >/dev/null 2>&1 || true
`, unit, unit, pid, pid, pid, k.procPattern())
}

func StopScript(k Kind) string {
	unit := k.Unit()
	pid := k.PidFile()
	return userRuntimeEnv() + fmt.Sprintf(`
if command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ]; then
  systemctl --user stop %s.service || true
fi
if [ -f %s ]; then
  kill "$(cat %s)" 2>/dev/null || true
  rm -f %s
fi
pkill -f '%s' >/dev/null 2>&1 || true
true
`, unit, unit, pid, pid, pid, k.procPattern())
}

// UpdateScript installs release tag of k and restarts it. Updating serve also
// updates an installed aiman-gateway, so the two stay on one release. The set -e
// covers the install: a refused download must fail the update, not restart the
// old binary and report success.
func UpdateScript(k Kind, tag string) string {
	script := "set -e\n" + k.InstallPipeAt(tag) + "\n" + StartScript(k)
	if k == KindServe {
		script += gatewayLockstep(tag)
	}
	return script
}

// gatewayLockstep runs after serve's start, under its set -e, so a failed
// gateway install fails the update visibly. A stopped gateway gets the new
// binary and stays stopped. The subshell keeps the start script's exports and
// set -e to itself.
func gatewayLockstep(tag string) string {
	k := KindGateway
	return fmt.Sprintf(`if [ -x "$HOME/.local/bin/%s" ] || command -v %s >/dev/null 2>&1; then
  gateway_running=0
  if pgrep -f '%s' >/dev/null; then gateway_running=1; fi
  %s
  if [ "$gateway_running" = 1 ]; then
    (
%s
    )
  fi
fi
`, k.Binary(), k.Binary(), k.procPattern(), k.InstallPipeAt(tag), StartScript(k))
}

func ProbeScript(k Kind) string {
	unit := k.Unit()
	pid := k.PidFile()
	logf := k.LogFile()
	bin := k.Binary()
	sockCheck := `echo SOCKET=0`
	if k == KindServe {
		// A leftover sock after a crash is not a live listener.
		sockCheck = `if [ "$ACTIVE" = active ] && [ -S "$HOME/.aiman/aiman.sock" ]; then echo SOCKET=1; else echo SOCKET=0; fi`
		sockCheck += "\n" + rtkCheck + "\n" + selfUpdateCheck
	}
	procCheck := fmt.Sprintf("pgrep -f '%s' >/dev/null", k.procPattern())
	return userRuntimeEnv() + fmt.Sprintf(`
DRIVER=none
ACTIVE=inactive
if command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ] && systemctl --user show-environment >/dev/null 2>&1; then
  DRIVER=systemd
  ACTIVE=$(systemctl --user show -p ActiveState --value %s.service 2>/dev/null || true)
  ACTIVE=${ACTIVE:-inactive}
elif [ -f %s ] && kill -0 "$(cat %s)" 2>/dev/null; then
  DRIVER=nohup
  if %s; then ACTIVE=active; else ACTIVE=activating; fi
elif %s; then
  DRIVER=nohup
  ACTIVE=active
fi
echo DRIVER=$DRIVER
echo ACTIVE=$ACTIVE
if command -v %s >/dev/null 2>&1; then
  echo VERSION=$(%s --version 2>/dev/null | head -1)
elif [ -x "$HOME/.local/bin/%s" ]; then
  echo VERSION=$("$HOME/.local/bin/%s" --version 2>/dev/null | head -1)
else
  echo VERSION=missing
fi
%s
echo '---LOGS---'
if [ "$DRIVER" = systemd ]; then
  journalctl --user -u %s.service -n 40 --no-pager 2>/dev/null || true
fi
tail -n 40 %s 2>/dev/null || true
`, unit, unit, pid, pid, procCheck, procCheck, bin, bin, bin, bin, sockCheck, unit, logf)
}

// rtkCheck reports the rtk version on the serve probe. The rtk installer puts
// the binary in ~/.local/bin, which a non-login SSH shell may not have on PATH.
const rtkCheck = `if command -v rtk >/dev/null 2>&1; then
  echo RTK=$(rtk --version 2>/dev/null | head -1)
elif [ -x "$HOME/.local/bin/rtk" ]; then
  echo RTK=$("$HOME/.local/bin/rtk" --version 2>/dev/null | head -1)
else
  echo RTK=missing
fi`

// selfUpdateCheck prints serve's host-update state file on one line, so the
// laptop can see an install the host is running on itself.
const selfUpdateCheck = `if [ -f "$HOME/.aiman/host-update.json" ]; then
  echo "SELFUPDATE=$(tr -d '\n' < "$HOME/.aiman/host-update.json" 2>/dev/null)"
else
  echo SELFUPDATE=
fi`

// logRotateMax is the size past which a nohup log is moved to <log>.old before
// a start. One older copy is kept.
const logRotateMax = 10 << 20

// rotateLog moves k's nohup log aside once it is over logRotateMax.
func rotateLog(k Kind) string {
	return fmt.Sprintf(`if [ -f %[1]s ] && [ "$(wc -c < %[1]s)" -gt %[2]d ]; then mv -f %[1]s %[1]s.old; fi`, k.LogFile(), logRotateMax)
}

// SupervisorFile is the nohup supervisor script nohupStart writes for k.
func (k Kind) SupervisorFile() string {
	return "$HOME/.aiman/" + k.Unit() + "-supervise.sh"
}

// supervisor is the body of k's nohup supervisor: it runs k, and 2 seconds
// after k exits, for whatever reason, runs it again. TERM or INT stops k and
// the loop together, so the pid file (which names the supervisor) is all a
// stop needs. The log is rotated before each run, which the child opens
// itself, so a crash loop cannot grow it without bound.
//
// For serve this keeps the holder contract: holders are setsid'd children of
// serve, never of the supervisor, and the trap signals only serve's own pid,
// exactly as `kill <serve pid>` did before the supervisor existed.
func supervisor(k Kind, cmd string) string {
	return fmt.Sprintf(`# aiman nohup supervisor for %[1]s: restarts it 2s after it exits.
child=
pause=
trap 'kill $child $pause 2>/dev/null; exit 0' TERM INT
while :; do
  %[2]s
  %[3]s >> %[4]s 2>&1 &
  child=$!
  wait "$child"
  echo "aiman supervisor: %[1]s exited with status $?, restarting in 2s" >> %[4]s
  child=
  sleep 2 &
  pause=$!
  wait "$pause"
  pause=
done`, k.Unit(), rotateLog(k), cmd, k.LogFile())
}

// nohupStart stops what the pid file names (the supervisor, which takes its
// child with it), then kills by pattern, like leftoverCleanup: a process the
// pid file does not name (such as a server started by a `pair` an old gateway
// binary did not understand, or one started before the supervisor existed)
// would otherwise keep running the old binary.
func nohupStart(k Kind) string {
	bin := "$HOME/.local/bin/" + k.Binary()
	cmd := bin
	if k == KindServe {
		cmd = bin + " serve"
	}
	return fmt.Sprintf(`mkdir -p "$HOME/.aiman"
if [ -f %[1]s ]; then kill "$(cat %[1]s)" 2>/dev/null || true; fi
pkill -f '%[2]s' >/dev/null 2>&1 || true
%[3]s
cat > %[4]s <<'SUPERVISOR'
%[5]s
SUPERVISOR
nohup bash %[4]s >/dev/null 2>&1 & echo $! > %[1]s
`, k.PidFile(), k.procPattern(), rotateLog(k), k.SupervisorFile(), supervisor(k, cmd))
}

func ParseProbe(kind Kind, host, stdout string) domain.Daemon {
	d := domain.Daemon{
		RemoteHost: host,
		Kind:       string(kind),
		Status:     domain.DaemonStatusStopped,
		UpdatedAt:  time.Now(),
	}
	logs := ""
	inLogs := false
	for _, line := range strings.Split(stdout, "\n") {
		if inLogs {
			logs += line + "\n"
			continue
		}
		if line == "---LOGS---" {
			inLogs = true
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "DRIVER":
			d.Driver = val
		case "ACTIVE":
			d.Status = parseActiveState(val)
		case "VERSION":
			d.Version = val
		case "SOCKET":
			d.SocketOK = val == "1"
		case "RTK":
			if val != "missing" {
				d.RTKVersion = val
			}
		case "SELFUPDATE":
			d.SelfUpdate, d.HostManaged = parseSelfUpdate(val, time.Now())
		}
	}
	d.Logs = strings.TrimRight(logs, "\n")
	if d.Status != domain.DaemonStatusRunning {
		d.SocketOK = false
	}
	return d
}

// hostManagedWithin is how recent the host manager's last release check must
// be for the laptop to treat the host as updating itself.
const hostManagedWithin = time.Hour

// parseSelfUpdate reads serve's host-update state file: the attempt (nil for an
// empty line, a file with no attempt, or one that does not parse) and whether
// the host's manager checked for a release within hostManagedWithin of now.
func parseSelfUpdate(val string, now time.Time) (*domain.SelfUpdateAttempt, bool) {
	if strings.TrimSpace(val) == "" {
		return nil, false
	}
	var doc struct {
		CheckedAt time.Time `json:"checked_at"`
		Attempt   *struct {
			Status string    `json:"status"`
			To     string    `json:"to"`
			At     time.Time `json:"at"`
		} `json:"attempt"`
	}
	if json.Unmarshal([]byte(val), &doc) != nil {
		return nil, false
	}
	managed := !doc.CheckedAt.IsZero() && now.Sub(doc.CheckedAt) < hostManagedWithin
	if doc.Attempt == nil {
		return nil, managed
	}
	return &domain.SelfUpdateAttempt{Status: doc.Attempt.Status, To: doc.Attempt.To, At: doc.Attempt.At}, managed
}

func parseActiveState(val string) domain.DaemonStatus {
	val = strings.TrimPrefix(strings.TrimSpace(val), "ActiveState=")
	fields := strings.Fields(val)
	if len(fields) == 0 {
		return domain.DaemonStatusStopped
	}
	switch strings.ToLower(fields[0]) {
	case "active", "activating", "reloading":
		return domain.DaemonStatusRunning
	case "failed":
		return domain.DaemonStatusError
	default:
		return domain.DaemonStatusStopped
	}
}
