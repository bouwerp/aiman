package main

import (
	"io"
	"os"
	"time"

	"github.com/bouwerp/aiman/internal/limits"
)

// limitsRecordMaxInput bounds what the recorder reads from the status line.
const limitsRecordMaxInput = 1 << 20

// runLimitsRecordClaude stores the rate limits from Claude status line input on
// stdin. It runs in the background of every status line paint and makes no serve
// call. It prints nothing and always returns nil, because a failed recording
// only leaves the previous file and nobody is there to read an error.
//
//nolint:unparam // the error result matches the shape of the CLI's other command runners
func runLimitsRecordClaude(stdin io.Reader) error {
	input, err := io.ReadAll(io.LimitReader(stdin, limitsRecordMaxInput))
	if err != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	_ = limits.RecordClaude(input, home, time.Now())
	return nil
}
