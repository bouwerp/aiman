package awsdelegation

import (
	"encoding/json"
	"testing"
)

func TestParseGetCallerIdentityJSON(t *testing.T) {
	const raw = `{"UserId": "AIDAI...","Account": "123456789012","Arn": "arn:aws:iam::123456789012:user/x"}`
	var o getCallerIdentityOutput
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	if o.Account != "123456789012" {
		t.Fatal(o.Account)
	}
}
