package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const (
	// AWSLoginStartTimeout bounds one aws.login_start: client registration and
	// device authorization, each limited to 30s by the engine.
	AWSLoginStartTimeout = 60 * time.Second
	// AWSStatusTimeout bounds aws.status {check:true}, which runs the host aws
	// CLI once per delegated profile.
	AWSStatusTimeout = 60 * time.Second

	awsCheckPerProfile = 10 * time.Second
	awsCheckCacheTTL   = 60 * time.Second

	awsLoginWatchEvery = 2 * time.Second
	// awsLoginWatchLimit outlasts the engine's 10-minute polling limit plus the
	// mint and write that follow an approval.
	awsLoginWatchLimit = 15 * time.Minute
)

// aws.status row states.
const (
	awsStatusValid     = "valid"
	awsStatusExpired   = "expired"
	awsStatusNotPushed = "not_pushed"
	awsStatusUnmanaged = "unmanaged"
)

// AWSLogin is the device-flow engine serve's aws.* handlers drive.
// *awslogin.Manager implements it.
type AWSLogin interface {
	Start(ctx context.Context, d awslogin.Delegation) (awslogin.Login, error)
	Status(id string) (awslogin.Login, bool)
}

// awsState is what SetAWS wires. A nil state means the methods answer unavailable.
type awsState struct {
	delegations     []config.ServeAWSDelegation
	credentialsPath string
	login           AWSLogin
	watchEvery      time.Duration

	// watching holds the login ids with a finish watcher, so a start that gets
	// a pending login back records nothing new.
	watchMu  sync.Mutex
	watching map[string]struct{}

	// checking admits one get-caller-identity pass at a time; a call that loses
	// answers from the cache instead of waiting for the CLI.
	checking atomic.Bool
	checkMu  sync.Mutex
	checks   map[string]awsCheckResult
}

// awsCheckResult is one cached get-caller-identity outcome. modTime ties it to
// the file it judged, so a new write makes it stale at once.
type awsCheckResult struct {
	status  string
	at      time.Time
	modTime time.Time
}

// awsProfileStatus is one aws.status row. It holds no credential value.
type awsProfileStatus struct {
	Profile      string `json:"profile"`
	Status       string `json:"status"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ExpiryApprox bool   `json:"expiry_approx,omitempty"`
	SSOReady     bool   `json:"sso_ready"`
	// modTime is set only on a delegated profile with a section on the host, the
	// rows a check may judge.
	modTime time.Time
}

// awsLoginView is the wire form of a login. Only aws.login_start fills the user
// code and approval URL.
type awsLoginView struct {
	LoginID                 string `json:"login_id"`
	Profile                 string `json:"profile"`
	State                   string `json:"state"`
	Failure                 string `json:"failure,omitempty"`
	ExpiresAt               string `json:"expires_at,omitempty"`
	UserCode                string `json:"user_code,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in,omitempty"`
}

// SetAWS wires the aws.* methods: cfg is serve's delegated list,
// credentialsPath the host credentials file, login the device-flow engine.
// Without it the methods answer unavailable.
func (s *Server) SetAWS(cfg config.ServeAWSConfig, credentialsPath string, login AWSLogin) {
	s.aws = &awsState{
		delegations:     cfg.Delegations,
		credentialsPath: credentialsPath,
		login:           login,
		watchEvery:      awsLoginWatchEvery,
		watching:        map[string]struct{}{},
		checks:          map[string]awsCheckResult{},
	}
}

// dispatchAWS routes the phone's AWS sign-in methods.
func (s *Server) dispatchAWS(ctx context.Context, req Request) Response {
	switch req.Method {
	case "aws.status":
		return s.handleAWSStatus(ctx, req)
	case "aws.login_start":
		return s.handleAWSLoginStart(ctx, req)
	default:
		return s.handleAWSLoginStatus(req)
	}
}

func awsUnavailable(req Request) Response {
	return errResp(req.ID, CodeUnavailable, "aws: no delegations on this host")
}

func (s *Server) handleAWSStatus(ctx context.Context, req Request) Response {
	a := s.aws
	if a == nil {
		return awsUnavailable(req)
	}
	var p struct {
		Check bool `json:"check"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	rows, err := a.statusRows(time.Now())
	if err != nil {
		log.Printf("aws.status: %v", err)
		return errResp(req.ID, CodeUnavailable, "aws credentials file unavailable")
	}
	if p.Check {
		s.applyAWSChecks(ctx, rows)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "aws_status", "profiles": rows}}
}

func (s *Server) handleAWSLoginStart(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may start an AWS sign-in")
	}
	a := s.aws
	if a == nil {
		return awsUnavailable(req)
	}
	var p struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, "invalid params")
	}
	d, ok := a.delegation(p.Profile)
	if !ok {
		return errResp(req.ID, CodeInvalidParams, "profile is not delegated on this host")
	}
	if !awsSSOReady(d) {
		return errResp(req.ID, CodeAWSNoSSO, "profile has no Identity Center settings")
	}
	startCtx, cancel := context.WithTimeout(ctx, AWSLoginStartTimeout)
	defer cancel()
	login, err := a.login.Start(startCtx, awslogin.Delegation{
		Profile:   d.Profile,
		StartURL:  d.SSOStartURL,
		SSORegion: d.SSORegion,
		AccountID: d.SSOAccountID,
		RoleName:  d.SSORoleName,
	})
	switch {
	case errors.Is(err, awslogin.ErrRateLimited):
		return errResp(req.ID, CodeAWSRateLimited, "too many AWS sign-ins; try again in a few minutes")
	case errors.Is(err, awslogin.ErrNoSSO):
		return errResp(req.ID, CodeAWSNoSSO, "profile has no Identity Center settings")
	case err != nil:
		log.Printf("aws.login_start %s: %v", d.Profile, err)
		return errResp(req.ID, CodeUnavailable, "AWS sign-in could not start; see the serve log")
	}
	s.recordAWSLoginStart(login)
	view := awsLoginStatusView(login)
	view.UserCode = login.UserCode
	view.VerificationURIComplete = login.VerificationURIComplete
	view.ExpiresIn = login.ExpiresIn
	return Response{ID: req.ID, Result: map[string]any{"type": "aws_login", "login": view}}
}

func (s *Server) handleAWSLoginStatus(req Request) Response {
	a := s.aws
	if a == nil {
		return awsUnavailable(req)
	}
	var p struct {
		LoginID string `json:"login_id"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.LoginID == "" {
		return errResp(req.ID, CodeInvalidParams, "login_id is required")
	}
	login, ok := a.login.Status(p.LoginID)
	if !ok {
		return errResp(req.ID, CodeNotFound, "login not found")
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "aws_login", "login": awsLoginStatusView(login)}}
}

// awsLoginStatusView is safe for a session caller: it never holds the user code
// or the approval URL.
func awsLoginStatusView(l awslogin.Login) awsLoginView {
	v := awsLoginView{LoginID: l.ID, Profile: l.Profile, State: l.State, Failure: l.Failure}
	if !l.ExpiresAt.IsZero() {
		v.ExpiresAt = l.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return v
}

func awsSSOReady(d config.ServeAWSDelegation) bool {
	return d.SSOStartURL != "" && d.SSORegion != "" && d.SSOAccountID != ""
}

func (a *awsState) delegation(profile string) (config.ServeAWSDelegation, bool) {
	if profile == "" {
		return config.ServeAWSDelegation{}, false
	}
	for _, d := range a.delegations {
		if d.Profile == profile {
			return d, true
		}
	}
	return config.ServeAWSDelegation{}, false
}

// statusRows is the union of the delegations and the file's sections: delegated
// profiles in config order, then the file's other sections in file order.
func (a *awsState) statusRows(now time.Time) ([]awsProfileStatus, error) {
	sections, err := awsdelegation.ReadHostCredentialProfiles(a.credentialsPath)
	if err != nil {
		return nil, err
	}
	onHost := make(map[string]awsdelegation.HostProfile, len(sections))
	for _, sec := range sections {
		onHost[sec.Name] = sec
	}
	rows := make([]awsProfileStatus, 0, len(a.delegations)+len(sections))
	seen := make(map[string]struct{}, len(a.delegations))
	for _, d := range a.delegations {
		if _, dup := seen[d.Profile]; dup || d.Profile == "" {
			continue
		}
		seen[d.Profile] = struct{}{}
		rows = append(rows, delegatedAWSRow(d, onHost, now))
	}
	for _, sec := range sections {
		if _, delegated := seen[sec.Name]; !delegated {
			rows = append(rows, strayAWSRow(sec, now))
		}
	}
	return rows, nil
}

func delegatedAWSRow(d config.ServeAWSDelegation, onHost map[string]awsdelegation.HostProfile, now time.Time) awsProfileStatus {
	row := awsProfileStatus{Profile: d.Profile, SSOReady: awsSSOReady(d)}
	sec, ok := onHost[d.Profile]
	if !ok {
		row.Status = awsStatusNotPushed
		return row
	}
	row.modTime = sec.ModTime
	if sec.HasExpiry {
		row.setExpiry(sec.ExpiresAt, now)
		return row
	}
	seconds := d.DurationSeconds
	if seconds <= 0 {
		seconds = awsdelegation.DefaultDurationSeconds
	}
	row.setExpiry(sec.ModTime.Add(time.Duration(seconds)*time.Second), now)
	row.ExpiryApprox = true
	return row
}

func strayAWSRow(sec awsdelegation.HostProfile, now time.Time) awsProfileStatus {
	row := awsProfileStatus{Profile: sec.Name}
	if !sec.HasExpiry {
		row.Status = awsStatusUnmanaged
		return row
	}
	row.setExpiry(sec.ExpiresAt, now)
	return row
}

func (r *awsProfileStatus) setExpiry(at, now time.Time) {
	r.ExpiresAt = at.UTC().Format(time.RFC3339)
	r.Status = awsStatusValid
	if !at.After(now) {
		r.Status = awsStatusExpired
	}
}

// applyAWSChecks overlays get-caller-identity results on the file-derived rows.
func (s *Server) applyAWSChecks(ctx context.Context, rows []awsProfileStatus) {
	a := s.aws
	if a.checking.CompareAndSwap(false, true) {
		defer a.checking.Store(false)
		s.refreshAWSChecks(ctx, rows)
	}
	now := time.Now()
	for i := range rows {
		if status, ok := a.cachedCheck(rows[i], now); ok {
			rows[i].Status = status
		}
	}
}

// refreshAWSChecks checks each delegated profile on the host in turn. With no
// aws on the host it does nothing, because CheckCredentials reads any non-zero
// exit as expired, "command not found" included.
func (s *Server) refreshAWSChecks(ctx context.Context, rows []awsProfileStatus) {
	a := s.aws
	ctx, cancel := context.WithTimeout(ctx, AWSStatusTimeout)
	defer cancel()
	if !s.awsCLIPresent(ctx) {
		return
	}
	for _, row := range rows {
		if row.modTime.IsZero() {
			continue
		}
		if _, fresh := a.cachedCheck(row, time.Now()); fresh {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if status, ok := s.checkAWSProfile(ctx, row.Profile); ok {
			a.storeCheck(row, status)
		}
	}
}

func (s *Server) awsCLIPresent(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, awsCheckPerProfile)
	defer cancel()
	out, err := s.remote.Execute(ctx, "command -v aws")
	return err == nil && strings.TrimSpace(out) != ""
}

// checkAWSProfile answers false when the check says nothing about the tokens,
// which leaves the file-derived status. The error text embeds CLI output, so
// only its sentinel is read.
func (s *Server) checkAWSProfile(ctx context.Context, profile string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, awsCheckPerProfile)
	defer cancel()
	err := awsdelegation.CheckCredentials(ctx, s.remote, profile)
	switch {
	case err == nil:
		return awsStatusValid, true
	case errors.Is(err, awsdelegation.ErrCredentialsExpired):
		return awsStatusExpired, true
	default:
		return "", false
	}
}

func (a *awsState) cachedCheck(row awsProfileStatus, now time.Time) (string, bool) {
	if row.modTime.IsZero() {
		return "", false
	}
	a.checkMu.Lock()
	defer a.checkMu.Unlock()
	c, ok := a.checks[row.Profile]
	if !ok || !c.modTime.Equal(row.modTime) || now.Sub(c.at) >= awsCheckCacheTTL {
		return "", false
	}
	return c.status, true
}

func (a *awsState) storeCheck(row awsProfileStatus, status string) {
	a.checkMu.Lock()
	defer a.checkMu.Unlock()
	a.checks[row.Profile] = awsCheckResult{status: status, at: time.Now(), modTime: row.modTime}
}

// recordAWSLoginStart writes aws.login_started and watches for the login's end.
// A login the engine handed back again is already recorded and watched. The
// ledger holds no login id: a session caller could poll a login with it.
func (s *Server) recordAWSLoginStart(login awslogin.Login) {
	a := s.aws
	a.watchMu.Lock()
	_, dup := a.watching[login.ID]
	if !dup {
		a.watching[login.ID] = struct{}{}
	}
	a.watchMu.Unlock()
	if dup {
		return
	}
	s.appendEvent(context.Background(), domain.AWSLoginLedger, domain.EventActorUser, domain.EventAWSLoginStarted, map[string]any{
		"profile": login.Profile,
		"state":   login.State,
	})
	go s.watchAWSLogin(login.ID)
}

// watchAWSLogin polls the engine, which has no finish callback, and records the
// end state once. A serve that exits first ends the login with no finished row.
func (s *Server) watchAWSLogin(id string) {
	a := s.aws
	defer func() {
		a.watchMu.Lock()
		delete(a.watching, id)
		a.watchMu.Unlock()
	}()
	tick := time.NewTicker(a.watchEvery)
	defer tick.Stop()
	limit := time.NewTimer(awsLoginWatchLimit)
	defer limit.Stop()
	for {
		select {
		case <-limit.C:
			log.Printf("aws login watcher: gave up after %s", awsLoginWatchLimit)
			return
		case <-tick.C:
		}
		login, ok := a.login.Status(id)
		if !ok {
			return
		}
		if !awsLoginEnded(login.State) {
			continue
		}
		payload := map[string]any{"profile": login.Profile, "state": login.State}
		if login.Failure != "" {
			payload["failure"] = login.Failure
		}
		s.appendEvent(context.Background(), domain.AWSLoginLedger, domain.EventActorUser, domain.EventAWSLoginFinished, payload)
		return
	}
}

func awsLoginEnded(state string) bool {
	switch state {
	case awslogin.StateDone, awslogin.StateDenied, awslogin.StateExpired, awslogin.StateFailed:
		return true
	}
	return false
}
