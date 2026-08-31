// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
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

// segmentFormat stamps the tag the segment's own bytes justify, so the version bump
// is BIDIRECTIONAL: a new writer keeps producing v1 artifacts for pre-blinding rows,
// and only a segment that actually carries a blind claims v2.
func segmentFormat(carriesBlind bool) string {
	if carriesBlind {
		return ArchiveFormat
	}
	return ArchiveFormatV1
}

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
var errStopWalk = errors.New("audit: stop walk")

// BuildSegment reads up to maxEvents events from fromSeq, bounded inclusively by
// toSeq when it is positive (zero is unbounded), and builds one archive segment.
// It requires the log's store.CanonicalWalker capability; the stored canonical
// meta is what the chain hash commits to (Walk drops it), and a clear error is
// returned when the log does not expose it. Every event's canonical hash is
// re-derived while building; linkage and declared sequence gaps are checked, so
// a corrupt range fails HERE rather than being sealed into a WORM archive
// (fail-closed).
// ok=false when the requested range has no events. The build is fully
// DETERMINISTIC: the same range of the same chain always yields byte-identical
// events and manifest (no wall clock anywhere), so a retried export re-puts the
// exact bytes a WORM sink already holds.
func BuildSegment(ctx context.Context, log store.AuditLog, tenant model.TenantID, fromSeq int64, maxEvents int, toSeq int64) (Segment, bool, error) {
	cw, isCW := log.(store.CanonicalWalker)
	if !isCW {
		return Segment{}, false, fmt.Errorf("audit: archive export needs the store's canonical-walk capability (store.CanonicalWalker): this AuditLog does not expose the stored canonical meta")
	}
	if fromSeq < 1 {
		fromSeq = 1
	}
	if maxEvents <= 0 {
		maxEvents = DefaultSegmentEvents
	}

	var (
		body                   []byte
		count                  int64
		first, last            model.AuditEvent
		expectedSeq, rangeFrom int64
		expectedPrev, prevSeg  []byte
		// carriesBlind stamps the manifest from the CONTENT rather than from the
		// build: a segment whose rows all predate metadata blinding is byte-for-byte
		// a v1 segment, so calling it v2 would strand it at any verifier — including
		// an older or out-of-tree one — for a field it does not contain, and would
		// break the byte-identical re-put a retried export depends on.
		carriesBlind bool
	)
	err := cw.WalkCanonical(ctx, fromSeq, func(ev model.AuditEvent, metaCanonical string, metaBlind []byte) error {
		if toSeq > 0 && ev.Seq > toSeq {
			return errStopWalk
		}
		if count == 0 {
			// The first event's prev_hash IS the previous segment's last hash in a
			// contiguous archive (and the zero genesis hash at seq 1) — recording it
			// in the manifest chains segments with no external state.
			expectedSeq = ev.Seq
			rangeFrom = ev.Seq
			expectedPrev = ev.PrevHash
			prevSeg = ev.PrevHash
			first = ev
			// A marker may be the first physical row after fromSeq. Its manifest
			// starts at the declared hole so boundary consumers keep using the
			// covered range, while prev_segment_last_hash remains the physical link.
			if ev.Seq > fromSeq && ev.Action == ActionGap && store.DeclaresGap(metaCanonical, fromSeq, ev.Seq) {
				expectedSeq = fromSeq
				rangeFrom = fromSeq
			}
		}
		// Export-time integrity: never seal an undeclared break into the archive;
		// the operator runs `audit verify` against the hot store instead.
		if ev.Seq != expectedSeq {
			if ev.Action != ActionGap || !store.DeclaresGap(metaCanonical, expectedSeq, ev.Seq) {
				return fmt.Errorf("audit: archive export: seq-gap at %d (want %d)", ev.Seq, expectedSeq)
			}
		} else if ev.Action == ActionGap {
			// An in-place marker (no hole before it) is a shape the live verifier
			// and the archive verifier both reject as gap-mismatch — sealing it
			// would mint a WORM archive that can never verify. Same fail-closed
			// stance as an undeclared break: refuse at export time.
			return fmt.Errorf("audit: archive export: gap marker without a hole at seq %d", ev.Seq)
		}
		if !bytesEq(ev.PrevHash, expectedPrev) {
			return fmt.Errorf("audit: archive export: prev-mismatch at seq %d", ev.Seq)
		}
		// Re-derive the commitment from the very bytes this segment is about to
		// WRITE, not from the value the row decoder handed us. The two agree today
		// because the decoder derives it from the same stored pair, but the archive
		// must not depend on that: its contract is that a corrupt range fails HERE
		// rather than being sealed into a WORM object, and the object it seals is the
		// LINE. If the decoded commitment could ever disagree with the (meta, blind)
		// written to the line — a decoder change, a second walker implementation, a
		// row altered between read and marshal — blessing the decoder's value would
		// pass the hash check and mint an archive under an S3 COMPLIANCE lock, held
		// for years, whose own line can never reproduce its own hash. Deriving here
		// makes the check self-contained: what is verified is exactly what is stored.
		// The tenant is in the HASH but not in the LINE: the verifier takes it from
		// the manifest. A row belonging to another chain would therefore build
		// cleanly here and be sealed into a WORM object that can never verify, since
		// the verifier would rebuild its preimage with the manifest's tenant. Fail at
		// export, where the object can still not be written.
		if ev.TenantID != tenant {
			return fmt.Errorf("audit: archive export: seq %d belongs to tenant %s, not %s", ev.Seq, ev.TenantID, tenant)
		}
		commitment, err := canon.MetaCommitmentFor(metaBlind, metaCanonical)
		if err != nil {
			return fmt.Errorf("audit: archive export: at seq %d: %w", ev.Seq, err)
		}
		if !bytesEq(commitment, ev.MetaCommitment) {
			return fmt.Errorf("audit: archive export: meta-commitment mismatch at seq %d (the stored metadata and blind do not reproduce the decoded commitment)", ev.Seq)
		}
		want, err := canon.EventHash(canon.Event{
			TenantID:       ev.TenantID.String(),
			Seq:            ev.Seq,
			OccurredAt:     ev.OccurredAt.String(),
			Actor:          ev.Actor,
			ActorKind:      ev.ActorKind,
			Action:         ev.Action,
			TargetKind:     string(ev.TargetKind),
			TargetID:       ev.TargetID.String(),
			MetaCommitment: commitment,
			PayloadHash:    ev.PayloadHash,
			PrevHash:       ev.PrevHash,
		})
		if err != nil {
			return fmt.Errorf("audit: archive export: at seq %d: %w", ev.Seq, err)
		}
		if !bytesEq(want, ev.Hash) {
			return fmt.Errorf("audit: archive export: hash-mismatch at seq %d (run `audit verify` against the store)", ev.Seq)
		}
		line, err := json.Marshal(archiveLine{
			Seq:         ev.Seq,
			ID:          ev.ID.String(),
			OccurredAt:  ev.OccurredAt.String(),
			Actor:       ev.Actor,
			ActorKind:   ev.ActorKind,
			Action:      ev.Action,
			TargetKind:  string(ev.TargetKind),
			TargetID:    ev.TargetID.String(),
			Meta:        metaCanonical,
			MetaBlind:   hex.EncodeToString(metaBlind),
			PayloadHash: hex.EncodeToString(ev.PayloadHash),
			PrevHash:    hex.EncodeToString(ev.PrevHash),
			Hash:        hex.EncodeToString(ev.Hash),
			Sig:         base64.StdEncoding.EncodeToString(ev.Sig),
		})
		if err != nil {
			return err
		}
		if len(metaBlind) > 0 {
			carriesBlind = true
		}
		body = append(body, line...)
		body = append(body, '\n')
		count++
		last = ev
		expectedSeq = ev.Seq + 1
		expectedPrev = ev.Hash
		if count >= int64(maxEvents) {
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return Segment{}, false, err
	}
	if count == 0 {
		return Segment{}, false, nil
	}
	sum := sha256.Sum256(body)
	return Segment{
		Manifest: SegmentManifest{
			Format:              segmentFormat(carriesBlind),
			Tenant:              tenant.String(),
			FromSeq:             rangeFrom,
			ToSeq:               last.Seq,
			Count:               count,
			FirstHash:           hex.EncodeToString(first.Hash),
			LastHash:            hex.EncodeToString(last.Hash),
			PrevSegmentLastHash: hex.EncodeToString(prevSeg),
			EventsSHA256:        hex.EncodeToString(sum[:]),
			// Content-derived, never the wall clock: see the field's doc comment.
			CreatedAt: last.OccurredAt.String(),
		},
		Events: body,
	}, true, nil
}

// ExportOptions configure ExportSegments.
type ExportOptions struct {
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

// ExportSegments drains a tenant's chain from opts.FromSeq through sink in
// segments of opts.SegmentEvents (§8.5). The chain head is captured
// ONCE up front and only events up to it are exported: events appended during
// the run — including the caller's own anchor events — wait for the next run,
// so a drain loop that anchors each segment terminates instead of chasing its
// own tail. Each segment is built in its own read transaction (constant,
// segment-sized memory) and written events-first, manifest-second; onSegment
// (optional) runs after both writes and aborts the export on error — a failed
// anchor must not advance the resume point. Delivery is at-least-once and
// CONVERGENT: segments are deterministic (BuildSegment) and a retried run that
// pins the in-flight boundary (opts.PendingToSeq) re-puts byte-identical
// objects to the same keys — DirSink absorbs an identical re-put; S3
// versioning+lock mints a harmless extra locked version.
func ExportSegments(ctx context.Context, st store.Store, tenant model.TenantID, sink ArchiveSink, opts ExportOptions, onSegment func(SegmentResult) error) (ExportReport, error) {
	if sink == nil {
		return ExportReport{}, fmt.Errorf("audit: archive export: nil sink")
	}
	next := opts.FromSeq
	if next < 1 {
		next = 1
	}
	segEvents := opts.SegmentEvents
	if segEvents <= 0 {
		segEvents = DefaultSegmentEvents
	}

	var head store.HeadRef
	var hasHead bool
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		head, hasHead, err = sc.Audit().Head(ctx)
		return err
	}); err != nil {
		return ExportReport{}, err
	}
	var rep ExportReport
	if !hasHead || head.Seq < next {
		return rep, nil // nothing pending
	}
	if opts.PendingToSeq >= next && head.Seq < opts.PendingToSeq {
		// A pending boundary always came from an earlier head snapshot and the
		// head never shrinks, so this is corrupt bookkeeping — refuse to guess.
		return rep, fmt.Errorf("audit: archive export: pending boundary %d is beyond the chain head %d (corrupt bookkeeping?)", opts.PendingToSeq, head.Seq)
	}

	first := true
	for next <= head.Seq {
		n := segEvents
		segmentToSeq := head.Seq
		if first && opts.PendingToSeq >= next {
			// Reuse the persisted boundary VERBATIM, even when the configured
			// segment size changed in between. The inclusive sequence bound pins
			// the key; the span-sized row cap only ensures it cannot truncate first.
			n = int(opts.PendingToSeq - next + 1)
			segmentToSeq = opts.PendingToSeq
		}
		var seg Segment
		var ok bool
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			seg, ok, err = BuildSegment(ctx, sc.Audit(), tenant, next, n, segmentToSeq)
			return err
		}); err != nil {
			return rep, err
		}
		if !ok {
			break
		}
		// The manifest covers declared holes, so it must still start exactly at
		// next; a later start means rows below next were purged without a marker.
		if seg.Manifest.FromSeq != next {
			return rep, fmt.Errorf("audit: archive export: chain starts at seq %d, expected %d (rows missing below)", seg.Manifest.FromSeq, next)
		}
		if first && opts.PendingToSeq >= next && seg.Manifest.ToSeq != opts.PendingToSeq {
			// The pinned boundary did not rebuild exactly — refuse to put a key
			// that overlaps the one the previous run already sealed.
			return rep, fmt.Errorf("audit: archive export: pending segment rebuilt as %d-%d, expected %d-%d (corrupt bookkeeping?)", seg.Manifest.FromSeq, seg.Manifest.ToSeq, next, opts.PendingToSeq)
		}
		first = false
		if opts.BeforePut != nil {
			if err := opts.BeforePut(seg.Manifest); err != nil {
				return rep, err
			}
		}
		res := SegmentResult{
			Manifest:    seg.Manifest,
			EventsKey:   SegmentKey(seg.Manifest.Tenant, seg.Manifest.FromSeq, seg.Manifest.ToSeq),
			ManifestKey: SegmentManifestKey(seg.Manifest.Tenant, seg.Manifest.FromSeq, seg.Manifest.ToSeq),
		}
		var err error
		res.EventsReceipt, err = sink.Put(ctx, res.EventsKey, seg.Events, ArchivePutOptions{
			ContentSHA256: seg.Manifest.EventsSHA256,
			RetainUntil:   opts.RetainUntil,
			LegalHold:     opts.LegalHold,
		})
		if err != nil {
			return rep, fmt.Errorf("audit: archive export: put %s: %w", res.EventsKey, err)
		}
		mb, err := json.Marshal(seg.Manifest)
		if err != nil {
			return rep, err
		}
		msum := sha256.Sum256(mb)
		res.ManifestReceipt, err = sink.Put(ctx, res.ManifestKey, mb, ArchivePutOptions{
			ContentSHA256: hex.EncodeToString(msum[:]),
			RetainUntil:   opts.RetainUntil,
			LegalHold:     opts.LegalHold,
		})
		if err != nil {
			return rep, fmt.Errorf("audit: archive export: put %s: %w", res.ManifestKey, err)
		}
		if onSegment != nil {
			if err := onSegment(res); err != nil {
				return rep, err
			}
		}
		if rep.Segments == 0 {
			rep.FromSeq = seg.Manifest.FromSeq
		}
		rep.Segments++
		rep.Events += seg.Manifest.Count
		rep.ToSeq = seg.Manifest.ToSeq
		rep.LastHash = seg.Manifest.LastHash
		next = seg.Manifest.ToSeq + 1
	}
	return rep, nil
}

// SegmentAnchorDraft builds the audit.archive.segment anchor draft for a
// durably written segment. It is split from AnchorSegment so a
// caller can append the anchor INSIDE its own Mutate tx, atomically with its
// resume bookkeeping — a crash can then never separate "anchored" from
// "advanced", which would shift the next segment's boundary (the anchor event
// itself moves the head). The meta carries only identifiers, counts and hashes
// (docs/SECURITY-HARDENING.md) — the range, the events object's key and digest, and the sink
// receipt's non-secret lock attestation.
func SegmentAnchorDraft(res SegmentResult) model.AuditDraft {
	meta := map[string]any{
		"archive.from_seq":      res.Manifest.FromSeq,
		"archive.to_seq":        res.Manifest.ToSeq,
		"archive.count":         res.Manifest.Count,
		"archive.events_sha256": res.Manifest.EventsSHA256,
		"archive.key":           res.EventsKey,
		"archive.lock_verified": res.EventsReceipt.LockVerified,
	}
	if res.EventsReceipt.Location != "" {
		meta["archive.location"] = res.EventsReceipt.Location
	}
	if res.EventsReceipt.ETag != "" {
		meta["archive.etag"] = res.EventsReceipt.ETag
	}
	if res.EventsReceipt.VersionID != "" {
		meta["archive.version_id"] = res.EventsReceipt.VersionID
	}
	if res.EventsReceipt.LockMode != "" {
		meta["archive.lock_mode"] = res.EventsReceipt.LockMode
	}
	if !res.EventsReceipt.RetainUntil.IsZero() {
		meta["archive.retain_until"] = model.NewTimestamp(res.EventsReceipt.RetainUntil).String()
	}
	return model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: ActionArchiveSegment, TargetKind: "core.audit_archive_segment",
		Meta: meta,
	}
}

// AnchorSegment appends the audit.archive.segment anchor event for a durably
// written segment: the archive is anchored INSIDE the chain it
// archives, and the next segment contains the anchor. A caller that also keeps
// resume bookkeeping should instead append SegmentAnchorDraft inside the SAME
// Mutate tx as its bookkeeping write (the §8.5 loop does).
func AnchorSegment(ctx context.Context, st store.Store, tenant model.TenantID, res SegmentResult) (model.AuditEvent, error) {
	var ev model.AuditEvent
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		ev, err = sc.Audit().Append(ctx, SegmentAnchorDraft(res))
		return err
	})
	if err != nil {
		return model.AuditEvent{}, err
	}
	return ev, nil
}

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

// WriteArchiveKeys marshals keys and writes them to sink under ArchiveKeysName.
func WriteArchiveKeys(ctx context.Context, sink ArchiveSink, keys ArchiveKeys) (ArchiveReceipt, error) {
	if keys.Format == "" {
		keys.Format = ArchiveKeysFormat
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	sum := sha256.Sum256(b)
	return sink.Put(ctx, ArchiveKeysName, b, ArchivePutOptions{ContentSHA256: hex.EncodeToString(sum[:])})
}

// bytesEq is digest equality (length-aware; not secret-comparing — these are
// public hashes).
func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
