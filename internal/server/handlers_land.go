package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/verify"
	"github.com/bouwerp/aiman/internal/worktree"
)

// SessionLandTimeout bounds session.land, including the wait for another
// child landing on the same branch.
const SessionLandTimeout = 5 * time.Minute

// session.land refusals.
const (
	// CodeLandParentDiverged: the coordinator's worktree is not on its
	// branch, is ahead of origin or has tracked changes.
	CodeLandParentDiverged = "land_parent_diverged"
	// CodeLandConflict: the child does not merge cleanly; data lists files.
	CodeLandConflict = "land_conflict"
	// CodeLandBusy: the parent branch kept moving while the merge was pushed.
	CodeLandBusy = "land_busy"
)

// LandConflictData is the data of a land_conflict refusal.
type LandConflictData struct {
	Files []string `json:"files"`
}

// LandGate answers whether the repository's local CI gate passed for a
// commit (piece A's cigate). applies is false when the repository has no
// gate list.
type LandGate interface {
	PassedFor(ctx context.Context, child domain.Session, sha string) (applies, passed bool, err error)
}

// landDeps is what session.land needs beyond the session store.
type landDeps struct {
	dir  string
	gate LandGate
}

// SetLandDir sets the directory holding land locks and scratch worktrees.
// Empty is ~/.aiman/land.
func (s *Server) SetLandDir(dir string) { s.land.dir = dir }

// SetLandGate sets the local CI gate session.land checks. nil skips it.
func (s *Server) SetLandGate(g LandGate) { s.land.gate = g }

func (s *Server) landBase() string {
	if s.land.dir != "" {
		return s.land.dir
	}
	home := hostHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aiman", "land")
}

// handleSessionLand merges a child's HEAD onto its parent's branch and pushes
// the merge, so the child needs no PR and costs no CI run.
func (s *Server) handleSessionLand(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, SessionLandTimeout)
	defer cancel()
	var p struct {
		Session string `json:"session"`
		ID      string `json:"id"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	target := cmp.Or(p.Session, p.ID, req.Caller)
	if target == "" {
		return errResp(req.ID, CodeInvalidParams, "session is required: aiman land runs from a child session")
	}
	// The scope check runs on the resolved id, so a coordinator can land its
	// child by name.
	child, ok := s.findSession(ctx, target)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	if resp, refused := s.refuseOutOfScope(ctx, req, child.ID); refused {
		return resp
	}
	if child.ParentID == "" || child.Role.Hidden() || child.FixPR != "" {
		return errResp(req.ID, CodeInvalidParams, "aiman land runs only from a child session")
	}
	parent, ok := s.findSession(ctx, child.ParentID)
	if !ok {
		return errResp(req.ID, CodeInvalidParams, "the child's coordinator is gone")
	}
	spec, code, err := s.landSpec(ctx, child, parent)
	if err != nil {
		return errResp(req.ID, code, err.Error())
	}
	res, err := git.Land(ctx, spec)
	if err != nil {
		return s.landRefusal(req.ID, child, parent, err)
	}
	s.recordLanded(ctx, child, parent, res.MergeSHA)
	return Response{ID: req.ID, Result: map[string]any{
		"type":      "landed",
		"session":   child.ID,
		"branch":    landTarget(child, parent),
		"merge_sha": res.MergeSHA,
	}}
}

// landTarget is the branch a child lands on: the base it was cut from,
// else its coordinator's branch. A coordinator can move to a per-project
// base branch after its own session was created, so its session branch is
// not always where its children belong.
func landTarget(child, parent domain.Session) string {
	return cmp.Or(child.BaseBranch, parent.Branch)
}

// landSpec runs the checks before a land, in order: the report validates, the
// child's tree is clean, verify passed for HEAD when the host verifies before
// a PR, and the local gate passed for HEAD when the repository has one. On
// failure it returns the response code.
func (s *Server) landSpec(ctx context.Context, child, parent domain.Session) (git.LandSpec, string, error) {
	target := landTarget(child, parent)
	if target == "" || parent.WorktreePath == "" {
		return git.LandSpec{}, CodeInvalidParams, errors.New("the coordinator has no branch or worktree")
	}
	dir := cmp.Or(child.WorktreePath, child.WorkingDirectory)
	if dir == "" {
		return git.LandSpec{}, CodeInvalidParams, errors.New("the child has no worktree")
	}
	info := worktree.Inspect(ctx, dir)
	if info.Kind == worktree.KindNone || info.CommonDir == "" {
		return git.LandSpec{}, CodeInvalidParams, fmt.Errorf("%s is not a git checkout", dir)
	}
	mainClone := mainCloneOf(info.CommonDir)
	def, err := git.DefaultBranch(ctx, mainClone)
	if err != nil {
		return git.LandSpec{}, CodeInvalidParams, err
	}
	if target == def {
		return git.LandSpec{}, CodeInvalidParams, fmt.Errorf("land never targets the default branch %s; open a PR instead", def)
	}
	if err := s.landReady(ctx, child, info); err != nil {
		return git.LandSpec{}, CodeInvalidParams, err
	}
	sha, err := headSHA(ctx, info.Root)
	if err != nil {
		return git.LandSpec{}, CodeInvalidParams, err
	}
	if err := s.landReviewed(ctx, child, info.Root, sha); err != nil {
		return git.LandSpec{}, CodeInvalidParams, err
	}
	base := s.landBase()
	if base == "" {
		return git.LandSpec{}, CodeUnavailable, errors.New("no home directory for land locks")
	}
	repo := cmp.Or(parent.RepoName, child.RepoName)
	if repo == "" {
		repo = filepath.Base(mainClone)
	}
	lockName, err := git.LandLockName(repo, target)
	if err != nil {
		return git.LandSpec{}, CodeInvalidParams, err
	}
	return git.LandSpec{
		MainClone:      mainClone,
		ParentWorktree: parent.WorktreePath,
		ParentBranch:   target,
		DefaultBranch:  def,
		ChildSHA:       sha,
		ScratchDir:     filepath.Join(base, child.ID),
		LockPath:       filepath.Join(base, lockName),
		CommitterName:  "Aiman",
		CommitterEmail: "aiman@localhost",
		Message:        fmt.Sprintf("Land %s on %s", sessionLabel(child), target),
	}, "", nil
}

// landReady checks the child's report and that its tree holds nothing the
// merge would leave behind; aiman's scaffolding does not count.
func (s *Server) landReady(ctx context.Context, child domain.Session, info worktree.Info) error {
	reportDir := cmp.Or(child.WorkingDirectory, info.Root)
	raw, err := os.ReadFile(filepath.Join(reportDir, domain.AimanReportFileName))
	if err != nil {
		return fmt.Errorf("read %s: %w", domain.AimanReportFileName, err)
	}
	if err := coordinator.ValidateReport(string(raw)); err != nil {
		return fmt.Errorf("%s: %w", domain.AimanReportFileName, err)
	}
	loss, err := worktree.Pending(ctx, info)
	if err != nil {
		return fmt.Errorf("read the child's tree: %w", err)
	}
	if loss.ChangedFiles > 0 {
		return fmt.Errorf("commit or remove %d changed or untracked files first: %s", loss.ChangedFiles, strings.Join(loss.Paths, ", "))
	}
	return nil
}

// landReviewed checks verify, when the host verifies before a PR, and the
// local gate, when the repository has one, for the commit about to land.
func (s *Server) landReviewed(ctx context.Context, child domain.Session, root, sha string) error {
	if s.verifier != nil && s.verifier.BeforePR() {
		tree, err := verify.TreeHash(ctx, root)
		if err != nil {
			return fmt.Errorf("hash the child's tree: %w", err)
		}
		r, ok := s.verifier.ForTree(child.ID, tree)
		if !ok || !r.Passed() {
			return errors.New("verify has not passed for HEAD; run aiman verify --wait")
		}
	}
	if s.land.gate == nil {
		return nil
	}
	applies, passed, err := s.land.gate.PassedFor(ctx, child, sha)
	if err != nil {
		return fmt.Errorf("local gate: %w", err)
	}
	if applies && !passed {
		return fmt.Errorf("the local gate has not passed for %s; run aiman ci local --wait", shortSHA(sha))
	}
	return nil
}

// landRefusal maps a Land error to its response, steering the coordinator
// when its worktree blocks the land.
func (s *Server) landRefusal(id string, child, parent domain.Session, err error) Response {
	var conflict *git.LandConflictError
	switch {
	case errors.As(err, &conflict):
		return Response{ID: id, Error: &Error{Code: CodeLandConflict,
			Message: err.Error() + "; merge origin/" + landTarget(child, parent) + " into your branch and land again",
			Data:    LandConflictData{Files: conflict.Files}}}
	case errors.Is(err, git.ErrLandParentDiverged):
		s.Steerer().Enqueue(SteerNote{SessionID: parent.ID, Author: domain.AuthorAiman,
			Text: fmt.Sprintf("[aiman] child %s is waiting to land on %s: put your worktree on that branch, level with origin, with no tracked changes.", sessionLabel(child), landTarget(child, parent))})
		return errResp(id, CodeLandParentDiverged, err.Error())
	case errors.Is(err, git.ErrLandBusy):
		return errResp(id, CodeLandBusy, err.Error())
	case errors.Is(err, git.ErrLandDefaultBranch):
		return errResp(id, CodeInvalidParams, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return errResp(id, CodeTimeout, err.Error())
	default:
		return errResp(id, CodeCreateFailed, err.Error())
	}
}

// recordLanded writes child.landed on both ledgers and tells the coordinator.
func (s *Server) recordLanded(ctx context.Context, child, parent domain.Session, sha string) {
	target := landTarget(child, parent)
	payload := map[string]any{"child": child.ID, "parent_branch": target, "merge_sha": sha}
	s.AppendLedger(ctx, child.ID, domain.EventChildLanded, payload)
	s.AppendLedger(ctx, parent.ID, domain.EventChildLanded, payload)
	s.Steerer().Enqueue(SteerNote{SessionID: parent.ID, Author: domain.AuthorAiman,
		Text: fmt.Sprintf("[aiman] child %s landed on %s at %s. Run git pull --ff-only.", sessionLabel(child), target, shortSHA(sha))})
}

// LandedLocal reports whether the child's latest child.landed merge is on
// origin/<landTarget>, fetching in the parent's main clone first. It is
// coordinator.Tracker.LandedLocal.
func (s *Server) LandedLocal(ctx context.Context, parent, child domain.Session) (bool, error) {
	sha, err := s.latestLanded(ctx, child.ID)
	target := landTarget(child, parent)
	if err != nil || sha == "" || target == "" {
		return false, err
	}
	info := worktree.Inspect(ctx, parent.WorktreePath)
	if info.CommonDir == "" {
		return false, fmt.Errorf("%s is not a git checkout", parent.WorktreePath)
	}
	return git.LandedOn(ctx, mainCloneOf(info.CommonDir), target, sha)
}

// latestLanded returns the merge sha of the child's newest child.landed row.
func (s *Server) latestLanded(ctx context.Context, childID string) (string, error) {
	lister, ok := s.repo.(sessionEventLister)
	if !ok {
		return "", errors.New("session ledger is not available")
	}
	var sha string
	var since int64
	for {
		events, err := lister.ListSessionEvents(ctx, childID, since, eventPageSize)
		if err != nil {
			return "", err
		}
		for _, ev := range events {
			since = ev.Seq
			if ev.Kind != domain.EventChildLanded {
				continue
			}
			var p struct {
				Child    string `json:"child"`
				MergeSHA string `json:"merge_sha"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Child == childID && p.MergeSHA != "" {
				sha = p.MergeSHA
			}
		}
		if len(events) < eventPageSize {
			return sha, nil
		}
	}
}

// mainCloneOf is the main clone owning a git common directory: the parent of
// a .git directory, or the directory itself for a bare clone.
func mainCloneOf(commonDir string) string {
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return commonDir
}

func headSHA(ctx context.Context, dir string) (string, error) {
	// #nosec G204 -- fixed git subcommand; dir is an argv element.
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func sessionLabel(s domain.Session) string {
	return cmp.Or(s.Name, s.ID)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
