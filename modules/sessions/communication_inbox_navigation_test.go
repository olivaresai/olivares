// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCommunicationInboxNavigationTokenExpiryUnknownKeyAndTamper(t *testing.T) {
	t.Parallel()
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Unix(testCursorIssuedAt, 0).UTC()
	claims := communicationInboxNavigationClaims{
		tenantID: testCursorTenant, workspaceID: testCursorWorkspace,
		reader:     RecipientRef{Kind: RecipientUser, Ref: testCursorReader.String()},
		filterHash: filterHash[:], cursorID: testCursorID, cursorVersion: 7,
		baseDeliverySeq: 40, afterDeliverySeq: 41, deliveryID: testCursorDelivery,
	}
	ring := testCommunicationCursorTokenKeyring(
		t, testCursorKID, bytes.Repeat([]byte{0x52}, sha256.Size),
	)
	token, err := ring.mintInboxNavigation(claims, issuedAt)
	if err != nil || !strings.HasPrefix(token, "c2n1.") {
		t.Fatalf("mint navigation token = %q, %v", token, err)
	}
	got, err := ring.verifyInboxNavigation(token, issuedAt)
	if err != nil || got.tenantID != claims.tenantID || got.workspaceID != claims.workspaceID ||
		got.reader != claims.reader || got.cursorID != claims.cursorID ||
		got.cursorVersion != claims.cursorVersion || got.baseDeliverySeq != claims.baseDeliverySeq ||
		got.afterDeliverySeq != claims.afterDeliverySeq || got.deliveryID != claims.deliveryID {
		t.Fatalf("navigation round trip = %#v, %v", got, err)
	}
	if _, err := ring.verifyInboxNavigation(token,
		issuedAt.Add(communicationCursorTokenTTL+communicationCursorTokenClockSkew)); !errors.Is(err, errCommunicationCursorTokenExpired) {
		t.Fatalf("expired navigation = %v, want expired", err)
	}
	unknown := testCommunicationCursorTokenKeyring(
		t, "different-kid", bytes.Repeat([]byte{0x52}, sha256.Size),
	)
	if _, err := unknown.verifyInboxNavigation(token, issuedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("unknown navigation key = %v, want invalid", err)
	}
	tampered := token[:len(token)-1] + "A"
	if strings.HasSuffix(token, "A") {
		tampered = token[:len(token)-1] + "B"
	}
	if _, err := ring.verifyInboxNavigation(tampered, issuedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("tampered navigation = %v, want invalid", err)
	}
}

func TestCommunicationInboxNavigationTokenBindsExactMailboxAndLineage(t *testing.T) {
	t.Parallel()
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		t.Fatal(err)
	}
	valid := communicationInboxNavigationClaims{
		tenantID: testCursorTenant, workspaceID: testCursorWorkspace,
		reader:     RecipientRef{Kind: RecipientAgent, Ref: testCursorReader.String()},
		filterHash: filterHash[:], cursorID: testCursorID, cursorVersion: 3,
		baseDeliverySeq: 9, afterDeliverySeq: 10, deliveryID: testCursorDelivery,
		issuedAt:  time.Unix(testCursorIssuedAt, 0).UTC(),
		expiresAt: time.Unix(testCursorIssuedAt, 0).UTC().Add(communicationCursorTokenTTL),
	}
	for name, mutate := range map[string]func(*communicationInboxNavigationClaims){
		"position at durable base": func(value *communicationInboxNavigationClaims) { value.afterDeliverySeq = value.baseDeliverySeq },
		"zero target":              func(value *communicationInboxNavigationClaims) { value.deliveryID = "" },
		"virtual lineage with ID":  func(value *communicationInboxNavigationClaims) { value.cursorVersion = 0 },
		"missing mailbox":          func(value *communicationInboxNavigationClaims) { value.reader.Ref = "" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := validateCommunicationInboxNavigationClaims(candidate); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Errorf("%s = %v, want invalid", name, err)
		}
	}
}
