// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func newChannelAdministrationKeyring(t *testing.T, kid string) *communicationCursorTokenKeyring {
	t.Helper()
	ring, err := newCommunicationCursorTokenKeyring(kid, []communicationCursorTokenKey{{
		kid: kid, material: bytes.Repeat([]byte{0x3c}, 32),
	}})
	if err != nil {
		t.Fatalf("administration navigation keyring: %v", err)
	}
	return ring
}

func channelAdministrationTestClaims(t *testing.T) communicationChannelAdministrationNavigationClaims {
	t.Helper()
	filter := channelAdministrationFilterHash(ChannelAdministrationStateAll)
	return communicationChannelAdministrationNavigationClaims{
		tenantID: model.TenantID(model.NewID()), workspaceID: model.NewID(),
		reader:     RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		filterHash: filter[:], lastChannelID: model.NewID(),
	}
}

func channelGrantAdministrationTestClaims(t *testing.T) communicationChannelGrantAdministrationNavigationClaims {
	t.Helper()
	filter := channelGrantAdministrationFilterHash(
		ChannelGrantAdministrationStateAll, CommunicationSubjectRef{},
	)
	return communicationChannelGrantAdministrationNavigationClaims{
		tenantID: model.TenantID(model.NewID()), workspaceID: model.NewID(),
		reader:         RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		channelID:      model.NewID(),
		channelVersion: 7, aclRevision: 4,
		filterHash: filter[:], lastGrantID: model.NewID(),
	}
}

// TestCommunicationNavigationFamiliesAreDistinct pins the four navigation
// prefixes and their four MAC domains as pairwise distinct. A shared prefix or a
// shared domain would let a token minted for one listing resume another one on
// the same keyring, which is exactly what the separate families exist to stop.
func TestCommunicationNavigationFamiliesAreDistinct(t *testing.T) {
	t.Parallel()
	prefixes := []string{
		communicationCursorTokenPrefix,
		communicationInboxNavigationPrefix,
		communicationChannelCatalogNavigationPrefix,
		communicationChannelAdministrationNavigationPrefix,
		communicationChannelGrantAdministrationNavigationPrefix,
	}
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		if prefix == "" || seen[prefix] {
			t.Fatalf("navigation prefix %q is empty or repeated in %v", prefix, prefixes)
		}
		seen[prefix] = true
	}
	domains := []string{
		communicationChannelCatalogNavigationDomain,
		communicationChannelAdministrationNavigationDomain,
		communicationChannelGrantAdministrationNavigationDomain,
	}
	seenDomains := map[string]bool{}
	for _, domain := range domains {
		if domain == "" || seenDomains[domain] {
			t.Fatalf("navigation MAC domain %q is empty or repeated", domain)
		}
		seenDomains[domain] = true
	}
	// The two administrative filter domains are distinct too, so a catalog filter
	// hash can never equal a grant filter hash.
	catalog := channelAdministrationFilterHash(ChannelAdministrationStateAll)
	grants := channelGrantAdministrationFilterHash(ChannelGrantAdministrationStateAll, CommunicationSubjectRef{})
	if bytes.Equal(catalog[:], grants[:]) {
		t.Fatal("the administrative catalog and grant filter hashes collide")
	}
}

// TestChannelAdministrationFilterHashSeparatesEverySelection proves each closed
// selection commits to its own hash: resuming a page under a different state,
// or with a subject filter added or removed, cannot verify.
func TestChannelAdministrationFilterHashSeparatesEverySelection(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, state := range channelAdministrationStateFilters() {
		hash := channelAdministrationFilterHash(state)
		key := base64.RawURLEncoding.EncodeToString(hash[:])
		if previous, clash := seen[key]; clash {
			t.Fatalf("catalog states %q and %q share a filter hash", previous, state)
		}
		seen[key] = string(state)
	}
	if len(seen) != 3 {
		t.Fatalf("catalog filter hashes = %d, want one per closed state", len(seen))
	}
	subject := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	other := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: subject.Ref}
	grantSeen := map[string]string{}
	for _, state := range channelGrantAdministrationStateFilters() {
		for label, filter := range map[string]CommunicationSubjectRef{
			"none": {}, "user": subject, "group": other,
		} {
			hash := channelGrantAdministrationFilterHash(state, filter)
			key := base64.RawURLEncoding.EncodeToString(hash[:])
			name := string(state) + "/" + label
			if previous, clash := grantSeen[key]; clash {
				t.Fatalf("grant selections %q and %q share a filter hash", previous, name)
			}
			grantSeen[key] = name
		}
	}
	if len(grantSeen) != 12 {
		t.Fatalf("grant filter hashes = %d, want one per (state, subject) selection", len(grantSeen))
	}
}

// TestChannelAdministrationNavigationTokenDisclosesOnlyTheReturnedAnchor decodes
// the c3a1 claims WITHOUT the key, as anyone holding the token can, and proves
// every claim is disclosure-safe: family/version, tenant, workspace, the
// canonical reader binding, the filter hash and the last RETURNED Channel.
func TestChannelAdministrationNavigationTokenDisclosesOnlyTheReturnedAnchor(t *testing.T) {
	t.Parallel()
	ring := newChannelAdministrationKeyring(t, "k3adm")
	claims := channelAdministrationTestClaims(t)
	issuedAt := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	token, err := ring.mintChannelAdministrationNavigation(claims, issuedAt)
	if err != nil {
		t.Fatalf("mint administration navigation: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c3a1" {
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
	for _, forbidden := range []string{"seq", "after", "cursor", "count", "hidden", "grant", "lg", "ch", "cv", "acl"} {
		if _, present := decoded[forbidden]; present {
			t.Fatalf("administration token discloses %q", forbidden)
		}
	}
	got, err := ring.verifyChannelAdministrationNavigation(token, issuedAt.Add(time.Minute))
	if err != nil || got.lastChannelID != claims.lastChannelID || got.reader != claims.reader ||
		got.tenantID != claims.tenantID || got.workspaceID != claims.workspaceID {
		t.Fatalf("verify = %+v, %v", got, err)
	}
}

// TestChannelGrantAdministrationNavigationTokenCarriesOnlyReturnedCoordinates
// does the same for c3g1: the Channel and the revisions it commits are exactly
// the ones the accompanying page returned, and no grant of another subject, no
// count and no store cursor is present.
func TestChannelGrantAdministrationNavigationTokenCarriesOnlyReturnedCoordinates(t *testing.T) {
	t.Parallel()
	ring := newChannelAdministrationKeyring(t, "k3grants")
	claims := channelGrantAdministrationTestClaims(t)
	issuedAt := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	token, err := ring.mintChannelGrantAdministrationNavigation(claims, issuedAt)
	if err != nil {
		t.Fatalf("mint grant navigation: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c3g1" {
		t.Fatalf("token shape = %q", token)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[2])
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("claims JSON: %v", err)
	}
	want := map[string]any{
		"v": float64(1), "ten": claims.tenantID.String(), "ws": claims.workspaceID.String(),
		"rk": "user", "rr": claims.reader.Ref,
		"ch": claims.channelID.String(), "cv": float64(7), "acl": float64(4),
		"fh":  base64.RawURLEncoding.EncodeToString(claims.filterHash),
		"lg":  claims.lastGrantID.String(),
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
	for _, forbidden := range []string{"cursor", "count", "seq", "subjects", "hidden"} {
		if _, present := decoded[forbidden]; present {
			t.Fatalf("grant token discloses %q", forbidden)
		}
	}
	got, err := ring.verifyChannelGrantAdministrationNavigation(token, issuedAt.Add(time.Minute))
	if err != nil || got.lastGrantID != claims.lastGrantID || got.channelID != claims.channelID ||
		got.channelVersion != claims.channelVersion || got.aclRevision != claims.aclRevision {
		t.Fatalf("verify = %+v, %v", got, err)
	}
}

// TestChannelAdministrationNavigationRejectsCrossFamilyTamperAndExpiry proves
// the two administrative verifiers refuse each other's tokens, the read
// catalog's and the inbox's, refuse a family-spliced segment on the SAME
// keyring, refuse tampering, and honour the database-time lifetime.
func TestChannelAdministrationNavigationRejectsCrossFamilyTamperAndExpiry(t *testing.T) {
	t.Parallel()
	ring := newChannelAdministrationKeyring(t, "k3adm")
	issuedAt := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	adminClaims := channelAdministrationTestClaims(t)
	adminToken, err := ring.mintChannelAdministrationNavigation(adminClaims, issuedAt)
	if err != nil {
		t.Fatalf("mint administration token: %v", err)
	}
	grantClaims := channelGrantAdministrationTestClaims(t)
	grantToken, err := ring.mintChannelGrantAdministrationNavigation(grantClaims, issuedAt)
	if err != nil {
		t.Fatalf("mint grant token: %v", err)
	}
	catalogFilter := channelCatalogFilterHash()
	catalogToken, err := ring.mintChannelCatalogNavigation(communicationChannelCatalogNavigationClaims{
		tenantID: adminClaims.tenantID, workspaceID: adminClaims.workspaceID,
		reader: adminClaims.reader, filterHash: catalogFilter[:], lastChannelID: model.NewID(),
	}, issuedAt)
	if err != nil {
		t.Fatalf("mint read catalog token: %v", err)
	}

	// Each verifier refuses every other family, on the SAME keyring and key.
	for name, token := range map[string]string{
		"grant token": grantToken, "read catalog token": catalogToken,
	} {
		if _, err := ring.verifyChannelAdministrationNavigation(token, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("administration verifier accepted a %s: %v", name, err)
		}
	}
	for name, token := range map[string]string{
		"administration token": adminToken, "read catalog token": catalogToken,
	} {
		if _, err := ring.verifyChannelGrantAdministrationNavigation(token, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("grant verifier accepted an %s: %v", name, err)
		}
	}
	if _, err := ring.verifyChannelCatalogNavigation(adminToken, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("the READ catalog verifier accepted an administration token: %v", err)
	}
	// Relabelling the prefix does not help: the MAC binds the family domain.
	grantParts := strings.Split(grantToken, ".")
	spliced := "c3a1." + grantParts[1] + "." + grantParts[2] + "." + grantParts[3]
	if _, err := ring.verifyChannelAdministrationNavigation(spliced, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("family-spliced grant token accepted: %v", err)
	}

	adminParts := strings.Split(adminToken, ".")
	adminRaw, _ := base64.RawURLEncoding.DecodeString(adminParts[2])
	reencodeAdmin := func(mutate func(map[string]any)) string {
		var wire map[string]any
		_ = json.Unmarshal(adminRaw, &wire)
		mutate(wire)
		out, _ := json.Marshal(wire)
		return adminParts[0] + "." + adminParts[1] + "." +
			base64.RawURLEncoding.EncodeToString(out) + "." + adminParts[3]
	}
	for name, mutated := range map[string]string{
		"tampered anchor": reencodeAdmin(func(w map[string]any) { w["lc"] = model.NewID().String() }),
		"tampered tenant": reencodeAdmin(func(w map[string]any) { w["ten"] = model.NewID().String() }),
		"tampered reader": reencodeAdmin(func(w map[string]any) { w["rr"] = model.NewID().String() }),
		"unknown filter": reencodeAdmin(func(w map[string]any) {
			w["fh"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, sha256.Size))
		}),
		"extended expiry":       reencodeAdmin(func(w map[string]any) { w["exp"] = float64(issuedAt.Add(time.Hour).Unix()) }),
		"non-canonical padding": adminParts[0] + "." + adminParts[1] + "." + base64.RawURLEncoding.EncodeToString(append(adminRaw, ' ')) + "." + adminParts[3],
		"missing segment":       adminParts[0] + "." + adminParts[1] + "." + adminParts[2],
		"tampered mac":          adminParts[0] + "." + adminParts[1] + "." + adminParts[2] + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, sha256.Size)),
	} {
		if _, err := ring.verifyChannelAdministrationNavigation(mutated, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}

	grantRaw, _ := base64.RawURLEncoding.DecodeString(grantParts[2])
	reencodeGrant := func(mutate func(map[string]any)) string {
		var wire map[string]any
		_ = json.Unmarshal(grantRaw, &wire)
		mutate(wire)
		out, _ := json.Marshal(wire)
		return grantParts[0] + "." + grantParts[1] + "." +
			base64.RawURLEncoding.EncodeToString(out) + "." + grantParts[3]
	}
	for name, mutated := range map[string]string{
		"tampered Channel":  reencodeGrant(func(w map[string]any) { w["ch"] = model.NewID().String() }),
		"tampered revision": reencodeGrant(func(w map[string]any) { w["cv"] = float64(99) }),
		"tampered acl":      reencodeGrant(func(w map[string]any) { w["acl"] = float64(99) }),
		"tampered anchor":   reencodeGrant(func(w map[string]any) { w["lg"] = model.NewID().String() }),
	} {
		if _, err := ring.verifyChannelGrantAdministrationNavigation(mutated, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("grant %s accepted: %v", name, err)
		}
	}

	// Lifetime is judged at DATABASE time, with the keyring's skew allowance.
	expiredAt := issuedAt.Add(communicationCursorTokenTTL + communicationCursorTokenClockSkew)
	if _, err := ring.verifyChannelAdministrationNavigation(adminToken, expiredAt); !errors.Is(err, errCommunicationCursorTokenExpired) {
		t.Fatalf("expired administration token accepted: %v", err)
	}
	if _, err := ring.verifyChannelGrantAdministrationNavigation(grantToken, expiredAt); !errors.Is(err, errCommunicationCursorTokenExpired) {
		t.Fatalf("expired grant token accepted: %v", err)
	}
	if _, err := ring.verifyChannelAdministrationNavigation(adminToken, issuedAt.Add(-2*communicationCursorTokenClockSkew)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("not-yet-valid administration token accepted: %v", err)
	}
	unknown := newChannelAdministrationKeyring(t, "other")
	if _, err := unknown.verifyChannelAdministrationNavigation(adminToken, issuedAt.Add(time.Minute)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("unknown KID accepted: %v", err)
	}

	// Malformed claims never mint.
	for name, bad := range map[string]communicationChannelAdministrationNavigationClaims{
		"zero anchor":    {tenantID: adminClaims.tenantID, workspaceID: adminClaims.workspaceID, reader: adminClaims.reader, filterHash: adminClaims.filterHash},
		"foreign filter": {tenantID: adminClaims.tenantID, workspaceID: adminClaims.workspaceID, reader: adminClaims.reader, filterHash: bytes.Repeat([]byte{2}, sha256.Size), lastChannelID: model.NewID()},
		"no reader":      {tenantID: adminClaims.tenantID, workspaceID: adminClaims.workspaceID, filterHash: adminClaims.filterHash, lastChannelID: model.NewID()},
	} {
		if _, err := ring.mintChannelAdministrationNavigation(bad, issuedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("administration %s minted: %v", name, err)
		}
	}
	for name, bad := range map[string]communicationChannelGrantAdministrationNavigationClaims{
		"zero Channel":  {tenantID: grantClaims.tenantID, workspaceID: grantClaims.workspaceID, reader: grantClaims.reader, channelVersion: 1, aclRevision: 1, filterHash: grantClaims.filterHash, lastGrantID: model.NewID()},
		"zero revision": {tenantID: grantClaims.tenantID, workspaceID: grantClaims.workspaceID, reader: grantClaims.reader, channelID: model.NewID(), aclRevision: 1, filterHash: grantClaims.filterHash, lastGrantID: model.NewID()},
		"zero acl":      {tenantID: grantClaims.tenantID, workspaceID: grantClaims.workspaceID, reader: grantClaims.reader, channelID: model.NewID(), channelVersion: 1, filterHash: grantClaims.filterHash, lastGrantID: model.NewID()},
		"zero anchor":   {tenantID: grantClaims.tenantID, workspaceID: grantClaims.workspaceID, reader: grantClaims.reader, channelID: model.NewID(), channelVersion: 1, aclRevision: 1, filterHash: grantClaims.filterHash},
	} {
		if _, err := ring.mintChannelGrantAdministrationNavigation(bad, issuedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("grant %s minted: %v", name, err)
		}
	}
}
