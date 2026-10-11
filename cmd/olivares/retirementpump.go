// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
)

// retirementPumpInterval is how often the pump looks for due retirements when
// nothing wakes it.
const retirementPumpInterval = 30 * time.Second

// retirementPumpBatch bounds the records one pass advances.
const retirementPumpBatch = 64

// retirementPump drives every pending retirement through the composition's
// declared modules. It runs only on the active writer; the retirement record is
// the work item, so a new leader resumes from it and a repeated pass changes
// nothing that already happened.
type retirementPump struct {
	authr   *auth.Authenticator
	modules []declaredModule
	// beforeStepHook runs before each declared step of a pass, after the pass has
	// read the record and the account's authority version into req; an error
	// fails the step. It is nil in production.
	beforeStepHook func(module string, req auth.RetirementRequest) error
	// now is the pump's clock; nil means the system clock.
	now func() time.Time
	// deferred holds, for a record whose last pass found exactly what it already
	// lists, when the pump looks at it again. Such a pass writes nothing, so the
	// record's own schedule stays due and the pump keeps the backoff itself; a new
	// leader starts with none and looks at each blocked record once before it
	// backs off again.
	mu       sync.Mutex
	deferred map[retirementKey]retirementDeferral
	// wakeCh carries an offboard's wake to the running loop; nil until the pump is
	// started.
	wakeCh   chan struct{}
	log      *slog.Logger
	failures *metrics.Counter
}

// retirementKey names one retirement: an account's generation in a tenant.
type retirementKey struct {
	user       model.ID
	tenant     model.TenantID
	generation int64
}

// retirementDeferral is when the pump looks at an unchanged retirement again,
// and how many unchanged passes it has seen in a row.
type retirementDeferral struct {
	until  time.Time
	passes int64
}

// errNoDeclaredModules refuses a pass of a pump that has nothing to run: an
// absence of declared modules is never an empty, finished retirement.
var errNoDeclaredModules = errors.New("retirement: the composition declares no module to retire an account from")

// newRetirementPump returns the pump over authr and the declared modules, ready
// to be started.
func newRetirementPump(authr *auth.Authenticator, declared []declaredModule, reg *metrics.Registry, log *slog.Logger) *retirementPump {
	if log == nil {
		log = slog.Default()
	}
	p := &retirementPump{authr: authr, modules: declared, wakeCh: make(chan struct{}, 1), log: log}
	if reg != nil {
		p.failures = reg.CounterVec("olivares_retirement_failures_total",
			"Retirement failures by cause: no declared modules, reading due records, or a record pass that could not complete.", "cause")
		for _, cause := range []string{"no_declared_modules", "read_due_failed", "record_pass_failed"} {
			p.failures.Add(0, cause)
		}
	}
	return p
}

// recordFailure exposes only a fixed cause code, never record identities or raw
// errors. Cancellation during shutdown is not an operational failure.
func (p *retirementPump) recordFailure(ctx context.Context, cause string) {
	if p != nil && p.failures != nil && ctx.Err() == nil {
		p.failures.Inc(cause)
	}
}

// wake asks the running loop for a pass now. It never blocks: a pending wake
// already covers this one.
func (p *retirementPump) wake() {
	if p == nil || p.wakeCh == nil {
		return
	}
	select {
	case p.wakeCh <- struct{}{}:
	default:
	}
}

// run drives passes until ctx ends: one on every wake and at least one every
// interval, each only while active reports this process as the active writer. A
// pump given no active func never runs a pass.
func (p *retirementPump) run(ctx context.Context, active func() bool) {
	ticker := time.NewTicker(retirementPumpInterval)
	defer ticker.Stop()
	for {
		if active != nil && active() {
			if _, err := p.runOnce(ctx); err != nil && ctx.Err() == nil {
				p.logger().Warn("retirement: a pass could not read the due retirements; the next pass retries", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wakeCh:
		}
	}
}

// logger returns the pump's logger, or the default one.
func (p *retirementPump) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// clock returns the pump's current time.
func (p *retirementPump) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// runOnce advances every due retirement once and returns how many it advanced:
// the records whose state or blocking references the pass changed. A record
// whose pass failed is left as the failure recorded it, with its next attempt
// scheduled later; a record whose pass found nothing new is deferred by the
// backoff of its attempts; a stale pass counts for nothing and the next pass
// reads afresh. The other records still advance.
func (p *retirementPump) runOnce(ctx context.Context) (int, error) {
	if p == nil || p.authr == nil {
		p.recordFailure(ctx, "no_declared_modules")
		return 0, errNoDeclaredModules
	}
	if len(p.modules) == 0 {
		p.recordFailure(ctx, "no_declared_modules")
		return 0, errNoDeclaredModules
	}
	now := p.clock()
	due, err := p.authr.DueRetirements(ctx, model.NewTimestamp(now), 0)
	if err != nil {
		p.recordFailure(ctx, "read_due_failed")
		return 0, err
	}
	steps := p.steps()
	p.mu.Lock()
	kept := make(map[retirementKey]retirementDeferral, len(p.deferred))
	var todo []model.TenantExclusion
	for _, rec := range due {
		key := retirementKey{user: rec.UserID, tenant: rec.TargetTenantID, generation: rec.RetirementGeneration}
		if d, ok := p.deferred[key]; ok {
			// A record that is no longer due was rewritten or settled since, and
			// its deferral goes with this map.
			kept[key] = d
			if now.Before(d.until) {
				continue
			}
		}
		if len(todo) < retirementPumpBatch {
			todo = append(todo, rec)
		}
	}
	p.deferred = kept
	p.mu.Unlock()

	advanced := 0
	for _, rec := range todo {
		key := retirementKey{user: rec.UserID, tenant: rec.TargetTenantID, generation: rec.RetirementGeneration}
		pass, err := p.authr.AdvanceRetirement(ctx, steps, rec.UserID, rec.TargetTenantID)
		p.mu.Lock()
		switch {
		case err != nil:
			delete(p.deferred, key)
		case pass.Progress == auth.RetirementUnchanged:
			d := p.deferred[key]
			d.passes++
			d.until = now.Add(auth.RetirementBackoff(pass.Record.Attempts + d.passes))
			p.deferred[key] = d
		default:
			delete(p.deferred, key)
		}
		p.mu.Unlock()
		if err != nil {
			p.recordFailure(ctx, "record_pass_failed")
			p.logger().Warn("retirement: a pass did not complete; the record keeps its state and is retried later",
				"tenant", rec.TargetTenantID.String(), "err", err)
			continue
		}
		if pass.Progress == auth.RetirementAdvanced {
			advanced++
		}
	}
	return advanced, nil
}

// steps returns the declared steps, each preceded by the before-step hook when
// one is set.
func (p *retirementPump) steps() []auth.RetirementStep {
	steps := retirementSteps(p.modules)
	if p.beforeStepHook == nil {
		return steps
	}
	out := make([]auth.RetirementStep, len(steps))
	for i, s := range steps {
		out[i] = hookedStep{RetirementStep: s, before: p.beforeStepHook}
	}
	return out
}

// hookedStep runs the before-step hook, then the step.
type hookedStep struct {
	auth.RetirementStep
	before func(module string, req auth.RetirementRequest) error
}

// RetireUser implements auth.RetirementStep.
func (s hookedStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	if err := s.before(s.Module(), req); err != nil {
		return auth.RetirementOutcome{}, err
	}
	return s.RetirementStep.RetireUser(ctx, req)
}
