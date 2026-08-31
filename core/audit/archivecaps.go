// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"strconv"
	"strings"
)

// This file declares OPTIONAL archive-sink capabilities for the long-horizon
// legal-hold orchestrator. They are deliberately NOT methods on ArchiveSink: the open
// archival loop (cmd/olivares) never calls them, DirSink does not implement them, and
// the s3archive write path is unchanged — so the open binary is BYTE-IDENTICAL. A
// closed enterprise orchestrator (enterprise/wormretention, -tags enterprise) type-
// asserts a live sink to these capabilities to (1) enumerate the segments covering a
// held matter and (2) place/lift an object-lock LEGAL HOLD on segments that were
// already written before the matter arose. A sink that cannot do either simply does
// not satisfy the interface, and the orchestrator degrades honestly (it reports the
// archive leg as unsupported rather than fabricating protection it did not apply).
//
// No rug-pull: the existing write-time PutOptions.LegalHold and the open verifier are
// untouched; this only ADDS post-hoc operations behind a capability the open code never
// invokes.

// SegmentRef identifies one archived segment object version within a sink, with the
// ledger-sequence range it covers — enough for the orchestrator to select the segments
// covering a held range (by seq) and to address a specific version for an object-lock
// legal hold. It carries NO event content (minimal data, docs/SECURITY-HARDENING.md).
type SegmentRef struct {
	// Key is the segment events object key (SegmentKey).
	Key string
	// VersionID is the object version this ref addresses ("" when the substrate is
	// unversioned); object-lock legal holds are set per version.
	VersionID string
	// FromSeq/ToSeq are the inclusive ledger-sequence range the segment covers (parsed
	// from the key — the manifest is not needed to select by seq).
	FromSeq int64
	ToSeq   int64
	// IsLatest reports this is the current version of the key (informational).
	IsLatest bool
}

// ArchiveLister is the OPTIONAL capability of an ArchiveSink that can ENUMERATE the
// segment object versions it has written for a tenant. The open loop never calls it.
type ArchiveLister interface {
	ListSegments(ctx context.Context, tenant string) ([]SegmentRef, error)
}

// LegalHoldSetter is the OPTIONAL capability of an ArchiveSink that can set or clear an
// object-lock LEGAL HOLD on an ALREADY-WRITTEN object version (S3 PutObjectLegalHold and
// the Azure/GCS equivalents). A legal hold is independent of retention: it preserves the
// version indefinitely until explicitly cleared, and may be set even under COMPLIANCE-mode
// retention (it never shortens retention). The closed orchestrator drives it; the open
// hold-release quorum (modules/compliance/holds.go) is never weakened — clearing an object
// hold is the add-on's own gated act, not a relaxation of the open engine.
type LegalHoldSetter interface {
	SetObjectLegalHold(ctx context.Context, key, versionID string, on bool) (ArchiveReceipt, error)
}

// ParseSegmentKey is the inverse of SegmentKey: it recovers the tenant and the inclusive
// sequence range from a segment EVENTS key ("<tenant>/seg-<from>-<to>.jsonl"). It returns
// ok=false for any other key (including the ".manifest.json" sidecars and keys.json), so a
// lister can filter a flat object listing down to exactly the segment bodies.
func ParseSegmentKey(key string) (tenant string, fromSeq, toSeq int64, ok bool) {
	const marker = "/seg-"
	const suffix = ".jsonl"
	if !strings.HasSuffix(key, suffix) {
		return "", 0, 0, false
	}
	i := strings.LastIndex(key, marker)
	if i <= 0 { // i==0 ⇒ empty tenant, also invalid
		return "", 0, 0, false
	}
	tenant = key[:i]
	mid := key[i+len(marker) : len(key)-len(suffix)] // "<from>-<to>"
	dash := strings.IndexByte(mid, '-')
	if dash <= 0 || dash >= len(mid)-1 {
		return "", 0, 0, false
	}
	f, err1 := strconv.ParseInt(mid[:dash], 10, 64)
	t, err2 := strconv.ParseInt(mid[dash+1:], 10, 64)
	if err1 != nil || err2 != nil || f < 1 || t < f {
		return "", 0, 0, false
	}
	return tenant, f, t, true
}
