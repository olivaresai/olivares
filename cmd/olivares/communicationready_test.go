// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// server-info carries the communication plane's readiness, so the console never
// has to ask a route that answers 503 to learn it. A default install's plane is
// staged, not effective.
func TestServerInfoCarriesCommunicationReadiness(t *testing.T) {
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	code, info, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/server-info", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("server-info = %d %s", code, raw)
	}
	ready, ok := info["communication_ready"].(bool)
	if !ok {
		t.Fatalf("server-info has no communication_ready: %s", raw)
	}
	if ready {
		t.Fatalf("a default install's communication plane reads ready: %s", raw)
	}
}

// The readiness is sampled at most once per ttl, and an evaluation error is "not
// ready".
func TestCommunicationReadySamplesOncePerTTL(t *testing.T) {
	calls := 0
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	answer, fail := true, error(nil)
	c := newCommunicationReady(func(context.Context) (bool, error) { calls++; return answer, fail })
	c.now = func() time.Time { return now }
	if !c.Ready(context.Background()) || !c.Ready(context.Background()) || calls != 1 {
		t.Fatalf("ready twice within the ttl: calls = %d, want 1", calls)
	}
	now = now.Add(communicationReadyTTL)
	answer, fail = true, errors.New("witness unavailable")
	if c.Ready(context.Background()) || calls != 2 {
		t.Fatalf("after the ttl with an error: ready, calls = %d", calls)
	}
}
