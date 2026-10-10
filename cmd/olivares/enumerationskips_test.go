// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// cannotListEveryTenant is a store with no tenant inventory and no administration
// role, as the default PostgreSQL install has: this node leads, and every
// cross-tenant listing is refused as not authoritative. A second listing error, when
// set, replaces that refusal (a fault rather than the deliberate state).
type cannotListEveryTenant struct {
	store.Store
	fault error
}

func (s *cannotListEveryTenant) Leader() store.LeaderElector { return activeLeader{} }
func (s *cannotListEveryTenant) System(_ context.Context, fn func(store.SystemScope) error) error {
	return fn(refusingSystem{fault: s.fault})
}

type activeLeader struct{ store.LeaderElector }

func (activeLeader) Active() bool { return true }

type refusingSystem struct {
	store.SystemScope
	fault error
}

func (r refusingSystem) ListOrgs(context.Context) ([]model.Org, error) {
	if r.fault != nil {
		return nil, r.fault
	}
	return nil, fmt.Errorf("%w: engine \"postgres\" holds no BYPASSRLS admin pool and no closed directory inventory routine", store.ErrEnumerationNotAuthoritative)
}

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// The jobs that must cover every tenant skip every tick on a database that cannot
// list every tenant. That is the deliberate state of a default PostgreSQL install
// until the inventory is installed, and server-info already names each job, so each
// says it ONCE, not once per tick. A fault that is not that state still warns on
// every tick.
func TestCoverageJobsSayOnceThatTheyCannotListEveryTenant(t *testing.T) {
	signer, err := audit.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		job  string
		tick func(st store.Store, log *slog.Logger) func()
	}{
		{"retention-sweep", func(st store.Store, log *slog.Logger) func() {
			l := &retentionSweepLoop{st: st, interval: time.Hour, log: log}
			return func() { _ = l.runOnce(context.Background()) }
		}},
		{"audit-legalhold", func(st store.Store, log *slog.Logger) func() {
			l := &longHorizonHoldLoop{st: st, interval: time.Hour, log: log}
			return func() { _ = l.runOnce(context.Background()) }
		}},
		{"audit-archive", func(st store.Store, log *slog.Logger) func() {
			l := &auditArchiveLoop{st: st, interval: time.Hour, log: log, keysAttempted: true}
			return func() { _ = l.runOnce(context.Background()) }
		}},
		{"audit-checkpoint", func(st store.Store, log *slog.Logger) func() {
			c := &checkpointer{store: st, signer: signer, log: log}
			return func() { c.once(context.Background()) }
		}},
	} {
		if tc.job == "audit-archive" && !audit.ExportLinked {
			continue
		}
		t.Run(tc.job, func(t *testing.T) {
			log, buf := captureLog()
			tick := tc.tick(&cannotListEveryTenant{}, log)
			for range 3 {
				tick()
			}
			if n := strings.Count(buf.String(), "level=WARN"); n != 1 || !strings.Contains(buf.String(), "cannot list every tenant") {
				t.Fatalf("three ticks without the tenant inventory logged %d warnings, want 1:\n%s", n, buf)
			}
			if strings.Contains(buf.String(), "level=ERROR") {
				t.Fatalf("the deliberate state was logged as an error:\n%s", buf)
			}

			log, buf = captureLog()
			tick = tc.tick(&cannotListEveryTenant{fault: errors.New("connection refused")}, log)
			for range 3 {
				tick()
			}
			if n := strings.Count(buf.String(), "level=WARN"); n != 3 {
				t.Fatalf("three ticks with a listing fault logged %d warnings, want 3:\n%s", n, buf)
			}
		})
	}
}

// The state change back: the first tick that lists every tenant again says so once,
// and a later loss of the inventory warns again.
func TestEnumerationSkipsLogOncePerStateChange(t *testing.T) {
	log, buf := captureLog()
	var s enumerationSkips
	refused := fmt.Errorf("list: %w", store.ErrEnumerationNotAuthoritative)
	s.skip(log, "retention-sweep", refused)
	s.skip(log, "retention-sweep", refused)
	s.listed(log, "retention-sweep")
	s.listed(log, "retention-sweep")
	s.skip(log, "retention-sweep", refused)
	out := buf.String()
	if w, i := strings.Count(out, "level=WARN"), strings.Count(out, "level=INFO"); w != 2 || i != 1 {
		t.Fatalf("skip, skip, listed, listed, skip logged %d warnings and %d infos, want 2 and 1:\n%s", w, i, out)
	}
	if !strings.Contains(out, "lists every tenant again") {
		t.Fatalf("the return to listing every tenant is not said:\n%s", out)
	}
}

// The start line that names a coverage job server-info lists as not running is a
// warning: the state is deliberate and has a remedy, it is not an engine fault.
func TestJobNotRunningIsAWarningAtStart(t *testing.T) {
	log, buf := captureLog()
	var jobs jobsNotRunning
	jobs.coverageJob(false, "retention", log)
	jobs.coverageJob(false, "retention", log)
	if out := buf.String(); strings.Count(out, "level=WARN") != 1 || strings.Contains(out, "level=ERROR") {
		t.Fatalf("a coverage job that does not run, recorded twice, logged:\n%s", out)
	}
}
