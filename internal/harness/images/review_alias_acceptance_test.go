package images_test

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
	"github.com/bouwerp/aiman/internal/harness/images"
)

// Pointer types are assignable only when the types are identical, so these
// hold only while the image review's names are aliases of agentjob's.
var (
	_ *agentjob.Container     = (*images.ReviewContainer)(nil)
	_ *images.ReviewContainer = (*agentjob.Container)(nil)
	_ *agentjob.DockerRunner  = (*images.DockerReviewRunner)(nil)
	_ *agentjob.Credential    = (*images.Credential)(nil)
	_ agentjob.Runner         = images.DockerReviewRunner{}
	_ images.ReviewRunner     = agentjob.DockerRunner{}
)

// jobRunner is an agentjob.Runner; the image review takes it unchanged.
type jobRunner struct{ runs []agentjob.Container }

func (r *jobRunner) Run(_ context.Context, c agentjob.Container) (string, error) {
	r.runs = append(r.runs, c)
	return "", nil
}

func TestReviewNamesAreAgentJobAliases(t *testing.T) {
	r := &jobRunner{}
	var rr images.ReviewRunner = r
	if _, err := rr.Run(context.Background(), images.ReviewContainer{Name: "aiman-review-x"}); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != 1 || r.runs[0].Name != "aiman-review-x" {
		t.Fatalf("runs = %+v", r.runs)
	}
	o := images.ReviewOptions{Runner: r, Credentials: func(string, time.Duration) (agentjob.Credential, error) { return agentjob.Credential{}, nil }}
	if o.Runner == nil || o.Credentials == nil {
		t.Fatal("ReviewOptions does not take agentjob's runner and credentials")
	}
}
