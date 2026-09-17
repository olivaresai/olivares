// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// evidence_test.go — the module-side acceptance of the read glue: a case reads
// the exact separate records with their completeness, and reading evidence
// neither writes anything nor disturbs the AccessEdge projection.

const evidenceTestInstant = "2026-09-06T10:00:00.000000000Z"

func evidenceInstantAt(t *testing.T, offset time.Duration) string {
	t.Helper()
	base, err := model.ParseTimestamp(evidenceTestInstant)
	if err != nil {
		t.Fatalf("parse instant: %v", err)
	}
	return model.NewTimestamp(base.Time().Add(offset)).String()
}

func evidenceAppend(t *testing.T, eventType, sourceEventID string) store.AccessEvidenceAppend {
	t.Helper()
	return store.AccessEvidenceAppend{
		Envelope: sdk.AccessEvidenceEnvelope{
			SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
			ProducerInstance: "collector-a",
			SourceEventID:    sourceEventID,
			EventType:        eventType,
			AdapterVersion:   "1.0.0",
			OccurredAt:       evidenceInstantAt(t, 0),
		},
		Actor:     "collector-a",
		ActorKind: model.ActorSystem,
	}
}

func evidenceTestQuestion() sdk.AccessQuestion {
	return sdk.AccessQuestion{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ActorRef:         "agent-7",
		SourceInstance:   "pg-prod-1",
		ResourceKind:     "postgres.table",
		ResourceRef:      "public.customers",
		Action:           "SELECT",
		ActionVocabulary: "postgres.sql.v1",
	}
}

// seedEvidenceCase records a retained artifact, a decision that consumed it and
// an observation governed by that decision, and returns the observation id.
func seedEvidenceCase(t *testing.T, st store.Store, tenant model.TenantID) (model.ID, model.ID) {
	t.Helper()
	ctx := context.Background()
	body := "permit(principal, action, resource);"
	var observationID, decisionID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		artifact, err := ev.RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: evidenceAppend(t, sdk.EventTypePolicyArtifact, "src-artifact"),
			Artifact: sdk.PolicyArtifactContent{
				SchemaVersion:   sdk.AccessEvidenceSchemaVersion,
				AuthorityID:     "olivares.governance",
				Surface:         "cedar",
				Engine:          "cedar-3",
				ArtifactDigest:  sdk.ArtifactContentDigest([]byte(body)),
				DigestAlgorithm: sdk.ArtifactDigestAlgorithm,
				Origin:          sdk.OriginLocalAuthoritative,
				Availability:    sdk.AvailabilityRetained,
				Content:         body,
				ContentBytes:    int64(len(body)),
			},
		})
		if err != nil {
			return err
		}
		if _, err := ev.AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: evidenceAppend(t, sdk.EventTypeAuthorityTransition, "src-transition"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           artifact.Record.ID.String(),
				Transition:           sdk.TransitionActivate,
				EffectiveAt:          evidenceInstantAt(t, 0),
				KnownAt:              evidenceInstantAt(t, time.Minute),
				ReasonCode:           "revision.published",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		}); err != nil {
			return err
		}
		decision, err := ev.AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: evidenceAppend(t, sdk.EventTypeAuthorizationDecision, "src-decision"),
			Decision: sdk.AuthorizationDecisionContent{
				SchemaVersion:      sdk.AccessEvidenceSchemaVersion,
				Question:           evidenceTestQuestion(),
				Purpose:            sdk.PurposeLiveAuthorization,
				Evaluator:          "cedar",
				EvaluatorVersion:   "3.1.0",
				Outcome:            sdk.AccessOutcomeAllow,
				Disposition:        sdk.DispositionAllow,
				ReasonCode:         "policy.allow",
				AuthorizationPoint: "sourcescope.resolver",
				ReplayCompleteness: sdk.ReplayComplete,
				Inputs: []sdk.AccessDependency{{
					Kind: sdk.DependencyPolicyArtifact, Ref: artifact.Record.ID.String(), Required: true,
				}},
			},
		})
		if err != nil {
			return err
		}
		decisionID = decision.Record.ID

		blocked, err := ev.AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: evidenceAppend(t, sdk.EventTypeActionObservation, "src-observation"),
			Observation: sdk.ActionObservationContent{
				SchemaVersion: sdk.AccessEvidenceSchemaVersion,
				Question:      evidenceTestQuestion(),
				Stage:         sdk.StageRequested,
				Mediation:     sdk.MediationOlivaresPEP,
				Prevention:    sdk.PreventionCanPrevent,
				DecisionRef:   decisionID.String(),
			},
		})
		if err != nil {
			return err
		}
		observationID = blocked.Record.ID
		return nil
	}); err != nil {
		t.Fatalf("seed evidence case: %v", err)
	}
	return observationID, decisionID
}

// TestEvidenceCaseReadsTheSeparateRecords is the module-side round trip: the
// case carries the observation, the decision it was linked to, the retained
// artifact that decision consumed, and the store's own completeness verdict —
// each still a distinct record.
func TestEvidenceCaseReadsTheSeparateRecords(t *testing.T) {
	st, tenant := newStore(t)
	observationID, decisionID := seedEvidenceCase(t, st, tenant)

	m := New()
	m.UseData(api.NewModuleData(st))

	got, err := m.EvidenceCase(context.Background(), tenant, observationID)
	if err != nil {
		t.Fatalf("evidence case: %v", err)
	}
	if got.Observation.ID != observationID {
		t.Fatalf("case observation = %q, want %q", got.Observation.ID, observationID)
	}
	if !got.DecisionRecorded || got.Decision.ID != decisionID {
		t.Fatalf("case decision = %q recorded=%t, want %q", got.Decision.ID, got.DecisionRecorded, decisionID)
	}
	if len(got.Artifacts) != 1 || !got.Artifacts[0].Reconstructible() {
		t.Fatalf("case artifacts = %+v, want the one retained artifact", got.Artifacts)
	}
	if !got.Reconstructible() {
		t.Fatalf("case completeness = %+v, want reconstructible", got.Completeness)
	}
	// The stage stays what the producer recorded: a request, not an effect.
	if got.Observation.Observation.Stage != sdk.StageRequested {
		t.Fatalf("case stage = %q, want the recorded requested stage", got.Observation.Observation.Stage)
	}
	if got.Observation.Observation.Confirmation != "" {
		t.Fatalf("a requested stage carries confirmation %q", got.Observation.Observation.Confirmation)
	}

	stages, err := m.EvidenceStages(context.Background(), tenant, got.Observation.QuestionDigest)
	if err != nil {
		t.Fatalf("evidence stages: %v", err)
	}
	if len(stages) != 1 || stages[0].ID != observationID {
		t.Fatalf("stages = %d rows, want the one recorded", len(stages))
	}

	history, err := m.AuthorityHistory(context.Background(), tenant, got.Artifacts[0].ID.String())
	if err != nil {
		t.Fatalf("authority history: %v", err)
	}
	if len(history) != 1 || history[0].Transition.Transition != sdk.TransitionActivate {
		t.Fatalf("authority history = %+v, want the one activation", history)
	}
}

// TestEvidenceCaseWithoutADecisionIsAnAbsence proves the reader never turns a
// missing decision into a denial or into a permission: it reports that none was
// linked, and refuses to call the case reconstructible.
func TestEvidenceCaseWithoutADecisionIsAnAbsence(t *testing.T) {
	st, tenant := newStore(t)
	ctx := context.Background()

	var observationID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: evidenceAppend(t, sdk.EventTypeActionObservation, "src-ungoverned"),
			Observation: sdk.ActionObservationContent{
				SchemaVersion: sdk.AccessEvidenceSchemaVersion,
				Question:      evidenceTestQuestion(),
				Stage:         sdk.StageDispatched,
				Mediation:     sdk.MediationExternalObserver,
				Prevention:    sdk.PreventionCannotPrevent,
			},
		})
		observationID = res.Record.ID
		return err
	}); err != nil {
		t.Fatalf("seed ungoverned observation: %v", err)
	}

	m := New()
	m.UseData(api.NewModuleData(st))
	got, err := m.EvidenceCase(ctx, tenant, observationID)
	if err != nil {
		t.Fatalf("evidence case: %v", err)
	}
	if got.DecisionRecorded {
		t.Fatal("a case with no linked decision reported one")
	}
	if got.Reconstructible() {
		t.Fatal("a case with no decision reported itself reconstructible; there is nothing to replay")
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("case artifacts = %+v, want none", got.Artifacts)
	}
}

// TestEvidenceReadsDoNotTouchTheAccessEdgeProjection is the regression that
// keeps the two worlds apart: reading the evidence records changes no edge, and
// the module remains the sole writer of the edge graph through Ingest alone.
func TestEvidenceReadsDoNotTouchTheAccessEdgeProjection(t *testing.T) {
	st, tenant := newStore(t)
	ctx := context.Background()
	seedDiscoveredAgent(t, st, tenant)

	m := New()
	m.UseData(api.NewModuleData(st))
	if _, err := m.Ingest(ctx, tenant.String(),
		obs("agent", claudeAgentExt, "postgres.table", "public.customers",
			sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	before := findEdge(t, st, tenant, "public.customers")

	observationID, _ := seedEvidenceCase(t, st, tenant)
	if _, err := m.EvidenceCase(ctx, tenant, observationID); err != nil {
		t.Fatalf("evidence case: %v", err)
	}

	after := findEdge(t, st, tenant, "public.customers")
	if after.Version != before.Version || after.OccurrenceCount != before.OccurrenceCount {
		t.Fatalf("reading evidence moved the edge: version %d→%d, occurrences %d→%d",
			before.Version, after.Version, before.OccurrenceCount, after.OccurrenceCount)
	}
	if after.Permitted != before.Permitted || after.Observed != before.Observed {
		t.Fatalf("reading evidence changed the edge's permitted/observed flags: %+v → %+v", before, after)
	}
}

// TestEvidenceCaseWithoutDataHandleIsExplicit keeps the unwired case a named
// error rather than a nil dereference, matching Ingest's behaviour.
func TestEvidenceCaseWithoutDataHandleIsExplicit(t *testing.T) {
	m := New()
	if _, err := m.EvidenceCase(context.Background(), model.NewTenantID(), model.NewID()); err != errNoData {
		t.Fatalf("unwired EvidenceCase = %v, want errNoData", err)
	}
	if _, err := m.EvidenceStages(context.Background(), model.NewTenantID(), "digest"); err != errNoData {
		t.Fatalf("unwired EvidenceStages = %v, want errNoData", err)
	}
	if _, err := m.AuthorityHistory(context.Background(), model.NewTenantID(), "ref"); err != errNoData {
		t.Fatalf("unwired AuthorityHistory = %v, want errNoData", err)
	}
}
