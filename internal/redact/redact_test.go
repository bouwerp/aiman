package redact

import (
	"slices"
	"testing"
)

func TestLinesHidesPEMBody(t *testing.T) {
	in := []string{
		"loading key",
		"-----BEGIN RSA PRIVATE KEY-----",
		"MIIEowIBAAKCAQEA0Z3VS5JJcds3xfn",
		"abcdEFGH",
		"-----END RSA PRIVATE KEY-----",
		"done",
	}
	out, n := Lines(in, nil)
	want := []string{"loading key", Mark, Mark, Mark, Mark, "done"}
	if !slices.Equal(out, want) || n != 4 {
		t.Fatalf("Lines = %q, %d", out, n)
	}
}

func TestLineKeepsPrefixes(t *testing.T) {
	cases := map[string]string{
		"https://hooks.slack.com/services/T0/B0/xyz": "https://hooks.slack.com/services/" + Mark,
		"Authorization: Bearer abc.def":              "Authorization: Bearer " + Mark,
		"postgres://u:p@h/db":                        "postgres://" + Mark + "@h/db",
		"/v1?access_token=abc&x=1":                   "/v1?access_token=" + Mark + "&x=1",
	}
	for in, want := range cases {
		if got, n := Line(in, nil); got != want || n != 1 {
			t.Errorf("Line(%q) = %q, %d; want %q, 1", in, got, n, want)
		}
	}
}
