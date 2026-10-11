// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// RecordAuthorization appends the final answer and immutable evaluated inputs
// through AccessEvidence, atomically with their ordinary chained-audit anchors.
// Its caller owns the request boundary; authorization never opens nested writes.
func (m *Module) RecordAuthorization(ctx context.Context, record auth.AuthorizationRecord) error {
	if m == nil || m.data == nil || record.Snapshot.Tenant.IsZero() || record.PrincipalRef == "" {
		return errors.New("governance: authorization recorder unavailable")
	}
	nativeVersion := "rbac-v1"
	for _, in := range record.Snapshot.Scoped {
		if in.Engine == retainedCedarScopeV1 || in.Engine == retainedCedarScopeV2 {
			var c retainedCedar
			if err := json.Unmarshal(in.Inputs, &c); err != nil {
				return err
			}
			nativeVersion = fmt.Sprintf("rbac-v1;cedar:%d;cedar-managed:%d;cedar-ddil:%d", c.Authored, c.Managed, c.Adopted)
		}
	}
	// Read only revision labels from the original input above. Redacted Cedar
	// source may no longer compile; it must be stored as incomplete, not parsed
	// again or repaired into a different evaluated policy.
	snapshot, err := redactRetainedAuthorization(record.Snapshot, record.EvidenceRedactor)
	if err != nil {
		return err
	}
	record.Snapshot = snapshot
	record.Actor = redactPDPText(record.EvidenceRedactor, record.Actor)
	record.ActorKind = redactPDPText(record.EvidenceRedactor, record.ActorKind)
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	content := string(b)
	question := retainedAuthorizationQuestion(record.Snapshot)
	if question.PrincipalRef != record.PrincipalRef {
		return errRetainedQuestion
	}
	// An ID needs its kind; collection checks do not fabricate a resource identity.
	if question.ResourceRef != "" && strings.TrimSpace(question.ResourceKind) == "" {
		return errors.New("governance: authorization resource kind unavailable")
	}
	outcome := retainedAccessOutcome(record.Outcome)
	completeness := sdk.ReplayIncomplete
	if record.Snapshot.Complete {
		completeness = sdk.ReplayComplete
	}
	reason, point := "authorization_"+string(outcome), "auth.final_pdp"
	if record.Snapshot.Precondition == "step_up_required" {
		reason, point = "authentication_step_up_required", "auth.authentication_precondition"
		completeness = sdk.ReplayIncomplete
	}
	if record.Snapshot.InputRedacted {
		reason = "input_redacted"
		completeness = sdk.ReplayIncomplete
	}
	appendFor := func(event string) store.AccessEvidenceAppend {
		return store.AccessEvidenceAppend{Actor: record.Actor, ActorKind: record.ActorKind,
			Envelope: sdk.AccessEvidenceEnvelope{SchemaVersion: sdk.AccessEvidenceSchemaVersion,
				ProducerInstance: "olivares", SourceEventID: model.NewID().String(), EventType: event,
				AdapterVersion: retainedAuthorizationEngine, OccurredAt: sdk.FormatEvidenceTime(record.At)}}
	}
	purpose := record.Purpose
	if purpose == "" {
		purpose = sdk.PurposeLiveAuthorization
	}
	if !purpose.Valid() {
		return errors.New("governance: authorization history purpose unavailable")
	}
	var dropped bool
	err = m.data.Mutate(ctx, record.Snapshot.Tenant, func(sc store.Scope) error {
		artifact, err := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendFor(sdk.EventTypePolicyArtifact),
			Artifact: sdk.PolicyArtifactContent{SchemaVersion: sdk.AccessEvidenceSchemaVersion,
				AuthorityID: "olivares", Surface: "authorization", Engine: retainedAuthorizationEngine,
				NativeVersion: nativeVersion, ArtifactDigest: sdk.ArtifactContentDigest(b),
				DigestAlgorithm: sdk.ArtifactDigestAlgorithm, Origin: sdk.OriginLocalAuthoritative,
				Availability: sdk.AvailabilityRetained, Content: content, ContentBytes: int64(len(b))},
		})
		if err != nil {
			return err
		}
		if artifact.Dropped {
			dropped = true
			return nil // commit the store's DEGRADE loss accounting
		}
		decision := sdk.AuthorizationDecisionContent{SchemaVersion: sdk.AccessEvidenceSchemaVersion,
			Question: question, Purpose: purpose,
			Evaluator: retainedAuthorizationEngine, EvaluatorVersion: "1", Outcome: outcome,
			ReasonCode: reason, AuthorizationPoint: point,
			ReplayCompleteness: completeness,
			Inputs: []sdk.AccessDependency{{Kind: sdk.DependencyPolicyArtifact, Ref: artifact.Record.ID.String(),
				Digest: artifact.Record.Artifact.ArtifactDigest, Required: true}},
		}
		decision, err = sdk.StampDecisionReconstructionFields(decision)
		if err != nil {
			return err
		}
		written, err := sc.AccessEvidence().AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendFor(sdk.EventTypeAuthorizationDecision), Decision: decision,
		})
		dropped = written.Dropped
		return err
	})
	if err == nil && dropped {
		return errors.New("governance: authorization history audit budget exhausted")
	}
	return err
}
