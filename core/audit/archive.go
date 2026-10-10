// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// ArchiveFormat is the versioned format tag stamped on every segment manifest
//. The field names below are pinned by this tag and future readers
// depend on them.
//
// v2 adds meta_blind: the per-record blind of the metadata commitment, without
// which a copy carrying the metadata could not verify the commitment the chain
// hash consumes. ArchiveFormats lists every tag a verifier accepts, and v1 stays
// on that list permanently — an archive is a long-horizon artifact whose whole
// purpose is being readable years later, so retiring a tag would strand the
// evidence it was written to preserve. A v1 line has no blind, which is exactly
// the discriminator that selects the unblinded rule its rows were sealed under.
const ArchiveFormat = "olivares.audit.archive.v2"

// ArchiveFormatV1 is the pre-blinding tag. It is not legacy scaffolding: a segment
// whose rows all predate metadata blinding contains no meta_blind field, so it IS a
// v1 artifact and must be stamped as one. Anything else would claim a shape the bytes
// do not have, and would strand the segment at a verifier that only knows v1.
const ArchiveFormatV1 = "olivares.audit.archive.v1"

// ArchiveFormats are the manifest format tags a verifier accepts, newest first.
// Copy it before mutating: it is package state, and a verifier that widened it in
// place would widen it for every other caller in the process.
var ArchiveFormats = []string{ArchiveFormat, ArchiveFormatV1}

const (
	// ActionArchiveSegment is the audit action of a segment anchor event: after a
	// segment is durably written, the engine appends this event (meta: from/to/
	// sha256/key/receipt) so the archive is anchored INSIDE the chain it archives —
	// the next segment contains the anchor, cross-linking archive and ledger.
	ActionArchiveSegment = "audit.archive.segment"
	// ActionGap is the signed in-chain marker that declares the only sanctioned
	// audit sequence discontinuity.
	ActionGap = "audit.gap"
)

// DefaultSegmentEvents is the default maximum number of events per segment
// (OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS).
const DefaultSegmentEvents = 10000

// archiveLine is one JSONL events line. The field names and order
// are PINNED — the offline verifier and any future reader re-derive
// canon.EventHash from exactly these fields. meta is the STORED canonical meta
// string (the authoritative commitment input, store.CanonicalWalker), carried
// raw; meta_blind is that record's commitment blind as hex, omitted for a record
// sealed before blinding existed (its absence selects the legacy unblinded rule);
// hashes are hex; sig is base64 ("" when unsigned).
type archiveLine struct {
	Seq         int64  `json:"seq"`
	ID          string `json:"id"`
	OccurredAt  string `json:"occurred_at"`
	Actor       string `json:"actor"`
	ActorKind   string `json:"actor_kind"`
	Action      string `json:"action"`
	TargetKind  string `json:"target_kind"`
	TargetID    string `json:"target_id"`
	Meta        string `json:"meta"`
	MetaBlind   string `json:"meta_blind,omitempty"`
	PayloadHash string `json:"payload_hash"`
	PrevHash    string `json:"prev_hash"`
	Hash        string `json:"hash"`
	Sig         string `json:"sig"`
}

// SegmentManifest is the sidecar manifest of one segment (field
// names pinned). prev_segment_last_hash is the hex hash the segment's first
// event links to — the previous segment's last_hash in a contiguous archive,
// and the all-zero genesis hash when from_seq is 1 — so segments chain to each
// other with no external state (multi-year continuity).
type SegmentManifest struct {
	Format              string `json:"format"`
	Tenant              string `json:"tenant"`
	FromSeq             int64  `json:"from_seq"`
	ToSeq               int64  `json:"to_seq"`
	Count               int64  `json:"count"`
	FirstHash           string `json:"first_hash"`
	LastHash            string `json:"last_hash"`
	PrevSegmentLastHash string `json:"prev_segment_last_hash"`
	EventsSHA256        string `json:"events_sha256"`
	// CreatedAt is NOT a wall-clock stamp: it is the last event's canonical
	// occurred_at string, derived from the segment CONTENT, so rebuilding the
	// same range yields a byte-identical manifest. That determinism is what
	// makes at-least-once delivery converge — a retried Put after a crash is
	// the SAME bytes to the SAME key, which a WORM sink absorbs instead of
	// refusing. Wall-clock export provenance lives in the sink receipt and the
	// anchor event, never here.
	CreatedAt string `json:"created_at"`
}

// SegmentKey returns the events object key for a segment:
// "<tenant>/seg-<from%012d>-<to%012d>.jsonl". Twelve digits keep keys
// lexicographically ordered for centuries of sequence numbers.
func SegmentKey(tenant string, fromSeq, toSeq int64) string {
	return fmt.Sprintf("%s/seg-%012d-%012d.jsonl", tenant, fromSeq, toSeq)
}

// SegmentManifestKey returns the manifest object key for a segment
// (the events key + ".manifest.json").
func SegmentManifestKey(tenant string, fromSeq, toSeq int64) string {
	return SegmentKey(tenant, fromSeq, toSeq) + ".manifest.json"
}

// Segment is one built archive segment: the JSONL events body plus its manifest.
type Segment struct {
	Manifest SegmentManifest
	// Events is the JSONL body (one line per event, newline-terminated) whose
	// SHA-256 is Manifest.EventsSHA256.
	Events []byte
}

// errStopWalk terminates a canonical walk early once a segment is full; it is
// internal and never escapes BuildSegment.

// ExportOptions configure ExportSegments.
type ExportOptions struct {
	// Ledger optionally reads an existing installation without opening a runtime
	// Store. Offline exports use the catalog-only reader; the archival loop keeps
	// using its Store when Ledger is nil.
	Ledger store.AuditReader
	// FromSeq is the first sequence number to export (<1 means 1). The archival
	// loop resumes from its bookkept last_seq+1.
	FromSeq int64
	// SegmentEvents is the maximum events per segment (<=0 means
	// DefaultSegmentEvents).
	SegmentEvents int
	// RetainUntil and LegalHold are forwarded to every sink Put (object-lock
	// retention on a WORM sink; zero/false defers to the bucket default).
	RetainUntil time.Time
	LegalHold   bool
	// PendingToSeq, when >= FromSeq, pins the FIRST segment's to-boundary
	// instead of recomputing it from the live head (the §8.5 pending-boundary
	// protocol): after a crash between a segment's Puts and its anchor, the
	// retried drain MUST rebuild the byte-identical segment for the same keys.
	// A boundary recomputed from a moved head would orphan the already-sealed
	// WORM objects as a permanent overlap ("segment-gap" forever — undeletable
	// for years on a COMPLIANCE-locked bucket). <FromSeq (the zero value) means
	// no pending boundary. The chain must reach the pinned boundary (it always
	// does when the value comes from a previous run: the head never shrinks);
	// anything else is a loud error, never a guess.
	PendingToSeq int64
	// BeforePut (optional) runs after a segment is built and BEFORE its first
	// sink Put. The §8.5 loop persists the segment's "<from>-<to>" pending
	// boundary there, so a crash mid-write leaves the boundary on record for
	// the next tick to reuse via PendingToSeq. An error aborts the export.
	BeforePut func(SegmentManifest) error
}

// SegmentResult is what ExportSegments hands the per-segment callback after a
// segment (events + manifest) is durably written: enough to anchor the segment
// in the ledger (AnchorSegment) and to advance the resume bookkeeping.
type SegmentResult struct {
	Manifest        SegmentManifest
	EventsKey       string
	ManifestKey     string
	EventsReceipt   ArchiveReceipt
	ManifestReceipt ArchiveReceipt
}

// ExportReport summarizes one ExportSegments run.
type ExportReport struct {
	// Segments and Events are how many segments / events were written.
	Segments int
	Events   int64
	// FromSeq/ToSeq bound the exported range (zero when nothing was pending);
	// the caller resumes at ToSeq+1.
	FromSeq int64
	ToSeq   int64
	// LastHash is the hex chain hash of the last exported event ("" when none).
	LastHash string
}

// Per-field object-identifier limits, derived from the documented forms of
// every substrate this anchor can attest (H-04; independent security review — the shared 128
// byte cap refused lawful S3 VersionIDs and was not any provider's contract):
//
//   - archive.etag: DirSink stores the segment's 64-hex SHA-256
//     (core/audit/dirsink.go). S3 forwards its ETag header: a 32-hex MD5, plus
//     "-<part count>" for multipart uploads (≤ 10000 parts), which with quotes
//     and the W/ weak prefix tops out at 42 bytes
//     (docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html);
//     S3-compatible stores (MinIO, Ceph RGW) follow the same S3 form
//     (pkg.go.dev/minio/pkg/etag). Azure answers a quoted "0x<16 hex>",
//     optionally "W/"-weak (≤ 23; learn.microsoft.com/en-us/rest/api/
//     storageservices/put-blob response headers). GCS answers a short base64
//     etag (cloud.google.com/storage/docs/json_api/v1/objects#resource). No
//     documented form exceeds 42 bytes; DirSink's own 64 is the largest and is
//     the limit.
//   - archive.version_id: AWS documents "Version IDs are Unicode, UTF-8
//     encoded, URL-ready, opaque strings that are no more than 1,024 bytes
//     long" (docs.aws.amazon.com/AmazonS3/latest/userguide/versioning-workflows.html).
//     Azure's x-ms-version-id is a DateTime and GCS's generation a decimal
//     int64 — far shorter. The AWS 1,024-byte contract is the limit.
const (
	maxAnchorETagLen      = 64
	maxAnchorVersionIDLen = 1024
)

// ArchiveKeysFormat tags the advisory keys.json an export writes alongside the
// segments.
const ArchiveKeysFormat = "olivares.audit.archive.keys.v1"

// ArchiveKeysName is the keys.json object key, at the archive root (the engine
// keys are deployment-wide, not per tenant).
const ArchiveKeysName = "keys.json"

// ArchiveKeys is the ADVISORY key material exported next to the segments: the
// engine's own public keys, so a casual verify works out of the box. It is
// advisory by definition — an archive written by a compromised host carries the
// attacker's keys — so verifier-supplied pins REPLACE it (the cmd_audit
// precedent), and an attacker-resistant audit always pins off-box copies of the
// keys (docs/SECURITY-HARDENING.md). An off-box KMS/HSM checkpoint key is never exported here;
// its public key is exactly what the auditor pins.
type ArchiveKeys struct {
	Format string `json:"format"`
	// EventPubKeys are raw base64 Ed25519 keys covering per-event signatures
	// (the current engine key plus prior rotation generations).
	EventPubKeys []string `json:"event_pubkeys"`
	// CheckpointKeys cover checkpoint signatures: raw base64 Ed25519, or
	// "<alg>:<base64 DER SPKI>" for an off-box scheme (the --pubkey spec form).
	CheckpointKeys []string `json:"checkpoint_keys"`
	CreatedAt      string   `json:"created_at"`
}
