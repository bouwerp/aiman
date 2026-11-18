package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

// The applier is a one-shot container of the edge's image running aiman's
// static binary (`aiman harness aws-apply`) on the inner network: it reads
// the plan on stdin, signs every call with the run's test key, sends it to
// the edge tap, reads seed files from the exported tree at /src, and prints
// one result line.
const (
	awsApplySuffix  = "-emu-apply"
	profileAWSApply = "aws-apply"
	// AWSApplyMarker starts the applier's result line.
	AWSApplyMarker = "AIMAN_AWS_APPLY "
	// awsApplyPlanMax bounds the plan the applier reads.
	awsApplyPlanMax = 64 << 20
	// awsApplyTimeout bounds one apply inside the container.
	awsApplyTimeout = 15 * time.Minute
)

// awsApplyLimits bound the applier container.
var awsApplyLimits = ResolvedLimits{Memory: 256 << 20, CPUs: 1, PIDs: 64}

// AWSApplyService is the compose service of an aws service's applier.
func AWSApplyService(name string) string { return name + awsApplySuffix }

// awsApplyService runs the applier: the edge image and aiman binary, the
// run's user, a read-only root, no capabilities, the inner network only,
// the tree read-only at /src. Its profile keeps `up` from starting it.
func awsApplyService(spec RunSpec) composeService {
	s := composeService{
		Image:       spec.EdgeImage,
		PullPolicy:  pullNever,
		Entrypoint:  []string{EdgeBinPath},
		Command:     []string{"harness", "aws-apply"},
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		Volumes:     []string{spec.AimanBin + ":" + EdgeBinPath + ":ro", spec.Src + ":/src:ro"},
		Networks:    []string{NetworkInner},
		Sysctls:     disableIPv6,
		Profiles:    []string{profileAWSApply},
	}
	limited(&s, awsApplyLimits)
	return s
}

// awsApplyReply is the applier's result line.
type awsApplyReply struct {
	Result *awsiac.ApplyResult `json:"result,omitempty"`
	Write  *awsiac.WriteResult `json:"write,omitempty"`
	Error  string              `json:"error,omitempty"`
}

// awsApplyWriteArg makes the applier read one operator write instead of a
// plan.
const awsApplyWriteArg = "--write"

// AWSApplyMain runs inside the applier container: the plan (or, with
// --write, one operator write) on stdin, the result line on stdout.
func AWSApplyMain(args []string, stdin io.Reader, stdout io.Writer) error {
	var reply awsApplyReply
	switch {
	case len(args) == 0:
		reply = applyPlan(stdin, "/src")
	case len(args) == 1 && args[0] == awsApplyWriteArg:
		reply = applyWrite(stdin, "/src")
	default:
		reply = awsApplyReply{Error: "usage: aiman harness aws-apply [--write]"}
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s%s\n", AWSApplyMarker, b)
	return err
}

func applyPlan(stdin io.Reader, src string) awsApplyReply {
	var p awsiac.Plan
	dec := json.NewDecoder(io.LimitReader(stdin, awsApplyPlanMax))
	if err := dec.Decode(&p); err != nil {
		return awsApplyReply{Error: "read the plan: " + err.Error()}
	}
	var root *os.Root
	if p.Seed != nil {
		r, err := os.OpenRoot(src)
		if err != nil {
			return awsApplyReply{Error: "open the tree: " + err.Error()}
		}
		defer r.Close()
		root = r
	}
	ctx, cancel := context.WithTimeout(context.Background(), awsApplyTimeout)
	defer cancel()
	a := &awsiac.Applier{Key: AWSAccessKeyID, Secret: AWSSecretAccessKey, Root: root}
	res, err := a.Apply(ctx, &p)
	reply := awsApplyReply{Result: res}
	if err != nil && res == nil {
		reply.Error = err.Error()
	}
	return reply
}

// applyWrite runs one operator write; a reset applies its plan again, with
// the seed read from src.
func applyWrite(stdin io.Reader, src string) awsApplyReply {
	var w awsiac.Write
	dec := json.NewDecoder(io.LimitReader(stdin, awsApplyPlanMax))
	if err := dec.Decode(&w); err != nil {
		return awsApplyReply{Error: "read the write: " + err.Error()}
	}
	a := &awsiac.Applier{Key: AWSAccessKeyID, Secret: AWSSecretAccessKey}
	if w.Plan != nil && w.Plan.Seed != nil {
		r, err := os.OpenRoot(src)
		if err != nil {
			return awsApplyReply{Error: "open the tree: " + err.Error()}
		}
		defer r.Close()
		a.Root = r
	}
	ctx, cancel := context.WithTimeout(context.Background(), awsApplyTimeout)
	defer cancel()
	res, err := a.Write(ctx, &w)
	reply := awsApplyReply{Write: res}
	if err != nil && res == nil {
		reply.Error = err.Error()
	}
	return reply
}

// parseApplyOutput finds the applier's result line in compose's output.
func parseApplyOutput(out string) (*awsApplyReply, error) {
	var line string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), AWSApplyMarker); ok {
			line = rest
		}
	}
	if line == "" {
		return nil, errors.New("the applier printed no result")
	}
	var r awsApplyReply
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		return nil, fmt.Errorf("read the applier's result: %w", err)
	}
	return &r, nil
}

// aws applies the scenario's aws plan, writes the manifest, and writes the
// compose project again with the app's env from the plan. It runs after
// the services, so the emulator is healthy, and before fixtures and setup,
// which see the resources.
func (s *starter) aws(ctx context.Context) error {
	s.lr.mu.Lock()
	p := s.lr.awsPlan
	s.lr.mu.Unlock()
	if p == nil {
		return nil
	}
	res := &awsiac.ApplyResult{}
	if len(p.Steps) > 0 {
		var err error
		if res, err = s.applyPlan(ctx, p); err != nil {
			return err
		}
	}
	for key, attrs := range res.Learned {
		for k, v := range attrs {
			p.Learn(key, k, v)
		}
	}
	env, err := p.ResolveEnv()
	if err != nil {
		return &stepError{step: "aws env", service: p.Service, err: err}
	}
	if err := writeAWSManifest(s.sandbox, p); err != nil {
		return &stepError{step: "aws manifest", service: p.Service, err: err}
	}
	if err := rewriteAWSCompose(s.sandbox, s.lr, env); err != nil {
		return &stepError{step: "aws env", service: p.Service, err: err}
	}
	runAWS := runAWSOf(p)
	s.lr.mu.Lock()
	s.lr.run.AWS = runAWS
	run := s.lr.run
	s.lr.mu.Unlock()
	s.run.AWS = runAWS
	if err := s.m.writeRun(run); err != nil {
		s.m.logf("write run %s: %v", run.ID, err)
	}
	s.m.record(run.Session, domain.EventActorAiman, domain.EventHarnessAWSApplied, map[string]any{
		"run": run.ID, "service": p.Service, "resources": len(p.Resources), "calls": len(res.Steps), "warnings": len(p.Warnings),
	})
	return nil
}

// applyPlan runs the applier container with the plan on stdin and reads
// its result; a failed call fails the step named after it.
func (s *starter) applyPlan(ctx context.Context, p *awsiac.Plan) (*awsiac.ApplyResult, error) {
	in, err := json.Marshal(p)
	if err != nil {
		return nil, &stepError{step: "aws", err: err}
	}
	out, exit, err := s.compose(ctx, "aws apply", bytes.NewReader(in), "run", "--rm", "-T", "--no-deps", AWSApplyService(p.Service))
	reply, perr := parseApplyOutput(out)
	switch {
	case err != nil:
		return nil, &stepError{step: "aws", service: p.Service, exit: exit, out: out, err: err}
	case perr != nil:
		return nil, &stepError{step: "aws", service: p.Service, exit: exit, out: out, err: perr}
	case reply.Error != "":
		return nil, &stepError{step: "aws", service: p.Service, err: errors.New(reply.Error)}
	case reply.Result == nil:
		return nil, &stepError{step: "aws", service: p.Service, exit: exit, out: out, err: errors.New("the applier gave no result")}
	}
	if f := reply.Result.Failed; f != nil {
		return nil, &stepError{step: f.Label(), service: p.Service, err: errors.New(strings.TrimSpace(f.Code + " " + f.Message))}
	}
	return reply.Result, nil
}

// writeAWSManifest writes resources.json beside the config and
// credentials, where every container reads it at AWSMount.
func writeAWSManifest(sandbox string, p *awsiac.Plan) error {
	b, err := json.MarshalIndent(p.Manifest(), "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(sandbox, AWSResourcesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.WriteFile(path, append(b, '\n'), 0o444) //nolint:gosec // G306: the app reads it as its own user through a read-only bind
}

// rewriteAWSCompose writes the compose file again with the app's env from
// the applied plan; the app and checks start from it. Running services keep
// their containers: their definitions do not change.
func rewriteAWSCompose(sandbox string, lr *liveRun, env map[string]string) error {
	lr.mu.Lock()
	spec, f := lr.spec, lr.file
	lr.mu.Unlock()
	spec.AWSAppEnv = env
	proj, err := Compose(f, spec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sandbox, "compose.yaml"), proj.YAML, 0o600); err != nil {
		return err
	}
	lr.mu.Lock()
	lr.spec = spec
	lr.mu.Unlock()
	return nil
}
