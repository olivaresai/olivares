// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package pepkit holds the rules the policy enforcement points share once they
// live outside package main: the business-tenant policy, the ledger anchor
// encoding and the delegation evidence. The composition root and every PEP
// package read them from here, so each rule keeps one source.
package pepkit

import (
	"encoding/binary"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// ParseBusinessTenant is the ONE place that decides what makes an
// operator-configured (or decision-carried) tenant reference usable. Before
// the same predicate was hand-written at every reader, and the copies had drifted:
// five refused a broken tenant, one warned and widened, and two of the six never
// checked the reserved SYSTEM tenant at all. Every reader now asks this function
// for the POLICY and keeps its own REACTION — refuse to mount, skip the entry, or
// decline to anchor evidence.
//
// It resolves the reference into a BUSINESS tenant, deny-closed, and separates
// the two cases that must never be conflated:
//
//   - ABSENT (raw is blank): present=false, err=nil. This is a legitimate
//     configuration — "no fixed tenant, infer per credential" — and is the documented
//     default of the inference proxy's `tenant` field. Callers decide whether an
//     absent tenant means "mount nothing" or "mount without a fixed tenant".
//   - PRESENT AND INVALID: err names the field and the offending value. Invalid means
//     unparseable, the nil UUID (the "unset" sentinel), or the reserved SYSTEM tenant.
//   - PRESENT AND VALID: the parsed tenant, present=true.
//
// The system-tenant leg is not symmetry for its own sake. model.ParseTenantID has an
// explicit special case that returns the system tenant with a NIL error
// (core/model/ids.go:56-58), and the system tenant is non-zero by design (ids.go:28) —
// so the common `err == nil && !tid.IsZero()` shape admits it silently. The system
// tenant is reserved for cross-tenant/system rows; a governed surface bound to it
// would authorize, budget and attribute business traffic outside every business
// boundary. Its exclusion is the same rule the estate-wide loops already apply when
// they enumerate business tenants (see the businessTenants helpers).
func ParseBusinessTenant(field, raw string) (model.TenantID, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	tid, err := model.ParseTenantID(trimmed)
	if err != nil {
		return "", false, fmt.Errorf("%s: %q is not a valid tenant id: %w", field, raw, err)
	}
	if tid.IsZero() {
		return "", false, fmt.Errorf("%s: %q is the unset tenant, not a business tenant", field, raw)
	}
	if tid.IsSystem() {
		return "", false, fmt.Errorf("%s: %q is the reserved system tenant, not a business tenant", field, raw)
	}
	return tid, true, nil
}

// FirstNonEmpty returns the first non-blank string.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// WriteLenPrefixed is the injective length-prefixed encoding of the ledger
// anchor hashes.
func WriteLenPrefixed(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

// ReanchorTimeout bounds the single decoupled re-anchor of a downgraded deny so a wedged store
// cannot block the response indefinitely; the deny stands regardless of the outcome.
const ReanchorTimeout = 5 * time.Second

// AddDelegationMeta names both sides of an on-behalf-of decision. The fields
// intentionally live in Meta, not PayloadHash: the ledger's canonical MetaDigest is
// already part of the event hash and Ed25519 signature, while keeping the v1 decision
// PayloadHash preimage stable avoids a second, redundant commitment format.
func AddDelegationMeta(meta map[string]any, isDelegated bool, actAs string) {
	if !isDelegated {
		return
	}
	meta["is_delegated"] = true
	meta["act_as"] = actAs
}
