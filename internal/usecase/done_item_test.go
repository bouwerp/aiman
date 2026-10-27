package usecase

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type doneRemote struct {
	*restartRemote
	out string
}

func (d *doneRemote) Execute(context.Context, string) (string, error) { return d.out, nil }

func TestRemoteDoneItemsToleratesOldServe(t *testing.T) {
	r := &doneRemote{restartRemote: &restartRemote{}, out: `unknown command "rpc"`}
	if err := (RemoteDoneItems{}).Ensure(context.Background(), r, domain.Session{ID: "s1"}); err != nil {
		t.Fatalf("Ensure = %v, want nil for an old serve", err)
	}
}
