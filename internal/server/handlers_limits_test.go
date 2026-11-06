package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
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

type namedLimits struct {
	limitsStub
	name string
}

func (n namedLimits) Name() string { return n.name }

// The phone reads a session's limits here: session.get is on the device
// allowlist and limits.get is not.
func TestSessionGetCarriesTheLimitsOfItsProvider(t *testing.T) {
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo := testRepo(t)
	ctx := context.Background()
	for _, sess := range []domain.Session{
		{ID: "a", Name: "with", AgentName: "grok", Status: domain.SessionStatusActive},
		{ID: "b", Name: "without", AgentName: "agy", Status: domain.SessionStatusActive},
	} {
		if err := repo.Save(ctx, &sess); err != nil {
			t.Fatal(err)
		}
	}
	s := New(ln, repo, nil, nil, nil, nil, "test")
	manager := limits.NewManager([]limits.Source{namedLimits{name: "grok"}}, time.Now)
	defer manager.Close()
	s.SetLimits(manager)

	get := func(id string) SessionInfo {
		t.Helper()
		resp := s.dispatch(ctx, []byte(`{"id":"1","method":"session.get","params":{"id":"`+id+`"}}`))
		if resp.Error != nil {
			t.Fatalf("error %+v", resp.Error)
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Session SessionInfo `json:"session"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		return body.Session
	}
	for range 200 {
		if got := get("a").Limits; got != nil {
			if got.Plan != "plus" {
				t.Fatalf("limits %+v", got)
			}
			if other := get("b").Limits; other != nil {
				t.Fatalf("an agent with no source got %+v", other)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session.get never carried limits")
}

func TestSessionGetWithoutAManagerOmitsLimits(t *testing.T) {
	var s Server
	if got := s.sessionLimits(domain.Session{AgentName: "grok"}); got != nil {
		t.Fatalf("limits %+v", got)
	}
}
