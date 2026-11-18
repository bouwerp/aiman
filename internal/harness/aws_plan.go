package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/awsiac"
)

// RunAWS is run.json's aws object: the scenario's aws service, its planned
// resources (with the attributes the apply learned once it ran), and the
// plan's warnings and notes.
type RunAWS struct {
	Service   string                    `json:"service"`
	Resources []awsiac.ManifestResource `json:"resources"`
	Warnings  []awsiac.Warning          `json:"warnings,omitempty"`
	Notes     []awsiac.Warning          `json:"notes,omitempty"`
}

// awsReservedEnvPrefixes are names a plan's env may not set: aiman's own.
var awsReservedEnvPrefixes = []string{"AWS_", "AIMAN_"}

// AWSPlan builds the plan of aws service name for scenario sc (nil: no
// scenario overrides) from the repository tree at root: each stack through
// its format's importer, the seed file, then the app's env. A bad reference,
// a seed entry naming a skipped resource or an importer error is
// harness_invalid naming it; anything a run can go on without is a warning
// on the plan.
func AWSPlan(ctx context.Context, f *File, name string, sc *Scenario, root *os.Root) (*awsiac.Plan, error) {
	svc, ok := f.Services[name]
	if !ok || svc.AWS == nil {
		return nil, invalid(fmt.Sprintf("%s is not an aws service", name))
	}
	a := svc.AWS
	var over ScenarioAWS
	if sc != nil {
		over = sc.AWS[name]
	}
	at := "services." + name
	p := awsiac.New(name, a.RegionOrDefault(), awsEndpoint(name), a.Services)
	var errs []string
	for i, st := range a.Stacks {
		src := awsiac.StackSource{Name: st.Name, Root: root.FS(), Imports: st.Imports, VarFiles: st.VarFiles}
		if st.IsTerraform() {
			src.Format, src.Path = awsiac.SourceTerraform, path.Clean(st.Terraform)
			src.Vars = mergeAny(st.Vars, over.Vars[st.Name])
		} else {
			src.Format, src.Path = awsiac.SourceCloudFormation, path.Clean(st.CloudFormation)
			src.Parameters = mergeStrings(st.Parameters, over.Parameters[st.Name])
		}
		imp, ok := awsiac.ImporterFor(src.Format)
		if !ok {
			p.Warn(awsiac.Warning{Stack: st.Name, File: src.Path, Message: fmt.Sprintf("this aiman cannot import %s yet; the stack adds no resources", src.Format)})
			continue
		}
		if err := imp(ctx, src, p); err != nil {
			errs = append(errs, fmt.Sprintf("%s: aws.stacks[%d] %s: %v", at, i, st.Name, err))
		}
	}
	seed := a.Seed
	if over.Seed != "" {
		seed = over.Seed
	}
	if seed != "" {
		if err := setSeed(p, a, root, seed); err != nil {
			errs = append(errs, fmt.Sprintf("%s: aws.seed %s: %v", at, seed, err))
		}
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	if err := p.Build(); err != nil {
		return nil, invalid(fmt.Sprintf("%s: %v", at, err))
	}
	req := awsiac.EnvRequest{Env: map[string]awsiac.Ref{}, ReservedPrefixes: awsReservedEnvPrefixes}
	for k, v := range a.Env {
		r, err := a.ParseRef(v)
		if err != nil {
			return nil, invalid(fmt.Sprintf("%s: aws.env %s: %v", at, k, err))
		}
		req.Env[k] = toIACRef(r)
	}
	for _, fe := range a.FunctionEnv {
		r, err := a.ParseRef(fe)
		if err != nil {
			return nil, invalid(fmt.Sprintf("%s: aws.function_env %s: %v", at, fe, err))
		}
		req.Functions = append(req.Functions, toIACRef(r))
	}
	if err := p.BuildEnv(req); err != nil {
		return nil, invalid(splitJoined(at, err)...)
	}
	return p, nil
}

func toIACRef(r AWSRef) awsiac.Ref {
	return awsiac.Ref{Stack: r.Stack, LogicalID: r.LogicalID, Attr: r.Attr, Output: r.Output}
}

// splitJoined turns an errors.Join into one message per error.
func splitJoined(at string, err error) []string {
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if line != "" {
			out = append(out, at+": "+line)
		}
	}
	return out
}

// setSeed reads the seed file from the tree and binds it to the plan. A
// target whose first part names a stack of the service is a reference;
// anything else is a literal physical name.
func setSeed(p *awsiac.Plan, a *AWS, root *os.Root, name string) error {
	b, err := readTreeFile(root, name, awsiac.MaxSeedFileBytes)
	if err != nil {
		return err
	}
	sf, err := awsiac.ParseSeed(b)
	if err != nil {
		return err
	}
	if err := checkSeedPaths(root, sf); err != nil {
		return err
	}
	return p.SetSeed(name, sf, func(t string) (awsiac.Ref, bool, error) {
		stack, _, ok := strings.Cut(t, ".")
		if _, isStack := a.Stack(stack); !ok || !isStack {
			return awsiac.Ref{}, false, nil
		}
		r, err := a.ParseRef(t)
		if err != nil {
			return awsiac.Ref{}, false, err
		}
		return toIACRef(r), true, nil
	})
}

// checkSeedPaths refuses seed paths that are missing, of the wrong type or
// symlinks, before a run starts.
func checkSeedPaths(root *os.Root, sf *awsiac.SeedFile) error {
	var errs []error
	check := func(name string, dir bool) {
		fi, err := root.Lstat(path.Clean(name))
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		case fi.Mode()&os.ModeSymlink != 0:
			errs = append(errs, fmt.Errorf("%s is a symlink; seed files are read without following links", name))
		case dir && !fi.IsDir():
			errs = append(errs, fmt.Errorf("%s is not a directory", name))
		case !dir && !fi.Mode().IsRegular():
			errs = append(errs, fmt.Errorf("%s is not a regular file", name))
		}
	}
	for _, o := range sf.Objects {
		check(o.From, true)
	}
	for _, i := range sf.Items {
		check(i.File, false)
	}
	for _, m := range sf.Messages {
		check(m.File, false)
	}
	return errors.Join(errs...)
}

// readTreeFile reads a regular file of the tree, at most limit bytes.
func readTreeFile(root *os.Root, name string, limit int64) ([]byte, error) {
	name = path.Clean(name)
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is over %d bytes", name, limit)
	}
	fh, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return io.ReadAll(io.LimitReader(fh, limit+1))
}

func mergeStrings(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func mergeAny(base, over map[string]any) map[string]any {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// scenarioAWSPlan builds the plan of the scenario's aws service from the
// exported tree src; nil when the scenario has none.
func scenarioAWSPlan(ctx context.Context, f *File, sc *Scenario, src string) (*awsiac.Plan, error) {
	names := f.AWSServices(sc)
	if len(names) == 0 {
		return nil, nil
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return AWSPlan(ctx, f, names[0], sc, root)
}

// runAWSOf is run.json's view of a plan.
func runAWSOf(p *awsiac.Plan) *RunAWS {
	m := p.Manifest()
	return &RunAWS{Service: p.Service, Resources: m.Resources, Warnings: p.Warnings, Notes: p.Notes}
}

// AWSPlanView is what harness.aws_plan answers per aws service: the plan's
// resources with physical names and attributes, outputs and the app's env
// (sensitive values redacted; values the emulator gives on create shown as
// pending), the steps, the seed, warnings and notes.
type AWSPlanView struct {
	Service   string                       `json:"service"`
	Scenario  string                       `json:"scenario,omitempty"`
	Region    string                       `json:"region"`
	Endpoint  string                       `json:"endpoint"`
	Resources []awsiac.ManifestResource    `json:"resources"`
	Outputs   map[string]map[string]string `json:"outputs"`
	Env       map[string]string            `json:"env"`
	Steps     []awsiac.Step                `json:"steps"`
	Seed      *awsiac.Seed                 `json:"seed,omitempty"`
	Warnings  []awsiac.Warning             `json:"warnings"`
	Notes     []awsiac.Warning             `json:"notes"`
}

// Redacted stands for a sensitive value in a view.
const Redacted = "[redacted]"

// planView shows a plan built but not applied.
func planView(p *awsiac.Plan, scenario string) AWSPlanView {
	v := AWSPlanView{
		Service: p.Service, Scenario: scenario, Region: p.Region, Endpoint: p.Endpoint,
		Outputs: map[string]map[string]string{}, Env: map[string]string{},
		Steps: p.Steps, Seed: p.Seed, Warnings: p.Warnings, Notes: p.Notes,
	}
	if v.Steps == nil {
		v.Steps = []awsiac.Step{}
	}
	if v.Warnings == nil {
		v.Warnings = []awsiac.Warning{}
	}
	if v.Notes == nil {
		v.Notes = []awsiac.Warning{}
	}
	v.Resources = p.Manifest().Resources
	show := func(val awsiac.Value, sensitive bool) string {
		if sensitive {
			return Redacted
		}
		s, err := p.Resolve(val)
		if errors.Is(err, awsiac.ErrPending) {
			return "(set when applied: " + val.String() + ")"
		}
		return s
	}
	for _, o := range p.Outputs {
		if v.Outputs[o.Stack] == nil {
			v.Outputs[o.Stack] = map[string]string{}
		}
		v.Outputs[o.Stack][o.Name] = show(o.Value, o.Sensitive)
	}
	for k, val := range p.Env {
		v.Env[k] = show(val, containsString(p.SensitiveEnv, k))
	}
	return v
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// AWSPlanRequest asks for the plans of a repository's harness at a commit:
// one scenario's aws service with its overrides, or with Scenario empty
// every aws service of the file without overrides.
type AWSPlanRequest struct {
	Repo     string
	SHA      string
	Override []byte
	Scenario string
	// Dirty builds the plans from the working tree instead of the commit.
	Dirty bool
}

// AWSPlans builds plans from the commit, or the working tree with Dirty,
// without a run (harness.aws_plan): the tree is exported into a scratch
// directory, read and removed.
func (m *Manager) AWSPlans(ctx context.Context, req AWSPlanRequest) (sha string, out []AWSPlanView, err error) {
	l, err := loadForPlan(ctx, req)
	if err != nil {
		return "", nil, err
	}
	f := l.File
	type job struct {
		name string
		sc   *Scenario
	}
	var jobs []job
	if req.Scenario != "" {
		sc, ok := f.Scenario(req.Scenario)
		if !ok {
			return "", nil, &Error{Code: CodeUnknownScenario, Msg: fmt.Sprintf("no scenario %q in the harness file", req.Scenario)}
		}
		names := f.AWSServices(sc)
		if len(names) == 0 {
			return "", nil, paramsError(fmt.Sprintf("scenario %s starts no aws service", sc.ID))
		}
		jobs = append(jobs, job{names[0], sc})
	} else {
		for _, name := range sortedKeys(f.Services) {
			if f.Services[name].AWS != nil {
				jobs = append(jobs, job{name, nil})
			}
		}
		if len(jobs) == 0 {
			return "", nil, paramsError("the harness file has no aws service")
		}
	}
	tmp, err := os.MkdirTemp(m.opts.Dir, "aws-plan-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp)
	export := func() error { return exportCommit(ctx, req.Repo, l.SHA, tmp) }
	if req.Dirty {
		export = func() error { return exportWorkingTree(ctx, req.Repo, tmp) }
	}
	if err := export(); err != nil {
		return "", nil, fmt.Errorf("export the tree: %w", err)
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	for _, j := range jobs {
		p, err := AWSPlan(ctx, f, j.name, j.sc, root)
		if err != nil {
			return "", nil, err
		}
		id := ""
		if j.sc != nil {
			id = j.sc.ID
		}
		out = append(out, planView(p, id))
	}
	return l.SHA, out, nil
}

// loadForPlan reads the harness file AWSPlans works from.
func loadForPlan(ctx context.Context, req AWSPlanRequest) (*Loaded, error) {
	if req.Dirty {
		return LoadDirty(ctx, req.Repo, req.Override)
	}
	commit := req.SHA
	if commit == "" {
		commit = "HEAD"
	}
	return LoadAtSHA(ctx, req.Repo, commit, req.Override)
}
