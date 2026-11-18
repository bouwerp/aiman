// Package awsiac turns a harness aws service's infrastructure as code into a
// plan: the resources to create in the emulator, their physical names and
// attributes, the calls that create them in dependency order, the seed data,
// the app's environment and the resource manifest (docs/specs/harness-aws.md).
//
// Importers (CloudFormation and SAM, Terraform) fill a Plan through its
// builder methods; Build then names, checks and orders it. The package has
// no Docker and no network: the applier (Apply) runs the plan's steps
// against the emulator.
package awsiac

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is the emulated resource type.
type Kind string

// Resource kinds a plan creates.
const (
	KindTable        Kind = "table"
	KindBucket       Kind = "bucket"
	KindQueue        Kind = "queue"
	KindTopic        Kind = "topic"
	KindSubscription Kind = "subscription"
)

// Source is the IaC a resource came from. It decides the attribute names
// references use: CloudFormation's (Arn, StreamArn, QueueUrl) or
// Terraform's (arn, stream_arn, url).
type Source string

// Sources.
const (
	SourceCloudFormation Source = "cloudformation"
	SourceTerraform      Source = "terraform"
)

// AccountID is the account the emulator gives the run's fixed test key.
const AccountID = "000000000000"

// Resource is one planned emulator resource. Exactly the field of its Kind
// is set. Name is the explicit physical name the IaC gives; empty means a
// generated name (NamePrefix, a Terraform name_prefix or bucket_prefix,
// shapes it). Build sets Physical, Generated and Attrs.
type Resource struct {
	Stack     string `json:"stack"`
	LogicalID string `json:"logical_id"`
	// Type is the IaC type (AWS::DynamoDB::Table, aws_sqs_queue).
	Type   string `json:"type"`
	Source Source `json:"source"`
	Kind   Kind   `json:"kind"`
	Name   string `json:"name,omitempty"`
	// NamePrefix is a Terraform name_prefix or bucket_prefix.
	NamePrefix string `json:"name_prefix,omitempty"`
	// DependsOn names logical ids in the same stack (DependsOn,
	// depends_on) that must be created first.
	DependsOn []string `json:"depends_on,omitempty"`
	// File and Line point at the definition, for warnings.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`

	Table        *Table        `json:"table,omitempty"`
	Bucket       *Bucket       `json:"bucket,omitempty"`
	Queue        *Queue        `json:"queue,omitempty"`
	Topic        *Topic        `json:"topic,omitempty"`
	Subscription *Subscription `json:"subscription,omitempty"`

	// Physical is the name in the emulator; Generated marks a name aiman
	// made (GeneratedName). Attrs are the resource's attributes by
	// canonical name (Attr*); a learned one (a stream ARN) appears after
	// the applier ran.
	Physical  string            `json:"physical,omitempty"`
	Generated bool              `json:"generated,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Key is the resource's plan key, <stack>.<logical id>.
func (r *Resource) Key() string { return Key(r.Stack, r.LogicalID) }

// Key joins a stack and a logical id.
func Key(stack, logicalID string) string { return stack + "." + logicalID }

// Table is a DynamoDB table. Attributes are the AttributeDefinitions; every
// key of the table and its indexes must be among them. BillingMode is
// PAY_PER_REQUEST (the default) or PROVISIONED. StreamViewType turns the
// stream on; TTLAttribute turns TTL on.
type Table struct {
	HashKey            string            `json:"hash_key"`
	RangeKey           string            `json:"range_key,omitempty"`
	Attributes         []AttributeDef    `json:"attributes"`
	BillingMode        string            `json:"billing_mode,omitempty"`
	ReadCapacity       int64             `json:"read_capacity,omitempty"`
	WriteCapacity      int64             `json:"write_capacity,omitempty"`
	GSIs               []Index           `json:"gsis,omitempty"`
	LSIs               []Index           `json:"lsis,omitempty"`
	StreamViewType     string            `json:"stream_view_type,omitempty"`
	TTLAttribute       string            `json:"ttl_attribute,omitempty"`
	TableClass         string            `json:"table_class,omitempty"`
	DeletionProtection bool              `json:"deletion_protection,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
}

// AttributeDef is a key attribute's name and type (S, N or B).
type AttributeDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Index is a global or local secondary index. Projection is ALL (the
// default), KEYS_ONLY or INCLUDE with NonKeyAttributes. An LSI's HashKey is
// the table's.
type Index struct {
	Name             string   `json:"name"`
	HashKey          string   `json:"hash_key"`
	RangeKey         string   `json:"range_key,omitempty"`
	Projection       string   `json:"projection,omitempty"`
	NonKeyAttributes []string `json:"non_key_attributes,omitempty"`
	ReadCapacity     int64    `json:"read_capacity,omitempty"`
	WriteCapacity    int64    `json:"write_capacity,omitempty"`
}

// Bucket is an S3 bucket. Versioning is Enabled, Suspended or empty.
type Bucket struct {
	Versioning    string            `json:"versioning,omitempty"`
	ObjectLock    bool              `json:"object_lock,omitempty"`
	Notifications []Notification    `json:"notifications,omitempty"`
	Policy        Value             `json:"policy,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// Notification sends a bucket's events to a queue or a topic, by ARN.
// Destination is KindQueue or KindTopic.
type Notification struct {
	ID          string   `json:"id,omitempty"`
	Destination Kind     `json:"destination"`
	ARN         Value    `json:"arn"`
	Events      []string `json:"events"`
	Prefix      string   `json:"prefix,omitempty"`
	Suffix      string   `json:"suffix,omitempty"`
}

// Queue is an SQS queue. Attributes are SQS attribute names
// (VisibilityTimeout, RedrivePolicy, …) other than FifoQueue and Policy;
// Policy is applied after every queue and topic exists.
type Queue struct {
	Fifo       bool              `json:"fifo,omitempty"`
	Attributes map[string]Value  `json:"attributes,omitempty"`
	Policy     Value             `json:"policy,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// Topic is an SNS topic. Attributes are SNS attribute names other than
// FifoTopic and Policy.
type Topic struct {
	Fifo       bool              `json:"fifo,omitempty"`
	Attributes map[string]Value  `json:"attributes,omitempty"`
	Policy     Value             `json:"policy,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// Subscription subscribes a queue (Protocol sqs, Endpoint its ARN) to a
// topic. Attributes are SNS subscription attributes (RawMessageDelivery,
// FilterPolicy, FilterPolicyScope, RedrivePolicy).
type Subscription struct {
	Topic      Value            `json:"topic"`
	Protocol   string           `json:"protocol"`
	Endpoint   Value            `json:"endpoint"`
	Attributes map[string]Value `json:"attributes,omitempty"`
}

// Output is a CloudFormation Outputs entry or a Terraform output block,
// evaluated. Sensitive (NoEcho sources, sensitive outputs) is never shown.
type Output struct {
	Stack     string `json:"stack"`
	Name      string `json:"name"`
	Value     Value  `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// FunctionEnv is a Lambda function's Environment.Variables, read for
// function_env; the function itself is never created.
type FunctionEnv struct {
	Stack     string   `json:"stack"`
	LogicalID string   `json:"logical_id"`
	Vars      []EnvVar `json:"vars"`
}

// EnvVar is one variable of a function's environment.
type EnvVar struct {
	Name      string `json:"name"`
	Value     Value  `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// Warning is something the plan could not do as the IaC says: a value it
// could not resolve, a property it dropped, a resource it skipped. A note
// has the same shape and is information (a type that is not emulated).
type Warning struct {
	Stack     string `json:"stack,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Message   string `json:"message"`
}

func (w Warning) String() string {
	var at []string
	if w.Stack != "" {
		id := w.Stack
		if w.Resource != "" {
			id += "." + w.Resource
		}
		at = append(at, id)
	}
	if w.Attribute != "" {
		at = append(at, w.Attribute)
	}
	s := w.Message
	if len(at) > 0 {
		s = strings.Join(at, " ") + ": " + s
	}
	if w.File != "" {
		if w.Line > 0 {
			s += fmt.Sprintf(" (%s:%d)", w.File, w.Line)
		} else {
			s += " (" + w.File + ")"
		}
	}
	return s
}

// Step is one applier call: a resource's create or configure call, or a
// seed entry. Service is the AWS service (s3, dynamodb, sqs, sns).
type Step struct {
	N         int       `json:"n"`
	Resource  string    `json:"resource,omitempty"`
	Service   string    `json:"service"`
	Operation string    `json:"operation"`
	Seed      *SeedStep `json:"seed,omitempty"`
}

// SeedStep points a step at a seed entry: Kind is objects, items or
// messages, Index the entry.
type SeedStep struct {
	Kind  string `json:"kind"`
	Index int    `json:"index"`
}

// Plan is one aws service's plan. New starts it; importers add resources,
// outputs, function environments, warnings and notes; Build names, checks
// and orders it.
type Plan struct {
	Service  string `json:"service"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint"`
	// Services are the AWS services the emulator loads (s3, dynamodb, sqs,
	// sns); a resource of a service not loaded is skipped with a warning.
	Services  []string      `json:"services"`
	Resources []*Resource   `json:"resources"`
	Outputs   []Output      `json:"outputs,omitempty"`
	Functions []FunctionEnv `json:"functions,omitempty"`
	Warnings  []Warning     `json:"warnings,omitempty"`
	Notes     []Warning     `json:"notes,omitempty"`
	Seed      *Seed         `json:"seed,omitempty"`
	Steps     []Step        `json:"steps,omitempty"`
	// Env is the app's environment from aws.env and function_env
	// (BuildEnv); SensitiveEnv names the variables whose source is NoEcho
	// or sensitive.
	Env          map[string]Value `json:"env,omitempty"`
	SensitiveEnv []string         `json:"sensitive_env,omitempty"`

	built bool
}

// New starts the plan of the aws service named service: the emulator in
// region, reached by the app at endpoint (http://<service>:4566), loading
// services.
func New(service, region, endpoint string, services []string) *Plan {
	return &Plan{Service: service, Region: region, Endpoint: endpoint, Services: services, Resources: []*Resource{}}
}

// Add plans a resource. A second resource with the same key is refused.
func (p *Plan) Add(r *Resource) error {
	if r == nil || r.Stack == "" || r.LogicalID == "" {
		return fmt.Errorf("awsiac: a resource needs a stack and a logical id")
	}
	if p.Resource(r.Key()) != nil {
		return fmt.Errorf("awsiac: %s is planned twice", r.Key())
	}
	set := 0
	for _, ok := range []bool{r.Table != nil, r.Bucket != nil, r.Queue != nil, r.Topic != nil, r.Subscription != nil} {
		if ok {
			set++
		}
	}
	if set != 1 || r.kindBody() == nil {
		return fmt.Errorf("awsiac: %s must set exactly the body of kind %q", r.Key(), r.Kind)
	}
	if r.Source == "" {
		r.Source = SourceCloudFormation
	}
	p.Resources = append(p.Resources, r)
	p.built = false
	return nil
}

func (r *Resource) kindBody() any {
	switch r.Kind {
	case KindTable:
		if r.Table != nil {
			return r.Table
		}
	case KindBucket:
		if r.Bucket != nil {
			return r.Bucket
		}
	case KindQueue:
		if r.Queue != nil {
			return r.Queue
		}
	case KindTopic:
		if r.Topic != nil {
			return r.Topic
		}
	case KindSubscription:
		if r.Subscription != nil {
			return r.Subscription
		}
	}
	return nil
}

// AddOutput adds an evaluated output.
func (p *Plan) AddOutput(o Output) { p.Outputs = append(p.Outputs, o) }

// AddFunctionEnv adds a function's environment for function_env.
func (p *Plan) AddFunctionEnv(f FunctionEnv) { p.Functions = append(p.Functions, f) }

// Warn records a warning: a value the plan could not resolve, a property
// dropped or a resource skipped. A warning never fails a run.
func (p *Plan) Warn(w Warning) { p.Warnings = append(p.Warnings, w) }

// Note records information, such as a type that is not emulated.
func (p *Plan) Note(w Warning) { p.Notes = append(p.Notes, w) }

// Resource returns the planned resource with key <stack>.<logical id>, or
// nil.
func (p *Plan) Resource(key string) *Resource {
	for _, r := range p.Resources {
		if r.Key() == key {
			return r
		}
	}
	return nil
}

// Output returns a stack's output.
func (p *Plan) Output(stack, name string) (*Output, bool) {
	for i := range p.Outputs {
		if p.Outputs[i].Stack == stack && p.Outputs[i].Name == name {
			return &p.Outputs[i], true
		}
	}
	return nil, false
}

// Function returns a function's environment.
func (p *Plan) Function(stack, logicalID string) (*FunctionEnv, bool) {
	for i := range p.Functions {
		if p.Functions[i].Stack == stack && p.Functions[i].LogicalID == logicalID {
			return &p.Functions[i], true
		}
	}
	return nil, false
}

// kindService is the AWS service that holds a kind.
func kindService(k Kind) string {
	switch k {
	case KindTable:
		return "dynamodb"
	case KindBucket:
		return "s3"
	case KindQueue:
		return "sqs"
	default:
		return "sns"
	}
}

// Build names every resource, checks references, skipping or dropping what
// cannot resolve with a named warning, binds the seed and orders the steps.
// It runs once, after the importers and SetSeed; BuildEnv follows it.
func (p *Plan) Build() error {
	p.Steps = nil
	if err := p.name(); err != nil {
		return err
	}
	for _, r := range p.Resources {
		r.Attrs = baseAttrs(p, r)
	}
	p.check()
	if err := p.checkSeed(); err != nil {
		return err
	}
	order, err := p.order()
	if err != nil {
		return err
	}
	p.Steps = steps(order, p.Seed)
	p.built = true
	return nil
}

// Built reports whether Build ran since the last change.
func (p *Plan) Built() bool { return p.built }

// skip removes a resource with a warning.
func (p *Plan) skip(r *Resource, attr, msg string) {
	p.Warn(Warning{Stack: r.Stack, Resource: r.LogicalID, Attribute: attr, File: r.File, Line: r.Line, Message: msg + "; the resource is skipped"})
	p.Resources = removeResource(p.Resources, r)
}

func removeResource(rs []*Resource, r *Resource) []*Resource {
	out := rs[:0]
	for _, x := range rs {
		if x != r {
			out = append(out, x)
		}
	}
	return out
}

// sortedKeys returns a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
