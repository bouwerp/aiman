package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/limits"
)

type limitsStub struct{}

func (limitsStub) Name() string           { return "stub" }
func (limitsStub) MinAge() time.Duration  { return time.Hour }
func (limitsStub) Timeout() time.Duration { return time.Second }
func (limitsStub) Read(context.Context) limits.Reading {
	return limits.Reading{Provider: "stub", State: limits.StateOK, Plan: "plus"}
}

func TestLimitsGetReturnsReadings(t *testing.T) {
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln, testRepo(t), nil, nil, nil, nil, "test")
	s.SetLimits(limits.NewManager([]limits.Source{limitsStub{}}, time.Now))
	var last map[string]json.RawMessage
	for range 200 {
		resp := s.dispatch(context.Background(), []byte(`{"id":"1","method":"limits.get","params":{"refresh":false}}`))
		if resp.Error != nil {
			t.Fatalf("error %+v", resp.Error)
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		last = nil
		if err := json.Unmarshal(raw, &last); err != nil {
			t.Fatal(err)
		}
		if string(last["type"]) != `"limits"` {
			t.Fatalf("type %s", last["type"])
		}
		if string(last["refreshing"]) == "false" {
			var rs []limits.Reading
			if err := json.Unmarshal(last["readings"], &rs); err != nil {
				t.Fatal(err)
			}
			if len(rs) != 1 || rs[0].Plan != "plus" {
				t.Fatalf("readings %+v", rs)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never settled: %v", last)
}

func TestLimitsGetWithoutAManagerIsUnavailable(t *testing.T) {
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln, testRepo(t), nil, nil, nil, nil, "test")
	resp := s.dispatch(context.Background(), []byte(`{"id":"1","method":"limits.get"}`))
	if resp.Error == nil || resp.Error.Code == CodeInvalidParams {
		t.Fatalf("response %+v: want an error other than unknown method", resp)
	}
}
