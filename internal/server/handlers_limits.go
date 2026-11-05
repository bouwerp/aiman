package server

import "github.com/bouwerp/aiman/internal/limits"

// SetLimits gives serve its limits manager. Stub until P1 wires limits.get.
func (s *Server) SetLimits(_ *limits.Manager) {}
