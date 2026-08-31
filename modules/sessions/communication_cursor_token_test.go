// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const (
	testCursorTenant    model.TenantID = "018f0000-0000-7000-8000-000000000001"
	testCursorWorkspace model.ID       = "018f0000-0000-7000-8000-000000000002"
	testCursorReader    model.ID       = "018f0000-0000-7000-8000-000000000003"
	testCursorID        model.ID       = "018f0000-0000-7000-8000-000000000004"
	testCursorDelivery  model.ID       = "018f0000-0000-7000-8000-000000000005"
	testCursorKID                      = "cursor-2026-08"
	testCursorIssuedAt                 = int64(1786752000)
)

func TestCommunicationCursorTokenCanonicalGoldenAndRoundTrip(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, sha256.Size)
	ring := testCommunicationCursorTokenKeyring(t, testCursorKID, key)
	claims := testCommunicationCursorTokenClaims()
	issuedAt := time.Unix(testCursorIssuedAt, 987654321).UTC()

	token, err := ring.mint(claims, issuedAt)
	if err != nil {
		t.Fatalf("mint canonical cursor token: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c2v1" {
		t.Fatalf("token syntax = %q", token)
	}
	if strings.Contains(token, "=") || len(token) > communicationCursorTokenMaxBytes {
		t.Fatalf("token is padded or unbounded: len=%d token=%q", len(token), token)
	}
	kid, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || string(kid) != testCursorKID {
		t.Fatalf("kid segment = %q, %v", kid, err)
	}
	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	wantClaims := `{"v":1,"ten":"018f0000-0000-7000-8000-000000000001",` +
		`"ws":"018f0000-0000-7000-8000-000000000002","rk":"user",` +
		`"rr":"018f0000-0000-7000-8000-000000000003","mk":"personal",` +
		`"mr":"018f0000-0000-7000-8000-000000000003","cc":"direct_notice_v1",` +
		`"fh":"mN4SCPkKMYkZlEe2rK4FcGk1-aoCSmGBgyRs2OBeetU",` +
		`"cid":"018f0000-0000-7000-8000-000000000004","cv":7,"base":40,` +
		`"after":41,"did":"018f0000-0000-7000-8000-000000000005",` +
		`"iat":1786752000,"exp":1786752300}`
	if string(rawClaims) != wantClaims {
		t.Fatalf("canonical claims\n got: %s\nwant: %s", rawClaims, wantClaims)
	}

	// Independent oracle: pin the domain, separator order and raw (not encoded)
	// KID so an internally self-consistent HMAC format mutant cannot pass.
	wantMAC := hmac.New(sha256.New, key)
	_, _ = wantMAC.Write([]byte("olivares.sessions.inbox.cursor.c2.v1\x00"))
	_, _ = wantMAC.Write([]byte(testCursorKID))
	_, _ = wantMAC.Write([]byte{0})
	_, _ = wantMAC.Write(rawClaims)
	if parts[3] != base64.RawURLEncoding.EncodeToString(wantMAC.Sum(nil)) {
		t.Fatalf("MAC segment does not bind the canonical domain/KID/claims tuple")
	}

	got, err := ring.verify(token, time.Unix(testCursorIssuedAt, 0).UTC())
	if err != nil {
		t.Fatalf("verify canonical cursor token: %v", err)
	}
	claims.issuedAt = time.Unix(testCursorIssuedAt, 0).UTC()
	claims.expiresAt = time.Unix(testCursorIssuedAt+300, 0).UTC()
	if !reflect.DeepEqual(got, claims) {
		t.Fatalf("verified claims\n got: %#v\nwant: %#v", got, claims)
	}
}

func TestCommunicationCursorTokenVirtualAndNoAdvanceShapes(t *testing.T) {
	t.Parallel()

	ring := testCommunicationCursorTokenKeyring(
		t,
		testCursorKID,
		bytes.Repeat([]byte{0x31}, sha256.Size),
	)
	now := time.Unix(testCursorIssuedAt, 0).UTC()

	tests := []struct {
		name        string
		claims      communicationCursorTokenClaims
		wantCID     bool
		wantDID     bool
		wantBase    int64
		wantAfter   int64
		wantVersion int64
	}{
		{
			name:        "virtual empty observation",
			claims:      testCommunicationCursorTokenVirtualClaims(0),
			wantVersion: 0,
		},
		{
			name:        "virtual advancing observation",
			claims:      testCommunicationCursorTokenVirtualClaims(41),
			wantDID:     true,
			wantAfter:   41,
			wantVersion: 0,
		},
		{
			name: "durable no advance",
			claims: func() communicationCursorTokenClaims {
				claims := testCommunicationCursorTokenClaims()
				claims.afterDeliverySeq = claims.baseDeliverySeq
				claims.deliveryID = ""
				return claims
			}(),
			wantCID:     true,
			wantBase:    40,
			wantAfter:   40,
			wantVersion: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := ring.mint(tt.claims, now)
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			wire := testCommunicationCursorTokenWireFromToken(t, token)
			if (wire.CursorID != "") != tt.wantCID || (wire.DeliveryID != "") != tt.wantDID ||
				wire.CursorVersion != tt.wantVersion || wire.BaseDeliverySeq != tt.wantBase ||
				wire.AfterDeliverySeq != tt.wantAfter {
				t.Fatalf("wire optional/position shape = %#v", wire)
			}
			if _, err := ring.verify(token, now); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

func TestCommunicationCursorTokenKeyringRejectsAmbiguousOrWeakConfiguration(t *testing.T) {
	t.Parallel()

	validKey := bytes.Repeat([]byte{0x51}, sha256.Size)
	tooMany := make([]communicationCursorTokenKey, communicationCursorTokenMaxKeys+1)
	for i := range tooMany {
		tooMany[i] = communicationCursorTokenKey{
			kid: fmt.Sprintf("key-%02d", i), material: validKey,
		}
	}
	tests := []struct {
		name       string
		signingKID string
		keys       []communicationCursorTokenKey
	}{
		{name: "missing signing kid", keys: []communicationCursorTokenKey{{kid: "a", material: validKey}}},
		{name: "invalid signing kid", signingKID: "bad kid", keys: []communicationCursorTokenKey{{kid: "bad kid", material: validKey}}},
		{name: "oversized signing kid", signingKID: strings.Repeat("a", communicationCursorTokenMaxKIDBytes+1), keys: []communicationCursorTokenKey{{kid: "a", material: validKey}}},
		{name: "empty set", signingKID: "a"},
		{name: "too many keys", signingKID: "key-00", keys: tooMany},
		{name: "weak key", signingKID: "a", keys: []communicationCursorTokenKey{{kid: "a", material: validKey[:sha256.Size-1]}}},
		{name: "oversized key", signingKID: "a", keys: []communicationCursorTokenKey{{kid: "a", material: make([]byte, communicationCursorTokenMaxKeyBytes+1)}}},
		{name: "invalid verification kid", signingKID: "a", keys: []communicationCursorTokenKey{{kid: "a", material: validKey}, {kid: "b/1", material: validKey}}},
		{name: "duplicate kid", signingKID: "a", keys: []communicationCursorTokenKey{{kid: "a", material: validKey}, {kid: "a", material: append([]byte(nil), validKey...)}}},
		{name: "current key absent", signingKID: "missing", keys: []communicationCursorTokenKey{{kid: "retained", material: validKey}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := newCommunicationCursorTokenKeyring(tt.signingKID, tt.keys)
			if ring != nil || !errors.Is(err, errCommunicationCursorTokenUnavailable) {
				t.Fatalf("ring=%#v err=%v, want unavailable", ring, err)
			}
		})
	}

	maximumKID := strings.Repeat("k", communicationCursorTokenMaxKIDBytes)
	maximumKey := bytes.Repeat([]byte{0x7f}, communicationCursorTokenMaxKeyBytes)
	if _, err := newCommunicationCursorTokenKeyring(maximumKID, []communicationCursorTokenKey{
		{kid: maximumKID, material: maximumKey},
	}); err != nil {
		t.Fatalf("documented inclusive maxima must remain usable: %v", err)
	}
}

func TestCommunicationCursorTokenRotationAndDefensiveCopies(t *testing.T) {
	t.Parallel()

	oldKeyInput := bytes.Repeat([]byte{0x11}, sha256.Size)
	newKeyInput := bytes.Repeat([]byte{0x22}, sha256.Size)
	oldRing := testCommunicationCursorTokenKeyring(t, "old", oldKeyInput)
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	claims := testCommunicationCursorTokenClaims()
	oldToken, err := oldRing.mint(claims, now)
	if err != nil {
		t.Fatalf("mint old: %v", err)
	}

	rotated, err := newCommunicationCursorTokenKeyring("new", []communicationCursorTokenKey{
		{kid: "old", material: oldKeyInput},
		{kid: "new", material: newKeyInput},
	})
	if err != nil {
		t.Fatalf("construct rotated ring: %v", err)
	}
	for i := range oldKeyInput {
		oldKeyInput[i] ^= 0xff
		newKeyInput[i] ^= 0xff
	}
	if _, err := oldRing.verify(oldToken, now); err != nil {
		t.Fatalf("old ring retained caller key storage instead of a copy: %v", err)
	}
	if _, err := rotated.verify(oldToken, now); err != nil {
		t.Fatalf("retained old key must verify during rotation: %v", err)
	}
	newToken, err := rotated.mint(claims, now)
	if err != nil {
		t.Fatalf("mint new: %v", err)
	}
	newParts := strings.Split(newToken, ".")
	newKID, err := base64.RawURLEncoding.DecodeString(newParts[1])
	if err != nil || string(newKID) != "new" || newToken == oldToken {
		t.Fatalf("rotated signer did not select the unique current KID")
	}
	if _, err := oldRing.verify(newToken, now); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("old ring accepted future KID: %v", err)
	}
	retired := testCommunicationCursorTokenKeyring(
		t,
		"new",
		bytes.Repeat([]byte{0x22}, sha256.Size),
	)
	if _, err := retired.verify(oldToken, now); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("retired KID remained accepted: %v", err)
	}
	if _, err := retired.verify(newToken, now); err != nil {
		t.Fatalf("rotated ring retained caller key storage instead of a copy: %v", err)
	}

	verified, err := rotated.verify(newToken, now)
	if err != nil {
		t.Fatalf("verify new: %v", err)
	}
	originalHashByte := verified.filterHash[0]
	verified.filterHash[0] ^= 0xff
	verifiedAgain, err := rotated.verify(newToken, now)
	if err != nil {
		t.Fatalf("verify new again: %v", err)
	}
	if verifiedAgain.filterHash[0] != originalHashByte {
		t.Fatal("mutating returned claims rewrote a later verification result")
	}
}

func TestCommunicationCursorTokenMintRejectsInvalidServerClaims(t *testing.T) {
	t.Parallel()

	ring := testCommunicationCursorTokenKeyring(
		t,
		testCursorKID,
		bytes.Repeat([]byte{0x5a}, sha256.Size),
	)
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	tests := []struct {
		name   string
		mutate func(*communicationCursorTokenClaims)
	}{
		{name: "system tenant", mutate: func(c *communicationCursorTokenClaims) { c.tenantID = model.SystemTenantID }},
		{name: "workspace v4", mutate: func(c *communicationCursorTokenClaims) { c.workspaceID = "550e8400-e29b-41d4-a716-446655440000" }},
		{name: "wrong reader kind", mutate: func(c *communicationCursorTokenClaims) { c.readerKind = RecipientAgent }},
		{name: "crossed mailbox", mutate: func(c *communicationCursorTokenClaims) { c.mailboxRef = testCursorWorkspace }},
		{name: "wrong carrier", mutate: func(c *communicationCursorTokenClaims) { c.carrierClass = "notice" }},
		{name: "short filter", mutate: func(c *communicationCursorTokenClaims) { c.filterHash = c.filterHash[:sha256.Size-1] }},
		{name: "different 32-byte filter", mutate: func(c *communicationCursorTokenClaims) { c.filterHash[0] ^= 0xff }},
		{name: "negative version", mutate: func(c *communicationCursorTokenClaims) { c.cursorVersion = -1 }},
		{name: "v0 with durable id", mutate: func(c *communicationCursorTokenClaims) { c.cursorVersion = 0 }},
		{name: "v0 with base", mutate: func(c *communicationCursorTokenClaims) { c.cursorVersion = 0; c.cursorID = ""; c.baseDeliverySeq = 1 }},
		{name: "durable without id", mutate: func(c *communicationCursorTokenClaims) { c.cursorID = "" }},
		{name: "rewind", mutate: func(c *communicationCursorTokenClaims) { c.afterDeliverySeq = c.baseDeliverySeq - 1 }},
		{name: "unchanged with target", mutate: func(c *communicationCursorTokenClaims) { c.afterDeliverySeq = c.baseDeliverySeq }},
		{name: "advance without target", mutate: func(c *communicationCursorTokenClaims) { c.deliveryID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := testCommunicationCursorTokenClaims()
			tt.mutate(&claims)
			token, err := ring.mint(claims, now)
			if token != "" || !errors.Is(err, errCommunicationCursorTokenInvalid) {
				t.Fatalf("mint token=%q err=%v, want empty+invalid", token, err)
			}
		})
	}
}

func TestCommunicationCursorTokenRejectsCompactAndAuthenticationMutants(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x63}, sha256.Size)
	ring := testCommunicationCursorTokenKeyring(t, testCursorKID, key)
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	valid, err := ring.mint(testCommunicationCursorTokenClaims(), now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(valid, ".")

	mac, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatal(err)
	}
	mac[0] ^= 0x80
	badMAC := append([]string(nil), parts...)
	badMAC[3] = base64.RawURLEncoding.EncodeToString(mac)

	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	claimsRaw[len(claimsRaw)/2] ^= 1
	badClaimsMAC := append([]string(nil), parts...)
	badClaimsMAC[2] = base64.RawURLEncoding.EncodeToString(claimsRaw)

	unknownKID := append([]string(nil), parts...)
	unknownKID[1] = base64.RawURLEncoding.EncodeToString([]byte("retired"))
	badKID := append([]string(nil), parts...)
	badKID[1] = base64.RawURLEncoding.EncodeToString([]byte("bad kid"))
	shortMAC := append([]string(nil), parts...)
	shortMAC[3] = base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size-1))
	longMAC := append([]string(nil), parts...)
	longMAC[3] = base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size+1))
	paddedKID := append([]string(nil), parts...)
	paddedKID[1] += "="
	paddedClaims := append([]string(nil), parts...)
	paddedClaims[2] += "="
	paddedMAC := append([]string(nil), parts...)
	paddedMAC[3] += "="

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "wrong version", token: strings.Replace(valid, "c2v1.", "c2v2.", 1)},
		{name: "missing segment", token: strings.Join(parts[:3], ".")},
		{name: "extra segment", token: valid + ".extra"},
		{name: "leading whitespace", token: " " + valid},
		{name: "oversized", token: strings.Repeat("a", communicationCursorTokenMaxBytes+1)},
		{name: "kid padding", token: strings.Join(paddedKID, ".")},
		{name: "claims padding", token: strings.Join(paddedClaims, ".")},
		{name: "mac padding", token: strings.Join(paddedMAC, ".")},
		{name: "invalid kid alphabet", token: strings.Join(badKID, ".")},
		{name: "unknown kid", token: strings.Join(unknownKID, ".")},
		{name: "short mac", token: strings.Join(shortMAC, ".")},
		{name: "long mac", token: strings.Join(longMAC, ".")},
		{name: "mac bit flip", token: strings.Join(badMAC, ".")},
		{name: "claim bit flip", token: strings.Join(badClaimsMAC, ".")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ring.verify(tt.token, now); !errors.Is(err, errCommunicationCursorTokenInvalid) {
				t.Fatalf("verify error = %v, want invalid cursor", err)
			}
		})
	}

	unknown := "unknown-sensitive-marker"
	unknownToken := testCommunicationCursorTokenForRawClaims(
		unknown,
		key,
		[]byte(`{"v":1}`),
	)
	_, err = ring.verify(unknownToken, now)
	if err == nil || strings.Contains(err.Error(), unknown) {
		t.Fatalf("unknown non-secret KID leaked through boundary error: %v", err)
	}
}

func TestCommunicationCursorTokenRejectsSignedNonCanonicalJSONMutants(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x74}, sha256.Size)
	ring := testCommunicationCursorTokenKeyring(t, testCursorKID, key)
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	valid, err := ring.mint(testCommunicationCursorTokenClaims(), now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	raw := testCommunicationCursorTokenRawClaims(t, valid)

	replace := func(old, replacement string) []byte {
		t.Helper()
		changed := strings.Replace(string(raw), old, replacement, 1)
		if changed == string(raw) {
			t.Fatalf("fixture does not contain %q", old)
		}
		return []byte(changed)
	}
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "leading object whitespace", raw: append([]byte("{ "), raw[1:]...)},
		{name: "trailing whitespace", raw: append(append([]byte(nil), raw...), ' ')},
		{name: "duplicate exact key", raw: replace(`{"v":1,`, `{"v":1,"v":1,`)},
		{name: "duplicate case-folded key", raw: replace(`{"v":1,`, `{"v":1,"V":1,`)},
		{name: "case-folded key", raw: replace(`{"v":1,`, `{"V":1,`)},
		{name: "unknown key", raw: replace(`{"v":1,`, `{"v":1,"unknown":1,`)},
		{name: "fraction", raw: replace(`"cv":7`, `"cv":7.0`)},
		{name: "exponent", raw: replace(`"cv":7`, `"cv":7e0`)},
		{name: "integer overflow", raw: replace(`"cv":7`, `"cv":9223372036854775808`)},
		{name: "missing required zero-capable field", raw: replace(`,"base":40`, ``)},
		{name: "null optional id", raw: replace(`"cid":"018f0000-0000-7000-8000-000000000004"`, `"cid":null`)},
		{name: "escaped canonical enum", raw: replace(`"rk":"user"`, `"rk":"\u0075ser"`)},
		{name: "trailing JSON", raw: append(append([]byte(nil), raw...), []byte(`{}`)...)},
		{name: "array root", raw: append(append([]byte{'['}, raw...), ']')},
		{name: "nested scalar", raw: replace(`"v":1`, `"v":{"n":1}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := testCommunicationCursorTokenForRawClaims(testCursorKID, key, tt.raw)
			if _, err := ring.verify(token, now); !errors.Is(err, errCommunicationCursorTokenInvalid) {
				t.Fatalf("verify signed mutant = %v, want invalid cursor", err)
			}
		})
	}

	nonCanonicalFH := testCommunicationCursorTokenNonCanonicalBase64(
		t,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x19}, sha256.Size)),
	)
	if _, err := decodeCanonicalCommunicationCursorTokenSegment(nonCanonicalFH, sha256.Size); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("alternate base64 spelling accepted: %v", err)
	}
}

func TestCommunicationCursorTokenRejectsSignedClaimInvariantMutants(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x85}, sha256.Size)
	ring := testCommunicationCursorTokenKeyring(t, testCursorKID, key)
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	base := testCommunicationCursorTokenWire(t)
	v4 := "550e8400-e29b-41d4-a716-446655440000"

	tests := []struct {
		name   string
		mutate func(*communicationCursorTokenWireClaims)
	}{
		{name: "claims version", mutate: func(w *communicationCursorTokenWireClaims) { w.Version = 2 }},
		{name: "system tenant", mutate: func(w *communicationCursorTokenWireClaims) { w.TenantID = model.SystemTenantID.String() }},
		{name: "noncanonical tenant", mutate: func(w *communicationCursorTokenWireClaims) { w.TenantID = strings.ToUpper(w.TenantID) }},
		{name: "tenant v4", mutate: func(w *communicationCursorTokenWireClaims) { w.TenantID = v4 }},
		{name: "workspace v4", mutate: func(w *communicationCursorTokenWireClaims) { w.WorkspaceID = v4 }},
		{name: "reader kind", mutate: func(w *communicationCursorTokenWireClaims) { w.ReaderKind = string(RecipientAgent) }},
		{name: "reader ref v4", mutate: func(w *communicationCursorTokenWireClaims) { w.ReaderRef = v4 }},
		{name: "mailbox kind", mutate: func(w *communicationCursorTokenWireClaims) { w.MailboxKind = string(MailboxChannel) }},
		{name: "mailbox mismatch", mutate: func(w *communicationCursorTokenWireClaims) { w.MailboxRef = testCursorWorkspace.String() }},
		{name: "carrier class", mutate: func(w *communicationCursorTokenWireClaims) { w.CarrierClass = "notice" }},
		{name: "short filter hash", mutate: func(w *communicationCursorTokenWireClaims) {
			w.FilterHash = base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size-1))
		}},
		{name: "different 32-byte filter hash", mutate: func(w *communicationCursorTokenWireClaims) {
			w.FilterHash = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xee}, sha256.Size))
		}},
		{name: "padded filter hash", mutate: func(w *communicationCursorTokenWireClaims) { w.FilterHash += "=" }},
		{name: "negative cursor version", mutate: func(w *communicationCursorTokenWireClaims) { w.CursorVersion = -1 }},
		{name: "v0 carries cursor id", mutate: func(w *communicationCursorTokenWireClaims) { w.CursorVersion = 0 }},
		{name: "v0 nonzero base", mutate: func(w *communicationCursorTokenWireClaims) {
			w.CursorVersion = 0
			w.CursorID = ""
			w.BaseDeliverySeq = 1
		}},
		{name: "durable cursor misses id", mutate: func(w *communicationCursorTokenWireClaims) { w.CursorID = "" }},
		{name: "durable cursor id v4", mutate: func(w *communicationCursorTokenWireClaims) { w.CursorID = v4 }},
		{name: "negative base", mutate: func(w *communicationCursorTokenWireClaims) { w.BaseDeliverySeq = -1 }},
		{name: "rewind", mutate: func(w *communicationCursorTokenWireClaims) { w.AfterDeliverySeq = w.BaseDeliverySeq - 1 }},
		{name: "unchanged target has delivery", mutate: func(w *communicationCursorTokenWireClaims) { w.AfterDeliverySeq = w.BaseDeliverySeq }},
		{name: "advance misses delivery", mutate: func(w *communicationCursorTokenWireClaims) { w.DeliveryID = "" }},
		{name: "delivery id v4", mutate: func(w *communicationCursorTokenWireClaims) { w.DeliveryID = v4 }},
		{name: "zero issued at", mutate: func(w *communicationCursorTokenWireClaims) { w.IssuedAtUnix = 0; w.ExpiresAtUnix = 300 }},
		{name: "expiry before issue", mutate: func(w *communicationCursorTokenWireClaims) { w.ExpiresAtUnix = w.IssuedAtUnix - 1 }},
		{name: "short ttl", mutate: func(w *communicationCursorTokenWireClaims) { w.ExpiresAtUnix-- }},
		{name: "long ttl", mutate: func(w *communicationCursorTokenWireClaims) { w.ExpiresAtUnix++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := base
			tt.mutate(&wire)
			raw, err := json.Marshal(wire)
			if err != nil {
				t.Fatalf("marshal mutant: %v", err)
			}
			token := testCommunicationCursorTokenForRawClaims(testCursorKID, key, raw)
			if _, err := ring.verify(token, now); !errors.Is(err, errCommunicationCursorTokenInvalid) {
				t.Fatalf("verify signed invariant mutant = %v, want invalid cursor", err)
			}
		})
	}
}

func TestCommunicationCursorTokenFreshnessUsesClosedSkewBounds(t *testing.T) {
	t.Parallel()

	ring := testCommunicationCursorTokenKeyring(
		t,
		testCursorKID,
		bytes.Repeat([]byte{0x96}, sha256.Size),
	)
	iat := time.Unix(testCursorIssuedAt, 0).UTC()
	token, err := ring.mint(testCommunicationCursorTokenClaims(), iat.Add(999*time.Millisecond))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	wire := testCommunicationCursorTokenWireFromToken(t, token)
	if wire.IssuedAtUnix != testCursorIssuedAt || wire.ExpiresAtUnix != testCursorIssuedAt+300 {
		t.Fatalf("issuer did not canonicalize DB time to whole Unix seconds: %#v", wire)
	}

	tests := []struct {
		name        string
		now         time.Time
		wantExpired bool
		wantOK      bool
	}{
		{name: "future beyond skew", now: iat.Add(-31 * time.Second)},
		{name: "future at skew", now: iat.Add(-30 * time.Second), wantOK: true},
		{name: "issued", now: iat, wantOK: true},
		{name: "nominal expiry", now: iat.Add(5 * time.Minute), wantOK: true},
		{name: "inside expiry skew", now: iat.Add(5*time.Minute + 29*time.Second), wantOK: true},
		{name: "expiry skew boundary", now: iat.Add(5*time.Minute + 30*time.Second), wantExpired: true},
		{name: "long expired", now: iat.Add(24 * time.Hour), wantExpired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ring.verify(token, tt.now)
			switch {
			case tt.wantOK && err != nil:
				t.Fatalf("verify = %v, want success", err)
			case tt.wantExpired && (!errors.Is(err, errCommunicationCursorTokenExpired) ||
				!errors.Is(err, errCommunicationCursorTokenInvalid)):
				t.Fatalf("verify = %v, want expired+invalid", err)
			case !tt.wantOK && !tt.wantExpired && !errors.Is(err, errCommunicationCursorTokenInvalid):
				t.Fatalf("verify = %v, want invalid", err)
			}
		})
	}
	if _, err := ring.verify(token, time.Time{}); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("zero verifier DB time = %v, want invalid", err)
	}
	if _, err := ring.mint(testCommunicationCursorTokenClaims(), time.Time{}); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("zero issuer DB time = %v, want invalid", err)
	}
	if _, err := ring.mint(testCommunicationCursorTokenClaims(), time.Unix(-1, 0)); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("pre-epoch issuer DB time = %v, want invalid", err)
	}
}

func TestCommunicationCursorTokenMintReservesTTLAndVerifierSkew(t *testing.T) {
	t.Parallel()

	ring := testCommunicationCursorTokenKeyring(
		t,
		testCursorKID,
		bytes.Repeat([]byte{0xa1}, sha256.Size),
	)
	wantReserve := communicationCursorTokenTTL + communicationCursorTokenClockSkew
	if communicationCursorTokenReserve != wantReserve {
		t.Fatalf(
			"token reserve = %s, want independently derived TTL+skew %s",
			communicationCursorTokenReserve,
			wantReserve,
		)
	}
	reserveSeconds := int64(wantReserve / time.Second)
	maxIssuedAt := int64(math.MaxInt64) - reserveSeconds
	token, err := ring.mint(
		testCommunicationCursorTokenClaims(),
		time.Unix(maxIssuedAt, 0).UTC(),
	)
	if err != nil {
		t.Fatalf("mint at inclusive reserve boundary: %v", err)
	}
	wire := testCommunicationCursorTokenWireFromToken(t, token)
	if wire.IssuedAtUnix != maxIssuedAt ||
		wire.ExpiresAtUnix != maxIssuedAt+int64(communicationCursorTokenTTL/time.Second) {
		t.Fatalf("boundary wire lifetime = %#v", wire)
	}
	if token, err := ring.mint(
		testCommunicationCursorTokenClaims(),
		time.Unix(maxIssuedAt+1, 0).UTC(),
	); token != "" || !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("mint above reserve boundary token=%q err=%v, want empty+invalid", token, err)
	}
}

func TestCommunicationCursorTokenCompactBoundPrecedesExactSegmentation(t *testing.T) {
	t.Parallel()

	if validCommunicationCursorTokenCompactBound("") {
		t.Fatal("empty token passed the compact bound")
	}
	if !validCommunicationCursorTokenCompactBound(
		strings.Repeat("a", communicationCursorTokenMaxBytes),
	) {
		t.Fatal("inclusive compact bound was rejected")
	}
	if validCommunicationCursorTokenCompactBound(
		strings.Repeat("a", communicationCursorTokenMaxBytes+1),
	) {
		t.Fatal("token above the compact bound was accepted")
	}

	segments, err := splitCommunicationCursorToken("c2v1.a.b.c")
	if err != nil || segments != (communicationCursorTokenSegments{kid: "a", claims: "b", mac: "c"}) {
		t.Fatalf("exact split = %#v, %v", segments, err)
	}
	for _, malformed := range []string{
		"c2v1.a.b", "c2v1.a.b.c.d", "c2v2.a.b.c",
		strings.Repeat(".", communicationCursorTokenMaxBytes),
		strings.Repeat(".", communicationCursorTokenMaxBytes+1),
	} {
		if _, err := splitCommunicationCursorToken(malformed); !errors.Is(err, errCommunicationCursorTokenInvalid) {
			t.Fatalf("split %q = %v, want invalid", malformed, err)
		}
	}
}

func TestCommunicationCursorTokenSecurityPrimitiveSourceContract(t *testing.T) {
	t.Parallel()

	_, testPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	sourcePath := filepath.Join(filepath.Dir(testPath), "communication_cursor_token.go")
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse token source: %v", err)
	}
	findFunction := func(name string) *ast.FuncDecl {
		t.Helper()
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.Name == name {
				return function
			}
		}
		t.Fatalf("source contract function %s is absent", name)
		return nil
	}
	requireInvalidReturn := func(statement ast.Stmt, zeroType, reason string) {
		t.Helper()
		returned, ok := statement.(*ast.ReturnStmt)
		if !ok || len(returned.Results) != 2 {
			t.Fatalf("security guard statement is %T with unexpected results, want direct two-value return", statement)
		}
		zero, ok := returned.Results[0].(*ast.CompositeLit)
		if !ok || len(zero.Elts) != 0 {
			t.Fatalf("security guard zero result is %#v, want empty %s literal", returned.Results[0], zeroType)
		}
		zeroIdent, ok := zero.Type.(*ast.Ident)
		if !ok || zeroIdent.Name != zeroType {
			t.Fatalf("security guard zero type is %#v, want %s", zero.Type, zeroType)
		}
		invalidCall, ok := returned.Results[1].(*ast.CallExpr)
		if !ok || len(invalidCall.Args) != 1 {
			t.Fatalf("security guard error is %#v, want invalid-token call", returned.Results[1])
		}
		invalidFunction, ok := invalidCall.Fun.(*ast.Ident)
		if !ok || invalidFunction.Name != "communicationCursorTokenInvalid" {
			t.Fatalf("security guard error function is %#v, want communicationCursorTokenInvalid", invalidCall.Fun)
		}
		reasonLiteral, ok := invalidCall.Args[0].(*ast.BasicLit)
		if !ok || reasonLiteral.Kind != token.STRING || reasonLiteral.Value != fmt.Sprintf("%q", reason) {
			t.Fatalf("security guard reason is %#v, want %q", invalidCall.Args[0], reason)
		}
	}

	verifyFunction := findFunction("verify")
	hmacDecisionGuards := 0
	ast.Inspect(verifyFunction.Body, func(node ast.Node) bool {
		decision, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		negated, ok := decision.Cond.(*ast.UnaryExpr)
		if !ok || negated.Op != token.NOT {
			return true
		}
		call, ok := negated.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "hmac" || selector.Sel.Name != "Equal" {
			return true
		}
		hmacDecisionGuards++
		if decision.Init != nil || decision.Else != nil {
			t.Fatalf("MAC rejection guard has init=%#v else=%#v, want neither", decision.Init, decision.Else)
		}
		if len(call.Args) != 2 {
			t.Fatalf("hmac.Equal has %d arguments, want presented and expected MAC", len(call.Args))
		}
		presented, ok := call.Args[0].(*ast.Ident)
		if !ok || presented.Name != "presentedMAC" {
			t.Fatalf("hmac.Equal first argument is %#v, want presentedMAC", call.Args[0])
		}
		expected, ok := call.Args[1].(*ast.SliceExpr)
		if !ok || expected.Low != nil || expected.High != nil || expected.Max != nil || expected.Slice3 {
			t.Fatalf("hmac.Equal second argument is %#v, want expectedMAC[:]", call.Args[1])
		}
		expectedIdent, ok := expected.X.(*ast.Ident)
		if !ok || expectedIdent.Name != "expectedMAC" {
			t.Fatalf("hmac.Equal expected slice is %#v, want expectedMAC[:]", expected.X)
		}
		if len(decision.Body.List) != 1 {
			t.Fatalf("MAC rejection body has %d statements, want one direct return", len(decision.Body.List))
		}
		requireInvalidReturn(
			decision.Body.List[0],
			"communicationCursorTokenClaims",
			"authentication failed",
		)
		return true
	})
	if hmacDecisionGuards != 1 {
		t.Fatalf("verify has %d decisive hmac.Equal guards, want exactly one", hmacDecisionGuards)
	}

	splitFunction := findFunction("splitCommunicationCursorToken")
	if len(splitFunction.Body.List) == 0 {
		t.Fatal("token split function has no body")
	}
	boundGuard, ok := splitFunction.Body.List[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("first split statement is %T, want the compact-bound guard", splitFunction.Body.List[0])
	}
	negated, ok := boundGuard.Cond.(*ast.UnaryExpr)
	if !ok || negated.Op != token.NOT {
		t.Fatalf("first split condition is %T, want negated compact-bound call", boundGuard.Cond)
	}
	boundCall, ok := negated.X.(*ast.CallExpr)
	if !ok {
		t.Fatalf("bound condition operand is %T, want call", negated.X)
	}
	boundFunction, ok := boundCall.Fun.(*ast.Ident)
	if !ok || boundFunction.Name != "validCommunicationCursorTokenCompactBound" {
		t.Fatalf("first split guard calls %#v, want compact-bound helper", boundCall.Fun)
	}
	if boundGuard.Init != nil || boundGuard.Else != nil {
		t.Fatalf("compact-bound guard has init=%#v else=%#v, want neither", boundGuard.Init, boundGuard.Else)
	}
	if len(boundCall.Args) != 1 {
		t.Fatalf("compact-bound call has %d arguments, want token only", len(boundCall.Args))
	}
	boundArgument, ok := boundCall.Args[0].(*ast.Ident)
	if !ok || boundArgument.Name != "token" {
		t.Fatalf("compact-bound argument is %#v, want token", boundCall.Args[0])
	}
	if len(boundGuard.Body.List) != 1 {
		t.Fatalf("compact-bound guard has %d statements, want one direct return", len(boundGuard.Body.List))
	}
	requireInvalidReturn(
		boundGuard.Body.List[0],
		"communicationCursorTokenSegments",
		"token length is invalid",
	)

	cutCalls := 0
	splitCalls := 0
	firstCut := token.NoPos
	ast.Inspect(splitFunction.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "strings" {
			return true
		}
		switch selector.Sel.Name {
		case "Cut":
			cutCalls++
			if firstCut == token.NoPos || call.Pos() < firstCut {
				firstCut = call.Pos()
			}
		case "Split", "SplitN":
			splitCalls++
		}
		return true
	})
	if cutCalls != 3 || splitCalls != 0 || firstCut <= boundGuard.End() {
		t.Fatalf(
			"split primitive contract: cuts=%d splits=%d first_cut=%v bound_end=%v",
			cutCalls,
			splitCalls,
			firstCut,
			boundGuard.End(),
		)
	}
}

func TestCommunicationCursorTokenConcurrentImmutableSnapshot(t *testing.T) {
	t.Parallel()

	keyInput := bytes.Repeat([]byte{0xa7}, sha256.Size)
	ring := testCommunicationCursorTokenKeyring(t, testCursorKID, keyInput)
	for i := range keyInput {
		keyInput[i] = byte(i)
	}
	now := time.Unix(testCursorIssuedAt, 0).UTC()
	claims := testCommunicationCursorTokenClaims()
	const workers = 32
	const iterations = 32

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for iteration := range iterations {
				token, err := ring.mint(claims, now)
				if err != nil {
					errs <- fmt.Errorf("worker %d iteration %d mint: %w", worker, iteration, err)
					return
				}
				got, err := ring.verify(token, now)
				if err != nil || !bytes.Equal(got.filterHash, claims.filterHash) {
					errs <- fmt.Errorf("worker %d iteration %d verify: %w", worker, iteration, err)
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCommunicationCursorTokenNilOrCorruptedRingDeniesClosed(t *testing.T) {
	t.Parallel()

	now := time.Unix(testCursorIssuedAt, 0).UTC()
	var nilRing *communicationCursorTokenKeyring
	if _, err := nilRing.mint(testCommunicationCursorTokenClaims(), now); !errors.Is(err, errCommunicationCursorTokenUnavailable) {
		t.Fatalf("nil mint = %v, want unavailable", err)
	}
	if _, err := nilRing.verify("c2v1.a.b.c", now); !errors.Is(err, errCommunicationCursorTokenUnavailable) {
		t.Fatalf("nil verify = %v, want unavailable", err)
	}

	missingSigner := &communicationCursorTokenKeyring{
		signingKID: "missing",
		keys: map[string][]byte{
			"retained": bytes.Repeat([]byte{0xb8}, sha256.Size),
		},
	}
	if _, err := missingSigner.mint(testCommunicationCursorTokenClaims(), now); !errors.Is(err, errCommunicationCursorTokenUnavailable) {
		t.Fatalf("missing signer = %v, want unavailable", err)
	}
	if _, err := missingSigner.verify("c2v1.a.b.c", now); !errors.Is(err, errCommunicationCursorTokenUnavailable) {
		t.Fatalf("missing signer verify = %v, want unavailable", err)
	}
	corruptVerifier := testCommunicationCursorTokenKeyring(
		t,
		testCursorKID,
		bytes.Repeat([]byte{0xc9}, sha256.Size),
	)
	token, err := corruptVerifier.mint(testCommunicationCursorTokenClaims(), now)
	if err != nil {
		t.Fatal(err)
	}
	corruptVerifier.keys[testCursorKID] = []byte("short")
	if _, err := corruptVerifier.verify(token, now); !errors.Is(err, errCommunicationCursorTokenUnavailable) {
		t.Fatalf("corrupt verifier = %v, want unavailable", err)
	}
}

func testCommunicationCursorTokenClaims() communicationCursorTokenClaims {
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		panic(fmt.Sprintf("build DirectNotice cursor filter hash fixture: %v", err))
	}
	return communicationCursorTokenClaims{
		tenantID:         testCursorTenant,
		workspaceID:      testCursorWorkspace,
		readerKind:       RecipientUser,
		readerRef:        testCursorReader,
		mailboxKind:      MailboxPersonal,
		mailboxRef:       testCursorReader,
		carrierClass:     string(CursorCarrierDirectNoticeV1),
		filterHash:       append([]byte(nil), filterHash[:]...),
		cursorID:         testCursorID,
		cursorVersion:    7,
		baseDeliverySeq:  40,
		afterDeliverySeq: 41,
		deliveryID:       testCursorDelivery,
	}
}

func testCommunicationCursorTokenVirtualClaims(after int64) communicationCursorTokenClaims {
	claims := testCommunicationCursorTokenClaims()
	claims.cursorID = ""
	claims.cursorVersion = 0
	claims.baseDeliverySeq = 0
	claims.afterDeliverySeq = after
	if after == 0 {
		claims.deliveryID = ""
	}
	return claims
}

func testCommunicationCursorTokenKeyring(
	t *testing.T,
	kid string,
	key []byte,
) *communicationCursorTokenKeyring {
	t.Helper()
	ring, err := newCommunicationCursorTokenKeyring(kid, []communicationCursorTokenKey{
		{kid: kid, material: key},
	})
	if err != nil {
		t.Fatalf("construct cursor token keyring: %v", err)
	}
	return ring
}

func testCommunicationCursorTokenRawClaims(t *testing.T, token string) []byte {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		t.Fatalf("token has %d segments", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode token claims: %v", err)
	}
	return raw
}

func testCommunicationCursorTokenWireFromToken(
	t *testing.T,
	token string,
) communicationCursorTokenWireClaims {
	t.Helper()
	raw := testCommunicationCursorTokenRawClaims(t, token)
	var wire communicationCursorTokenWireClaims
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode wire claims: %v", err)
	}
	return wire
}

func testCommunicationCursorTokenWire(t *testing.T) communicationCursorTokenWireClaims {
	t.Helper()
	claims := testCommunicationCursorTokenClaims()
	claims.issuedAt = time.Unix(testCursorIssuedAt, 0).UTC()
	claims.expiresAt = time.Unix(testCursorIssuedAt+300, 0).UTC()
	wire, err := communicationCursorTokenClaimsToWire(claims)
	if err != nil {
		t.Fatalf("create canonical wire fixture: %v", err)
	}
	return wire
}

func testCommunicationCursorTokenForRawClaims(kid string, key, raw []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("olivares.sessions.inbox.cursor.c2.v1\x00"))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(raw)
	return "c2v1." + base64.RawURLEncoding.EncodeToString([]byte(kid)) + "." +
		base64.RawURLEncoding.EncodeToString(raw) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func testCommunicationCursorTokenNonCanonicalBase64(t *testing.T, canonical string) string {
	t.Helper()
	want, err := base64.RawURLEncoding.DecodeString(canonical)
	if err != nil {
		t.Fatalf("decode canonical segment: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, replacement := range alphabet {
		candidate := canonical[:len(canonical)-1] + string(replacement)
		if candidate == canonical {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(candidate)
		if err == nil && bytes.Equal(decoded, want) {
			return candidate
		}
	}
	t.Fatalf("fixture has no alternate base64url spelling: %q", canonical)
	return ""
}
