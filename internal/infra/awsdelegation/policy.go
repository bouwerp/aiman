package awsdelegation

import (
	"encoding/json"
	"strings"
)

type regionPolicyStatement struct {
	Effect    string         `json:"Effect"`
	Action    any            `json:"Action"`
	Resource  string         `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

type regionPolicy struct {
	Version   string                  `json:"Version"`
	Statement []regionPolicyStatement `json:"Statement"`
}

// route53DNSActions are hosted-zone APIs needed to find a zone and write ACM
// (or similar) DNS validation records. They have no RequestedRegion.
var route53DNSActions = []string{
	"route53:ListHostedZones",
	"route53:ListHostedZonesByName",
	"route53:GetHostedZone",
	"route53:ListResourceRecordSets",
	"route53:ChangeResourceRecordSets",
	"route53:GetChange",
}

// iamPolicyActions are IAM APIs used to inspect a role's policies, simulate
// access, and apply a policy-document change (managed versions or inline).
// IAM is global (API in us-east-1); they have no RequestedRegion.
var iamPolicyActions = []string{
	"iam:GetRole",
	"iam:ListRolePolicies",
	"iam:GetRolePolicy",
	"iam:ListAttachedRolePolicies",
	"iam:GetPolicy",
	"iam:GetPolicyVersion",
	"iam:SimulatePrincipalPolicy",
	"iam:CreatePolicyVersion",
	"iam:SetDefaultPolicyVersion",
	"iam:ListPolicyVersions",
	"iam:DeletePolicyVersion",
	"iam:PutRolePolicy",
	"iam:DeleteRolePolicy",
	"iam:CreateRole",
	"iam:DeleteRole",
	"iam:UpdateRole",
	"iam:UpdateAssumeRolePolicy",
	"iam:AttachRolePolicy",
	"iam:DetachRolePolicy",
	"iam:TagRole",
	"iam:UntagRole",
	"iam:ListRoles",
	"iam:PassRole",
}

// BuildRegionPolicy returns an inline IAM JSON policy that restricts all
// actions to the given AWS regions via the aws:RequestedRegion condition,
// plus unconditional Route53, IAM, and S3 access (those APIs are global or
// need to work regardless of the session's locked region).
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
			// Route53 is global (API in us-east-1). A RequestedRegion lock
			// otherwise denies ListHostedZones and ACM DNS validation records.
			{
				Effect:   "Allow",
				Action:   route53DNSActions,
				Resource: "*",
			},
			// IAM is global (API in us-east-1). A RequestedRegion lock
			// otherwise denies ListRolePolicies / CreateRole.
			{
				Effect:   "Allow",
				Action:   iamPolicyActions,
				Resource: "*",
			},
			// ListBuckets is global; CreateBucket in us-east-1 has no
			// LocationConstraint. A RequestedRegion lock otherwise denies
			// listing and some creates even when object APIs in-region work.
			{
				Effect:   "Allow",
				Action:   "s3:*",
				Resource: "*",
			},
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}
