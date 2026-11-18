package awsiac

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

func init() { RegisterImporter(SourceCloudFormation, ImportCloudFormation) }

// ImportCloudFormation reads a CloudFormation or SAM template (YAML with
// short-form tags, or JSON, including a CDK cdk.out template) into the
// plan: tables, buckets, queues, topics and SQS subscriptions, their
// policies and bucket notifications, the outputs and the functions'
// environments. A value it cannot evaluate is a warning naming the stack,
// resource, property, file and line; in a property that decides a
// resource's identity or schema the resource is skipped, elsewhere the
// property is left out. A template it cannot read, a parameter with no
// value or one that breaks its constraints is an error.
func ImportCloudFormation(ctx context.Context, src StackSource, p *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tmpl, err := readTemplate(src.Root, path.Clean(src.Path))
	if err != nil {
		return fmt.Errorf("%s: %w", src.Path, err)
	}
	s := &cfnStack{src: src, p: p, tmpl: tmpl, condMemo: map[string]int{}, condWhy: map[string]string{}, res: map[string]*cfnResource{}}
	if err := s.load(); err != nil {
		return fmt.Errorf("%s: %w", src.Path, err)
	}
	return s.plan()
}

// load reads the template's sections: transforms, parameters, conditions,
// mappings and the resource list with each resource's condition.
func (s *cfnStack) load() error {
	s.transforms()
	if err := s.loadParams(); err != nil {
		return err
	}
	s.conds = map[string]*cfnNode{}
	if c := s.tmpl.get("Conditions"); c != nil {
		if c.kind != cfnMap {
			return errors.New("the template's Conditions section is not a mapping")
		}
		for i, k := range c.keys {
			s.conds[k] = c.vals[i]
		}
	}
	s.mappings = s.tmpl.get("Mappings")
	rs := s.tmpl.get("Resources")
	if rs == nil || rs.kind != cfnMap {
		return errors.New("the template has no Resources mapping")
	}
	for i, id := range rs.keys {
		n := rs.vals[i]
		if n.kind != cfnMap {
			return fmt.Errorf("resource %s (line %d) is not a mapping", id, n.line)
		}
		typ, ok := scalarText(n.get("Type"))
		if !ok {
			return fmt.Errorf("resource %s (line %d) has no Type", id, n.line)
		}
		props := n.get("Properties")
		if props == nil {
			props = &cfnNode{kind: cfnMap, line: n.line}
		}
		s.res[id] = &cfnResource{id: id, typ: typ, node: n, props: props, line: n.line, state: condTrue}
		s.order = append(s.order, id)
	}
	if s.sam {
		s.applyGlobals()
	}
	// Conditions are evaluated once every resource is known, since a
	// condition never names a resource.
	for _, id := range s.order {
		r := s.res[id]
		c := r.node.get("Condition")
		if c == nil {
			continue
		}
		name, ok := scalarText(c)
		if !ok {
			r.state, r.why = condUnresolvable, "its Condition is not a name"
			continue
		}
		b, u := s.condition(name, c.line)
		switch {
		case u != nil:
			r.state, r.why = condUnresolvable, u.why
		case !b:
			r.state, r.why = condFalse, "condition "+name+" is false"
		}
	}
	return nil
}

// transforms reads Transform: SAM and AWS::LanguageExtensions are applied;
// any other macro is a warning and the template is read as written.
func (s *cfnStack) transforms() {
	t := s.tmpl.get("Transform")
	if t == nil {
		return
	}
	var names []string
	switch t.kind {
	case cfnScalar:
		names = []string{t.text}
	case cfnSeq:
		for _, it := range t.items {
			names = append(names, it.text)
		}
	case cfnMap:
		names = []string{"(a mapping)"}
	}
	for _, n := range names {
		switch n {
		case samTransform:
			s.sam = true
		case langExtTransform:
			s.langExt = true
		default:
			s.p.Warn(Warning{Stack: s.src.Name, Attribute: "Transform", File: s.src.Path, Line: t.line,
				Message: fmt.Sprintf("transform %s is a macro aiman does not run; the template is read as written", n)})
		}
	}
}

// Resource types the importer reads but does not create, besides the
// supported ones.
const (
	typeLambda           = "AWS::Lambda::Function"
	typeSAMFunction      = "AWS::Serverless::Function"
	typeBucketPolicy     = "AWS::S3::BucketPolicy"
	typeQueuePolicy      = "AWS::SQS::QueuePolicy"
	typeTopicPolicy      = "AWS::SNS::TopicPolicy"
	typeCDKNotifications = "Custom::S3BucketNotifications"
	typeCDKMetadata      = "AWS::CDK::Metadata"
	typeNestedStack      = "AWS::CloudFormation::Stack"
	typeSAMApplication   = "AWS::Serverless::Application"
)

// plan adds the template's resources, policies, functions and outputs.
func (s *cfnStack) plan() error {
	notEmulated := map[string][]string{}
	var attach []*cfnResource
	for _, id := range s.order {
		r := s.res[id]
		switch r.state {
		case condFalse:
			s.p.Note(Warning{Stack: s.src.Name, Resource: id, Attribute: "Condition", File: s.src.Path, Line: r.line, Message: r.why + "; the resource is left out"})
			continue
		case condUnresolvable:
			s.p.Warn(Warning{Stack: s.src.Name, Resource: id, Attribute: "Condition", File: s.src.Path, Line: r.line, Message: r.why + "; the resource is skipped"})
			continue
		}
		switch r.typ {
		case typeCDKMetadata:
		case typeBucketPolicy, typeQueuePolicy, typeTopicPolicy, typeCDKNotifications:
			attach = append(attach, r)
		case typeLambda, typeSAMFunction:
			s.function(r)
			notEmulated[r.typ] = append(notEmulated[r.typ], id)
		case typeNestedStack, typeSAMApplication:
			s.p.Note(Warning{Stack: s.src.Name, Resource: id, File: s.src.Path, Line: r.line,
				Message: fmt.Sprintf("nested stack (%s) is not imported; list its template as a stack of its own", r.typ)})
		default:
			if _, ok := supportedTypes[r.typ]; !ok {
				notEmulated[r.typ] = append(notEmulated[r.typ], id)
				continue
			}
			if err := s.resource(r); err != nil {
				return err
			}
		}
	}
	for _, r := range attach {
		s.attach(r)
	}
	for _, typ := range sortedKeys(notEmulated) {
		s.p.Note(Warning{Stack: s.src.Name, File: s.src.Path, Message: fmt.Sprintf("not emulated: %s (%s)", typ, strings.Join(notEmulated[typ], ", "))})
	}
	s.outputs()
	return nil
}

// build reads one resource's properties, recording what it used and what
// it could not resolve.
type build struct {
	s    *cfnStack
	r    *cfnResource
	used map[string]bool
	// skipped is set when an identity property does not resolve.
	skipped bool
}

func (s *cfnStack) newBuild(r *cfnResource) *build {
	return &build{s: s, r: r, used: map[string]bool{}}
}

// prop evaluates a property; nil when it is absent or AWS::NoValue.
func (b *build) prop(name string) *cval {
	b.used[name] = true
	n := b.r.props.get(name)
	if n == nil {
		return nil
	}
	v := b.s.eval(n)
	if v.kind == cvNoValue || v.kind == cvNull {
		return nil
	}
	return v
}

func (b *build) line(v *cval) int {
	if v != nil && v.line > 0 {
		return v.line
	}
	return b.r.line
}

func (b *build) warn(attr string, v *cval, msg string) {
	b.s.p.Warn(Warning{Stack: b.s.src.Name, Resource: b.r.id, Attribute: attr, File: b.s.src.Path, Line: b.line(v), Message: msg})
}

func (b *build) note(attr string, v *cval, msg string) {
	b.s.p.Note(Warning{Stack: b.s.src.Name, Resource: b.r.id, Attribute: attr, File: b.s.src.Path, Line: b.line(v), Message: msg})
}

// skip records the first identity failure; the resource is not planned.
func (b *build) skip(attr string, v *cval, msg string) {
	if b.skipped {
		return
	}
	b.skipped = true
	b.warn(attr, v, msg+"; the resource is skipped")
}

// identity checks a property that decides the resource's identity or
// schema: it must evaluate and name no other resource.
func (b *build) identity(attr string, v *cval) bool {
	if v == nil {
		return true
	}
	if u := v.firstUnres(); u != nil {
		b.skip(attr, u, u.why)
		return false
	}
	if v.hasRefs() {
		b.skip(attr, v, "names another resource; aiman needs this property literal before it names resources")
		return false
	}
	return true
}

// ok checks any other property: when it does not evaluate it is left out.
func (b *build) ok(attr string, v *cval) bool {
	if v == nil {
		return false
	}
	if u := v.firstUnres(); u != nil {
		b.warn(attr, u, u.why+"; the property is left out")
		return false
	}
	return true
}

// idString reads an identity string property; ok is false when the
// resource is skipped.
func (b *build) idString(name string) (string, bool) {
	v := b.prop(name)
	if v == nil {
		return "", true
	}
	if !b.identity(name, v) {
		return "", false
	}
	t, ok := v.literal()
	if !ok {
		b.skip(name, v, "is not a string")
		return "", false
	}
	return t, true
}

// idBool reads an identity bool property.
func (b *build) idBool(name string) (bool, bool) {
	v := b.prop(name)
	if v == nil {
		return false, true
	}
	if !b.identity(name, v) {
		return false, false
	}
	t, _ := v.literal()
	switch strings.ToLower(t) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	b.skip(name, v, fmt.Sprintf("%q is not true or false", t))
	return false, false
}

// shallow checks a container property whose entries are checked one by
// one: only the container itself must evaluate.
func (b *build) shallow(attr string, v *cval) bool {
	if v == nil {
		return false
	}
	if v.kind == cvUnres {
		return b.ok(attr, v)
	}
	return true
}

// literalOf reads a literal scalar of an evaluated value at attr, warning
// and returning false when it does not resolve or names a resource.
func (b *build) literalOf(attr string, v *cval) (string, bool) {
	if !b.ok(attr, v) {
		return "", false
	}
	t, ok := v.literal()
	if !ok {
		b.warn(attr, v, "must be a literal string; the property is left out")
		return "", false
	}
	return t, true
}

// str reads an optional literal property.
func (b *build) str(name string) (string, bool) {
	v := b.prop(name)
	if v == nil {
		return "", false
	}
	return b.literalOf(name, v)
}

// boolOf reads a bool.
func (b *build) boolOf(attr string, v *cval) (bool, bool) {
	t, ok := b.literalOf(attr, v)
	if !ok {
		return false, false
	}
	switch strings.ToLower(t) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	b.warn(attr, v, fmt.Sprintf("%q is not true or false; the property is left out", t))
	return false, false
}

// intOf reads a whole number.
func (b *build) intOf(attr string, v *cval) (int64, bool) {
	t, ok := b.literalOf(attr, v)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		b.warn(attr, v, fmt.Sprintf("%q is not a whole number; the property is left out", t))
		return 0, false
	}
	return n, true
}

// attrValue reads a scalar property as a Value (references allowed) or,
// for a mapping or list, as JSON: an SQS or SNS attribute.
func (b *build) attrValue(attr string, v *cval) (Value, bool) {
	if !b.ok(attr, v) {
		return nil, false
	}
	if val, ok := v.value(); ok {
		return val, true
	}
	if v.kind == cvMap || v.kind == cvList {
		j, err := JSON(v.tree())
		if err != nil {
			b.warn(attr, v, err.Error()+"; the property is left out")
			return nil, false
		}
		return j, true
	}
	b.warn(attr, v, "is not a value aiman can set; the property is left out")
	return nil, false
}

// tags reads Tags: a list of {Key, Value} (CloudFormation) or a mapping
// (SAM). A tag whose value names another resource or does not evaluate is
// left out with a warning.
func (b *build) tags() map[string]string {
	const name = "Tags"
	v := b.prop(name)
	if v == nil {
		return nil
	}
	out := map[string]string{}
	add := func(attr string, k, val *cval) {
		key, ok := b.literalOf(attr, k)
		if !ok {
			return
		}
		t, ok := b.literalOf(attr, val)
		if !ok {
			return
		}
		out[key] = t
	}
	switch v.kind {
	case cvList:
		for i, e := range v.list {
			attr := fmt.Sprintf("%s[%d]", name, i)
			if e.kind == cvUnres {
				b.ok(attr, e)
				continue
			}
			k, val := e.field("Key"), e.field("Value")
			if k == nil || val == nil {
				b.warn(attr, e, "a tag needs Key and Value; it is left out")
				continue
			}
			add(attr, k, val)
		}
	case cvMap:
		for i, k := range v.keys {
			add(name+"."+k, strVal(Lit(k), v.vals[i].line), v.vals[i])
		}
	default:
		b.ok(name, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// finish adds the resource unless an identity property failed, and notes
// the properties it does not emulate.
func (b *build) finish(res *Resource, extra ...string) error {
	if b.skipped {
		return nil
	}
	var dropped []string
	for _, k := range b.r.props.keys {
		if !b.used[k] && !slices.Contains(extra, k) {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		b.note("", nil, fmt.Sprintf("not emulated: %s; left out", strings.Join(dropped, ", ")))
	}
	res.Stack, res.LogicalID, res.Type, res.Source = b.s.src.Name, b.r.id, b.r.typ, SourceCloudFormation
	res.File, res.Line = b.s.src.Path, b.r.line
	res.DependsOn = b.s.dependsOn(b.r)
	return b.s.p.Add(res)
}

// dependsOn keeps DependsOn entries that name planned types; a dependency
// on anything else (a role, a policy) is met without it.
func (s *cfnStack) dependsOn(r *cfnResource) []string {
	n := r.node.get("DependsOn")
	if n == nil {
		return nil
	}
	var ids []string
	switch n.kind {
	case cfnScalar:
		ids = []string{n.text}
	case cfnSeq:
		for _, it := range n.items {
			ids = append(ids, it.text)
		}
	}
	var out []string
	for _, id := range ids {
		d, ok := s.res[id]
		if !ok || d.state != condTrue {
			continue
		}
		if _, ok := supportedTypes[d.typ]; ok {
			out = append(out, id)
		}
	}
	return out
}

// resource plans one resource of a supported type.
func (s *cfnStack) resource(r *cfnResource) error {
	b := s.newBuild(r)
	switch r.typ {
	case "AWS::DynamoDB::Table", "AWS::DynamoDB::GlobalTable":
		return s.table(b)
	case "AWS::Serverless::SimpleTable":
		return s.simpleTable(b)
	case "AWS::S3::Bucket":
		return s.bucket(b)
	case "AWS::SQS::Queue":
		return s.queue(b)
	case "AWS::SNS::Topic":
		return s.topic(b)
	case "AWS::SNS::Subscription":
		return s.subscription(b)
	}
	return nil
}

// keySchema reads a KeySchema list into hash and range keys.
func keySchema(v *cval) (hash, rng string, err error) {
	if v == nil || v.kind != cvList {
		return "", "", errors.New("is not a list of keys")
	}
	for _, k := range v.list {
		name, _ := k.field("AttributeName").literal()
		typ, _ := k.field("KeyType").literal()
		switch {
		case name == "":
			return "", "", errors.New("a key has no AttributeName")
		case typ == "HASH" && hash == "":
			hash = name
		case typ == "RANGE" && rng == "":
			rng = name
		default:
			return "", "", fmt.Errorf("key %s has KeyType %q", name, typ)
		}
	}
	if hash == "" {
		return "", "", errors.New("has no HASH key")
	}
	return hash, rng, nil
}

// projection reads an index's Projection.
func projection(v *cval) (string, []string) {
	if v == nil {
		return "", nil
	}
	typ, _ := v.field("ProjectionType").literal()
	var nk []string
	if l := v.field("NonKeyAttributes"); l != nil {
		for _, e := range l.list {
			t, _ := e.literal()
			nk = append(nk, t)
		}
	}
	return typ, nk
}

func capacity(v *cval) (rc, wc int64) {
	if v == nil {
		return 0, 0
	}
	r, _ := v.field("ReadCapacityUnits").literal()
	w, _ := v.field("WriteCapacityUnits").literal()
	rc, _ = strconv.ParseInt(r, 10, 64)
	wc, _ = strconv.ParseInt(w, 10, 64)
	return rc, wc
}

// indexes reads GlobalSecondaryIndexes or LocalSecondaryIndexes.
func (b *build) indexes(name string, local bool) ([]Index, bool) {
	v := b.prop(name)
	if v == nil {
		return nil, true
	}
	if !b.identity(name, v) {
		return nil, false
	}
	if v.kind != cvList {
		b.skip(name, v, "is not a list")
		return nil, false
	}
	var out []Index
	for i, e := range v.list {
		attr := fmt.Sprintf("%s[%d]", name, i)
		ixName, _ := e.field("IndexName").literal()
		hash, rng, err := keySchema(e.field("KeySchema"))
		if err != nil {
			b.skip(attr+".KeySchema", e, "KeySchema "+err.Error())
			return nil, false
		}
		ix := Index{Name: ixName, HashKey: hash, RangeKey: rng}
		ix.Projection, ix.NonKeyAttributes = projection(e.field("Projection"))
		if !local {
			ix.ReadCapacity, ix.WriteCapacity = capacity(e.field("ProvisionedThroughput"))
		}
		out = append(out, ix)
	}
	return out, true
}

func (s *cfnStack) table(b *build) error {
	global := b.r.typ == "AWS::DynamoDB::GlobalTable"
	name, ok := b.idString("TableName")
	if !ok {
		return nil
	}
	t := &Table{}
	ks := b.prop("KeySchema")
	if !b.identity("KeySchema", ks) {
		return nil
	}
	hash, rng, err := keySchema(ks)
	if err != nil {
		b.skip("KeySchema", ks, "KeySchema "+err.Error())
		return nil
	}
	t.HashKey, t.RangeKey = hash, rng
	ad := b.prop("AttributeDefinitions")
	if !b.identity("AttributeDefinitions", ad) {
		return nil
	}
	if ad == nil || ad.kind != cvList {
		b.skip("AttributeDefinitions", ad, "AttributeDefinitions is not a list")
		return nil
	}
	for _, a := range ad.list {
		n, _ := a.field("AttributeName").literal()
		typ, _ := a.field("AttributeType").literal()
		t.Attributes = append(t.Attributes, AttributeDef{Name: n, Type: typ})
	}
	if t.GSIs, ok = b.indexes("GlobalSecondaryIndexes", false); !ok {
		return nil
	}
	if t.LSIs, ok = b.indexes("LocalSecondaryIndexes", true); !ok {
		return nil
	}
	if bm, ok := b.str("BillingMode"); ok {
		t.BillingMode = bm
	}
	if pt := b.prop("ProvisionedThroughput"); b.ok("ProvisionedThroughput", pt) {
		if rc, ok := b.intOf("ProvisionedThroughput.ReadCapacityUnits", pt.field("ReadCapacityUnits")); ok {
			t.ReadCapacity = rc
		}
		if wc, ok := b.intOf("ProvisionedThroughput.WriteCapacityUnits", pt.field("WriteCapacityUnits")); ok {
			t.WriteCapacity = wc
		}
		if t.BillingMode == "" {
			t.BillingMode = "PROVISIONED"
		}
	}
	if ss := b.prop("StreamSpecification"); b.ok("StreamSpecification", ss) {
		if vt, ok := b.literalOf("StreamSpecification.StreamViewType", ss.field("StreamViewType")); ok {
			t.StreamViewType = vt
		}
	}
	if ttl := b.prop("TimeToLiveSpecification"); b.ok("TimeToLiveSpecification", ttl) {
		on, ok := b.boolOf("TimeToLiveSpecification.Enabled", ttl.field("Enabled"))
		if ok && on {
			if a, ok := b.literalOf("TimeToLiveSpecification.AttributeName", ttl.field("AttributeName")); ok {
				t.TTLAttribute = a
			}
		}
	}
	var extra []string
	if global {
		replica := s.localReplica(b)
		extra = append(extra, "Replicas", "WriteProvisionedThroughputSettings", "WriteOnDemandThroughputSettings")
		if replica != nil {
			b.applyReplica(t, replica)
		}
	} else {
		if tc, ok := b.str("TableClass"); ok {
			t.TableClass = tc
		}
		if dp := b.prop("DeletionProtectionEnabled"); dp != nil {
			if on, ok := b.boolOf("DeletionProtectionEnabled", dp); ok {
				t.DeletionProtection = on
			}
		}
		t.Tags = b.tags()
	}
	if sse := b.prop("SSESpecification"); sse != nil {
		b.note("SSESpecification", sse, "encryption settings are not emulated; left out")
	}
	return b.finish(&Resource{Kind: KindTable, Name: name, Table: t}, extra...)
}

// localReplica returns the GlobalTable replica in the run's region and
// warns that the others are not created.
func (s *cfnStack) localReplica(b *build) *cval {
	v := b.prop("Replicas")
	b.warn("Replicas", v, "only the replica in the run's region is created, as a plain table; replicas in other regions are not")
	if v == nil || v.kind != cvList {
		return nil
	}
	for _, r := range v.list {
		if reg, _ := r.field("Region").literal(); reg == s.p.Region {
			return r
		}
	}
	return nil
}

func (b *build) applyReplica(t *Table, r *cval) {
	if tc := r.field("TableClass"); tc != nil {
		if s, ok := b.literalOf("Replicas.TableClass", tc); ok {
			t.TableClass = s
		}
	}
	if dp := r.field("DeletionProtectionEnabled"); dp != nil {
		if on, ok := b.boolOf("Replicas.DeletionProtectionEnabled", dp); ok {
			t.DeletionProtection = on
		}
	}
	if tags := r.field("Tags"); tags != nil && tags.kind == cvList {
		t.Tags = map[string]string{}
		for i, e := range tags.list {
			attr := fmt.Sprintf("Replicas.Tags[%d]", i)
			k, ok := b.literalOf(attr, e.field("Key"))
			if !ok {
				continue
			}
			if v, ok := b.literalOf(attr, e.field("Value")); ok {
				t.Tags[k] = v
			}
		}
	}
}

func (s *cfnStack) bucket(b *build) error {
	name, ok := b.idString("BucketName")
	if !ok {
		return nil
	}
	bk := &Bucket{}
	if vc := b.prop("VersioningConfiguration"); b.ok("VersioningConfiguration", vc) {
		if st, ok := b.literalOf("VersioningConfiguration.Status", vc.field("Status")); ok {
			bk.Versioning = st
		}
	}
	if ol := b.prop("ObjectLockEnabled"); ol != nil {
		if on, ok := b.boolOf("ObjectLockEnabled", ol); ok {
			bk.ObjectLock = on
		}
	}
	for _, k := range []string{"ObjectLockConfiguration", "CorsConfiguration", "LifecycleConfiguration"} {
		if v := b.prop(k); v != nil {
			b.note(k, v, "aiman does not apply this configuration in the emulator yet; left out")
		}
	}
	if nc := b.prop("NotificationConfiguration"); b.shallow("NotificationConfiguration", nc) {
		bk.Notifications = b.notifications("NotificationConfiguration", nc)
	}
	bk.Tags = b.tags()
	return b.finish(&Resource{Kind: KindBucket, Name: name, Bucket: bk})
}

// notifications reads a bucket's NotificationConfiguration, in the
// CloudFormation shape (QueueConfigurations[].Event/Queue,
// Filter.S3Key.Rules) or the S3 API shape CDK's notification resource uses
// (Events, QueueArn, Filter.Key.FilterRules). Lambda and EventBridge
// destinations are warnings and left out.
func (b *build) notifications(attr string, nc *cval) []Notification {
	var out []Notification
	for _, k := range []string{"LambdaConfigurations", "LambdaFunctionConfigurations", "EventBridgeConfiguration"} {
		if v := nc.field(k); v != nil {
			b.warn(attr+"."+k, v, "the run has no Lambda or EventBridge; these notifications are left out")
		}
	}
	for _, d := range []struct {
		list, cfnKey, apiKey string
		kind                 Kind
	}{
		{"QueueConfigurations", "Queue", "QueueArn", KindQueue},
		{"TopicConfigurations", "Topic", "TopicArn", KindTopic},
	} {
		l := nc.field(d.list)
		if l == nil || !b.shallow(attr+"."+d.list, l) {
			continue
		}
		for i, c := range l.list {
			at := fmt.Sprintf("%s.%s[%d]", attr, d.list, i)
			if !b.ok(at, c) {
				continue
			}
			arn := c.field(d.cfnKey)
			if arn == nil {
				arn = c.field(d.apiKey)
			}
			val, ok := arn.value()
			if !ok {
				b.warn(at, c, "names no destination; the notification is left out")
				continue
			}
			n := Notification{Destination: d.kind, ARN: val}
			if id, ok := c.field("Id").literal(); ok {
				n.ID = id
			}
			if ev, ok := c.field("Event").literal(); ok {
				n.Events = []string{ev}
			}
			if evs := c.field("Events"); evs != nil {
				for _, e := range evs.list {
					if t, ok := e.literal(); ok {
						n.Events = append(n.Events, t)
					}
				}
			}
			rules := c.field("Filter").field("S3Key").field("Rules")
			if rules == nil {
				rules = c.field("Filter").field("Key").field("FilterRules")
			}
			if rules != nil {
				for _, r := range rules.list {
					name, _ := r.field("Name").literal()
					value, _ := r.field("Value").literal()
					switch strings.ToLower(name) {
					case "prefix":
						n.Prefix = value
					case "suffix":
						n.Suffix = value
					}
				}
			}
			out = append(out, n)
		}
	}
	return out
}

// SQS and SNS properties that become attributes of the same name.
var (
	queueAttrs = []string{"ContentBasedDeduplication", "DeduplicationScope", "FifoThroughputLimit", "DelaySeconds", "MaximumMessageSize",
		"MessageRetentionPeriod", "ReceiveMessageWaitTimeSeconds", "VisibilityTimeout", "RedrivePolicy", "RedriveAllowPolicy"}
	topicAttrs        = []string{"ContentBasedDeduplication", "DisplayName", "SignatureVersion", "TracingConfig"}
	subscriptionAttrs = []string{"RawMessageDelivery", "FilterPolicy", "FilterPolicyScope", "RedrivePolicy"}
)

func (b *build) attrs(names []string) map[string]Value {
	out := map[string]Value{}
	for _, k := range names {
		if v := b.prop(k); v != nil {
			if val, ok := b.attrValue(k, v); ok {
				out[k] = val
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *cfnStack) queue(b *build) error {
	name, ok := b.idString("QueueName")
	if !ok {
		return nil
	}
	fifo, ok := b.idBool("FifoQueue")
	if !ok {
		return nil
	}
	q := &Queue{Fifo: fifo, Attributes: b.attrs(queueAttrs), Tags: b.tags()}
	return b.finish(&Resource{Kind: KindQueue, Name: name, Queue: q})
}

func (s *cfnStack) topic(b *build) error {
	name, ok := b.idString("TopicName")
	if !ok {
		return nil
	}
	fifo, ok := b.idBool("FifoTopic")
	if !ok {
		return nil
	}
	t := &Topic{Fifo: fifo, Attributes: b.attrs(topicAttrs), Tags: b.tags()}
	subs := b.prop("Subscription")
	if err := b.finish(&Resource{Kind: KindTopic, Name: name, Topic: t}); err != nil || b.skipped {
		return err
	}
	if subs == nil || !b.ok("Subscription", subs) {
		return nil
	}
	for i, sv := range subs.list {
		at := fmt.Sprintf("Subscription[%d]", i)
		sub, ok := b.subscriptionOf(at, RefOf(s.src.Name, b.r.id), sv.field("Protocol"), sv.field("Endpoint"), sv)
		if !ok {
			continue
		}
		res := &Resource{Stack: s.src.Name, LogicalID: b.r.id + "/" + at, Type: "AWS::SNS::Topic.Subscription", Kind: KindSubscription,
			File: s.src.Path, Line: b.line(sv), Subscription: sub}
		if err := s.p.Add(res); err != nil {
			return err
		}
	}
	return nil
}

// subscriptionOf checks a subscription's protocol and endpoint: only sqs
// is emulated, and the endpoint must evaluate.
func (b *build) subscriptionOf(attr string, topic Value, proto, endpoint, at *cval) (*Subscription, bool) {
	if attr != "" {
		attr += "."
	}
	p, ok := proto.literal()
	if !ok {
		if u := proto.firstUnres(); u != nil {
			b.warn(attr+"Protocol", u, u.why+"; the subscription is skipped")
		} else {
			b.warn(attr+"Protocol", at, "names no literal protocol; the subscription is skipped")
		}
		return nil, false
	}
	if p != "sqs" {
		b.warn(attr+"Protocol", proto, fmt.Sprintf("protocol %q is not emulated; only sqs subscriptions are; the subscription is skipped", p))
		return nil, false
	}
	if u := endpoint.firstUnres(); u != nil {
		b.warn(attr+"Endpoint", u, u.why+"; the subscription is skipped")
		return nil, false
	}
	ep, ok := endpoint.value()
	if !ok {
		b.warn(attr+"Endpoint", at, "names no endpoint; the subscription is skipped")
		return nil, false
	}
	return &Subscription{Topic: topic, Protocol: p, Endpoint: ep}, true
}

func (s *cfnStack) subscription(b *build) error {
	tv := b.prop("TopicArn")
	if tv == nil {
		b.skip("TopicArn", nil, "the subscription names no topic")
		return nil
	}
	if u := tv.firstUnres(); u != nil {
		b.skip("TopicArn", u, u.why)
		return nil
	}
	topic, ok := tv.value()
	if !ok {
		b.skip("TopicArn", tv, "is not a topic ARN")
		return nil
	}
	proto, endpoint := b.prop("Protocol"), b.prop("Endpoint")
	sub, ok := b.subscriptionOf("", topic, proto, endpoint, &cval{line: b.r.line})
	if !ok {
		return nil
	}
	sub.Attributes = b.attrs(subscriptionAttrs)
	return b.finish(&Resource{Kind: KindSubscription, Subscription: sub}, "Region", "DeliveryPolicy", "ReplayPolicy")
}

// attach applies a policy or a CDK notification resource to the bucket,
// queues or topics it names.
func (s *cfnStack) attach(r *cfnResource) {
	b := s.newBuild(r)
	switch r.typ {
	case typeBucketPolicy:
		target, doc := b.prop("Bucket"), b.prop("PolicyDocument")
		res := s.target(b, "Bucket", target, KindBucket)
		if res == nil {
			return
		}
		if pol, ok := b.policy(doc); ok {
			res.Bucket.Policy = pol
		}
	case typeQueuePolicy, typeTopicPolicy:
		list, kind := "Queues", KindQueue
		if r.typ == typeTopicPolicy {
			list, kind = "Topics", KindTopic
		}
		targets, doc := b.prop(list), b.prop("PolicyDocument")
		if !b.ok(list, targets) {
			return
		}
		pol, ok := b.policy(doc)
		if !ok {
			return
		}
		for i, t := range targets.list {
			res := s.target(b, fmt.Sprintf("%s[%d]", list, i), t, kind)
			if res == nil {
				continue
			}
			if kind == KindQueue {
				res.Queue.Policy = pol
			} else {
				res.Topic.Policy = pol
			}
		}
	case typeCDKNotifications:
		target, nc := b.prop("BucketName"), b.prop("NotificationConfiguration")
		res := s.target(b, "BucketName", target, KindBucket)
		if res == nil || !b.shallow("NotificationConfiguration", nc) {
			return
		}
		res.Bucket.Notifications = append(res.Bucket.Notifications, b.notifications("NotificationConfiguration", nc)...)
	}
}

// policy reads a PolicyDocument: a mapping encoded as JSON, or a string.
func (b *build) policy(doc *cval) (Value, bool) {
	if doc == nil {
		b.warn("PolicyDocument", nil, "the policy has no PolicyDocument; it is left out")
		return nil, false
	}
	return b.attrValue("PolicyDocument", doc)
}

// target finds the planned resource a policy names: a Ref to a resource of
// this stack, or a bucket's literal name.
func (s *cfnStack) target(b *build, attr string, v *cval, kind Kind) *Resource {
	if v == nil {
		b.warn(attr, nil, "names nothing; the policy is left out")
		return nil
	}
	if !b.ok(attr, v) {
		return nil
	}
	val, _ := v.value()
	var res *Resource
	if refs := val.Refs(); len(val) == 1 && len(refs) == 1 {
		res = s.p.Resource(refs[0].Key())
	} else if lit, ok := val.Literal(); ok && kind == KindBucket {
		for _, r := range s.p.Resources {
			if r.Kind == KindBucket && r.Name == lit {
				res = r
			}
		}
	}
	if res == nil || res.Kind != kind {
		b.warn(attr, v, fmt.Sprintf("%s is not a planned %s of this template; the policy is left out", val, kind))
		return nil
	}
	return res
}

// function reads a Lambda or SAM function's Environment.Variables for
// function_env.
func (s *cfnStack) function(r *cfnResource) {
	fe := FunctionEnv{Stack: s.src.Name, LogicalID: r.id, File: s.src.Path}
	envNode := r.props.get("Environment").get("Variables")
	if envNode != nil {
		v := s.eval(envNode)
		switch v.kind {
		case cvUnres:
			s.p.Warn(Warning{Stack: s.src.Name, Resource: r.id, Attribute: "Environment.Variables", File: s.src.Path, Line: v.line,
				Message: v.why + "; the function's environment is left out"})
		case cvMap:
			for i, k := range v.keys {
				e := v.vals[i]
				ev := EnvVar{Name: k, Line: e.line, Sensitive: e.isSecret()}
				if u := e.firstUnres(); u != nil {
					ev.Unresolved, ev.Line = u.why, u.line
				} else if val, ok := e.value(); ok {
					ev.Value = val
				} else {
					ev.Unresolved = "the value is not a string"
				}
				fe.Vars = append(fe.Vars, ev)
			}
		}
	}
	s.p.AddFunctionEnv(fe)
	if r.typ == typeSAMFunction {
		s.samEvents(r)
	}
}

// outputs evaluates Outputs; one with a false condition is absent.
func (s *cfnStack) outputs() {
	o := s.tmpl.get("Outputs")
	if o == nil || o.kind != cfnMap {
		return
	}
	for i, name := range o.keys {
		n := o.vals[i]
		w := Warning{Stack: s.src.Name, Resource: "outputs." + name, File: s.src.Path, Line: n.line}
		if c := n.get("Condition"); c != nil {
			cname, _ := scalarText(c)
			b, u := s.condition(cname, c.line)
			if u != nil {
				w.Message, w.Line = u.why+"; the output is left out", u.line
				s.p.Warn(w)
				continue
			}
			if !b {
				continue
			}
		}
		v := s.eval(n.get("Value"))
		if u := v.firstUnres(); u != nil {
			w.Message, w.Line = u.why+"; the output is left out", u.line
			s.p.Warn(w)
			continue
		}
		val, ok := v.value()
		if !ok {
			w.Message = "the output's Value is not a string; the output is left out"
			s.p.Warn(w)
			continue
		}
		s.p.AddOutput(Output{Stack: s.src.Name, Name: name, Value: val, Sensitive: v.isSecret()})
	}
}
