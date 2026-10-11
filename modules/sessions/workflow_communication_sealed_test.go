// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestWorkflowSealedChannelPreservesCredentialAndPayload(t *testing.T) {
	t.Parallel()
	f := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "sealed-workflow"), false)
	ctx := context.Background()
	err := f.m.mutateCommunication(ctx, f.scope, func(tx *communicationTx) error {
		record, err := tx.lockRecord(ctx, channelKind, f.channel.ID)
		if err != nil {
			return err
		}
		before, err := channelFromRecord(record)
		if err != nil {
			return err
		}
		after := before
		after.ContentProtection = ContentProtectionApplicationSealed
		after.ProtectionGeneration++
		after.Version++
		after.UpdatedAt = tx.now.Time()
		if err := ValidateChannelUpdate(before, after); err != nil {
			return err
		}
		updated, err := channelToRecord(after)
		if err != nil {
			return err
		}
		updated[model.ColVersion] = before.Version
		_, err = tx.update(ctx, channelKind, updated)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sealer := &communicationContentTestSealer{
		sealVersion: "seal-v1", digestVersion: "digest-v1",
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	func() { f.m.CommunicationSealer = sealer; f.m.normalize() }()
	// Prepared publishes read the channel before their owning transaction creates work.
	if _, err := f.m.workflowCommunicationChannelScope(ctx, f.tenant, f.scope.WorkspaceID, f.channel.ID); err != nil {
		t.Errorf("prepare sealed channel: %v", err)
	}
	if _, err := f.m.workflowCommunicationChannelScope(ctx, f.tenant, model.NewID(), f.channel.ID); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("cross-workspace preparation = %v", err)
	}
	cmd := f.task(f.bound, "sealed-workflow")
	before := f.effectCounts()
	if err := f.authr.RevokeSession(ctx, f.authUser, f.authUser.CredID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.send(ctx, cmd); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
		t.Fatalf("revoked credential on sealed channel = %v", err)
	}
	f.requireNoEffect(before, "revoked credential")
	fresh := f.freshSession()
	successor, err := f.authr.RebindCredential(ctx, f.binding, 1, fresh, f.subject(f.runID), fresh)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Actor = f.actorFor(successor, f.runID)
	f.resync()
	func() { f.m.CommunicationSealer = nil; f.m.normalize() }()
	if _, err := f.send(ctx, cmd); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("missing sealing custody = %v", err)
	}
	f.requireNoEffect(before, "missing sealing custody")
	func() { f.m.CommunicationSealer = sealer; f.m.normalize() }()
	result, err := f.send(ctx, cmd)
	if err != nil {
		t.Fatalf("publish after reauthorization: %v", err)
	}
	message, err := messageFromRecord(workflowCommunicationRecord(t, f.workflowCommunicationFixture, messageKind, result.MessageID), 0)
	if err != nil {
		t.Fatal(err)
	}
	if message.Payload.Encoding != PayloadSealedV1 || len(message.Payload.PlainJSON) != 0 || message.Payload.Sealed == nil {
		t.Fatal("workflow payload was not sealed")
	}
	policy := ProtectedPayloadPolicy{Encoding: PayloadSealedV1, ProtectionGeneration: message.Payload.ProtectionGeneration}
	schema, _ := PayloadSlotMessage.schema()
	aad := ContentAAD{TenantID: f.tenant, WorkspaceID: f.scope.WorkspaceID, ChannelID: f.channel.ID,
		EntityKind: messageKind, EntityID: result.MessageID, Schema: schema, ProtectionGeneration: policy.ProtectionGeneration}
	plan, err := PlanProtectedPayloadOpen(message.Payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenProtectedPayload(ctx, sealer, plan)
	want, canonicalErr := CanonicalProtectedPayloadSlot(PayloadSlotMessage, cmd.Content)
	if err != nil || canonicalErr != nil || !bytes.Equal(opened, want) {
		t.Fatalf("sealed content did not round trip: open=%v canonical=%v", err, canonicalErr)
	}
	before = f.effectCounts()
	again, err := f.send(ctx, cmd)
	if err != nil || !again.Replayed || again.MessageID != result.MessageID || again.EventSeq != result.EventSeq {
		t.Fatalf("sealed replay = %+v, %v", again, err)
	}
	f.requireNoEffect(before, "sealed replay")
	f.requireLegacyUnused()
}
