package remotesvc

import (
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Kind string

const (
	KindServe   Kind = "serve"
	KindTrigger Kind = "trigger"

	// OpTimeout covers remote install/update (curl the GitHub binary) plus
	// systemctl. The default SSH call deadline is too short for that.
	OpTimeout = 3 * time.Minute
)

func (k Kind) Unit() string {
	if k == KindTrigger {
		return "aiman-trigger"
	}
	return "aiman-serve"
}

func (k Kind) Binary() string {
	if k == KindTrigger {
		return "aiman-trigger"
	}
	return "aiman"
}

func (k Kind) ExecLine() string {
	if k == KindTrigger {
		return "%h/.local/bin/aiman-trigger"
	}
	return "%h/.local/bin/aiman serve"
}

func (k Kind) LogFile() string {
	if k == KindTrigger {
		return "$HOME/.aiman/trigger.log"
	}
	return "$HOME/.aiman/serve.log"
}

func (k Kind) PidFile() string {
	if k == KindTrigger {
		return "$HOME/.aiman/trigger.pid"
	}
	return "$HOME/.aiman/serve.pid"
}

func (k Kind) InstallPipe() string {
	if k == KindTrigger {
		return "curl -sSfL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | BINARY_NAME=aiman-trigger sh"
	}
	return "curl -sSfL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | sh"
}

func (k Kind) procPattern() string {
	if k == KindTrigger {
		return "[a]iman-trigger"
	}
	return "[a]iman serve"
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
// The trigger daemon owns no such processes; the default group cleanup is the
// right behaviour for its transient ssh children.
func (k Kind) killMode() string {
	if k == KindTrigger {
		return ""
	}
	return "KillMode=process\n"
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

func InstallEnableScript(k Kind) string {
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
`, k.Binary(), k.Binary(), k.InstallPipe(), unit, UnitFile(k), leftoverCleanup(k), unit, fallback, fallback)
}

func StartScript(k Kind) string {
	unit := k.Unit()
	return "set -e\n" + userRuntimeEnv() + fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
mkdir -p "$HOME/.aiman" "$HOME/.config/systemd/user"
if command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ] && systemctl --user show-environment >/dev/null 2>&1; then
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
`, unit, unit, UnitFile(k), leftoverCleanup(k), unit, unit, nohupStart(k))
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
tmux kill-session -t %s >/dev/null 2>&1 || true
true
`, unit, unit, pid, pid, pid, k.procPattern(), unit)
}

func UpdateScript(k Kind) string {
	return k.InstallPipe() + "\n" + StartScript(k)
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
  ACTIVE=active
elif %s; then
  DRIVER=nohup
  ACTIVE=active
elif tmux has-session -t %s 2>/dev/null; then
  DRIVER=tmux
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
`, unit, unit, pid, pid, procCheck, unit, bin, bin, bin, bin, sockCheck, unit, logf)
}

func nohupStart(k Kind) string {
	bin := "$HOME/.local/bin/" + k.Binary()
	cmd := bin
	if k == KindServe {
		cmd = bin + " serve"
	}
	return fmt.Sprintf(`mkdir -p "$HOME/.aiman"
if [ -f %s ]; then kill "$(cat %s)" 2>/dev/null || true; fi
nohup %s >> %s 2>&1 & echo $! > %s
`, k.PidFile(), k.PidFile(), cmd, k.LogFile(), k.PidFile())
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
		}
	}
	d.Logs = strings.TrimRight(logs, "\n")
	if d.Status != domain.DaemonStatusRunning {
		d.SocketOK = false
	}
	return d
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
