package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// awsCFNFile points an aws service at a SAM template; the scenario
// overrides a parameter.
const awsCFNFile = `version: 1
app:
  run: exec sleep 600
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1
      stacks:
        - {name: app, cloudformation: infra/template.yaml, parameters: {Stage: test}}
      env:
        EVENTS_TOPIC: app.outputs.TopicArn
        UPLOADS_QUEUE_URL: app.UploadQueue.QueueUrl
      function_env: [app.Recorder]
scenarios:
  - id: sam
    services: [aws]
    aws:
      aws:
        parameters: {app: {Stage: e2e}}
    steps: [{do: look}]
    checks:
      - {name: boto, run: "true", image: "` + MiniStackImage + `"}
`

// awsSAMTemplate is a SAM app: a SimpleTable and a function whose
// environment names it, a bucket that notifies a queue, and a topic with
// an SQS subscription.
const awsSAMTemplate = `Transform: AWS::Serverless-2016-10-31
Parameters:
  Stage: {Type: String, AllowedValues: [test, e2e]}
Globals:
  Function:
    Environment:
      Variables:
        STAGE: !Ref Stage
Resources:
  Records:
    Type: AWS::Serverless::SimpleTable
    Properties:
      TableName: !Sub "records-${Stage}"
      PrimaryKey: {Name: key, Type: String}
  UploadQueue:
    Type: AWS::SQS::Queue
  UploadQueuePolicy:
    Type: AWS::SQS::QueuePolicy
    Properties:
      Queues: [!Ref UploadQueue]
      PolicyDocument:
        Statement:
          - {Effect: Allow, Principal: {Service: [s3.amazonaws.com, sns.amazonaws.com]}, Action: sqs:SendMessage, Resource: !GetAtt UploadQueue.Arn}
  Uploads:
    Type: AWS::S3::Bucket
    DependsOn: UploadQueuePolicy
    Properties:
      VersioningConfiguration: {Status: Enabled}
      NotificationConfiguration:
        QueueConfigurations:
          - Event: s3:ObjectCreated:*
            Queue: !GetAtt UploadQueue.Arn
  Events:
    Type: AWS::SNS::Topic
    Properties:
      Subscription:
        - {Protocol: sqs, Endpoint: !GetAtt UploadQueue.Arn}
  Recorder:
    Type: AWS::Serverless::Function
    Properties:
      Handler: app.handler
      Runtime: python3.13
      CodeUri: src/
      Environment:
        Variables:
          RECORDS_TABLE: !Ref Records
          UPLOADS_BUCKET: !Ref Uploads
Outputs:
  TopicArn:
    Value: !Ref Events
`

func TestAWSPlanCFNImport(t *testing.T) {
	repo := awsRepo(t, awsCFNFile, map[string]string{"infra/template.yaml": awsSAMTemplate})
	tm := newTestManager(t)
	_, plans, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "sam"})
	if err != nil {
		t.Fatal(err)
	}
	v := plans[0]
	if len(v.Resources) != 5 {
		t.Fatalf("resources %+v", v.Resources)
	}
	if v.Env["RECORDS_TABLE"] != "records-e2e" || v.Env["STAGE"] != "e2e" || !strings.HasPrefix(v.Env["UPLOADS_BUCKET"], "app-uploads-") ||
		!strings.HasPrefix(v.Env["EVENTS_TOPIC"], "arn:aws:sns:eu-west-1:000000000000:app-Events-") ||
		!strings.HasPrefix(v.Env["UPLOADS_QUEUE_URL"], "http://aws:4566/000000000000/app-UploadQueue-") {
		t.Fatalf("env %v", v.Env)
	}
	if len(v.Warnings) != 0 {
		t.Fatalf("warnings %v", v.Warnings)
	}
	// Without the scenario the stack's own parameter applies.
	_, all, err := tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo})
	if err != nil || all[0].Env["RECORDS_TABLE"] != "records-test" {
		t.Fatalf("all %v %+v", err, all)
	}
	// A value outside AllowedValues refuses the plan.
	bad := strings.Replace(awsCFNFile, "Stage: e2e", "Stage: prod", 1)
	repo = awsRepo(t, bad, map[string]string{"infra/template.yaml": awsSAMTemplate})
	_, _, err = tm.AWSPlans(context.Background(), AWSPlanRequest{Repo: repo, Scenario: "sam"})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid || !strings.Contains(err.Error(), "parameter Stage") || !strings.Contains(err.Error(), "AllowedValues test, e2e") {
		t.Fatalf("err %v", err)
	}
}
