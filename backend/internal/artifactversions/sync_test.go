package artifactversions

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSnapshotRecordsAnEditStillInsideTheSettleWindow(t *testing.T) {
	h := newHarness(t)
	h.write("plan.md", "first")
	h.tick()
	h.tick()
	h.write("plan.md", "second")
	h.cap.now = func() time.Time { return h.mtime.Add(SettleWindow / 2) }

	if err := h.cap.Snapshot(h.ctx, h.sess, "plan.md"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	vs := h.versions("plan.md")
	if len(vs) != 2 || vs[0].Source != domain.ArtifactVersionSourceAgent {
		t.Fatalf("versions = %+v, want the fresh edit kept as a second, agent-written one", vs)
	}
}

func TestSyncRecordsASettledEditWithoutWaitingForTheObserver(t *testing.T) {
	h := newHarness(t)
	h.write("plan.md", "first")
	h.tick()
	h.tick()
	h.write("plan.md", "second")

	// The file was just written, so it may still be in flux.
	h.cap.now = func() time.Time { return h.mtime.Add(SettleWindow / 2) }
	if err := h.cap.Sync(h.ctx, h.sess, "plan.md"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(h.versions("plan.md")); got != 1 {
		t.Fatalf("versions while unsettled = %d, want 1", got)
	}

	h.cap.now = func() time.Time { return h.mtime.Add(2 * SettleWindow) }
	if err := h.cap.Sync(h.ctx, h.sess, "plan.md"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	vs := h.versions("plan.md")
	if len(vs) != 2 || vs[0].Source != domain.ArtifactVersionSourceAgent {
		t.Fatalf("versions after settling = %+v, want a second, agent-written one", vs)
	}

	// Syncing again, or an observer tick, adds nothing.
	if err := h.cap.Sync(h.ctx, h.sess, "plan.md"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	h.tick()
	h.tick()
	if got := len(h.versions("plan.md")); got != 2 {
		t.Fatalf("versions after repeats = %d, want 2", got)
	}
}
