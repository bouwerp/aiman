package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func TestFormatAWSLogLine(t *testing.T) {
	at := time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		kind    domain.EventKind
		payload string
		want    string
	}{
		{
			name:    "started",
			kind:    domain.EventAWSLoginStarted,
			payload: `{"profile":"dev","state":"pending"}`,
			want:    "2026-11-08T09:30:00Z aws.login_started profile=dev state=pending",
		},
		{
			name:    "finished with failure",
			kind:    domain.EventAWSLoginFinished,
			payload: `{"profile":"dev","state":"failed","failure":"write_failed"}`,
			want:    "2026-11-08T09:30:00Z aws.login_finished profile=dev state=failed failure=write_failed",
		},
		{
			name:    "finished without failure",
			kind:    domain.EventAWSLoginFinished,
			payload: `{"profile":"prod","state":"done"}`,
			want:    "2026-11-08T09:30:00Z aws.login_finished profile=prod state=done",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := domain.SessionEvent{SessionID: domain.AWSLoginLedger, Seq: 1, At: at, Kind: tc.kind, Payload: json.RawMessage(tc.payload)}
			if got := formatAWSLogLine(ev); got != tc.want {
				t.Fatalf("line = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunAWSLogPassesFlagsAndPrintsLines(t *testing.T) {
	at := time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	var gotParams map[string]any
	call := func(params map[string]any) (server.Response, error) {
		gotParams = params
		return server.Response{Result: map[string]any{
			"type": "aws_log",
			"events": []domain.SessionEvent{{
				SessionID: domain.AWSLoginLedger, Seq: 4, At: at, Kind: domain.EventAWSLoginStarted,
				Payload: json.RawMessage(`{"profile":"dev","state":"pending"}`),
			}},
		}}, nil
	}
	var out bytes.Buffer
	if err := runAWSLog([]string{"--since", "3", "--limit", "10"}, call, &out); err != nil {
		t.Fatal(err)
	}
	if gotParams["since_seq"] != int64(3) || gotParams["limit"] != 10 {
		t.Fatalf("params = %v", gotParams)
	}
	if want := "2026-11-08T09:30:00Z aws.login_started profile=dev state=pending\n"; out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := runAWSLog([]string{"--json"}, call, &out); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Type != "aws_log" {
		t.Fatalf("json out = %q (%v)", out.String(), err)
	}
	if len(gotParams) != 0 {
		t.Fatalf("params without flags = %v, want none", gotParams)
	}
}
