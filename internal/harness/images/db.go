package images

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Scanner databases under ~/.aiman/harness/db/.
const (
	DBTrivy  = "trivy"
	DBClamAV = "clamav"
	DBOSV    = "osv"
	DBYARA   = "yara"
)

// Database ages.
const (
	// DBMaxAge is how old the Trivy, ClamAV and OSV databases may be before
	// vetting fails.
	DBMaxAge = 7 * 24 * time.Hour
	// DBRefreshFloor is the shortest refresh interval (harness.images.db_refresh).
	DBRefreshFloor = 24 * time.Hour
	// YARARefreshEvery is how often the YARA Forge bundle is refreshed.
	YARARefreshEvery = 7 * 24 * time.Hour
)

// Sources of a database.
const (
	DBSourceRefresh = "refresh"
	DBSourceLaptop  = "laptop"
)

// freshDBs are the databases a vetting needs fresh. YARA falls back to the
// bundle in the scanner image.
var freshDBs = []string{DBTrivy, DBClamAV, DBOSV}

// ErrDBStale is a database missing or older than DBMaxAge.
var ErrDBStale = errors.New("scanner databases are not fresh")

// DBMeta is one database's provenance, kept in db/meta.json.
type DBMeta struct {
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"`
	// SHA256 is the checksum the bundle was checked against, when it had one.
	SHA256 string `json:"sha256,omitempty"`
	// Release names the YARA Forge release.
	Release string `json:"release,omitempty"`
}

// DBMetas is db/meta.json.
type DBMetas map[string]DBMeta

func metaPath(dbDir string) string { return filepath.Join(dbDir, "meta.json") }

// ReadDBMeta reads db/meta.json; a missing file is empty.
func ReadDBMeta(dbDir string) (DBMetas, error) {
	b, err := os.ReadFile(metaPath(dbDir))
	if errors.Is(err, os.ErrNotExist) {
		return DBMetas{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := DBMetas{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read %s: %w", metaPath(dbDir), err)
	}
	return m, nil
}

// setDBMeta records one database's provenance.
func setDBMeta(dbDir, name string, meta DBMeta) error {
	unlock, err := lockFile(filepath.Join(dbDir, "meta.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	all, err := ReadDBMeta(dbDir)
	if err != nil {
		all = DBMetas{}
	}
	all[name] = meta
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(dbDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(dbDir))
}

// CheckFresh returns ErrDBStale naming every database a vetting needs that
// is missing or older than DBMaxAge.
func (m DBMetas) CheckFresh(now time.Time) error {
	var bad []string
	for _, name := range freshDBs {
		meta, ok := m[name]
		switch {
		case !ok || meta.UpdatedAt.IsZero():
			bad = append(bad, name+" missing")
		case now.Sub(meta.UpdatedAt) > DBMaxAge:
			bad = append(bad, fmt.Sprintf("%s from %s", name, meta.UpdatedAt.Format(time.DateOnly)))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w (%s): the daily refresh needs a network, or push them from the laptop", ErrDBStale, strings.Join(bad, ", "))
	}
	return nil
}

// RefreshDue reports whether the network databases are older than every.
func (m DBMetas) RefreshDue(now time.Time, every time.Duration) bool {
	every = max(every, DBRefreshFloor)
	for _, name := range freshDBs {
		if now.Sub(m[name].UpdatedAt) >= every {
			return true
		}
	}
	return false
}

// DBInstall is a database bundle the laptop pushed to the host: a tar.gz of
// the database directory's contents, its sha256, and when the laptop built it.
type DBInstall struct {
	Name    string
	Archive string
	SHA256  string
	BuiltAt time.Time
}

// InstallDB checks a pushed bundle's sha256 and its age, extracts it safely
// into db/<name>.new and swaps it in. The layout is the one the refresh job
// writes: trivy/db/trivy.db, clamav/*.cvd, osv/osv-scanner/<ecosystem>/all.zip,
// yara/rules.yar.
func InstallDB(dbDir string, in DBInstall, now time.Time) error {
	if !slices.Contains([]string{DBTrivy, DBClamAV, DBOSV, DBYARA}, in.Name) {
		return fmt.Errorf("unknown database %q", in.Name)
	}
	if in.BuiltAt.IsZero() || now.Sub(in.BuiltAt) > DBMaxAge || in.BuiltAt.After(now.Add(time.Hour)) {
		return fmt.Errorf("the %s bundle is from %s: it must be under %s old", in.Name, in.BuiltAt.Format(time.RFC3339), DBMaxAge)
	}
	sum, err := fileSHA256(in.Archive)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, strings.TrimPrefix(in.SHA256, "sha256:")) {
		return fmt.Errorf("the %s bundle's sha256 is %s, not %s", in.Name, sum, in.SHA256)
	}
	next := filepath.Join(dbDir, in.Name+".new")
	_ = os.RemoveAll(next)
	f, err := os.Open(in.Archive)
	if err != nil {
		return err
	}
	err = ExtractLayer(f, next, &Budget{Bytes: 8 << 30, Entries: 200_000})
	_ = f.Close()
	if err != nil {
		_ = os.RemoveAll(next)
		return fmt.Errorf("unpack the %s bundle: %w", in.Name, err)
	}
	if err := swapDir(filepath.Join(dbDir, in.Name), next); err != nil {
		return err
	}
	return setDBMeta(dbDir, in.Name, DBMeta{UpdatedAt: in.BuiltAt, Source: DBSourceLaptop, SHA256: sum})
}

// swapDir replaces dir with next, keeping the old one until the rename.
func swapDir(dir, next string) error {
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(next, dir); err != nil {
		_ = os.Rename(old, dir)
		return err
	}
	return os.RemoveAll(old)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a bundle path the operator named
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Refresher updates the scanner databases. Trivy and freshclam run in the
// scanner image with a network and the database directory writable; the OSV
// ecosystem archives and the YARA Forge bundle are plain HTTPS downloads.
type Refresher struct {
	DBDir    string
	Image    string
	UID, GID int
	Docker   DockerCLI
	HTTP     *http.Client
	// OSVBase is the OSV bucket; empty is the public one.
	OSVBase string
	// OSVEcosystems are the ecosystems whose archives are kept.
	OSVEcosystems []string
	// YARARelease is the GitHub API URL of the latest YARA Forge release.
	YARARelease string
	Now         func() time.Time
}

// DefaultOSVEcosystems are the package ecosystems the OSV malicious-package
// list covers.
var DefaultOSVEcosystems = []string{"npm", "PyPI", "crates.io", "RubyGems", "Go", "NuGet", "Maven", "Packagist"}

const (
	defaultOSVBase     = "https://osv-vulnerabilities.storage.googleapis.com"
	defaultYARARelease = "https://api.github.com/repos/YARAHQ/yara-forge/releases/latest"
	yaraCoreAsset      = "yara-forge-rules-core.zip"
	refreshToolTimeout = 30 * time.Minute
)

func (r Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Refresher) http() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// Refresh updates the Trivy, ClamAV and OSV databases, and the YARA bundle
// when it is a week old. Each database that refreshed records its time; one
// that failed keeps its last copy and is named in the error.
func (r Refresher) Refresh(ctx context.Context) error {
	if err := os.MkdirAll(r.DBDir, 0o700); err != nil {
		return err
	}
	var errs []error
	step := func(name string, fn func(context.Context) error) {
		if err := fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return
		}
		if err := setDBMeta(r.DBDir, name, DBMeta{UpdatedAt: r.now(), Source: DBSourceRefresh}); err != nil {
			errs = append(errs, err)
		}
	}
	step(DBTrivy, r.trivy)
	step(DBClamAV, r.clamav)
	step(DBOSV, r.osv)
	meta, _ := ReadDBMeta(r.DBDir)
	if r.now().Sub(meta[DBYARA].UpdatedAt) >= YARARefreshEvery {
		if err := r.yara(ctx); err != nil {
			errs = append(errs, fmt.Errorf("yara: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r Refresher) runTool(ctx context.Context, name string, cmd ...string) error {
	if err := os.MkdirAll(filepath.Join(r.DBDir, name), 0o700); err != nil {
		return err
	}
	args := RunArgs(ContainerOptions{
		Name: "aiman-scan-refresh-" + name, Image: r.Image, UID: r.UID, GID: r.GID, Network: true,
		Mounts: []Mount{{Host: filepath.Join(r.DBDir, name), Container: ContainerDB + "/" + name}},
		Env:    []string{"HOME=/tmp"},
		Cmd:    cmd,
	})
	ctx, cancel := context.WithTimeout(ctx, refreshToolTimeout)
	defer cancel()
	out, code, err := r.Docker(ctx, args)
	if err != nil || code != 0 {
		return toolErr(name, []byte(out), code, err)
	}
	return nil
}

func (r Refresher) trivy(ctx context.Context) error {
	return r.runTool(ctx, DBTrivy, "trivy", "image", "--download-db-only", "--no-progress", "--cache-dir", ContainerDB+"/trivy")
}

func (r Refresher) clamav(ctx context.Context) error {
	return r.runTool(ctx, DBClamAV, "freshclam", "--config-file=/opt/aiman/freshclam.conf", "--datadir="+ContainerDB+"/clamav", "--stdout")
}

// osv downloads each ecosystem's all.zip into the layout osv-scanner reads
// offline: osv/osv-scanner/<ecosystem>/all.zip.
func (r Refresher) osv(ctx context.Context) error {
	base := r.OSVBase
	if base == "" {
		base = defaultOSVBase
	}
	ecos := r.OSVEcosystems
	if ecos == nil {
		ecos = DefaultOSVEcosystems
	}
	for _, eco := range ecos {
		dst := filepath.Join(r.DBDir, DBOSV, "osv-scanner", eco, "all.zip")
		if err := r.download(ctx, base+"/"+eco+"/all.zip", dst, ""); err != nil {
			return fmt.Errorf("%s: %w", eco, err)
		}
	}
	return nil
}

// yara fetches the latest YARA Forge core bundle, checks it against the
// sha256 GitHub publishes for the asset, and keeps the last bundle on any
// failure.
func (r Refresher) yara(ctx context.Context) error {
	url := r.YARARelease
	if url == "" {
		url = defaultYARARelease
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := r.getJSON(ctx, url, &rel); err != nil {
		return err
	}
	for _, a := range rel.Assets {
		if a.Name != yaraCoreAsset {
			continue
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || len(sum) != 64 {
			return fmt.Errorf("release %s publishes no sha256 for %s", rel.TagName, a.Name)
		}
		zipPath := filepath.Join(r.DBDir, DBYARA+".zip.part")
		defer func() { _ = os.Remove(zipPath) }()
		if err := r.download(ctx, a.BrowserDownloadURL, zipPath, sum); err != nil {
			return err
		}
		if err := unzipRules(zipPath, filepath.Join(r.DBDir, DBYARA, "rules.yar")); err != nil {
			return err
		}
		return setDBMeta(r.DBDir, DBYARA, DBMeta{UpdatedAt: r.now(), Source: DBSourceRefresh, SHA256: sum, Release: rel.TagName})
	}
	return fmt.Errorf("release %s has no %s", rel.TagName, yaraCoreAsset)
}

func (r Refresher) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.http().Do(req) //nolint:gosec // G107: fixed release URLs
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// download writes url to dst through a temporary file, checking sum when set.
func (r Refresher) download(ctx context.Context, url, dst, sum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.http().Do(req) //nolint:gosec // G107: fixed database URLs
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // G304: a database path
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum != "" && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(sum) {
		err = fmt.Errorf("%s: sha256 %x does not match %s", url, h.Sum(nil), sum)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// unzipRules copies the core rules file out of the YARA Forge zip.
func unzipRules(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "yara-rules-core.yar") || f.UncompressedSize64 > 256<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			_ = rc.Close()
			return err
		}
		tmp := dst + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // G304: the rules path
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 256<<20))
		_ = rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, dst)
	}
	return errors.New("the YARA Forge zip has no yara-rules-core.yar")
}
