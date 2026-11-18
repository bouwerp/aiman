package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// awsFixture is the design's harness file: an aws service with a
// CloudFormation and a Terraform stack, env references, function_env, a
// seed, and a scenario overriding both stacks' inputs.
const awsFixture = `version: 1
app:
  run: python3 /src/app.py
  ports: [{name: web, container: 8080}]
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1
      stacks:
        - name: orders
          cloudformation: infra/template.yaml
          parameters: {Stage: test}
          imports: {SharedVpcId: vpc-0}
        - name: shared
          terraform: infra/tf/shared
          var_files: [infra/tf/test.tfvars]
          vars: {env: test, azs: [a, b]}
      env:
        ORDERS_TABLE: orders.OrdersTable
        ORDERS_STREAM: orders.OrdersTable.StreamArn
        UPLOAD_BUCKET: orders.outputs.UploadBucketName
        EVENTS_QUEUE_URL: shared.aws_sqs_queue.events.url
        DLQ_ARN: shared.module.q.aws_sqs_queue.dlq["a.b"].arn
        TF_OUT: shared.outputs.queue_url
      function_env: [orders.OrdersFunction]
      seed: .aiman/aws/seed.yaml
    limits: {memory: 768m}
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    database: {kind: postgres}
scenarios:
  - id: upload
    services: [aws, db]
    aws:
      aws:
        parameters: {orders: {Stage: e2e}}
        vars: {shared: {env: e2e}}
        seed: .aiman/aws/seed-upload.yaml
    checks: [{name: unit, run: pytest}]
    steps:
      - do: Upload a file at {{web}}
`

func awsFile(t *testing.T) *File {
	t.Helper()
	f, err := Parse([]byte(awsFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func invalidErrs(t *testing.T, yamlText string) []string {
	t.Helper()
	_, err := Parse([]byte(yamlText))
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("want harness_invalid, got %v", err)
	}
	return he.Errors
}

func wantErr(t *testing.T, errs []string, sub string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return
		}
	}
	t.Errorf("no error containing %q in %q", sub, errs)
}

func TestAWSFileParses(t *testing.T) {
	f := awsFile(t)
	a := f.Services["aws"].AWS
	if a == nil || a.Kind != AWSKindMiniStack || a.RegionOrDefault() != "eu-west-1" || !a.VerifiesSignatures() {
		t.Fatalf("aws block = %+v", a)
	}
	if got := a.EmulatorServices(); !slices.Equal(got, []string{"s3", "dynamodb", "dynamodbstreams", "sqs", "sns"}) {
		t.Fatalf("SERVICES = %v", got)
	}
	if st, _ := a.Stack("shared"); !st.IsTerraform() || st.Vars["azs"] == nil {
		t.Fatalf("shared stack = %+v", st)
	}
	sc, _ := f.Scenario("upload")
	if sc.AWS["aws"].Parameters["orders"]["Stage"] != "e2e" || sc.AWS["aws"].Seed != ".aiman/aws/seed-upload.yaml" {
		t.Fatalf("scenario aws = %+v", sc.AWS)
	}
	if got := f.AWSServices(sc); !slices.Equal(got, []string{"aws"}) {
		t.Fatalf("AWSServices = %v", got)
	}
	if f.ServiceImageRef("aws") != MiniStackImage || f.ComposeService("aws") != "aws-emu" || f.ComposeService("db") != "db" {
		t.Fatalf("image %q compose %q", f.ServiceImageRef("aws"), f.ComposeService("aws"))
	}
	paths := f.Paths()
	for _, p := range []string{"infra/template.yaml", "infra/tf/shared", "infra/tf/test.tfvars", ".aiman/aws/seed.yaml", ".aiman/aws/seed-upload.yaml"} {
		if !slices.Contains(paths, p) {
			t.Errorf("Paths %v misses %s", paths, p)
		}
	}
	if l := f.ServiceLimits("aws", 0); l.Memory != 768<<20 {
		t.Errorf("aws memory %d, want the file's 768m", l.Memory)
	}
	delete(f.Services, "db")
	f.Services["aws"] = Service{AWS: a}
	if l := f.ServiceLimits("aws", 4<<30); l.Memory != DefaultAWSMemory {
		t.Errorf("aws default memory %d, want %d", l.Memory, DefaultAWSMemory)
	}
}

func TestAWSFileRefs(t *testing.T) {
	a := awsFile(t).Services["aws"].AWS
	for ref, want := range map[string]AWSRef{
		"orders.OrdersTable":                           {Stack: "orders", LogicalID: "OrdersTable"},
		"orders.OrdersTable.StreamArn":                 {Stack: "orders", LogicalID: "OrdersTable", Attr: "StreamArn"},
		"orders.Db.Endpoint.Address":                   {Stack: "orders", LogicalID: "Db", Attr: "Endpoint.Address"},
		"orders.outputs.UploadBucketName":              {Stack: "orders", Output: "UploadBucketName"},
		"shared.aws_sqs_queue.events":                  {Stack: "shared", LogicalID: "aws_sqs_queue.events"},
		"shared.aws_sqs_queue.events.url":              {Stack: "shared", LogicalID: "aws_sqs_queue.events", Attr: "url"},
		"shared.aws_sqs_queue.q[0].arn":                {Stack: "shared", LogicalID: "aws_sqs_queue.q[0]", Attr: "arn"},
		`shared.module.q.module.r[1].aws_sns_topic.t`:  {Stack: "shared", LogicalID: "module.q.module.r[1].aws_sns_topic.t"},
		`shared.module.q.aws_sqs_queue.dlq["a.b"].arn`: {Stack: "shared", LogicalID: `module.q.aws_sqs_queue.dlq["a.b"]`, Attr: "arn"},
		"shared.outputs.queue_url":                     {Stack: "shared", Output: "queue_url"},
	} {
		got, err := a.ParseRef(ref)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", ref, got, err, want)
			continue
		}
		if got.String() != ref {
			t.Errorf("String() = %q, want %q", got.String(), ref)
		}
	}
	for _, bad := range []string{
		"", "orders", "nostack.X", "orders.Orders-Table", "orders.outputs.", "shared.aws_sqs_queue",
		"shared.data.aws_caller_identity.me.id", "shared.aws_sqs_queue.q.arn.more", `shared.aws_sqs_queue.q["x`,
		"shared.aws_sqs_queue..x", "shared.aws_sqs_queue.q[a].arn",
	} {
		if r, err := a.ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) = %+v, want an error", bad, r)
		}
	}
}

func TestAWSFileValidation(t *testing.T) {
	base := `version: 1
app: {run: x}
services:
  aws:
%s
scenarios:
  - id: s
    services: [aws]
`
	cases := map[string]string{
		"    aws: {kind: localstack, services: [s3]}":                                                                       `aws.kind "localstack" must be ministack`,
		"    image: ministackorg/ministack:latest\n    aws: {kind: ministack, services: [s3]}":                              "image must be empty for an aws service",
		"    aws: {kind: ministack, services: []}":                                                                          "aws.services must list at least one",
		"    aws: {kind: ministack, services: [lambda]}":                                                                    "lambda needs the Docker socket",
		"    aws: {kind: ministack, services: [kinesis]}":                                                                   `aws.services "kinesis" is not one of`,
		"    aws: {kind: ministack, services: [s3, s3]}":                                                                    "lists s3 twice",
		"    aws: {kind: ministack, services: [s3], region: EU}":                                                            `aws.region "EU" is not a region name`,
		"    aws: {kind: ministack, services: [s3]}\n    database: {kind: postgres}":                                        "one of database, evm and aws",
		"    aws: {kind: ministack, services: [s3]}\n    root: true":                                                        "drop root and caps",
		"    aws: {kind: ministack, services: [s3]}\n    env: {AUTH: 'false'}":                                              "env AUTH is set by aiman",
		"    aws: {kind: ministack, services: [s3]}\n    env: {LAMBDA_EXECUTOR: x}":                                         "env LAMBDA_EXECUTOR is set by aiman",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: A, cloudformation: t.yaml}]}":                           "name \"A\" must match",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a}]}":                                                   "set exactly one of cloudformation and terraform",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml, terraform: tf}]}":            "set exactly one of cloudformation and terraform",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: ../t.yaml}]}":                        "escapes the repository",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: /t.yaml}]}":                          "must be relative",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml}, {name: a, terraform: tf}]}": `duplicate name "a"`,
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, terraform: tf, parameters: {X: y}}]}":                "parameters and imports belong to a cloudformation stack",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml, vars: {x: 1}}]}":             "var_files and vars belong to a terraform stack",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml}], env: {AWS_X: a.T}}":        "names starting AWS_ or AIMAN_",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml}], env: {AIMAN_X: a.T}}":      "names starting AWS_ or AIMAN_",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml}], env: {T: b.T}}":            `names no stack "b"`,
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, cloudformation: t.yaml}], function_env: [a.F.Arn]}":  "must be <stack>.<logical id> of a function",
		"    aws: {kind: ministack, services: [s3], stacks: [{name: a, terraform: tf}], function_env: [a.aws_sqs_queue.f]}": "a terraform function_env names an aws_lambda_function",
		"    aws: {kind: ministack, services: [s3], seed: ../seed.yaml}":                                                    "escapes the repository",
		"    aws: {kind: ministack, services: [s3], verify: false}":                                                         "unknown key verify",
	}
	for block, want := range cases {
		wantErr(t, invalidErrs(t, strings.Replace(base, "%s", block, 1)), want)
	}
}

func TestAWSFileScenarioRules(t *testing.T) {
	two := `version: 1
app: {run: x}
services:
  a1: {aws: {kind: ministack, services: [s3]}}
  a2: {aws: {kind: ministack, services: [sqs], stacks: [{name: st, terraform: tf}]}}
  db: {image: postgres:16}
  db-emu: {image: postgres:16}
  db-emu-init: {image: postgres:16}
scenarios:
  - id: s
    services: [a1, a2]
  - id: t
    services: [a2, db]
    aws:
      db: {seed: x.yaml}
      a2: {parameters: {st: {X: y}}, vars: {nope: {x: 1}}, seed: /abs.yaml}
`
	errs := invalidErrs(t, two)
	for _, want := range []string{
		"scenarios[0] s: a scenario starts at most one aws service; it starts 2",
		"scenarios[1] t: aws.db names no aws service",
		"aws.a2.parameters.st names no cloudformation stack",
		"aws.a2.vars.nope names no terraform stack",
		`aws.a2.seed "/abs.yaml" must be relative`,
		"services.db-emu: names ending -emu, -emu-init or -emu-apply are reserved",
		"services.db-emu-init: names ending -emu, -emu-init or -emu-apply are reserved",
	} {
		wantErr(t, errs, want)
	}
}

// TestAWSRefuseIdentityVars refuses AWS identity variables in every harness
// file, aws service or not, wherever the file sets a variable.
func TestAWSRefuseIdentityVars(t *testing.T) {
	file := `version: 1
app:
  run: x
  debug: {lang: python, entry: app.py, wrap_env: [AWS_PROFILE]}
services:
  db:
    image: postgres:16
    env: {AWS_SECRET_ACCESS_KEY: x, AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE: /t, AWS_EC2_METADATA_SERVICE_ENDPOINT: http://x, AWS_REGION: eu-west-1}
scenarios:
  - id: s
    services: [db]
    checks:
      - {name: c, run: pytest, debug: {lang: python, wrap_env: [AWS_SESSION_TOKEN]}}
`
	errs := invalidErrs(t, file)
	for _, want := range []string{
		"services.db.env: AWS_SECRET_ACCESS_KEY selects or carries real AWS credentials",
		"services.db.env: AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
		"services.db.env: AWS_EC2_METADATA_SERVICE_ENDPOINT",
		"app.debug.wrap_env: AWS_PROFILE",
		"scenarios[0].checks[0].debug.wrap_env: AWS_SESSION_TOKEN",
	} {
		wantErr(t, errs, want)
	}
	for _, e := range errs {
		if strings.Contains(e, "AWS_REGION") {
			t.Errorf("AWS_REGION is not an identity variable: %s", e)
		}
	}
	for _, v := range AWSIdentityVars {
		name := strings.TrimSuffix(v, "*") + map[bool]string{true: "X", false: ""}[strings.HasSuffix(v, "*")]
		errs := invalidErrs(t, "version: 1\napp: {run: x}\nservices:\n  s: {image: redis:7, env: {"+name+": v}}\nscenarios: [{id: s, services: [s]}]\n")
		wantErr(t, errs, name+" selects or carries real AWS credentials")
	}
}

// TestAWSRefuseKeyIDs refuses a value shaped like an access key id in env,
// parameters, vars, imports and anywhere else in the file.
func TestAWSRefuseKeyIDs(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE"
	file := `version: 1
app: {run: "echo ` + key + `"}
services:
  db: {image: postgres:16, env: {TOKEN: "id=ASIAABCDEFGHIJKLMNOP"}}
  aws:
    aws:
      kind: ministack
      services: [s3]
      stacks:
        - {name: a, cloudformation: t.yaml, parameters: {K: ` + key + `}, imports: {V: ` + key + `}}
        - {name: b, terraform: tf, vars: {list: [x, ` + key + `]}}
scenarios:
  - id: s
    services: [db, aws]
    aws: {aws: {parameters: {a: {K: ` + key + `}}}}
`
	errs := invalidErrs(t, file)
	for _, want := range []string{
		"app.run: holds a value shaped like an AWS access key id",
		"services.db.env.TOKEN: holds a value shaped like an AWS access key id",
		"services.aws.aws.stacks[0].parameters.K: holds",
		"services.aws.aws.stacks[0].imports.V: holds",
		"services.aws.aws.stacks[1].vars.list[1]: holds",
		"scenarios[0].aws.aws.parameters.a.K: holds",
	} {
		wantErr(t, errs, want)
	}
	// Lower case and too short are not key ids.
	if _, err := Parse([]byte("version: 1\napp: {run: 'echo akiaiosfodnn7example AKIA123'}\nscenarios: [{id: s}]\n")); err != nil {
		t.Fatalf("not a key id: %v", err)
	}
}

func awsSpec() RunSpec {
	s := testSpec()
	s.Scenario = "upload"
	s.ReadPassword = "ro-secret"
	return s
}

func TestAWSComposeEmulator(t *testing.T) {
	p, doc := buildDebug(t, awsFixture, awsSpec())
	svcs, _ := doc["services"].(map[string]any)
	if _, ok := svcs["aws"]; ok {
		t.Fatalf("a compose service holds the aws name, which is the edge's alias: %v", keys(svcs))
	}
	emu := svcOf(t, doc, "aws-emu")
	if emu["image"] != MiniStackImage || emu["user"] != "1004:1004" || emu["read_only"] != true || emu["pull_policy"] != "never" {
		t.Fatalf("emulator = %v", emu)
	}
	if !slices.Equal(strs(emu["cap_drop"]), []string{"ALL"}) || !slices.Equal(strs(emu["security_opt"]), []string{"no-new-privileges:true"}) ||
		emu["cap_add"] != nil || !slices.Equal(strs(emu["tmpfs"]), []string{"/tmp"}) || !slices.Equal(strs(emu["networks"]), []string{NetworkInner}) {
		t.Fatalf("emulator hardening = %v", emu)
	}
	if emu["mem_limit"] != 768<<20 {
		t.Fatalf("emulator memory %v", emu["mem_limit"])
	}
	env := envOf(t, emu)
	for k, v := range map[string]string{
		"SERVICES": "s3,dynamodb,dynamodbstreams,sqs,sns", "AUTH": "true", "MINISTACK_REGION": "eu-west-1",
		"GATEWAY_PORT": "4566", "S3_PERSIST": "1", "S3_DATA_DIR": "/data/s3", "LOG_LEVEL": "WARNING",
	} {
		if env[k] != v {
			t.Errorf("emulator %s = %q, want %q", k, env[k], v)
		}
	}
	for k := range env {
		if strings.HasPrefix(k, "AWS_") || strings.Contains(strings.ToLower(k), "proxy") {
			t.Errorf("emulator gets %s", k)
		}
	}
	if !slices.Equal(strs(emu["volumes"]), []string{"data-aws:/data"}) {
		t.Errorf("emulator volumes %v", strs(emu["volumes"]))
	}
	hc, _ := emu["healthcheck"].(map[string]any)
	test := strs(hc["test"])
	if len(test) < 4 || test[0] != "CMD" || test[1] != "python" || !strings.Contains(test[3], "/_ministack/health") ||
		!slices.Equal(test[4:], []string{"s3", "dynamodb", "dynamodbstreams", "sqs", "sns"}) {
		t.Errorf("health check %v", test)
	}
	dep, _ := emu["depends_on"].(map[string]any)
	if c, _ := dep["aws-emu-init"].(map[string]any); c["condition"] != "service_completed_successfully" {
		t.Errorf("depends_on %v", dep)
	}
	init := svcOf(t, doc, "aws-emu-init")
	if init["user"] != "0:0" || !slices.Equal(strs(init["cap_add"]), []string{"CHOWN"}) || init["network_mode"] != "none" ||
		!slices.Equal(strs(init["command"]), []string{"1004:1004", "/data"}) || !slices.Equal(strs(init["entrypoint"]), []string{"chown"}) {
		t.Errorf("init = %v", init)
	}
	vols, _ := doc["volumes"].(map[string]any)
	if _, ok := vols["data-aws"]; !ok {
		t.Errorf("volumes %v miss data-aws", keys(vols))
	}
	if len(p.Relays) != 3 || p.Relays[0].Name != "web" || p.Relays[2].Name != "db" ||
		p.Relays[1] != (Relay{Name: "aws", Service: "aws-emu", Port: FirstRelayPort + 1, Target: "aws-emu:4566", Kind: RelayKindAWS, Region: "eu-west-1"}) {
		t.Errorf("relays %+v: the aws service publishes its read relay", p.Relays)
	}
	cmd := strs(svcOf(t, doc, ServiceEdge)["command"])
	if i := slices.Index(cmd, "--aws-relay"); i < 2 || cmd[i+1] != "aws=10002=aws-emu:4566" || cmd[i-2] != "--aws-region" || cmd[i-1] != "eu-west-1" {
		t.Errorf("edge command %v", cmd)
	}
}

func TestAWSComposeSignaturesOffAndImage(t *testing.T) {
	file := strings.Replace(awsFixture, "      region: eu-west-1\n", "      verify_signatures: false\n", 1)
	spec := awsSpec()
	spec.ServiceImages = map[string]string{"aws": "sha256:emu", "db": "sha256:pg"}
	_, doc := buildDebug(t, file, spec)
	emu := svcOf(t, doc, "aws-emu")
	if env := envOf(t, emu); env["AUTH"] != "false" || env["MINISTACK_REGION"] != DefaultAWSRegion {
		t.Errorf("env %v", env)
	}
	if emu["image"] != "sha256:emu" || svcOf(t, doc, "aws-emu-init")["image"] != "sha256:emu" {
		t.Errorf("image %v", emu["image"])
	}
}

func TestAWSEnvOnEveryContainer(t *testing.T) {
	file := checkImageFixture(strings.Replace(awsFixture, "    checks: [{name: unit, run: pytest}]\n", "", 1))
	spec := awsSpec()
	spec.CheckImages = map[string]string{CheckService(1): "sha256:node"}
	spec.Proxy = &ProxySpec{}
	p, doc := buildDebug(t, file, spec)
	want := map[string]string{
		"AWS_ENDPOINT_URL": "http://aws:4566", "AWS_ENDPOINT_URL_S3": "http://aws:4566", "AWS_ENDPOINT_URL_DYNAMODB": "http://aws:4566",
		"AWS_ENDPOINT_URL_DYNAMODB_STREAMS": "http://aws:4566", "AWS_ENDPOINT_URL_SQS": "http://aws:4566", "AWS_ENDPOINT_URL_SNS": "http://aws:4566",
		"AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "eu-west-1", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_CONFIG_FILE": "/aiman-aws/config", "AWS_SHARED_CREDENTIALS_FILE": "/aiman-aws/credentials", "AWS_EC2_METADATA_DISABLED": "true",
	}
	for _, name := range []string{ServiceApp, "db", CheckService(1)} {
		s := svcOf(t, doc, name)
		env := envOf(t, s)
		for k, v := range want {
			if env[k] != v {
				t.Errorf("%s %s = %q, want %q", name, k, env[k], v)
			}
		}
		if !slices.Contains(strs(s["volumes"]), "./aws:/aiman-aws:ro") {
			t.Errorf("%s volumes %v miss /aiman-aws", name, strs(s["volumes"]))
		}
	}
	if !strings.Contains(envOf(t, svcOf(t, doc, ServiceApp))["NO_PROXY"], "aws") {
		t.Errorf("NO_PROXY misses the aws service")
	}
	if string(p.Files[AWSConfigFile]) != "[default]\nregion = eu-west-1\nendpoint_url = http://aws:4566\ns3 =\n    addressing_style = path\n" {
		t.Errorf("config file %q", p.Files[AWSConfigFile])
	}
	if string(p.Files[AWSCredentialsFile]) != "[default]\naws_access_key_id = test\naws_secret_access_key = test\n" {
		t.Errorf("credentials file %q", p.Files[AWSCredentialsFile])
	}
	if len(p.Files[InitDBFile]) == 0 {
		t.Errorf("the postgres init script went missing")
	}
	// The aws variables win over a service's own.
	f := strings.Replace(awsFixture, "env: {POSTGRES_PASSWORD: test}", "env: {POSTGRES_PASSWORD: test, AWS_REGION: us-west-2}", 1)
	_, doc = buildDebug(t, f, awsSpec())
	if got := envOf(t, svcOf(t, doc, "db"))["AWS_REGION"]; got != "eu-west-1" {
		t.Errorf("db AWS_REGION = %q, want aiman's", got)
	}
}

func TestAWSComposeEdgeTapAndBlock(t *testing.T) {
	spec := awsSpec()
	spec.Proxy = &ProxySpec{Online: true, UpstreamRoots: "/etc/ssl/certs/ca-certificates.crt"}
	_, doc := buildDebug(t, awsFixture, spec)
	edgeSvc := svcOf(t, doc, ServiceEdge)
	cmd := strings.Join(strs(edgeSvc["command"]), " ")
	if !strings.Contains(cmd, "--aws-tap aws=aws-emu:4566") {
		t.Errorf("edge command %q has no tap", cmd)
	}
	for _, s := range AWSBlockSuffixes {
		if !strings.Contains(cmd, "--block-host-suffix "+s) {
			t.Errorf("edge command %q does not block %s", cmd, s)
		}
	}
	nets, _ := edgeSvc["networks"].(map[string]any)
	inner, _ := nets[NetworkInner].(map[string]any)
	if !slices.Equal(strs(inner["aliases"]), []string{"aws"}) || nets[NetworkOuter] == nil {
		t.Errorf("edge networks %v", nets)
	}
	// Without an aws service nothing is blocked or tapped.
	_, doc = buildFixture(t, proxySpec(true))
	cmd = strings.Join(strs(svcOf(t, doc, ServiceEdge)["command"]), " ")
	if strings.Contains(cmd, "--block-host-suffix") || strings.Contains(cmd, "--aws-tap") {
		t.Errorf("a run with no aws service: %q", cmd)
	}
}

func TestAWSComposeDebugEdgeKeepsAddress(t *testing.T) {
	a := withAlias(map[string]composeAttach{NetworkInner: {}, NetworkDebug: {IPv4Address: "10.232.1.4"}}, NetworkInner, "aws")
	if !slices.Equal(a[NetworkInner].Aliases, []string{"aws"}) || a[NetworkDebug].IPv4Address != "10.232.1.4" {
		t.Fatalf("alias on the map form = %+v", a)
	}
	b := withAlias([]string{NetworkInner, NetworkOuter}, NetworkInner, "aws")
	if len(b) != 2 || !slices.Equal(b[NetworkInner].Aliases, []string{"aws"}) {
		t.Fatalf("alias on the list form = %+v", b)
	}
}

func TestAWSAllowedWithOverride(t *testing.T) {
	got := AllowedWithAWS(nil, map[string]string{AWSKindMiniStack: "registry.example/ministack:1.6"})
	if !ImageAllowed("registry.example/ministack:1.6", got) || !ImageAllowed("postgres:16", got) {
		t.Fatalf("allowed = %v", got)
	}
	if got := AllowedWithAWS([]string{"redis:7"}, nil); !slices.Equal(got, []string{"redis:7"}) {
		t.Fatalf("allowed = %v", got)
	}
}

func TestAWSRunContainers(t *testing.T) {
	r := Run{Services: []string{"aws", "db"}, AWSServices: []string{"aws"}}
	if got := r.containers(); !slices.Equal(got, []string{ServiceApp, ServiceEdge, "aws-emu", "db"}) {
		t.Fatalf("containers %v", got)
	}
}

const awsManagerFile = `version: 1
app:
  run: python3 app.py
services:
  aws:
    aws: {kind: ministack, services: [s3]}
scenarios:
  - id: aws
    services: [aws]
`

// TestAWSManagerImageAndStart asks for the pinned MiniStack image as
// builtin, starts the emulator's compose service, and records the aws
// service on the run; an operator override is asked for as a plain image.
func TestAWSManagerImageAndStart(t *testing.T) {
	v := &recordingVetter{}
	tm := newTestManager(t, func(o *Options) { o.Images = v })
	run := tm.upReady(t, testRepo(t, awsManagerFile), "aws")
	var emu *ImageRequest
	for i := range v.reqs {
		if v.reqs[i].Ref == MiniStackImage {
			emu = &v.reqs[i]
		}
	}
	if emu == nil || !emu.Builtin {
		t.Fatalf("emulator request = %+v in %+v", emu, v.reqs)
	}
	if run.Images["aws"] == "" || !slices.Equal(run.AWSServices, []string{"aws"}) {
		t.Fatalf("run = %+v", run)
	}
	if tm.docker.called("up -d --wait --wait-timeout 60 aws-emu") != 1 {
		t.Fatalf("emulator not started by its compose name; calls %q", tm.docker.calls)
	}

	v2 := &recordingVetter{}
	tm2 := newTestManager(t, func(o *Options) {
		o.Images = v2
		o.AWSImages = map[string]string{AWSKindMiniStack: "registry.example/ministack:2"}
	})
	tm2.upReady(t, testRepo(t, awsManagerFile), "aws")
	found := false
	for _, r := range v2.reqs {
		if r.Ref == MiniStackImage || (r.Ref == "registry.example/ministack:2" && r.Builtin) {
			t.Fatalf("override request = %+v", r)
		}
		found = found || r.Ref == "registry.example/ministack:2"
	}
	if !found {
		t.Fatalf("override never asked for: %+v", v2.reqs)
	}
}
