package chathostbudget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryAcquireRespectsCapsAndPressure(t *testing.T) {
	alive := map[string]bool{}
	var avail uint64 = 16 << 30
	b := New(DefaultCaps(), func(id string) bool { return alive[id] }, func() (uint64, error) { return avail, nil })
	b.MarkSeededForTest()

	for i, id := range []string{"w1", "w2", "w3", "w4"} {
		if err := b.TryAcquireSpawn(id, ClassWorker); err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		alive[id] = true
	}
	if err := b.TryAcquireSpawn("w5", ClassWorker); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("5th worker = %v, want ErrNoCapacity", err)
	}
	if err := b.TryAcquireSpawn("o1", ClassOrchestrator); err != nil {
		t.Fatal(err)
	}
	alive["o1"] = true
	if err := b.TryAcquireSpawn("o2", ClassOrchestrator); err != nil {
		t.Fatal(err)
	}
	alive["o2"] = true
	// total 6 — next orch or worker blocked
	if err := b.TryAcquireSpawn("o3", ClassOrchestrator); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("3rd orch = %v", err)
	}

	avail = 4 << 30 // enter pressure
	alive["w1"] = false
	if !b.ReleaseIfDead("w1") {
		t.Fatal("expected release")
	}
	// pressure caps: total 3, already hold o1,o2,w2,w3,w4 = 5 until we release more
	for _, id := range []string{"w2", "w3", "w4"} {
		alive[id] = false
		b.ReleaseIfDead(id)
	}
	// held: o1, o2 (2). pressure orch max 1 — cannot acquire another orch; worker ok up to pressure worker 2
	if err := b.TryAcquireSpawn("o3", ClassOrchestrator); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("orch under pressure = %v", err)
	}
	if err := b.TryAcquireSpawn("w6", ClassWorker); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptDoesNotDoubleCharge(t *testing.T) {
	b := New(DefaultCaps(), func(string) bool { return true }, nil)
	b.MarkSeededForTest()
	b.NoteAdopt("s1", ClassWorker)
	if err := b.TryAcquireSpawn("s1", ClassWorker); err != nil {
		t.Fatal(err)
	}
	if got := b.Snapshot(); len(got.Held) != 1 {
		t.Fatalf("held = %v", got.Held)
	}
}

func TestAcquireSurvivesFailedAttachSemantics(t *testing.T) {
	// Spawn reserved; PID still alive after attach fail → ReleaseIfDead no-ops.
	alive := map[string]bool{}
	b := New(DefaultCaps(), func(id string) bool { return alive[id] }, nil)
	b.MarkSeededForTest()
	if err := b.TryAcquireSpawn("orphan", ClassWorker); err != nil {
		t.Fatal(err)
	}
	alive["orphan"] = true
	if b.ReleaseIfDead("orphan") {
		t.Fatal("must not release while PID alive")
	}
	if err := b.TryAcquireSpawn("other", ClassWorker); err != nil {
		t.Fatal(err)
	}
	alive["orphan"] = false
	if !b.ReleaseIfDead("orphan") {
		t.Fatal("expected release after death")
	}
}

func TestForceReleaseAfterFailedSpawnBeforePID(t *testing.T) {
	b := New(DefaultCaps(), func(string) bool { return false }, nil)
	b.MarkSeededForTest()
	_ = b.TryAcquireSpawn("ghost", ClassWorker)
	b.ForceRelease("ghost")
	if err := b.TryAcquireSpawn("next", ClassWorker); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueDedupAndFairDrain(t *testing.T) {
	alive := map[string]bool{}
	b := New(Caps{
		MaxTotal: 2, MaxOrchestrator: 1, MaxWorker: 2,
		PressureTotal: 2, PressureOrch: 1, PressureWorker: 2,
		EnterAvailableB: 1, ExitAvailableB: 2,
	}, func(id string) bool { return alive[id] }, func() (uint64, error) { return 100 << 30, nil })
	b.MarkSeededForTest()

	b.Enqueue(QueuedWake{HostID: "a1", ProjectID: "A", Class: ClassWorker})
	b.Enqueue(QueuedWake{HostID: "b1", ProjectID: "B", Class: ClassWorker})
	b.Enqueue(QueuedWake{HostID: "a1", ProjectID: "A", Class: ClassWorker}) // dedup
	if b.Snapshot().Queued != 2 {
		t.Fatalf("queued = %d", b.Snapshot().Queued)
	}
	got := b.Drain(context.Background())
	if len(got) != 2 {
		t.Fatalf("admitted = %v", got)
	}
	for _, w := range got {
		alive[w.HostID] = true
	}
	b.Enqueue(QueuedWake{HostID: "a2", ProjectID: "A", Class: ClassWorker})
	if len(b.Drain(context.Background())) != 0 {
		t.Fatal("expected capacity full")
	}
	alive["a1"] = false
	b.ReleaseIfDead("a1")
	got = b.Drain(context.Background())
	if len(got) != 1 || got[0].HostID != "a2" {
		t.Fatalf("drain after free = %v", got)
	}
}

func TestHysteresis(t *testing.T) {
	var avail uint64 = 10 << 30
	b := New(DefaultCaps(), func(string) bool { return false }, func() (uint64, error) { return avail, nil })
	b.MarkSeededForTest()
	_ = b.TryAcquireSpawn("w1", ClassWorker)
	if b.Snapshot().Pressure {
		t.Fatal("should not be in pressure yet")
	}
	avail = 5 << 30
	_ = b.TryAcquireSpawn("w2", ClassWorker)
	if !b.Snapshot().Pressure {
		t.Fatal("expected pressure enter")
	}
	avail = 7 << 30 // between enter and exit — stay in pressure
	b.ReleaseIfDead("w1")
	_ = b.TryAcquireSpawn("w3", ClassWorker)
	if !b.Snapshot().Pressure {
		t.Fatal("hysteresis must hold until exit threshold")
	}
	avail = 10 << 30
	b.ReleaseIfDead("w2")
	_ = b.TryAcquireSpawn("w4", ClassWorker)
	if b.Snapshot().Pressure {
		t.Fatal("expected pressure exit")
	}
}

func TestSeedFromDisk(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"live", "dead"} {
		if err := os.MkdirAll(filepath.Join(dir, "chat-hosts", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	alive := map[string]bool{"live": true}
	b := New(DefaultCaps(), func(id string) bool { return alive[id] }, nil)
	if err := b.Seed(dir); err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot()
	if !snap.Seeded || len(snap.Held) != 1 {
		t.Fatalf("snap = %+v", snap)
	}
	if _, ok := snap.Held["live"]; !ok {
		t.Fatal("missing live")
	}
}

func TestClassify(t *testing.T) {
	if Classify("review-1", false) != ClassReview {
		t.Fatal("review")
	}
	if Classify("btw-run-x", false) != ClassSide {
		t.Fatal("side")
	}
	if Classify("sess", true) != ClassOrchestrator {
		t.Fatal("orch")
	}
	if Classify("sess", false) != ClassWorker {
		t.Fatal("worker")
	}
}

func TestNotSeededBlocksSpawn(t *testing.T) {
	b := New(DefaultCaps(), func(string) bool { return false }, nil)
	if err := b.TryAcquireSpawn("x", ClassWorker); !errors.Is(err, ErrNotSeeded) {
		t.Fatalf("got %v", err)
	}
}
