package remotesvc

import "fmt"

// Scripts serve runs on its own host (not over SSH) for host.restart and
// host.logs.

// LogSourceMarker starts the first line LogsScript prints:
// "AIMAN_LOG_SOURCE=journal" or "AIMAN_LOG_SOURCE=file".
const LogSourceMarker = "AIMAN_LOG_SOURCE="

// unitExists is the shell test for k's user unit being installed and user
// systemd answering.
func unitExists(k Kind) string {
	return fmt.Sprintf(`command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/%s.service" ] && systemctl --user show-environment >/dev/null 2>&1`, k.Unit())
}

// RestartScript restarts k in place. Under systemd it is a plain
// `systemctl --user restart`; the unit is never rewritten. Under the nohup
// supervisor it kills the supervisor's children (the daemon, or the pause
// between runs), and the supervisor starts the daemon again 2s later. serve's
// pty holders are setsid'd children of serve, never of the supervisor, so they
// are not touched. With neither, it fails: there is nothing to restart.
func RestartScript(k Kind) string {
	pid := k.PidFile()
	return userRuntimeEnv() + fmt.Sprintf(`if %[1]s; then
  systemctl --user reset-failed %[2]s.service >/dev/null 2>&1 || true
  exec systemctl --user restart %[2]s.service
fi
if [ -f %[3]s ]; then
  sup="$(cat %[3]s)"
  if [ -n "$sup" ] && kill -0 "$sup" 2>/dev/null; then
    kids="$(pgrep -P "$sup" 2>/dev/null || true)"
    if [ -z "$kids" ]; then
      echo "aiman restart: %[4]s supervisor $sup has no child" >&2
      exit 1
    fi
    for c in $kids; do kill "$c" 2>/dev/null || true; done
    echo "aiman restart: %[4]s restarting under the nohup supervisor ($sup)"
    exit 0
  fi
fi
echo "aiman restart: %[4]s has no unit and no running supervisor" >&2
exit 1
`, unitExists(k), k.Unit(), pid, k)
}

// LogsScript prints the last n lines of k's output after a LogSourceMarker
// line: the user journal when k's unit exists, else the nohup log.
func LogsScript(k Kind, n int) string {
	return userRuntimeEnv() + fmt.Sprintf(`if %[1]s; then
  echo %[2]sjournal
  journalctl --user -u %[3]s -n %[4]d --no-pager -o short-iso 2>&1 || true
else
  echo %[2]sfile
  tail -n %[4]d %[5]s 2>/dev/null || true
fi
`, unitExists(k), LogSourceMarker, k.Unit(), n, k.LogFile())
}

// StateScript prints k's state as KEY=value lines: INSTALLED (1 or 0),
// DRIVER (systemd, nohup or none), ACTIVE, PID (the daemon's own process, 0
// when none) and VERSION (the first line of --version). Each command is
// bounded to 2s.
func StateScript(k Kind) string {
	return userRuntimeEnv() + fmt.Sprintf(`BIN=
if [ -x "$HOME/.local/bin/%[1]s" ]; then BIN="$HOME/.local/bin/%[1]s"; elif command -v %[1]s >/dev/null 2>&1; then BIN="$(command -v %[1]s)"; fi
UNIT=0
if [ -f "$HOME/.config/systemd/user/%[2]s.service" ]; then UNIT=1; fi
if [ -z "$BIN" ] && [ "$UNIT" = 0 ]; then echo INSTALLED=0; exit 0; fi
echo INSTALLED=1
DRIVER=none
ACTIVE=inactive
PID=0
if [ "$UNIT" = 1 ] && command -v systemctl >/dev/null 2>&1 && timeout 2 systemctl --user show-environment >/dev/null 2>&1; then
  DRIVER=systemd
  ACTIVE=$(timeout 2 systemctl --user show -p ActiveState --value %[2]s.service 2>/dev/null || true)
  PID=$(timeout 2 systemctl --user show -p MainPID --value %[2]s.service 2>/dev/null || true)
elif [ -f %[3]s ] && kill -0 "$(cat %[3]s)" 2>/dev/null; then
  DRIVER=nohup
  PID=$(pgrep -P "$(cat %[3]s)" -f '%[4]s' 2>/dev/null | head -1)
  if [ -n "$PID" ]; then ACTIVE=active; else ACTIVE=activating; fi
else
  PID=$(pgrep -f '%[4]s' 2>/dev/null | head -1)
  if [ -n "$PID" ]; then DRIVER=nohup; ACTIVE=active; fi
fi
echo DRIVER=$DRIVER
echo ACTIVE=${ACTIVE:-inactive}
echo PID=${PID:-0}
if [ -n "$BIN" ]; then echo "VERSION=$(timeout 2 "$BIN" --version 2>/dev/null | head -1)"; else echo VERSION=; fi
`, k.Binary(), k.Unit(), k.PidFile(), k.procPattern())
}
