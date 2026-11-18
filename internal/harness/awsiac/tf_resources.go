package awsiac

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// tfSchema is the arguments and nested blocks aiman reads of a block.
type tfSchema struct {
	attrs  []string
	blocks map[string]tfSchema
}

// tfConverter reads one resource instance into the plan.
type tfConverter func(c *tfRes)

var tfConverters = map[string]tfConverter{
	"aws_dynamodb_table":                 tfTable,
	"aws_s3_bucket":                      tfBucket,
	"aws_sqs_queue":                      tfQueue,
	"aws_sns_topic":                      tfTopic,
	"aws_sns_topic_subscription":         tfSubscription,
	"aws_s3_bucket_versioning":           tfBucketVersioning,
	"aws_s3_bucket_notification":         tfBucketNotification,
	"aws_s3_bucket_policy":               tfBucketPolicy,
	"aws_sqs_queue_policy":               tfQueuePolicy,
	"aws_sqs_queue_redrive_policy":       tfQueueRedrive("redrive_policy", "RedrivePolicy"),
	"aws_sqs_queue_redrive_allow_policy": tfQueueRedrive("redrive_allow_policy", "RedriveAllowPolicy"),
	"aws_sns_topic_policy":               tfTopicPolicy,
	"aws_lambda_function":                tfLambda,
}

// tfNotModelled explains types the spec names that the plan model has no
// place for yet.
var tfNotModelled = map[string]string{
	"aws_s3_bucket_cors_configuration":        "bucket CORS configuration is not in the plan model yet",
	"aws_s3_bucket_lifecycle_configuration":   "bucket lifecycle configuration is not in the plan model yet",
	"aws_s3_bucket_object_lock_configuration": "object lock retention is not in the plan model yet (object_lock_enabled on the bucket is)",
}

// tfMetaNames are meta-arguments and blocks of every resource.
var tfMetaNames = []string{"count", "for_each", "depends_on", "provider", "lifecycle", "provisioner", "connection", "timeouts", "tags_all"}

// tfRes is one resource instance being read.
type tfRes struct {
	ev      *tfEval
	in      *tfInst
	r       *tfResource
	key     tfKey
	id      string
	skipped bool
}

func (c *tfRes) warning(attr string, rng *hcl.Range, msg string) Warning {
	w := Warning{Stack: c.ev.src.Name, Resource: c.id, Attribute: attr, File: c.r.rng.Filename, Line: c.r.rng.Start.Line, Message: msg}
	if rng != nil {
		w.File, w.Line = rng.Filename, rng.Start.Line
	}
	return w
}

func (c *tfRes) warn(attr string, rng *hcl.Range, msg string) { c.ev.p.Warn(c.warning(attr, rng, msg)) }

// skip leaves the instance out with a warning; once is enough.
func (c *tfRes) skip(attr string, rng *hcl.Range, msg string) {
	if !c.skipped {
		c.warn(attr, rng, msg+"; the resource is skipped")
	}
	c.skipped = true
}

// drop reports a property that cannot be resolved: an identity property
// skips the resource, any other is left out.
func (c *tfRes) drop(attr string, rng *hcl.Range, identity bool, err error) {
	if identity {
		c.skip(attr, rng, err.Error())
		return
	}
	c.warn(attr, rng, err.Error()+"; the property is left out")
}

// tfBody is a block's arguments and nested blocks, with the extra
// variables (count, each, a dynamic iterator) its expressions see.
type tfBody struct {
	c      *tfRes
	at     string
	attrs  hcl.Attributes
	blocks map[string][]*tfBody
	extra  map[string]cty.Value
	rng    hcl.Range
}

// read reads a body against a schema, expanding dynamic blocks of the
// schema's block types.
func (c *tfRes) read(body hcl.Body, s tfSchema, at string, extra map[string]cty.Value, rng hcl.Range) *tfBody {
	hs := &hcl.BodySchema{}
	for _, a := range s.attrs {
		hs.Attributes = append(hs.Attributes, hcl.AttributeSchema{Name: a})
	}
	for _, name := range sortedKeys(s.blocks) {
		hs.Blocks = append(hs.Blocks, hcl.BlockHeaderSchema{Type: name})
	}
	hs.Blocks = append(hs.Blocks, hcl.BlockHeaderSchema{Type: "dynamic", LabelNames: []string{"type"}})
	content, _, diags := body.PartialContent(hs)
	if diags.HasErrors() {
		c.warn(strings.TrimSuffix(at, "."), &rng, tfDiagText(diags)+"; the block is read in part")
	}
	b := &tfBody{c: c, at: at, attrs: content.Attributes, blocks: map[string][]*tfBody{}, extra: extra, rng: rng}
	for _, blk := range content.Blocks {
		if blk.Type != "dynamic" {
			b.blocks[blk.Type] = append(b.blocks[blk.Type], c.read(blk.Body, s.blocks[blk.Type], at+blk.Type+".", extra, blk.DefRange))
			continue
		}
		typ := blk.Labels[0]
		inner, ok := s.blocks[typ]
		if !ok {
			continue
		}
		b.blocks[typ] = append(b.blocks[typ], c.dynamic(blk, typ, inner, at, extra)...)
	}
	return b
}

var tfDynamicSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "for_each", Required: true}, {Name: "iterator"}, {Name: "labels"}},
	Blocks:     []hcl.BlockHeaderSchema{{Type: "content"}},
}

// dynamic expands a dynamic block: one block per element of for_each,
// the iterator (default: the block type) holding key and value.
func (c *tfRes) dynamic(blk *hcl.Block, typ string, s tfSchema, at string, extra map[string]cty.Value) []*tfBody {
	where := at + typ
	content, diags := blk.Body.Content(tfDynamicSchema)
	if diags.HasErrors() || len(content.Blocks) != 1 {
		c.warn(where, &blk.DefRange, "a dynamic block needs for_each and one content block; it is left out")
		return nil
	}
	iter := typ
	if a, ok := content.Attributes["iterator"]; ok {
		if t, d := hcl.AbsTraversalForExpr(a.Expr); !d.HasErrors() && len(t) == 1 {
			iter = t.RootName()
		}
	}
	fe := content.Attributes["for_each"]
	v := c.in.evalValue(fe.Expr, extra)
	if !v.IsWhollyKnown() {
		c.warn(where, &fe.Range, "dynamic for_each "+tfErrUnknown(v).Error()+"; the blocks are left out")
		return nil
	}
	v, _ = v.UnmarkDeep()
	if v.IsNull() || !v.CanIterateElements() {
		c.warn(where, &fe.Range, "dynamic for_each is not a collection; the blocks are left out")
		return nil
	}
	var out []*tfBody
	for it := v.ElementIterator(); it.Next(); {
		k, e := it.Element()
		if v.Type().IsSetType() {
			k = e
		}
		x := map[string]cty.Value{}
		for n, val := range extra {
			x[n] = val
		}
		x[iter] = cty.ObjectVal(map[string]cty.Value{"key": k, "value": e})
		cb := content.Blocks[0]
		out = append(out, c.read(cb.Body, s, at+typ+".", x, cb.DefRange))
	}
	return out
}

// prop evaluates an argument; a is nil when it is absent.
func (b *tfBody) prop(name string) (cty.Value, *hcl.Attribute) {
	a, ok := b.attrs[name]
	if !ok {
		return cty.NilVal, nil
	}
	return b.c.in.evalValue(a.Expr, b.extra), a
}

// val is an argument as a plan Value, references kept; false when it is
// absent, null or cannot be resolved (warned).
func (b *tfBody) val(name string, identity bool) (Value, bool) {
	v, a := b.prop(name)
	if a == nil {
		return nil, false
	}
	val, err := b.c.ev.value(v)
	if err != nil {
		b.c.drop(b.at+name, &a.Range, identity, err)
		return nil, false
	}
	return val, val != nil
}

// lit is an argument's literal text.
func (b *tfBody) lit(name string, identity bool) (string, bool) {
	v, a := b.prop(name)
	if a == nil {
		return "", false
	}
	if !v.IsWhollyKnown() {
		b.c.drop(b.at+name, &a.Range, identity, tfErrUnknown(v))
		return "", false
	}
	if uv, _ := v.UnmarkDeep(); uv.IsNull() {
		return "", false
	}
	s, err := b.c.ev.literal(v)
	if err != nil {
		b.c.drop(b.at+name, &a.Range, identity, err)
		return "", false
	}
	return s, true
}

// typed converts an argument to ty; false when absent, null or not
// convertible (warned).
func (b *tfBody) typed(name string, ty cty.Type, identity bool) (cty.Value, bool) {
	v, a := b.prop(name)
	if a == nil {
		return cty.NilVal, false
	}
	if !v.IsWhollyKnown() {
		b.c.drop(b.at+name, &a.Range, identity, tfErrUnknown(v))
		return cty.NilVal, false
	}
	v, _ = v.UnmarkDeep()
	if v.IsNull() {
		return cty.NilVal, false
	}
	cv, err := convert.Convert(v, ty)
	if err != nil {
		b.c.drop(b.at+name, &a.Range, identity, fmt.Errorf("must be %s: %v", ty.FriendlyName(), err))
		return cty.NilVal, false
	}
	return cv, true
}

func (b *tfBody) boolean(name string, identity bool) bool {
	v, ok := b.typed(name, cty.Bool, identity)
	return ok && v.True()
}

func (b *tfBody) integer(name string) int64 {
	v, ok := b.typed(name, cty.Number, false)
	if !ok {
		return 0
	}
	i, _ := v.AsBigFloat().Int64()
	return i
}

// strs is a list of literal strings.
func (b *tfBody) strs(name string, identity bool) []string {
	v, ok := b.typed(name, cty.List(cty.String), identity)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range v.AsValueSlice() {
		s, err := b.c.ev.literal(e)
		if err != nil {
			a := b.attrs[name]
			b.c.drop(b.at+name, &a.Range, identity, err)
			return nil
		}
		out = append(out, s)
	}
	return out
}

// tags is a map of literal strings; any value that is not literal drops
// the whole map with a warning.
func (b *tfBody) tags() map[string]string {
	v, ok := b.typed("tags", cty.Map(cty.String), false)
	if !ok || v.LengthInt() == 0 {
		return nil
	}
	out := map[string]string{}
	for k, e := range v.AsValueMap() {
		s, err := b.c.ev.literal(e)
		if err != nil {
			a := b.attrs["tags"]
			b.c.drop(b.at+"tags", &a.Range, false, err)
			return nil
		}
		out[k] = s
	}
	return out
}

// noteRest notes the arguments and blocks aiman does not read, for HCL
// bodies (JSON bodies cannot list theirs).
func (c *tfRes) noteRest(s tfSchema) {
	sb, ok := c.r.body.(*hclsyntax.Body)
	if !ok {
		return
	}
	known := append(slices.Clone(s.attrs), tfMetaNames...)
	for name := range s.blocks {
		known = append(known, name)
	}
	var rest []string
	for name := range sb.Attributes {
		if !slices.Contains(known, name) {
			rest = append(rest, name)
		}
	}
	for _, blk := range sb.Blocks {
		name := blk.Type
		if name == "dynamic" && len(blk.Labels) == 1 {
			name = blk.Labels[0]
		}
		if !slices.Contains(known, name) && !slices.Contains(rest, name) {
			rest = append(rest, name)
		}
	}
	if len(rest) == 0 {
		return
	}
	sort.Strings(rest)
	c.ev.note(Warning{Resource: c.id, File: c.r.rng.Filename, Line: c.r.rng.Start.Line, Message: "not emulated, dropped: " + strings.Join(rest, ", ")})
}

// body reads the resource's own body.
func (c *tfRes) body(s tfSchema) *tfBody {
	c.noteRest(s)
	return c.read(c.r.body, s, "", c.key.extra(), c.r.rng)
}

func (c *tfRes) resource(kind Kind) *Resource {
	c.ev.origin[c.id] = c
	return &Resource{Stack: c.ev.src.Name, LogicalID: c.id, Type: c.r.typ, Source: SourceTerraform, Kind: kind,
		File: c.r.rng.Filename, Line: c.r.rng.Start.Line}
}

// named adds a resource whose physical name finish settles: the explicit
// name (which may refer to another resource's name), else a generated
// one from the prefix.
func (c *tfRes) named(r *Resource, b *tfBody, nameArg, prefixArg string) {
	name, hasName := b.val(nameArg, true)
	prefix, _ := b.lit(prefixArg, true)
	if c.skipped {
		return
	}
	p := &tfPending{r: r, c: c, prefix: prefix}
	if hasName {
		p.name = name
	}
	c.ev.pending = append(c.ev.pending, p)
}

var tfIndexSchema = tfSchema{attrs: []string{"name", "hash_key", "range_key", "projection_type", "non_key_attributes", "read_capacity", "write_capacity"}}

var tfTableSchema = tfSchema{
	attrs: []string{"name", "hash_key", "range_key", "billing_mode", "read_capacity", "write_capacity", "stream_enabled", "stream_view_type", "table_class", "deletion_protection_enabled", "tags"},
	blocks: map[string]tfSchema{
		"attribute":              {attrs: []string{"name", "type"}},
		"global_secondary_index": tfIndexSchema,
		"local_secondary_index":  tfIndexSchema,
		"ttl":                    {attrs: []string{"attribute_name", "enabled"}},
	},
}

func tfTable(c *tfRes) {
	b := c.body(tfTableSchema)
	t := &Table{}
	t.HashKey, _ = b.lit("hash_key", true)
	t.RangeKey, _ = b.lit("range_key", true)
	for _, ab := range b.blocks["attribute"] {
		n, _ := ab.lit("name", true)
		ty, _ := ab.lit("type", true)
		t.Attributes = append(t.Attributes, AttributeDef{Name: n, Type: ty})
	}
	index := func(ib *tfBody) Index {
		ix := Index{}
		ix.Name, _ = ib.lit("name", true)
		ix.HashKey, _ = ib.lit("hash_key", true)
		ix.RangeKey, _ = ib.lit("range_key", true)
		ix.Projection, _ = ib.lit("projection_type", true)
		ix.NonKeyAttributes = ib.strs("non_key_attributes", true)
		ix.ReadCapacity, ix.WriteCapacity = ib.integer("read_capacity"), ib.integer("write_capacity")
		return ix
	}
	for _, ib := range b.blocks["global_secondary_index"] {
		t.GSIs = append(t.GSIs, index(ib))
	}
	for _, ib := range b.blocks["local_secondary_index"] {
		ix := index(ib)
		ix.HashKey = ""
		t.LSIs = append(t.LSIs, ix)
	}
	if c.skipped {
		return
	}
	// Terraform's default billing mode is PROVISIONED.
	t.BillingMode = "PROVISIONED"
	if m, ok := b.lit("billing_mode", false); ok {
		t.BillingMode = m
	}
	t.ReadCapacity, t.WriteCapacity = b.integer("read_capacity"), b.integer("write_capacity")
	if b.boolean("stream_enabled", false) {
		t.StreamViewType, _ = b.lit("stream_view_type", false)
		if t.StreamViewType == "" {
			c.warn("stream_view_type", nil, "stream_enabled needs a stream_view_type; the stream is left out")
		}
	}
	for _, tb := range b.blocks["ttl"] {
		if n, ok := tb.lit("attribute_name", false); ok && n != "" && tb.boolean("enabled", false) {
			t.TTLAttribute = n
		}
	}
	t.TableClass, _ = b.lit("table_class", false)
	t.DeletionProtection = b.boolean("deletion_protection_enabled", false)
	t.Tags = b.tags()
	r := c.resource(KindTable)
	r.Table = t
	c.named(r, b, "name", "")
}

var tfBucketSchema = tfSchema{
	attrs: []string{"bucket", "bucket_prefix", "object_lock_enabled", "policy", "tags", "force_destroy"},
	blocks: map[string]tfSchema{
		"versioning":                {attrs: []string{"enabled", "mfa_delete"}},
		"object_lock_configuration": {attrs: []string{"object_lock_enabled"}, blocks: map[string]tfSchema{"rule": {}}},
	},
}

func tfBucket(c *tfRes) {
	b := c.body(tfBucketSchema)
	bk := &Bucket{}
	bk.ObjectLock = b.boolean("object_lock_enabled", false)
	for _, ol := range b.blocks["object_lock_configuration"] {
		if s, _ := ol.lit("object_lock_enabled", false); s == "Enabled" {
			bk.ObjectLock = true
		}
		if len(ol.blocks["rule"]) > 0 {
			c.ev.note(Warning{Resource: c.id, Attribute: "object_lock_configuration.rule", File: c.r.rng.Filename, Line: c.r.rng.Start.Line, Message: tfNotModelled["aws_s3_bucket_object_lock_configuration"]})
		}
	}
	for _, vb := range b.blocks["versioning"] {
		if vb.boolean("enabled", false) {
			bk.Versioning = "Enabled"
		}
	}
	bk.Policy, _ = b.val("policy", false)
	bk.Tags = b.tags()
	r := c.resource(KindBucket)
	r.Bucket = bk
	c.named(r, b, "bucket", "bucket_prefix")
}

// tfQueueAttrs maps aws_sqs_queue arguments to SQS attribute names.
var tfQueueAttrs = map[string]string{
	"deduplication_scope":        "DeduplicationScope",
	"fifo_throughput_limit":      "FifoThroughputLimit",
	"delay_seconds":              "DelaySeconds",
	"max_message_size":           "MaximumMessageSize",
	"message_retention_seconds":  "MessageRetentionPeriod",
	"receive_wait_time_seconds":  "ReceiveMessageWaitTimeSeconds",
	"visibility_timeout_seconds": "VisibilityTimeout",
	"redrive_policy":             "RedrivePolicy",
	"redrive_allow_policy":       "RedriveAllowPolicy",
}

func tfQueue(c *tfRes) {
	s := tfSchema{attrs: []string{"name", "name_prefix", "fifo_queue", "content_based_deduplication", "policy", "tags"}}
	s.attrs = append(s.attrs, sortedKeys(tfQueueAttrs)...)
	b := c.body(s)
	q := &Queue{Attributes: map[string]Value{}}
	q.Fifo = b.boolean("fifo_queue", true)
	for _, arg := range sortedKeys(tfQueueAttrs) {
		if v, ok := b.val(arg, false); ok {
			q.Attributes[tfQueueAttrs[arg]] = v
		}
	}
	if b.boolean("content_based_deduplication", false) {
		q.Attributes["ContentBasedDeduplication"] = Lit("true")
	}
	q.Policy, _ = b.val("policy", false)
	q.Tags = b.tags()
	r := c.resource(KindQueue)
	r.Queue = q
	c.named(r, b, "name", "name_prefix")
}

func tfTopic(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"name", "name_prefix", "fifo_topic", "content_based_deduplication", "display_name", "policy", "tags"}})
	t := &Topic{Attributes: map[string]Value{}}
	t.Fifo = b.boolean("fifo_topic", true)
	if b.boolean("content_based_deduplication", false) {
		t.Attributes["ContentBasedDeduplication"] = Lit("true")
	}
	if v, ok := b.val("display_name", false); ok {
		t.Attributes["DisplayName"] = v
	}
	t.Policy, _ = b.val("policy", false)
	t.Tags = b.tags()
	r := c.resource(KindTopic)
	r.Topic = t
	c.named(r, b, "name", "name_prefix")
}

func tfSubscription(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"topic_arn", "protocol", "endpoint", "raw_message_delivery", "filter_policy", "filter_policy_scope", "redrive_policy"}})
	s := &Subscription{Attributes: map[string]Value{}}
	s.Topic, _ = b.val("topic_arn", true)
	s.Protocol, _ = b.lit("protocol", true)
	s.Endpoint, _ = b.val("endpoint", true)
	if c.skipped {
		return
	}
	if b.boolean("raw_message_delivery", false) {
		s.Attributes["RawMessageDelivery"] = Lit("true")
	}
	for arg, attr := range map[string]string{"filter_policy": "FilterPolicy", "filter_policy_scope": "FilterPolicyScope", "redrive_policy": "RedrivePolicy"} {
		if v, ok := b.val(arg, false); ok {
			s.Attributes[attr] = v
		}
	}
	r := c.resource(KindSubscription)
	r.Subscription = s
	c.ev.added = append(c.ev.added, r)
}

// attach records a configuration resource that changes another planned
// resource (a bucket's versioning, a queue's policy); finish applies it
// once names are known.
func (c *tfRes) attach(kind Kind, b *tfBody, targetArg string, apply func(*Resource)) {
	target, ok := b.val(targetArg, true)
	if !ok || c.skipped {
		if !ok && !c.skipped {
			c.skip(targetArg, nil, "names no "+string(kind))
		}
		return
	}
	c.ev.attachments = append(c.ev.attachments, &tfAttach{c: c, kind: kind, arg: targetArg, target: target, apply: apply})
}

func tfBucketVersioning(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"bucket", "expected_bucket_owner", "mfa"}, blocks: map[string]tfSchema{"versioning_configuration": {attrs: []string{"status", "mfa_delete"}}}})
	status := ""
	for _, vb := range b.blocks["versioning_configuration"] {
		status, _ = vb.lit("status", false)
	}
	if status == "Disabled" {
		status = ""
	}
	if status != "" && status != "Enabled" && status != "Suspended" {
		c.skip("versioning_configuration.status", nil, fmt.Sprintf("status %q is not Enabled, Suspended or Disabled", status))
		return
	}
	c.attach(KindBucket, b, "bucket", func(r *Resource) { r.Bucket.Versioning = status })
}

var tfNotifySchema = tfSchema{attrs: []string{"id", "queue_arn", "topic_arn", "lambda_function_arn", "events", "filter_prefix", "filter_suffix"}}

func tfBucketNotification(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"bucket", "eventbridge"}, blocks: map[string]tfSchema{"queue": tfNotifySchema, "topic": tfNotifySchema, "lambda_function": tfNotifySchema}})
	var ns []Notification
	read := func(kind Kind, block, arnArg string) {
		for _, nb := range b.blocks[block] {
			n := Notification{Destination: kind}
			var ok bool
			if n.ARN, ok = nb.val(arnArg, false); !ok {
				continue
			}
			n.ID, _ = nb.lit("id", false)
			n.Events = nb.strs("events", false)
			n.Prefix, _ = nb.lit("filter_prefix", false)
			n.Suffix, _ = nb.lit("filter_suffix", false)
			ns = append(ns, n)
		}
	}
	read(KindQueue, "queue", "queue_arn")
	read(KindTopic, "topic", "topic_arn")
	if len(b.blocks["lambda_function"]) > 0 {
		c.warn("lambda_function", nil, "no Lambda runs in the harness; the lambda_function notifications are left out")
	}
	if b.boolean("eventbridge", false) {
		c.warn("eventbridge", nil, "no EventBridge runs in the harness; the eventbridge notification is left out")
	}
	c.attach(KindBucket, b, "bucket", func(r *Resource) { r.Bucket.Notifications = append(r.Bucket.Notifications, ns...) })
}

func tfBucketPolicy(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"bucket", "policy"}})
	pol, ok := b.val("policy", true)
	if !ok {
		c.skip("policy", nil, "has no policy")
		return
	}
	c.attach(KindBucket, b, "bucket", func(r *Resource) { r.Bucket.Policy = pol })
}

func tfQueuePolicy(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"queue_url", "policy"}})
	pol, ok := b.val("policy", true)
	if !ok {
		c.skip("policy", nil, "has no policy")
		return
	}
	c.attach(KindQueue, b, "queue_url", func(r *Resource) { r.Queue.Policy = pol })
}

func tfQueueRedrive(arg, attr string) tfConverter {
	return func(c *tfRes) {
		b := c.body(tfSchema{attrs: []string{"queue_url", arg}})
		v, ok := b.val(arg, true)
		if !ok {
			c.skip(arg, nil, "has no "+arg)
			return
		}
		c.attach(KindQueue, b, "queue_url", func(r *Resource) { r.Queue.Attributes[attr] = v })
	}
}

func tfTopicPolicy(c *tfRes) {
	b := c.body(tfSchema{attrs: []string{"arn", "policy"}})
	pol, ok := b.val("policy", true)
	if !ok {
		c.skip("policy", nil, "has no policy")
		return
	}
	c.attach(KindTopic, b, "arn", func(r *Resource) { r.Topic.Policy = pol })
}

// tfLambda reads a function's environment variables for function_env; the
// function itself is never created.
func tfLambda(c *tfRes) {
	c.ev.note(Warning{Resource: c.r.typ, Message: "not emulated: aws_lambda_function (its environment is read for function_env)"})
	b := c.read(c.r.body, tfSchema{blocks: map[string]tfSchema{"environment": {attrs: []string{"variables"}}}}, "", c.key.extra(), c.r.rng)
	fe := FunctionEnv{Stack: c.ev.src.Name, LogicalID: c.id, Vars: []EnvVar{}}
	for _, eb := range b.blocks["environment"] {
		v, a := eb.prop("variables")
		if a == nil {
			continue
		}
		if !v.IsKnown() {
			c.warn("environment.variables", &a.Range, tfErrUnknown(v).Error()+"; the function's environment is left out")
			continue
		}
		sensitive := tfIsSensitive(v)
		v, _ = v.Unmark()
		if v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
			c.warn("environment.variables", &a.Range, "is not a map; the function's environment is left out")
			continue
		}
		m := v.AsValueMap()
		for _, k := range sortedKeys(m) {
			val, err := c.ev.value(m[k])
			if err != nil {
				c.warn("environment.variables."+k, &a.Range, err.Error()+"; the variable is left out")
				continue
			}
			if val == nil {
				continue
			}
			fe.Vars = append(fe.Vars, EnvVar{Name: k, Value: val, Sensitive: sensitive || tfIsSensitive(m[k])})
		}
	}
	c.ev.functions = append(c.ev.functions, fe)
}

// tfPending is a named resource waiting for its physical name.
type tfPending struct {
	r      *Resource
	c      *tfRes
	name   Value
	prefix string
	// physical is the settled name; done once it is.
	physical string
	done     bool
}

// tfAttach is a configuration resource waiting for its target.
type tfAttach struct {
	c      *tfRes
	kind   Kind
	arg    string
	target Value
	apply  func(*Resource)
}

// run reads the whole stack into the plan.
func (ev *tfEval) run(mod *tfModule, vars map[string]cty.Value) {
	root := ev.newInst(mod, "", 0, vars)
	ev.walk(root)
	for _, in := range ev.insts {
		if ev.ctx.Err() != nil {
			ev.fail(ev.ctx.Err())
			return
		}
		for _, d := range in.mod.data {
			ev.note(Warning{Resource: "data." + d.typ, Message: "data sources are not read; references to them are unresolvable"})
		}
		for _, r := range in.mod.resources {
			ev.resource(in, r)
		}
	}
	ev.outputs(root)
	ev.finish()
}

func (ev *tfEval) resource(in *tfInst, r *tfResource) {
	conv := tfConverters[r.typ]
	if conv == nil {
		msg := "not emulated: " + r.typ
		if why, ok := tfNotModelled[r.typ]; ok {
			msg += " (" + why + ")"
		}
		ev.note(Warning{Resource: r.typ, Message: msg})
		return
	}
	e := in.expand(r.addr(), r.count, r.forEach)
	if e.why != "" {
		ev.warn(Warning{Resource: in.prefix + r.addr(), File: r.rng.Filename, Line: r.rng.Start.Line, Message: e.why + "; the resource is skipped"})
		return
	}
	for _, k := range e.keys {
		if ev.converted++; ev.converted > tfMaxResources {
			ev.fail(fmt.Errorf("terraform: the stack expands to more than %d resources", tfMaxResources))
			return
		}
		conv(&tfRes{ev: ev, in: in, r: r, key: k, id: in.prefix + r.addr() + k.suffix()})
	}
}

// outputs adds the root module's outputs; one that cannot be resolved is
// a warning and left out.
func (ev *tfEval) outputs(root *tfInst) {
	for _, o := range root.mod.outputs {
		v := root.output(o)
		val, err := ev.value(v)
		if err != nil {
			ev.warn(Warning{Resource: "outputs." + o.name, File: o.rng.Filename, Line: o.rng.Start.Line, Message: err.Error() + "; the output is left out"})
			continue
		}
		if val == nil {
			continue
		}
		ev.p.AddOutput(Output{Stack: ev.src.Name, Name: o.name, Value: val, Sensitive: o.sensitive || tfIsSensitive(v)})
	}
}

// finish settles names, applies configuration resources, then adds every
// resource and function environment to the plan.
func (ev *tfEval) finish() {
	byKey := map[string]*tfPending{}
	for _, p := range ev.pending {
		byKey[p.r.LogicalID] = p
	}
	for progress := true; progress; {
		progress = false
		for _, p := range ev.pending {
			if !p.done && ev.settle(p, byKey) {
				p.done, progress = true, true
			}
		}
	}
	var added []*Resource
	for _, p := range ev.pending {
		if !p.done {
			ref := p.name.Refs()[0]
			p.c.skip("name", nil, fmt.Sprintf("the name refers to %s, which has no name aiman can work out", ref))
			continue
		}
		if p.name != nil {
			p.r.Name = p.physical
		} else {
			p.r.NamePrefix = p.prefix
		}
		added = append(added, p.r)
	}
	added = append(added, ev.added...)
	for _, a := range ev.attachments {
		ev.applyAttach(a, byKey)
	}
	ids := map[string]bool{}
	for _, r := range added {
		ids[r.LogicalID] = true
	}
	for _, r := range added {
		r.DependsOn = ev.dependsOn(r, ids)
		if err := ev.p.Add(r); err != nil {
			ev.fail(err)
		}
	}
	for _, f := range ev.functions {
		ev.p.AddFunctionEnv(f)
	}
}

// settle works out p's physical name when every reference in it names a
// resource whose name is settled.
func (ev *tfEval) settle(p *tfPending, byKey map[string]*tfPending) bool {
	if p.name == nil {
		p.physical = GeneratedName(p.r.Kind, SourceTerraform, p.r.Stack, p.r.LogicalID, p.prefix, p.r.isFifo())
		return true
	}
	var b strings.Builder
	for _, part := range p.name {
		if part.Ref == nil {
			b.WriteString(part.Lit)
			continue
		}
		s, ok := ev.attrOf(*part.Ref, byKey)
		if !ok {
			return false
		}
		b.WriteString(s)
	}
	p.physical = b.String()
	return true
}

// attrOf is a settled resource's attribute, worked out as Build will.
func (ev *tfEval) attrOf(a AttrRef, byKey map[string]*tfPending) (string, bool) {
	t, ok := byKey[a.LogicalID]
	if !ok || !t.done || a.Stack != ev.src.Name {
		return "", false
	}
	tmp := &Resource{Kind: t.r.Kind, Source: SourceTerraform, Physical: t.physical}
	c, ok := canonicalAttr(tmp, a.Attr)
	if !ok {
		return "", false
	}
	v, ok := baseAttrs(ev.p, tmp)[c]
	return v, ok && v != ""
}

// applyAttach finds a configuration resource's target, by reference or by
// physical name, and applies it.
func (ev *tfEval) applyAttach(a *tfAttach, byKey map[string]*tfPending) {
	var target *tfPending
	if refs := a.target.Refs(); len(refs) == 1 && len(a.target) == 1 {
		target = byKey[refs[0].LogicalID]
	} else if s, ok := a.target.Literal(); ok {
		for _, p := range ev.pending {
			if p.done && p.r.Kind == a.kind && tfNames(ev.p, p, s) {
				target = p
				break
			}
		}
	}
	if target == nil || !target.done || target.r.Kind != a.kind || target.c.skipped {
		a.c.warn(a.arg, nil, fmt.Sprintf("%s is not a planned %s; the %s is left out", a.target, a.kind, a.c.r.typ))
		return
	}
	a.apply(target.r)
}

// tfNames reports whether s names p: its physical name, URL or ARN.
func tfNames(plan *Plan, p *tfPending, s string) bool {
	if s == p.physical {
		return true
	}
	for _, v := range baseAttrs(plan, &Resource{Kind: p.r.Kind, Physical: p.physical}) {
		if v == s {
			return true
		}
	}
	return false
}

// dependsOn turns depends_on references into the planned instances they
// name in the same module instance.
func (ev *tfEval) dependsOn(r *Resource, ids map[string]bool) []string {
	c, ok := ev.origin[r.LogicalID]
	if !ok {
		return nil
	}
	var out []string
	for _, t := range c.r.dependsOn {
		if len(t) < 2 {
			continue
		}
		base := c.in.prefix + t.RootName() + "." + tfAttrName(t, 1)
		for id := range ids {
			if id != r.LogicalID && (id == base || strings.HasPrefix(id, base+"[") || strings.HasPrefix(id, base+".")) {
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}
