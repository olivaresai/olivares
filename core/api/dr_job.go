// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// DR job states.
const (
	drJobPending   = "pending"
	drJobRunning   = "running"
	drJobCompleted = "completed"
	drJobFailed    = "failed"
)

// DR job kinds.
const (
	drJobBackup  = "backup"
	drJobRestore = "restore"
)

// drJob tracks the progress of an async DR operation (backup or restore).
type drJob struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Progress   int    `json:"progress"`
	BundlePath string `json:"bundle_path,omitempty"`
	BundleID   string `json:"bundle_id,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at"`
	DoneAt     string `json:"done_at,omitempty"`
}

// drJobTracker manages in-memory DR job state with an SSE broker for
// real-time progress updates to connected clients.
type drJobTracker struct {
	mu     sync.RWMutex
	jobs   map[string]*drJob
	broker *drJobBroker
}

func newDRJobTracker() *drJobTracker {
	return &drJobTracker{
		jobs:   make(map[string]*drJob),
		broker: newDRJobBroker(),
	}
}

func (t *drJobTracker) create(kind, notes string) *drJob {
	id := generateJobID()
	job := &drJob{
		ID:        id,
		Kind:      kind,
		Status:    drJobPending,
		Phase:     "init",
		Progress:  0,
		Notes:     notes,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	t.mu.Lock()
	t.jobs[id] = job
	t.mu.Unlock()
	t.broker.publish(id, *job)
	return job
}

func (t *drJobTracker) get(id string) (drJob, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	j, ok := t.jobs[id]
	if !ok {
		return drJob{}, false
	}
	return *j, true
}

func (t *drJobTracker) update(id string, fn func(*drJob)) {
	t.mu.Lock()
	j, ok := t.jobs[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	fn(j)
	cp := *j
	t.mu.Unlock()
	t.broker.publish(id, cp)
}

func (t *drJobTracker) list() []drJob {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]drJob, 0, len(t.jobs))
	for _, j := range t.jobs {
		out = append(out, *j)
	}
	return out
}

// drJobBroker fans out job updates to SSE subscribers, keyed by job ID.
type drJobBroker struct {
	mu   sync.Mutex
	next int
	subs map[int]drJobSub
}

type drJobSub struct {
	jobID string
	ch    chan drJob
}

func newDRJobBroker() *drJobBroker {
	return &drJobBroker{subs: make(map[int]drJobSub)}
}

func (b *drJobBroker) subscribe(jobID string) (<-chan drJob, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan drJob, 8)
	b.subs[id] = drJobSub{jobID: jobID, ch: ch}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(s.ch)
		}
	}
}

func (b *drJobBroker) publish(jobID string, job drJob) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.jobID != jobID {
			continue
		}
		select {
		case sub.ch <- job:
		default:
		}
	}
}

func generateJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "drj_" + hex.EncodeToString(b)
}
