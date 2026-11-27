package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/artifact/kit"
	"github.com/bouwerp/aiman/internal/harness/awssig"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

func artifactUsage() error {
	fmt.Fprint(os.Stderr, `Usage:
  aiman artifact publish <page.html> [--file [PATH=]SRC ...] [--title T] [--theme plex|grotesk|signal]
                         [--id ID] [--expect-version N] [--session NAME]
  aiman artifact list
  aiman artifact get <id>
  aiman artifact share <id> [--email E ...] [--domain D ...]            (operator)
  aiman artifact unshare <id> [--email E ...] [--domain D ...] [--all]  (operator)
  aiman artifact delete <id>                                            (operator)
  aiman artifact setup [--account ID] [--zone Z] [--host H] [--bucket B] [--team T]
                       [--idp ID] [--owner EMAIL ...] [--viewer EMAIL ...]
                       [--viewer-domain D ...] [--cf-oauth]            (operator, once)
  aiman artifact setup --host-key                                       (operator, per host)
--session defaults to $AIMAN_ID. The page is published as index.html; a --file
without PATH= keeps its path relative to the page's directory.
setup reads the Cloudflare API token from stdin, or with --cf-oauth the cf
CLI's OAuth token; missing flags come from the artifacts config. --host-key
reads {"access_key_id","secret_access_key"} from stdin into
~/.aiman/artifacts-r2.json (0600).
`)
	return errUsage
}

func runArtifact(args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return artifactUsage()
	}
	switch args[0] {
	case "publish":
		params, err := artifactPublishParams(args[1:], getenv)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return artifactUsage()
		}
		return callArtifact("artifact.publish", params)
	case "list":
		if len(args) != 1 {
			return artifactUsage()
		}
		return callArtifact("artifact.list", map[string]any{})
	case "get":
		if len(args) != 2 || args[1] == "" {
			return artifactUsage()
		}
		return callArtifact("artifact.get", map[string]any{"id": args[1]})
	case "share", "unshare", "delete":
		params, err := artifactShareParams(args[0], args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return artifactUsage()
		}
		if err := callArtifact("artifact."+args[0], params); err != nil {
			return err
		}
		if emails, _ := params["emails"].([]string); args[0] == "share" && len(emails) > 0 {
			fmt.Fprintln(os.Stderr, "Access admits only setup's viewers: run aiman artifact setup with --viewer for anyone new.")
		}
		return nil
	case "setup":
		return runArtifactSetup(args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	return artifactUsage()
}

// artifactPublishParams maps the publish arguments onto artifact.publish.
// Relative sources are made absolute from the caller's directory, which is
// the session's.
func artifactPublishParams(args []string, getenv func(string) string) (map[string]any, error) {
	params := map[string]any{}
	var (
		page  string
		files []map[string]string
	)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		if wd, err := os.Getwd(); err == nil {
			return filepath.Join(wd, p)
		}
		return p
	}
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if !strings.HasPrefix(a, "--") {
			if page != "" {
				return nil, fmt.Errorf("one page only, got %q and %q", page, a)
			}
			page = a
			continue
		}
		key, val, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !inline {
			if len(args) == 0 {
				return nil, fmt.Errorf("--%s needs a value", key)
			}
			val, args = args[0], args[1:]
		}
		switch key {
		case "file":
			dest, src, ok := strings.Cut(val, "=")
			if !ok {
				dest, src = "", val
			}
			if src == "" {
				return nil, fmt.Errorf("--file %q has no source", val)
			}
			f := map[string]string{"from": abs(src)}
			if dest != "" {
				f["path"] = dest
			}
			files = append(files, f)
		case "title", "theme", "id", "session":
			params[key] = val
		case "expect-version":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("--expect-version %q is not a version number", val)
			}
			params["expect_version"] = n
		default:
			return nil, fmt.Errorf("unknown flag --%s", key)
		}
	}
	if page == "" {
		return nil, errors.New("a page is required")
	}
	params["page"] = abs(page)
	if len(files) > 0 {
		params["files"] = files
	}
	if s, _ := params["session"].(string); strings.TrimSpace(s) == "" {
		if id := strings.TrimSpace(getenv("AIMAN_ID")); id != "" {
			params["session"] = id
		} else {
			delete(params, "session")
		}
	}
	return params, nil
}

func callArtifact(method string, params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, method, raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodePublishFailed
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return writeJSON(resp.Result)
}

// artifactKeyFile holds the host's bucket-scoped R2 key, never pushed from
// the laptop.
const artifactKeyFile = "artifacts-r2.json"

// newArtifactPublisher builds serve's publisher from the artifacts config and
// the R2 key file under dir (~/.aiman). It returns nil, with the reason, when
// either is missing, so the artifact methods answer unavailable.
func newArtifactPublisher(cfg config.ArtifactsConfig, dir string, db *sql.DB, canWrite func(owner, caller string) bool) (*artifact.Publisher, string) {
	if !cfg.Configured() {
		return nil, "artifacts.host, account_id and bucket are not all set"
	}
	keyPath := filepath.Join(dir, artifactKeyFile)
	fi, err := os.Stat(keyPath)
	if err != nil {
		return nil, "no R2 key: " + err.Error()
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Sprintf("%s is mode %o; it must be 0600", keyPath, fi.Mode().Perm())
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, "read R2 key: " + err.Error()
	}
	var key struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	if err := json.Unmarshal(raw, &key); err != nil || key.AccessKeyID == "" || key.SecretAccessKey == "" {
		return nil, keyPath + " needs access_key_id and secret_access_key"
	}
	store, err := artifact.OpenSQLStore(db)
	if err != nil {
		return nil, err.Error()
	}
	kitFiles, err := artifactKit(kit.FS)
	if err != nil {
		return nil, "theme kit: " + err.Error()
	}
	return &artifact.Publisher{
		Bucket: artifact.NewR2(artifact.R2Endpoint(strings.TrimSpace(cfg.AccountID)), strings.TrimSpace(cfg.Bucket),
			awssig.Credentials{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey}, nil),
		Store:    store,
		Host:     strings.TrimSpace(cfg.Host),
		Kit:      kitFiles,
		CanWrite: canWrite,
	}, ""
}

// artifactKit reads every kit file at its path relative to kit/.
func artifactKit(fsys fs.FS) ([]artifact.File, error) {
	var out []artifact.File
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out = append(out, artifact.File{Path: p, Data: data})
		return nil
	})
	return out, err
}

// artifactShareParams maps share, unshare and delete arguments onto their
// RPC params. --email and --domain repeat; unshare also takes --all.
func artifactShareParams(verb string, args []string) (map[string]any, error) {
	var (
		id              string
		emails, domains []string
		all             bool
	)
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if !strings.HasPrefix(a, "--") {
			if id != "" {
				return nil, fmt.Errorf("one id only, got %q and %q", id, a)
			}
			id = a
			continue
		}
		key, val, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if key == "all" && verb == "unshare" && !inline {
			all = true
			continue
		}
		if verb == "delete" || (key != "email" && key != "domain") {
			return nil, fmt.Errorf("unknown flag --%s", key)
		}
		if !inline {
			if len(args) == 0 {
				return nil, fmt.Errorf("--%s needs a value", key)
			}
			val, args = args[0], args[1:]
		}
		if key == "email" {
			emails = append(emails, val)
		} else {
			domains = append(domains, val)
		}
	}
	if id == "" {
		return nil, errors.New("an artifact id is required")
	}
	params := map[string]any{"id": id}
	switch verb {
	case "share":
		if len(emails)+len(domains) == 0 {
			return nil, errors.New("share needs --email or --domain")
		}
	case "unshare":
		if !all && len(emails)+len(domains) == 0 {
			return nil, errors.New("unshare needs --email, --domain or --all")
		}
		if all {
			params["all"] = true
		}
	}
	if len(emails) > 0 {
		params["emails"] = emails
	}
	if len(domains) > 0 {
		params["domains"] = domains
	}
	return params, nil
}

// artifactSetupArgs is the parsed setup command line.
type artifactSetupArgs struct {
	cfg     artifact.SetupConfig
	cfOAuth bool
	hostKey bool
}

func parseArtifactSetup(args []string, cfg config.ArtifactsConfig) (artifactSetupArgs, error) {
	var out artifactSetupArgs
	c := &out.cfg
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if !strings.HasPrefix(a, "--") {
			return out, fmt.Errorf("unexpected argument %q", a)
		}
		key, val, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch key {
		case "cf-oauth", "host-key":
			if inline {
				return out, fmt.Errorf("--%s takes no value", key)
			}
			if key == "cf-oauth" {
				out.cfOAuth = true
			} else {
				out.hostKey = true
			}
			continue
		case "token", "api-token":
			return out, errors.New("the Cloudflare token is read from stdin or --cf-oauth, never from the command line")
		}
		if !inline {
			if len(args) == 0 {
				return out, fmt.Errorf("--%s needs a value", key)
			}
			val, args = args[0], args[1:]
		}
		switch key {
		case "account":
			c.AccountID = val
		case "zone":
			c.Zone = val
		case "host":
			c.Host = val
		case "bucket":
			c.Bucket = val
		case "team":
			c.TeamDomain = val
		case "idp":
			c.IdentityProvider = val
		case "owner":
			c.Owners = append(c.Owners, val)
		case "viewer":
			c.Viewers = append(c.Viewers, val)
		case "viewer-domain":
			c.ViewerDomains = append(c.ViewerDomains, val)
		default:
			return out, fmt.Errorf("unknown flag --%s", key)
		}
	}
	if out.hostKey {
		if out.cfOAuth || c.AccountID+c.Zone+c.Host+c.Bucket+c.TeamDomain+c.IdentityProvider != "" ||
			len(c.Owners)+len(c.Viewers)+len(c.ViewerDomains) > 0 {
			return out, errors.New("--host-key takes no other flag")
		}
		return out, nil
	}
	pick := func(flag *string, conf, def string) {
		if strings.TrimSpace(*flag) == "" {
			*flag = strings.TrimSpace(conf)
		}
		if *flag == "" {
			*flag = def
		}
	}
	pick(&c.AccountID, cfg.AccountID, "")
	pick(&c.Host, cfg.Host, "")
	pick(&c.Zone, c.Host, "")
	pick(&c.Bucket, cfg.Bucket, artifact.SetupWorkerName)
	pick(&c.TeamDomain, cfg.Team, "")
	pick(&c.IdentityProvider, cfg.IdentityProvider, "")
	if len(c.Owners) == 0 {
		c.Owners = cfg.Owners
	}
	if t := strings.TrimSpace(c.TeamDomain); t != "" && !strings.Contains(t, ".") {
		c.TeamDomain = t + ".cloudflareaccess.com"
	}
	var missing []string
	for flag, v := range map[string]string{"--account": c.AccountID, "--host": c.Host, "--team": c.TeamDomain, "--idp": c.IdentityProvider} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, flag)
		}
	}
	if len(c.Owners) == 0 {
		missing = append(missing, "--owner")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return out, fmt.Errorf("setup needs %s (or artifacts.* in the config)", strings.Join(missing, ", "))
	}
	return out, nil
}

// runArtifactSetup configures Cloudflare, or with --host-key stores the
// host's R2 key. The token never reaches argv, the environment or output.
func runArtifactSetup(args []string, in io.Reader, out, errOut io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var conf config.ArtifactsConfig
	if cfg, err := config.Load(); err == nil {
		conf = cfg.Artifacts
	}
	conf, err = config.HostArtifacts(home, conf)
	if err != nil {
		fmt.Fprintf(errOut, "aiman artifact setup: warning: %v\n", err)
	}
	parsed, err := parseArtifactSetup(args, conf)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return artifactUsage()
	}
	if parsed.hostKey {
		if isTerminal(in) {
			fmt.Fprint(errOut, `Paste {"access_key_id":"…","secret_access_key":"…"}, then ctrl+d: `)
		}
		path := filepath.Join(home, config.DirName, artifactKeyFile)
		if err := writeArtifactHostKey(path, in); err != nil {
			writeCLIError(server.CodeInvalidParams, err.Error())
			return err
		}
		return writeJSON(map[string]any{"type": "artifact_host_key", "path": path})
	}
	var token string
	if parsed.cfOAuth {
		token, err = setupCFToken(home, parsed.cfg.AccountID)
	} else {
		token, err = readSecretLine(in, errOut, "Cloudflare API token: ")
	}
	if err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	api := &artifact.CloudflareAPI{Base: artifact.CloudflareBase, Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := artifact.Setup(ctx, api, parsed.cfg, errOut)
	if err != nil {
		writeCLIError(server.CodePublishFailed, err.Error())
		return err
	}
	saved := hostArtifactsFrom(parsed, conf.Auth)
	if err := config.SaveHostArtifacts(home, saved); err != nil {
		writeCLIError(server.CodePublishFailed, "save "+config.HostArtifactsPath(home)+": "+err.Error())
		return err
	}
	return writeJSON(map[string]any{
		"type": "artifact_setup", "app_id": res.AppID, "aud": res.AUD, "policy_id": res.PolicyID,
		"config": config.HostArtifactsPath(home),
	})
}

// hostArtifactsFrom is what setup saves to the host's artifacts.yaml. With
// --cf-oauth the auth is pinned to cf_oauth; otherwise an earlier choice
// stays, and unset lets serve pick from the credentials it finds.
func hostArtifactsFrom(p artifactSetupArgs, auth string) config.ArtifactsConfig {
	if p.cfOAuth {
		auth = config.ArtifactsAuthCFOAuth
	}
	team := strings.TrimSuffix(p.cfg.TeamDomain, ".cloudflareaccess.com")
	return config.ArtifactsConfig{
		Host: p.cfg.Host, AccountID: p.cfg.AccountID, Bucket: p.cfg.Bucket,
		Owners: p.cfg.Owners, Team: team, IdentityProvider: p.cfg.IdentityProvider, Auth: auth,
	}
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readSecretLine reads a token: without echo from a terminal, else the first
// line of stdin.
func readSecretLine(in io.Reader, errOut io.Writer, prompt string) (string, error) {
	var raw string
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(errOut, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(errOut)
		if err != nil {
			return "", err
		}
		raw = string(b)
	} else {
		line, err := bufio.NewReader(io.LimitReader(in, 64<<10)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		raw = line
	}
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", errors.New("no Cloudflare API token on stdin")
	}
	return token, nil
}

// setupCFToken takes the cf CLI's OAuth token. With cf on PATH it renews an
// expiring token through `cf auth whoami`, as serve does; without it, it
// refuses an expired one.
func setupCFToken(home, account string) (string, error) {
	path := cfConfigPath(home)
	cf, err := exec.LookPath("cf")
	if err != nil {
		return cfOAuthToken(path, time.Now())
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfRefreshTimeout+5*time.Second)
	defer cancel()
	return artifact.CFOAuthToken(path, cfRefresher(cf, account))(ctx)
}

// cfOAuthToken reads the cf CLI's OAuth token, refusing one past its
// expiration_time.
func cfOAuthToken(path string, now time.Time) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cf OAuth token: %w", err)
	}
	var f struct {
		Token   string `json:"oauth_token"`
		Expires string `json:"expiration_time"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.Token) == "" {
		return "", fmt.Errorf("cf OAuth token: %s has no oauth_token", path)
	}
	if t, err := time.Parse(time.RFC3339, f.Expires); err == nil && !now.Before(t) {
		return "", fmt.Errorf("cf OAuth token expired at %s: log in with the cf CLI again", t.Format(time.RFC3339))
	}
	return strings.TrimSpace(f.Token), nil
}

// writeArtifactHostKey checks the R2 key JSON on in and writes it to path
// 0600 through a temporary file and a rename.
func writeArtifactHostKey(path string, in io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return err
	}
	var key struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return errors.New(`stdin is not {"access_key_id","secret_access_key"} JSON`)
	}
	key.AccessKeyID, key.SecretAccessKey = strings.TrimSpace(key.AccessKeyID), strings.TrimSpace(key.SecretAccessKey)
	if key.AccessKeyID == "" || key.SecretAccessKey == "" {
		return errors.New("access_key_id and secret_access_key are both required")
	}
	body, err := json.Marshal(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifacts-r2-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
