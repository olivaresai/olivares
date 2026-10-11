// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type blockedIOFailureRecorder struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockedIOFailureRecorder) Record(context.Context, model.TenantID, string, RecordedFrame) error {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return errors.New("private-path secret-value")
}

func (*blockedIOFailureRecorder) Finalize(context.Context, model.TenantID, string) error { return nil }

func TestRuntime_IOEvidenceFailureSurvivesOperatorStop(t *testing.T) {
	rec := &blockedIOFailureRecorder{entered: make(chan struct{}), release: make(chan struct{})}
	m, proc, lr := newFanoutRun(t, rec, true)
	var release sync.Once
	unblock := func() { release.Do(func() { close(rec.release) }) }
	t.Cleanup(unblock)
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"status"}`)}
	select {
	case <-rec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recording did not start")
	}
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := m.stopRun(ctx, lr.tenant, lr.runRef, "user:u1", model.ActorUser)
		stopped <- err
	}()
	// The stop has replaced stopReason and ended the process. Finalize still
	// waits on outputMu while the failed Record remains in flight.
	select {
	case <-proc.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("operator stop did not end the process")
	}
	unblock()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	dto, err := m.getRun(context.Background(), lr.tenant, lr.runRef)
	if err != nil {
		t.Fatal(err)
	}
	if dto.State != stateFailed || dto.Reason != "session I/O evidence recording failed" {
		t.Fatalf("operator stop hid the recording failure: state=%s reason=%q", dto.State, dto.Reason)
	}
}
