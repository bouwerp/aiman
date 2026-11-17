package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/images"
)

const harnessImageUsageText = `Usage: aiman harness image status [<ref|digest>] [--json]
       aiman harness image approve <digest> [--accept-risk --reason TEXT] [--json]   (operator)
       aiman harness image db-install <trivy|clamav|osv|yara> <bundle.tar.gz> --sha256 HEX --built-at RFC3339   (host)
       aiman harness image db-push [--remote NAME] [trivy|clamav|osv|yara ...]   (laptop)
       aiman harness approve-image <digest> --quote TEXT [--json]   (the session whose list holds the gate)
`

func harnessImageUsage(w io.Writer) error {
	_, _ = io.WriteString(w, harnessImageUsageText)
	return errUsage
}

// harnessImageMain runs `aiman harness image ...`.
func harnessImageMain(args []string, h harnessIO) error {
	if len(args) == 0 {
		return harnessImageUsage(h.errOut)
	}
	a, err := parseHarnessArgs(args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(h.errOut, err)
		return harnessImageUsage(h.errOut)
	}
	switch args[0] {
	case "status":
		if len(a.pos) > 1 {
			return harnessImageUsage(h.errOut)
		}
		return harnessImageStatus(h, a)
	case "approve":
		if len(a.pos) != 1 || (a.acceptRisk && a.reason == "") || (!a.acceptRisk && a.reason != "") {
			_, _ = fmt.Fprintln(h.errOut, "approve takes one digest; --accept-risk needs --reason and --reason needs --accept-risk")
			return harnessImageUsage(h.errOut)
		}
		params := map[string]any{"digest": a.pos[0]}
		if a.acceptRisk {
			params["accept_risk"], params["reason"] = true, a.reason
		}
		return harnessImageApprove(h, a, params)
	case "db-install":
		return harnessDBInstall(h, a)
	}
	return harnessImageUsage(h.errOut)
}

// harnessApproveImage runs `aiman harness approve-image <digest> --quote`.
func harnessApproveImage(args []string, h harnessIO) error {
	a, err := parseHarnessArgs(args)
	if err != nil || len(a.pos) != 1 || strings.TrimSpace(a.quote) == "" {
		if err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
		}
		_, _ = fmt.Fprintln(h.errOut, "approve-image needs the digest and --quote with the operator's words")
		return harnessImageUsage(h.errOut)
	}
	return harnessImageApprove(h, a, map[string]any{"digest": a.pos[0], "quote": a.quote})
}

type harnessImageReply struct {
	Image  images.Record   `json:"image"`
	Images []images.Record `json:"images"`
}

func harnessImageStatus(h harnessIO, a harnessArgs) error {
	params := map[string]any{}
	if len(a.pos) == 1 {
		key := "ref"
		if strings.HasPrefix(a.pos[0], "sha256:") {
			key = "digest"
		}
		params[key] = a.pos[0]
	}
	raw, err := harnessCallOut(h, a, "harness.image_status", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var r harnessImageReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if len(a.pos) == 1 {
		r.Images = []images.Record{r.Image}
	}
	for _, rec := range r.Images {
		writeImageRecord(h.out, rec)
	}
	return nil
}

func harnessImageApprove(h harnessIO, a harnessArgs, params map[string]any) error {
	raw, err := harnessCallOut(h, a, "harness.image_approve", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var r harnessImageReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	writeImageRecord(h.out, r.Image)
	return nil
}

func writeImageRecord(w io.Writer, rec images.Record) {
	_, _ = fmt.Fprintf(w, "%s  %s  %s", images.ShortDigest(rec.Digest), rec.Ref, rec.State)
	if rec.Stage != "" {
		_, _ = fmt.Fprintf(w, " (%s)", rec.Stage)
	}
	if rec.ApprovedBy != "" && rec.State == images.StateApproved {
		_, _ = fmt.Fprintf(w, " by %s", rec.ApprovedBy)
		if rec.Approver != "" {
			_, _ = fmt.Fprintf(w, " (%s)", rec.Approver)
		}
	}
	_, _ = fmt.Fprintf(w, "\n  %s\n", images.Summary(rec))
	for _, r := range rec.Reasons {
		_, _ = fmt.Fprintf(w, "  reason: %s\n", r)
	}
	if rec.Scan.Report != "" {
		_, _ = fmt.Fprintf(w, "  report: %s\n", rec.Scan.Report)
	}
}

// harnessDBInstall installs a scanner database bundle the laptop copied to
// this host. It runs on the host, as serve's user, and calls no RPC.
func harnessDBInstall(h harnessIO, a harnessArgs) error {
	if len(a.pos) != 2 || a.sha256 == "" || a.builtAt == "" {
		_, _ = fmt.Fprintln(h.errOut, "db-install needs the database, the bundle, --sha256 and --built-at")
		return harnessImageUsage(h.errOut)
	}
	built, err := time.Parse(time.RFC3339, a.builtAt)
	if err != nil {
		return fmt.Errorf("--built-at: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	db := filepath.Join(home, ".aiman", "harness", "db")
	if err := os.MkdirAll(db, 0o700); err != nil {
		return err
	}
	in := images.DBInstall{Name: a.pos[0], Archive: a.pos[1], SHA256: a.sha256, BuiltAt: built}
	if err := images.InstallDB(db, in, time.Now()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(h.out, "installed %s from %s\n", a.pos[0], a.pos[1])
	return nil
}

// harnessImageWait is what harness.up's pending refusal carries.
type harnessImageWait struct {
	Digest       string `json:"digest"`
	Ref          string `json:"ref"`
	Stage        string `json:"stage"`
	RetryAfterMS int64  `json:"retry_after_ms"`
}

// harnessUpWaitImages calls harness.up and, while an image is in vetting,
// prints its stage and asks again after the refusal's retry_after_ms. An
// image waiting for approval, or any other refusal, ends the wait.
func harnessUpWaitImages(h harnessIO, a harnessArgs, params map[string]any) (json.RawMessage, error) {
	last := ""
	for waited := time.Duration(0); ; {
		raw, err := h.call("harness.up", params)
		var re *harnessRPCError
		if err == nil || !errors.As(err, &re) || re.Code != harness.CodeImagePending || waited >= harnessUpWaitLimit {
			return harnessCallOutResult(h, a, raw, err)
		}
		var p harnessImageWait
		if json.Unmarshal(re.Data, &p) != nil || p.Stage == string(images.StageAwaitingApproval) {
			return harnessCallOutResult(h, a, raw, err)
		}
		if line := fmt.Sprintf("image %s (%s): %s", p.Ref, images.ShortDigest(p.Digest), p.Stage); line != last && !a.json {
			_, _ = fmt.Fprintln(h.errOut, line)
			last = line
		}
		wait := time.Duration(p.RetryAfterMS) * time.Millisecond
		if wait <= 0 {
			wait = harnessPollEvery
		}
		h.sleep(wait)
		waited += wait
	}
}
