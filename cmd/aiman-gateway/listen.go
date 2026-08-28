package main

import (
	"net"

	"tailscale.com/tsnet"
)

func listenTS(s *tsnet.Server, funnel bool, addr string) (net.Listener, error) {
	if funnel {
		return s.ListenFunnel("tcp", addr)
	}
	return s.Listen("tcp", addr)
}
