package domain

import "time"

type DaemonStatus string

const (
	DaemonStatusRunning DaemonStatus = "RUNNING"
	DaemonStatusStopped DaemonStatus = "STOPPED"
	DaemonStatusError   DaemonStatus = "ERROR"
)

type Daemon struct {
	RemoteHost string
	Kind       string // "serve" or "trigger"
	Status     DaemonStatus
	Driver     string // systemd, nohup, tmux, none
	Version    string
	SocketOK   bool
	Logs       string
	UpdatedAt  time.Time
}

func DaemonKey(host, kind string) string {
	if kind == "" {
		kind = "trigger"
	}
	return host + "\x00" + kind
}
