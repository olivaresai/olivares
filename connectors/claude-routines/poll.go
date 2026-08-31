// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// pollLoop runs an immediate first refresh, then re-runs every refresh
// interval until ctx is canceled.
func (s *Source) pollLoop(ctx context.Context, sink sdk.Sink) {
	s.refreshOnce(ctx, sink)
	t := time.NewTicker(s.cfg.refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshOnce(ctx, sink)
		}
	}
}

// refreshOnce fetches triggers and emits inventory edges + governance findings.
// A fetch failure degrades to a self-audit finding rather than silence.
func (s *Source) refreshOnce(ctx context.Context, sink sdk.Sink) {
	at := s.clock()
	triggers, err := s.cl.fetchTriggers(ctx)
	if err != nil {
		s.degrade(ctx, sink, "triggers", err, at)
		return
	}
	for _, t := range triggers {
		if !s.emit(ctx, sink, routineEdge(t, s.cfg.organizationID, at)) {
			return
		}
		if f, ok := cadenceFinding(t, s.cfg.maxCadenceSeconds, at); ok {
			if !s.emit(ctx, sink, f) {
				return
			}
		}
		if f, ok := reviewFinding(t, s.cfg.reviewAfterDays, at); ok {
			if !s.emit(ctx, sink, f) {
				return
			}
		}
		if f, ok := nameFinding(t, at); ok {
			if !s.emit(ctx, sink, f) {
				return
			}
		}
	}
}

// degrade emits an honest self-audit posture finding for a failed surface
// fetch, so the ledger carries proof of a coverage gap rather than silence.
func (s *Source) degrade(ctx context.Context, sink sdk.Sink, surface string, err error, at time.Time) {
	_ = sink.Emit(ctx, model.FindingReport{
		Kind:        findingSelfAudit,
		Severity:    model.SeverityLow,
		SubjectKind: connectorSubject,
		SubjectRef:  surface,
		Title:       "Claude Routines observation degraded: " + surface,
		DetailHash:  redact.Hash("routines poll degraded surface=" + surface + " err=" + redact.Clean(err.Error())),
		OccurredAt:  at,
	})
}
