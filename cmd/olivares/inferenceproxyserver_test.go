// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
)

// TestProxyApprovalsForKeepsAnAbsentBridgeNil pins the property the decider's
// `d.Approvals == nil` guards rely on: no bridge means no opener, never an opener that
// wraps a nil bridge.
func TestProxyApprovalsForKeepsAnAbsentBridgeNil(t *testing.T) {
	if got := proxyApprovalsFor(nil); got != nil {
		t.Fatalf("proxyApprovalsFor(nil) = %#v, want a nil ApprovalOpener", got)
	}
	if got := proxyApprovalsFor(&approvalBridge{}); got == nil {
		t.Fatal("proxyApprovalsFor(bridge) = nil, want the bridge adapter")
	}
}

// TestSharedLedgerEncodingIsPinned pins the helpers internal/inferencepep copies (its
// TestCopiedHelpersMatchTheCompositionRoot pins the same vector), so the two copies of the
// Anchor encoding and the session-credential prefix cannot drift apart silently.
func TestSharedLedgerEncodingIsPinned(t *testing.T) {
	if sessionHookTokenPrefix != "olvsess_" {
		t.Fatalf("sessionHookTokenPrefix = %q, want the core/auth prefix olvsess_", sessionHookTokenPrefix)
	}
	if engineReserveUnreachable != finops.UnreachableDeny {
		t.Fatalf("engineReserveUnreachable = %q, want deny", engineReserveUnreachable)
	}
	h := sha256.New()
	writeLenPrefixed(h, []byte("olivares"))
	writeInt64(h, -42)
	got := hex.EncodeToString(h.Sum(nil)) + "/" + hexSHA("olivares") + "/" + firstNonEmpty(" ", "x")
	const want = "deaa9ac6c69967e0b102859bd0870ea97a1c2ba3a4a11a2a0dee48ed2c7941af/c347a416e62a1713e9c78d2b13f0931c05224d1acea73db0a1026eab12c1c0e1/x"
	if got != want {
		t.Fatalf("ledger encoding vector = %s, want %s", got, want)
	}
}
