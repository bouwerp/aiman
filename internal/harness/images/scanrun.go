package images

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Scanner tools. A plan names the ones a stage runs; a tool that is missing
// from the scanner image fails the stage.
const (
	ToolTrivy  = "trivy"
	ToolClamAV = "clamav"
	ToolYARA   = "yara"
	ToolOSV    = "osv"
)

// Scan stages the scanner container runs, one container each.
const (
	ScanStageCVE    = "cve"
	ScanStageStatic = "static"
)

// Paths inside the scanner container.
const (
	ContainerIn      = "/in"
	ContainerScratch = "/scratch"
	ContainerDB      = "/db"
	ContainerAiman   = "/aiman"
	// BundledYARA is the YARA Forge core bundle the scanner image carries;
	// /db/yara/rules.yar, when the refresh job wrote one, wins.
	BundledYARA = "/opt/aiman/yara/rules.yar"
)

// ClamAV limits per scan.
const (
	clamMaxFileSize  = "200M"
	clamMaxScanSize  = "800M"
	clamMaxRecursion = "16"
	clamMaxFiles     = "200000"
)

// ScanPlan is what serve asks one scanner container to do. It is written to
// <scratch>/plan.json; the container runs `aiman harness scan`.
type ScanPlan struct {
	Stage string   `json:"stage"`
	Tools []string `json:"tools"`
	// MaxBytes and MaxEntries bound what extraction writes to the scratch.
	MaxBytes   int64 `json:"max_bytes"`
	MaxEntries int   `json:"max_entries"`
	// ToolTimeout bounds each tool.
	ToolTimeout time.Duration `json:"tool_timeout"`
	// In, Scratch and DB default to the container paths (tests set them).
	In      string `json:"in,omitempty"`
	Scratch string `json:"scratch,omitempty"`
	DB      string `json:"db,omitempty"`
}

// StageResult is one stage's findings, written to <scratch>/out/<stage>.json.
type StageResult struct {
	Stage      string   `json:"stage"`
	TrivyDB    string   `json:"trivy_db,omitempty"`
	Critical   []string `json:"critical,omitempty"`
	High       []string `json:"high,omitempty"`
	StaticHits []string `json:"static_hits,omitempty"`
	MalIDs     []string `json:"mal_ids,omitempty"`
	// Error is set when the stage could not finish; the vetting then fails.
	Error string `json:"error,omitempty"`
}

// ToolExec runs one scanner tool and returns its stdout and exit code; err is
// set only when the tool could not run.
type ToolExec func(ctx context.Context, name string, args, env []string) (stdout []byte, exit int, err error)

// ExecTool runs a tool from PATH.
func ExecTool(ctx context.Context, name string, args, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: fixed scanner commands with paths serve chose
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), 0, nil
	case ctx.Err() != nil:
		return out.Bytes(), -1, fmt.Errorf("%s: %w", name, ctx.Err())
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		if ee.ExitCode() > 1 {
			out.WriteString("\n" + lastLines(errb.String()))
		}
		return out.Bytes(), ee.ExitCode(), nil
	default:
		return nil, -1, fmt.Errorf("%s: %w", name, err)
	}
}

func lastLines(s string) string {
	const n = 5
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// RunScan runs one stage of plan inside the scanner container and writes
// its StageResult. It returns the result too; a stage error is in
// result.Error and the returned error stays nil once the result is written.
func RunScan(ctx context.Context, plan ScanPlan, run ToolExec) (StageResult, error) {
	plan = plan.withDefaults()
	res := StageResult{Stage: plan.Stage}
	out := filepath.Join(plan.Scratch, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return res, err
	}
	var err error
	switch plan.Stage {
	case ScanStageCVE:
		err = runCVE(ctx, plan, run, &res)
	case ScanStageStatic:
		err = runStatic(ctx, plan, run, &res)
	case ScanStageSBOM:
		err = runSBOM(ctx, plan, run, &res)
	default:
		err = fmt.Errorf("unknown scan stage %q", plan.Stage)
	}
	if err != nil {
		res.Error = err.Error()
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		return res, merr
	}
	return res, os.WriteFile(filepath.Join(out, plan.Stage+".json"), b, 0o600)
}

func (p ScanPlan) withDefaults() ScanPlan {
	if p.In == "" {
		p.In = ContainerIn
	}
	if p.Scratch == "" {
		p.Scratch = ContainerScratch
	}
	if p.DB == "" {
		p.DB = ContainerDB
	}
	if p.MaxEntries <= 0 {
		p.MaxEntries = DefaultMaxEntries
	}
	if p.ToolTimeout <= 0 {
		p.ToolTimeout = 10 * time.Minute
	}
	return p
}

func (p ScanPlan) has(tool string) bool {
	for _, t := range p.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

func (p ScanPlan) sbom() string { return filepath.Join(p.Scratch, "out", "sbom.cdx.json") }

// tool runs one tool under the plan's time limit.
func (p ScanPlan) tool(ctx context.Context, run ToolExec, name string, args, env []string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, p.ToolTimeout)
	defer cancel()
	return run(ctx, name, args, env)
}

func trivyCommon(p ScanPlan) []string {
	return []string{"--cache-dir", filepath.Join(p.DB, "trivy"), "--cache-backend", "memory",
		"--skip-db-update", "--skip-java-db-update", "--offline-scan", "--quiet"}
}

// runCVE writes the layout as an OCI archive, makes the CycloneDX SBOM with
// Trivy, then scans the SBOM for vulnerabilities, so a later re-scan of the
// SBOM reads the same packages.
func runCVE(ctx context.Context, p ScanPlan, run ToolExec, res *StageResult) error {
	if !p.has(ToolTrivy) {
		return errors.New("the cve stage needs trivy")
	}
	archive := filepath.Join(p.Scratch, "image.tar")
	if err := tarDir(p.In, archive, p.MaxBytes); err != nil {
		return fmt.Errorf("archive the quarantine: %w", err)
	}
	defer func() { _ = os.Remove(archive) }()
	args := append([]string{"image", "--input", archive, "--format", "cyclonedx", "--output", p.sbom()}, trivyCommon(p)...)
	if out, code, err := p.tool(ctx, run, "trivy", args, nil); err != nil || code != 0 {
		return toolErr("trivy sbom", out, code, err)
	}
	crit, high, db, err := TrivySBOM(ctx, p, run, p.sbom())
	res.Critical, res.High, res.TrivyDB = crit, high, db
	return err
}

// TrivySBOM scans a CycloneDX SBOM for vulnerabilities offline and returns
// the critical and high ids and the DB's update time.
func TrivySBOM(ctx context.Context, p ScanPlan, run ToolExec, sbom string) (critical, high []string, db string, err error) {
	p = p.withDefaults()
	report := filepath.Join(p.Scratch, "out", "trivy.json")
	args := append([]string{"sbom", "--scanners", "vuln", "--format", "json", "--output", report}, trivyCommon(p)...)
	args = append(args, sbom)
	if out, code, err := p.tool(ctx, run, "trivy", args, nil); err != nil || code != 0 {
		return nil, nil, "", toolErr("trivy vuln", out, code, err)
	}
	b, err := os.ReadFile(report) //nolint:gosec // G304: the report this scan wrote
	if err != nil {
		return nil, nil, "", err
	}
	critical, high, err = ParseTrivy(b)
	if err != nil {
		return nil, nil, "", err
	}
	out, code, verr := p.tool(ctx, run, "trivy", []string{"version", "--format", "json", "--cache-dir", filepath.Join(p.DB, "trivy")}, nil)
	if verr == nil && code == 0 {
		db = ParseTrivyDBTime(out)
	}
	return critical, high, db, nil
}

func toolErr(what string, out []byte, code int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s exited %d: %s", what, code, lastLines(string(out)))
}

// runSBOM re-scans the stored SBOM <in>/sbom.cdx.json.
func runSBOM(ctx context.Context, p ScanPlan, run ToolExec, res *StageResult) error {
	sbom := filepath.Join(p.In, "sbom.cdx.json")
	if p.has(ToolTrivy) {
		crit, high, db, err := TrivySBOM(ctx, p, run, sbom)
		if err != nil {
			return err
		}
		res.Critical, res.High, res.TrivyDB = crit, high, db
	}
	if p.has(ToolOSV) {
		ids, err := OSVMalIDs(ctx, p, run, sbom)
		if err != nil {
			return err
		}
		res.MalIDs = ids
	}
	return nil
}

// runStatic extracts every layer into its own directory and runs ClamAV,
// YARA and osv-scanner's MAL check as the plan lists them.
func runStatic(ctx context.Context, p ScanPlan, run ToolExec, res *StageResult) error {
	layers := filepath.Join(p.Scratch, "layers")
	_ = os.RemoveAll(layers)
	defer func() { _ = os.RemoveAll(layers) }()
	if err := extractLayout(p, layers); err != nil {
		return err
	}
	if p.has(ToolClamAV) {
		hits, err := clamScan(ctx, p, run, layers)
		if err != nil {
			return err
		}
		res.StaticHits = append(res.StaticHits, hits...)
	}
	if p.has(ToolYARA) {
		hits, err := yaraScan(ctx, p, run, layers)
		if err != nil {
			return err
		}
		res.StaticHits = append(res.StaticHits, hits...)
	}
	if p.has(ToolOSV) {
		ids, err := OSVMalIDs(ctx, p, run, p.sbom())
		if err != nil {
			return err
		}
		res.MalIDs = ids
	}
	return nil
}

// extractLayout extracts each layer of the layout's one manifest into
// <dir>/<n>/.
func extractLayout(p ScanPlan, dir string) error {
	b, err := os.ReadFile(filepath.Join(p.In, "index.json"))
	if err != nil {
		return err
	}
	var idx layoutIndex
	if err := json.Unmarshal(b, &idx); err != nil || len(idx.Manifests) != 1 {
		return errors.New("the quarantine index must name one manifest")
	}
	mb, err := readChecked(p.In, idx.Manifests[0])
	if err != nil {
		return err
	}
	doc, err := parseManifest(mb, idx.Manifests[0].MediaType)
	if err != nil {
		return err
	}
	budget := &Budget{Bytes: p.MaxBytes, Entries: p.MaxEntries}
	for i, l := range doc.Layers {
		f, err := os.Open(blobPath(p.In, l.Digest)) //nolint:gosec // G304: a quarantine blob named by a checked digest
		if err != nil {
			return err
		}
		err = ExtractLayer(f, filepath.Join(dir, fmt.Sprint(i)), budget)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("layer %d (%s): %w", i, ShortDigest(l.Digest), err)
		}
	}
	return nil
}

func clamScan(ctx context.Context, p ScanPlan, run ToolExec, dir string) ([]string, error) {
	args := []string{"--recursive", "--infected", "--no-summary", "--stdout",
		"--database=" + filepath.Join(p.DB, "clamav"),
		"--max-filesize=" + clamMaxFileSize, "--max-scansize=" + clamMaxScanSize,
		"--max-recursion=" + clamMaxRecursion, "--max-files=" + clamMaxFiles, dir}
	out, code, err := p.tool(ctx, run, "clamscan", args, nil)
	if err != nil || code > 1 {
		return nil, toolErr("clamscan", out, code, err)
	}
	return ParseClamAV(out, dir), nil
}

func yaraScan(ctx context.Context, p ScanPlan, run ToolExec, dir string) ([]string, error) {
	rules := filepath.Join(p.DB, "yara", "rules.yar")
	if _, err := os.Stat(rules); err != nil {
		rules = BundledYARA
	}
	out, code, err := p.tool(ctx, run, "yara", []string{"--recursive", "--no-warnings", "--no-follow-symlinks", rules, dir}, nil)
	if err != nil || code != 0 {
		return nil, toolErr("yara", out, code, err)
	}
	return ParseYARA(out, dir), nil
}

// OSVMalIDs checks an SBOM against the local OSV database and returns the
// MAL- ids it names.
func OSVMalIDs(ctx context.Context, p ScanPlan, run ToolExec, sbom string) ([]string, error) {
	p = p.withDefaults()
	env := []string{"OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY=" + filepath.Join(p.DB, "osv")}
	args := []string{"scan", "source", "--offline-vulnerabilities", "--format", "json", "-L", sbom}
	out, code, err := p.tool(ctx, run, "osv-scanner", args, env)
	// 0: nothing found, 1: vulnerabilities found, 128: no packages.
	if err != nil || (code != 0 && code != 1 && code != 128) {
		return nil, toolErr("osv-scanner", out, code, err)
	}
	if code == 128 {
		return nil, nil
	}
	return ParseOSVMal(out)
}

// tarDir writes dir as a tar file, refusing more than limit bytes.
func tarDir(dir, file string, limit int64) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // G304: the scratch archive
	if err != nil {
		return err
	}
	err = tarLimited(dir, f, limit)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// tarTo writes dir's directories and regular files as a tar stream.
func tarTo(dir string, w io.Writer) error { return tarLimited(dir, w, 0) }

func tarLimited(dir string, w io.Writer, limit int64) error {
	tw := tar.NewWriter(w)
	written := int64(0)
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if written += info.Size(); limit > 0 && written > limit {
			return ErrScratchFull
		}
		src, err := os.Open(path) //nolint:gosec // G304: a quarantine file
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, src)
		_ = src.Close()
		return err
	})
	if err := tw.Close(); walkErr == nil {
		walkErr = err
	}
	return walkErr
}
