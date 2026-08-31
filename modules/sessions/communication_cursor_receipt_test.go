// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func communicationCursorReceiptFixture(t *testing.T) CommunicationCommandReceipt {
	t.Helper()
	completedAt := communicationTestNow
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(
			communicationStateTestScope(), completedAt,
		),
		CommandID:          model.NewID(),
		ActorFingerprint:   bytes.Repeat([]byte{0x61}, sha256.Size),
		CommandScope:       "inbox.cursor.advance",
		IdempotencyKeyHash: bytes.Repeat([]byte{0x62}, sha256.Size),
		RequestDigest:      bytes.Repeat([]byte{0x63}, sha256.Size),
		PlanHash:           bytes.Repeat([]byte{0x64}, sha256.Size),
		ResultKind:         string(inboxCursorKind),
		ResultID:           model.NewID(),
		HTTPStatus:         200,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			Version: 1,
			InboxCursor: &InboxCursorReceiptProjection{
				LastSeenSeq: 41,
			},
		},
		AuditSeq:    1,
		AuditHash:   bytes.Repeat([]byte{0x65}, sha256.Size),
		CompletedAt: completedAt,
	}
	communicationRebindCursorReceipt(t, &receipt)
	return receipt
}

func communicationRebindCursorReceipt(t *testing.T, receipt *CommunicationCommandReceipt) {
	t.Helper()
	binding, err := CanonicalCommunicationReceiptResponseBinding(*receipt)
	if err != nil {
		t.Fatalf("cursor receipt binding: %v", err)
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
}

func TestCommunicationCursorReceiptProjectionIsClosedAndBound(t *testing.T) {
	t.Parallel()

	receipt := communicationCursorReceiptFixture(t)
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		t.Fatalf("valid cursor receipt: %v", err)
	}

	withBarrier := receipt
	withBarrier.ResponseProjectionJSON.InboxCursor = &InboxCursorReceiptProjection{
		LastSeenSeq:       41,
		BarrierDeliveryID: model.NewID(),
		BarrierReason:     BarrierTemporarilyInvisible,
	}
	communicationRebindCursorReceipt(t, &withBarrier)
	if err := ValidateCommunicationCommandReceipt(withBarrier); err != nil {
		t.Fatalf("valid cursor receipt with barrier: %v", err)
	}

	changedWithoutDigest := withBarrier
	changedWithoutDigest.ResponseProjectionJSON.InboxCursor = &InboxCursorReceiptProjection{
		LastSeenSeq:       40,
		BarrierDeliveryID: withBarrier.ResponseProjectionJSON.InboxCursor.BarrierDeliveryID,
		BarrierReason:     withBarrier.ResponseProjectionJSON.InboxCursor.BarrierReason,
	}
	if err := ValidateCommunicationCommandReceipt(changedWithoutDigest); err == nil {
		t.Fatal("cursor response changed without invalidating the receipt digest")
	}
}

func TestCommunicationCursorReceiptProjectionRejectsCrossProducts(t *testing.T) {
	t.Parallel()

	base := communicationCursorReceiptFixture(t)
	mutations := map[string]func(*CommunicationCommandReceipt){
		"negative last seen": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor.LastSeenSeq = -1
		},
		"barrier id without reason": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor.BarrierDeliveryID = model.NewID()
		},
		"barrier reason without id": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor.BarrierReason = BarrierNotYetAvailable
		},
		"non-v7 barrier id": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor.BarrierDeliveryID =
				model.ID("550e8400-e29b-41d4-a716-446655440000")
			value.ResponseProjectionJSON.InboxCursor.BarrierReason = BarrierNotYetAvailable
		},
		"unknown barrier reason": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor.BarrierDeliveryID = model.NewID()
			value.ResponseProjectionJSON.InboxCursor.BarrierReason = "recipient_hidden"
		},
		"missing cursor projection": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.InboxCursor = nil
		},
		"missing result id": func(value *CommunicationCommandReceipt) {
			value.ResultID = ""
		},
		"wrong status": func(value *CommunicationCommandReceipt) {
			value.HTTPStatus = 201
		},
		"event id": func(value *CommunicationCommandReceipt) {
			value.EventID = model.NewID()
		},
		"zero cursor version": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.Version = 0
		},
		"state projection": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.State = string(DeliveryAvailable)
		},
		"id projection": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.IDs = map[string]model.ID{"result_id": value.ResultID}
		},
		"count projection": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.Counts = map[string]int64{"delivery_count": 1}
		},
		"digest projection": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.Digests = map[string][]byte{
				"response": bytes.Repeat([]byte{0x71}, sha256.Size),
			}
		},
		"cursor projection on another result": func(value *CommunicationCommandReceipt) {
			value.ResultKind = string(messageKind)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := base
			projection := *base.ResponseProjectionJSON.InboxCursor
			mutated.ResponseProjectionJSON.InboxCursor = &projection
			mutate(&mutated)
			communicationRebindCursorReceipt(t, &mutated)
			if err := ValidateCommunicationCommandReceipt(mutated); err == nil {
				t.Fatal("invalid cursor receipt cross-product was accepted")
			}
		})
	}
}

func TestCommunicationCursorReceiptCodecRoundTrip(t *testing.T) {
	t.Parallel()

	receipt := communicationCursorReceiptFixture(t)
	receipt.ResponseProjectionJSON.InboxCursor.BarrierDeliveryID = model.NewID()
	receipt.ResponseProjectionJSON.InboxCursor.BarrierReason = BarrierNotYetAvailable
	communicationRebindCursorReceipt(t, &receipt)
	record, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		t.Fatalf("encode cursor receipt: %v", err)
	}
	got, err := communicationCommandReceiptFromRecord(record)
	if err != nil {
		t.Fatalf("decode cursor receipt: %v", err)
	}
	if !reflect.DeepEqual(got, receipt) {
		t.Fatalf("cursor receipt round trip = %#v, want %#v", got, receipt)
	}
}
