package mutagen

import (
	"reflect"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestParseSyncListOutput(t *testing.T) {
	output := `
Name: session-1
Identifier: id-1
Labels:
    session: PP12PB-715-Daily-Points-Multipliers-Position-Service-Implementa
    aiman-id: 1234-5678-90ab-cdef
Alpha:
    URL: /local/path
Beta:
    URL: user@host:/remote/path
Status: Watching for changes
--------------------------------------------------------------------------------
Name: session-2
Identifier: id-2
Alpha:
    URL: /local/path2
Beta:
    URL: user@host:/remote/path2
Status: Watching for changes
`
	e := &Engine{}
	sessions := e.parseSyncListOutput(output)

	expected := []domain.SyncSession{
		{
			Name: "session-1",
			ID:   "id-1",
			Labels: map[string]string{
				"session":  "PP12PB-715-Daily-Points-Multipliers-Position-Service-Implementa",
				"aiman-id": "1234-5678-90ab-cdef",
			},
			LocalPath:  "/local/path",
			RemotePath: "user@host:/remote/path",
			Status:     "Watching for changes",
		},
		{
			Name:       "session-2",
			ID:         "id-2",
			LocalPath:  "/local/path2",
			RemotePath: "user@host:/remote/path2",
			Status:     "Watching for changes",
		},
	}

	if !reflect.DeepEqual(sessions, expected) {
		t.Errorf("expected %v, got %v", expected, sessions)
	}
}

func TestPostProcessMutagenSessions_RemoteEndpoint(t *testing.T) {
	sessions := []domain.SyncSession{{
		LocalPath:  "/Users/dev/.aiman/work/uuid",
		RemotePath: "code@regent0:/home/code/repos/proj",
	}}
	postProcessMutagenSessions(sessions)
	if sessions[0].RemotePath != "/home/code/repos/proj" {
		t.Errorf("RemotePath = %q", sessions[0].RemotePath)
	}
	if sessions[0].RemoteEndpoint != "code@regent0" {
		t.Errorf("RemoteEndpoint = %q", sessions[0].RemoteEndpoint)
	}
}
