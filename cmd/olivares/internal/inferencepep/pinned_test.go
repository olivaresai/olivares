// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
)

// TestCopiedHelpersMatchTheCompositionRoot pins the values this package copies from
// cmd/olivares (see the ponytail note in inferenceproxy.go). The composition root pins the
// same vector in TestSharedLedgerEncodingIsPinned, so a drift on either side turns red.
func TestCopiedHelpersMatchTheCompositionRoot(t *testing.T) {
	if sessionHookTokenPrefix != "olvsess_" {
		t.Fatalf("sessionHookTokenPrefix = %q, want the core/auth prefix olvsess_", sessionHookTokenPrefix)
	}
	if engineReserveUnreachable != finops.UnreachableDeny {
		t.Fatalf("engineReserveUnreachable = %q, want deny", engineReserveUnreachable)
	}
	if got := ledgerEncodingVector(); got != pinnedLedgerEncodingVector {
		t.Fatalf("ledger encoding vector = %s, want %s", got, pinnedLedgerEncodingVector)
	}
}

const pinnedLedgerEncodingVector = "deaa9ac6c69967e0b102859bd0870ea97a1c2ba3a4a11a2a0dee48ed2c7941af/c347a416e62a1713e9c78d2b13f0931c05224d1acea73db0a1026eab12c1c0e1/x"

// ledgerEncodingVector runs the copied helpers over fixed inputs.
func ledgerEncodingVector() string {
	h := sha256.New()
	writeLenPrefixed(h, []byte("olivares"))
	writeInt64(h, -42)
	return hex.EncodeToString(h.Sum(nil)) + "/" + hexSHA("olivares") + "/" + firstNonEmpty(" ", "x")
}
