package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"github.com/bouwerp/aiman/internal/infra/ssh"
)

// slackConsentPrefix is the only URL a remote login may open in the laptop's
// browser; the host prints it, so anything else on its stderr stays text.
const slackConsentPrefix = "https://slack.com/oauth/v2/authorize?"

// slackRemoteAiman is the host's aiman, where remote installs put it.
const slackRemoteAiman = "$HOME/.local/bin/aiman"

// slackRemoteGrace lets the host report its own timeout before the laptop
// kills the ssh process.
const slackRemoteGrace = 30 * time.Second

// slackSafeDuration matches a duration string that needs no shell quoting.
var slackSafeDuration = regexp.MustCompile(`^[0-9.a-z]+$`)

// slackLoginTimeout parses --timeout (default 5m) and returns it with the
// text to hand to a host login.
func slackLoginTimeout(flags map[string]string) (time.Duration, string, error) {
	raw := flags["timeout"]
	if raw == "" {
		return 5 * time.Minute, "5m", nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, "", errors.New("timeout must be a duration like 5m")
	}
	if !slackSafeDuration.MatchString(raw) {
		raw = d.String()
	}
	return d, raw, nil
}

// slackRemoteTarget finds --remote in the laptop config, ignoring case.
func slackRemoteTarget(name string) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("cannot read the laptop config for remote %s: %v", name, err)
	}
	for _, r := range cfg.Remotes {
		if strings.EqualFold(r.Name, name) {
			return ssh.Target(r.Host, r.User), nil
		}
	}
	return "", fmt.Errorf("no remote named %s in ~/.aiman/config.yaml", name)
}

// runSlackAuthLoginRemote logs in on the host over one ssh process that
// forwards the OAuth redirect port. The host exchanges and stores the token;
// the laptop only opens the consent URL and relays output.
func runSlackAuthLoginRemote(ctx context.Context, name, clientID string, timeout time.Duration, timeoutArg string, out, errOut io.Writer) error {
	target, err := slackRemoteTarget(name)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	ln, err := net.Listen("tcp", slack.ListenAddr)
	if err != nil {
		return slackFail(errOut, "slack_params", "cannot forward "+slack.ListenAddr+
			": another login or process holds the port")
	}
	_ = ln.Close()
	_, remotePort, _ := net.SplitHostPort(slack.DefaultListenAddr)
	base := []string{"-o", "BatchMode=yes", "-o", "ForwardAgent=no"}
	if clientID != "" {
		if err := slackRemoteClientID(ctx, base, target, clientID, errOut); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+slackRemoteGrace)
	defer cancel()
	args := append(append([]string{}, base...), "-o", "ExitOnForwardFailure=yes",
		"-L", slack.ListenAddr+":127.0.0.1:"+remotePort, target,
		slackRemoteAiman+" slack auth login --timeout "+timeoutArg)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = out
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return slackFail(errOut, "slack_api", "ssh to "+target+" did not start: "+err.Error())
	}
	opened := false
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		_, _ = fmt.Fprintln(errOut, line)
		if !opened && strings.HasPrefix(line, slackConsentPrefix) {
			opened = true
			if err := slack.BrowserOpen(line); err != nil {
				_, _ = fmt.Fprintln(errOut, "Could not open a browser on this machine. Open the URL yourself.")
			}
		}
	}
	err = cmd.Wait()
	if err != nil && ctx.Err() != nil {
		return slackFail(errOut, "slack_timeout", "login on "+target+" did not finish within "+timeoutArg)
	}
	return slackRemoteExit(err, target, errOut)
}

// slackRemoteClientID saves the client id on the host with auth app, the id
// on stdin so it never reaches an ssh argv.
func slackRemoteClientID(ctx context.Context, base []string, target, clientID string, errOut io.Writer) error {
	args := append(append([]string{}, base...), target, slackRemoteAiman+" slack auth app")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(strings.TrimSpace(clientID) + "\n")
	cmd.Stdout = io.Discard
	cmd.Stderr = errOut
	return slackRemoteExit(cmd.Run(), target, errOut)
}

// slackRemoteExit turns an ssh result into the command's error. The host's
// stderr is already relayed; exit 255 is ssh itself failing.
func slackRemoteExit(err error, target string, errOut io.Writer) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() != 255 {
		return errUsage
	}
	return slackFail(errOut, "slack_api", "ssh to "+target+" failed: "+err.Error())
}
