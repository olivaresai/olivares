// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strconv"
	"testing"
	"time"
)

func TestVerifyWithinAcceptsFreshRejectsReplay(t *testing.T) {
	const secret = "shhh"
	body := []byte(`{"type":"finding.reported","title":"x"}`)
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := formatSignatureHeader(ts, Sign(secret, ts, body))

	// Fresh: same instant.
	if !VerifyWithin(secret, ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("fresh delivery rejected")
	}
	// Within window (4 min later).
	if !VerifyWithin(secret, ts, sig, body, now.Add(4*time.Minute), DefaultReplayWindow) {
		t.Fatal("delivery 4 min old rejected (within 5 min window)")
	}
	// Replay: 6 min later (outside window) — signature still authentic, but stale.
	if VerifyWithin(secret, ts, sig, body, now.Add(6*time.Minute), DefaultReplayWindow) {
		t.Fatal("stale replay (6 min) accepted; replay window not enforced")
	}
	// A captured request re-dated to the FUTURE is equally suspect.
	if VerifyWithin(secret, ts, sig, body, now.Add(-6*time.Minute), DefaultReplayWindow) {
		t.Fatal("future-skewed timestamp accepted")
	}
}

func TestVerifyWithinBadSignatureFailsClosedBeforeWindow(t *testing.T) {
	const secret = "shhh"
	body := []byte(`{}`)
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	// Wrong secret -> bad signature, must fail regardless of freshness.
	sig := formatSignatureHeader(ts, Sign("other", ts, body))
	if VerifyWithin(secret, ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("bad signature accepted")
	}
}

func TestVerifyWithinMalformedTimestampFailsClosed(t *testing.T) {
	const secret = "shhh"
	body := []byte(`{}`)
	now := time.Now()
	// Signature computed over a non-integer ts: Verify passes (the bytes match) but
	// the freshness parse must fail closed rather than treat it as fresh.
	ts := "not-a-number"
	sig := formatSignatureHeader(ts, Sign(secret, ts, body))
	if VerifyWithin(secret, ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("non-integer timestamp treated as fresh")
	}
	// With the window disabled it is signature-only and passes.
	if !VerifyWithin(secret, ts, sig, body, now, 0) {
		t.Fatal("signature-only (tolerance<=0) rejected a valid signature")
	}
}

func TestSignatureTimestampExtraction(t *testing.T) {
	if got := SignatureTimestamp("t=1717668000,v1=abcd"); got != "1717668000" {
		t.Fatalf("SignatureTimestamp = %q", got)
	}
	if got := SignatureTimestamp("abcd"); got != "" {
		t.Fatalf("bare hex must yield no timestamp, got %q", got)
	}
}
