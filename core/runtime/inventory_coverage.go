// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

var errCollectionProtocol = errors.New("runtime: inventory collection protocol rejected")

// collectionPass owns one Gather's mutable state. The mutex serializes Emit,
// including connectors that emit concurrently. No store transaction spans Publish.
type collectionPass struct {
	mu       sync.Mutex
	runtime  *Runtime
	next     sdk.Sink
	recorder event.InventoryRecorder
	run      event.InventoryRun
	start    *model.InventoryCollectionStart
	report   *model.InventoryCollectionReport
	expected int64
	invalid  string
	closed   bool
	warned   bool
}

func (r *Runtime) collectionSink(ctx context.Context, source *sourceReg, next sdk.Sink) *collectionPass {
	p := &collectionPass{runtime: r, next: next}
	if r.sinkFactory == nil && source.registration != nil && source.registration.Valid() && r.inventoryRecorder != nil {
		p.run = event.InventoryRun{ID: uuid.NewString(), Tenant: source.tenant, Registration: *source.registration.Clone(), StartedAt: time.Now().UTC()}
		p.run.Registration.BindingRef = ""
		if err := r.inventoryRecorder.BeginRun(ctx, p.run); err == nil {
			p.recorder = r.inventoryRecorder
		} else {
			p.persistenceFailure(err)
		}
	}
	return p
}

func (p *collectionPass) Emit(ctx context.Context, observation model.Observation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errCollectionProtocol
	}
	switch o := observation.(type) {
	case *model.InventoryCollectionStart:
		if o == nil {
			p.invalid = "protocol_error"
			return errCollectionProtocol
		}
		observation = *o
	case *model.InventoryCollectionMember:
		if o == nil {
			p.invalid = "protocol_error"
			return errCollectionProtocol
		}
		observation = *o
	case *model.InventoryCollectionReport:
		if o == nil {
			p.invalid = "protocol_error"
			return errCollectionProtocol
		}
		observation = *o
	}
	switch o := observation.(type) {
	case model.InventoryCollectionStart:
		if p.recorder == nil {
			p.compatibility()
			return nil
		}
		if p.start != nil || p.report != nil || !o.Scope.Valid() || o.ObservedAt.IsZero() {
			p.invalid = "protocol_error"
			return errCollectionProtocol
		}
		o.Scope = o.Scope.Clone()
		p.start = &o
		if err := p.recorder.StartCollection(ctx, p.run, o); err != nil {
			p.persistenceFailure(err)
			return errCollectionProtocol
		}
		return nil
	case model.InventoryCollectionReport:
		if p.recorder == nil {
			p.compatibility()
			return nil
		}
		if p.report != nil || !model.ValidInventoryResult(o.State, o.Reason) || !model.ValidInventoryFingerprint(o.FulfilledScope) || o.Count < 0 || o.Count > model.MaxInventoryMembers || o.ObservedUntil.IsZero() || (p.start == nil && (o.State == "complete" || o.RequestedScope != "" || o.FulfilledScope != "" || o.Count != 0)) || (p.start != nil && (o.ObservedUntil.Before(p.start.ObservedAt) || o.RequestedScope != p.start.Scope.Fingerprint())) {
			p.invalid = "protocol_error"
			return errCollectionProtocol
		}
		p.report = &o
		return nil
	case model.InventoryCollectionMember:
		// Even invalid/unattributed collection metadata cannot erase a useful edge.
		if p.recorder == nil {
			p.compatibility()
			return p.emitOrdinary(ctx, o.Edge)
		}
		if p.start == nil || p.report != nil || !p.start.Scope.Contains(o.Edge) || p.expected >= model.MaxInventoryMembers {
			p.invalid = "protocol_error"
			return p.emitOrdinary(ctx, o.Edge)
		}
		b, ok := p.next.(*busSink)
		if !ok {
			p.invalid = "protocol_error"
			return p.emitOrdinary(ctx, o.Edge)
		}
		admissionRejected := false
		err := b.emit(ctx, o.Edge, func(e *event.Event) error {
			m := event.InventoryMember{RunID: p.run.ID, Ordinal: p.expected + 1, Digest: event.InventoryMemberDigest(*e)}
			if err := p.recorder.AdmitMember(ctx, p.run, e.ID, m); err != nil {
				p.persistenceFailure(err)
				admissionRejected = true
				// Preserve the admitted positive edge without collection authority.
				return nil
			}
			p.expected++
			e.InventoryMember = &m
			return nil
		})
		if err == nil && admissionRejected {
			err = errCollectionProtocol
		}
		if err != nil && p.invalid == "" {
			p.invalid = "sink_error"
		}
		return err
	default:
		return p.emitOrdinary(ctx, observation)
	}
}
func (p *collectionPass) emitOrdinary(ctx context.Context, o model.Observation) error {
	err := p.next.Emit(ctx, o)
	if err != nil && p.invalid == "" {
		p.invalid = "sink_error"
	}
	return err
}
func (p *collectionPass) compatibility() {
	if !p.warned {
		p.runtime.log.Debug("inventory coverage unavailable on legacy or unattributed ingestion", "reason", "scope_unproven")
		p.warned = true
	}
}
func (p *collectionPass) finish(ctx context.Context, gatherErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.recorder == nil {
		return
	}
	report := model.InventoryCollectionReport{State: "unknown", Reason: "missing_report"}
	if p.report != nil {
		report = *p.report
	}
	if gatherErr != nil && p.invalid == "" {
		p.invalid = "gather_error"
	}
	if ctx.Err() != nil {
		p.invalid = "canceled"
	}
	if p.invalid != "" {
		report.State = "partial"
		report.Reason = p.invalid
	}
	if report.State == "complete" && (p.start == nil || report.FulfilledScope != p.start.Scope.Fingerprint() || report.Count != p.expected || report.Reason != "exhausted") {
		report.State = "partial"
		report.Reason = "protocol_error"
	}
	if err := p.recorder.FinishRun(ctx, p.run, event.InventoryFinish{Report: report, Expected: p.expected, FinishedAt: time.Now().UTC()}); err != nil {
		p.persistenceFailure(err)
	}
}

// Preserve uncertainty in the diagnostic. This classification never retries or
// rewrites a potentially durable Finish, and never includes the underlying text.
func (p *collectionPass) persistenceFailure(err error) {
	reason := "persistence_error"
	switch {
	case errors.Is(err, store.ErrCommitOutcomeUnknown):
		reason = "commit_outcome_unknown"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		reason = "persistence_canceled"
	case errors.Is(err, store.ErrStoreUnavailable):
		reason = "persistence_unavailable"
	}
	p.invalid = reason
	p.runtime.log.Warn("inventory collection persistence outcome", "reason", reason)
}
