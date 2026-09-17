// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestScopedSessionConsumersKeepIdentity(t *testing.T) {
	ss, st, tenant := newSessionsStore(t)
	ctx := context.Background()
	external := model.NewID()
	now := model.NewTimestamp(time.Now().UTC()).String()
	var a, b model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		lives, err := sc.Ext("sessions.live")
		if err != nil {
			return err
		}
		timeline, err := sc.Ext("sessions.timeline")
		if err != nil {
			return err
		}
		for i, profile := range []string{"review-profile-a", "review-profile-b"} {
			row, err := lives.Create(ctx, model.Record{"session_ref": external.String(), "observation_scope": "observed:" + profile, "provider_profile_id": profile, "provider": "claude", "environment_ref": "review-local", "first_event_at": now, "last_event_at": now, "event_count": int64(1), "input_tokens": int64(10 + i), "output_tokens": int64(0), "cost_micro_usd": int64(0), "tool_call_count": int64(1)})
			if err != nil {
				return err
			}
			id := model.ID(row.String(model.ColID))
			if i == 0 {
				a = id
			} else {
				b = id
			}
			if _, err = timeline.Create(ctx, model.Record{"session_ref": external.String(), "live_ref": id.String(), "at": now, "kind": "tool", "tool_ref": "Read", "resource_ref": "/fixture/" + profile, "mode": "read", "source": "review"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	exact, _, err := ss.ReplayTimelineByLiveRef(ctx, tenant, a.String(), 10)
	if err != nil || len(exact) != 1 || exact[0].ResourceRef != "/fixture/review-profile-a" {
		t.Fatalf("scoped positive replay failed len=%d err=%v", len(exact), err)
	}
	steps, err := (sessionsHistoryAdapter{ss: ss}).Timeline(ctx, tenant, external.String())
	t.Logf("sandbox scoped positive=%d bare adapter=%d error=%v", len(exact), len(steps), err)
	if err != nil || len(steps) != 0 {
		t.Fatal("legacy adapter borrowed scoped history")
	}
	scopedSteps, err := (sessionsHistoryAdapter{ss: ss}).TimelineByLiveRef(ctx, tenant, a.String())
	if err != nil || len(scopedSteps) != 1 {
		t.Fatalf("scoped sandbox replay steps=%d err=%v", len(scopedSteps), err)
	}
	samples, err := (sessionsSampleAdapter{ss: ss, window: 0}).Sample(ctx, tenant, evals.SampleQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("evals exact session selector returned %d samples for two distinct live_refs; refs_equal=%t", len(samples), len(samples) == 2 && samples[0].SessionRef == samples[1].SessionRef)
	if len(samples) != 2 || samples[0].LiveRef == samples[1].LiveRef || samples[0].ProfileRef == samples[1].ProfileRef {
		t.Fatal("scoped samples lost identity")
	}
	for _, live := range []model.ID{a, b} {
		one, err := (sessionsSampleAdapter{ss: ss}).Sample(ctx, tenant, evals.SampleQuery{LiveRef: live.String()})
		if err != nil || len(one) != 1 || one[0].LiveRef != live.String() || !one[0].FindingsUnavailable {
			t.Fatal("exact scoped evals selection lost identity or evidence limitation")
		}
	}
	legacy, err := (sessionsSampleAdapter{ss: ss}).Sample(ctx, tenant, evals.SampleQuery{SubjectKind: "session", SubjectRef: external.String()})
	if err != nil || len(legacy) != 0 {
		t.Fatal("legacy sampler borrowed a scoped row")
	}

	// Legacy evidence is not a finding on either scoped profile.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		lives, err := sc.Ext("sessions.live")
		if err != nil {
			return err
		}
		if _, err = lives.Create(ctx, model.Record{"session_ref": external.String(), "first_event_at": now, "last_event_at": now, "event_count": int64(1), "input_tokens": int64(0), "output_tokens": int64(0), "cost_micro_usd": int64(0), "tool_call_count": int64(0)}); err != nil {
			return err
		}
		_, err = sc.Findings().Create(ctx, model.Finding{Kind: "guardrail", Severity: model.SeverityHigh, Status: model.FindingOpen, Source: "legacy-review", SubjectKind: "session", SubjectID: external, Title: "legacy-only fixture", OccurredAt: model.NewTimestamp(time.Now())})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, live := range []model.ID{a, b} {
		rows, err := ss.SampleLive(ctx, tenant, sessions.LiveSampleQuery{LiveRef: live.String(), Limit: 10})
		if err != nil || len(rows) != 1 {
			t.Fatalf("scoped sample len=%d err=%v", len(rows), err)
		}
		t.Logf("exact live_ref legacy finding attribution: findings=%d severity=%s", rows[0].Findings, rows[0].MaxSeverity)
		if rows[0].Findings != 0 || !rows[0].FindingsUnavailable {
			t.Error("exact scoped SampleLive borrowed a legacy external-id finding")
		}
	}
}
