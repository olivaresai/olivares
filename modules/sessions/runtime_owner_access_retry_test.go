// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestOwnerAccessSweepMissingOwnerBindingKeepsOrdinarySession(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, clock := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	// External PEPs and legacy sessions have no entry in the engine's issuer.
	// The real issuer's missing binding is not an observed owner withdrawal.
	issuer := auth.NewSessionCredentials(nil, nil)
	m.SessionAccessCheck = issuer.CheckOwnerAccess
	for range 3 {
		m.sweepSessionAccess(t.Context())
		clock.advance(6 * time.Minute)
	}
	view, err := m.getRun(t.Context(), tenant, dto.RunRef)
	if err != nil || view.ProcessState != "running" {
		t.Fatalf("an ordinary session without an owner binding must keep running: %+v, %v", view, err)
	}
	select {
	case <-fr.lastProc().stopped:
		t.Fatal("missing owner binding stopped the ordinary session")
	default:
	}
}

func TestOwnerAccessSweepTransientErrorLogsRetriesAndKeepsSession(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, clock := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	m.log = slog.New(slog.NewJSONHandler(&logs, nil))
	attempts := 0
	fail := true
	m.SessionAccessCheck = func(context.Context, model.TenantID, string) (auth.SessionScope, string, error) {
		attempts++
		if fail {
			return auth.SessionScope{}, "", fmt.Errorf("temporary store stall: %w", context.DeadlineExceeded)
		}
		return auth.SessionScope{}, "", nil
	}
	m.sweepSessionAccess(t.Context())
	select {
	case <-fr.lastProc().stopped:
		t.Fatal("a transient owner read error stopped the live session")
	default:
	}
	var warning map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &warning); err != nil {
		t.Fatalf("owner read failure must be logged: %v; log=%s", err, logs.String())
	}
	if warning["run_ref"] != dto.RunRef || warning["attempt"] != float64(1) || warning["cause"] != "deadline_exceeded" {
		t.Fatalf("missing read-error cause/run/attempt: %v", warning)
	}
	m.sweepSessionAccess(t.Context())
	if attempts != 1 {
		t.Fatal("owner read retried before its backoff")
	}
	clock.advance(5 * time.Second)
	fail = false
	m.sweepSessionAccess(t.Context())
	if attempts != 2 {
		t.Fatal("owner read did not retry after its backoff")
	}
	view, err := m.getRun(t.Context(), tenant, dto.RunRef)
	if err != nil || view.ProcessState != "running" {
		t.Fatalf("recovered owner check must keep the session running: %+v, %v", view, err)
	}
	// A later outage gets its own grace and starts again at attempt one.
	logs.Reset()
	clock.advance(6 * time.Minute)
	fail = true
	m.sweepSessionAccess(t.Context())
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &warning); err != nil || warning["attempt"] != float64(1) {
		t.Fatalf("successful check did not reset the failure window: %v, %v", warning, err)
	}
	view, err = m.getRun(t.Context(), tenant, dto.RunRef)
	if err != nil || view.ProcessState != "running" {
		t.Fatal("a recovered failure window poisoned a later transient read")
	}
}

func TestOwnerAccessSweepProvenEndStopsImmediately(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	m.SessionAccessCheck = func(context.Context, model.TenantID, string) (auth.SessionScope, string, error) {
		// A proven withdrawal still stops when scoped graceful admission fails.
		return auth.SessionScope{}, "the owner", fmt.Errorf("standing withdrawn: %w", auth.ErrSessionAccessEnded)
	}
	m.sweepSessionAccess(t.Context())
	waitFor(t, "proven owner withdrawal stopped process", func() bool {
		select {
		case <-fr.lastProc().stopped:
			return true
		default:
			return false
		}
	})
	waitFor(t, "owner withdrawal finalized", func() bool {
		view, err := m.getRun(t.Context(), tenant, dto.RunRef)
		return err == nil && view.ProcessState == "stopped" && view.Reason == "Access ended for the owner"
	})
}

func TestOwnerAccessSweepPersistentErrorsStopAfterFiveMinuteGrace(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, clock := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	m.log = slog.New(slog.NewJSONHandler(&logs, nil))
	attempts := 0
	m.SessionAccessCheck = func(context.Context, model.TenantID, string) (auth.SessionScope, string, error) {
		attempts++
		if attempts == 1 {
			// Time spent waiting for the first failure is outside the grace.
			clock.advance(5 * time.Second)
		}
		return auth.SessionScope{}, "", fmt.Errorf("store read failed with sensitive-store-value")
	}
	m.sweepSessionAccess(t.Context())
	// Backoff grows from 5s to 10s, 20s, then remains capped at 30s.
	for i, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second} {
		clock.advance(delay - time.Nanosecond)
		m.sweepSessionAccess(t.Context())
		if attempts != i+1 {
			t.Fatal("owner read retried before backoff")
		}
		clock.advance(time.Nanosecond)
		m.sweepSessionAccess(t.Context())
		if attempts != i+2 {
			t.Fatal("owner read did not retry when backoff elapsed")
		}
	}
	clock.advance(204 * time.Second) // elapsed 299s: still inside the five-minute grace.
	m.sweepSessionAccess(t.Context())
	view, err := m.getRun(t.Context(), tenant, dto.RunRef)
	if err != nil || view.ProcessState != "running" {
		t.Fatal("persistent errors stopped the session before five minutes")
	}
	clock.advance(time.Second)
	m.sweepSessionAccess(t.Context())
	waitFor(t, "persistent owner read failure stopped session", func() bool {
		view, err := m.getRun(t.Context(), tenant, dto.RunRef)
		return err == nil && view.ProcessState == "stopped" && view.Reason == "owner access could not be checked for 5 minutes"
	})
	if strings.Contains(logs.String(), "sensitive-store-value") || !strings.Contains(logs.String(), `"cause":"read_failed"`) {
		t.Fatal("read error cause must be logged without sensitive store text")
	}
}
