package awsiac

import (
	"errors"
	"fmt"
	"slices"
)

// check skips or trims resources whose references do not resolve, until
// nothing changes: a resource that loses an identity reference (a
// subscription's topic or endpoint) is skipped, which may in turn affect
// another. Any other property that does not resolve is dropped with a
// warning; so is one that needs an attribute the emulator gives only later
// (a stream ARN in a policy). Outputs that do not resolve are dropped too.
func (p *Plan) check() {
	for changed := true; changed; {
		changed = false
		for _, r := range slices.Clone(p.Resources) {
			if !p.checkResource(r) {
				changed = true
			}
		}
	}
	outs := p.Outputs[:0]
	for _, o := range p.Outputs {
		if err := p.valueStatus(o.Value); err != nil && !errors.Is(err, ErrPending) {
			p.Warn(Warning{Stack: o.Stack, Resource: "outputs." + o.Name, Message: err.Error() + "; the output is left out"})
			continue
		}
		outs = append(outs, o)
	}
	p.Outputs = outs
}

// usable reports whether a property value resolves now, warning and
// asking the caller to drop it otherwise.
func (p *Plan) usable(r *Resource, attr string, v Value) bool {
	err := p.valueStatus(v)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrPending) {
		err = errors.New("needs an attribute the emulator gives only when the resource is created")
	}
	p.Warn(Warning{Stack: r.Stack, Resource: r.LogicalID, Attribute: attr, File: r.File, Line: r.Line, Message: err.Error() + "; the property is left out"})
	return false
}

// checkResource returns false when it skipped r.
func (p *Plan) checkResource(r *Resource) bool {
	if !slices.Contains(p.Services, kindService(r.Kind)) {
		p.skip(r, "", fmt.Sprintf("the aws service does not load %s (aws.services)", kindService(r.Kind)))
		return false
	}
	deps := r.DependsOn[:0]
	for _, d := range r.DependsOn {
		if p.Resource(Key(r.Stack, d)) == nil {
			p.Warn(Warning{Stack: r.Stack, Resource: r.LogicalID, Attribute: "DependsOn", File: r.File, Line: r.Line,
				Message: fmt.Sprintf("%s is not a planned resource; the dependency is left out", d)})
			continue
		}
		deps = append(deps, d)
	}
	r.DependsOn = deps
	switch r.Kind {
	case KindTable:
		if err := checkTable(r.Table); err != nil {
			p.skip(r, "KeySchema", err.Error())
			return false
		}
	case KindBucket:
		b := r.Bucket
		if b.Policy != nil && !p.usable(r, "Policy", b.Policy) {
			b.Policy = nil
		}
		ns := b.Notifications[:0]
		for i, n := range b.Notifications {
			if (n.Destination != KindQueue && n.Destination != KindTopic) || len(n.Events) == 0 {
				p.Warn(Warning{Stack: r.Stack, Resource: r.LogicalID, Attribute: fmt.Sprintf("NotificationConfiguration[%d]", i), File: r.File, Line: r.Line,
					Message: "a notification needs a queue or topic destination and events; it is left out"})
				continue
			}
			if p.usable(r, fmt.Sprintf("NotificationConfiguration[%d]", i), n.ARN) {
				ns = append(ns, n)
			}
		}
		b.Notifications = ns
	case KindQueue:
		p.trimAttrs(r, r.Queue.Attributes)
		if r.Queue.Policy != nil && !p.usable(r, "Policy", r.Queue.Policy) {
			r.Queue.Policy = nil
		}
	case KindTopic:
		p.trimAttrs(r, r.Topic.Attributes)
		if r.Topic.Policy != nil && !p.usable(r, "Policy", r.Topic.Policy) {
			r.Topic.Policy = nil
		}
	case KindSubscription:
		s := r.Subscription
		if s.Protocol != "sqs" {
			p.skip(r, "Protocol", fmt.Sprintf("protocol %q is not emulated; only sqs subscriptions are", s.Protocol))
			return false
		}
		for _, f := range []struct {
			name string
			v    Value
		}{{"TopicArn", s.Topic}, {"Endpoint", s.Endpoint}} {
			if err := p.valueStatus(f.v); err != nil || f.v == nil {
				if err == nil {
					err = errors.New("is empty")
				}
				p.skip(r, f.name, err.Error())
				return false
			}
		}
		p.trimAttrs(r, s.Attributes)
	}
	return true
}

func (p *Plan) trimAttrs(r *Resource, attrs map[string]Value) {
	for _, k := range sortedKeys(attrs) {
		if !p.usable(r, k, attrs[k]) {
			delete(attrs, k)
		}
	}
}

// checkTable checks a table's key schema against its attribute definitions.
func checkTable(t *Table) error {
	defined := map[string]bool{}
	for _, a := range t.Attributes {
		if a.Type != "S" && a.Type != "N" && a.Type != "B" {
			return fmt.Errorf("attribute %s has type %q, not S, N or B", a.Name, a.Type)
		}
		defined[a.Name] = true
	}
	need := func(what, name string) error {
		if name != "" && !defined[name] {
			return fmt.Errorf("%s %q has no attribute definition", what, name)
		}
		return nil
	}
	if t.HashKey == "" {
		return errors.New("the table has no hash key")
	}
	errs := []error{need("hash key", t.HashKey), need("range key", t.RangeKey)}
	for _, ix := range t.GSIs {
		if ix.Name == "" || ix.HashKey == "" {
			return errors.New("a global secondary index needs a name and a hash key")
		}
		errs = append(errs, need("index "+ix.Name+" key", ix.HashKey), need("index "+ix.Name+" key", ix.RangeKey))
	}
	for _, ix := range t.LSIs {
		if ix.Name == "" || ix.RangeKey == "" {
			return errors.New("a local secondary index needs a name and a range key")
		}
		if ix.HashKey != "" && ix.HashKey != t.HashKey {
			return fmt.Errorf("local secondary index %s must share the table's hash key %s", ix.Name, t.HashKey)
		}
		errs = append(errs, need("index "+ix.Name+" key", ix.RangeKey))
	}
	return errors.Join(errs...)
}

// phase orders kinds: queues and topics, then (in steps) their policies,
// subscriptions, tables, buckets and bucket configuration.
func phase(k Kind) int {
	switch k {
	case KindQueue, KindTopic:
		return 0
	case KindSubscription:
		return 2
	case KindTable:
		return 3
	default:
		return 4
	}
}

// createDeps are the resources r's create call needs first: references
// outside policies (policies are applied once every queue and topic
// exists, and bucket policies after the bucket) and DependsOn.
func createDeps(r *Resource) []string {
	var vs []Value
	switch {
	case r.Queue != nil:
		for _, k := range sortedKeys(r.Queue.Attributes) {
			vs = append(vs, r.Queue.Attributes[k])
		}
	case r.Topic != nil:
		for _, k := range sortedKeys(r.Topic.Attributes) {
			vs = append(vs, r.Topic.Attributes[k])
		}
	case r.Subscription != nil:
		vs = append(vs, r.Subscription.Topic, r.Subscription.Endpoint)
		for _, k := range sortedKeys(r.Subscription.Attributes) {
			vs = append(vs, r.Subscription.Attributes[k])
		}
	case r.Bucket != nil:
		for _, n := range r.Bucket.Notifications {
			vs = append(vs, n.ARN)
		}
	}
	var out []string
	for _, v := range vs {
		for _, a := range v.Refs() {
			if a.Key() != r.Key() {
				out = append(out, a.Key())
			}
		}
	}
	for _, d := range r.DependsOn {
		out = append(out, Key(r.Stack, d))
	}
	return out
}

// order sorts the resources by phase, then by dependencies, keeping plan
// order otherwise. A dependency on a later phase is met by the phase
// order itself; a cycle is an error naming its members.
func (p *Plan) order() ([]*Resource, error) {
	index := map[string]int{}
	for i, r := range p.Resources {
		index[r.Key()] = i
	}
	deps := map[string][]string{}
	for _, r := range p.Resources {
		for _, d := range createDeps(r) {
			if _, ok := index[d]; ok && phase(p.Resources[index[d]].Kind) == phase(r.Kind) {
				deps[r.Key()] = append(deps[r.Key()], d)
			}
		}
	}
	done := map[string]bool{}
	var out []*Resource
	for len(out) < len(p.Resources) {
		var next *Resource
		for _, r := range p.Resources {
			if done[r.Key()] || !allDone(deps[r.Key()], done) {
				continue
			}
			if next == nil || phase(r.Kind) < phase(next.Kind) {
				next = r
			}
		}
		if next == nil {
			var cyc []string
			for _, r := range p.Resources {
				if !done[r.Key()] {
					cyc = append(cyc, r.Key())
				}
			}
			return nil, fmt.Errorf("awsiac: resources depend on each other in a cycle: %v", cyc)
		}
		done[next.Key()] = true
		out = append(out, next)
	}
	return out, nil
}

func allDone(keys []string, done map[string]bool) bool {
	for _, k := range keys {
		if !done[k] {
			return false
		}
	}
	return true
}

// steps lists the applier's calls for resources in order, then the seed.
func steps(order []*Resource, seed *Seed) []Step {
	var out []Step
	add := func(r *Resource, svc, op string) {
		out = append(out, Step{N: len(out) + 1, Resource: r.Key(), Service: svc, Operation: op})
	}
	for _, r := range order {
		switch r.Kind {
		case KindQueue:
			add(r, "sqs", "CreateQueue")
		case KindTopic:
			add(r, "sns", "CreateTopic")
		}
	}
	for _, r := range order {
		switch {
		case r.Queue != nil && r.Queue.Policy != nil:
			add(r, "sqs", "SetQueueAttributes")
		case r.Topic != nil && r.Topic.Policy != nil:
			add(r, "sns", "SetTopicAttributes")
		}
	}
	for _, r := range order {
		if r.Kind == KindSubscription {
			add(r, "sns", "Subscribe")
		}
	}
	for _, r := range order {
		if r.Kind == KindTable {
			add(r, "dynamodb", "CreateTable")
			if r.Table.TTLAttribute != "" {
				add(r, "dynamodb", "UpdateTimeToLive")
			}
		}
	}
	for _, r := range order {
		if r.Kind == KindBucket {
			add(r, "s3", "CreateBucket")
		}
	}
	for _, r := range order {
		if b := r.Bucket; b != nil {
			if len(b.Tags) > 0 {
				add(r, "s3", "PutBucketTagging")
			}
			if b.Versioning != "" {
				add(r, "s3", "PutBucketVersioning")
			}
			if b.Policy != nil {
				add(r, "s3", "PutBucketPolicy")
			}
			if len(b.Notifications) > 0 {
				add(r, "s3", "PutBucketNotificationConfiguration")
			}
		}
	}
	if seed != nil {
		for i := range seed.Objects {
			out = append(out, Step{N: len(out) + 1, Resource: seed.Objects[i].Resource, Service: "s3", Operation: "PutObject", Seed: &SeedStep{Kind: SeedKindObjects, Index: i}})
		}
		for i := range seed.Items {
			out = append(out, Step{N: len(out) + 1, Resource: seed.Items[i].Resource, Service: "dynamodb", Operation: "BatchWriteItem", Seed: &SeedStep{Kind: SeedKindItems, Index: i}})
		}
		for i := range seed.Messages {
			out = append(out, Step{N: len(out) + 1, Resource: seed.Messages[i].Resource, Service: "sqs", Operation: "SendMessageBatch", Seed: &SeedStep{Kind: SeedKindMessages, Index: i}})
		}
	}
	return out
}
