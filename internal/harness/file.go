// Package harness runs repository scenarios for human testers: the harness
// file that declares an app, its services and its scenarios, and the runs
// that start them in an offline sandbox.
package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FilePath is the repository file holding the harness.
const FilePath = ".aiman/harness.yaml"

// FileVersion is the only harness file version this code reads.
const FileVersion = 1

// MaxFileBytes caps a harness file; a larger one is refused before parsing.
const MaxFileBytes = 256 << 10

// Refusal codes. They match the RPC error codes serve answers with.
const (
	CodeNoFile          = "harness_no_file"
	CodeInvalid         = "harness_invalid"
	CodeUnknownScenario = "harness_unknown_scenario"
	CodeSubmodule       = "harness_submodule"
	CodeTooLarge        = "harness_too_large"
	// CodeDirty refuses a run of a tree with uncommitted or untracked files
	// unless the caller asks for a dirty run.
	CodeDirty = "harness_dirty"
	// CodeImageMissing refuses an image that harness.allowed_images does not
	// list or that is not on the host, when no image vetting is wired; without
	// vetting nothing is pulled.
	CodeImageMissing = "harness_image_missing"
	// CodeImagePending refuses a run while one of its images is in vetting or
	// waits for approval.
	CodeImagePending = "harness_image_pending"
	// CodeImageBlocked refuses a run with a blocked, failed or stale image.
	CodeImageBlocked = "harness_image_blocked"
	// CodeImageResolveFailed refuses a ref the registry did not resolve.
	CodeImageResolveFailed = "harness_image_resolve_failed"
	// CodeImageTooLarge refuses an image whose layers sum above
	// harness.images.max_size, before anything is fetched.
	CodeImageTooLarge = "harness_image_too_large"
	// CodeImageNotPending refuses an approval of a digest that is not
	// pending_approval, without accept_risk.
	CodeImageNotPending = "harness_image_not_pending"
	// CodeRunning refuses a second live run for one session.
	CodeRunning = "harness_running"
	// CodeBusy refuses a run over harness.max_runs live runs on the host.
	CodeBusy = "harness_busy"
	// CodeNotRunning refuses an action that needs a ready run, or names no run.
	CodeNotRunning = "harness_not_running"
	// CodeServiceFailed reports a service that did not become ready.
	CodeServiceFailed = "harness_service_failed"
)

// Sentinels for errors.Is; an *Error matches the sentinel with its code.
var (
	ErrNoFile          = &Error{Code: CodeNoFile}
	ErrInvalid         = &Error{Code: CodeInvalid}
	ErrUnknownScenario = &Error{Code: CodeUnknownScenario}
	ErrSubmodule       = &Error{Code: CodeSubmodule}
	ErrTooLarge        = &Error{Code: CodeTooLarge}
)

// Error is a harness refusal with a stable code. Errors is set for
// CodeInvalid, Path for CodeSubmodule, Sum and Limit (bytes) for CodeTooLarge,
// Paths for CodeDirty, Image for CodeImageMissing, Service and Tail for
// CodeServiceFailed. The image codes carry their data in ImageData.
type Error struct {
	Code    string
	Msg     string
	Errors  []string
	Path    string
	Sum     int64
	Limit   int64
	Paths   []string
	Image   string
	Service string
	Tail    string
	// Lang is the language of CodeDebugUnsupported; Tool the tool of
	// CodeDebugTool.
	Lang string
	Tool string
	// Reason says why a language with an adapter cannot be debugged
	// (ReasonMusl, ReasonBunVersion); empty for a language with none.
	Reason string
	// ImageData is the RPC data of the image refusals.
	ImageData map[string]any
}

func (e *Error) Error() string {
	switch {
	case e.Msg != "":
		return e.Msg
	case len(e.Errors) > 0:
		return "invalid harness file: " + strings.Join(e.Errors, "; ")
	default:
		return e.Code
	}
}

// Is matches another *Error with the same code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// Data is the refusal's RPC data: errors, path, or sum and limit.
func (e *Error) Data() map[string]any {
	switch e.Code {
	case CodeInvalid:
		return map[string]any{"errors": e.Errors}
	case CodeSubmodule:
		return map[string]any{"path": e.Path}
	case CodeTooLarge:
		return map[string]any{"sum": e.Sum, "limit": e.Limit}
	case CodeDirty:
		return map[string]any{"paths": e.Paths}
	case CodeImageMissing:
		return map[string]any{"image": e.Image}
	case CodeServiceFailed:
		return map[string]any{"service": e.Service, "tail": e.Tail}
	case CodeDebugUnsupported:
		if e.Reason != "" {
			return map[string]any{"lang": e.Lang, "reason": e.Reason}
		}
		return map[string]any{"lang": e.Lang}
	case CodeDebugTool:
		return map[string]any{"tool": e.Tool}
	case CodeImagePending, CodeImageBlocked, CodeImageResolveFailed, CodeImageTooLarge, CodeImageNotPending:
		return e.ImageData
	}
	return nil
}

func invalid(errs ...string) *Error {
	return &Error{Code: CodeInvalid, Errors: errs}
}

// File is a parsed harness file.
type File struct {
	Version   int                `yaml:"version" json:"version"`
	App       App                `yaml:"app" json:"app"`
	Services  map[string]Service `yaml:"services" json:"services,omitempty"`
	Scenarios []Scenario         `yaml:"scenarios" json:"scenarios"`
}

// App is the application under test. Build and Run are shell commands run in
// the app container; Ports are the container ports the edge relays.
type App struct {
	// Image runs the app instead of the image picked from the exported tree;
	// it is vetted like every image.
	Image  string `yaml:"image" json:"image,omitempty"`
	Build  string `yaml:"build" json:"build,omitempty"`
	Run    string `yaml:"run" json:"run"`
	Ports  []Port `yaml:"ports" json:"ports,omitempty"`
	Limits Limits `yaml:"limits" json:"limits"`
	Debug  *Debug `yaml:"debug" json:"debug,omitempty"`
}

// Port is a named app port; {{Name}} in a step becomes its laptop URL.
type Port struct {
	Name      string `yaml:"name" json:"name"`
	Container int    `yaml:"container" json:"container"`
}

// Debug selects the debugger for debug runs. Lang is go, node, bun or
// python. Program and Args are Go's: what dlv starts, empty taking app.run
// split into words. The other languages start Entry (relative to /src) under
// Runtime with RuntimeArgs in Cwd, or Run, a whole debug command that uses
// $AIMAN_INSPECT. WrapEnv (python only) names variables that serve points at
// a debugpy wrapper. A check's debug block uses Lang, Run and WrapEnv.
type Debug struct {
	Lang        string   `yaml:"lang" json:"lang"`
	Program     string   `yaml:"program" json:"program,omitempty"`
	Args        []string `yaml:"args" json:"args,omitempty"`
	Entry       string   `yaml:"entry" json:"entry,omitempty"`
	Cwd         string   `yaml:"cwd" json:"cwd,omitempty"`
	Runtime     string   `yaml:"runtime" json:"runtime,omitempty"`
	RuntimeArgs []string `yaml:"runtime_args" json:"runtime_args,omitempty"`
	Run         string   `yaml:"run" json:"run,omitempty"`
	WrapEnv     []string `yaml:"wrap_env" json:"wrap_env,omitempty"`
}

// Service is a container a scenario starts beside the app. Caps holds only
// AllowedCaps, upper case without the CAP_ prefix. Root runs it as 0:0.
type Service struct {
	Image    string            `yaml:"image" json:"image"`
	Env      map[string]string `yaml:"env" json:"env,omitempty"`
	Ready    string            `yaml:"ready" json:"ready,omitempty"`
	Database *Database         `yaml:"database" json:"database,omitempty"`
	// EVM marks an EVM node the console can read traces from (evm.go).
	EVM    *EVM     `yaml:"evm" json:"evm,omitempty"`
	Caps   []string `yaml:"caps" json:"caps,omitempty"`
	Root   bool     `yaml:"root" json:"root,omitempty"`
	Limits Limits   `yaml:"limits" json:"limits"`
}

// Database names the database a service holds. Kind is postgres or sqlite.
type Database struct {
	Kind string `yaml:"kind" json:"kind"`
	Name string `yaml:"name" json:"name,omitempty"`
}

// Limits are a container's limits as the file spells them; zero values take
// the container's default. Memory is a size such as "2g" or "512M".
type Limits struct {
	Memory string  `yaml:"memory" json:"memory,omitempty"`
	CPUs   float64 `yaml:"cpus" json:"cpus,omitempty"`
	PIDs   int     `yaml:"pids" json:"pids,omitempty"`
}

// Scenario is one named use case.
type Scenario struct {
	ID       string    `yaml:"id" json:"id"`
	Title    string    `yaml:"title" json:"title,omitempty"`
	Services []string  `yaml:"services" json:"services,omitempty"`
	Setup    []Script  `yaml:"setup" json:"setup,omitempty"`
	Fixtures []Fixture `yaml:"fixtures" json:"fixtures,omitempty"`
	Steps    []Step    `yaml:"steps" json:"steps,omitempty"`
	Checks   []Check   `yaml:"checks" json:"checks,omitempty"`
	Stubs    []Stub    `yaml:"stubs" json:"stubs,omitempty"`
}

// Script is a setup command run in the app container.
type Script struct {
	Run string `yaml:"run" json:"run"`
}

// Fixture is data loaded before the tester starts: exactly one of SQL, SQLite
// or Script, each a repository path. Into names the service it loads into and
// is required for SQL and SQLite.
type Fixture struct {
	SQL    string `yaml:"sql" json:"sql,omitempty"`
	SQLite string `yaml:"sqlite" json:"sqlite,omitempty"`
	Script string `yaml:"script" json:"script,omitempty"`
	Into   string `yaml:"into" json:"into,omitempty"`
}

// Step is an instruction for the tester and the result to look for.
type Step struct {
	Do     string `yaml:"do" json:"do"`
	Expect string `yaml:"expect" json:"expect,omitempty"`
}

// Check is a command run in the sandbox; exit 0 is a pass.
type Check struct {
	Name string `yaml:"name" json:"name"`
	Run  string `yaml:"run" json:"run"`
	// Debug runs the check under a debugger; parsed and validated here,
	// run by harness.check {debug: true}.
	Debug *Debug `yaml:"debug" json:"debug,omitempty"`
}

// Stub is a fixed response the edge proxy gives for a host and path.
type Stub struct {
	Host     string `yaml:"host" json:"host"`
	Path     string `yaml:"path" json:"path"`
	Status   int    `yaml:"status" json:"status,omitempty"`
	Body     string `yaml:"body" json:"body,omitempty"`
	BodyFile string `yaml:"body_file" json:"body_file,omitempty"`
}

// Scenario returns the scenario with id.
func (f *File) Scenario(id string) (*Scenario, bool) {
	for i := range f.Scenarios {
		if f.Scenarios[i].ID == id {
			return &f.Scenarios[i], true
		}
	}
	return nil, false
}

// Parse decodes a harness file strictly (an unknown key anywhere is an error)
// and validates it. Every problem found is in the *Error's Errors.
func Parse(b []byte) (*File, error) {
	if len(b) > MaxFileBytes {
		return nil, invalid(fmt.Sprintf("the file is larger than %d bytes", MaxFileBytes))
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalid("the file is empty")
		}
		return nil, invalid(decodeErrors(err)...)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalid("the file must hold one YAML document")
	}
	normalise(&f)
	if errs := f.Validate(); len(errs) > 0 {
		return nil, invalid(errs...)
	}
	return &f, nil
}

// unsafeKeys are compose fields a harness file must never carry. They are
// unknown to the structs anyway; naming them gives a clearer refusal.
var unsafeKeys = map[string]bool{
	"privileged": true, "network_mode": true, "pid": true, "ipc": true,
	"devices": true, "volumes": true, "cap_add": true, "security_opt": true,
	"userns_mode": true, "user": true, "uts": true, "cgroup_parent": true,
	"sysctls": true, "tmpfs": true, "ports": true, "network": true, "networks": true,
}

var unknownFieldRe = regexp.MustCompile(`^(line \d+): field (\S+) not found in type \S+$`)

func decodeErrors(err error) []string {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return []string{err.Error()}
	}
	out := make([]string, 0, len(te.Errors))
	for _, e := range te.Errors {
		m := unknownFieldRe.FindStringSubmatch(e)
		switch {
		case m == nil:
			out = append(out, e)
		case unsafeKeys[m[2]]:
			out = append(out, fmt.Sprintf("%s: %s is not allowed in a harness file", m[1], m[2]))
		default:
			out = append(out, fmt.Sprintf("%s: unknown key %s", m[1], m[2]))
		}
	}
	return out
}

// normalise trims fields and spells caps the one way validation accepts.
func normalise(f *File) {
	f.App.Build = strings.TrimSpace(f.App.Build)
	f.App.Run = strings.TrimSpace(f.App.Run)
	for name, s := range f.Services {
		s.Image = strings.TrimSpace(s.Image)
		for i, c := range s.Caps {
			s.Caps[i] = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")
		}
		f.Services[name] = s
	}
	for i := range f.Scenarios {
		f.Scenarios[i].ID = strings.TrimSpace(f.Scenarios[i].ID)
	}
}
