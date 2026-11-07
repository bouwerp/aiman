package awsdelegation

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The permission set and the laptop's session policy must stay one document:
// removing the indentation has to give BuildRegionPolicy's exact bytes.
func TestPermissionSetPolicyCompactsToRegionPolicy(t *testing.T) {
	for _, regions := range [][]string{{"us-east-2"}, {"us-east-2", "eu-west-1"}} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(PermissionSetPolicy(regions))); err != nil {
			t.Fatalf("compact %q: %v", regions, err)
		}
		if got, want := compact.String(), BuildRegionPolicy(regions); got != want {
			t.Fatalf("regions %q:\n got %s\nwant %s", regions, got, want)
		}
	}
}
