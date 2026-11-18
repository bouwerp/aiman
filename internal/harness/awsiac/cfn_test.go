package awsiac

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

var updateCFN = flag.Bool("update", false, "rewrite the CloudFormation golden plans under testdata/cfn")

var allServices = []string{"s3", "dynamodb", "sqs", "sns"}

// cfnGoldenView is what a golden plan records: the plan after Build and
// BuildEnv, with each value shown resolved (or pending).
type cfnGoldenView struct {
	Resources []*Resource       `json:"resources"`
	Outputs   map[string]string `json:"outputs"`
	Functions []FunctionEnv     `json:"functions,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Sensitive []string          `json:"sensitive_env,omitempty"`
	Steps     []string          `json:"steps"`
	Warnings  []string          `json:"warnings"`
	Notes     []string          `json:"notes"`
}

func goldenView(p *Plan) cfnGoldenView {
	v := cfnGoldenView{Resources: p.Resources, Outputs: map[string]string{}, Functions: p.Functions, Env: map[string]string{}, Sensitive: p.SensitiveEnv}
	show := func(val Value) string {
		s, err := p.Resolve(val)
		if err != nil {
			return "(pending) " + val.String()
		}
		return s
	}
	for _, o := range p.Outputs {
		v.Outputs[o.Name] = show(o.Value)
	}
	for k, val := range p.Env {
		v.Env[k] = show(val)
	}
	for _, st := range p.Steps {
		v.Steps = append(v.Steps, st.Service+" "+st.Operation+" "+st.Resource)
	}
	for _, w := range p.Warnings {
		v.Warnings = append(v.Warnings, w.String())
	}
	for _, w := range p.Notes {
		v.Notes = append(v.Notes, w.String())
	}
	return v
}

// importDir imports a template from a testdata directory into a new plan.
func importDir(t *testing.T, dir, file string, params, imports map[string]string) (*Plan, error) {
	t.Helper()
	p := New("aws", "eu-west-1", "http://aws:4566", allServices)
	src := StackSource{Name: "app", Format: SourceCloudFormation, Path: file, Root: os.DirFS(dir), Parameters: params, Imports: imports}
	return p, ImportCloudFormation(context.Background(), src, p)
}

func TestCFNGoldenPlans(t *testing.T) {
	cases := []struct {
		name, dir, file string
		params          map[string]string
		imports         map[string]string
		functions       []string
		env             map[string]Ref
	}{
		{
			name: "sam-app", dir: "sam-app", file: "template.yaml",
			functions: []string{"RecordFunction"},
			env:       map[string]Ref{"UPLOAD_BUCKET": {Stack: "app", Output: "UploadBucketName"}, "TABLE": {Stack: "app", LogicalID: "UploadsTable"}},
		},
		{
			name: "sam-app-prod", dir: "sam-app", file: "template.yaml",
			params:    map[string]string{"Stage": "prod", "ApiToken": "s3cret"},
			imports:   map[string]string{"shared-vpc-id": "vpc-0abc"},
			functions: []string{"RecordFunction"},
		},
		{
			name: "stack", dir: "stack", file: "template.yaml",
			env: map[string]Ref{"STREAM": {Stack: "app", LogicalID: "OrdersTable", Attr: "StreamArn"}},
		},
		{
			name: "stack-archive", dir: "stack", file: "template.yaml",
			params: map[string]string{"ArchiveBucketName": "orders-archive", "Env": "prod"},
		},
		{
			name: "cdk", dir: "cdk.out", file: "OrdersStack.template.json",
			functions: []string{"Handler886CB40B"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := importDir(t, filepath.Join("testdata", "cfn", tc.dir), tc.file, tc.params, tc.imports)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Build(); err != nil {
				t.Fatal(err)
			}
			req := EnvRequest{Env: tc.env, ReservedPrefixes: []string{"AWS_", "AIMAN_"}}
			for _, f := range tc.functions {
				req.Functions = append(req.Functions, Ref{Stack: "app", LogicalID: f})
			}
			if err := p.BuildEnv(req); err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(goldenView(p), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "cfn", tc.name+".golden.json")
			if *updateCFN {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -run TestCFNGoldenPlans -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("plan differs from %s (rerun with -update and review the diff):\n%s", golden, got)
			}
		})
	}
}

// cfnBase is the template the expression tests evaluate in.
const cfnBase = `
Parameters:
  Env: {Type: String, Default: test}
  Secret: {Type: String, NoEcho: true, Default: pw}
  List: {Type: CommaDelimitedList, Default: "x,y,z"}
  Vpc: {Type: "AWS::EC2::VPC::Id"}
  Ssm: {Type: "AWS::SSM::Parameter::Value<String>", Default: /some/name}
Mappings:
  M:
    test: {Size: "10", Names: [a, b]}
Conditions:
  IsTest: !Equals [!Ref Env, test]
  IsProd: !Not [!Condition IsTest]
  Loop: !Condition Loop
Resources:
  T:
    Type: AWS::DynamoDB::Table
  Q:
    Type: AWS::SQS::Queue
  R:
    Type: AWS::IAM::Role
  Gone:
    Type: AWS::S3::Bucket
    Condition: IsProd
`

// evalExpr evaluates expr (YAML) in cfnBase.
func evalExpr(t *testing.T, expr string, langExt bool) *cval {
	t.Helper()
	tmpl := cfnBase + "X: " + expr + "\n"
	if langExt {
		tmpl += "Transform: AWS::LanguageExtensions\n"
	}
	n, err := parseTemplate([]byte(tmpl))
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	p := New("aws", "eu-west-1", "http://aws:4566", allServices)
	s := &cfnStack{src: StackSource{Name: "st", Path: "t.yaml", Imports: map[string]string{"Shared": "imported"}},
		p: p, tmpl: n, condMemo: map[string]int{}, condWhy: map[string]string{}, res: map[string]*cfnResource{}}
	if err := s.load(); err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return s.eval(n.get("X"))
}

// show renders an evaluated value for comparison.
func show(v *cval) string {
	switch v.kind {
	case cvUnres:
		return "!" + v.why
	case cvList:
		var parts []string
		for _, e := range v.list {
			parts = append(parts, show(e))
		}
		return "[" + strings.Join(parts, " ") + "]"
	case cvMap:
		var parts []string
		for i, k := range v.keys {
			parts = append(parts, k+"="+show(v.vals[i]))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case cvNoValue:
		return "<novalue>"
	case cvNull:
		return "<null>"
	}
	return v.s.String()
}

func TestCFNIntrinsics(t *testing.T) {
	cases := []struct {
		expr, want string
	}{
		{"!Ref Env", "test"},
		{"{Ref: Env}", "test"},
		{"!Ref AWS::Region", "eu-west-1"},
		{"!Ref AWS::AccountId", "000000000000"},
		{"!Ref AWS::StackName", "st"},
		{"!Ref AWS::Partition", "aws"},
		{"!Ref AWS::URLSuffix", "amazonaws.com"},
		{"!Ref AWS::StackId", "arn:aws:cloudformation:eu-west-1:000000000000:stack/st/00000000-0000-0000-0000-000000000000"},
		{"!Ref AWS::NoValue", "<novalue>"},
		{"!Ref T", "${st.T}"},
		{"!Ref List", "[x y z]"},
		{"!GetAtt T.StreamArn", "${st.T.StreamArn}"},
		{"!GetAtt [Q, QueueUrl]", "${st.Q.QueueUrl}"},
		{`{"Fn::GetAtt": ["Q", "Arn"]}`, "${st.Q.Arn}"},
		{`!Sub "${Env}-${AWS::Region}-${T}-${Q.Arn}-${!Literal}"`, "test-eu-west-1-${st.T}-${st.Q.Arn}-${Literal}"},
		{`!Sub ["${A}/${Env}", {A: !Ref Q}]`, "${st.Q}/test"},
		{`!Join [":", [a, !Ref Env, !GetAtt T.Arn]]`, "a:test:${st.T.Arn}"},
		{`!Join ["", []]`, ""},
		{`!Select [1, !Ref List]`, "y"},
		{`!Select ["0", [p, q]]`, "p"},
		{`!Split [",", "a,b"]`, "[a b]"},
		{`!Select [2, !Split ["-", !Sub "${Env}-x-y"]]`, "y"},
		{`!If [IsTest, yes, no]`, "yes"},
		{`!If [IsProd, yes, !Ref AWS::NoValue]`, "<novalue>"},
		{`[a, !If [IsProd, b, !Ref AWS::NoValue], c]`, "[a c]"},
		{`{k: !If [IsProd, b, !Ref AWS::NoValue], j: 1}`, "{j=1}"},
		{`!Equals [a, a]`, "true"},
		{`!And [!Condition IsTest, !Equals [x, x]]`, "true"},
		{`!Or [!Condition IsProd, !Equals [x, y]]`, "false"},
		{`!Not [!Condition IsProd]`, "true"},
		{`!FindInMap [M, !Ref Env, Size]`, "10"},
		{`!FindInMap [M, test, Names]`, "[a b]"},
		{`!ImportValue Shared`, "imported"},
		{`!GetAZs ""`, "[eu-west-1a eu-west-1b eu-west-1c]"},
		{`!GetAZs us-east-1`, "[us-east-1a us-east-1b us-east-1c]"},
		{`!Base64 hi`, "aGk="},
		// Unresolvable: the reason, never a guess.
		{`!Ref Nope`, "!Ref Nope: Nope is not a parameter or resource of the template"},
		{`!Ref R`, "!Ref R: R is a AWS::IAM::Role, which is not emulated"},
		{`!GetAtt R.Arn`, "!Fn::GetAtt R.Arn: R is a AWS::IAM::Role, which is not emulated"},
		{`!Ref Gone`, "!Ref Gone: Gone is left out by its condition"},
		{`!Ref Vpc`, "!parameter Vpc has type AWS::EC2::VPC::Id; give it a value in the stack's parameters"},
		{`!Ref Ssm`, "!parameter Ssm has type AWS::SSM::Parameter::Value<String>; give it a value in the stack's parameters"},
		{`!ImportValue Missing`, "!Fn::ImportValue Missing has no value in the stack's imports"},
		{`!FindInMap [M, prod, Size]`, "!Fn::FindInMap: Mappings has no M.prod.Size"},
		{`!Select [5, [a]]`, "!Fn::Select index 5 is outside a list of 1"},
		{`!Split [",", !Ref Q]`, "!Fn::Split of a value that names another resource is known only once names are assigned"},
		{`!Base64 {Ref: Q}`, "!Fn::Base64 of a value that names another resource is known only once the plan is applied"},
		{`!If [Nope, a, b]`, "!condition Nope is not in the template's Conditions"},
		{`!If [Loop, a, b]`, "!condition Loop cannot be evaluated: condition Loop refers to itself"},
		{`!Equals [!Ref Q, x]`, "!Fn::Equals compares a value that names another resource or is not a string"},
		{`!Cidr [10.0.0.0/16, 2, 8]`, "!Fn::Cidr is not evaluated by aiman"},
		{`{"Fn::Transform": {Name: AWS::Include}}`, "!Fn::Transform runs a macro, which aiman does not run"},
		{`!ToJsonString {a: 1}`, "!Fn::ToJsonString needs AWS::LanguageExtensions in the template's Transform"},
		{`{"Fn::Nope": 1}`, "!Fn::Nope is not a CloudFormation function aiman evaluates"},
		{`!Join [",", [a, !ImportValue Missing]]`, "!Fn::ImportValue Missing has no value in the stack's imports"},
		{`!Sub "${Missing.Arn}"`, "!Fn::GetAtt Missing.Arn: Missing is not a parameter or resource of the template"},
	}
	for _, tc := range cases {
		if got := show(evalExpr(t, tc.expr, false)); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.expr, got, tc.want)
		}
	}
}

func TestCFNLanguageExtensions(t *testing.T) {
	if got := show(evalExpr(t, `!ToJsonString {a: 1, b: [!Ref Env, !GetAtt Q.Arn]}`, true)); got != `{"a":1,"b":["test","${st.Q.Arn}"]}` {
		t.Errorf("ToJsonString = %s", got)
	}
	if got := show(evalExpr(t, `!Length [a, b, c]`, true)); got != "3" {
		t.Errorf("Length = %s", got)
	}
}

func TestCFNSecretTaint(t *testing.T) {
	for expr, want := range map[string]bool{
		`!Sub "x-${Secret}"`:                  true,
		`!Join ["", [a, !Ref Secret]]`:        true,
		`!Base64 {Ref: Secret}`:               true,
		`!If [IsTest, !Ref Secret, b]`:        true,
		`!Sub "x-${Env}"`:                     false,
		`!Select [0, !Split [",", !Ref Env]]`: false,
	} {
		if got := evalExpr(t, expr, false).isSecret(); got != want {
			t.Errorf("%s secret = %v, want %v", expr, got, want)
		}
	}
}

func TestCFNParameters(t *testing.T) {
	tmpl := func(param string) string {
		ref := "[!Ref P]"
		if strings.Contains(param, "List") {
			ref = "!Ref P"
		}
		return "Parameters:\n  P: " + param + "\nResources:\n  Q: {Type: AWS::SQS::Queue}\nOutputs:\n  O: {Value: !Join [',', " + ref + "]}\n"
	}
	cases := []struct {
		name, param string
		params      map[string]string
		want, err   string
	}{
		{"default", "{Type: String, Default: a}", nil, "a", ""},
		{"stack value wins", "{Type: String, Default: a}", map[string]string{"P": "b"}, "b", ""},
		{"no value", "{Type: String}", nil, "", "parameter P (line 2) has no Default and no value"},
		{"allowed", "{Type: String, Default: c, AllowedValues: [a, b]}", nil, "", "is not one of AllowedValues a, b"},
		{"pattern", "{Type: String, Default: A1, AllowedPattern: '[a-z]+'}", nil, "", "does not match AllowedPattern"},
		{"min length", "{Type: String, Default: ab, MinLength: 3}", nil, "", "shorter than MinLength 3"},
		{"max length", "{Type: String, Default: abcd, MaxLength: 3}", nil, "", "longer than MaxLength 3"},
		{"number", "{Type: Number, Default: x}", nil, "", "is not a number"},
		{"min value", "{Type: Number, Default: 1, MinValue: 2}", nil, "", "below MinValue 2"},
		{"max value", "{Type: Number, Default: 9, MaxValue: 2}", nil, "", "above MaxValue 2"},
		{"noecho hidden", "{Type: String, NoEcho: true, Default: hunter2, AllowedValues: [a]}", nil, "", "(NoEcho value)"},
		{"list", "{Type: CommaDelimitedList, Default: 'a, b'}", nil, "a,b", ""},
		{"list allowed", "{Type: CommaDelimitedList, Default: 'a,c', AllowedValues: [a, b]}", nil, "", "AllowedValues"},
		{"undeclared override", "{Type: String, Default: a}", map[string]string{"Q": "x"}, "", "parameter Q is not declared"},
		{"java pattern noted", "{Type: String, Default: a, AllowedPattern: '(?=a)a'}", nil, "a", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{"t.yaml": {Data: []byte(tmpl(tc.param))}}
			p := New("aws", "eu-west-1", "http://aws:4566", allServices)
			err := ImportCloudFormation(context.Background(), StackSource{Name: "st", Path: "t.yaml", Root: fsys, Parameters: tc.params}, p)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("NoEcho value printed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			o, ok := p.Output("st", "O")
			if !ok || o.Value.String() != tc.want {
				t.Fatalf("output %+v", p.Outputs)
			}
		})
	}
}

func TestCFNLoader(t *testing.T) {
	cases := []struct {
		name, src, err string
	}{
		{"anchor", "Resources:\n  Q: &q {Type: AWS::SQS::Queue}\n  R: *q\n", "anchors and aliases"},
		{"duplicate yaml", "Resources:\n  Q: {Type: AWS::SQS::Queue}\n  Q: {Type: AWS::SQS::Queue}\n", `line 3: duplicate key "Q"`},
		{"duplicate json", "{\"Resources\": {\n\"Q\": {\"Type\": \"AWS::SQS::Queue\"},\n\"Q\": {}}}", `line 3: duplicate key "Q"`},
		{"unknown tag", "Resources:\n  Q: !Nope x\n", "unknown tag !Nope"},
		{"merge key", "A: {x: 1}\nResources:\n  Q:\n    <<: {Type: AWS::SQS::Queue}\n", "merge keys"},
		{"not a mapping", "- a\n", "not a mapping"},
		{"two documents", "Resources: {}\n---\nResources: {}\n", "more than one YAML document"},
		{"no resources", "Outputs: {}\n", "no Resources mapping"},
		{"no type", "Resources:\n  Q: {Properties: {}}\n", "resource Q (line 2) has no Type"},
		{"deep", "Resources: {}\nX: " + strings.Repeat("[", 70) + strings.Repeat("]", 70) + "\n", "nests deeper than 64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{"t.yaml": {Data: []byte(tc.src)}}
			p := New("aws", "eu-west-1", "http://aws:4566", allServices)
			err := ImportCloudFormation(context.Background(), StackSource{Name: "st", Path: "t.yaml", Root: fsys}, p)
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err %v, want %q", err, tc.err)
			}
		})
	}
	t.Run("too large", func(t *testing.T) {
		fsys := fstest.MapFS{"t.yaml": {Data: bytes.Repeat([]byte("#"), MaxTemplateBytes+1)}}
		err := ImportCloudFormation(context.Background(), StackSource{Name: "st", Path: "t.yaml", Root: fsys}, New("aws", "eu-west-1", "", allServices))
		if err == nil || !strings.Contains(err.Error(), "over 1048576 bytes") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("short forms nest", func(t *testing.T) {
		src := "Conditions:\n  C: !Equals [a, b]\nResources:\n  Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      QueueName: !If [C, !Ref \"AWS::NoValue\", !Sub \"q-${AWS::Region}\"]\n"
		n, err := parseTemplate([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		qn := n.get("Resources").get("Q").get("Properties").get("QueueName")
		if qn.keys[0] != "Fn::If" || qn.vals[0].items[1].keys[0] != "Ref" || qn.vals[0].items[2].keys[0] != "Fn::Sub" || qn.line != 7 {
			t.Fatalf("tree %+v", qn)
		}
	})
	t.Run("json lines", func(t *testing.T) {
		src := "{\n  \"Resources\": {\n    \"Q\": {\n      \"Type\": \"AWS::SQS::Queue\",\n      \"Properties\": {\"QueueName\": {\"Fn::ImportValue\": \"x\"}}\n    }\n  }\n}\n"
		p := New("aws", "eu-west-1", "", allServices)
		if err := ImportCloudFormation(context.Background(), StackSource{Name: "st", Path: "t.json", Root: fstest.MapFS{"t.json": {Data: []byte(src)}}}, p); err != nil {
			t.Fatal(err)
		}
		if len(p.Warnings) != 1 || p.Warnings[0].Line != 5 || p.Warnings[0].Attribute != "QueueName" {
			t.Fatalf("warnings %+v", p.Warnings)
		}
	})
}

// importYAML imports one template given as text.
func importYAML(t *testing.T, src string) *Plan {
	t.Helper()
	p := New("aws", "eu-west-1", "http://aws:4566", allServices)
	err := ImportCloudFormation(context.Background(), StackSource{Name: "st", Path: "t.yaml", Root: fstest.MapFS{"t.yaml": {Data: []byte(src)}}}, p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func warningsOf(p *Plan) string {
	var out []string
	for _, w := range p.Warnings {
		out = append(out, w.String())
	}
	return strings.Join(out, "\n")
}

func notesOf(p *Plan) string {
	var out []string
	for _, w := range p.Notes {
		out = append(out, w.String())
	}
	return strings.Join(out, "\n")
}

func TestCFNUnresolvable(t *testing.T) {
	cases := []struct {
		name, resource string
		// planned is whether the resource is in the plan after import.
		planned bool
		warning string
	}{
		{"table name", "T:\n    Type: AWS::DynamoDB::Table\n    Properties:\n      TableName: !ImportValue nope\n      KeySchema: [{AttributeName: k, KeyType: HASH}]\n      AttributeDefinitions: [{AttributeName: k, AttributeType: S}]",
			false, "st.T TableName: Fn::ImportValue nope has no value in the stack's imports; the resource is skipped (t.yaml:10)"},
		{"key schema", "T:\n    Type: AWS::DynamoDB::Table\n    Properties:\n      KeySchema: [{AttributeName: !ImportValue k, KeyType: HASH}]\n      AttributeDefinitions: [{AttributeName: k, AttributeType: S}]",
			false, "st.T KeySchema: Fn::ImportValue k has no value in the stack's imports; the resource is skipped (t.yaml:10)"},
		{"attribute definitions", "T:\n    Type: AWS::DynamoDB::Table\n    Properties:\n      KeySchema: [{AttributeName: k, KeyType: HASH}]\n      AttributeDefinitions: [{AttributeName: k, AttributeType: !Ref Ssm}]",
			false, "st.T AttributeDefinitions: parameter Ssm has type AWS::SSM::Parameter::Value<String>; give it a value in the stack's parameters; the resource is skipped"},
		{"gsi", "T:\n    Type: AWS::DynamoDB::Table\n    Properties:\n      KeySchema: [{AttributeName: k, KeyType: HASH}]\n      AttributeDefinitions: [{AttributeName: k, AttributeType: S}]\n      GlobalSecondaryIndexes: [{IndexName: !GetAtt Role.Arn, KeySchema: [{AttributeName: k, KeyType: HASH}]}]",
			false, "st.T GlobalSecondaryIndexes: Fn::GetAtt Role.Arn: Role is a AWS::IAM::Role, which is not emulated; the resource is skipped"},
		{"lsi", "T:\n    Type: AWS::DynamoDB::Table\n    Properties:\n      KeySchema: [{AttributeName: k, KeyType: HASH}, {AttributeName: s, KeyType: RANGE}]\n      AttributeDefinitions: [{AttributeName: k, AttributeType: S}]\n      LocalSecondaryIndexes: [{IndexName: i, KeySchema: [{AttributeName: k, KeyType: HASH}, {AttributeName: !ImportValue x, KeyType: RANGE}]}]",
			false, "st.T LocalSecondaryIndexes: Fn::ImportValue x has no value"},
		{"name names a resource", "Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      QueueName: !Sub \"${Other.QueueName}-copy\"",
			false, "st.Q QueueName: names another resource; aiman needs this property literal before it names resources; the resource is skipped"},
		{"fifo queue", "Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      FifoQueue: !ImportValue fifo",
			false, "st.Q FifoQueue: Fn::ImportValue fifo has no value in the stack's imports; the resource is skipped"},
		{"fifo topic", "Tp:\n    Type: AWS::SNS::Topic\n    Properties:\n      FifoTopic: maybe",
			false, `st.Tp FifoTopic: "maybe" is not true or false; the resource is skipped`},
		{"bucket name", "B:\n    Type: AWS::S3::Bucket\n    Properties:\n      BucketName: !Ref Vpc",
			false, "st.B BucketName: parameter Vpc has type AWS::EC2::VPC::Id"},
		{"other property dropped", "Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      VisibilityTimeout: !ImportValue vt\n      DelaySeconds: 5",
			true, "st.Q VisibilityTimeout: Fn::ImportValue vt has no value in the stack's imports; the property is left out (t.yaml:10)"},
		{"redrive to a role", "Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      RedrivePolicy: {deadLetterTargetArn: !GetAtt Role.Arn, maxReceiveCount: 2}",
			true, "st.Q RedrivePolicy: Fn::GetAtt Role.Arn: Role is a AWS::IAM::Role, which is not emulated; the property is left out"},
		{"tag", "Q:\n    Type: AWS::SQS::Queue\n    Properties:\n      Tags: [{Key: a, Value: !GetAtt Other.Arn}]",
			true, "st.Q Tags[0]: must be a literal string; the property is left out"},
		{"condition unresolvable", "Q:\n    Type: AWS::SQS::Queue\n    Condition: Unknowable",
			false, "st.Q Condition: condition Unknowable cannot be evaluated: Fn::ImportValue c has no value in the stack's imports; the resource is skipped"},
		{"lambda notification", "B:\n    Type: AWS::S3::Bucket\n    Properties:\n      NotificationConfiguration:\n        LambdaConfigurations: [{Event: 's3:ObjectCreated:*', Function: x}]\n        EventBridgeConfiguration: {EventBridgeEnabled: true}",
			true, "st.B NotificationConfiguration.LambdaConfigurations: the run has no Lambda or EventBridge; these notifications are left out"},
		{"subscription protocol", "S:\n    Type: AWS::SNS::Subscription\n    Properties:\n      TopicArn: !Ref Other\n      Protocol: lambda\n      Endpoint: !GetAtt Role.Arn",
			false, `st.S Protocol: protocol "lambda" is not emulated; only sqs subscriptions are; the subscription is skipped`},
		{"subscription endpoint", "S:\n    Type: AWS::SNS::Subscription\n    Properties:\n      TopicArn: !Ref Other\n      Protocol: sqs\n      Endpoint: !ImportValue q",
			false, "st.S Endpoint: Fn::ImportValue q has no value in the stack's imports; the subscription is skipped"},
		{"policy target", "P:\n    Type: AWS::SQS::QueuePolicy\n    Properties:\n      Queues: [https://sqs/elsewhere]\n      PolicyDocument: {Statement: []}",
			false, "st.P Queues[0]: https://sqs/elsewhere is not a planned queue of this template; the policy is left out"},
	}
	head := "Parameters:\n  Vpc: {Type: \"AWS::EC2::VPC::Id\"}\n  Ssm: {Type: \"AWS::SSM::Parameter::Value<String>\", Default: /x}\nConditions:\n  Unknowable: !Equals [!ImportValue c, x]\nResources:\n  "
	tail := "\n  Other:\n    Type: AWS::SQS::Queue\n  Role:\n    Type: AWS::IAM::Role\n"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := importYAML(t, head+tc.resource+tail)
			id := strings.SplitN(tc.resource, ":", 2)[0]
			if got := p.Resource("st."+id) != nil; got != tc.planned {
				t.Errorf("planned = %v, want %v", got, tc.planned)
			}
			if !strings.Contains(warningsOf(p), tc.warning) {
				t.Errorf("warnings:\n%s\nwant %q", warningsOf(p), tc.warning)
			}
		})
	}
}

func TestCFNResources(t *testing.T) {
	src := `Resources:
  GT:
    Type: AWS::DynamoDB::GlobalTable
    Properties:
      TableName: global
      BillingMode: PAY_PER_REQUEST
      KeySchema: [{AttributeName: k, KeyType: HASH}]
      AttributeDefinitions: [{AttributeName: k, AttributeType: S}]
      StreamSpecification: {StreamViewType: NEW_AND_OLD_IMAGES}
      Replicas:
        - Region: eu-west-1
          TableClass: STANDARD_INFREQUENT_ACCESS
          Tags: [{Key: r, Value: local}]
        - Region: us-east-1
  ST:
    Type: AWS::Serverless::SimpleTable
    Properties:
      PrimaryKey: {Name: n, Type: Number}
      ProvisionedThroughput: {ReadCapacityUnits: 3, WriteCapacityUnits: 4}
  Fifo:
    Type: AWS::SNS::Topic
    Properties:
      FifoTopic: "true"
      ContentBasedDeduplication: true
      Tags: [{Key: a, Value: b}]
  Lock:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: lock-bucket
      ObjectLockEnabled: true
      ObjectLockConfiguration: {ObjectLockEnabled: Enabled}
      LifecycleConfiguration: {Rules: []}
      Tags: [{Key: k, Value: v}]
`
	p := importYAML(t, src)
	gt := p.Resource("st.GT")
	if gt == nil || gt.Table.TableClass != "STANDARD_INFREQUENT_ACCESS" || gt.Table.Tags["r"] != "local" || gt.Table.StreamViewType != "NEW_AND_OLD_IMAGES" {
		t.Fatalf("global table %+v", gt)
	}
	if !strings.Contains(warningsOf(p), "st.GT Replicas: only the replica in the run's region is created") {
		t.Errorf("warnings %s", warningsOf(p))
	}
	st := p.Resource("st.ST").Table
	if st.HashKey != "n" || st.Attributes[0].Type != "N" || st.BillingMode != "PROVISIONED" || st.ReadCapacity != 3 || st.WriteCapacity != 4 {
		t.Fatalf("simple table %+v", st)
	}
	f := p.Resource("st.Fifo").Topic
	if !f.Fifo || f.Attributes["ContentBasedDeduplication"].String() != "true" || f.Tags["a"] != "b" {
		t.Fatalf("topic %+v", f)
	}
	lock := p.Resource("st.Lock")
	if !lock.Bucket.ObjectLock || lock.Name != "lock-bucket" || lock.Bucket.Tags["k"] != "v" {
		t.Fatalf("bucket %+v", lock.Bucket)
	}
	notes := notesOf(p)
	for _, want := range []string{
		"st.Lock ObjectLockConfiguration: aiman does not apply this configuration in the emulator yet; left out",
		"st.Lock LifecycleConfiguration: aiman does not apply this configuration",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes:\n%s\nwant %q", notes, want)
		}
	}
	if err := p.Build(); err != nil {
		t.Fatal(err)
	}
	if p.Resource("st.Fifo").Physical != GeneratedName(KindTopic, SourceCloudFormation, "st", "Fifo", "", true) {
		t.Errorf("fifo topic name %s", p.Resource("st.Fifo").Physical)
	}
}

func TestCFNSAMGlobals(t *testing.T) {
	src := `Transform: [AWS::Serverless-2016-10-31]
Globals:
  Function:
    Environment:
      Variables: {A: global, B: global}
    Layers: [l1]
Resources:
  Fn:
    Type: AWS::Serverless::Function
    Properties:
      Environment:
        Variables: {B: local, C: !Ref AWS::Region}
      Layers: [l2]
  Plain:
    Type: AWS::Lambda::Function
    Properties:
      Environment:
        Variables: {D: d}
`
	p := importYAML(t, src)
	fe, ok := p.Function("st", "Fn")
	if !ok {
		t.Fatal("no function env")
	}
	got := map[string]string{}
	for _, v := range fe.Vars {
		got[v.Name] = v.Value.String()
	}
	if got["A"] != "global" || got["B"] != "local" || got["C"] != "eu-west-1" || len(got) != 3 {
		t.Fatalf("vars %v", got)
	}
	n := samMerge(mustParse(t, "Layers: [l1]"), mustParse(t, "Layers: [l2]"))
	if l := n.get("Layers"); len(l.items) != 2 || l.items[0].text != "l1" {
		t.Fatalf("lists are concatenated: %+v", l)
	}
	if fe, ok := p.Function("st", "Plain"); !ok || fe.Vars[0].Name != "D" {
		t.Fatalf("plain lambda %+v", fe)
	}
	if !strings.Contains(notesOf(p), "not emulated: AWS::Lambda::Function (Plain)") || !strings.Contains(notesOf(p), "not emulated: AWS::Serverless::Function (Fn)") {
		t.Errorf("notes %s", notesOf(p))
	}
}

func mustParse(t *testing.T, src string) *cfnNode {
	t.Helper()
	n, err := parseTemplate([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCFNOtherTransformsAndNestedStacks(t *testing.T) {
	src := `Transform: [MyMacro]
Resources:
  Child:
    Type: AWS::CloudFormation::Stack
    Properties: {TemplateURL: ./child.yaml}
  App:
    Type: AWS::Serverless::Application
    Properties: {Location: ./app.yaml}
`
	p := importYAML(t, src)
	if !strings.Contains(warningsOf(p), "transform MyMacro is a macro aiman does not run") {
		t.Errorf("warnings %s", warningsOf(p))
	}
	notes := notesOf(p)
	if !strings.Contains(notes, "st.Child: nested stack (AWS::CloudFormation::Stack) is not imported") || !strings.Contains(notes, "st.App: nested stack (AWS::Serverless::Application)") {
		t.Errorf("notes %s", notes)
	}
}

func TestCFNRegistered(t *testing.T) {
	if _, ok := ImporterFor(SourceCloudFormation); !ok {
		t.Fatal("the CloudFormation importer is not registered")
	}
}
