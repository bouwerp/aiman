package harness

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/cigate"
)

// DebugLangs are the app.debug.lang values a file may name. A language whose
// adapter has not landed parses and is refused at harness.up with
// CodeDebugUnsupported.
var DebugLangs = []string{"go", "node", "bun", "python"}

// loaderRuntimes start a second runtime of their own, so the inspector
// would sit in the wrong process; they belong in runtime_args.
var loaderRuntimes = []string{"tsx", "ts-node", "ts-node-esm", "nodemon", "vite-node"}

var (
	runtimeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	runtimePathRe = regexp.MustCompile(`^[A-Za-z0-9._+-][A-Za-z0-9._/+-]{0,255}$`)
	inspectRefRe  = regexp.MustCompile(`\$(` + InspectEnv + `\b|\{` + InspectEnv + `\})`)
)

// validate checks one debug block at at. check is set for checks[].debug,
// which takes only lang, run and wrap_env.
func (d *Debug) validate(at string, check bool) []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, at+fmt.Sprintf(format, args...)) }
	if !slices.Contains(DebugLangs, d.Lang) {
		add(".lang %q must be one of %s", d.Lang, strings.Join(DebugLangs, ", "))
		return errs
	}
	if d.Program != "" || len(d.Args) > 0 {
		errs = append(errs, d.validateProgram(at, check)...)
	}
	if d.Lang == "go" && !check {
		for _, f := range []struct{ name, v string }{{"entry", d.Entry}, {"cwd", d.Cwd}, {"runtime", d.Runtime}, {"run", d.Run}} {
			if f.v != "" {
				add(".%s is for node, bun and python; go takes program and args", f.name)
			}
		}
		if len(d.RuntimeArgs) > 0 {
			add(".runtime_args is for node, bun and python; go takes program and args")
		}
	} else {
		errs = append(errs, d.validateCommand(at, check)...)
	}
	if len(d.WrapEnv) > 0 && d.Lang != "python" {
		add(".wrap_env is for python only")
	}
	for _, n := range d.WrapEnv {
		if !envNameRe.MatchString(n) {
			add(".wrap_env %q is not a variable name", n)
		}
	}
	return errs
}

func (d *Debug) validateProgram(at string, check bool) []string {
	switch {
	case d.Lang != "go" || check:
		return []string{at + ".program and args are for go apps; use entry or run"}
	case d.Program == "":
		return []string{at + ".args needs " + at + ".program"}
	case !path.IsAbs(d.Program):
		return []string{fmt.Sprintf("%s.program %q must be an absolute path in the app container (for example /out/app)", at, d.Program)}
	}
	return nil
}

// validateCommand checks entry, cwd, runtime, runtime_args and run of a
// node, bun or python block, or a check's run.
func (d *Debug) validateCommand(at string, check bool) []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, at+fmt.Sprintf(format, args...)) }
	if check {
		for _, f := range []struct{ name, v string }{{"entry", d.Entry}, {"cwd", d.Cwd}, {"runtime", d.Runtime}} {
			if f.v != "" {
				add(".%s is not used by a check; give the whole command in run", f.name)
			}
		}
		if len(d.RuntimeArgs) > 0 {
			add(".runtime_args is not used by a check; give the whole command in run")
		}
	}
	wrapOnly := d.Lang == "python" && len(d.WrapEnv) > 0
	switch {
	case d.Entry != "" && d.Run != "":
		add(": set entry or run, not both")
	case d.Entry == "" && d.Run == "" && !wrapOnly:
		if check {
			add(": %s needs run", d.Lang)
		} else {
			add(": %s needs entry or run", d.Lang)
		}
	}
	for _, f := range []struct{ name, v string }{{"entry", d.Entry}, {"cwd", d.Cwd}} {
		if f.v == "" {
			continue
		}
		if err := cigate.ValidateDir(f.v); err != nil || strings.ContainsAny(f.v, "\n\x00") {
			add(".%s %q must be a path relative to the repository root", f.name, f.v)
		}
	}
	if d.Runtime != "" {
		errs = append(errs, validateRuntime(at, d.Runtime)...)
	}
	for _, a := range d.RuntimeArgs {
		if msg := refusedDebugFlag(a); msg != "" {
			add(".runtime_args: %s", msg)
		}
	}
	if d.Run != "" {
		errs = append(errs, validateDebugRun(at, d.Run)...)
	}
	return errs
}

func validateRuntime(at, rt string) []string {
	ok := runtimeNameRe.MatchString(rt)
	if !ok && runtimePathRe.MatchString(rt) && cigate.ValidateDir(rt) == nil {
		ok = true
	}
	if !ok {
		return []string{fmt.Sprintf("%s.runtime %q must be a command name or a path relative to the repository root", at, rt)}
	}
	if slices.Contains(loaderRuntimes, path.Base(rt)) {
		return []string{fmt.Sprintf("%s.runtime %q starts a second runtime, which the debugger would not reach; keep the default runtime and load it through runtime_args (for example [\"--import\", \"tsx\"])", at, rt)}
	}
	return nil
}

func validateDebugRun(at, run string) []string {
	var errs []string
	if n := len(inspectRefRe.FindAllStringIndex(run, -1)); n != 1 {
		if n == 0 {
			errs = append(errs, fmt.Sprintf("%s.run must use $%s, the debug address serve gives the run", at, InspectEnv))
		} else {
			errs = append(errs, fmt.Sprintf("%s.run must use $%s once, not %d times", at, InspectEnv, n))
		}
	}
	for _, w := range strings.Fields(run) {
		if msg := refusedDebugFlag(strings.Trim(w, `'"`)); msg != "" {
			errs = append(errs, at+".run: "+msg)
		}
	}
	return errs
}

// refusedDebugFlag names a flag a debug command must not carry: a watch or
// hot reload ends the debug session on every restart, and an inspector on
// 0.0.0.0 would listen on every network of the container.
func refusedDebugFlag(arg string) string {
	switch {
	case arg == "--watch" || strings.HasPrefix(arg, "--watch=") || strings.HasPrefix(arg, "--watch-"):
		return arg + " restarts the process and ends the debug session; leave --watch out"
	case arg == "--hot" || strings.HasPrefix(arg, "--hot="):
		return arg + " reloads the process and ends the debug session; leave --hot out"
	case strings.HasPrefix(arg, "--inspect") && strings.Contains(arg, "0.0.0.0"):
		return arg + " listens on every address; use $" + InspectEnv
	}
	return ""
}
