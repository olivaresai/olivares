// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// C3-L1: a tenant created after this process started has a complete durable
// authorization generation but no process-local scoped Cedar state, so typed
// evidence returns UNKNOWN until something loads it. Nothing in the creation path
// can call governance (reloadTenantGrants opens its own View and the creating
// transaction still holds the single SQLite connection), so the load happens here,
// once, at the beginning of the typed evidence paths.
//
// This is an AVAILABILITY mechanism, not an authority one. A load only lets the
// existing evidence View run; that View still owns the generation, the database
// observation time, freshness, lineage facts and the runtime operation bracket. A
// cache load is not a permit, and it never writes: the read-only reloadTenantGrants
// is used precisely because ReloadActivePDP can backfill freshness through Mutate.
const (
	// evidenceLoadCooldownCapacity bounds the retained completed-failure records.
	// At capacity the oldest completed entry is evicted so a new tenant is never
	// refused because other tenants occupy the cooldown; eviction can only add
	// read/compile work, never authority.
	evidenceLoadCooldownCapacity = 1024
	// evidenceLoadCooldown sheds repeated cold loads for up to one second WHILE THE
	// ENTRY IS RETAINED. It is deliberately not a per-tenant rate guarantee, a
	// freshness interval or part of any authorization identity: eviction and clock
	// regression may both end it early.
	evidenceLoadCooldown = time.Second
)

// evidenceLoadOutcome selects the admission cleanup for one completed attempt. It
// mirrors the construction's completion table and is the only thing the deferred
// release needs to know; its zero value is the panic/cancellation case, so an
// unwinding attempt can never leave a cooldown behind.
type evidenceLoadOutcome uint8

const (
	// evidenceLoadOutcomeNoCooldown removes in-flight ownership and retains no
	// cooldown: a canceled/exhausted caller, a cancellation-shaped callback error
	// and a panic must all leave a healthy later call free to retry immediately.
	evidenceLoadOutcomeNoCooldown evidenceLoadOutcome = iota
	// evidenceLoadOutcomeSuccess additionally clears any retained cooldown for the
	// tenant, because the runtime is now usable.
	evidenceLoadOutcomeSuccess
	// evidenceLoadOutcomeCooldown records the completed failure.
	evidenceLoadOutcomeCooldown
)

// evidenceLoadCooldownEntry is one retained completed failure. It is a node of the
// FIFO and the value of the index, so every removal unlinks both representations
// and no tombstone or duplicate can survive an update.
type evidenceLoadCooldownEntry struct {
	tenant model.TenantID
	expiry time.Time
	older  *evidenceLoadCooldownEntry
	newer  *evidenceLoadCooldownEntry
}

// evidenceLoadAdmission owns operational load admission for one scoped engine. Its
// mutex is metadata-only: it is never held across a runtime capture, the loader
// callback, compilation or any evidence View, so one tenant's loader I/O wait can
// never be inherited by another tenant. It is also never held together with the
// engine's runtime mutex.
//
// The two structures are deliberately separate. inFlight is EXACT live ownership,
// holding one entry per executing callback and nothing else; cooldown is the bounded
// completed-failure record. In-flight ownership is never evicted.
type evidenceLoadAdmission struct {
	mu       sync.Mutex
	inFlight map[model.TenantID]struct{}
	cooldown map[model.TenantID]*evidenceLoadCooldownEntry
	oldest   *evidenceLoadCooldownEntry
	newest   *evidenceLoadCooldownEntry
	// now is this engine's OPERATIONAL clock, initialized directly to time.Now so
	// production comparisons keep their monotonic readings. model.Clock is not used
	// here: its UTC conversion discards them. Policy freshness and evidence keep
	// their existing module/database clocks.
	now func() time.Time
	// lastRead detects a regression of that clock (a test seam, a corrected wall
	// clock). A regression clears retained cooldowns and never extends a denial.
	lastRead time.Time
}

func newEvidenceLoadAdmission(now func() time.Time) *evidenceLoadAdmission {
	return &evidenceLoadAdmission{
		inFlight: map[model.TenantID]struct{}{},
		cooldown: map[model.TenantID]*evidenceLoadCooldownEntry{},
		now:      now,
	}
}

// admit reserves exact in-flight ownership for tenant. A concurrent loader for the
// same tenant or a retained unexpired cooldown refuses without calling the loader.
func (a *evidenceLoadAdmission) admit(tenant model.TenantID) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.observeLocked()
	if _, busy := a.inFlight[tenant]; busy {
		return false
	}
	if entry, retained := a.cooldown[tenant]; retained {
		if entry.expiry.After(now) {
			return false
		}
		// An expired entry is removed directly rather than waiting for it to reach
		// the FIFO front.
		a.removeLocked(entry)
	}
	if a.inFlight == nil {
		a.inFlight = map[model.TenantID]struct{}{}
	}
	a.inFlight[tenant] = struct{}{}
	return true
}

// release ends one admitted attempt. It always removes in-flight ownership, which is
// why the caller installs it as a defer immediately after admission: a panic must not
// leave a tenant permanently owned.
func (a *evidenceLoadAdmission) release(tenant model.TenantID, outcome evidenceLoadOutcome) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inFlight, tenant)
	now := a.observeLocked()
	switch outcome {
	case evidenceLoadOutcomeSuccess:
		if entry, retained := a.cooldown[tenant]; retained {
			a.removeLocked(entry)
		}
	case evidenceLoadOutcomeCooldown:
		a.insertCooldownLocked(tenant, now)
	case evidenceLoadOutcomeNoCooldown:
	}
}

// observeLocked reads the operational clock and keeps the bounded structures
// consistent with it: a regression clears the retained cooldowns (never in-flight
// ownership), then expired entries leave the FIFO front. Both loops are bounded by
// the fixed capacity, so no operation can scan an unbounded history.
func (a *evidenceLoadAdmission) observeLocked() time.Time {
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	if now.Before(a.lastRead) {
		a.clearCooldownLocked()
	}
	a.lastRead = now
	for i := 0; i < evidenceLoadCooldownCapacity && a.oldest != nil && !a.oldest.expiry.After(now); i++ {
		a.removeLocked(a.oldest)
	}
	return now
}

// insertCooldownLocked records a completed failure. An already retained tenant moves
// to the tail with one new expiry instead of gaining a second node.
func (a *evidenceLoadAdmission) insertCooldownLocked(tenant model.TenantID, now time.Time) {
	if a.cooldown == nil {
		a.cooldown = map[model.TenantID]*evidenceLoadCooldownEntry{}
	}
	if entry, retained := a.cooldown[tenant]; retained {
		entry.expiry = now.Add(evidenceLoadCooldown)
		a.unlinkLocked(entry)
		a.appendLocked(entry)
		return
	}
	if len(a.cooldown) >= evidenceLoadCooldownCapacity && a.oldest != nil {
		a.removeLocked(a.oldest)
	}
	entry := &evidenceLoadCooldownEntry{tenant: tenant, expiry: now.Add(evidenceLoadCooldown)}
	a.cooldown[tenant] = entry
	a.appendLocked(entry)
}

func (a *evidenceLoadAdmission) removeLocked(entry *evidenceLoadCooldownEntry) {
	if entry == nil {
		return
	}
	a.unlinkLocked(entry)
	delete(a.cooldown, entry.tenant)
}

func (a *evidenceLoadAdmission) unlinkLocked(entry *evidenceLoadCooldownEntry) {
	switch {
	case entry.older != nil:
		entry.older.newer = entry.newer
	case a.oldest == entry:
		a.oldest = entry.newer
	}
	switch {
	case entry.newer != nil:
		entry.newer.older = entry.older
	case a.newest == entry:
		a.newest = entry.older
	}
	entry.older, entry.newer = nil, nil
}

func (a *evidenceLoadAdmission) appendLocked(entry *evidenceLoadCooldownEntry) {
	entry.older, entry.newer = a.newest, nil
	if a.newest != nil {
		a.newest.newer = entry
	}
	a.newest = entry
	if a.oldest == nil {
		a.oldest = entry
	}
}

func (a *evidenceLoadAdmission) clearCooldownLocked() {
	a.cooldown = map[model.TenantID]*evidenceLoadCooldownEntry{}
	a.oldest, a.newest = nil, nil
}

// ensureEvidenceRuntime is the shared entry seam of ScopedEvidence and
// EvaluateEvidence. It returns the EXACT recaptured runtime state, never a
// reconstructed identity, and a Boolean that means only that this state may enter
// the existing typed evidence path. That Boolean is NOT an authorization verdict:
// the evidence View still has to establish the generation, clock and freshness, and
// a coherent empty selection produces the ordinary ABSTAIN/CLEAN contribution.
//
// At most one load is initiated per typed evidence contribution, and at most one
// lazy load per tenant runs in this process at a time. Separate contributions,
// including several questions in one capabilities batch, are separate calls.
func (e *scopedEngine) ensureEvidenceRuntime(
	ctx context.Context,
	tenant model.TenantID,
) (scopedTenantState, bool) {
	before, loaded := e.tenantState(tenant)
	if governanceEvidenceScopedStateUsable(tenant, before, loaded) {
		// A usable warm state pays no admission bookkeeping, invokes no callback
		// and triggers no refresh. An obsolete-but-available generation is
		// deliberately not refreshed here; the evidence View still rejects it.
		return before, true
	}
	if !e.evidenceLoadAdmissible(ctx, tenant) {
		return before, false
	}
	if !e.admission.admit(tenant) {
		return before, false
	}
	outcome := evidenceLoadOutcomeNoCooldown
	defer func() { e.admission.release(tenant, outcome) }()

	// A writer or a just-completed load may have installed a usable state across the
	// initial-capture/admission gap. Recheck before spending a durable read.
	if recaptured, recapturedLoaded := e.tenantState(tenant); governanceEvidenceScopedStateUsable(
		tenant, recaptured, recapturedLoaded,
	) {
		outcome = evidenceLoadOutcomeSuccess
		return recaptured, true
	}

	// The callback receives the ORIGINAL caller context: no new deadline, detached
	// context, goroutine, queue, retry sleep or second attempt. The evidence View
	// then runs on whatever budget remains.
	loadErr := e.loadTenant(ctx, tenant)

	// The callback error alone does not describe the resulting runtime, so completion
	// uses the actual recaptured state and the complete usability predicate: a newer
	// writer can install a usable state while the loader returns an error, and an
	// older observation can return nil while a newer unavailable state stands.
	after, afterLoaded := e.tenantState(tenant)
	usable := governanceEvidenceScopedStateUsable(tenant, after, afterLoaded)
	switch {
	case evidenceLoadCallerBudgetSpent(ctx):
		// Warming the cache can consume the whole budget. This contribution stays
		// UNKNOWN without opening a new evidence View, and no cooldown is retained,
		// so a later healthy call can use the state this one paid for.
		outcome = evidenceLoadOutcomeNoCooldown
		return after, false
	case usable:
		outcome = evidenceLoadOutcomeSuccess
		return after, true
	case evidenceLoadCanceled(loadErr):
		outcome = evidenceLoadOutcomeNoCooldown
		return after, false
	default:
		outcome = evidenceLoadOutcomeCooldown
		return after, false
	}
}

// evidenceLoadAdmissible is the complete precondition set for a cold load. A rejected
// precondition allocates no admission state and calls no storage. The real callback
// still checks the module's own data handle and fails closed when miswired; this does
// not duplicate that check with a second production handle.
func (e *scopedEngine) evidenceLoadAdmissible(ctx context.Context, tenant model.TenantID) bool {
	return e != nil && e.resolver != nil && e.resolver.data != nil &&
		e.loadTenant != nil && e.admission != nil &&
		validGovernanceEvidenceTenant(tenant) &&
		!evidenceLoadCallerBudgetSpent(ctx)
}

// evidenceLoadCallerBudgetSpent reports whether the CALLER's own budget is gone. It
// reads the caller's context and the real clock on purpose: the injected operational
// cooldown clock must never decide cancellation, and typed evidence has no bounded
// witness at all without a finite deadline still in the future.
func evidenceLoadCallerBudgetSpent(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return !ok || !deadline.After(time.Now())
}

// evidenceLoadCanceled classifies a callback failure as cancellation/deadline, which
// says nothing about the tenant's durable policy and therefore records no cooldown.
func evidenceLoadCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// validGovernanceEvidenceTenant is the canonical tenant predicate shared by the
// evidence fact check and cold-load admission, so the two can never drift.
func validGovernanceEvidenceTenant(tenant model.TenantID) bool {
	parsed, err := model.ParseTenantID(tenant.String())
	return err == nil && parsed == tenant && !tenant.IsZero() && !tenant.IsSystem()
}
