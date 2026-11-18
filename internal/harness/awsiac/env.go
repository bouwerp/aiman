package awsiac

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Ref is a reference the harness file writes: an output
// (<stack>.outputs.<name>), or a resource's Ref value or attribute.
type Ref struct {
	Stack     string `json:"stack"`
	LogicalID string `json:"logical_id,omitempty"`
	Attr      string `json:"attr,omitempty"`
	Output    string `json:"output,omitempty"`
}

func (r Ref) String() string {
	switch {
	case r.Output != "":
		return r.Stack + ".outputs." + r.Output
	case r.Attr != "":
		return r.Stack + "." + r.LogicalID + "." + r.Attr
	default:
		return r.Stack + "." + r.LogicalID
	}
}

// Lookup turns a reference into a Value. It fails when the output or the
// resource is not in the plan (skipped, not emulated, a typo) or the
// attribute is not one aiman models; a learned attribute (a stream ARN)
// looks up fine and resolves after the apply.
func (p *Plan) Lookup(r Ref) (Value, error) {
	if r.Output != "" {
		o, ok := p.Output(r.Stack, r.Output)
		if !ok {
			return nil, fmt.Errorf("%s: stack %s has no output %s (missing, or left out with a warning)", r, r.Stack, r.Output)
		}
		return o.Value, nil
	}
	v := AttrOf(r.Stack, r.LogicalID, r.Attr)
	if err := p.valueStatus(v); err != nil && !errors.Is(err, ErrPending) {
		return nil, fmt.Errorf("%s: %w", r, err)
	}
	return v, nil
}

// EnvRequest is what the harness file asks of the plan for the app's
// environment: env maps names to references, Functions lists function_env
// entries (<stack>.<logical id>) in file order.
type EnvRequest struct {
	Env       map[string]Ref
	Functions []Ref
	// ReservedPrefixes are name prefixes the harness sets itself (AWS_,
	// AIMAN_): a function variable with one is left out with a note.
	ReservedPrefixes []string
}

// BuildEnv resolves the app's environment into p.Env: every env entry,
// then each function_env function's variables in order. An env reference
// that does not resolve is an error (the run is refused, never started with
// an empty variable). A function variable that does not resolve is a
// warning and left out; a function variable set twice with different
// values is a warning and the later wins; env wins over a function
// variable of the same name.
func (p *Plan) BuildEnv(req EnvRequest) error {
	env := map[string]Value{}
	sensitive := map[string]bool{}
	var errs []error
	for _, name := range sortedKeys(req.Env) {
		ref := req.Env[name]
		v, err := p.Lookup(ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("aws.env %s: %w", name, err))
			continue
		}
		env[name] = v
		if ref.Output != "" {
			if o, _ := p.Output(ref.Stack, ref.Output); o.Sensitive {
				sensitive[name] = true
			}
		}
	}
	fromFunc := map[string]string{}
	for _, ref := range req.Functions {
		fn, ok := p.Function(ref.Stack, ref.LogicalID)
		if !ok {
			errs = append(errs, fmt.Errorf("aws.function_env %s: no Lambda or SAM function %s in stack %s", ref, ref.LogicalID, ref.Stack))
			continue
		}
		for _, ev := range fn.Vars {
			w := Warning{Stack: fn.Stack, Resource: fn.LogicalID, Attribute: "Environment.Variables." + ev.Name}
			if hasAnyPrefix(ev.Name, req.ReservedPrefixes) {
				w.Message = "the harness sets this variable itself; it is left out"
				p.Note(w)
				continue
			}
			if err := p.valueStatus(ev.Value); err != nil && !errors.Is(err, ErrPending) {
				w.Message = err.Error() + "; the variable is left out of the app's environment"
				p.Warn(w)
				continue
			}
			if _, set := req.Env[ev.Name]; set {
				w.Message = "aws.env sets " + ev.Name + " too; aws.env wins"
				p.Note(w)
				continue
			}
			if prev, ok := env[ev.Name]; ok && prev.String() != ev.Value.String() {
				w.Message = fmt.Sprintf("%s is also set by %s with another value; this one wins", ev.Name, fromFunc[ev.Name])
				p.Warn(w)
			}
			env[ev.Name], fromFunc[ev.Name] = ev.Value, Key(fn.Stack, fn.LogicalID)
			if ev.Sensitive {
				sensitive[ev.Name] = true
			} else {
				delete(sensitive, ev.Name)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	p.Env = env
	p.SensitiveEnv = sortedKeys(sensitive)
	return nil
}

// ResolveEnv is the app's environment as text. Called before the apply it
// fails with ErrPending for a variable that needs a learned attribute.
func (p *Plan) ResolveEnv() (map[string]string, error) {
	out := make(map[string]string, len(p.Env))
	var errs []error
	for _, k := range sortedKeys(p.Env) {
		s, err := p.Resolve(p.Env[k])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		out[k] = s
	}
	return out, errors.Join(errs...)
}

// Manifest is resources.json, what AIMAN_AWS_RESOURCES names in every
// container: every planned resource with its physical name and attributes,
// the evaluated outputs per stack, and the env the harness file maps.
// Values the emulator gives only on create appear once the plan is
// applied.
type Manifest struct {
	Service   string                       `json:"service"`
	Region    string                       `json:"region"`
	Endpoint  string                       `json:"endpoint"`
	Resources []ManifestResource           `json:"resources"`
	Outputs   map[string]map[string]string `json:"outputs"`
	Env       map[string]string            `json:"env"`
	// Sensitive names outputs (stack.name) and env variables whose
	// source is NoEcho or sensitive; the app reads them, displays must not.
	Sensitive []string `json:"sensitive,omitempty"`
}

// ManifestResource is one resource of the manifest. Attributes are keyed
// by canonical name: ref, arn, name, url, stream_arn, stream_label,
// domain_name, regional_domain_name.
type ManifestResource struct {
	Stack      string            `json:"stack"`
	LogicalID  string            `json:"logical_id"`
	Type       string            `json:"type"`
	Kind       Kind              `json:"kind"`
	Physical   string            `json:"physical,omitempty"`
	Generated  bool              `json:"generated"`
	Attributes map[string]string `json:"attributes"`
}

// Manifest builds the manifest from what is known now: before the apply,
// learned attributes and the outputs and env that need them are absent.
func (p *Plan) Manifest() Manifest {
	m := Manifest{
		Service: p.Service, Region: p.Region, Endpoint: p.Endpoint,
		Resources: []ManifestResource{}, Outputs: map[string]map[string]string{}, Env: map[string]string{},
	}
	for _, r := range p.Resources {
		attrs := map[string]string{}
		for k, v := range r.Attrs {
			if v != "" {
				attrs[k] = v
			}
		}
		m.Resources = append(m.Resources, ManifestResource{
			Stack: r.Stack, LogicalID: r.LogicalID, Type: r.Type, Kind: r.Kind,
			Physical: r.Physical, Generated: r.Generated, Attributes: attrs,
		})
	}
	for _, o := range p.Outputs {
		s, err := p.Resolve(o.Value)
		if err != nil {
			continue
		}
		if m.Outputs[o.Stack] == nil {
			m.Outputs[o.Stack] = map[string]string{}
		}
		m.Outputs[o.Stack][o.Name] = s
		if o.Sensitive {
			m.Sensitive = append(m.Sensitive, o.Stack+".outputs."+o.Name)
		}
	}
	for k, v := range p.Env {
		if s, err := p.Resolve(v); err == nil {
			m.Env[k] = s
		}
	}
	m.Sensitive = append(m.Sensitive, p.SensitiveEnv...)
	slices.Sort(m.Sensitive)
	return m
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
