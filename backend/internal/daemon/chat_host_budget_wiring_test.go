package daemon

import (
	"errors"
	"testing"

	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

func TestShouldRequeueWakeDropsPermanentErrors(t *testing.T) {
	if shouldRequeueWake(nil) {
		t.Fatal("nil")
	}
	for _, err := range []error{
		sessionmanager.ErrNotFound,
		sessionmanager.ErrTerminated,
		sessionmanager.ErrNotResumable,
		errors.Join(errors.New("wake failed"), sessionmanager.ErrNotFound),
	} {
		if shouldRequeueWake(err) {
			t.Fatalf("must drop permanent error: %v", err)
		}
	}
	if !shouldRequeueWake(errors.New("temporary glitch")) {
		t.Fatal("transient must requeue")
	}
}
