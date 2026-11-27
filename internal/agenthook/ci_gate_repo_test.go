package agenthook

import "testing"

func TestGateQuestionGHRepoForms(t *testing.T) {
	for command, want := range map[string]string{
		"gh --repo=o/c pr create":    "o/c",
		"gh pr create -R o/d --fill": "o/d",
		"gh pr ready --repo o/e 3":   "o/e",
		"gh -Ro/f pr ready":          "o/f",
		"gh pr create -- --repo o/g": "",
		"gh -R o/a -R o/b pr create": "o/b",
	} {
		q, ok := GateQuestionForCommand(command)
		if !ok || q.Repo != want {
			t.Fatalf("%q: %+v %v, want repo %q", command, q, ok, want)
		}
	}
}
