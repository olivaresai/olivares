// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkflowBindingAudienceEvidenceAtSameDatabaseTick(t *testing.T) {
	tick := time.Date(2026, 9, 28, 9, 0, 0, 123000000, time.UTC)
	source := &testClock{now: tick}
	clock := workflowBindingTestClock{source: source, engine: store.EngineSQLite}
	attestor := &directNoticeAudienceAttestor{epoch: 1}
	recipient := model.NewID().String()
	request := PublicationAudienceRequest{
		Scope:     DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()},
		ChannelID: model.NewID(), ChannelACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
		MessageKind: MessageWorkTask, Urgency: UrgencyNormal,
		Sender:     CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
		SourceKind: RouteSourceUserMessage, ChannelDefaultWake: WakeNone,
		ContentProtection: ContentProtectionStorage, ProtectionGeneration: 1,
		Selectors: []AudienceSelector{{Kind: AudienceUser, Ref: recipient, WakePolicy: WakeNone}},
	}
	for _, offset := range []time.Duration{0, time.Nanosecond, time.Millisecond - time.Nanosecond, time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			source.set(tick.Add(offset))
			request.RequestedAt = clock.Now().Time()
			snapshot, attestation, err := attestor.AttestPublicationAudience(context.Background(), request)
			if err != nil {
				t.Fatalf("attest fixture audience: %v", err)
			}
			dbNow := tick
			if offset == time.Millisecond {
				dbNow = tick.Add(time.Millisecond)
			}
			if err := validatePublicationAudienceAttestation(request, snapshot, attestation, dbNow); err != nil {
				t.Fatalf("same-tick audience rejected at %s: %v", dbNow, err)
			}
			if !communicationEvidenceCurrent(snapshot.ObservedAt, snapshot.FreshUntil, dbNow) {
				t.Fatal("same-tick evidence was rejected by the mutation freshness guard")
			}
			if !snapshot.ObservedAt.Equal(dbNow) {
				t.Fatalf("observation did not advance with the database tick: %s", snapshot.ObservedAt)
			}
			if communicationEvidenceCurrent(snapshot.ObservedAt, snapshot.FreshUntil, dbNow.Add(-time.Nanosecond)) {
				t.Fatal("future evidence was admitted")
			}
			if communicationEvidenceCurrent(snapshot.ObservedAt, snapshot.FreshUntil, snapshot.FreshUntil.Add(time.Nanosecond)) {
				t.Fatal("expired evidence was admitted")
			}
		})
	}
}

func TestWorkflowBindingPostgresClockRetainsItsPrecision(t *testing.T) {
	source := &testClock{now: time.Date(2026, 9, 28, 9, 0, 0, 123456000, time.UTC)}
	clock := workflowBindingTestClock{source: source, engine: store.EnginePostgres}
	if got := clock.Now().Time(); !got.Equal(source.get()) {
		t.Fatalf("PostgreSQL fixture clock changed: %s", got)
	}
}
