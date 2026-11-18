package awsiac

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateTFGolden = flag.Bool("update-tf", false, "rewrite testdata/tf/*/plan.golden.json")

// tfGoldenCases are realistic layouts under testdata/tf, each planned and
// compared with its plan.golden.json (go test -run TestTFGolden
// -update-tf rewrites them).
var tfGoldenCases = []struct {
	name     string
	path     string
	vars     map[string]any
	varFiles []string
}{
	{name: "basic", path: "basic"},
	{name: "expand", path: "expand"},
	{name: "vars", path: "vars", varFiles: []string{"vars/env/test.tfvars"}, vars: map[string]any{"from_vars": "vars", "replicas": 2}},
	{name: "unresolved", path: "unresolved"},
	{name: "json", path: "json"},
}

func tfPlan(t *testing.T, root, dir string, vars map[string]any, varFiles []string) (*Plan, error) {
	t.Helper()
	p := New("aws", "eu-west-1", "http://aws:4566", []string{"s3", "dynamodb", "sqs", "sns"})
	src := StackSource{Name: "infra", Format: SourceTerraform, Path: dir, Root: os.DirFS(root), Vars: vars, VarFiles: varFiles}
	if err := TerraformImporter(context.Background(), src, p); err != nil {
		return p, err
	}
	return p, p.Build()
}

func TestTFGolden(t *testing.T) {
	for _, tc := range tfGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tfPlan(t, "testdata/tf", tc.path, tc.vars, tc.varFiles)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata/tf", tc.path, "plan.golden.json")
			if *updateTFGolden {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update-tf)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("plan differs from %s (run with -update-tf and review the diff):\n%s", golden, got)
			}
		})
	}
}
