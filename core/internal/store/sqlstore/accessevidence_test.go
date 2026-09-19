// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// accessevidence_test.go — the engine-independent half of the access-evidence
// acceptance. Every case here runs on SQLite; accessevidence_pg_test.go runs the
// same behaviours against a real PostgreSQL with a separate owner role, because
// several of them (the immutability guard's ACL leg, the append-only revoke, two
// genuinely concurrent same-key ingests) can only be MEASURED there.

// evidenceClock is a fixed instant the fixtures build canonical timestamps from,
// so a record's digest does not change between runs.
const evidenceInstant = "2026-09-06T10:00:00.000000000Z"

func canonicalInstant(t *testing.T, offset time.Duration) string {
	t.Helper()
	base, err := model.ParseTimestamp(evidenceInstant)
	if err != nil {
		t.Fatalf("parse fixture instant: %v", err)
	}
	return model.NewTimestamp(base.Time().Add(offset)).String()
}

// artifactContent builds a RETAINED artifact whose declared digest is the real
// digest of its bytes — the shape the store accepts.
func artifactContent(body string) sdk.PolicyArtifactContent {
	return sdk.PolicyArtifactContent{
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
	}
}

// referencedArtifactContent builds an artifact held in a protected store: a
// durable reference and an explicit availability, with the content NOT here.
func referencedArtifactContent(digest string) sdk.PolicyArtifactContent {
	return sdk.PolicyArtifactContent{
		SchemaVersion:   sdk.AccessEvidenceSchemaVersion,
		AuthorityID:     "acme.iam",
		Surface:         "aws.iam",
		Engine:          "aws-iam",
		ArtifactDigest:  digest,
		DigestAlgorithm: "aws.policy.sha256",
		Origin:          sdk.OriginRemoteAuthenticatedSnapshot,
		Availability:    sdk.AvailabilityExternalRestricted,
		ContentRef:      "protected://vault/policies/" + digest,
	}
}

func evidenceQuestion() sdk.AccessQuestion {
	return sdk.AccessQuestion{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		PrincipalKind:    "user",
		PrincipalRef:     "u-42",
		Issuer:           "https://idp.example",
		CredentialClass:  "oidc",
		ActorRef:         "agent-7",
		SourceInstance:   "pg-prod-1",
		Endpoint:         "10.0.0.4:5432",
		Namespace:        "public",
		ResourceKind:     "postgres.table",
		ResourceRef:      "public.customers",
		Action:           "SELECT",
		ActionVocabulary: "postgres.sql.v1",
		Mode:             "read",
	}
}

func observationContent(stage sdk.ObservationStage) sdk.ActionObservationContent {
	return sdk.ActionObservationContent{
		SchemaVersion: sdk.AccessEvidenceSchemaVersion,
		Question:      evidenceQuestion(),
		Stage:         stage,
		Mediation:     sdk.MediationOlivaresPEP,
		Prevention:    sdk.PreventionCanPrevent,
	}
}

func decisionContent(inputs ...sdk.AccessDependency) sdk.AuthorizationDecisionContent {
	completeness := sdk.ReplayComplete
	if len(inputs) == 0 {
		completeness = sdk.ReplayUnknown
	}
	return sdk.AuthorizationDecisionContent{
		SchemaVersion:      sdk.AccessEvidenceSchemaVersion,
		Question:           evidenceQuestion(),
		Purpose:            sdk.PurposeLiveAuthorization,
		Evaluator:          "cedar",
		EvaluatorVersion:   "3.1.0",
		Outcome:            sdk.AccessOutcomeAllow,
		Disposition:        sdk.DispositionAllow,
		ReasonCode:         "policy.allow",
		AuthorizationPoint: "sourcescope.resolver",
		ReplayCompleteness: completeness,
		Inputs:             inputs,
	}
}

func envelope(t *testing.T, eventType, sourceEventID string) sdk.AccessEvidenceEnvelope {
	t.Helper()
	return sdk.AccessEvidenceEnvelope{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ProducerInstance: "collector-a",
		SourceEventID:    sourceEventID,
		EventType:        eventType,
		AdapterVersion:   "1.0.0",
		OccurredAt:       canonicalInstant(t, 0),
	}
}

func appendMeta(t *testing.T, eventType, sourceEventID string) store.AccessEvidenceAppend {
	t.Helper()
	return store.AccessEvidenceAppend{
		Envelope:  envelope(t, eventType, sourceEventID),
		Actor:     "collector-a",
		ActorKind: model.ActorSystem,
	}
}

// retainArtifact stores one retained artifact and returns it.
func retainArtifact(t *testing.T, st store.Store, tenant model.TenantID, sourceEventID, body string) model.PolicyArtifact {
	t.Helper()
	var out model.PolicyArtifact
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().RetainPolicyArtifact(context.Background(), store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, sourceEventID),
			Artifact:             artifactContent(body),
		})
		out = res.Record
		if err == nil && !res.Fresh {
			t.Fatalf("retain %q reported a duplicate on first delivery", sourceEventID)
		}
		return err
	})
	if err != nil {
		t.Fatalf("retain artifact %q: %v", sourceEventID, err)
	}
	return out
}

// TestAccessEvidenceRoundTripSQLite is the core acceptance: append the four
// separate records, reopen the database, and read back EXACTLY what was written
// with its dependency/completeness status.
//
// It uses a FILE database rather than :memory: on purpose — a round trip that
// never leaves the process proves serialization, not durability.
func TestAccessEvidenceRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "access-evidence.db")
	st := openSQLiteAt(t, dsn)
	tenant := provisionTenant(t, st, "evidence-roundtrip")

	artifact := retainArtifact(t, st, tenant, "src-artifact-1", `permit(principal, action, resource);`)
	if !artifact.Reconstructible() {
		t.Fatal("a retained artifact must report itself reconstructible")
	}

	var transitionID, observationID, decisionID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		tr, err := ev.AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorityTransition, "src-transition-1"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           artifact.ID.String(),
				Transition:           sdk.TransitionActivate,
				GovernanceSurface:    "cedar",
				GovernanceRevision:   7,
				AuthoritySequence:    1,
				EffectiveAt:          canonicalInstant(t, 0),
				KnownAt:              canonicalInstant(t, time.Minute),
				ReasonCode:           "revision.published",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		})
		if err != nil {
			return err
		}
		transitionID = tr.Record.ID

		dec, err := ev.AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-decision-1"),
			Decision: decisionContent(sdk.AccessDependency{
				Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(),
				Digest: artifact.Artifact.ArtifactDigest, Required: true,
			}),
		})
		if err != nil {
			return err
		}
		decisionID = dec.Record.ID

		content := observationContent(sdk.StageEffectConfirmed)
		content.Confirmation = sdk.ConfirmationDurableEffect
		content.DecisionRef = decisionID.String()
		obs, err := ev.AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-observation-1"),
			Observation:          content,
		})
		if err != nil {
			return err
		}
		observationID = obs.Record.ID
		return nil
	}); err != nil {
		t.Fatalf("append the four records: %v", err)
	}

	// Close and reopen: everything below is read from the file, not from memory.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := openSQLiteAt(t, dsn)

	if err := reopened.View(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()

		gotArtifact, err := ev.PolicyArtifact(ctx, artifact.ID)
		if err != nil {
			return fmt.Errorf("read artifact: %w", err)
		}
		if gotArtifact.Artifact.Content != artifact.Artifact.Content ||
			gotArtifact.RecordDigest != artifact.RecordDigest ||
			gotArtifact.LedgerRef == "" {
			t.Errorf("artifact round trip = %+v", gotArtifact)
		}
		if gotArtifact.Artifact.Availability != sdk.AvailabilityRetained {
			t.Errorf("artifact availability = %q, want retained", gotArtifact.Artifact.Availability)
		}

		gotTransition, err := ev.AuthorityTransition(ctx, transitionID)
		if err != nil {
			return fmt.Errorf("read transition: %w", err)
		}
		if gotTransition.SubjectArtifactID != artifact.ID {
			t.Errorf("transition subject = %q, want the resolved artifact %q", gotTransition.SubjectArtifactID, artifact.ID)
		}
		// The two temporal coordinates survive separately: effective_at is the
		// issuer's, known_at is ours, and reading one for the other is the defect
		// the pair exists to prevent.
		if gotTransition.Transition.EffectiveAt == gotTransition.Transition.KnownAt {
			t.Errorf("transition collapsed effective_at and known_at to %q", gotTransition.Transition.EffectiveAt)
		}

		gotObservation, err := ev.ActionObservation(ctx, observationID)
		if err != nil {
			return fmt.Errorf("read observation: %w", err)
		}
		if gotObservation.Observation.Confirmation != sdk.ConfirmationDurableEffect {
			t.Errorf("observation confirmation = %q, want durable_effect", gotObservation.Observation.Confirmation)
		}
		if gotObservation.DecisionID != decisionID {
			t.Errorf("observation decision = %q, want %q", gotObservation.DecisionID, decisionID)
		}
		if gotObservation.QuestionDigest == "" {
			t.Error("observation stored no question digest")
		}

		gotDecision, err := ev.AuthorizationDecision(ctx, decisionID)
		if err != nil {
			return fmt.Errorf("read decision: %w", err)
		}
		if gotDecision.Decision.Outcome != sdk.AccessOutcomeAllow || gotDecision.Decision.Purpose != sdk.PurposeLiveAuthorization {
			t.Errorf("decision round trip = %+v", gotDecision.Decision)
		}
		if gotDecision.QuestionDigest != gotObservation.QuestionDigest {
			t.Errorf("the same question digested differently for the observation (%s) and the decision (%s)",
				gotObservation.QuestionDigest, gotDecision.QuestionDigest)
		}

		completeness, err := ev.DecisionCompleteness(ctx, decisionID)
		if err != nil {
			return fmt.Errorf("completeness: %w", err)
		}
		if !completeness.Reconstructible() || completeness.Overclaimed() {
			t.Errorf("completeness over a retained artifact = %+v, want reconstructible", completeness)
		}

		stages, err := ev.ActionObservationsForQuestion(ctx, gotObservation.QuestionDigest)
		if err != nil {
			return fmt.Errorf("stages: %w", err)
		}
		if len(stages) != 1 || stages[0].ID != observationID {
			t.Errorf("stages for the question = %d rows, want exactly the one recorded", len(stages))
		}

		history, err := ev.AuthorityTransitionsFor(ctx, artifact.ID.String())
		if err != nil {
			return fmt.Errorf("history: %w", err)
		}
		if len(history) != 1 || history[0].ID != transitionID {
			t.Errorf("authority history = %d rows, want the one transition", len(history))
		}
		return nil
	}); err != nil {
		t.Fatalf("read back after reopen: %v", err)
	}
}

// TestAccessEvidenceRollbackLeavesNothing proves the append is genuinely part of
// the caller's transaction: a callback that fails AFTER a successful append
// leaves no record and no ledger event.
func TestAccessEvidenceRollbackLeavesNothing(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-rollback")

	sentinel := errors.New("caller aborted after the append")
	var staged model.ID
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		res, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-rolled-back"),
			Artifact:             artifactContent("permit(principal, action, resource);"),
		})
		if aerr != nil {
			return aerr
		}
		staged = res.Record.ID
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Mutate error = %v, want the caller's sentinel", err)
	}
	if staged.IsZero() {
		t.Fatal("the append did not stage a row to roll back")
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, gerr := sc.AccessEvidence().PolicyArtifact(ctx, staged)
		if !errors.Is(gerr, store.ErrNotFound) {
			t.Fatalf("after rollback the artifact reads back as %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("view after rollback: %v", err)
	}
}

// TestAccessEvidenceDuplicateDeliveryIsNotASecondFact covers at-least-once
// delivery: the identical record redelivered returns the recorded row, creates
// no second row and appends NO second ledger event.
//
// The ledger count is the part that matters. A duplicate that quietly re-anchors
// would inflate the evidence chain with events for facts that happened once.
func TestAccessEvidenceDuplicateDeliveryIsNotASecondFact(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-duplicate")

	first := retainArtifact(t, st, tenant, "src-dup", "permit(principal, action, resource);")
	before := auditEventCount(t, st, tenant)

	var second store.AccessEvidenceWrite[model.PolicyArtifact]
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var aerr error
		second, aerr = sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-dup"),
			Artifact:             artifactContent("permit(principal, action, resource);"),
		})
		return aerr
	}); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if second.Fresh {
		t.Fatal("an identical redelivery reported Fresh; it recorded a second fact")
	}
	if second.Record.ID != first.ID {
		t.Fatalf("redelivery returned %q, want the recorded row %q", second.Record.ID, first.ID)
	}
	if after := auditEventCount(t, st, tenant); after != before {
		t.Fatalf("an identical redelivery appended %d ledger events, want none", after-before)
	}
}

// TestAccessEvidenceConflictingRebindRefuses covers the other half of
// idempotency: the SAME identity carrying DIFFERENT content is a conflict, and
// the recorded row stands.
func TestAccessEvidenceConflictingRebindRefuses(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-conflict")

	first := retainArtifact(t, st, tenant, "src-conflict", "permit(principal, action, resource);")

	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-conflict"),
			Artifact:             artifactContent("forbid(principal, action, resource);"),
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceConflict) {
		t.Fatalf("same identity with different content = %v, want ErrAccessEvidenceConflict", err)
	}

	if verr := st.View(ctx, tenant, func(sc store.Scope) error {
		got, gerr := sc.AccessEvidence().PolicyArtifact(ctx, first.ID)
		if gerr != nil {
			return gerr
		}
		if got.Artifact.Content != "permit(principal, action, resource);" {
			t.Fatalf("the recorded artifact changed to %q", got.Artifact.Content)
		}
		return nil
	}); verr != nil {
		t.Fatalf("read the recorded artifact: %v", verr)
	}
}

// TestAccessEvidenceCrossedTenantReferenceRefuses proves the tenant boundary:
// tenant B may not link a transition to tenant A's artifact, and the refusal is
// "does not resolve" rather than an existence oracle.
func TestAccessEvidenceCrossedTenantReferenceRefuses(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenantA := provisionTenant(t, st, "evidence-tenant-a")
	tenantB := provisionTenant(t, st, "evidence-tenant-b")

	artifactA := retainArtifact(t, st, tenantA, "src-tenant-a", "permit(principal, action, resource);")

	err := st.Mutate(ctx, tenantB, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorityTransition, "src-crossed"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           artifactA.ID.String(),
				Transition:           sdk.TransitionActivate,
				EffectiveAt:          canonicalInstant(t, 0),
				KnownAt:              canonicalInstant(t, 0),
				ReasonCode:           "revision.published",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceDependencyMissing) {
		t.Fatalf("cross-tenant subject = %v, want ErrAccessEvidenceDependencyMissing", err)
	}

	// And the decision path refuses the same crossing through its inputs.
	err = st.Mutate(ctx, tenantB, func(sc store.Scope) error {
		content := decisionContent(sdk.AccessDependency{
			Kind: sdk.DependencyPolicyArtifact, Ref: artifactA.ID.String(), Required: true,
		})
		_, aerr := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-crossed-decision"),
			Decision:             content,
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceOverclaim) {
		t.Fatalf("cross-tenant required input = %v, want the completeness refusal", err)
	}
}

// TestAccessEvidenceMissingDependencyRefuses covers a reference that resolves
// nowhere at all.
func TestAccessEvidenceMissingDependencyRefuses(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-missing-dep")
	absent := model.NewID()

	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorityTransition, "src-missing"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           absent.String(),
				Transition:           sdk.TransitionRevoke,
				EffectiveAt:          canonicalInstant(t, 0),
				KnownAt:              canonicalInstant(t, 0),
				ReasonCode:           "issuer.revoked",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceDependencyMissing) {
		t.Fatalf("absent subject = %v, want ErrAccessEvidenceDependencyMissing", err)
	}

	// An observation naming a parent that does not exist is refused the same way,
	// rather than being stored with a dangling link.
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		content := observationContent(sdk.StageRequested)
		content.ParentRef = model.NewID().String()
		_, aerr := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-missing-parent"),
			Observation:          content,
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceDependencyMissing) {
		t.Fatalf("absent parent = %v, want ErrAccessEvidenceDependencyMissing", err)
	}
}

// TestAccessEvidenceIncompleteArtifactCannotBeClaimedComplete is the control for
// "a digest alone does not demonstrate reconstructibility": a decision whose
// required input is held elsewhere may be recorded as INCOMPLETE and may not be
// recorded as complete.
func TestAccessEvidenceIncompleteArtifactCannotBeClaimedComplete(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-incomplete")

	var restricted model.PolicyArtifact
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		res, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-restricted"),
			Artifact:             referencedArtifactContent("f00dcafe"),
		})
		restricted = res.Record
		return aerr
	}); err != nil {
		t.Fatalf("retain restricted artifact: %v", err)
	}
	if restricted.Reconstructible() {
		t.Fatal("an artifact held in a protected store reported itself locally reconstructible")
	}

	dep := sdk.AccessDependency{
		Kind: sdk.DependencyPolicyArtifact, Ref: restricted.ID.String(),
		Digest: restricted.Artifact.ArtifactDigest, Required: true,
	}

	// The overclaim is refused.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		content := decisionContent(dep)
		content.ReplayCompleteness = sdk.ReplayComplete
		_, aerr := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-overclaim"),
			Decision:             content,
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceOverclaim) {
		t.Fatalf("claiming complete over a non-retained input = %v, want ErrAccessEvidenceOverclaim", err)
	}

	// The honest claim is accepted, and the store's own verdict agrees.
	var honest model.ID
	if merr := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		content := decisionContent(dep)
		content.ReplayCompleteness = sdk.ReplayIncomplete
		res, aerr := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-honest"),
			Decision:             content,
		})
		honest = res.Record.ID
		return aerr
	}); merr != nil {
		t.Fatalf("record the honest claim: %v", merr)
	}
	if verr := st.View(ctx, tenant, func(sc store.Scope) error {
		c, cerr := sc.AccessEvidence().DecisionCompleteness(ctx, honest)
		if cerr != nil {
			return cerr
		}
		if c.Reconstructible() {
			t.Errorf("completeness = %+v, want not reconstructible", c)
		}
		if len(c.UnavailableRefs) != 1 || c.UnavailableRefs[0] != restricted.ID.String() {
			t.Errorf("unavailable refs = %v, want the restricted artifact", c.UnavailableRefs)
		}
		if len(c.MissingRefs) != 0 {
			t.Errorf("missing refs = %v, want none: the artifact resolved, its content is elsewhere", c.MissingRefs)
		}
		return nil
	}); verr != nil {
		t.Fatalf("read completeness: %v", verr)
	}
}

// TestAccessEvidenceRefusesUngroundedOperation proves the reuse of the existing
// journal: a record may name an operation only if the tenant's journal holds it,
// and a different effect digest is the journal's own rebind.
func TestAccessEvidenceRefusesUngroundedOperation(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "evidence-operation")

	content := observationContent(sdk.StageDispatched)
	content.OperationID = "op-not-journaled"
	content.EffectDigest = "digest-1"
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-ungrounded"),
			Observation:          content,
		})
		return aerr
	})
	if !errors.Is(err, store.ErrAccessEvidenceDependencyMissing) {
		t.Fatalf("observation naming an unjournaled operation = %v, want the missing-dependency refusal", err)
	}

	// Claim the operation through the EXISTING journal, unchanged.
	claim := store.EvidenceClaim{
		OperationID: "op-journaled", EffectDigest: "digest-1",
		Surface: "mcp.gateway", Action: "mcp.tool.call",
		Actor: "collector-a", ActorKind: model.ActorSystem,
	}
	if merr := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, cerr := sc.EvidenceOperations().Claim(ctx, claim)
		return cerr
	}); merr != nil {
		t.Fatalf("claim through the journal: %v", merr)
	}

	// The matching binding is accepted.
	content.OperationID = "op-journaled"
	if merr := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-grounded"),
			Observation:          content,
		})
		return aerr
	}); merr != nil {
		t.Fatalf("observation over a journaled operation: %v", merr)
	}

	// A different effect digest for the same operation is a rebind, reported with
	// the journal's OWN sentinel so a consumer classifies it exactly as before.
	content.EffectDigest = "digest-2"
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-rebind"),
			Observation:          content,
		})
		return aerr
	})
	if !errors.Is(err, store.ErrEvidenceRebind) {
		t.Fatalf("rebinding a journaled operation = %v, want store.ErrEvidenceRebind", err)
	}
}

// TestAccessEvidenceImmutabilityIsEnforcedByTheEngine goes UNDER the repository
// and issues raw UPDATE and DELETE against each relation.
//
// The repository exposes no mutate or delete method, but that is an API fact and
// not a guarantee: the guarantee is the append-only trigger the dialect emits
// from the descriptor, and a test that only called the repository would prove
// nothing about it.
func TestAccessEvidenceImmutabilityIsEnforcedByTheEngine(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "access-evidence-immutable.db")
	st := openSQLiteAt(t, dsn)
	tenant := provisionTenant(t, st, "evidence-immutable")
	// Every relation is seeded, and that is not tidiness: a row trigger fires per
	// ROW, so an UPDATE or DELETE against an EMPTY table succeeds trivially and
	// proves nothing about the guard. The first version of this test seeded only
	// the artifact table and duly reported the other three as unguarded.
	seedEveryAccessEvidenceRelation(t, st, tenant, "immutable")
	for _, table := range accessEvidenceTables() {
		if got := rawRowCount(t, st, tenant, table); got == 0 {
			t.Fatalf("%s is empty; an UPDATE against it would prove nothing", table)
		}
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close before raw access: %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close() //nolint:errcheck // test cleanup

	for _, table := range accessEvidenceTables() {
		if _, err := db.ExecContext(ctx, "UPDATE "+table+" SET record_digest = 'tampered'"); err == nil {
			t.Errorf("%s accepted an UPDATE; the append-only guard is not attached", table)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err == nil {
			t.Errorf("%s accepted a DELETE; the append-only guard is not attached", table)
		}
	}
}

// TestAccessEvidenceMigratesOntoAnExistingDatabase stages the condition an
// upgrading v26.8 deployment is in — a live database with real data whose schema
// predates these four relations — and proves the next boot creates them WHOLE,
// with their guards, without disturbing what was already there.
//
// WHAT THE STAGING HAD TO BECOME, and why the change is a strengthening. It used to drop
// the four relations from a completed database and let reconcileColumns recreate them.
// That path no longer exists and must not: the relations are append-only, so their
// presence is part of the guard census, and creating them in the generic reconciler would
// have meant a database whose ledger says one edition and whose census says another. Core
// v9 owns their creation, and once v9 is recorded a missing relation is DAMAGE to report
// rather than growth to converge — which is what TestAccessEvidenceCompletedV9DamageIsReportedNotRepaired
// measures on the same shape.
//
// So the fixture stages the real predecessor state instead: the relations absent, v9 not
// recorded, and the guard ledger standing at the edition this build's base census had
// BEFORE the access-evidence delta. That is the `guarded-legacy-pending-v9` start class,
// and the boot below crosses the exact E2 -> E5 edge for it.
func TestAccessEvidenceMigratesOntoAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "access-evidence-upgrade.db")
	st := openSQLiteAt(t, dsn)
	tenant := provisionTenant(t, st, "evidence-upgrade")

	// Pre-existing v26.8 evidence: an access edge and a journal row.
	agent := mustCreateAgent(t, st, tenant, "legacy-agent")
	var resource model.Resource
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		r, rerr := sc.Resources().Create(ctx, model.Resource{Name: "customers", Kind: "postgres.table"})
		resource = r
		return rerr
	}); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, uerr := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
			OriginKind: "agent", OriginID: agent.ID, ResourceID: resource.ID,
			Mode: "read", SignalSource: "otel", Confidence: "attributed",
			Observed: true, FirstSeen: model.NewTimestamp(time.Now()), LastSeen: model.NewTimestamp(time.Now()),
		})
		return uerr
	}); err != nil {
		t.Fatalf("seed access edge: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close before downgrade: %v", err)
	}

	// Stage the pre-descriptor schema AND the edition that goes with it.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		raw.Close() //nolint:errcheck // test cleanup on a failure path
		t.Fatal("SQLite dialect unavailable")
	}
	// The staged predecessor is pre-v9, so it is pre-v10 as well: core v10 requires a
	// tracked v9 and could not have run. Undoing it whole — H, its two guards, the
	// four-column control and the protocol-aware source triggers, back to the frozen
	// v9 shapes — is what makes the history below a contiguous [1..8] prefix rather
	// than the [1..8, 10] an edited tracker would show.
	dropUserAuthorityForHistoricalFixture(t, raw, dia)
	for _, table := range accessEvidenceTables() {
		if _, err := raw.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			raw.Close() //nolint:errcheck // test cleanup on a failure path
			t.Fatalf("stage pre-descriptor schema (%s): %v", table, err)
		}
	}
	if _, err := raw.ExecContext(ctx, dia.Rebind(
		"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
		coreAccessEvidenceMigrationVersion); err != nil {
		raw.Close() //nolint:errcheck // test cleanup on a failure path
		t.Fatalf("stage the pre-v9 migration history: %v", err)
	}
	// The guard ledger is rewound to the edition a pre-access-evidence build of this
	// same base census recorded: its bootstrap plus its v7 completion, and nothing else.
	for _, table := range []string{
		guardGateEventsTable, guardReceiptsTable, guardInventoryEventsTable,
	} {
		wipeSQLiteGuardLogForFixture(t, raw, table)
	}
	predecessor := coreOnlyCurrentGuardManifest(t)
	seedGuardHistoricalEdition(t, raw, dia, predecessor)
	appendGuardV7Witness(t, raw, dia, predecessor, false)
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// The upgrade boot.
	upgraded := openSQLiteAt(t, dsn)
	if err := upgraded.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-after-upgrade"),
			Artifact:             artifactContent("permit(principal, action, resource);"),
		})
		return aerr
	}); err != nil {
		t.Fatalf("append after the upgrade boot: %v", err)
	}
	// The pre-existing evidence is untouched.
	if err := upgraded.View(ctx, tenant, func(sc store.Scope) error {
		edges, eerr := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "agent", ID: agent.ID}, model.Outgoing)
		if eerr != nil {
			return eerr
		}
		if len(edges) != 1 || edges[0].ResourceID != resource.ID {
			t.Fatalf("the pre-existing access edge did not survive the upgrade: %+v", edges)
		}
		return nil
	}); err != nil {
		t.Fatalf("read the pre-existing edge: %v", err)
	}
	// And the upgrade really crossed the compiled edge rather than growing a table: the
	// history is the transitioned E2 -> E5 lineage, over the predecessor's own receipts.
	verifier, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close() //nolint:errcheck // test cleanup
	dia2, _ := dialect.New(store.EngineSQLite)
	history, err := verifyGuardEditionHistory(ctx, verifier, dia2, coreOnlyAccessEvidenceManifest(t))
	if err != nil {
		t.Fatalf("verify the upgraded edition: %v", err)
	}
	if history.Kind != guardEditionHistoryTransitioned || history.Path != "2>5" ||
		history.ParentEpoch != 2 || !history.CompletedV9 {
		t.Fatalf("upgraded history = %s/path-%s parent %d completedV9=%t, want transitioned/path-2>5",
			history.Kind, history.Path, history.ParentEpoch, history.CompletedV9)
	}
}

// TestAccessEvidenceStoreMintsNoCapability is the durable form of "a stored
// evidence descriptor is data, never an authorization witness".
//
// It parses the two contract files and fails if any exported function or method
// returns an sdk.EvidenceReceipt or an authorization witness. A comment could
// not hold this: the tempting future change is a convenience helper that turns a
// recorded allow into a receipt, and it would look reasonable at the call site.
func TestAccessEvidenceStoreMintsNoCapability(t *testing.T) {
	t.Parallel()
	forbidden := []string{"EvidenceReceipt", "RouteAuthorizationWitness", "ReadDecisionWitness"}
	files := []string{
		filepath.Join("..", "..", "..", "store", "accessevidence.go"),
		"accessevidence.go",
		filepath.Join("..", "..", "..", "model", "accessevidence.go"),
	}
	for _, path := range files {
		src, err := os.ReadFile(path) //nolint:gosec // a fixed in-repo path
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Type.Results == nil {
				return true
			}
			for _, result := range fn.Type.Results.List {
				rendered := renderTypeExpr(result.Type)
				for _, bad := range forbidden {
					if strings.Contains(rendered, bad) {
						t.Errorf("%s: %s returns %s; a stored record is data and must never be handed back as a capability",
							path, fn.Name.Name, rendered)
					}
				}
			}
			return true
		})
	}
}

// renderTypeExpr renders a type expression well enough to name the type being
// returned. It handles the shapes these files use (identifiers, qualified
// identifiers, pointers, slices and generic instantiations).
func renderTypeExpr(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return renderTypeExpr(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + renderTypeExpr(t.X)
	case *ast.ArrayType:
		return "[]" + renderTypeExpr(t.Elt)
	case *ast.IndexExpr:
		return renderTypeExpr(t.X) + "[" + renderTypeExpr(t.Index) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, len(t.Indices))
		for i, idx := range t.Indices {
			parts[i] = renderTypeExpr(idx)
		}
		return renderTypeExpr(t.X) + "[" + strings.Join(parts, ",") + "]"
	default:
		return fmt.Sprintf("%T", e)
	}
}

// TestAccessEvidenceRejectsMalformedRecords collects the write-side refusals
// that keep an unusable or overclaiming record out of the store. Each case names
// what would otherwise be recorded as true.
func TestAccessEvidenceRejectsMalformedRecords(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "evidence-malformed")

	oversized := strings.Repeat("a", model.MaxPolicyArtifactBytes+1)

	cases := []struct {
		name string
		run  func(sc store.Scope) error
		want error
	}{
		{
			name: "a retained artifact larger than the ceiling is refused, not shortened",
			run: func(sc store.Scope) error {
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-oversize"),
					Artifact:             artifactContent(oversized),
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a retained artifact whose bytes do not match its declared digest is refused",
			run: func(sc store.Scope) error {
				content := artifactContent("permit(principal, action, resource);")
				content.Content = "forbid(principal, action, resource);"
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-mismatch"),
					Artifact:             content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "an artifact declared absent may not smuggle content",
			run: func(sc store.Scope) error {
				content := artifactContent("permit(principal, action, resource);")
				content.Availability = sdk.AvailabilityAbsent
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-absent"),
					Artifact:             content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a policy artifact carrying an inline provider credential is refused",
			run: func(sc store.Scope) error {
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-secret"),
					Artifact:             artifactContent(`{"apiKey":"sk-ant-live"}`),
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "an external observer may not claim it could have prevented the stage",
			run: func(sc store.Scope) error {
				content := observationContent(sdk.StageDispatched)
				content.Mediation = sdk.MediationExternalObserver
				content.Prevention = sdk.PreventionCanPrevent
				_, err := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-observer"),
					Observation:          content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a confirmed effect must say what the protocol confirmed",
			run: func(sc store.Scope) error {
				_, err := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-unqualified"),
					Observation:          observationContent(sdk.StageEffectConfirmed),
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a stage that confirms nothing may not carry a confirmation level",
			run: func(sc store.Scope) error {
				content := observationContent(sdk.StageDispatched)
				content.Confirmation = sdk.ConfirmationDurableEffect
				_, err := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-overqualified"),
					Observation:          content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a shadow decision has no effective disposition",
			run: func(sc store.Scope) error {
				content := decisionContent()
				content.Shadow = true
				content.Disposition = sdk.DispositionDeny
				_, err := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-shadow"),
					Decision:             content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a reconstruction may not bind the operation it did not govern",
			run: func(sc store.Scope) error {
				content := decisionContent()
				content.Purpose = sdk.PurposeHistoricalReconstruction
				content.OperationID = "op-1"
				content.EffectDigest = "digest-1"
				_, err := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "src-reconstruction"),
					Decision:             content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "an envelope from an unimplemented contract version is refused, not reinterpreted",
			run: func(sc store.Scope) error {
				in := store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "src-future"),
					Artifact:             artifactContent("permit(principal, action, resource);"),
				}
				in.Envelope.SchemaVersion = sdk.AccessEvidenceSchemaVersion + 1
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, in)
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "an envelope whose event type contradicts its content is refused",
			run: func(sc store.Scope) error {
				in := store.PolicyArtifactAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-mislabelled"),
					Artifact:             artifactContent("permit(principal, action, resource);"),
				}
				_, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, in)
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
		{
			name: "a resource named without its origin scope is refused",
			run: func(sc store.Scope) error {
				content := observationContent(sdk.StageRequested)
				content.Question.SourceInstance = ""
				_, err := sc.AccessEvidence().AppendActionObservation(ctx, store.ActionObservationAppend{
					AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "src-unscoped"),
					Observation:          content,
				})
				return err
			},
			want: store.ErrAccessEvidenceInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return tc.run(sc) })
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestAccessEvidenceDescriptorsAreAppendOnlyAndRegistered pins the schema
// decisions the guards depend on. It is cheap and it catches the one edit that
// would silently remove every immutability guarantee: dropping AppendOnly from a
// descriptor.
func TestAccessEvidenceDescriptorsAreAppendOnlyAndRegistered(t *testing.T) {
	t.Parallel()
	registered := make(map[model.Kind]model.EntityDescriptor)
	for _, d := range coreDescriptors() {
		registered[d.Kind] = d
	}
	for _, want := range accessEvidenceDescriptors() {
		got, ok := registered[want.Kind]
		if !ok {
			t.Fatalf("%s is not in coreDescriptors(); it would never be created", want.Kind)
		}
		if !got.AppendOnly {
			t.Errorf("%s is not AppendOnly: its immutability guard, its append-only ACL and its retention on tenant drop all follow from that flag", want.Kind)
		}
		if got.SoftDelete {
			t.Errorf("%s declares soft delete; evidence is not deletable", want.Kind)
		}
		if len(got.Checks) == 0 {
			t.Errorf("%s declares no CHECK vocabulary; an out-of-band write could plant an unknown value", want.Kind)
		}
		var hasIngestIndex bool
		for _, ix := range got.Indexes {
			if ix.Unique && strings.HasSuffix(ix.Name, "_ingest_uniq") {
				hasIngestIndex = true
			}
		}
		if !hasIngestIndex {
			t.Errorf("%s has no unique ingest index; idempotency would rest on a read that races", want.Kind)
		}
	}
}

// TestAccessEvidenceTimeLayoutMatchesTheStore pins the equality the digest
// depends on. The sdk formats normative instants as strings so a digest survives
// storage; if the engine's canonical layout drifted from the sdk's, a record
// would hash differently after a round trip and every stored record would read
// as tampered.
func TestAccessEvidenceTimeLayoutMatchesTheStore(t *testing.T) {
	t.Parallel()
	instant := time.Date(2026, 9, 6, 10, 0, 0, 123456789, time.UTC)
	if got, want := sdk.FormatEvidenceTime(instant), model.NewTimestamp(instant).String(); got != want {
		t.Fatalf("sdk instant %q != store instant %q", got, want)
	}
}

// openSQLiteAt opens a FILE-backed SQLite store. It exists because several
// access-evidence cases have to close and reopen the database — durability and
// the append-only guard are properties of what is on disk, not of what a process
// happens to hold.
func openSQLiteAt(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, MaxConns: 1, Debug: true,
	}, nil)
	if err != nil {
		t.Fatalf("open sqlite at %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// auditEventCount returns the tenant's chain tip sequence — the number of ledger
// events appended so far. It is how a duplicate delivery is shown to append
// nothing: comparing rows would miss a re-anchor that wrote an event and then
// resolved to the recorded row.
func auditEventCount(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err != nil {
			return err
		}
		if ok {
			seq = head.Seq
		}
		return nil
	}); err != nil {
		t.Fatalf("read audit head: %v", err)
	}
	return seq
}

// accessEvidenceTables names the four relations, in creation order.
func accessEvidenceTables() []string {
	return []string{
		policyArtifactTable, authorityTransitionTable, actionObservationTable, authorizationDecisionTable,
	}
}

// seedEveryAccessEvidenceRelation writes one record into each of the four
// relations, linked as a real case is: a transition over the artifact, a
// decision consuming it, and an observation governed by that decision.
func seedEveryAccessEvidenceRelation(t *testing.T, st store.Store, tenant model.TenantID, prefix string) {
	t.Helper()
	ctx := context.Background()
	artifact := retainArtifact(t, st, tenant, prefix+"-artifact", "permit(principal, action, resource);")
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		if _, err := ev.AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorityTransition, prefix+"-transition"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           artifact.ID.String(),
				Transition:           sdk.TransitionActivate,
				EffectiveAt:          canonicalInstant(t, 0),
				KnownAt:              canonicalInstant(t, time.Minute),
				ReasonCode:           "revision.published",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		}); err != nil {
			return err
		}
		decision, err := ev.AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, prefix+"-decision"),
			Decision: decisionContent(sdk.AccessDependency{
				Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(), Required: true,
			}),
		})
		if err != nil {
			return err
		}
		content := observationContent(sdk.StageDispatched)
		content.DecisionRef = decision.Record.ID.String()
		_, err = ev.AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, prefix+"-observation"),
			Observation:          content,
		})
		return err
	}); err != nil {
		t.Fatalf("seed the access-evidence relations: %v", err)
	}
}

// rawRowCount counts a relation's rows for the pinned tenant through the store's
// own scope, so the count respects the same tenant boundary every read does.
func rawRowCount(t *testing.T, st store.Store, tenant model.TenantID, table string) int {
	t.Helper()
	var descriptor model.EntityDescriptor
	for _, d := range accessEvidenceDescriptors() {
		if d.Table == table {
			descriptor = d
		}
	}
	if descriptor.Table == "" {
		t.Fatalf("unknown access-evidence relation %q", table)
	}
	var n int
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		recs, _, err := sc.(*tenantScope).repo(descriptor).List(context.Background(), model.Query{Limit: maxLimit})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func assertDecisionReconstructionStamp(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	artifact := retainArtifact(t, st, tenant, "stamp-artifact", "permit(principal, action, resource);")
	var decision model.AuthorizationDecision
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "stamp-decision"),
			Decision:             decisionContent(sdk.AccessDependency{Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(), Required: true}),
		})
		decision = res.Record
		return err
	}); err != nil {
		t.Fatalf("append decision: %v", err)
	}
	if decision.Decision.PolicyVersionID != artifact.ID.String() {
		t.Fatalf("policy_version_id = %q, want artifact %q (not an authoring revision, not unknown)",
			decision.Decision.PolicyVersionID, artifact.ID)
	}
	if !decision.InputsDigestKnown() {
		t.Fatal("new decision row must carry inputs_digest")
	}
	digest, err := sdk.AccessInputsDigest(decision.Decision.Inputs)
	if err != nil {
		t.Fatalf("inputs digest: %v", err)
	}
	if decision.Decision.InputsDigest != digest {
		t.Fatalf("stored inputs_digest %q != recomputed %q", decision.Decision.InputsDigest, digest)
	}

	qdig, err := evidenceQuestion().Digest()
	if err != nil {
		t.Fatalf("question digest: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rows, err := sc.AccessEvidence().AuthorizationDecisionsForQuestion(ctx, qdig)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ID != decision.ID {
			t.Errorf("decisions for question = %d rows, want the stamped row", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("list by question: %v", err)
	}
}

func TestDecisionReconstructionStampSQLite(t *testing.T) {
	st := openSQLiteAt(t, filepath.Join(t.TempDir(), "stamp.db"))
	tenant := provisionTenant(t, st, "stamp-sqlite")
	assertDecisionReconstructionStamp(t, st, tenant)
}

func TestLegacyDecisionJSONStaysUnknownOnRead(t *testing.T) {
	// The "migration" for old rows is honesty, not backfill: a content document
	// that never named a policy version remains unknown after decode. Fabricating
	// the live authoring revision would be the defect this test exists to catch.
	raw := []byte(`{"schema_version":1,"question":{"schema_version":1,"actor_ref":"a","action":"read"},"purpose":"live_authorization","evaluator":"cedar","evaluator_version":"3","outcome":"allow","reason_code":"x","replay_completeness":"unknown","authorization_point":"t"}`)
	var d sdk.AuthorizationDecisionContent
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	rec := model.AuthorizationDecision{Decision: d}
	if rec.PolicyVersionKnown() || rec.InputsDigestKnown() {
		t.Fatalf("legacy row must stay unknown, got %+v", rec.Decision)
	}
}
