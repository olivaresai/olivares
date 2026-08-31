// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"sync"
	"testing"
	"time"
)

func TestDRJobTracker_CreateAndGet(t *testing.T) {
	tracker := newDRJobTracker()

	job := tracker.create(drJobBackup, "test backup")
	if job.ID == "" {
		t.Fatal("job ID should not be empty")
	}
	if job.Kind != drJobBackup {
		t.Errorf("kind = %q, want %q", job.Kind, drJobBackup)
	}
	if job.Status != drJobPending {
		t.Errorf("status = %q, want %q", job.Status, drJobPending)
	}
	if job.Notes != "test backup" {
		t.Errorf("notes = %q, want %q", job.Notes, "test backup")
	}

	got, ok := tracker.get(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if got.ID != job.ID {
		t.Errorf("got ID = %q, want %q", got.ID, job.ID)
	}
}

func TestDRJobTracker_Update(t *testing.T) {
	tracker := newDRJobTracker()

	job := tracker.create(drJobBackup, "")
	tracker.update(job.ID, func(j *drJob) {
		j.Status = drJobRunning
		j.Phase = "snapshot"
		j.Progress = 50
	})

	got, ok := tracker.get(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if got.Status != drJobRunning {
		t.Errorf("status = %q, want %q", got.Status, drJobRunning)
	}
	if got.Phase != "snapshot" {
		t.Errorf("phase = %q, want %q", got.Phase, "snapshot")
	}
	if got.Progress != 50 {
		t.Errorf("progress = %d, want 50", got.Progress)
	}
}

func TestDRJobTracker_List(t *testing.T) {
	tracker := newDRJobTracker()

	tracker.create(drJobBackup, "first")
	tracker.create(drJobRestore, "second")

	jobs := tracker.list()
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(jobs))
	}
}

func TestDRJobTracker_GetNotFound(t *testing.T) {
	tracker := newDRJobTracker()

	_, ok := tracker.get("nonexistent")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestDRJobBroker_SubscribeReceivesUpdates(t *testing.T) {
	tracker := newDRJobTracker()

	job := tracker.create(drJobBackup, "")
	ch, cancel := tracker.broker.subscribe(job.ID)
	defer cancel()

	tracker.update(job.ID, func(j *drJob) {
		j.Status = drJobRunning
		j.Phase = "snapshot"
	})

	select {
	case got := <-ch:
		if got.Status != drJobRunning {
			t.Errorf("status = %q, want %q", got.Status, drJobRunning)
		}
		if got.Phase != "snapshot" {
			t.Errorf("phase = %q, want %q", got.Phase, "snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for update")
	}
}

func TestDRJobBroker_UnsubscribeStopsDelivery(t *testing.T) {
	broker := newDRJobBroker()

	ch, cancel := broker.subscribe("job1")
	cancel()

	broker.publish("job1", drJob{ID: "job1", Status: drJobRunning})

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed after cancel")
		}
	default:
	}
}

func TestDRJobBroker_IsolatesByJobID(t *testing.T) {
	broker := newDRJobBroker()

	ch1, cancel1 := broker.subscribe("job1")
	defer cancel1()
	ch2, cancel2 := broker.subscribe("job2")
	defer cancel2()

	broker.publish("job1", drJob{ID: "job1", Status: drJobRunning})

	select {
	case got := <-ch1:
		if got.ID != "job1" {
			t.Errorf("got ID = %q, want job1", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job1 update")
	}

	select {
	case <-ch2:
		t.Fatal("job2 subscriber should not receive job1 update")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDRJobTracker_ConcurrentAccess(t *testing.T) {
	tracker := newDRJobTracker()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job := tracker.create(drJobBackup, "concurrent")
			tracker.update(job.ID, func(j *drJob) {
				j.Status = drJobRunning
			})
			tracker.get(job.ID)
			tracker.list()
		}()
	}
	wg.Wait()

	jobs := tracker.list()
	if len(jobs) != 100 {
		t.Errorf("len(jobs) = %d, want 100", len(jobs))
	}
}

func TestGenerateJobID_UniquePrefix(t *testing.T) {
	id := generateJobID()
	if len(id) < 10 {
		t.Errorf("job ID too short: %q", id)
	}
	if id[:4] != "drj_" {
		t.Errorf("job ID prefix = %q, want drj_", id[:4])
	}

	id2 := generateJobID()
	if id == id2 {
		t.Errorf("two job IDs are identical: %q", id)
	}
}
