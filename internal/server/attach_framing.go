package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Attach stream framing (client -> server only; the server streams output back
// as raw bytes). Every message is [4-byte big-endian length][kind][payload]:
//
//	0x01 input   — payload is raw terminal input
//	0x02 resize  — payload is two big-endian uint16: cols, rows
//
// Framing keeps resize control data unambiguous against keystrokes a user may
// legitimately type inside the terminal.
const (
	attachFrameInput  = 0x01
	attachFrameResize = 0x02

	attachMaxFrame = 64 << 10
)

// readAttachFrame reads one frame. Returns (kind, payload, err); io.EOF at a
// clean boundary ends the stream.
func readAttachFrame(r io.Reader) (byte, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > attachMaxFrame {
		return 0, nil, fmt.Errorf("pty attach: bad frame length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

// writeAttachFrame emits one frame.
func writeAttachFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload)+1 > attachMaxFrame {
		return errors.New("pty attach: frame too large")
	}
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)+1)) //nolint:gosec // G115: length is capped by attachMaxFrame above
	buf[4] = kind
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

// handlePTYAttachConnInput consumes frames until the connection closes,
// forwarding input to the session and applying resizes inline.
func handlePTYAttachConnInput(ctx context.Context, sessID string, write func([]byte) error, resize func(int, int) error, r io.Reader) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		kind, payload, err := readAttachFrame(r)
		if err != nil {
			return
		}
		switch kind {
		case attachFrameInput:
			if len(payload) > 0 {
				_ = write(payload)
			}
		case attachFrameResize:
			if len(payload) >= 4 {
				cols := int(binary.BigEndian.Uint16(payload[:2]))
				rows := int(binary.BigEndian.Uint16(payload[2:4]))
				_ = resize(cols, rows)
			}
		default:
			// Unknown frame kinds are ignored for forward compatibility.
		}
	}
}
