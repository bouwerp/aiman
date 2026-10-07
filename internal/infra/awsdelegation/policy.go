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

// route53DNSActions are hosted-zone reads and the writes needed to create a
// zone and publish ACM validation records. Route53 has no RequestedRegion.
// List* and Get* keep the generated session policy under the packed-policy cap.
var route53DNSActions = []string{
	"route53:List*",
	"route53:Get*",
	"route53:CreateHostedZone",
	"route53:ChangeResourceRecordSets",
}

// iamPolicyActions are IAM APIs used to inspect a role's or user's policies,
// simulate access, and apply a policy-document change (managed versions or
// inline, on either principal type). IAM is global (API in us-east-1); they
// have no RequestedRegion. Narrow action-family wildcards keep the generated
// session policy below STS's separate packed-policy limit.
var iamPolicyActions = []string{
	// Get* and List* are the IAM reads. IAM has no RequestedRegion, so these
	// cannot be limited to us-east-1. List* does not cover CreateAccessKey.
	"iam:Get*",
	"iam:List*",
	"iam:SimulatePrincipalPolicy",
	"iam:*PolicyVersion*",
	"iam:*RolePolicy",
	"iam:CreateRole",
	"iam:DeleteRole",
	"iam:UpdateRole",
	"iam:TagRole",
	"iam:UntagRole",
	"iam:PassRole",
	"iam:*UserPolicy",
	"iam:TagUser",
	"iam:UntagUser",
	// Access-key lifecycle. A region lock otherwise denies rotating an IAM
	// user's keys (SES SMTP).
	"iam:*AccessKey*",
}

// edgeGlobalActions are CloudFront calls. The API is global and does not
// follow the session's locked working region, same as Route53.
var edgeGlobalActions = []string{
	"cloudfront:*",
}

// usEast1EdgeActions are CloudFormation and ACM calls that must succeed in
// us-east-1 even when the delegated session is locked to another region.
// A CloudFront distribution can only use an ACM certificate from us-east-1,
// and the stack that requests that certificate has to be deployed there.
// A CloudFront-scope WAFv2 WebACL is likewise only creatable in us-east-1,
// and publishing its ARN to SSM Parameter Store for other stacks to consume
// needs the matching write action there too. DynamoDB reads are included so
// a session locked to another region can still inspect us-east-1 tables.
var usEast1EdgeActions = []string{
	"cloudformation:*",
	"acm:*",
	"wafv2:*",
	"ssm:PutParameter",
	// Secret, function, API Gateway, key, and backup-vault calls in us-east-1.
	// The region lock otherwise denies them when the session is locked to
	// another region. CreateKey, TagResource, and PutKeyPolicy stay
	// unconditional: those checks have no RequestedRegion. Family wildcards
	// keep the session policy under the packed-policy cap. kms:* includes
	// alias deletion, deletion scheduling, grants, and data-key calls that
	// vault creation requires. backup:* and backup-storage:* are the vault
	// control plane and its storage companion.
	"secretsmanager:DescribeSecret",
	"lambda:Get*",
	"lambda:List*",
	"lambda:UpdateFunction*",
	"lambda:TagResource",
	// Management API verbs. GET alone does not cover a terraform apply.
	"apigateway:*",
	"kms:*",
	"backup:*",
	"backup-storage:*",
	// Table inspection and state-lock writes in us-east-1. Item reads (GetItem,
	// Query, Scan, ListTables) are already unconditional. Describe* covers
	// DescribeTimeToLive. PutItem and DeleteItem in the locked region are the
	// Action * statement. The statements use Resource *, so one table ARN does
	// not need its own allow.
	"dynamodb:Describe*",
	"dynamodb:List*",
	"dynamodb:Get*",
	"dynamodb:BatchGetItem",
	"dynamodb:PutItem",
	"dynamodb:DeleteItem",
}

// anyRegionMutations are table and Lambda configuration writes. Action *
// already allows them in the locked region. They have no region condition,
// so the same calls work in every other region.
var anyRegionMutations = []string{
	"dynamodb:UpdateTable",
	"dynamodb:UpdateContinuousBackups",
	"lambda:UpdateFunctionConfiguration",
}

// kmsCreateActions are key creation, the tag call it makes, and PutKeyPolicy.
// All three are authorized without aws:RequestedRegion, so the region lock
// denies them even in the session's working region.
var kmsCreateActions = []string{
	"kms:CreateKey",
	"kms:TagResource",
	"kms:PutKeyPolicy",
}

// resourceDiscoveryActions are read-only inventory and item-read calls
// (Lambda, DynamoDB) that a delegated session may need to run against the
// account regardless of the locked working region, e.g. checking what exists
// elsewhere in the account before switching regions.
var resourceDiscoveryActions = []string{
	"lambda:ListFunctions",
	"lambda:GetFunctionConfiguration",
	"dynamodb:ListTables",
	"dynamodb:GetItem",
	"dynamodb:Query",
	"dynamodb:Scan",
}

// BuildRegionPolicy returns an inline IAM JSON policy that restricts all
// actions to the given AWS regions via the aws:RequestedRegion condition,
// plus unconditional Route53, IAM, S3, KMS key creation, and resource-discovery
// access (those APIs are global or need to work regardless of the session's
// locked region).
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
			// otherwise denies CreateHostedZone, ListHostedZones, and ACM DNS
			// validation records.
			{
				Effect:   "Allow",
				Action:   route53DNSActions,
				Resource: "*",
			},
			// IAM is global (API in us-east-1). A RequestedRegion lock
			// otherwise denies ListRolePolicies / CreateRole / PutUserPolicy.
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
			// Account-wide inventory reads a delegated session may need
			// regardless of the locked working region.
			{
				Effect:   "Allow",
				Action:   resourceDiscoveryActions,
				Resource: "*",
			},
			{
				Effect:   "Allow",
				Action:   anyRegionMutations,
				Resource: "*",
			},
			{
				Effect:   "Allow",
				Action:   edgeGlobalActions,
				Resource: "*",
			},
			{
				Effect:   "Allow",
				Action:   kmsCreateActions,
				Resource: "*",
			},
			{
				Effect:   "Allow",
				Action:   usEast1EdgeActions,
				Resource: "*",
				Condition: map[string]any{
					"StringEquals": map[string]any{
						"aws:RequestedRegion": "us-east-1",
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
