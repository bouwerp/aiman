//go:build !linux

package server

import "net"

// peerPID is unavailable off Linux, so the operator check is skipped there.
func peerPID(net.Conn) (int, bool) { return 0, false }
