package projectxfer

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Registry tracks in-flight export/import jobs and their progress for the UI.
type Registry struct {
	mu   sync.RWMutex
	jobs map[string]*job
}

type job struct {
	progress Progress
	artifact []byte // completed export package (gzip tar)
	updated  time.Time
}

func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*job)}
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (r *Registry) start(kind string) string {
	id := newJobID()
	r.mu.Lock()
	r.jobs[id] = &job{
		progress: Progress{JobID: id, Kind: kind, Phase: "preparing", Percent: 0},
		updated:  time.Now(),
	}
	r.mu.Unlock()
	return id
}

// Begin starts a tracked job (same as an in-flight export/import).
func (r *Registry) Begin(kind string) string { return r.start(kind) }

// Complete marks a job finished with an optional export artifact.
func (r *Registry) Complete(id, projectID string, artifact []byte) {
	r.succeed(id, projectID, artifact)
}

func (r *Registry) set(id string, update func(*Progress)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return
	}
	update(&j.progress)
	j.updated = time.Now()
}

func (r *Registry) fail(id string, err error) {
	r.set(id, func(p *Progress) {
		p.Phase = "failed"
		p.Error = err.Error()
		p.Done = true
	})
}

func (r *Registry) succeed(id string, projectID string, artifact []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return
	}
	j.artifact = artifact
	j.progress.Phase = "done"
	j.progress.Percent = 100
	j.progress.Done = true
	j.progress.ProjectID = projectID
	j.progress.Error = ""
	j.updated = time.Now()
}

// Get returns a copy of job progress.
func (r *Registry) Get(id string) (Progress, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok {
		return Progress{}, false
	}
	return j.progress, true
}

// Artifact returns the completed export bytes.
func (r *Registry) Artifact(id string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	if !ok || !j.progress.Done || j.progress.Error != "" || len(j.artifact) == 0 {
		return nil, false
	}
	out := make([]byte, len(j.artifact))
	copy(out, j.artifact)
	return out, true
}

// Cancel marks a job failed/cancelled.
func (r *Registry) Cancel(id string) {
	r.set(id, func(p *Progress) {
		if p.Done {
			return
		}
		p.Phase = "failed"
		p.Error = "cancelled"
		p.Done = true
	})
}
