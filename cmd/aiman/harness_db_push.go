package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	harnessscanner "github.com/bouwerp/aiman/build/harness-scanner"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/shellx"
)

const harnessDBPushUsage = `Usage: aiman harness image db-push [--remote NAME] [trivy|clamav|osv|yara ...]   (laptop)

Builds the scanner database bundles on this machine (Trivy and ClamAV in the
pinned scanner image under the local Docker, OSV and YARA Forge over HTTPS),
copies each to the remote over ssh and runs db-install there. With no names it
pushes all four.
`

// Timeouts of one db-push step. Building the image and fetching a database
// carry the refresher's own limits.
const (
	dbPushUploadTimeout  = time.Hour
	dbPushInstallTimeout = 15 * time.Minute
	dbPushRemoteDir      = "$HOME/.aiman/tmp"
	dbPushRemoteAiman    = "$HOME/.local/bin/aiman"
)

// dbPushAll is every database, in push order.
var dbPushAll = []string{images.DBTrivy, images.DBClamAV, images.DBOSV, images.DBYARA}

// dbPushRemote is the ssh manager's part db-push uses.
type dbPushRemote interface {
	ExecuteWithTimeout(ctx context.Context, cmd string, timeout time.Duration) (string, error)
	UploadFile(ctx context.Context, local, path string, timeout time.Duration) error
}

type dbPushDeps struct {
	remote dbPushRemote
	// docker runs the laptop's docker CLI.
	docker images.DockerCLI
	// buildArgs are harness.images.scanners' overrides of the pinned versions.
	buildArgs map[string]string
	// refresher carries the fetch settings (HTTP client, OSV and YARA
	// sources); db-push fills DBDir, Image, Docker, UID and GID.
	refresher images.Refresher
	// tempParent holds db-push's temporary directory; empty is os.TempDir.
	tempParent string
	now        func() time.Time
	out        io.Writer
	errOut     io.Writer
}

// runHarnessDBPush is `aiman harness image db-push` on the laptop.
func runHarnessDBPush(args []string) error {
	a, err := parseHarnessArgs(args)
	if err == nil {
		_, err = dbPushNames(a.pos)
	}
	if err != nil || a.session != "" || a.run != "" || a.json || a.force || a.wait {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Fprint(os.Stderr, harnessDBPushUsage)
		return errUsage
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("read the laptop config: %w", err)
	}
	remote, err := pickConsoleRemote(cfg.Remotes, cfg.ActiveRemote, a.remote)
	if err != nil {
		return err
	}
	buildArgs, warns := cfg.Harness.Images.ScannerBuildArgs()
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "config: %s\n", w)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	return harnessDBPush(ctx, a.pos, dbPushDeps{
		remote: mgr, docker: images.ExecDockerCLI, buildArgs: buildArgs,
		refresher: images.Refresher{HTTP: &http.Client{Timeout: 30 * time.Minute}},
		now:       time.Now, out: os.Stdout, errOut: os.Stderr,
	})
}

// dbPushNames checks and orders the named databases; none is all four.
func dbPushNames(pos []string) ([]string, error) {
	if len(pos) == 0 {
		return dbPushAll, nil
	}
	var names []string
	for _, n := range pos {
		if !slices.Contains(dbPushAll, n) {
			return nil, fmt.Errorf("unknown database %q: name trivy, clamav, osv or yara", n)
		}
	}
	for _, n := range dbPushAll {
		if slices.Contains(pos, n) {
			names = append(names, n)
		}
	}
	return names, nil
}

// harnessDBPush builds, copies and installs each named database in turn. A
// failed database is reported and the rest still go. Everything it writes on
// the laptop lives in one temporary directory removed on every path; the
// remote copy of a bundle is removed after db-install, whatever its result.
func harnessDBPush(ctx context.Context, pos []string, d dbPushDeps) error {
	names, err := dbPushNames(pos)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(d.tempParent, "aiman-db-push-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	r := d.refresher
	r.DBDir, r.Docker, r.UID, r.GID = filepath.Join(tmp, "db"), d.docker, os.Getuid(), os.Getgid()
	var imageErr error
	if slices.Contains(names, images.DBTrivy) || slices.Contains(names, images.DBClamAV) {
		_, _ = fmt.Fprintln(d.errOut, "building the scanner image with the local docker")
		scanner := &images.ScannerImage{Docker: d.docker, Dockerfile: harnessscanner.Dockerfile, BuildArgs: d.buildArgs, Dir: tmp}
		r.Image, imageErr = scanner.Ensure(ctx)
	}
	if _, err := d.remote.ExecuteWithTimeout(ctx, "mkdir -p "+shellx.QuoteHomePath(dbPushRemoteDir)+" && chmod 700 "+shellx.QuoteHomePath(dbPushRemoteDir), 0); err != nil {
		return fmt.Errorf("prepare %s on the remote: %w", dbPushRemoteDir, err)
	}
	var errs []error
	for _, name := range names {
		if imageErr != nil && (name == images.DBTrivy || name == images.DBClamAV) {
			err = fmt.Errorf("scanner image: %w", imageErr)
		} else {
			err = pushOneDB(ctx, d, r, tmp, name)
		}
		if err != nil {
			_, _ = fmt.Fprintf(d.errOut, "failed %s: %v\n", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}

// pushOneDB fetches one database, bundles it, copies the bundle and runs
// db-install with the bundle's sha256 and build time.
func pushOneDB(ctx context.Context, d dbPushDeps, r images.Refresher, tmp, name string) error {
	_, _ = fmt.Fprintf(d.errOut, "fetching %s\n", name)
	dir := filepath.Join(r.DBDir, name)
	defer func() { _ = os.RemoveAll(dir) }()
	if err := r.Fetch(ctx, name); err != nil {
		return err
	}
	built := d.now().UTC().Truncate(time.Second)
	bundle := filepath.Join(tmp, name+".tar.gz")
	defer func() { _ = os.Remove(bundle) }()
	sum, size, err := writeDBBundle(dir, bundle)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	remotePath := fmt.Sprintf("%s/harness-db-%s-%d.tar.gz", dbPushRemoteDir, name, built.Unix())
	quoted := shellx.QuoteHomePath(remotePath)
	_, _ = fmt.Fprintf(d.errOut, "copying %s (%s)\n", name, formatBytes(size))
	if err := d.remote.UploadFile(ctx, bundle, remotePath, dbPushUploadTimeout); err != nil {
		_, _ = d.remote.ExecuteWithTimeout(context.WithoutCancel(ctx), "rm -f "+quoted+" "+shellx.QuoteHomePath(remotePath+".part"), 0)
		return err
	}
	cmd := shellx.QuoteHomePath(dbPushRemoteAiman) + " harness image db-install " + shellx.Quote(name) + " " + quoted +
		" --sha256 " + sum + " --built-at " + built.Format(time.RFC3339) +
		"; rc=$?; rm -f " + quoted + "; exit $rc"
	if out, err := d.remote.ExecuteWithTimeout(ctx, cmd, dbPushInstallTimeout); err != nil {
		return fmt.Errorf("db-install on the remote: %w", err)
	} else if out != "" {
		_, _ = fmt.Fprintf(d.errOut, "remote: %s\n", out)
	}
	_, _ = fmt.Fprintf(d.out, "pushed %s: %s, sha256 %s, built %s\n", name, formatBytes(size), sum, built.Format(time.RFC3339))
	return nil
}

// writeDBBundle writes dir's contents as the tar.gz db-install unpacks into
// db/<name>: names relative to dir, directories and regular files only. It
// answers the archive's sha256 and size.
func writeDBBundle(dir, dst string) (string, int64, error) {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: a path in db-push's temp dir
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	gz := gzip.NewWriter(cw)
	tw := tar.NewWriter(gz)
	files := 0
	err = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: rel + "/", Mode: 0o700, ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			files++
			return addBundleFile(tw, p, rel, info)
		}
		return nil
	})
	if err == nil && files == 0 {
		err = fmt.Errorf("%s is empty", dir)
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

func addBundleFile(tw *tar.Writer, path, rel string, info fs.FileInfo) error {
	src, err := os.Open(path) //nolint:gosec // G304: a file the refresher fetched
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: rel, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, src, info.Size())
	return err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// formatBytes prints a size in MiB, or bytes under one MiB.
func formatBytes(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
