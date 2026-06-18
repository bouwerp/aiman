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
	Status     DaemonStatus
	Logs       string // Last N lines of tmux pane capture
	UpdatedAt  time.Time
}
