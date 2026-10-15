package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/chathostbudget"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
	if !shouldRequeueWake(sessionmanager.ErrChatHostQueued) {
		t.Fatal("soft capacity deferral must stay queued")
	}
}

type stubPressureHibernator struct {
	ids []string
}

func (s *stubPressureHibernator) HibernateChatForPressure(_ context.Context, id domain.SessionID) (bool, error) {
	s.ids = append(s.ids, string(id))
	return false, nil
}

func TestReclaimSkipsAuxHostsWithoutDataDir(t *testing.T) {
	alive := map[string]bool{
		"review-1": true,
		"worker-1": true,
	}
	b := chathostbudget.New(chathostbudget.Caps{
		MaxTotal: 1, MaxOrchestrator: 1, MaxWorker: 1,
	}, func(id string) bool { return alive[id] }, nil)
	b.MarkSeeded()
	if _, err := b.SoftAdmitWake("worker-1", chathostbudget.ClassWorker, "p"); err != nil {
		t.Fatal(err)
	}
	b.Enqueue(chathostbudget.QueuedWake{HostID: "orch-waiting", ProjectID: "p", Class: chathostbudget.ClassOrchestrator})
	if !b.ShouldReclaim() {
		t.Fatal("blocked queue must reclaim")
	}
	hib := &stubPressureHibernator{}
	// No dataDir: must not panic; worker hibernate attempted; aux left held.
	reclaimChatHostsUnderPressure(context.Background(), b, hib, "", nil)
	if len(hib.ids) != 1 || hib.ids[0] != "worker-1" {
		t.Fatalf("hibernate targets = %v", hib.ids)
	}
	if _, ok := b.Snapshot().Held["review-1"]; ok {
		t.Fatal("review was never admitted")
	}
	// Admit a review into the budget and prove empty dataDir leaves it held.
	b.ForceRelease("worker-1")
	if reserved, err := b.SoftAdmitWake("review-1", chathostbudget.ClassReview, "p"); err != nil || !reserved {
		t.Fatalf("admit review: reserved=%v err=%v", reserved, err)
	}
	b.Enqueue(chathostbudget.QueuedWake{HostID: "orch-waiting", ProjectID: "p", Class: chathostbudget.ClassOrchestrator})
	hib.ids = nil
	reclaimChatHostsUnderPressure(context.Background(), b, hib, "", nil)
	if _, ok := b.Snapshot().Held["review-1"]; !ok {
		t.Fatal("aux host must stay held without dataDir shutdown path")
	}
}
