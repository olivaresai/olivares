// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func newChannelCatalogNavigationKeyring(t *testing.T, kid string) *communicationCursorTokenKeyring {
	t.Helper()
	ring, err := newCommunicationCursorTokenKeyring(kid, []communicationCursorTokenKey{{
		kid: kid, material: bytes.Repeat([]byte{0x5a}, 32),
	}})
	if err != nil {
		t.Fatalf("catalog navigation keyring: %v", err)
	}
	return ring
}

func channelCatalogNavigationTestClaims(t *testing.T) communicationChannelCatalogNavigationClaims {
	t.Helper()
	filter := channelCatalogFilterHash()
	return communicationChannelCatalogNavigationClaims{
		tenantID: model.TenantID(model.NewID()), workspaceID: model.NewID(),
		reader:     RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		filterHash: filter[:], lastChannelID: model.NewID(),
	}
}

// TestChannelCatalogNavigationTokenDisclosesOnlyTheReturnedAnchor decodes the
// token's JSON segment WITHOUT the key, as anyone holding the token can, and
// proves every claim is disclosure-safe: family/version, tenant, workspace, the
// canonical reader binding, the filter hash and the last RETURNED Channel. No
// hidden Channel ID, grant ID, count, store cursor or existence flag is present.
func TestChannelCatalogNavigationTokenDisclosesOnlyTheReturnedAnchor(t *testing.T) {
	t.Parallel()
	ring := newChannelCatalogNavigationKeyring(t, "k3cat")
	claims := channelCatalogNavigationTestClaims(t)
	issuedAt := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	token, err := ring.mintChannelCatalogNavigation(claims, issuedAt)
	if err != nil {
		t.Fatalf("mint catalog navigation: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c3n1" {
		t.Fatalf("token shape = %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode claims segment without the key: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("claims JSON: %v", err)
	}
	want := map[string]any{
		"v": float64(1), "ten": claims.tenantID.String(), "ws": claims.workspaceID.String(),
		"rk": "user", "rr": claims.reader.Ref,
		"fh":  base64.RawURLEncoding.EncodeToString(claims.filterHash),
		"lc":  claims.lastChannelID.String(),
		"iat": float64(issuedAt.Unix()), "exp": float64(issuedAt.Add(communicationCursorTokenTTL).Unix()),
	}
	if len(decoded) != len(want) {
		t.Fatalf("claim set = %v, want exactly %v", decoded, want)
	}
	for key, value := range want {
		if decoded[key] != value {
			t.Fatalf("claim %q = %v, want %v", key, decoded[key], value)
		}
	}
	for _, forbidden := range []string{"seq", "after", "cursor", "count", "hidden", "grant", "did", "cid", "base"} {
		if _, present := decoded[forbidden]; present {
			t.Fatalf("token discloses %q", forbidden)
		}
	}
	got, err := ring.verifyChannelCatalogNavigation(token, issuedAt.Add(time.Minute))
	if err != nil || got.lastChannelID != claims.lastChannelID || got.reader != claims.reader ||
		got.tenantID != claims.tenantID || got.workspaceID != claims.workspaceID {
		t.Fatalf("verify = %+v, %v", got, err)
	}
}

func TestChannelCatalogNavigationTokenRejectsTamperExpiryFamilyKeyAndScope(t *testing.T) {
	t.Parallel()
	ring := newChannelCatalogNavigationKeyring(t, "k3cat")
	claims := channelCatalogNavigationTestClaims(t)
	issuedAt := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	token, err := ring.mintChannelCatalogNavigation(claims, issuedAt)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[2])

	reencode := func(mutate func(map[string]any)) string {
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		mutate(wire)
		out, _ := json.Marshal(wire)
		return parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(out) + "." + parts[3]
	}
	cases := map[string]string{
		"tampered anchor": reencode(func(w map[string]any) { w["lc"] = model.NewID().String() }),
		"tampered tenant": reencode(func(w map[string]any) { w["ten"] = model.NewID().String() }),
		"tampered reader": reencode(func(w map[string]any) { w["rr"] = model.NewID().String() }),
		"tampered filter": reencode(func(w map[string]any) {
			w["fh"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
		}),
		"extended expiry":       reencode(func(w map[string]any) { w["exp"] = float64(issuedAt.Add(time.Hour).Unix()) }),
		"non-canonical padding": parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(append(raw, ' ')) + "." + parts[3],
		"inbox family":          "c2n1." + parts[1] + "." + parts[2] + "." + parts[3],
		"missing segment":       parts[0] + "." + parts[1] + "." + parts[2],
		"tampered mac":          parts[0] + "." + parts[1] + "." + parts[2] + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	}
	for name, mutated := range cases {
		if _, err := ring.verifyChannelCatalogNavigation(mutated, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if _, err := ring.verifyChannelCatalogNavigation(token, issuedAt.Add(communicationCursorTokenTTL+communicationCursorTokenClockSkew)); !errors.Is(err, errCommunicationCursorTokenExpired) {
		t.Fatalf("expired token accepted: %v", err)
	}
	if _, err := ring.verifyChannelCatalogNavigation(token, issuedAt.Add(-2*communicationCursorTokenClockSkew)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("not-yet-valid token accepted: %v", err)
	}
	unknown := newChannelCatalogNavigationKeyring(t, "other")
	if _, err := unknown.verifyChannelCatalogNavigation(token, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("unknown KID accepted: %v", err)
	}
	// An inbox token of the same keyring is a different family and domain: the
	// catalog verifier rejects it even though the MAC key is shared.
	inboxFilter, err := directNoticeCursorFilterHash()
	if err != nil {
		t.Fatalf("inbox filter hash: %v", err)
	}
	inbox, err := ring.mintInboxNavigation(communicationInboxNavigationClaims{
		tenantID: claims.tenantID, workspaceID: claims.workspaceID, reader: claims.reader,
		filterHash: inboxFilter[:], afterDeliverySeq: 3, deliveryID: model.NewID(),
	}, issuedAt)
	if err != nil {
		t.Fatalf("mint inbox token: %v", err)
	}
	if _, err := ring.verifyChannelCatalogNavigation(inbox, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("inbox family token accepted by the catalog verifier: %v", err)
	}
	inboxParts := strings.Split(inbox, ".")
	spliced := "c3n1." + inboxParts[1] + "." + inboxParts[2] + "." + inboxParts[3]
	if _, err := ring.verifyChannelCatalogNavigation(spliced, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("family-spliced inbox token accepted: %v", err)
	}
	// Malformed claims never mint.
	for name, bad := range map[string]communicationChannelCatalogNavigationClaims{
		"zero anchor":    {tenantID: claims.tenantID, workspaceID: claims.workspaceID, reader: claims.reader, filterHash: claims.filterHash},
		"foreign filter": {tenantID: claims.tenantID, workspaceID: claims.workspaceID, reader: claims.reader, filterHash: bytes.Repeat([]byte{2}, 32), lastChannelID: claims.lastChannelID},
		"no reader":      {tenantID: claims.tenantID, workspaceID: claims.workspaceID, filterHash: claims.filterHash, lastChannelID: claims.lastChannelID},
	} {
		if _, err := ring.mintChannelCatalogNavigation(bad, issuedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("%s minted: %v", name, err)
		}
	}
}
