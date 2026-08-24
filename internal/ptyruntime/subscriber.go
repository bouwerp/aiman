package ptyruntime

// subscriber is one attach client receiving live output.
type subscriber struct {
	ch chan []byte
}

func (s *subscriber) close() { close(s.ch) }
