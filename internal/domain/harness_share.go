package domain

import "time"

// HarnessShare is a link that lets a tester reach a session's ready harness
// run through the gateway. Only the id is secret; a share ends when it is
// revoked, past CapAt, or when its run is no longer ready.
type HarnessShare struct {
	ID        string
	Session   string
	Run       string
	CreatedAt time.Time
	CapAt     *time.Time
	RevokedAt *time.Time
}

// GatewayAnnounce is what the running aiman-gateway told serve: where its
// console listens, so serve can build share URLs.
type GatewayAnnounce struct {
	Base        string
	ConsolePort int
	Scheme      string
	PID         int
	At          time.Time
}
