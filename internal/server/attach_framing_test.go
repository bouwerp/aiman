package server

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestAttachFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("echo hi\r")
	if err := writeAttachFrame(&buf, attachFrameInput, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	kind, got, err := readAttachFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != attachFrameInput || !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: kind=%d payload=%q", kind, got)
	}
}

func TestAttachFrameResizeLayout(t *testing.T) {
	var buf bytes.Buffer
	resize := make([]byte, 4)
	binary.BigEndian.PutUint16(resize[:2], 200)
	binary.BigEndian.PutUint16(resize[2:], 50)
	if err := writeAttachFrame(&buf, attachFrameResize, resize); err != nil {
		t.Fatalf("write: %v", err)
	}
	kind, got, err := readAttachFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != attachFrameResize {
		t.Fatalf("kind = %d", kind)
	}
	cols := int(binary.BigEndian.Uint16(got[:2]))
	rows := int(binary.BigEndian.Uint16(got[2:4]))
	if cols != 200 || rows != 50 {
		t.Fatalf("resize payload decoded as %dx%d", cols, rows)
	}
}

func TestAttachFrameRejectsGarbageLength(t *testing.T) {
	buf := bytes.Repeat([]byte{0xFF}, 8)
	if _, _, err := readAttachFrame(bytes.NewReader(buf)); err == nil {
		t.Fatal("oversized frame length must fail")
	}
	zero := make([]byte, 8)
	if _, _, err := readAttachFrame(bytes.NewReader(zero)); err == nil {
		t.Fatal("zero-length frame must fail")
	}
}
