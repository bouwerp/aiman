package awsdelegation

import (
	"strings"
	"testing"
)

func TestMergeProfileIntoConfig_Insert(t *testing.T) {
	got := MergeProfileIntoConfig("", "delegated-access",
		"arn:aws:iam::111:role/TemporaryDelegatedRole", "their-default-profile", "")
	if !strings.Contains(got, "[profile delegated-access]") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "role_arn = arn:aws:iam::111:role/TemporaryDelegatedRole") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "source_profile = their-default-profile") {
		t.Fatal(got)
	}
}

func TestMergeProfileIntoConfig_Insert_WithRegion(t *testing.T) {
	got := MergeProfileIntoConfig("", "delegated-access",
		"arn:aws:iam::111:role/R", "src", "ap-southeast-2")
	if !strings.Contains(got, "region = ap-southeast-2") {
		t.Fatalf("expected region line, got:\n%s", got)
	}
}

func TestMergeProfileIntoConfig_Replace(t *testing.T) {
	before := `[default]
region = us-east-1

[profile delegated-access]
role_arn = arn:aws:iam::OLD:role/Old
source_profile = oldsrc

[profile other]
region = eu-west-1
`
	got := MergeProfileIntoConfig(before, "delegated-access",
		"arn:aws:iam::NEW:role/New", "newsrc", "")
	if strings.Contains(got, "arn:aws:iam::OLD:role/Old") {
		t.Fatalf("old block should be gone:\n%s", got)
	}
	if !strings.Contains(got, "arn:aws:iam::NEW:role/New") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "source_profile = newsrc") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "[profile other]") {
		t.Fatal(got)
	}
}

func TestMergeProfileIntoConfig_DefaultProfile(t *testing.T) {
	got := MergeProfileIntoConfig("", "default",
		"arn:aws:iam::111:role/R", "src", "")
	if !strings.Contains(got, "[default]") {
		t.Errorf("expected [default] for profile name 'default', got:\n%s", got)
	}
	if strings.Contains(got, "[profile default]") {
		t.Errorf("did not expect [profile default], got:\n%s", got)
	}
}

func TestMergeProfileIntoConfig_Remove(t *testing.T) {
	before := `[profile delegated-access]
role_arn = arn:aws:iam::1:role/R
source_profile = s

[default]
region = x
`
	got := MergeProfileIntoConfig(before, "delegated-access", "", "", "")
	if strings.Contains(got, "delegated-access") {
		t.Fatalf("profile should be removed:\n%s", got)
	}
	if !strings.Contains(got, "[default]") {
		t.Fatal(got)
	}
}
