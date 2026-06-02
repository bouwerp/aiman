package ui

import (
	"reflect"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestAWSResetProfilesForRemote(t *testing.T) {
	remote := config.Remote{
		AWSDelegation:  &config.AWSDelegation{Profile: ""},
		AWSDelegations: []*config.AWSDelegation{{Profile: "prod"}, {Profile: "prod"}},
	}

	got := awsResetProfilesForRemote(remote, "staging")
	want := []string{"default", "prod", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected reset profiles:\nwant: %#v\ngot:  %#v", want, got)
	}
}
