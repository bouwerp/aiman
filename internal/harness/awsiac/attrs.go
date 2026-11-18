package awsiac

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Canonical attribute names, the keys of Resource.Attrs and of the
// manifest's attributes. References use the source's own names, which
// canonicalAttr maps here.
const (
	AttrRefValue           = "ref"
	AttrARN                = "arn"
	AttrName               = "name"
	AttrURL                = "url"
	AttrStreamARN          = "stream_arn"
	AttrStreamLabel        = "stream_label"
	AttrDomainName         = "domain_name"
	AttrRegionalDomainName = "regional_domain_name"
)

// ErrPending marks an attribute the emulator gives only when the applier
// creates the resource (a stream ARN, a subscription ARN).
var ErrPending = errors.New("known once the plan is applied")

// attrNames maps a source's attribute names, per kind, to canonical ones.
// The empty name is the Ref value (CloudFormation Ref, Terraform id).
var attrNames = map[Source]map[Kind]map[string]string{
	SourceCloudFormation: {
		KindTable:        {"": AttrRefValue, "Arn": AttrARN, "StreamArn": AttrStreamARN},
		KindBucket:       {"": AttrRefValue, "Arn": AttrARN, "DomainName": AttrDomainName, "RegionalDomainName": AttrRegionalDomainName},
		KindQueue:        {"": AttrRefValue, "Arn": AttrARN, "QueueName": AttrName, "QueueUrl": AttrURL},
		KindTopic:        {"": AttrRefValue, "TopicArn": AttrARN, "TopicName": AttrName},
		KindSubscription: {"": AttrRefValue, "Arn": AttrARN},
	},
	SourceTerraform: {
		KindTable:        {"": AttrRefValue, "id": AttrRefValue, "name": AttrName, "arn": AttrARN, "stream_arn": AttrStreamARN, "stream_label": AttrStreamLabel},
		KindBucket:       {"": AttrRefValue, "id": AttrRefValue, "bucket": AttrName, "arn": AttrARN, "bucket_domain_name": AttrDomainName, "bucket_regional_domain_name": AttrRegionalDomainName},
		KindQueue:        {"": AttrRefValue, "id": AttrRefValue, "url": AttrURL, "arn": AttrARN, "name": AttrName},
		KindTopic:        {"": AttrRefValue, "id": AttrRefValue, "arn": AttrARN, "name": AttrName},
		KindSubscription: {"": AttrRefValue, "id": AttrRefValue, "arn": AttrARN},
	},
}

// canonicalAttr maps a reference's attribute name to a canonical one.
func canonicalAttr(r *Resource, attr string) (string, bool) {
	c, ok := attrNames[r.Source][r.Kind][attr]
	return c, ok
}

// AttrNames lists the attribute names a reference to r may use.
func AttrNames(r *Resource) []string {
	avail := map[string]bool{}
	for k := range baseAttrs(&Plan{}, r) {
		avail[k] = true
	}
	for _, k := range pendingAttrs(r) {
		avail[k] = true
	}
	var out []string
	for name, c := range attrNames[r.Source][r.Kind] {
		if name != "" && avail[c] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// baseAttrs are the attributes known from the physical name alone.
func baseAttrs(p *Plan, r *Resource) map[string]string {
	n := r.Physical
	arn := func(svc, res string) string {
		return "arn:aws:" + svc + ":" + p.Region + ":" + AccountID + ":" + res
	}
	switch r.Kind {
	case KindTable:
		return map[string]string{AttrRefValue: n, AttrName: n, AttrARN: arn("dynamodb", "table/"+n)}
	case KindBucket:
		return map[string]string{
			AttrRefValue: n, AttrName: n, AttrARN: "arn:aws:s3:::" + n,
			AttrDomainName: n + ".s3.amazonaws.com", AttrRegionalDomainName: n + ".s3." + p.Region + ".amazonaws.com",
		}
	case KindQueue:
		url := strings.TrimRight(p.Endpoint, "/") + "/" + AccountID + "/" + n
		return map[string]string{AttrRefValue: url, AttrURL: url, AttrName: n, AttrARN: arn("sqs", n)}
	case KindTopic:
		a := arn("sns", n)
		return map[string]string{AttrRefValue: a, AttrARN: a, AttrName: n}
	}
	return map[string]string{}
}

// pendingAttrs are the attributes the applier learns from the emulator.
func pendingAttrs(r *Resource) []string {
	switch {
	case r.Table != nil && r.Table.StreamViewType != "":
		return []string{AttrStreamARN, AttrStreamLabel}
	case r.Kind == KindSubscription:
		return []string{AttrRefValue, AttrARN}
	}
	return nil
}

// Learn records an attribute the applier read from the emulator.
func (p *Plan) Learn(key, attr, value string) {
	if r := p.Resource(key); r != nil {
		if r.Attrs == nil {
			r.Attrs = map[string]string{}
		}
		r.Attrs[attr] = value
		if attr == AttrStreamARN {
			if _, label, ok := strings.Cut(value, "/stream/"); ok {
				r.Attrs[AttrStreamLabel] = label
			}
		}
	}
}

// refStatus says whether a reference resolves: nil, ErrPending (it will
// once the plan is applied), or why it cannot.
func (p *Plan) refStatus(a AttrRef) error {
	r := p.Resource(a.Key())
	if r == nil {
		return fmt.Errorf("%s is not a planned resource (skipped, not emulated or not in the stack)", a.Key())
	}
	c, ok := canonicalAttr(r, a.Attr)
	if !ok {
		return fmt.Errorf("%s has no attribute %q aiman models (%s)", a.Key(), a.Attr, strings.Join(AttrNames(r), ", "))
	}
	if v, ok := r.Attrs[c]; ok && v != "" {
		return nil
	}
	if slices.Contains(pendingAttrs(r), c) {
		return ErrPending
	}
	if c == AttrStreamARN || c == AttrStreamLabel {
		return fmt.Errorf("table %s has no stream", a.Key())
	}
	return fmt.Errorf("%s.%s is not known", a.Key(), a.Attr)
}

// valueStatus is refStatus over every reference of v: nil, ErrPending, or
// the first reason one cannot resolve.
func (p *Plan) valueStatus(v Value) error {
	pending := false
	for _, a := range v.Refs() {
		switch err := p.refStatus(a); {
		case err == nil:
		case errors.Is(err, ErrPending):
			pending = true
		default:
			return err
		}
	}
	if pending {
		return ErrPending
	}
	return nil
}

// Resolve turns a value into its text. It fails with ErrPending while a
// learned attribute is not known yet.
func (p *Plan) Resolve(v Value) (string, error) {
	var b strings.Builder
	for _, part := range v {
		if part.Ref == nil {
			b.WriteString(part.Lit)
			continue
		}
		if err := p.refStatus(*part.Ref); err != nil {
			return "", fmt.Errorf("%s: %w", part.Ref, err)
		}
		r := p.Resource(part.Ref.Key())
		c, _ := canonicalAttr(r, part.Ref.Attr)
		b.WriteString(r.Attrs[c])
	}
	return b.String(), nil
}
