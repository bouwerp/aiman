package awsiac

import (
	"fmt"
	"path"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// tfCallInsts is a module call's instances, or why they are not known.
type tfCallInsts struct {
	exp   tfExpansion
	insts []*tfInst
	// why is set for a call aiman does not read (a remote source, a
	// directory outside the repository) or cannot expand.
	why string
}

// tfLocalSource reports whether a module source is a local path.
func tfLocalSource(src string) bool {
	return strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
}

// callInsts expands a module call once: each instance's variables are
// evaluated in this instance, with count or each, and its module parsed.
// A remote source is a warning naming the call; a local module that
// cannot be read is an error.
func (in *tfInst) callInsts(name string) *tfCallInsts {
	if c, ok := in.calls[name]; ok {
		return c
	}
	c := in.mod.call(name)
	out := &tfCallInsts{}
	in.calls[name] = out
	addr := in.prefix + "module." + name
	if !tfLocalSource(c.source) {
		out.why = fmt.Sprintf("%s has the remote source %q, which aiman does not read", addr, c.source)
		in.ev.warn(Warning{Resource: addr, File: c.rng.Filename, Line: c.rng.Start.Line,
			Message: fmt.Sprintf("module source %q is not a local path; its resources are not planned and its outputs are unresolvable", c.source)})
		return out
	}
	dir := path.Join(in.mod.dir, c.source)
	if dir == ".." || strings.HasPrefix(dir, "../") {
		in.ev.fail(fmt.Errorf("terraform: %s: source %q leaves the repository (%s)", addr, c.source, tfAt(c.rng)))
		out.why = addr + " leaves the repository"
		return out
	}
	if in.depth+1 > tfMaxModuleDepth {
		in.ev.fail(fmt.Errorf("terraform: %s: modules nest deeper than %d (%s)", addr, tfMaxModuleDepth, tfAt(c.rng)))
		out.why = addr + " nests too deep"
		return out
	}
	mod, err := in.ev.loader.module(dir)
	if err != nil {
		in.ev.fail(fmt.Errorf("terraform: %s: %w", addr, err))
		out.why = addr + " cannot be read"
		return out
	}
	out.exp = in.expand("module."+name, c.count, c.forEach)
	if out.exp.why != "" {
		out.why = addr + ": " + out.exp.why
		in.ev.warn(Warning{Resource: addr, File: c.rng.Filename, Line: c.rng.Start.Line,
			Message: out.exp.why + "; the module's resources are skipped"})
		return out
	}
	for _, k := range out.exp.keys {
		given := map[string]cty.Value{}
		for argName, a := range c.args {
			if mod.variable(argName) == nil {
				in.ev.warn(Warning{Resource: addr, Attribute: argName, File: a.Range.Filename, Line: a.Range.Start.Line,
					Message: "the module declares no such variable; the argument is ignored"})
				continue
			}
			given[argName] = in.evalValue(a.Expr, k.extra())
		}
		prefix := addr + k.suffix() + "."
		vars, err := in.ev.setVars(mod, prefix, given)
		if err != nil {
			in.ev.fail(err)
			out.why = addr + " has a variable with no value"
			out.insts = nil
			return out
		}
		out.insts = append(out.insts, in.ev.newInst(mod, prefix, in.depth+1, vars))
	}
	return out
}

// moduleValue is what module.<name> reads: the instance's outputs, a
// tuple of them for count, an object by key for for_each.
func (in *tfInst) moduleValue(name string) cty.Value {
	c := in.callInsts(name)
	if c.why != "" {
		return tfUnknownVal("%s", c.why)
	}
	switch c.exp.mode {
	case tfCount:
		if len(c.insts) == 0 {
			return cty.EmptyTupleVal
		}
		vals := make([]cty.Value, len(c.insts))
		for i, ci := range c.insts {
			vals[i] = ci.outputsValue()
		}
		return cty.TupleVal(vals)
	case tfForEach:
		m := map[string]cty.Value{}
		for i, ci := range c.insts {
			m[c.exp.keys[i].key] = ci.outputsValue()
		}
		return cty.ObjectVal(m)
	}
	if len(c.insts) == 0 {
		return tfUnknownVal("module.%s has no instance", name)
	}
	return c.insts[0].outputsValue()
}

// walk lists the instance and every module instance under it, depth
// first in file order.
func (ev *tfEval) walk(in *tfInst) {
	ev.insts = append(ev.insts, in)
	for _, ig := range in.mod.ignored {
		ev.note(Warning{File: ig, Message: "override files are not merged; the file is ignored"})
	}
	for _, c := range in.mod.calls {
		if ev.ctx.Err() != nil {
			return
		}
		for _, child := range in.callInsts(c.name).insts {
			ev.walk(child)
		}
	}
}
