//go:build acceptance

package awslogin_test

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
)

func TestLoginHappyPath(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.tokens = []icResp{tokenErr("AuthorizationPendingException", "authorization_pending"), tokenOK()}

	l := mustStart(t, r.m, delegation("default"))
	if l.Profile != "default" || l.UserCode != testUserCode {
		t.Fatalf("pending login = %+v", l)
	}
	if !strings.Contains(l.VerificationURIComplete, "user_code="+testUserCode) {
		t.Fatalf("VerificationURIComplete = %q", l.VerificationURIComplete)
	}
	if l.ExpiresIn != 600 || !l.ExpiresAt.Equal(testStart.Add(600*time.Second)) {
		t.Fatalf("ExpiresIn %d ExpiresAt %v, want 600 and %v", l.ExpiresIn, l.ExpiresAt, testStart.Add(600*time.Second))
	}

	end := waitEnd(t, r.m, l.ID)
	if end.State != awslogin.StateDone || end.Failure != "" {
		t.Fatalf("end = %q/%q, want done", end.State, end.Failure)
	}

	reg := r.ic.requests("/client/register")
	if len(reg) != 1 || reg[0].Form["clientName"] != "aiman-serve" || reg[0].Form["clientType"] != "public" {
		t.Fatalf("register requests = %+v", reg)
	}
	da := r.ic.requests("/device_authorization")
	if len(da) != 1 || da[0].Form["startUrl"] != testStartURL || da[0].Form["clientId"] != "client-id-1" {
		t.Fatalf("device_authorization requests = %+v", da)
	}
	for _, tr := range r.ic.requests("/token") {
		if tr.Form["grantType"] != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Fatalf("token grantType = %q", tr.Form["grantType"])
		}
		if !strings.HasPrefix(tr.Form["deviceCode"], plantDeviceCode) {
			t.Fatalf("token deviceCode not the one issued")
		}
	}
	fc := r.ic.requests("/federation/credentials")
	if len(fc) != 1 || fc[0].Bearer != plantAccessToken || fc[0].Query["account_id"] != testAccount || fc[0].Query["role_name"] != awslogin.DefaultRoleName {
		t.Fatalf("GetRoleCredentials: %d calls, account %q role %q, bearer set %v",
			len(fc), queryOf(fc, "account_id"), queryOf(fc, "role_name"), len(fc) == 1 && fc[0].Bearer == plantAccessToken)
	}
	for _, region := range r.ic.askedRegions() {
		if region != testRegion {
			t.Fatalf("endpoint asked for region %q, want %q", region, testRegion)
		}
	}

	w := r.writer.written()
	if len(w) != 1 || w[0].profile != "default" {
		t.Fatalf("writer calls = %d", len(w))
	}
	c := w[0].creds
	if c.AccessKeyID != plantAccessKeyID || c.SecretAccessKey != plantSecretKey || c.SessionToken != plantSessionToken {
		t.Fatal("writer got the wrong credentials")
	}
	if !c.Expiration.Equal(r.ic.credExpires) {
		t.Fatalf("Expiration = %v, want %v (from the millisecond epoch)", c.Expiration, r.ic.credExpires)
	}

	regFile := filepath.Join(r.home, ".aiman", "aws-sso-client.json")
	st, err := os.Stat(regFile)
	if err != nil {
		t.Fatalf("registration file: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("registration file mode = %o, want 600", st.Mode().Perm())
	}

	// A second Start reuses the cached registration.
	r.ic.tokens = []icResp{tokenOK()}
	l2 := mustStart(t, r.m, delegation("default"))
	if l2.ID == l.ID {
		t.Fatal("a finished login was returned for a new start")
	}
	if got := waitEnd(t, r.m, l2.ID); got.State != awslogin.StateDone {
		t.Fatalf("second login = %q/%q", got.State, got.Failure)
	}
	if n := len(r.ic.requests("/client/register")); n != 1 {
		t.Fatalf("RegisterClient called %d times, want 1 (cached)", n)
	}

	// Finished logins are kept 10 minutes.
	if _, ok := r.m.Status(l.ID); !ok {
		t.Fatal("finished login gone before 10 minutes")
	}
	r.clock.Advance(10*time.Minute + time.Second)
	if _, ok := r.m.Status(l2.ID); ok {
		t.Fatal("finished login still listed after 10 minutes")
	}
}

func queryOf(reqs []icRequest, key string) string {
	if len(reqs) == 0 {
		return ""
	}
	return reqs[0].Query[key]
}

func TestLoginDenied(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.tokens = []icResp{tokenErr("AccessDeniedException", "access_denied")}
	l := mustStart(t, r.m, delegation("default"))
	end := waitEnd(t, r.m, l.ID)
	if end.State != awslogin.StateDenied {
		t.Fatalf("state = %q/%q, want denied", end.State, end.Failure)
	}
	if len(r.ic.requests("/federation/credentials")) != 0 || len(r.writer.written()) != 0 {
		t.Fatal("a denied login reached the portal or the writer")
	}
}

func TestLoginExpired(t *testing.T) {
	t.Run("ExpiredTokenException", func(t *testing.T) {
		r := newRig(t, true, nil)
		r.ic.tokens = []icResp{tokenErr("ExpiredTokenException", "expired_token")}
		l := mustStart(t, r.m, delegation("default"))
		if end := waitEnd(t, r.m, l.ID); end.State != awslogin.StateExpired {
			t.Fatalf("state = %q/%q, want expired", end.State, end.Failure)
		}
	})
	t.Run("expires_in passed", func(t *testing.T) {
		r := newRig(t, true, nil)
		r.ic.expiresIn, r.ic.interval = 30, 5
		l := mustStart(t, r.m, delegation("default"))
		if end := waitEnd(t, r.m, l.ID); end.State != awslogin.StateExpired {
			t.Fatalf("state = %q/%q, want expired", end.State, end.Failure)
		}
		if el := r.clock.Now().Sub(testStart); el > 35*time.Second {
			t.Fatalf("kept polling %v past a 30s expires_in", el)
		}
	})
	t.Run("10 minute cap", func(t *testing.T) {
		r := newRig(t, true, nil)
		r.ic.expiresIn, r.ic.interval = 3600, 5
		l := mustStart(t, r.m, delegation("default"))
		if end := waitEnd(t, r.m, l.ID); end.State != awslogin.StateExpired {
			t.Fatalf("state = %q/%q, want expired", end.State, end.Failure)
		}
		if el := r.clock.Now().Sub(testStart); el > 10*time.Minute+5*time.Second {
			t.Fatalf("kept polling %v, past the 10 minute cap", el)
		}
		if len(r.writer.written()) != 0 {
			t.Fatal("expired login wrote credentials")
		}
	})
}

func TestLoginSlowDown(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.interval = 1
	r.ic.tokens = []icResp{
		tokenErr("SlowDownException", "slow_down"),
		tokenErr("SlowDownException", "slow_down"),
		tokenOK(),
	}
	l := mustStart(t, r.m, delegation("default"))
	if end := waitEnd(t, r.m, l.ID); end.State != awslogin.StateDone {
		t.Fatalf("state = %q/%q, want done", end.State, end.Failure)
	}
	tr := r.ic.requests("/token")
	if len(tr) != 3 {
		t.Fatalf("CreateToken called %d times, want 3", len(tr))
	}
	if g := tr[1].At.Sub(tr[0].At); g != 6*time.Second {
		t.Fatalf("gap after first slow_down = %v, want 6s", g)
	}
	if g := tr[2].At.Sub(tr[1].At); g != 11*time.Second {
		t.Fatalf("gap after second slow_down = %v, want 11s", g)
	}
}

func TestLoginRoleDenied(t *testing.T) {
	for _, errType := range []struct {
		name   string
		status int
	}{{"ForbiddenException", http.StatusForbidden}, {"UnauthorizedException", http.StatusUnauthorized}} {
		t.Run(errType.name, func(t *testing.T) {
			r := newRig(t, true, nil)
			r.ic.tokens = []icResp{tokenOK()}
			r.ic.portal = []icResp{{status: errType.status, errType: errType.name}}
			l := mustStart(t, r.m, delegation("default"))
			end := waitEnd(t, r.m, l.ID)
			if end.State != awslogin.StateFailed || end.Failure != awslogin.FailureSSORoleDenied {
				t.Fatalf("end = %q/%q, want failed/sso_role_denied", end.State, end.Failure)
			}
			if len(r.writer.written()) != 0 {
				t.Fatal("writer called after a role denial")
			}
		})
	}
}

func TestLoginWriteFailed(t *testing.T) {
	r := newRig(t, true, nil)
	r.writer.err = errors.New("disk full")
	r.ic.tokens = []icResp{tokenOK()}
	l := mustStart(t, r.m, delegation("default"))
	end := waitEnd(t, r.m, l.ID)
	if end.State != awslogin.StateFailed || end.Failure != awslogin.FailureWriteFailed {
		t.Fatalf("end = %q/%q, want failed/write_failed", end.State, end.Failure)
	}
	if len(r.writer.written()) != 1 {
		t.Fatal("writer not called once")
	}
}

func TestLoginOtherFailure(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.tokens = []icResp{{status: http.StatusInternalServerError, errType: "InternalServerException"}}
	l := mustStart(t, r.m, delegation("default"))
	end := waitEnd(t, r.m, l.ID)
	if end.State != awslogin.StateFailed || end.Failure != awslogin.FailureOther {
		t.Fatalf("end = %q/%q, want failed/other", end.State, end.Failure)
	}
}

func TestLoginReregistersOnInvalidClient(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.tokens = []icResp{tokenOK()}
	first := mustStart(t, r.m, delegation("default"))
	waitEnd(t, r.m, first.ID)

	r.ic.deviceAuth = []icResp{{status: http.StatusBadRequest, errType: "InvalidClientException", oauth: "invalid_client"}}
	r.ic.tokens = []icResp{tokenOK()}
	l := mustStart(t, r.m, delegation("default"))
	if end := waitEnd(t, r.m, l.ID); end.State != awslogin.StateDone {
		t.Fatalf("state = %q/%q, want done", end.State, end.Failure)
	}
	if n := len(r.ic.requests("/client/register")); n != 2 {
		t.Fatalf("RegisterClient called %d times, want 2 (re-registered on InvalidClientException)", n)
	}
}

func TestLoginReregistersNearExpiry(t *testing.T) {
	r := newRig(t, true, nil)
	r.ic.regExpires = testStart.Add(12 * time.Hour) // within a day
	r.ic.tokens = []icResp{tokenOK()}
	first := mustStart(t, r.m, delegation("default"))
	waitEnd(t, r.m, first.ID)
	r.ic.tokens = []icResp{tokenOK()}
	second := mustStart(t, r.m, delegation("default"))
	waitEnd(t, r.m, second.ID)
	if n := len(r.ic.requests("/client/register")); n != 2 {
		t.Fatalf("RegisterClient called %d times, want 2 (registration within a day of expiry)", n)
	}
}

func TestRateLimit(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		r := newRig(t, false, nil) // logins stay pending: the clock never fires
		var ids []string
		for i := 1; i <= 3; i++ {
			ids = append(ids, mustStart(t, r.m, delegation(fmt.Sprintf("p%d", i))).ID)
		}
		again, err := r.m.Start(t.Context(), delegation("p1"))
		if err != nil || again.ID != ids[0] {
			t.Fatalf("start for pending p1 = %q, %v; want %q", again.ID, err, ids[0])
		}
		if _, err := r.m.Start(t.Context(), delegation("p4")); !errors.Is(err, awslogin.ErrRateLimited) {
			t.Fatalf("4th pending login err = %v, want ErrRateLimited", err)
		}
		if n := len(r.ic.requests("/device_authorization")); n != 3 {
			t.Fatalf("StartDeviceAuthorization called %d times, want 3", n)
		}
	})
	t.Run("starts per 10 minutes", func(t *testing.T) {
		r := newRig(t, false, func(o *awslogin.Options) { o.MaxPending = 20 })
		first := mustStart(t, r.m, delegation("p1"))
		for i := 2; i <= 6; i++ {
			mustStart(t, r.m, delegation(fmt.Sprintf("p%d", i)))
		}
		for range 3 {
			again, err := r.m.Start(t.Context(), delegation("p1"))
			if err != nil || again.ID != first.ID {
				t.Fatalf("start for pending p1 = %q, %v; want %q without counting", again.ID, err, first.ID)
			}
		}
		if _, err := r.m.Start(t.Context(), delegation("p7")); !errors.Is(err, awslogin.ErrRateLimited) {
			t.Fatalf("7th start in 10 minutes err = %v, want ErrRateLimited", err)
		}
		r.clock.Advance(10*time.Minute + time.Second)
		if _, err := r.m.Start(t.Context(), delegation("p8")); err != nil {
			t.Fatalf("start after the window: %v", err)
		}
	})
}

func TestNoSSO(t *testing.T) {
	r := newRig(t, true, nil)
	full := delegation("default")
	cases := map[string]func(*awslogin.Delegation){
		"no start url": func(d *awslogin.Delegation) { d.StartURL = "" },
		"no region":    func(d *awslogin.Delegation) { d.SSORegion = "" },
		"no account":   func(d *awslogin.Delegation) { d.AccountID = "" },
	}
	for name, strip := range cases {
		d := full
		strip(&d)
		if _, err := r.m.Start(t.Context(), d); !errors.Is(err, awslogin.ErrNoSSO) {
			t.Errorf("%s: err = %v, want ErrNoSSO", name, err)
		}
	}
	if n := r.ic.total(); n != 0 {
		t.Fatalf("Identity Center called %d times for a delegation without SSO facts", n)
	}
}

func TestLoginIDRandom(t *testing.T) {
	r := newRig(t, false, func(o *awslogin.Options) { o.MaxPending = 10 })
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := range 5 {
		l := mustStart(t, r.m, delegation(fmt.Sprintf("p%d", i)))
		if !hex32.MatchString(l.ID) {
			t.Fatalf("ID %q is not 32 lowercase hex chars", l.ID)
		}
		if seen[l.ID] {
			t.Fatalf("ID %q repeated", l.ID)
		}
		seen[l.ID] = true
	}
}

func TestNoSecretLeak(t *testing.T) {
	sink := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	prevSlog := slog.Default()
	log.SetOutput(sink)
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		slog.SetDefault(prevSlog)
	})

	var seen []string
	note := func(l awslogin.Login, err error) {
		seen = append(seen, fmt.Sprintf("%+v", l))
		if err != nil {
			seen = append(seen, err.Error())
		}
	}
	echo := " echo " + strings.Join(allPlanted, " ")

	flows := map[string]func(r *rig){
		"done":         func(r *rig) { r.ic.tokens = []icResp{tokenOK()} },
		"denied":       func(r *rig) { r.ic.tokens = []icResp{tokenErr("AccessDeniedException", "access_denied")} },
		"write failed": func(r *rig) { r.ic.tokens = []icResp{tokenOK()}; r.writer.err = errors.New("write refused") },
		"role denied": func(r *rig) {
			r.ic.tokens = []icResp{tokenOK()}
			r.ic.portal = []icResp{{status: http.StatusForbidden, rawBody: `{"__type":"ForbiddenException","message":"` + echo + `"}`}}
		},
		"token error body echoes secrets": func(r *rig) {
			r.ic.tokens = []icResp{{status: http.StatusInternalServerError, rawBody: `{"error":"server_error","error_description":"` + echo + `"}`}}
		},
	}
	for name, setup := range flows {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, true, nil)
			setup(r)
			l, err := r.m.Start(t.Context(), delegation("default"))
			note(l, err)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			end := waitEnd(t, r.m, l.ID)
			note(end, nil)
		})
	}

	// A Start that fails at device authorization with a body echoing secrets.
	t.Run("start error", func(t *testing.T) {
		r := newRig(t, true, nil)
		r.ic.deviceAuth = []icResp{{status: http.StatusInternalServerError, rawBody: `{"__type":"InternalServerException","message":"` + echo + `"}`}}
		l, err := r.m.Start(t.Context(), delegation("default"))
		note(l, err)
		if err == nil {
			t.Fatal("Start succeeded against a failing StartDeviceAuthorization")
		}
	})

	all := strings.Join(seen, "\n") + "\n" + sink.String()
	for _, s := range append(allPlanted, plantAccessKeyID) {
		if strings.Contains(all, s) {
			t.Errorf("planted secret %q leaked into a Login, an error or the log", s[:12]+"…")
		}
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) // #nosec G304 -- package source files
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), `"os/exec"`) {
			t.Errorf("%s imports os/exec: the engine must not start a child process", f)
		}
	}
}
