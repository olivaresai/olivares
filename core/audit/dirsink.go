// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"time"
)

// ArchiveSink is where archive objects (segments, manifests, keys.json) are
// durably written. The WORM sink is connectors/s3archive (S3
// object-lock), adapted by the composition root; DirSink below is the
// directory sink for tests, air-gapped exports and the CLI. A sink must be
// safe for sequential reuse; the archival loop never writes one key from two
// goroutines.
type ArchiveSink interface {
	// Put writes one object. Implementations verify body against
	// opts.ContentSHA256 when set (fail-closed: a corrupt buffer must not be
	// sealed), apply the requested retention/lock when the substrate supports
	// it, and report honestly in the receipt what was verified.
	Put(ctx context.Context, key string, body []byte, opts ArchivePutOptions) (ArchiveReceipt, error)
}

// ArchivePutOptions carry per-object integrity and immutability requests.
type ArchivePutOptions struct {
	// ContentSHA256 is the hex SHA-256 of body ("" skips the check).
	ContentSHA256 string
	// RetainUntil requests object-lock retention until the instant; the zero
	// time defers to the sink/bucket default.
	RetainUntil time.Time
	// LegalHold requests an object-lock legal hold (independent of retention).
	LegalHold bool
}

// ArchiveReceipt is the sink's non-secret write attestation, recorded in the
// segment anchor event (AnchorSegment) as custody evidence.
type ArchiveReceipt struct {
	// Location is where the object landed (a URL, bucket/key, or file path).
	Location string
	// ETag and VersionID are the substrate's object identifiers ("" when the
	// substrate has none).
	ETag      string
	VersionID string
	// LockMode is the immutability mode actually applied ("" when none).
	LockMode string
	// RetainUntil is the retention applied (zero when none).
	RetainUntil time.Time
	// LockVerified is true only when the sink CONFIRMED the lock after writing
	// (e.g. s3archive's verify-after-write HEAD). A sink that cannot enforce a
	// lock reports false — it never claims immutability it does not have.
	LockVerified bool
}

// DirSink writes archive objects under a root directory, then drops write
// permission (0o444) on each file. This is the filelog WORM posture (docs/SECURITY-HARDENING.md
// §5): the copy is truly immutable only when the substrate is (a WORM share, a
// write-once medium, an append-only volume) — the chmod prevents accidental
// overwrites, not a root attacker, and the receipt honestly reports
// LockVerified=false. Re-putting a key with identical content succeeds
// (idempotent recovery, mirroring s3archive's re-PUT semantics); different
// content for an existing key is refused.
type DirSink struct {
	root string
}

// Compile-time proof DirSink satisfies the sink seam.
var _ ArchiveSink = (*DirSink)(nil)
