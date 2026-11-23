package usecase

import "testing"

func TestRPCStreamsCloseOnEmptySetIsSafe(t *testing.T) {
	s := NewRPCStreams()
	s.Close()
	s.Close()
}
