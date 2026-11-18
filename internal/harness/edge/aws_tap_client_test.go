package edge

import "testing"

// TestAWSTapFixedClient: the applier and an operator write name themselves
// by User-Agent; anything else is named by the lookup.
func TestAWSTapFixedClient(t *testing.T) {
	cases := map[string]string{
		"aiman-harness-aws-apply/1":                  AWSClientAiman,
		"Go-http-client aiman-harness-aws-write/1":   AWSClientOperator,
		"Boto3/1.35 aiman-harness-aws-write/1 extra": AWSClientOperator,
		"Boto3/1.35": "",
	}
	for agent, want := range cases {
		if got := fixedClient(agent); got != want {
			t.Errorf("%q: %q, want %q", agent, got, want)
		}
	}
	var names clientNames
	if got := names.name("10.0.0.5", AWSClientOperator); got != AWSClientOperator {
		t.Errorf("label lost: %q", got)
	}
}
