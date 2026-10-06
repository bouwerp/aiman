package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// runDeepcodeWatch polls ~/.deepcode/projects/<code>/sessions-index.json until
// SIGTERM or SIGINT. The launch script sends SIGTERM when the CLI exits, which
// is the session-end signal: Deep Code has no hook events.
func runDeepcodeWatch(args []string) error {
	flags, _ := takeFlags(args)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return agenthook.WatchDeepcode(ctx, agenthook.DeepcodeWatch{
		Dir:      flags["dir"],
		ResumeID: flags["resume"],
		Emit:     emitDeepcodeReport,
	})
}

func emitDeepcodeReport(r agenthook.Report) {
	sessionID := strings.TrimSpace(os.Getenv("AIMAN_ID"))
	if sessionID == "" {
		return
	}
	if dir, err := config.GetDir(); err == nil {
		_ = agenthook.WriteStored(dir, sessionID, r)
	}
	sock, err := socketPath()
	if err != nil {
		return
	}
	// Serve is optional. The sidecar write above is what resume and wait read
	// when the agent API is down, same as the hook reporter.
	_ = reportNativeToServe(sock, sessionID, r)
}
