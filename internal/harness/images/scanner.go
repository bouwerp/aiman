package images

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// ScanStageSBOM re-scans a stored SBOM: Trivy vulnerabilities and OSV MAL ids.
const ScanStageSBOM = "sbom"

// Scanner container limits.
const (
	ScannerMemory = "2g"
	ScannerCPUs   = "2"
	ScannerPIDs   = 256
)

// ScanRequest asks the scanner container to run one stage.
type ScanRequest struct {
	Digest string
	// Input is the quarantine layout, or for ScanStageSBOM a directory
	// holding sbom.cdx.json. It is mounted read-only.
	Input string
	Stage string
	Tools []string
	// MaxBytes bounds what the stage writes to its scratch.
	MaxBytes int64
	// Timeout bounds the container.
	Timeout time.Duration
}

// ErrScannerUnavailable is a scanner image the host could not build or find.
var ErrScannerUnavailable = errors.New("scanner image unavailable")

// Scanner runs scan stages.
type Scanner interface {
	Scan(ctx context.Context, req ScanRequest) (StageResult, error)
}

// Mount is one bind mount of a scanner container.
type Mount struct {
	Host, Container string
	ReadOnly        bool
}

// ContainerOptions describe one hardened scanner container.
type ContainerOptions struct {
	Name     string
	Image    string
	UID, GID int
	// Network gives the container the default bridge; scans never have one,
	// only the database refresh job does.
	Network bool
	Mounts  []Mount
	Env     []string
	Cmd     []string
}

// RunArgs are the docker run arguments of a scanner container: no network
// unless asked, a read-only root, the host uid, every capability dropped, no
// new privileges, and the scanner's memory, CPU and PID limits.
func RunArgs(o ContainerOptions) []string {
	args := []string{"run", "--rm", "--name", o.Name,
		"--read-only", "--user", fmt.Sprintf("%d:%d", o.UID, o.GID),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", ScannerMemory, "--memory-swap", ScannerMemory, "--cpus", ScannerCPUs,
		"--pids-limit", strconv.Itoa(ScannerPIDs),
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=64m",
	}
	if !o.Network {
		args = append(args, "--network", "none")
	}
	for _, m := range o.Mounts {
		spec := "type=bind,source=" + m.Host + ",target=" + m.Container
		if m.ReadOnly {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	for _, e := range o.Env {
		args = append(args, "--env", e)
	}
	args = append(args, "--entrypoint", o.Cmd[0], o.Image)
	return append(args, o.Cmd[1:]...)
}

// DockerCLI runs docker with args and returns its combined output and exit
// code; err is set only when docker could not run.
type DockerCLI func(ctx context.Context, args []string) (string, int, error)

// ExecDockerCLI runs the docker CLI.
func ExecDockerCLI(ctx context.Context, args []string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: aiman's own docker arguments
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0, nil
	case ctx.Err() != nil:
		return out.String(), -1, ctx.Err()
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		return out.String(), ee.ExitCode(), nil
	default:
		return out.String(), -1, err
	}
}

// DockerScanner runs scan stages in the scanner image. The aiman binary is
// mounted read-only and runs `aiman harness scan`; the quarantine and the
// databases are read-only, and the stage writes only to its scratch
// directory, whose size extraction bounds itself.
type DockerScanner struct {
	// Image is the scanner image (an ID or a local name); ImageFor, when
	// set, answers it per scan (building it on first use).
	Image    string
	ImageFor func(ctx context.Context) (string, error)
	AimanBin string
	// DBDir holds trivy/, clamav/, osv/ and yara/.
	DBDir string
	// ScratchDir holds one directory per digest while it is scanned.
	ScratchDir string
	UID, GID   int
	// Docker runs the docker CLI; nil is ExecDockerCLI.
	Docker DockerCLI
}

func (s DockerScanner) docker() DockerCLI {
	if s.Docker != nil {
		return s.Docker
	}
	return ExecDockerCLI
}

// Scan runs one stage and returns its result. A container that fails, runs
// out of time or writes no result is an error.
func (s DockerScanner) Scan(ctx context.Context, req ScanRequest) (StageResult, error) {
	short := ShortDigest(req.Digest)
	scratch := filepath.Join(s.ScratchDir, short)
	if err := os.MkdirAll(filepath.Join(scratch, "out"), 0o700); err != nil {
		return StageResult{}, err
	}
	_ = os.Remove(filepath.Join(scratch, "out", req.Stage+".json"))
	plan := ScanPlan{Stage: req.Stage, Tools: req.Tools, MaxBytes: req.MaxBytes, MaxEntries: DefaultMaxEntries, ToolTimeout: req.Timeout}
	b, err := json.Marshal(plan)
	if err != nil {
		return StageResult{}, err
	}
	if err := os.WriteFile(filepath.Join(scratch, "plan.json"), b, 0o600); err != nil {
		return StageResult{}, err
	}
	name := "aiman-scan-" + short + "-" + req.Stage
	in := Mount{Host: req.Input, Container: ContainerIn, ReadOnly: true}
	if req.Stage == ScanStageSBOM {
		in.Container = ContainerIn + "/sbom.cdx.json"
	}
	image := s.Image
	if s.ImageFor != nil {
		if image, err = s.ImageFor(ctx); err != nil {
			return StageResult{}, fmt.Errorf("%w: %w", ErrScannerUnavailable, err)
		}
	}
	args := RunArgs(ContainerOptions{
		Name: name, Image: image, UID: s.UID, GID: s.GID,
		Mounts: []Mount{
			{Host: s.AimanBin, Container: ContainerAiman, ReadOnly: true},
			in,
			{Host: s.DBDir, Container: ContainerDB, ReadOnly: true},
			{Host: scratch, Container: ContainerScratch},
		},
		Env: []string{"HOME=" + ContainerScratch, "TMPDIR=" + ContainerScratch},
		Cmd: []string{ContainerAiman, "harness", "scan", "--plan", ContainerScratch + "/plan.json"},
	})
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, code, err := s.docker()(cctx, args)
	if cctx.Err() != nil {
		rmCtx, rmCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, _, _ = s.docker()(rmCtx, []string{"rm", "-f", name})
		rmCancel()
		return StageResult{}, fmt.Errorf("%s stage ran out of time (%s)", req.Stage, timeout)
	}
	if err != nil {
		return StageResult{}, fmt.Errorf("run the scanner: %w", err)
	}
	rb, rerr := os.ReadFile(filepath.Join(scratch, "out", req.Stage+".json")) //nolint:gosec // G304: the stage result the scanner wrote
	if rerr != nil {
		return StageResult{}, fmt.Errorf("the %s stage exited %d with no result: %s", req.Stage, code, lastLines(out))
	}
	var res StageResult
	if err := json.Unmarshal(rb, &res); err != nil {
		return StageResult{}, fmt.Errorf("read the %s stage result: %w", req.Stage, err)
	}
	if res.Error != "" {
		return res, errors.New(res.Error)
	}
	if code != 0 {
		return res, fmt.Errorf("the %s stage exited %d: %s", req.Stage, code, lastLines(out))
	}
	return res, nil
}

// ScratchOf is the scratch directory of digest under the scanner's dir.
func (s DockerScanner) ScratchOf(digest string) string {
	return filepath.Join(s.ScratchDir, ShortDigest(digest))
}
