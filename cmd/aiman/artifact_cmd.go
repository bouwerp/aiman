package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
--session defaults to $AIMAN_ID. The page is published as index.html; a --file
without PATH= keeps its path relative to the page's directory.
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
