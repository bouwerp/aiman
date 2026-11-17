package images

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDecideTable(t *testing.T) {
	cases := []struct {
		name       string
		scan       Scan
		in         DecideInput
		want       State
		approvedBy string
	}{
		{"critical blocks", Scan{Critical: []string{"CVE-1"}, ModelVerdict: VerdictClean}, DecideInput{Allowed: true, Operator: true}, StateBlocked, ""},
		{"high blocks at cve_block high", Scan{High: []string{"CVE-2"}}, DecideInput{CVEBlock: BlockHigh}, StateBlocked, ""},
		{"static hit blocks", Scan{StaticHits: []string{"clamav:Eicar /x"}, ModelVerdict: VerdictClean}, DecideInput{Operator: true}, StateBlocked, ""},
		{"MAL id blocks", Scan{MalIDs: []string{"MAL-2024-1"}}, DecideInput{}, StateBlocked, ""},
		{"malicious verdict blocks", Scan{ModelVerdict: VerdictMalicious}, DecideInput{Allowed: true}, StateBlocked, ""},
		{"high asks", Scan{High: []string{"CVE-3"}, ModelVerdict: VerdictClean}, DecideInput{Allowed: true, Operator: true}, StatePendingApproval, ""},
		{"suspicious asks the operator", Scan{ModelVerdict: VerdictSuspicious}, DecideInput{Operator: true}, StatePendingApproval, ""},
		{"listed clean approved", Scan{ModelVerdict: VerdictClean}, DecideInput{Allowed: true}, StateApproved, ApprovedByConfig},
		{"listed without verdict asks a session", Scan{}, DecideInput{Allowed: true}, StatePendingApproval, ""},
		{"operator run without verdict approved", Scan{}, DecideInput{Operator: true}, StateApproved, ApprovedByOperatorRun},
		{"unlisted clean session run asks", Scan{ModelVerdict: VerdictClean}, DecideInput{}, StatePendingApproval, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.scan, c.in)
			if d.State != c.want || d.ApprovedBy != c.approvedBy {
				t.Fatalf("Decide = %+v, want %s by %q", d, c.want, c.approvedBy)
			}
			if c.want == StateBlocked && len(d.Reasons) == 0 {
				t.Fatal("a blocked decision carries reasons")
			}
		})
	}
}

func TestStoreUpdateAndList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "h")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("sha256:aa"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Get on an empty store = %v", err)
	}
	now := time.Now()
	for i, d := range []string{"sha256:aa", "sha256:bb"} {
		if _, err := s.Update(d, func(r *Record, ok bool) error {
			if ok {
				t.Fatal("new digest reported as present")
			}
			r.Ref, r.State, r.At = "x", StateVetting, now.Add(time.Duration(i)*time.Second)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Update("sha256:aa", func(*Record, bool) error { return errSkip }); err != nil {
		t.Fatalf("skip: %v", err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Digest != "sha256:bb" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	again, _ := NewStore(dir)
	if r, err := again.Get("sha256:aa"); err != nil || r.State != StateVetting {
		t.Fatalf("reopened Get = %+v, %v", r, err)
	}
}
