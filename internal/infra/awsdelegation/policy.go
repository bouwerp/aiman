package awsdelegation

import (
	"encoding/json"
	"strings"
)

type regionPolicyStatement struct {
	Effect    string         `json:"Effect"`
	Action    string         `json:"Action"`
	Resource  string         `json:"Resource"`
	Condition map[string]any `json:"Condition"`
}

type regionPolicy struct {
	Version   string                  `json:"Version"`
	Statement []regionPolicyStatement `json:"Statement"`
}

// BuildRegionPolicy returns an inline IAM JSON policy that restricts all
// actions to the given AWS regions via the aws:RequestedRegion condition.
// Returns an empty string when regions is nil or empty.
func BuildRegionPolicy(regions []string) string {
	trimmed := make([]string, 0, len(regions))
	for _, r := range regions {
		if r := strings.TrimSpace(r); r != "" {
			trimmed = append(trimmed, r)
		}
	}
	if len(trimmed) == 0 {
		return ""
	}

	var condition any
	if len(trimmed) == 1 {
		condition = trimmed[0]
	} else {
		condition = trimmed
	}

	p := regionPolicy{
		Version: "2012-10-17",
		Statement: []regionPolicyStatement{
			{
				Effect:   "Allow",
				Action:   "*",
				Resource: "*",
				Condition: map[string]any{
					"StringEquals": map[string]any{
						"aws:RequestedRegion": condition,
					},
				},
			},
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}
