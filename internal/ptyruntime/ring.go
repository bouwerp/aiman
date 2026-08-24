package ptyruntime

import "sync"

// ringBuffer is a fixed-capacity byte ring holding the most recent output for
// replay on attach and for capture. Appends are amortised O(n) in the worst
// case (wrap copy) but O(1) typically; reads are O(n).
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	cap  int
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = DefaultScrollbackBytes
	}
	return &ringBuffer{data: make([]byte, 0, capacity), cap: capacity}
}

func (r *ringBuffer) append(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(data) >= r.cap {
		r.data = append(r.data[:0], data[len(data)-r.cap:]...)
		return
	}
	r.data = append(r.data, data...)
	if over := len(r.data) - r.cap; over > 0 {
		r.data = append(r.data[:0], r.data[over:]...)
	}
}

// tail returns the last maxBytes bytes, or the whole buffer when maxBytes <= 0.
func (r *ringBuffer) tail(maxBytes int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if maxBytes <= 0 || maxBytes >= len(r.data) {
		out := make([]byte, len(r.data))
		copy(out, r.data)
		return out
	}
	out := make([]byte, maxBytes)
	copy(out, r.data[len(r.data)-maxBytes:])
	return out
}

func (r *ringBuffer) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.data)
}
