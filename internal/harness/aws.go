package harness

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/cigate"
)

// AWS services (P10, docs/specs/harness-aws.md): a service with an aws block
// runs an AWS emulator on the run's offline inner network. The app reaches it
// at http://<service>:4566 with the fixed key test/test; it never holds or
// reaches real AWS credentials or hosts.

// AWS kinds. A kind is an image pin, a compose function and an env map.
const AWSKindMiniStack = "ministack"

// MiniStackImage is MiniStack 1.5.41 pinned by OCI index digest (amd64 and
// arm64). It is vetted as builtin; harness.aws.images.ministack replaces it
// and is vetted as a listed image.
const MiniStackImage = "ministackorg/ministack:1.5.41@sha256:aef90fec1a4581c5fa7b4f79d347898f8475111bac95bbab3736f4a44f5f8573"

// AWS emulator constants.
const (
	// AWSPort is the emulator's gateway port and the edge tap's port.
	AWSPort = 4566
	// DefaultAWSRegion is the region of an aws block that names none.
	DefaultAWSRegion = "us-east-1"
	// AWSAccessKeyID and AWSSecretAccessKey are the only credentials a run
	// holds. MiniStack derives the account from the key id, so the app, the
	// applier and the relay must all use this one.
	AWSAccessKeyID     = "test"
	AWSSecretAccessKey = "test" //nolint:gosec // G101: the emulator's fixed public test key, not a credential
	// AWSAccountID is the account MiniStack gives the test key.
	AWSAccountID = "000000000000"
	// AWSMount is where every container of a run with an aws service reads
	// the AWS config, credentials and (from A4) the resource manifest.
	AWSMount = "/aiman-aws"
	// AWSConfigFile and AWSCredentialsFile are Project.Files paths, beside
	// the compose file, bound read-only at AWSMount.
	AWSConfigFile      = awsFilesDir + "/config"
	AWSCredentialsFile = awsFilesDir + "/credentials"
	awsFilesDir        = "aws"
	// DefaultAWSMemory is an aws service's memory limit unless it sets one.
	DefaultAWSMemory int64 = 512 << 20
	// awsEmuSuffix and awsInitSuffix name the emulator's compose services.
	awsEmuSuffix  = "-emu"
	awsInitSuffix = "-emu-init"
	// awsDataDir is the emulator's data volume; S3 bytes live under it.
	awsDataDir = "/data"
)

// AWSServiceNames are the AWS services an aws block may load.
var AWSServiceNames = []string{"s3", "dynamodb", "sqs", "sns"}

// AWSBlockSuffixes are the real AWS host suffixes the capture proxy refuses
// whenever a scenario runs an aws service, online and offline.
var AWSBlockSuffixes = []string{"amazonaws.com", "amazonaws.com.cn", "api.aws", "on.aws"}

// awsUnsupported says why a known AWS service cannot run in a harness.
var awsUnsupported = map[string]string{
	"lambda": "needs the Docker socket", "ecs": "needs the Docker socket", "eks": "needs the Docker socket",
	"rds": "needs the Docker socket", "ec2": "needs the Docker socket", "elasticache": "needs the Docker socket",
}

// awsEmuEnvRefused are emulator settings aiman sets or that open another way
// in; a service's env may not set them. A trailing * matches a prefix.
var awsEmuEnvRefused = []string{
	"SERVICES", "AUTH", "GATEWAY_PORT", "EDGE_PORT", "BIND_HOST", "PERSIST_STATE", "STATE_DIR",
	"S3_PERSIST", "S3_DATA_DIR", "LAMBDA_*", "DOCKER_HOST", "MINISTACK_*", "CLOUDTRAIL_*", "SFTP_ENABLED", "USE_SSL",
}

// AWSIdentityVars are variables no harness file may set anywhere, because
// they select or carry real AWS credentials. A trailing * matches a prefix.
var AWSIdentityVars = []string{
	"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_SECURITY_TOKEN", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN*",
	"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "AWS_EC2_METADATA_*",
}

// awsKeyIDRe matches an AWS access key id anywhere in a value.
var awsKeyIDRe = regexp.MustCompile(`\b(AKIA|ASIA|AROA|AIDA|ANPA|ANVA|AGPA)[A-Z0-9]{16}\b`)

var (
	awsRegionRe    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	awsStackNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,24}$`)
	cfnLogicalIDRe = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	cfnAttrRe      = regexp.MustCompile(`^[A-Za-z0-9]+(\.[A-Za-z0-9]+)*$`)
	cfnOutputRe    = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	tfTypeRe       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	tfNameRe       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	tfIndexRe      = regexp.MustCompile(`^\[(\d+|"[^"\\]*")\]$`)
)

// AWS declares a service as an AWS emulator. Kind is ministack. Services
// lists the AWS services it loads; Region defaults to us-east-1;
// VerifySignatures (default true) makes the emulator check SigV4. Stacks are
// the repository's IaC the plan is built from (A5, A6); Env maps app
// variables to references into the plan, FunctionEnv copies Lambda function
// environments, and Seed names a seed file (A4).
type AWS struct {
	Kind             string            `yaml:"kind" json:"kind"`
	Services         []string          `yaml:"services" json:"services"`
	Region           string            `yaml:"region" json:"region,omitempty"`
	VerifySignatures *bool             `yaml:"verify_signatures" json:"verify_signatures,omitempty"`
	Stacks           []AWSStack        `yaml:"stacks" json:"stacks,omitempty"`
	Env              map[string]string `yaml:"env" json:"env,omitempty"`
	FunctionEnv      []string          `yaml:"function_env" json:"function_env,omitempty"`
	Seed             string            `yaml:"seed" json:"seed,omitempty"`
}

// AWSStack is one IaC source: exactly one of CloudFormation (a template
// path) and Terraform (a directory). Parameters and Imports belong to a
// CloudFormation stack, VarFiles and Vars to a Terraform one.
type AWSStack struct {
	Name           string            `yaml:"name" json:"name"`
	CloudFormation string            `yaml:"cloudformation" json:"cloudformation,omitempty"`
	Terraform      string            `yaml:"terraform" json:"terraform,omitempty"`
	Parameters     map[string]string `yaml:"parameters" json:"parameters,omitempty"`
	Imports        map[string]string `yaml:"imports" json:"imports,omitempty"`
	VarFiles       []string          `yaml:"var_files" json:"var_files,omitempty"`
	Vars           map[string]any    `yaml:"vars" json:"vars,omitempty"`
}

// ScenarioAWS is a scenario's override for one aws service: parameters and
// vars keyed by stack name, and a seed file that replaces the service's.
type ScenarioAWS struct {
	Parameters map[string]map[string]string `yaml:"parameters" json:"parameters,omitempty"`
	Vars       map[string]map[string]any    `yaml:"vars" json:"vars,omitempty"`
	Seed       string                       `yaml:"seed" json:"seed,omitempty"`
}

// RegionOrDefault is the block's region, else DefaultAWSRegion.
func (a *AWS) RegionOrDefault() string {
	if a.Region == "" {
		return DefaultAWSRegion
	}
	return a.Region
}

// VerifiesSignatures reports whether the emulator checks SigV4 (AUTH=true).
func (a *AWS) VerifiesSignatures() bool { return a.VerifySignatures == nil || *a.VerifySignatures }

// EmulatorServices is the emulator's SERVICES list: the block's services,
// with dynamodbstreams after dynamodb.
func (a *AWS) EmulatorServices() []string {
	var out []string
	for _, s := range a.Services {
		out = append(out, s)
		if s == "dynamodb" {
			out = append(out, "dynamodbstreams")
		}
	}
	return out
}

// Stack returns the stack named name.
func (a *AWS) Stack(name string) (*AWSStack, bool) {
	for i := range a.Stacks {
		if a.Stacks[i].Name == name {
			return &a.Stacks[i], true
		}
	}
	return nil, false
}

// IsTerraform reports whether the stack is a Terraform directory.
func (s *AWSStack) IsTerraform() bool { return s.Terraform != "" }

// AWSImage is aiman's pinned image of an AWS kind; empty for an unknown kind.
func AWSImage(kind string) string {
	if kind == AWSKindMiniStack {
		return MiniStackImage
	}
	return ""
}

// AWSEmuService is the compose service of an aws service's emulator.
// The service's own name is the edge tap's network alias.
func AWSEmuService(name string) string { return name + awsEmuSuffix }

// awsInitService hands the emulator's data volume to the run's user.
func awsInitService(name string) string { return name + awsInitSuffix }

// AWSServices lists the scenario's aws services in scenario order.
func (f *File) AWSServices(sc *Scenario) []string {
	var out []string
	for _, name := range sc.Services {
		if f.Services[name].AWS != nil {
			out = append(out, name)
		}
	}
	return out
}

// ServiceImageRef is the image a service runs: the file's, or for an aws
// service its kind's pin.
func (f *File) ServiceImageRef(name string) string {
	s := f.Services[name]
	if s.AWS != nil {
		return AWSImage(s.AWS.Kind)
	}
	return s.Image
}

// ComposeService is the compose service that runs the file's service name:
// the emulator for an aws service, else the name itself.
func (f *File) ComposeService(name string) string {
	if f.Services[name].AWS != nil {
		return AWSEmuService(name)
	}
	return name
}

// AWSRef is a parsed reference into an aws service's plan: an output
// (<stack>.outputs.<name>), or a resource's Ref value (<stack>.<logical id>)
// or attribute (<stack>.<logical id>.<attribute>). A Terraform logical id is
// the resource address, module path and index included.
type AWSRef struct {
	Stack     string `json:"stack"`
	Output    string `json:"output,omitempty"`
	LogicalID string `json:"logical_id,omitempty"`
	Attr      string `json:"attr,omitempty"`
}

func (r AWSRef) String() string {
	switch {
	case r.Output != "":
		return r.Stack + ".outputs." + r.Output
	case r.Attr != "":
		return r.Stack + "." + r.LogicalID + "." + r.Attr
	default:
		return r.Stack + "." + r.LogicalID
	}
}

// ParseRef reads a reference against the block's stacks.
func (a *AWS) ParseRef(ref string) (AWSRef, error) {
	stack, rest, ok := strings.Cut(ref, ".")
	if !ok || rest == "" {
		return AWSRef{}, fmt.Errorf("reference %q must be <stack>.<logical id>[.<attribute>] or <stack>.outputs.<name>", ref)
	}
	st, ok := a.Stack(stack)
	if !ok {
		return AWSRef{}, fmt.Errorf("reference %q names no stack %q", ref, stack)
	}
	if out, ok := strings.CutPrefix(rest, "outputs."); ok {
		if !cfnOutputRe.MatchString(out) && (!st.IsTerraform() || !tfNameRe.MatchString(out)) {
			return AWSRef{}, fmt.Errorf("reference %q: output %q is not an output name", ref, out)
		}
		return AWSRef{Stack: stack, Output: out}, nil
	}
	if st.IsTerraform() {
		id, attr, err := splitTFAddress(rest)
		if err != nil {
			return AWSRef{}, fmt.Errorf("reference %q: %w", ref, err)
		}
		return AWSRef{Stack: stack, LogicalID: id, Attr: attr}, nil
	}
	id, attr, _ := strings.Cut(rest, ".")
	if !cfnLogicalIDRe.MatchString(id) {
		return AWSRef{}, fmt.Errorf("reference %q: %q is not a CloudFormation logical id", ref, id)
	}
	if attr != "" && !cfnAttrRe.MatchString(attr) {
		return AWSRef{}, fmt.Errorf("reference %q: %q is not an attribute name", ref, attr)
	}
	return AWSRef{Stack: stack, LogicalID: id, Attr: attr}, nil
}

// splitTFAddress splits a Terraform resource address with an optional
// trailing attribute: (module.<name>[index].)* <type>.<name>[index][.<attr>].
func splitTFAddress(s string) (addr, attr string, err error) {
	segs, err := tfSegments(s)
	if err != nil {
		return "", "", err
	}
	i := 0
	var parts []string
	for i+1 < len(segs) && segs[i] == "module" {
		name, idx := splitTFIndex(segs[i+1])
		if !tfNameRe.MatchString(name) || (idx != "" && !tfIndexRe.MatchString(idx)) {
			return "", "", fmt.Errorf("module %q is not a module name", segs[i+1])
		}
		parts = append(parts, "module", segs[i+1])
		i += 2
	}
	if len(segs)-i < 2 {
		return "", "", fmt.Errorf("%q is not a Terraform resource address", s)
	}
	typ := segs[i]
	if typ == "data" {
		return "", "", fmt.Errorf("%q is a data source; only managed resources are emulated", s)
	}
	name, idx := splitTFIndex(segs[i+1])
	if !tfTypeRe.MatchString(typ) || !tfNameRe.MatchString(name) || (idx != "" && !tfIndexRe.MatchString(idx)) {
		return "", "", fmt.Errorf("%q is not a Terraform resource address", s)
	}
	parts = append(parts, typ, segs[i+1])
	i += 2
	switch len(segs) - i {
	case 0:
	case 1:
		if !tfNameRe.MatchString(segs[i]) {
			return "", "", fmt.Errorf("%q is not an attribute name", segs[i])
		}
		attr = segs[i]
	default:
		return "", "", fmt.Errorf("%q has more than one attribute after the address", s)
	}
	return strings.Join(parts, "."), attr, nil
}

// tfSegments splits on dots outside brackets and quotes.
func tfSegments(s string) ([]string, error) {
	var segs []string
	var cur strings.Builder
	depth, quoted := 0, false
	for _, r := range s {
		switch {
		case quoted:
			if r == '"' {
				quoted = false
			}
		case r == '"':
			if depth == 0 {
				return nil, fmt.Errorf("%q has a quote outside an index", s)
			}
			quoted = true
		case r == '[':
			depth++
		case r == ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%q has an unbalanced ]", s)
			}
		case r == '.' && depth == 0:
			segs = append(segs, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if depth != 0 || quoted {
		return nil, fmt.Errorf("%q has an unclosed index", s)
	}
	segs = append(segs, cur.String())
	for _, seg := range segs {
		if seg == "" {
			return nil, fmt.Errorf("%q has an empty part", s)
		}
	}
	return segs, nil
}

func splitTFIndex(seg string) (name, idx string) {
	if i := strings.IndexByte(seg, '['); i >= 0 {
		return seg[:i], seg[i:]
	}
	return seg, ""
}

// validateAWS checks an aws service: its block, the fields a plain service
// has that an emulator does not take, and its env.
func validateAWS(at string, s Service) []string {
	a := s.AWS
	if a == nil {
		return nil
	}
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	if a.Kind != AWSKindMiniStack {
		add("aws.kind %q must be %s", a.Kind, AWSKindMiniStack)
	}
	if s.Image != "" {
		add("image must be empty for an aws service: aiman runs its pinned %s image", AWSKindMiniStack)
	}
	if s.Database != nil || s.EVM != nil {
		add("a service is one of database, evm and aws")
	}
	if s.Root || len(s.Caps) > 0 {
		add("an aws service runs as the run's user with no capabilities; drop root and caps")
	}
	for _, k := range sortedKeys(s.Env) {
		if matchesAny(k, awsEmuEnvRefused) {
			add("env %s is set by aiman or not allowed on an aws service", k)
		}
	}
	errs = append(errs, validateAWSServices(at, a.Services)...)
	if a.Region != "" && !awsRegionRe.MatchString(a.Region) {
		add("aws.region %q is not a region name such as eu-west-1", a.Region)
	}
	errs = append(errs, validateAWSStacks(at, a.Stacks)...)
	for _, k := range sortedKeys(a.Env) {
		switch {
		case !envNameRe.MatchString(k):
			add("aws.env name %q is not a variable name", k)
		case strings.HasPrefix(k, "AWS_") || strings.HasPrefix(k, "AIMAN_"):
			add("aws.env %s: names starting AWS_ or AIMAN_ are aiman's", k)
		}
		if _, err := a.ParseRef(a.Env[k]); err != nil {
			add("aws.env %s: %v", k, err)
		}
	}
	for i, fe := range a.FunctionEnv {
		r, err := a.ParseRef(fe)
		switch {
		case err != nil:
			add("aws.function_env[%d]: %v", i, err)
		case r.Output != "" || r.Attr != "":
			add("aws.function_env[%d] %q must be <stack>.<logical id> of a function", i, fe)
		default:
			if st, _ := a.Stack(r.Stack); st.IsTerraform() {
				add("aws.function_env[%d] %q: function_env reads CloudFormation functions only", i, fe)
			}
		}
	}
	if a.Seed != "" {
		if err := cigate.ValidateDir(a.Seed); err != nil {
			add("aws.seed %q %v", a.Seed, err)
		}
	}
	return errs
}

func validateAWSServices(at string, services []string) []string {
	var errs []string
	if len(services) == 0 {
		errs = append(errs, fmt.Sprintf("%s: aws.services must list at least one of %s", at, strings.Join(AWSServiceNames, ", ")))
	}
	seen := map[string]bool{}
	for _, s := range services {
		switch {
		case seen[s]:
			errs = append(errs, fmt.Sprintf("%s: aws.services lists %s twice", at, s))
		case slices.Contains(AWSServiceNames, s):
		case awsUnsupported[s] != "":
			errs = append(errs, fmt.Sprintf("%s: aws.services %s %s or egress, which a harness run has not; use %s", at, s, awsUnsupported[s], strings.Join(AWSServiceNames, ", ")))
		default:
			errs = append(errs, fmt.Sprintf("%s: aws.services %q is not one of %s", at, s, strings.Join(AWSServiceNames, ", ")))
		}
		seen[s] = true
	}
	return errs
}

func validateAWSStacks(at string, stacks []AWSStack) []string {
	var errs []string
	seen := map[string]bool{}
	for i, st := range stacks {
		add := func(format string, args ...any) {
			errs = append(errs, fmt.Sprintf("%s: aws.stacks[%d]: ", at, i)+fmt.Sprintf(format, args...))
		}
		if !awsStackNameRe.MatchString(st.Name) {
			add("name %q must match %s", st.Name, awsStackNameRe)
		} else if seen[st.Name] {
			add("duplicate name %q", st.Name)
		}
		seen[st.Name] = true
		if (st.CloudFormation == "") == (st.Terraform == "") {
			add("set exactly one of cloudformation and terraform")
		}
		for _, p := range []string{st.CloudFormation, st.Terraform} {
			if p != "" {
				if err := cigate.ValidateDir(p); err != nil {
					add("path %q %v", p, err)
				}
			}
		}
		for _, p := range st.VarFiles {
			if err := cigate.ValidateDir(p); err != nil {
				add("var_files %q %v", p, err)
			}
		}
		if st.Terraform != "" && (len(st.Parameters) > 0 || len(st.Imports) > 0) {
			add("parameters and imports belong to a cloudformation stack")
		}
		if st.CloudFormation != "" && (len(st.VarFiles) > 0 || len(st.Vars) > 0) {
			add("var_files and vars belong to a terraform stack")
		}
	}
	return errs
}

// validateScenarioAWS checks a scenario's aws overrides and that it starts
// at most one aws service.
func (f *File) validateScenarioAWS(sc *Scenario) []string {
	var errs []string
	if n := len(f.AWSServices(sc)); n > 1 {
		errs = append(errs, fmt.Sprintf("a scenario starts at most one aws service; it starts %d", n))
	}
	for _, name := range sortedKeys(sc.AWS) {
		o := sc.AWS[name]
		svc, ok := f.Services[name]
		if !slices.Contains(sc.Services, name) || !ok || svc.AWS == nil {
			errs = append(errs, fmt.Sprintf("aws.%s names no aws service the scenario starts", name))
			continue
		}
		for _, stack := range sortedKeys(o.Parameters) {
			if st, ok := svc.AWS.Stack(stack); !ok || st.IsTerraform() {
				errs = append(errs, fmt.Sprintf("aws.%s.parameters.%s names no cloudformation stack of %s", name, stack, name))
			}
		}
		for _, stack := range sortedKeys(o.Vars) {
			if st, ok := svc.AWS.Stack(stack); !ok || !st.IsTerraform() {
				errs = append(errs, fmt.Sprintf("aws.%s.vars.%s names no terraform stack of %s", name, stack, name))
			}
		}
		if o.Seed != "" {
			if err := cigate.ValidateDir(o.Seed); err != nil {
				errs = append(errs, fmt.Sprintf("aws.%s.seed %q %v", name, o.Seed, err))
			}
		}
	}
	return errs
}

// validateAWSIdentity refuses, in every harness file, a variable that picks
// or carries real AWS credentials wherever the file sets one, and any value
// shaped like an AWS access key id anywhere in the file.
func (f *File) validateAWSIdentity() []string {
	var errs []string
	refuse := func(at, name string) {
		if matchesAny(name, AWSIdentityVars) {
			errs = append(errs, fmt.Sprintf("%s: %s selects or carries real AWS credentials; a harness file may not set it", at, name))
		}
	}
	for _, name := range sortedKeys(f.Services) {
		s := f.Services[name]
		for _, k := range sortedKeys(s.Env) {
			refuse("services."+name+".env", k)
		}
		if s.AWS != nil {
			for _, k := range sortedKeys(s.AWS.Env) {
				refuse("services."+name+".aws.env", k)
			}
		}
	}
	if f.App.Debug != nil {
		for _, k := range f.App.Debug.WrapEnv {
			refuse("app.debug.wrap_env", k)
		}
	}
	for i, sc := range f.Scenarios {
		for j, c := range sc.Checks {
			if c.Debug != nil {
				for _, k := range c.Debug.WrapEnv {
					refuse(fmt.Sprintf("scenarios[%d].checks[%d].debug.wrap_env", i, j), k)
				}
			}
		}
	}
	walkStrings(reflect.ValueOf(f).Elem(), "", func(at, s string) {
		if awsKeyIDRe.MatchString(s) {
			errs = append(errs, fmt.Sprintf("%s: holds a value shaped like an AWS access key id; a harness file never carries AWS credentials", strings.TrimPrefix(at, ".")))
		}
	})
	return errs
}

// AllowedWithAWS is harness.allowed_images (nil is DefaultAllowedImages)
// plus each harness.aws.images replacement, so a replacement is vetted as
// a listed image.
func AllowedWithAWS(allowed []string, awsImages map[string]string) []string {
	if allowed == nil {
		allowed = DefaultAllowedImages
	}
	out := slices.Clone(allowed)
	for _, kind := range sortedKeys(awsImages) {
		if ref := strings.TrimSpace(awsImages[kind]); ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// matchesAny reports whether name equals a pattern, or starts with one that
// ends in *.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if pre, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(name, pre) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}

// walkStrings calls fn with every string in v (struct fields, map keys and
// values, slice elements, any), naming each by its yaml path.
func walkStrings(v reflect.Value, at string, fn func(at, s string)) {
	switch v.Kind() {
	case reflect.String:
		fn(at, v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), at, fn)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
			if name == "" {
				name = strings.ToLower(field.Name)
			}
			walkStrings(v.Field(i), at+"."+name, fn)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkStrings(v.Index(i), fmt.Sprintf("%s[%d]", at, i), fn)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			kat := fmt.Sprintf("%s.%v", at, k)
			walkStrings(k, kat, fn)
			walkStrings(v.MapIndex(k), kat, fn)
		}
	}
}
