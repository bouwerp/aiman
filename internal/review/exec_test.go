package review

import (
	"context"
	"strings"
	"testing"
)

func TestExecRunKeepsStderrOutOfOutput(t *testing.T) {
	out, err := ExecRun(context.Background(), "sh", "-c", "echo '{}'; echo warn >&2")
	if err != nil || out != "{}\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	_, err = ExecRun(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v, want stderr in it", err)
	}
}

func TestExecRunInputFeedsStdin(t *testing.T) {
	out, err := ExecRunInput(context.Background(), "hello", "cat")
	if err != nil || out != "hello" {
		t.Fatalf("out %q err %v", out, err)
	}
}
