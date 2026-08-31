// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/store"
)

// checkpointer periodically writes a signed Ed25519 checkpoint over every
// tenant's audit chain (docs/SECURITY-HARDENING.md). This matters because the per-tenant chain
// head is mutable: an attacker with raw database write could delete the tail AND
// rewrite the head consistently and pass chain verification. A signed checkpoint
// is the cryptographic anchor that survives such an attacker — but only if it
// actually exists. Without a live cadence the anchor is inert (a human would have
// to run `olivares audit checkpoint`), so the engine schedules it by default.
type checkpointer struct {
	signer   *audit.Signer
	store    store.Store
	interval time.Duration
	log      *slog.Logger
	stopCh   chan struct{}
	doneCh   chan struct{}

	// lastSuccess is the wall-clock unixnano of the last successful
	// CheckpointAll on THIS node — the source of the checkpoint_age SLI
	// (docs/17 §5, deferred by PR #32). Zero until the first success.
	lastSuccess atomic.Int64
	// mFailures counts failed checkpoint runs (the previously slog-only signal:
	// a failing off-box KMS signer is alertable via increase(), docs/17 §5).
	mFailures *metrics.Counter
}

// startCheckpointer launches the periodic checkpoint loop. interval<=0 disables
// it (with a loud warning) — appropriate only when an external scheduler drives
// `audit checkpoint`. The returned checkpointer must be stopped before the store
// closes, which also writes a final shutdown anchor.
//
// reg (nil-safe) receives the ledger-health SLIs (docs/17 §5):
//   - olivares_audit_checkpoint_age_seconds — scrape-time gauge, emitted ONLY
//     while this node is the leader AND has checkpointed at least once. A
//     standby (or a demoted ex-leader) emits nothing: its in-memory last-success
//     is not the cluster's anchor freshness, and a growing stale age after
//     failover would page falsely. Absence on the leader before the first tick
//     (up to one interval after promotion) is expected; the alert rule allows it.
//   - olivares_audit_checkpoint_failures_total — counter on the previously
//     slog-only failure path.
func startCheckpointer(signer *audit.Signer, st store.Store, interval time.Duration, log *slog.Logger, reg *metrics.Registry) *checkpointer {
	c := &checkpointer{
		signer: signer, store: st, interval: interval, log: log,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	if reg != nil {
		c.mFailures = reg.Counter("olivares_audit_checkpoint_failures_total",
			"Failed periodic audit-checkpoint runs (signer or store error). A rising count means the tamper-evidence anchor is NOT being refreshed; see docs/17 and the ledger-verify-failure runbook.")
		reg.RegisterFunc("olivares_audit_checkpoint_age_seconds", c.writeAge)
	}
	if interval <= 0 {
		log.Warn("audit checkpoint scheduler DISABLED (--checkpoint-interval=0): the tamper-evidence anchor exists only if you run `olivares audit checkpoint` out of band (docs/08 §5)")
		close(c.doneCh)
		return c
	}
	log.Info("audit checkpoint scheduler started", "interval", interval.String())
	go c.loop()
	return c
}

// writeAge emits the checkpoint-age gauge at scrape time (RegisterFunc
// contract: a complete family, or nothing). IsLeader is the raw observability
// predicate (core/store/leader.go documents it for exactly this use).
func (c *checkpointer) writeAge(w io.Writer) {
	last := c.lastSuccess.Load()
	if last == 0 || !c.store.Leader().IsLeader() {
		return
	}
	age := time.Since(time.Unix(0, last)).Seconds()
	fmt.Fprintf(w, "# HELP olivares_audit_checkpoint_age_seconds Seconds since this node's last successful signed audit checkpoint over every tenant chain. Emitted only by the active leader after its first checkpoint.\n# TYPE olivares_audit_checkpoint_age_seconds gauge\nolivares_audit_checkpoint_age_seconds %.3f\n", age)
}

func (c *checkpointer) loop() {
	defer close(c.doneCh)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			c.once(context.Background())
		}
	}
}

// once writes a checkpoint for every tenant. Failures are logged, not fatal: a
// transient store error must not take the engine down, and the next tick retries.
//
// HA: only the ACTIVE writer checkpoints. A standby skips — checkpointing
// is a write (it appends a signed checkpoint event), so doing it on a standby
// would fork the signed chain (and trip the store write-gate every tick). The
// per-node checkpointer starts at boot and gates here each tick, so a promoted
// standby begins checkpointing automatically the moment it becomes leader, and a
// leader stepping down stops.
func (c *checkpointer) once(ctx context.Context) {
	if !c.store.Leader().Active() {
		c.log.Debug("audit checkpoint skipped: this node is a standby, not the active writer")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.signer.CheckpointAll(cctx, c.store); err != nil {
		if c.mFailures != nil {
			c.mFailures.Inc()
		}
		c.log.Warn("audit checkpoint failed", "err", err)
		return
	}
	c.lastSuccess.Store(time.Now().UnixNano())
	c.log.Debug("audit checkpoint written for all tenants")
}

// stop halts the ticker and writes a FINAL checkpoint so the chain tip is signed
// at graceful shutdown. It is called (via defer) BEFORE the store closes, with a
// fresh context because the serve context is already canceled by then.
func (c *checkpointer) stop(ctx context.Context) {
	if c.interval <= 0 {
		return
	}
	close(c.stopCh)
	<-c.doneCh
	c.once(ctx) // final anchor over the tip before the store closes
}
