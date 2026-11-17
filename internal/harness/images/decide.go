package images

import (
	"fmt"
	"strings"
)

// CVE block levels (harness.images.cve_block).
const (
	BlockCritical = "critical"
	BlockHigh     = "high"
)

// DecideInput is what a decision weighs besides the scan.
type DecideInput struct {
	// CVEBlock is critical (default) or high: a CVE at or above it blocks.
	CVEBlock string
	// Allowed is set when the ref matches harness.allowed_images.
	Allowed bool
	// Operator is set when the operator started the run.
	Operator bool
	// Builtin is set for an image aiman's own code pins by digest.
	Builtin bool
}

// Decision is the outcome of a finished vetting.
type Decision struct {
	State State
	// ApprovedBy is builtin, config or operator_run for an image approved at
	// once.
	ApprovedBy string
	// Reasons say why the digest is blocked.
	Reasons []string
}

// Decide turns a scan into blocked, approved at once or pending_approval:
//
//   - blocked: a CVE at or above CVEBlock, any static hit, any MAL- id, or the
//     verdict malicious;
//   - approved at once: a builtin image with no block and no suspicious
//     verdict, high CVEs included (builtin: aiman's release pinned the
//     digest); else no high CVE, no static hit, a clean verdict, and the
//     image is listed (config) or the operator started the run (operator_run);
//   - pending_approval otherwise.
//
// A missing verdict (no review ran) counts as clean only for an
// operator-started run, whose call is itself the operator's question; a
// session-started run, listed or not, then always asks. The verdict can only
// add risk: clean never removes a CVE or a static hit.
func Decide(scan Scan, in DecideInput) Decision {
	if reasons := blockReasons(scan, in.CVEBlock); len(reasons) > 0 {
		return Decision{State: StateBlocked, Reasons: reasons}
	}
	if in.Builtin && scan.ModelVerdict != VerdictSuspicious {
		return Decision{State: StateApproved, ApprovedBy: ApprovedByBuiltin}
	}
	verdictOK := scan.ModelVerdict == VerdictClean || (scan.ModelVerdict == "" && in.Operator)
	if len(scan.High) == 0 && verdictOK {
		switch {
		case in.Operator:
			return Decision{State: StateApproved, ApprovedBy: ApprovedByOperatorRun}
		case in.Allowed:
			return Decision{State: StateApproved, ApprovedBy: ApprovedByConfig}
		}
	}
	return Decision{State: StatePendingApproval}
}

func blockReasons(scan Scan, cveBlock string) []string {
	var reasons []string
	if len(scan.Critical) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d critical CVE (%s)", len(scan.Critical), firstIDs(scan.Critical, 20)))
	}
	if cveBlock == BlockHigh && len(scan.High) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d high CVE (%s)", len(scan.High), firstIDs(scan.High, 20)))
	}
	if len(scan.StaticHits) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d static malware hit (%s)", len(scan.StaticHits), firstIDs(scan.StaticHits, 5)))
	}
	if len(scan.MalIDs) > 0 {
		reasons = append(reasons, fmt.Sprintf("malicious package (%s)", firstIDs(scan.MalIDs, 20)))
	}
	if scan.ModelVerdict == VerdictMalicious {
		reasons = append(reasons, "image review verdict malicious")
	}
	return reasons
}

// firstIDs joins up to n ids and says how many more there are.
func firstIDs(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:n], ", ") + fmt.Sprintf(" and %d more", len(ids)-n)
}

// NewIDs returns the ids in now that were not in before.
func NewIDs(before, now []string) []string {
	seen := map[string]bool{}
	for _, id := range before {
		seen[id] = true
	}
	var out []string
	for _, id := range now {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}
