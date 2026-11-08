package awsdelegation

import (
	"strings"
	"testing"
)

func TestApplyDelegatedCredentialsEmptyFileGetsOnlyBlock(t *testing.T) {
	f := &spliceRemote{home: "/home/dev"}
	creds := spliceCreds()
	if got, want := applyOnce(t, f, "dev", creds), FormatCredentialsSection("dev", creds); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRemoveCredentialsSectionKeepsCommentOnNextHeader(t *testing.T) {
	in := "[phone]\nk = 1\n\n# keep me\n[other]\nx = 1\n"
	if got, want := removeCredentialsSection(in, "phone"), "\n# keep me\n[other]\nx = 1\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := removeCredentialsSection(in, "Phone"); got != in {
		t.Fatalf("case variant removed: %q", got)
	}
	if strings.Contains(removeCredentialsSection(in, "other"), "[other]") {
		t.Fatal("[other] kept")
	}
}
