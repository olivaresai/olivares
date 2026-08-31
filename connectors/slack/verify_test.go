// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package slack

import (
	"strconv"
	"testing"
	"time"
)

func TestVerifyRequestRoundTrip(t *testing.T) {
	const secret = "8f742231b10e8888abcd99yyyzzz85a5"
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte("payload=%7B%22type%22%3A%22block_actions%22%7D")
	sig := SignRequest(secret, ts, body)

	if !VerifyRequest(secret, ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("authentic request rejected")
	}
}

func TestVerifyRequestRejectsTamperAndReplay(t *testing.T) {
	const secret = "topsecret"
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte("payload=approve")
	sig := SignRequest(secret, ts, body)

	// Tampered body.
	if VerifyRequest(secret, ts, sig, []byte("payload=deny"), now, DefaultReplayWindow) {
		t.Fatal("tampered body accepted")
	}
	// Wrong secret.
	if VerifyRequest("wrong", ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("wrong signing secret accepted")
	}
	// Stale (replay) beyond the 5-minute window.
	if VerifyRequest(secret, ts, sig, body, now.Add(6*time.Minute), DefaultReplayWindow) {
		t.Fatal("stale replay accepted")
	}
	// Future-skew beyond window.
	if VerifyRequest(secret, ts, sig, body, now.Add(-6*time.Minute), DefaultReplayWindow) {
		t.Fatal("future-skewed timestamp accepted")
	}
	// Malformed signature header (missing v0=).
	if VerifyRequest(secret, ts, "deadbeef", body, now, DefaultReplayWindow) {
		t.Fatal("signature without v0= prefix accepted")
	}
	// Empty secret fails closed.
	if VerifyRequest("", ts, sig, body, now, DefaultReplayWindow) {
		t.Fatal("empty signing secret accepted")
	}
}

func TestVerifyRequestNonIntegerTimestampFailsClosed(t *testing.T) {
	const secret = "s"
	body := []byte("x")
	ts := "abc"
	sig := SignRequest(secret, ts, body)
	if VerifyRequest(secret, ts, sig, body, time.Now(), DefaultReplayWindow) {
		t.Fatal("non-integer timestamp treated as fresh")
	}
	// Window disabled => signature-only, passes.
	if !VerifyRequest(secret, ts, sig, body, time.Now(), 0) {
		t.Fatal("signature-only rejected a valid signature")
	}
}
