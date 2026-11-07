package awslogin

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// defaultPollInterval applies when the authorization names none (RFC 8628).
	defaultPollInterval = 5 * time.Second
	slowDownStep        = 5 * time.Second
)

func pollInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultPollInterval
	}
	return time.Duration(seconds) * time.Second
}

// outcome is how a login ended. detail is an apiError's text and goes only to
// the log line.
type outcome struct {
	state, failure, detail string
}

// failedOther is the outcome for an error that has no code of its own. Only an
// apiError's text is kept, since nothing else is known to be free of secrets.
func failedOther(err error) outcome {
	out := outcome{state: StateFailed, failure: FailureOther}
	var ae *apiError
	if errors.As(err, &ae) {
		out.detail = ae.Error()
	}
	return out
}

// flow is one login's secrets and poll position. It lives only as long as its
// goroutine; the Manager's records hold nothing secret.
type flow struct {
	m          *Manager
	id         string
	d          Delegation
	reg        registration
	deviceCode string
	interval   time.Duration
	deadline   time.Time
}

func (f *flow) run(ctx context.Context) {
	defer f.m.wg.Done()
	out := f.execute(ctx)
	f.deviceCode, f.reg = "", registration{}
	f.m.finish(f.id, out)
}

// execute keeps the access token and role credentials in locals, so they are
// gone by the time run records the end state.
func (f *flow) execute(ctx context.Context) outcome {
	token, out, ok := f.poll(ctx)
	if !ok {
		return out
	}
	f.m.setState(f.id, StateApproved)
	f.m.setState(f.id, StateMinting)
	creds, err := f.m.getRoleCredentials(ctx, f.d, token)
	if err != nil {
		if s := apiStatus(err); s == http.StatusUnauthorized || s == http.StatusForbidden {
			return outcome{state: StateFailed, failure: FailureSSORoleDenied}
		}
		return failedOther(err)
	}
	if f.m.o.Writer == nil {
		return outcome{state: StateFailed, failure: FailureWriteFailed, detail: "no writer"}
	}
	if err := f.m.o.Writer.WriteHostCredentials(f.d.Profile, creds); err != nil {
		return outcome{state: StateFailed, failure: FailureWriteFailed}
	}
	return outcome{state: StateDone}
}

// poll calls CreateToken until it returns a token (ok) or the login ends.
func (f *flow) poll(ctx context.Context) (string, outcome, bool) {
	for {
		remaining := f.deadline.Sub(f.m.o.Now())
		if remaining <= 0 {
			return "", outcome{state: StateExpired}, false
		}
		select {
		case <-f.m.o.After(min(f.interval, remaining)):
		case <-ctx.Done():
			return "", failedOther(ctx.Err()), false
		}
		if !f.m.o.Now().Before(f.deadline) {
			return "", outcome{state: StateExpired}, false
		}
		token, err := f.m.createToken(ctx, f.d, f.reg, f.deviceCode)
		if err == nil {
			return token, outcome{}, true
		}
		switch apiCode(err) {
		case codePending:
		case codeSlowDown:
			f.interval += slowDownStep
		case codeAccessDenied:
			return "", outcome{state: StateDenied}, false
		case codeExpiredToken:
			return "", outcome{state: StateExpired}, false
		case codeInvalidClient:
			f.m.forgetRegistration(f.reg)
			return "", failedOther(err), false
		default:
			return "", failedOther(err), false
		}
	}
}
