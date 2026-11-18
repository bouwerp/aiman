package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/harness"
)

func init() {
	harnessAWSCommands["plan"] = harnessAWSPlan
}

// harnessAWSPlan is `aiman harness aws plan [<scenario>] [--json]`: the
// plan serve builds from the session's commit, with no run.
func harnessAWSPlan(h harnessIO, a harnessArgs) error {
	if len(a.pos) > 1 || a.run != "" {
		return harnessUsage(h.errOut)
	}
	params := map[string]any{"session": a.session}
	if len(a.pos) == 1 {
		params["scenario"] = a.pos[0]
	}
	raw, err := harnessCallOut(h, a, "harness.aws_plan", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		SHA   string                `json:"sha"`
		Plans []harness.AWSPlanView `json:"plans"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	for i, p := range res.Plans {
		if i > 0 {
			_, _ = fmt.Fprintln(h.out)
		}
		writeAWSPlan(h.out, res.SHA, p)
	}
	return nil
}

func writeAWSPlan(w io.Writer, sha string, p harness.AWSPlanView) {
	head := fmt.Sprintf("aws service %s (%s) at %s", p.Service, p.Region, shortSHA(sha))
	if p.Scenario != "" {
		head += ", scenario " + p.Scenario
	}
	_, _ = fmt.Fprintln(w, head)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STACK\tLOGICAL ID\tTYPE\tPHYSICAL NAME")
	for _, r := range p.Resources {
		name := r.Physical
		if r.Generated {
			name += " (generated)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Stack, r.LogicalID, r.Type, name)
	}
	_ = tw.Flush()
	stacks := make([]string, 0, len(p.Outputs))
	for s := range p.Outputs {
		stacks = append(stacks, s)
	}
	sort.Strings(stacks)
	for _, s := range stacks {
		for _, k := range sortedStrings(p.Outputs[s]) {
			_, _ = fmt.Fprintf(w, "output %s.outputs.%s = %s\n", s, k, p.Outputs[s][k])
		}
	}
	for _, k := range sortedStrings(p.Env) {
		_, _ = fmt.Fprintf(w, "env %s=%s\n", k, p.Env[k])
	}
	_, _ = fmt.Fprintf(w, "%d calls", len(p.Steps))
	if p.Seed != nil {
		_, _ = fmt.Fprintf(w, ", seed %s", p.Seed.File)
	}
	_, _ = fmt.Fprintln(w)
	for _, x := range p.Warnings {
		_, _ = fmt.Fprintln(w, "warning: "+x.String())
	}
	for _, x := range p.Notes {
		_, _ = fmt.Fprintln(w, "note: "+x.String())
	}
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
