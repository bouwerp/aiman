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
	Driver     string // systemd, nohup, none
	Version    string
	SocketOK   bool
	RTKVersion string // serve probe only; empty when rtk is not installed
	Logs       string
	UpdatedAt  time.Time
	// SelfUpdate is the host's own update attempt from the serve probe's
	// SELFUPDATE= line; nil when the host has recorded none.
	SelfUpdate *SelfUpdateAttempt
}

// SelfUpdateAttempt is the part of serve's host-update attempt the laptop
// reads: its status (started, ok, failed), the target tag and when it began.
type SelfUpdateAttempt struct {
	Status string
	To     string
	At     time.Time
}

func DaemonKey(host, kind string) string {
	if kind == "" {
		kind = "trigger"
	}
	return host + "\x00" + kind
}
