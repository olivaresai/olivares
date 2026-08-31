// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Inbound request verification (Block Kit interactivity, slash commands, events).
// Slack signs every request to an app's Request URL with HMAC-SHA256 over the base
// string "v0:<timestamp>:<rawBody>" using the app's signing secret, and delivers the
// result as two headers:
//
//	X-Slack-Signature:         "v0=<hex digest>"
//	X-Slack-Request-Timestamp: "<unix seconds>"
//
// A receiver authenticates an inbound Block Kit interactivity payload (an approve/deny
// button click) with VerifyRequest, exactly as the webhook connector's Verify
// authenticates an outbound Olivares delivery. This is the Apache, dependency-free
// crypto half of the AGPL inbound HITL receiver reuses it rather than
// re-implementing the scheme. Source: https://docs.slack.dev/authentication/verifying-requests-from-slack/
const (
	// HeaderSignature is the header carrying "v0=<hex>".
	HeaderSignature = "X-Slack-Signature"
	// HeaderTimestamp is the header carrying the Unix-seconds request timestamp.
	HeaderTimestamp = "X-Slack-Request-Timestamp"
	// sigVersion is the only signature version this package understands.
	sigVersion = "v0"
	// DefaultReplayWindow is the freshness tolerance Slack's guidance recommends: a
	// request whose timestamp is more than five minutes from the receiver's clock is
	// rejected even with a valid signature, to bound replay.
	DefaultReplayWindow = 5 * time.Minute
)

// SignRequest computes the X-Slack-Signature value ("v0=<hex>") for a request with the
// given signing secret, timestamp and raw body. It is the exact value Slack sends and
// what VerifyRequest recomputes; tests and a sender use it for symmetry.
func SignRequest(signingSecret, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(sigVersion + ":"))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{':'})
	mac.Write(rawBody)
	return sigVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyRequest authenticates an inbound Slack request and bounds replay. It recomputes
// the v0 HMAC over "v0:<timestamp>:<rawBody>", compares it to the delivered
// X-Slack-Signature in constant time (hmac.Equal, so it never leaks how much of the
// signature matched), and rejects a timestamp outside tolerance in EITHER direction (a
// far-future timestamp is as suspect as a stale one). rawBody MUST be the exact,
// unparsed request body — Slack signs the raw bytes, so any re-encoding breaks the MAC.
//
// signatureHeader is the X-Slack-Signature value ("v0=<hex>"); timestamp is the
// X-Slack-Request-Timestamp value. A non-positive tolerance disables the freshness
// check (signature-only). An empty secret, an empty/garbled signature, or a
// non-integer timestamp all fail closed.
func VerifyRequest(signingSecret, timestamp, signatureHeader string, rawBody []byte, now time.Time, tolerance time.Duration) bool {
	if signingSecret == "" {
		return false
	}
	got := strings.TrimSpace(signatureHeader)
	if !strings.HasPrefix(got, sigVersion+"=") {
		return false
	}
	gotRaw, err := hex.DecodeString(strings.TrimPrefix(got, sigVersion+"="))
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(SignRequest(signingSecret, timestamp, rawBody), sigVersion+"="))
	if err != nil {
		return false
	}
	if !hmac.Equal(gotRaw, want) {
		return false
	}
	if tolerance <= 0 {
		return true
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	delta := now.Sub(time.Unix(secs, 0))
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}
