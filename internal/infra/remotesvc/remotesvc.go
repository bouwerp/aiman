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

func UnitFile(k Kind) string {
	return fmt.Sprintf(`[Unit]
Description=Aiman %s
After=default.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
WorkingDirectory=%%h
Environment=HOME=%%h
StandardOutput=append:%%h/.aiman/%s.log
StandardError=append:%%h/.aiman/%s.log

[Install]
WantedBy=default.target
`, k, k.ExecLine(), k, k)
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
    systemctl --user enable --now %s.service
  else
    %s
  fi
else
  %s
fi
`, k.Binary(), k.Binary(), k.InstallPipe(), unit, UnitFile(k), unit, fallback, fallback)
}

func StartScript(k Kind) string {
	unit := k.Unit()
	return "set -e\n" + userRuntimeEnv() + fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
mkdir -p "$HOME/.aiman"
if command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ] && systemctl --user show-environment >/dev/null 2>&1; then
  systemctl --user daemon-reload
  systemctl --user restart %s.service
  systemctl --user enable %s.service >/dev/null 2>&1 || true
else
  %s
fi
`, unit, unit, unit, nohupStart(k))
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
		sockCheck = `if [ -S "$HOME/.aiman/aiman.sock" ]; then echo SOCKET=1; else echo SOCKET=0; fi`
	}
	procCheck := fmt.Sprintf("pgrep -f '%s' >/dev/null", k.procPattern())
	return userRuntimeEnv() + fmt.Sprintf(`
DRIVER=none
ACTIVE=inactive
if command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ] && systemctl --user show-environment >/dev/null 2>&1; then
  DRIVER=systemd
  ACTIVE=$(systemctl --user is-active %s.service 2>/dev/null || echo inactive)
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
  journalctl --user -u %s.service -n 80 --no-pager 2>/dev/null || tail -n 80 %s 2>/dev/null || true
else
  tail -n 80 %s 2>/dev/null || true
fi
`, unit, unit, pid, pid, procCheck, unit, bin, bin, bin, bin, sockCheck, unit, logf, logf)
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
			switch val {
			case "active":
				d.Status = domain.DaemonStatusRunning
			case "failed":
				d.Status = domain.DaemonStatusError
			}
		case "VERSION":
			d.Version = val
		case "SOCKET":
			d.SocketOK = val == "1"
		}
	}
	d.Logs = strings.TrimRight(logs, "\n")
	return d
}
