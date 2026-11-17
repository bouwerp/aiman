// Package images vets the container images a harness run names before the
// Docker engine sees them: it resolves each reference to a platform digest,
// fetches the image into a quarantine, scans it for known CVEs and for
// malicious content in a scanner container with no network, decides whether
// it is blocked or needs the operator's approval, and loads an approved image
// into the engine. Approval is per digest and host-wide.
package images

import (
	"slices"
	"strings"
	"time"
)

// State is where a digest stands.
type State string

const (
	// StateVetting is a digest being fetched or scanned.
	StateVetting State = "vetting"
	// StatePendingApproval is a scanned digest that waits for the operator.
	StatePendingApproval State = "pending_approval"
	// StateApproved is a digest runs may use.
	StateApproved State = "approved"
	// StateBlocked is a digest a scan blocked; only the operator's
	// accept-risk approves it.
	StateBlocked State = "blocked"
	// StateFailed is a vetting that could not finish (fetch, digest check,
	// scanner, time or space). It is never clean.
	StateFailed State = "failed"
	// StateStale is an approved digest whose SBOM re-scan found a new
	// critical CVE or a new MAL- id.
	StateStale State = "stale"
)

// Stage is the step a vetting is in, reported while it runs.
type Stage string

const (
	StageQueued           Stage = "queued"
	StageFetch            Stage = "fetch"
	StageCVE              Stage = "cve"
	StageStatic           Stage = "static"
	StageReview           Stage = "review"
	StageAwaitingApproval Stage = "awaiting_approval"
	StageLoad             Stage = "load"
)

// ApprovedBy says who approved a digest.
const (
	// ApprovedBySession is a quote from the session's terminal.
	ApprovedBySession = "session"
	// ApprovedByOperator is the TUI or the laptop CLI.
	ApprovedByOperator = "operator"
	// ApprovedByConfig is clean scans of an image harness.allowed_images lists.
	ApprovedByConfig = "config"
	// ApprovedByOperatorRun is clean scans on an operator-started run.
	ApprovedByOperatorRun = "operator_run"
)

// Model verdicts of the image review (stage 3).
const (
	VerdictClean      = "clean"
	VerdictSuspicious = "suspicious"
	VerdictMalicious  = "malicious"
)

// Signature statuses. A signature is shown in the approval prompt; it never
// skips a scan and its absence never blocks.
const (
	// SignatureAbsent: the registry holds no cosign signature for the digest.
	SignatureAbsent = "absent"
	// SignaturePresent: a cosign signature manifest exists for the digest.
	// Serve does not check it against an identity, so it is not "verified".
	SignaturePresent = "present"
	// SignatureUnknown: the lookup failed.
	SignatureUnknown = "unknown"
)

// Scan is what the scanners and the review found.
type Scan struct {
	// TrivyDB is the Trivy vulnerability DB's update time the scan used.
	TrivyDB    string   `json:"trivy_db,omitempty"`
	Critical   []string `json:"critical"`
	High       []string `json:"high"`
	StaticHits []string `json:"static_hits"`
	MalIDs     []string `json:"mal_ids"`
	// ModelVerdict is clean, suspicious or malicious; empty when no review
	// ran. An empty verdict is never clean for a session-started run.
	ModelVerdict string `json:"model_verdict,omitempty"`
	// ReviewAgent and ReviewModel ran the review.
	ReviewAgent string `json:"review_agent,omitempty"`
	ReviewModel string `json:"review_model,omitempty"`
	// Independent is false when the review fell back to the requesting
	// session's model family; nil when no review ran.
	Independent *bool `json:"independent,omitempty"`
	// ReviewReport is the kept review report on the host.
	ReviewReport string `json:"review_report,omitempty"`
	// ReviewFindings counts the review's high, medium and low findings.
	ReviewFindings [3]int `json:"review_findings,omitzero"`
	// Report is the path of the scan report on the host.
	Report string `json:"report,omitempty"`
	// SBOM is the path of the CycloneDX SBOM the re-scan reads.
	SBOM string `json:"sbom,omitempty"`
}

// Requester is a caller waiting on a digest.
type Requester struct {
	// Session is the session whose run named the image; it gets the gate.
	Session string `json:"session,omitempty"`
	// Operator is set when the operator started the run.
	Operator bool `json:"operator,omitempty"`
}

// Record is one digest in the image store.
type Record struct {
	// Digest is the platform manifest digest, the store key.
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
	// Repo is the registry repository the digest was fetched from.
	Repo string `json:"repo,omitempty"`
	// IndexDigest is the multi-platform index the ref resolved through.
	IndexDigest  string `json:"index_digest,omitempty"`
	ConfigDigest string `json:"config_digest,omitempty"`
	Platform     string `json:"platform,omitempty"`
	// Size is the manifest's config and layer sizes summed.
	Size  int64 `json:"size,omitempty"`
	State State `json:"state"`
	Stage Stage `json:"stage,omitempty"`
	// Allowed is set when the ref matched harness.allowed_images.
	Allowed    bool        `json:"allowed,omitempty"`
	Requesters []Requester `json:"requesters,omitempty"`
	Scan       Scan        `json:"scan"`
	Signature  string      `json:"signature,omitempty"`
	// Reasons say why a digest is blocked, failed or stale.
	Reasons []string `json:"reasons,omitempty"`
	// Retryable marks a failure of the host, not of the image: the next
	// caller starts the vetting again.
	Retryable     bool     `json:"retryable,omitempty"`
	ApprovedBy    string   `json:"approved_by,omitempty"`
	Approver      string   `json:"approver,omitempty"`
	Quote         string   `json:"quote,omitempty"`
	AcceptedHighs []string `json:"accepted_highs,omitempty"`
	AcceptRisk    bool     `json:"accept_risk,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	// ImageID is the engine's ID of the loaded image.
	ImageID string `json:"image_id,omitempty"`
	// LocalName is the name the loaded image carries in the engine.
	LocalName   string    `json:"local_name,omitempty"`
	At          time.Time `json:"at"`
	RescannedAt time.Time `json:"rescanned_at,omitzero"`
}

// ShortDigest is the first 12 hex digits of a sha256 digest.
func ShortDigest(d string) string {
	hex := strings.TrimPrefix(d, "sha256:")
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return hex
}

// Operator reports whether the operator asked for the digest.
func (r Record) Operator() bool {
	for _, q := range r.Requesters {
		if q.Operator {
			return true
		}
	}
	return false
}

// Sessions are the sessions that asked for the digest, in order.
func (r Record) Sessions() []string {
	var out []string
	for _, q := range r.Requesters {
		if q.Session != "" && !slices.Contains(out, q.Session) {
			out = append(out, q.Session)
		}
	}
	return out
}

// addRequester adds q unless it is there already and reports whether it did.
func (r *Record) addRequester(q Requester) bool {
	for _, have := range r.Requesters {
		if have == q {
			return false
		}
	}
	r.Requesters = append(r.Requesters, q)
	return true
}

// View is a record without the quote and the override reason, which stay in
// the store and the ledger.
func (r Record) View() Record {
	r.Quote, r.Reason = "", ""
	return r
}
