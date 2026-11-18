package awsiac

import "testing"

// TestTFReferenceInspection checks that nothing reads the text of a
// resource reference: a comparison, a conditional on one, or a function
// that looks inside it makes the value unresolved with a warning naming
// the property, file and line, while functions that only carry a
// reference along still work.
func TestTFReferenceInspection(t *testing.T) {
	p := mustTF(t, map[string]string{
		"stack/main.tf": `
resource "aws_sqs_queue" "q" { name = "jobs" }
resource "aws_sqs_queue" "r" {
  count = 2
  name  = "r${count.index}"
}
locals {
  is_jobs = aws_sqs_queue.q.name == "jobs"
}
resource "aws_sns_topic" "eq" {
  name         = "eq"
  display_name = aws_sqs_queue.q.name == "jobs" ? "a" : "b"
}
resource "aws_sns_topic" "ne" {
  name         = "ne"
  display_name = aws_sqs_queue.q.name != "x" ? "a" : "b"
}
resource "aws_sns_topic" "via_local" {
  name         = "via-local"
  display_name = local.is_jobs ? "a" : "b"
}
resource "aws_sns_topic" "named" {
  name = aws_sqs_queue.q.name == "jobs" ? "picked" : "other"
}
output "contains" { value = contains([aws_sqs_queue.q.name], "jobs") }
output "length" { value = length(aws_sqs_queue.q.arn) }
output "lower" { value = lower(aws_sqs_queue.q.name) }
output "replace" { value = replace(aws_sqs_queue.q.arn, "arn:aws:sqs:", "") }
output "split" { value = split(":", aws_sqs_queue.q.arn)[0] }
output "lookup_key" { value = lookup({jobs = "x"}, aws_sqs_queue.q.name, "none") }
output "for_if" { value = join(",", [for q in aws_sqs_queue.r : q.url if q.name != "r0"]) }
output "format" { value = format("%s|%s", aws_sqs_queue.q.name, aws_sqs_queue.q.url) }
output "joined" { value = join(",", aws_sqs_queue.r[*].name) }
output "count" { value = length(aws_sqs_queue.r) }
output "element" { value = element(aws_sqs_queue.r[*].name, 1) }
output "lookup_value" { value = lookup({a = aws_sqs_queue.q.arn}, "a", "") }
output "coalesce" { value = coalesce("", aws_sqs_queue.q.name) }
output "policy" { value = jsonencode({ Resource = aws_sqs_queue.q.arn }) }
`,
		"stack/extra.tf.json": `{"output": {"json_eq": {"value": "${aws_sqs_queue.q.name == \"jobs\" ? \"y\" : \"n\"}"}}}`,
	}, nil)
	for _, want := range [][]string{
		{"st.aws_sns_topic.eq display_name", "== reads st.aws_sqs_queue.q.name", "the property is left out", "stack/main.tf:12"},
		{"st.aws_sns_topic.ne display_name", "!= reads st.aws_sqs_queue.q.name", "stack/main.tf:16"},
		{"st.aws_sns_topic.via_local display_name", "== reads st.aws_sqs_queue.q.name"},
		{"st.aws_sns_topic.named name", "== reads st.aws_sqs_queue.q.name", "the resource is skipped", "stack/main.tf:23"},
		{"st.outputs.contains", "contains() reads"},
		{"st.outputs.length", "length() reads st.aws_sqs_queue.q.arn"},
		{"st.outputs.lower", "lower() reads"},
		{"st.outputs.replace", "replace() reads"},
		{"st.outputs.split", "split() reads"},
		{"st.outputs.lookup_key", "lookup() reads"},
		{"st.outputs.for_if", "!= reads st.aws_sqs_queue.r"},
		{"st.outputs.json_eq", "== reads st.aws_sqs_queue.q.name", "stack/extra.tf.json"},
	} {
		if !hasWarning(p.Warnings, want...) {
			t.Errorf("no warning %v in %v", want, p.Warnings)
		}
	}
	if p.Resource("st.aws_sns_topic.named") != nil {
		t.Error("a name chosen by comparing a reference was planned")
	}
	if eq := p.Resource("st.aws_sns_topic.eq"); eq == nil || eq.Topic.Attributes["DisplayName"] != nil {
		t.Errorf("eq topic %+v", eq)
	}
	for name, want := range map[string]string{
		"format":       "jobs|http://aws:4566/000000000000/jobs",
		"joined":       "r0,r1",
		"count":        "2",
		"element":      "r1",
		"lookup_value": "arn:aws:sqs:eu-west-1:000000000000:jobs",
		"coalesce":     "jobs",
		"policy":       `{"Resource":"arn:aws:sqs:eu-west-1:000000000000:jobs"}`,
	} {
		if got := tfOutputText(t, p, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, o := range []string{"contains", "length", "lower", "replace", "split", "lookup_key", "for_if", "json_eq"} {
		if _, ok := p.Output("st", o); ok {
			t.Errorf("output %s was evaluated from a reference", o)
		}
	}
}
