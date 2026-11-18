package awsiac

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// tfFS plans an in-memory stack in directory "stack".
func tfFS(t *testing.T, files map[string]string, vars map[string]any, varFiles ...string) (*Plan, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	p := New("aws", "eu-west-1", "http://aws:4566", []string{"s3", "dynamodb", "sqs", "sns"})
	src := StackSource{Name: "st", Format: SourceTerraform, Path: "stack", Root: fsys, Vars: vars, VarFiles: varFiles}
	if err := TerraformImporter(context.Background(), src, p); err != nil {
		return p, err
	}
	return p, p.Build()
}

func mustTF(t *testing.T, files map[string]string, vars map[string]any, varFiles ...string) *Plan {
	t.Helper()
	p, err := tfFS(t, files, vars, varFiles...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tfOutputText(t *testing.T, p *Plan, name string) string {
	t.Helper()
	o, ok := p.Output("st", name)
	if !ok {
		t.Fatalf("no output %s; warnings %v", name, p.Warnings)
	}
	s, err := p.Resolve(o.Value)
	if err != nil {
		t.Fatalf("output %s: %v", name, err)
	}
	return s
}

func hasWarning(ws []Warning, parts ...string) bool {
	for _, w := range ws {
		s := w.String()
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(s, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestTFRegistered(t *testing.T) {
	if _, ok := ImporterFor(SourceTerraform); !ok {
		t.Fatal("the terraform importer is not registered")
	}
}

func TestTFFunctionTable(t *testing.T) {
	cases := map[string]string{
		`format("%s-%03d", "a", 7)`:                             "a-007",
		`join(",", formatlist("x%s", ["a", "b"]))`:              "xa,xb",
		`lower("AbC")`:                                          "abc",
		`upper("abc")`:                                          "ABC",
		`title("hello world")`:                                  "Hello World",
		`trimspace("  x ")`:                                     "x",
		`trim("--x--", "-")`:                                    "x",
		`trimprefix("pre-x", "pre-")`:                           "x",
		`trimsuffix("x.fifo", ".fifo")`:                         "x",
		`replace("a-b-c", "-", "_")`:                            "a_b_c",
		`replace("a1b22", "/[0-9]+/", "#")`:                     "a#b#",
		`substr("abcdef", 1, 3)`:                                "bcd",
		`join("+", split(",", "a,b"))`:                          "a+b",
		`join("", concat(["a"], ["b"]))`:                        "ab",
		`merge({a = "1"}, {b = "2"})["b"]`:                      "2",
		`lookup({a = "1"}, "z", "dflt")`:                        "dflt",
		`element(["a", "b"], 3)`:                                "b",
		`length("abcd") + length(["a"]) + length({a=1})`:        "6",
		`join(",", keys({b = 1, a = 2}))`:                       "a,b",
		`join(",", values({b = "1", a = "2"}))`:                 "2,1",
		`contains(["a"], "a")`:                                  "true",
		`join(",", distinct(["a", "a", "b"]))`:                  "a,b",
		`join(",", flatten([["a"], ["b", ["c"]]]))`:             "a,b,c",
		`coalesce("", "x")`:                                     "x",
		`try(local.missing_map["k"], "fallback")`:               "fallback",
		`can(tonumber("x"))`:                                    "false",
		`tostring(5)`:                                           "5",
		`tonumber("5") + 1`:                                     "6",
		`tobool("true")`:                                        "true",
		`length(tolist(toset(["a", "a"])))`:                     "1",
		`tomap({a = "1"})["a"]`:                                 "1",
		`jsonencode({b = [1, true], a = null})`:                 `{"a":null,"b":[1,true]}`,
		`jsondecode("{\"k\": [1, 2]}").k[1]`:                    "2",
		`min(3, 1, 2) + max(4, 9)`:                              "10",
		`"%{ for x in ["a", "b"] }${x};%{ endfor }"`:            "a;b;",
		`[for k, v in {a = 1, b = 2} : "${k}${v}" if v > 1][0]`: "b2",
		`true ? "y" : "n"`:                                      "y",
		`path.module`:                                           "stack",
		`terraform.workspace`:                                   "default",
	}
	var b strings.Builder
	b.WriteString("locals {\n  missing_map = {}\n}\n")
	i := 0
	names := map[string]string{}
	for expr := range cases {
		name := "o" + strings.Repeat("x", i)
		i++
		names[name] = expr
		b.WriteString("output \"" + name + "\" {\n  value = " + expr + "\n}\n")
	}
	p := mustTF(t, map[string]string{"stack/main.tf": b.String()}, nil)
	for name, expr := range names {
		if _, ok := p.Output("st", name); !ok {
			t.Errorf("%s: no output", expr)
			continue
		}
		if got := tfOutputText(t, p, name); got != cases[expr] {
			t.Errorf("%s = %q, want %q", expr, got, cases[expr])
		}
	}
	if len(p.Warnings) > 0 {
		t.Errorf("warnings %v", p.Warnings)
	}
}

func TestTFFunctionsOutsideTheTable(t *testing.T) {
	p := mustTF(t, map[string]string{"stack/main.tf": `
output "a" { value = file("x.txt") }
output "b" { value = templatefile("t.tpl", {}) }
output "c" { value = timestamp() }
output "d" { value = uuid() }
output "e" { value = provider::aws::arn_parse("x") }
`}, nil)
	if len(p.Outputs) != 0 {
		t.Fatalf("outputs %v", p.Outputs)
	}
	for _, fn := range []string{"file", "templatefile", "timestamp", "uuid"} {
		if !hasWarning(p.Warnings, "no function named \""+fn+"\"", "the output is left out") {
			t.Errorf("no warning for %s in %v", fn, p.Warnings)
		}
	}
	if !hasWarning(p.Warnings, "outputs.e", "the output is left out") {
		t.Errorf("no warning for a provider function in %v", p.Warnings)
	}
}

func TestTFLocals(t *testing.T) {
	p := mustTF(t, map[string]string{"stack/main.tf": `
variable "env" { default = "dev" }
locals {
  a = "${local.b}-a"
  b = upper(var.env)
  unused = file("nope")
}
output "a" { value = local.a }
`}, nil)
	if got := tfOutputText(t, p, "a"); got != "DEV-a" {
		t.Fatalf("a = %q", got)
	}
	if len(p.Warnings) != 0 {
		t.Fatalf("an unused local warned: %v", p.Warnings)
	}
	_, err := tfFS(t, map[string]string{"stack/main.tf": `
locals {
  a = local.b
  b = local.a
}
output "a" { value = local.a }
`}, nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
}

func TestTFVariables(t *testing.T) {
	files := map[string]string{
		"stack/main.tf": `
variable "n" { type = number }
variable "list" {
  type    = list(string)
  default = []
}
variable "obj" {
  type    = object({ a = string, b = optional(string, "bd") })
  default = { a = "x" }
}
output "n" { value = var.n + 1 }
output "list" { value = join(",", var.list) }
output "obj" { value = "${var.obj.a}${var.obj.b}" }
`,
		"stack/terraform.tfvars": "n = 1\n",
		"other/ci.tfvars":        "n = 10\nlist = [\"a\", \"b\"]\n",
	}
	p := mustTF(t, files, nil)
	if got := tfOutputText(t, p, "n"); got != "2" {
		t.Errorf("n from terraform.tfvars = %q", got)
	}
	p = mustTF(t, files, map[string]any{"n": "41", "list": []any{"x"}}, "other/ci.tfvars")
	if got := tfOutputText(t, p, "n"); got != "42" {
		t.Errorf("n from vars = %q", got)
	}
	if got := tfOutputText(t, p, "list"); got != "x" {
		t.Errorf("list from vars = %q", got)
	}
	if got := tfOutputText(t, p, "obj"); got != "xbd" {
		t.Errorf("obj default = %q", got)
	}
	p = mustTF(t, files, map[string]any{"ghost": "x"}, "other/ci.tfvars")
	if got := tfOutputText(t, p, "list"); got != "a,b" {
		t.Errorf("list from var file = %q", got)
	}
	if !hasWarning(p.Warnings, "var.ghost", "does not declare") {
		t.Errorf("undeclared var: %v", p.Warnings)
	}
	_, err := tfFS(t, map[string]string{"stack/main.tf": files["stack/main.tf"]}, nil)
	if err == nil || !strings.Contains(err.Error(), "variable n has no value") {
		t.Errorf("missing value: %v", err)
	}
	_, err = tfFS(t, files, map[string]any{"n": "x"})
	if err == nil || !strings.Contains(err.Error(), "variable n") {
		t.Errorf("bad type: %v", err)
	}
}

func TestTFRefusals(t *testing.T) {
	cases := map[string]struct {
		files    map[string]string
		varFiles []string
		want     string
	}{
		"parse error":      {files: map[string]string{"stack/main.tf": "resource \"x\" {"}, want: "main.tf"},
		"no files":         {files: map[string]string{"stack/README.md": "x"}, want: "no .tf"},
		"missing dir":      {files: map[string]string{"other/main.tf": ""}, want: "stack"},
		"missing var file": {files: map[string]string{"stack/main.tf": ""}, varFiles: []string{"nope.tfvars"}, want: "nope.tfvars"},
		"var file expr":    {files: map[string]string{"stack/main.tf": "variable \"a\" {}", "v.tfvars": "a = var.b"}, varFiles: []string{"v.tfvars"}, want: "v.tfvars"},
		"leaves the repo":  {files: map[string]string{"stack/main.tf": `module "m" { source = "../../elsewhere" }`}, want: "leaves the repository"},
		"missing module":   {files: map[string]string{"stack/main.tf": `module "m" { source = "./nope" }`}, want: "module.m"},
		"module input":     {files: map[string]string{"stack/main.tf": `module "m" { source = "./m" }`, "stack/m/main.tf": `variable "need" {}`}, want: "module.m.variable need has no value"},
		"count and each":   {files: map[string]string{"stack/main.tf": "resource \"aws_sqs_queue\" \"q\" {\n  count = 1\n  for_each = {}\n}\n"}, want: "both count and for_each"},
		"duplicate":        {files: map[string]string{"stack/main.tf": "resource \"aws_sqs_queue\" \"q\" {}\n", "stack/b.tf": "resource \"aws_sqs_queue\" \"q\" {}\n"}, want: "declared twice"},
		"too deep": {files: map[string]string{
			"stack/main.tf":   `module "m" { source = "./m" }`,
			"stack/m/main.tf": `module "m" { source = "../m" }`,
		}, want: "deeper than 8"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tfFS(t, tc.files, nil, tc.varFiles...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTFUnresolvedIdentitySkips(t *testing.T) {
	p := mustTF(t, map[string]string{"stack/main.tf": `
data "aws_caller_identity" "me" {}
resource "aws_sqs_queue" "a" {
  name       = "a"
  fifo_queue = data.aws_caller_identity.me.id == "x"
}
resource "aws_dynamodb_table" "t" {
  name     = "tbl"
  hash_key = "id"
  stream_enabled = true
  stream_view_type = "KEYS_ONLY"
  attribute {
    name = "id"
    type = "S"
  }
}
resource "aws_sqs_queue" "stream_named" {
  name = substr(aws_dynamodb_table.t.stream_arn, 0, 10)
}
resource "aws_sqs_queue" "cut" {
  name = substr("x${aws_dynamodb_table.t.arn}", 0, 2)
}
resource "aws_sqs_queue" "keys" {
  for_each = toset([aws_dynamodb_table.t.arn])
  name     = "k"
}
`}, nil)
	if len(p.Resources) != 1 || p.Resources[0].LogicalID != "aws_dynamodb_table.t" {
		t.Fatalf("resources %v, warnings %v", p.Resources, p.Warnings)
	}
	for _, want := range [][]string{
		{"aws_sqs_queue.a", "fifo_queue", "data source", "skipped"},
		{"aws_sqs_queue.stream_named", "name", "skipped"},
		{"aws_sqs_queue.cut", "name", "substr() reads st.aws_dynamodb_table.t.arn", "skipped"},
		{"aws_sqs_queue.keys", "toset() reads st.aws_dynamodb_table.t.arn", "skipped"},
	} {
		if !hasWarning(p.Warnings, want...) {
			t.Errorf("no warning %v in %v", want, p.Warnings)
		}
	}
}

func TestTFDynamicAndJSONPolicy(t *testing.T) {
	p := mustTF(t, map[string]string{"stack/main.tf": `
resource "aws_sqs_queue" "q" { name = "q" }
resource "aws_sns_topic" "t" { name = "t" }
resource "aws_sqs_queue_policy" "q" {
  queue_url = aws_sqs_queue.q.url
  policy = jsonencode({
    Statement = [{
      Effect = "Allow"
      Action = "sqs:SendMessage"
      Resource = aws_sqs_queue.q.arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_sns_topic.t.arn } }
    }]
  })
}
resource "aws_sqs_queue_redrive_allow_policy" "q" {
  queue_url = "q"
  redrive_allow_policy = jsonencode({ redrivePermission = "allowAll" })
}
resource "aws_sns_topic_policy" "t" {
  arn    = aws_sns_topic.t.arn
  policy = jsonencode({ Statement = [] })
}
`}, nil)
	q := p.Resource("st.aws_sqs_queue.q")
	pol, err := p.Resolve(q.Queue.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pol, `"Resource":"arn:aws:sqs:eu-west-1:000000000000:q"`) || !strings.Contains(pol, `"aws:SourceArn":"arn:aws:sns:eu-west-1:000000000000:t"`) {
		t.Errorf("policy %s", pol)
	}
	if s, _ := q.Queue.Attributes["RedriveAllowPolicy"].Literal(); s != `{"redrivePermission":"allowAll"}` {
		t.Errorf("redrive allow %q", s)
	}
	if p.Resource("st.aws_sns_topic.t").Topic.Policy == nil {
		t.Error("topic policy not attached")
	}
}
