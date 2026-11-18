package awsiac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"sync"
)

// StackSource is one stack of an aws service, as an importer reads it.
// Root is the repository tree of the run's commit; Path is the template
// (CloudFormation) or directory (Terraform) inside it. Parameters and Vars
// already merge the stack's values with the scenario's overrides (the
// scenario wins).
type StackSource struct {
	Name       string            `json:"name"`
	Format     Source            `json:"format"`
	Path       string            `json:"path"`
	Root       fs.FS             `json:"-"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Imports    map[string]string `json:"imports,omitempty"`
	VarFiles   []string          `json:"var_files,omitempty"`
	Vars       map[string]any    `json:"vars,omitempty"`
}

// Importer reads one stack into the plan: Add for each emulated resource,
// AddOutput, AddFunctionEnv, Warn for what it could not resolve and Note
// for types that are not emulated. An error (a template it cannot parse, a
// parameter with no value) refuses the run; anything a run can go on
// without is a warning.
type Importer func(ctx context.Context, src StackSource, p *Plan) error

var (
	importersMu sync.RWMutex
	importers   = map[Source]Importer{}
)

// RegisterImporter sets the importer of a format; the CloudFormation and
// Terraform importers register themselves from init.
func RegisterImporter(format Source, imp Importer) {
	importersMu.Lock()
	defer importersMu.Unlock()
	importers[format] = imp
}

// ImporterFor returns the importer of a format.
func ImporterFor(format Source) (Importer, bool) {
	importersMu.RLock()
	defer importersMu.RUnlock()
	imp := importers[format]
	return imp, imp != nil
}

// Declared is a plan fragment written out by hand: what
// DeclarativeImporter reads. Every resource's, output's and function's
// stack is the stack's name, whatever the file says.
type Declared struct {
	Resources []*Resource   `json:"resources"`
	Outputs   []Output      `json:"outputs,omitempty"`
	Functions []FunctionEnv `json:"functions,omitempty"`
}

// DeclarativeImporter reads src.Path as Declared JSON. It is the
// test-only way to fill a plan without IaC: a test registers it for a
// format with RegisterImporter.
func DeclarativeImporter(_ context.Context, src StackSource, p *Plan) error {
	b, err := fs.ReadFile(src.Root, src.Path)
	if err != nil {
		return fmt.Errorf("stack %s: %w", src.Name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d Declared
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("stack %s: %s: %w", src.Name, src.Path, err)
	}
	return d.Into(src.Name, p)
}

// Into adds the fragment to the plan under stack.
func (d Declared) Into(stack string, p *Plan) error {
	for _, r := range d.Resources {
		r.Stack = stack
		if err := p.Add(r); err != nil {
			return err
		}
	}
	for _, o := range d.Outputs {
		o.Stack = stack
		p.AddOutput(o)
	}
	for _, f := range d.Functions {
		f.Stack = stack
		p.AddFunctionEnv(f)
	}
	return nil
}

// SwapImporter sets the importer of a format and returns a function that
// puts the previous one back: a test fills plans with DeclarativeImporter
// without losing the real importer for other tests.
func SwapImporter(format Source, imp Importer) (restore func()) {
	importersMu.Lock()
	prev, had := importers[format]
	importers[format] = imp
	importersMu.Unlock()
	return func() {
		importersMu.Lock()
		defer importersMu.Unlock()
		if had {
			importers[format] = prev
		} else {
			delete(importers, format)
		}
	}
}
