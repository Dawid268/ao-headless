// Package chathostbudget caps concurrent warm chat-host processes so one
// machine (laptop or headless VPS) can host many sessions without OOM thrash.
//
// Warm identity is a live host directory under chat-hosts/ (session, review-*,
// btw-*, background-*), not a SQLite session row. Adopt of an already-alive
// host does not consume a new slot; spawn does. Slots are held until the host
// PID is proven dead.
package chathostbudget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Host class for sub-caps. Orchestrators never auto-hibernate in AO today, so
// they use a separate budget from reclaimable workers.
type Class int

const (
	ClassWorker Class = iota
	ClassOrchestrator
	ClassSide    // btw-*
	ClassReview  // review-*
	ClassBackground
)

// Caps are the concurrent warm limits. Zero means use DefaultCaps.
type Caps struct {
	MaxTotal         int
	MaxOrchestrator  int
	MaxWorker        int // workers + side + review + background
	PressureTotal    int
	PressureOrch     int
	PressureWorker   int
	EnterAvailableB  uint64 // refuse/admit pressure when MemAvailable below this
	ExitAvailableB   uint64 // leave pressure when MemAvailable above this
}

// DefaultCaps targets ~32 GiB desktops: a few warm stacks plus Electron/Chrome.
func DefaultCaps() Caps {
	return Caps{
		MaxTotal:        6,
		MaxOrchestrator: 2,
		MaxWorker:       4,
		PressureTotal:   3,
		PressureOrch:    1,
		PressureWorker:  2,
		EnterAvailableB: 6 << 30,
		ExitAvailableB:  9 << 30,
	}
}

// ErrNoCapacity means the host cannot start another warm process right now.
var ErrNoCapacity = errors.New("chat host warm capacity exhausted")

// ErrNotSeeded means Seed has not run yet; spawn must wait for boot inventory.
var ErrNotSeeded = errors.New("chat host budget not seeded")

// ErrQueued means the wake was deferred onto the fair queue instead of starting
// now. Callers must not clear HibernatedAt or take ErrResumeInProgress.
var ErrQueued = errors.New("chat host wake queued for capacity")

// HostAlive reports whether hostID still has a live provider process.
type HostAlive func(hostID string) bool

// AvailableBytes reads MemAvailable (or equivalent). May be nil in tests.
type AvailableBytes func() (uint64, error)

// QueuedWake is one deferred spawn request.
type QueuedWake struct {
	HostID    string
	ProjectID string
	Class     Class
}

// Budget is the process-wide warm-host arbiter for one daemon.
type Budget struct {
	mu        sync.Mutex
	caps      Caps
	alive     HostAlive
	available AvailableBytes
	seeded    bool
	pressure  bool
	// held maps hostID → class for every charged warm slot (including
	// in-flight Acquires whose PID is not published yet).
	held map[string]Class
	// queue is fair-ish: round-robin by project on Drain.
	queue []QueuedWake
	rr    int
}

// New builds a Budget. alive must not be nil.
func New(caps Caps, alive HostAlive, available AvailableBytes) *Budget {
	if caps.MaxTotal <= 0 {
		caps = DefaultCaps()
	}
	if alive == nil {
		alive = func(string) bool { return false }
	}
	return &Budget{
		caps:      caps,
		alive:     alive,
		available: available,
		held:      make(map[string]Class),
	}
}

// ClassifyHostID maps a chat-hosts directory name to a budget class.
// Session rows that are orchestrators must be classified by the caller via
// Classify(hostID, isOrchestrator).
func ClassifyHostID(hostID string) Class {
	switch {
	case strings.HasPrefix(hostID, "review-"):
		return ClassReview
	case strings.HasPrefix(hostID, "btw-"):
		return ClassSide
	case strings.HasPrefix(hostID, "background-"):
		return ClassBackground
	default:
		return ClassWorker
	}
}

// Classify returns the class for hostID, overriding to orchestrator when asked.
func Classify(hostID string, orchestrator bool) Class {
	if orchestrator {
		return ClassOrchestrator
	}
	return ClassifyHostID(hostID)
}

// Seed inventories live hosts under dataDir/chat-hosts. Must run after
// persistenthost.Reconcile on boot and before any spawn.
func (b *Budget) Seed(dataDir string) error {
	root := filepath.Join(dataDir, "chat-hosts")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			b.mu.Lock()
			b.held = make(map[string]Class)
			b.seeded = true
			b.mu.Unlock()
			return nil
		}
		return err
	}
	next := make(map[string]Class)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if b.alive(id) {
			next[id] = ClassifyHostID(id)
		}
	}
	b.mu.Lock()
	b.held = next
	b.seeded = true
	b.refreshPressureLocked()
	b.mu.Unlock()
	return nil
}

// MarkSeeded marks the budget ready without scanning disk (boot fail-open / tests).
func (b *Budget) MarkSeeded() {
	b.mu.Lock()
	b.seeded = true
	b.mu.Unlock()
}

// MarkSeededForTest is an alias of MarkSeeded for existing tests.
func (b *Budget) MarkSeededForTest() { b.MarkSeeded() }

// NoteAdopt records an already-live host without consuming a new spawn slot.
// If the host was not held (e.g. raced Seed), it is charged once.
func (b *Budget) NoteAdopt(hostID string, class Class) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[hostID]; !ok {
		b.held[hostID] = class
	}
}

// TryAcquireSpawn reserves a slot for a new host process. Returns ErrNoCapacity
// when caps or memory pressure refuse. Idempotent if hostID is already held.
func (b *Budget) TryAcquireSpawn(hostID string, class Class) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.seeded {
		return ErrNotSeeded
	}
	if _, ok := b.held[hostID]; ok {
		return nil
	}
	b.refreshPressureLocked()
	if !b.canAdmitLocked(class) {
		return ErrNoCapacity
	}
	b.held[hostID] = class
	return nil
}

// ReleaseIfDead drops the slot when the host PID is gone. Safe to call
// repeatedly. Returns whether a slot was freed.
func (b *Budget) ReleaseIfDead(hostID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[hostID]; !ok {
		return false
	}
	if b.alive(hostID) {
		return false
	}
	delete(b.held, hostID)
	b.refreshPressureLocked()
	return true
}

// ForceRelease drops a slot without checking liveness. Only for tests or when
// the caller already proved the host never published a PID.
func (b *Budget) ForceRelease(hostID string) {
	b.mu.Lock()
	delete(b.held, hostID)
	b.mu.Unlock()
}

// Enqueue adds a deferred wake. Dedupes by hostID (last write wins class/project).
func (b *Budget) Enqueue(wake QueuedWake) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enqueueLocked(wake)
}

func (b *Budget) enqueueLocked(wake QueuedWake) {
	for i, q := range b.queue {
		if q.HostID == wake.HostID {
			b.queue[i] = wake
			return
		}
	}
	b.queue = append(b.queue, wake)
}

// SoftAdmitWake reserves a spawn slot when capacity is available so callers can
// clear HibernatedAt without racing another spawn into the last slot. Already
// held hosts return nil. On capacity miss (or before Seed) it enqueues and
// returns ErrQueued so callers soft-defer without clearing hibernation.
func (b *Budget) SoftAdmitWake(hostID string, class Class, projectID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[hostID]; ok {
		return nil
	}
	wake := QueuedWake{HostID: hostID, ProjectID: projectID, Class: class}
	if !b.seeded {
		b.enqueueLocked(wake)
		return ErrQueued
	}
	b.refreshPressureLocked()
	if !b.canAdmitLocked(class) {
		b.enqueueLocked(wake)
		return ErrQueued
	}
	// Reserve now — BeforeSpawn's TryAcquireSpawn is idempotent for this host.
	b.held[hostID] = class
	return nil
}

// PressureActive reports whether MemAvailable hysteresis is in the pressure band.
func (b *Budget) PressureActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshPressureLocked()
	return b.pressure
}

// WorkerHostIDs returns held non-orchestrator host IDs (pressure reclaim targets).
func (b *Budget) WorkerHostIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.held))
	for id, class := range b.held {
		if class != ClassOrchestrator {
			out = append(out, id)
		}
	}
	return out
}

// OverPressureCap reports whether holdings overflow the effective total cap.
func (b *Budget) OverPressureCap() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshPressureLocked()
	maxTotal := b.caps.MaxTotal
	if b.pressure {
		maxTotal = b.caps.PressureTotal
	}
	return len(b.held) > maxTotal
}

// ShouldReclaim is true under MemAvailable pressure, when holdings overflow the
// cap, or when the wake queue is blocked because the machine is at capacity.
func (b *Budget) ShouldReclaim() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshPressureLocked()
	if b.pressure {
		return true
	}
	maxTotal := b.caps.MaxTotal
	if len(b.held) > maxTotal {
		return true
	}
	return len(b.queue) > 0 && len(b.held) >= maxTotal
}

// Drain tries to admit queued wakes with round-robin rotation. Returns hosts
// that newly acquired a spawn slot (caller must ConnectOrStart). Entries whose
// host is already held are dropped from the queue without re-admitting.
func (b *Budget) Drain(ctx context.Context) []QueuedWake {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.seeded || len(b.queue) == 0 {
		return nil
	}
	b.refreshPressureLocked()
	n := len(b.queue)
	ordered := make([]QueuedWake, 0, n)
	for i := 0; i < n; i++ {
		ordered = append(ordered, b.queue[(b.rr+i)%n])
	}
	var admitted []QueuedWake
	remaining := make([]QueuedWake, 0, n)
	for _, wake := range ordered {
		if _, ok := b.held[wake.HostID]; ok {
			continue // already warm; drop from queue
		}
		if !b.canAdmitLocked(wake.Class) {
			remaining = append(remaining, wake)
			continue
		}
		b.held[wake.HostID] = wake.Class
		admitted = append(admitted, wake)
	}
	b.queue = remaining
	if n > 0 {
		b.rr = (b.rr + 1) % n
	}
	return admitted
}

// Snapshot is a read-only view for diagnostics/tests.
type Snapshot struct {
	Seeded   bool
	Pressure bool
	Held     map[string]Class
	Queued   int
	Caps     Caps
}

// Snapshot copies current state.
func (b *Budget) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := make(map[string]Class, len(b.held))
	for k, v := range b.held {
		held[k] = v
	}
	return Snapshot{Seeded: b.seeded, Pressure: b.pressure, Held: held, Queued: len(b.queue), Caps: b.caps}
}

func (b *Budget) refreshPressureLocked() {
	if b.available == nil || b.caps.EnterAvailableB == 0 {
		return
	}
	avail, err := b.available()
	if err != nil {
		return
	}
	if b.pressure {
		if avail >= b.caps.ExitAvailableB {
			b.pressure = false
		}
		return
	}
	if avail < b.caps.EnterAvailableB {
		b.pressure = true
	}
}

func (b *Budget) canAdmitLocked(class Class) bool {
	maxTotal, maxOrch, maxWorker := b.caps.MaxTotal, b.caps.MaxOrchestrator, b.caps.MaxWorker
	if b.pressure {
		maxTotal, maxOrch, maxWorker = b.caps.PressureTotal, b.caps.PressureOrch, b.caps.PressureWorker
	}
	total, orch, worker := 0, 0, 0
	for _, c := range b.held {
		total++
		if c == ClassOrchestrator {
			orch++
		} else {
			worker++
		}
	}
	if total >= maxTotal {
		return false
	}
	if class == ClassOrchestrator {
		return orch < maxOrch
	}
	return worker < maxWorker
}

// CapsFromEnv overlays DefaultCaps with AO_CHAT_HOST_MAX_* env vars when set.
func CapsFromEnv() Caps {
	caps := DefaultCaps()
	if v := envInt("AO_CHAT_HOST_MAX_TOTAL"); v > 0 {
		caps.MaxTotal = v
	}
	if v := envInt("AO_CHAT_HOST_MAX_ORCH"); v > 0 {
		caps.MaxOrchestrator = v
	}
	if v := envInt("AO_CHAT_HOST_MAX_WORKER"); v > 0 {
		caps.MaxWorker = v
	}
	return caps
}

func envInt(key string) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
