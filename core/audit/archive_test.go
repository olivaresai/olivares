// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// appendMetaEvents appends n meta-bearing events (the meta is what the archive
// must carry verbatim for the offline canon re-derivation to work).
func appendMetaEvents(t *testing.T, st store.Store, tenant model.TenantID, n int, marker string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.update", TargetKind: "core.agent",
				Meta: map[string]any{"note": marker + "-" + strconv.Itoa(i), "i": i},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append meta events: %v", err)
	}
}

// exportArchive seeds a signed multi-hundred-event chain (meta-bearing events +
// interleaved checkpoints), exports it in small segments to a fresh dir, and
// returns everything a tamper test needs.
func exportArchive(t *testing.T, segmentEvents int) (dir string, tenant model.TenantID, signer *audit.Signer, pub ed25519.PublicKey, rep audit.ExportReport) {
	t.Helper()
	dir, _, tenant, signer, pub, rep = exportArchiveStore(t, segmentEvents)
	return dir, tenant, signer, pub, rep
}

func exportArchiveStore(t *testing.T, segmentEvents int) (dir string, st store.Store, tenant model.TenantID, signer *audit.Signer, pub ed25519.PublicKey, rep audit.ExportReport) {
	t.Helper()
	ctx := context.Background()
	var priv ed25519.PrivateKey
	pub, priv, _ = ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st = signedStore(t, signer)
	tenant = provisionTenant(t, st) // seeds seq 1 (org.create)
	for batch := 0; batch < 3; batch++ {
		appendMetaEvents(t, st, tenant, 80, "batch"+strconv.Itoa(batch))
		if _, _, err := signer.Checkpoint(ctx, st, tenant); err != nil {
			t.Fatal(err)
		}
	}
	dir = t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err = audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{SegmentEvents: segmentEvents}, nil)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return dir, st, tenant, signer, pub, rep
}

func TestArchiveExportVerifyRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir, st, tenant, signer, pub, rep := exportArchiveStore(t, 25)

	// 1 org.create + 3×80 meta events + 3 checkpoints = 244 events, 25/segment.
	if rep.Events != 244 || rep.Segments != 10 || rep.FromSeq != 1 || rep.ToSeq != 244 {
		t.Fatalf("export report = %+v", rep)
	}

	// A SECOND tenant shares the archive root (one chain per tenant subtree);
	// the verifier must group and verify both independently. Same store ⇒ both
	// chains are signed by the same engine key.
	other := provisionTenant(t, st)
	appendMetaEvents(t, st, other, 5, "other")
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.ExportSegments(ctx, st, other, sink, audit.ExportOptions{SegmentEvents: 25}, nil); err != nil {
		t.Fatalf("export other tenant: %v", err)
	}

	// Structural verify (no keys): canon re-derivation, linkage, gaps, manifest
	// digests, continuity — across BOTH tenant subtrees of the shared root.
	vrep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vrep.OK || vrep.Tenants != 2 || vrep.Segments != 11 || vrep.Events != 250 || vrep.Checkpoints != 3 {
		t.Fatalf("structural verify = %+v", vrep)
	}
	// The report states the attested range per tenant; both reach genesis here.
	for tn, want := range map[string]int64{tenant.String(): 244, other.String(): 6} {
		if r := vrep.Ranges[tn]; r.FromSeq != 1 || r.ToSeq != want || r.StartsMidChain {
			t.Fatalf("range[%s] = %+v, want 1-%d from genesis", tn, r, want)
		}
	}

	// With the right keys, per-event and checkpoint signatures verify too.
	cpv, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vrep, err = audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{
		EventKeys: []audit.FencedKey{{Key: pub}}, Checkpoints: cpv,
	})
	if err != nil {
		t.Fatalf("signed verify: %v", err)
	}
	if !vrep.OK || vrep.Checkpoints != 3 {
		t.Fatalf("signed verify = %+v", vrep)
	}

	// The WRONG event key must fail (the --strict CLI gate rides on rep.OK).
	otherPub, _, _ := ed25519.GenerateKey(nil)
	vrep, err = audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: otherPub}}})
	if err != nil {
		t.Fatal(err)
	}
	if vrep.OK || vrep.Reason != "event-sig-invalid" {
		t.Fatalf("wrong-key verify = %+v", vrep)
	}
	// The wrong checkpoint key likewise.
	wrongCp := audit.NewCheckpointVerifier().AddEd25519(otherPub)
	vrep, _ = audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{Checkpoints: wrongCp})
	if vrep.OK || vrep.Reason != "checkpoint-sig-invalid" {
		t.Fatalf("wrong-checkpoint-key verify = %+v", vrep)
	}

	// Every archive file is read-only (the DirSink WORM-when-substrate-is posture).
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if perm := info.Mode().Perm(); perm != 0o444 {
			t.Errorf("%s mode = %o, want 0444", p, perm)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// segmentFiles lists the exported events files in from_seq order.
func segmentFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

// rewrite force-overwrites a read-only archive file (the attacker has the perms).
func rewrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveTamperOneByteFails(t *testing.T) {
	ctx := context.Background()
	dir, _, _, _, _ := exportArchive(t, 50)
	files := segmentFiles(t, dir)

	// Flip one byte inside the base64 sig field of the first line: the JSON stays
	// valid and the canon hash (which excludes the sig) still re-derives, so the
	// catch is the manifest's events_sha256 — the whole-file digest.
	body, err := os.ReadFile(files[1])
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(body), `"sig":"`) + len(`"sig":"`)
	tampered := append([]byte(nil), body...)
	if tampered[i] != 'A' {
		tampered[i] = 'A'
	} else {
		tampered[i] = 'B'
	}
	rewrite(t, files[1], tampered)

	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "events-sha256-mismatch" {
		t.Fatalf("tampered verify = %+v, want events-sha256-mismatch", rep)
	}
}

func TestArchiveAlteredMetaFailsCanonRederivation(t *testing.T) {
	ctx := context.Background()
	dir, _, _, _, _ := exportArchive(t, 50)
	files := segmentFiles(t, dir)

	// Alter the canonical meta of one archived event (batch0-3 → batch0-9). The
	// chain hash commits to MetaDigest(stored canonical meta), so the offline
	// canon re-derivation must break at exactly that event — the property
	// the old meta-less export could not give.
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	doctored := strings.Replace(string(body), `batch0-3`, `batch0-9`, 1)
	if doctored == string(body) {
		t.Fatal("marker not found in segment 0")
	}
	rewrite(t, files[0], []byte(doctored))

	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "hash-mismatch" {
		t.Fatalf("altered-meta verify = %+v, want hash-mismatch", rep)
	}
	if rep.BreakSegment == "" || rep.BreakAt == 0 {
		t.Fatalf("failure not located: %+v", rep)
	}
}

func TestArchiveRejectsReplayForwardRecoveryMarker(t *testing.T) {
	ctx := context.Background()
	_, onBoxPrivate, _ := ed25519.GenerateKey(nil)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPrivate, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 1)

	evidence := audit.RecoveryEvidence{
		Tenant: tenant.String(), BreakReason: "hash-mismatch", BreakAt: 2,
		ReanchorSeq: 1, OffBoxCheckpointSeq: 1, OffBoxKeyID: kms.KeyID(),
		QuarantinedFrom: 2, QuarantinedTo: 2, QuarantinedSHA256: strings.Repeat("ab", 32),
		Approvers: []string{"user:alice", "user:bob"}, Reason: "archive attack", RequestedBy: "svc:test",
	}
	var marker model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		marker, rerr = audit.RecordRecovery(ctx, sc.Audit(), signer, evidence)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{FromSeq: marker.Seq, SegmentEvents: 1}, nil); err != nil {
		t.Fatal(err)
	}
	verifier, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{Checkpoints: verifier}); err != nil || !rep.OK {
		t.Fatalf("legitimate recovery archive = %+v err=%v", rep, err)
	}

	files := segmentFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("archive files = %d, want 1", len(files))
	}
	eventsPath := files[0]
	body, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	var line recoveryArchiveAttackLine
	if err := json.Unmarshal(bytes.TrimSpace(body), &line); err != nil {
		t.Fatal(err)
	}
	line.Seq++
	payloadHash, err := hex.DecodeString(line.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	prevHash, err := hex.DecodeString(line.PrevHash)
	if err != nil {
		t.Fatal(err)
	}
	line.Hash = hex.EncodeToString(mustLineHash(t, canon.Event{
		TenantID: tenant.String(), Seq: line.Seq, OccurredAt: line.OccurredAt,
		Actor: line.Actor, ActorKind: line.ActorKind, Action: line.Action,
		TargetKind: line.TargetKind, TargetID: line.TargetID,
		// The line's OWN rule, resolved from the blind it carries — the fourth site
		// where picking MetaDigest here would re-hash a blinded row under the legacy
		// rule and make this attack fail as a hash mismatch instead of as the
		// position violation it is constructed to prove.
		MetaCommitment: mustLineCommitment(t, line.MetaBlind, line.Meta),
		PayloadHash:    payloadHash, PrevHash: prevHash,
	}))
	doctored, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	doctored = append(doctored, '\n')

	manifestPath := eventsPath + ".manifest.json"
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest audit.SegmentManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.FromSeq, manifest.ToSeq = line.Seq, line.Seq
	manifest.FirstHash, manifest.LastHash = line.Hash, line.Hash
	manifest.PrevSegmentLastHash = line.PrevHash
	sum := sha256.Sum256(doctored)
	manifest.EventsSHA256 = hex.EncodeToString(sum[:])
	doctoredManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, eventsPath, doctored)
	rewrite(t, manifestPath, doctoredManifest)
	newEventsPath := filepath.Join(dir, audit.SegmentKey(tenant.String(), line.Seq, line.Seq))
	if err := os.Rename(eventsPath, newEventsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(manifestPath, newEventsPath+".manifest.json"); err != nil {
		t.Fatal(err)
	}

	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{Checkpoints: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "recovery-position-invalid" || rep.BreakAt != line.Seq {
		t.Fatalf("replayed recovery archive = %+v, want recovery-position-invalid", rep)
	}
}

type recoveryArchiveAttackLine struct {
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Meta       string `json:"meta"`
	// Without this field the unmarshal/marshal round trip below would STRIP the
	// blind, silently switching the line to the legacy hash rule and making the
	// attack this test constructs fail for the wrong reason.
	MetaBlind   string `json:"meta_blind,omitempty"`
	PayloadHash string `json:"payload_hash"`
	PrevHash    string `json:"prev_hash"`
	Hash        string `json:"hash"`
	Sig         string `json:"sig"`
}

func TestArchiveMissingSegmentBreaksContinuity(t *testing.T) {
	ctx := context.Background()
	dir, _, _, _, _ := exportArchive(t, 50)
	files := segmentFiles(t, dir)
	if len(files) < 3 {
		t.Fatalf("want ≥3 segments, got %d", len(files))
	}

	// Remove a MIDDLE segment (events + manifest): no in-file check can notice,
	// only the cross-segment continuity (from = prev.to+1) can.
	mid := files[len(files)/2]
	for _, p := range []string{mid, mid + ".manifest.json"} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "segment-gap" {
		t.Fatalf("missing-segment verify = %+v, want segment-gap", rep)
	}
}

// TestArchiveResumeAcrossRuns proves the multi-year property: segments written
// by SEPARATE export runs (the §8.5 loop's resume-from-bookkeeping) still chain
// via prev_segment_last_hash with no external state, and anchoring each segment
// (AnchorSegment) terminates — the anchors land AFTER the captured head and are
// drained by the NEXT run, whose first segment then contains them.
func TestArchiveResumeAcrossRuns(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 30, "run1")

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	var anchors int
	anchor := func(res audit.SegmentResult) error {
		anchors++
		_, aerr := audit.AnchorSegment(ctx, st, tenant, res)
		return aerr
	}
	rep1, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{SegmentEvents: 10}, anchor)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	// 31 events, head captured up front: the run's own anchors are NOT chased.
	if rep1.Events != 31 || rep1.Segments != 4 || anchors != 4 {
		t.Fatalf("run 1 report = %+v anchors=%d", rep1, anchors)
	}

	appendMetaEvents(t, st, tenant, 12, "run2")
	rep2, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{FromSeq: rep1.ToSeq + 1, SegmentEvents: 10}, anchor)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	// Pending = 4 anchors from run 1 + 12 new events.
	if rep2.FromSeq != rep1.ToSeq+1 || rep2.Events != 16 {
		t.Fatalf("run 2 report = %+v", rep2)
	}

	vrep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: pub}}})
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK || vrep.Events != rep1.Events+rep2.Events {
		t.Fatalf("cross-run verify = %+v", vrep)
	}

	// The run-2 archive carries the run-1 anchor events (cross-anchoring §8.2):
	// find one and check its minimal-data meta (ids/counts/hashes only).
	var sawAnchor bool
	for _, p := range segmentFiles(t, dir) {
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var rec map[string]any
			if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
				t.Fatalf("bad line: %v", jerr)
			}
			if rec["action"] == audit.ActionArchiveSegment {
				sawAnchor = true
				meta := rec["meta"].(string)
				for _, want := range []string{"archive.from_seq", "archive.to_seq", "archive.events_sha256", "archive.key"} {
					if !strings.Contains(meta, want) {
						t.Errorf("anchor meta missing %s: %s", want, meta)
					}
				}
			}
		}
	}
	if !sawAnchor {
		t.Fatal("no audit.archive.segment anchor found in the run-2 archive")
	}
}

// TestBuildSegmentRequiresCanonicalWalker pins the clear error when the log
// lacks the capability (walkLog implements store.AuditLog but not WalkCanonical).
func TestBuildSegmentRequiresCanonicalWalker(t *testing.T) {
	_, _, err := audit.BuildSegment(context.Background(), walkLog{}, model.TenantID("22222222-2222-7222-8222-222222222222"), 1, 10, 0)
	if err == nil || !strings.Contains(err.Error(), "CanonicalWalker") {
		t.Fatalf("err = %v, want a clear CanonicalWalker error", err)
	}
}

// TestBuildSegmentDeterministicManifest is the regression for the retry wedge:
// the manifest used to stamp created_at from the wall clock, so a RETRIED
// build of the same range was byte-different and the WORM DirSink refused the
// re-put — that tenant's drain wedged forever. Two builds of the same range,
// even after the live head moved, must be byte-identical, with created_at
// derived from the segment content (the last event's canonical occurred_at).
func TestBuildSegmentDeterministicManifest(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 9, "det") // head = 10 with the org.create

	build := func() audit.Segment {
		var seg audit.Segment
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var ok bool
			var err error
			seg, ok, err = audit.BuildSegment(ctx, sc.Audit(), tenant, 1, 5, 0)
			if err == nil && !ok {
				t.Fatal("no segment built")
			}
			return err
		}); err != nil {
			t.Fatalf("build: %v", err)
		}
		return seg
	}
	seg1 := build()
	appendMetaEvents(t, st, tenant, 3, "later") // the head moves between attempts
	seg2 := build()

	m1, err := json.Marshal(seg1.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := json.Marshal(seg2.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(m1) != string(m2) {
		t.Fatalf("retried manifest differs:\n%s\n%s", m1, m2)
	}
	if string(seg1.Events) != string(seg2.Events) {
		t.Fatal("retried events body differs")
	}

	// created_at IS the last event's canonical occurred_at, never a clock.
	var lastOccurred string
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 5, func(ev model.AuditEvent) error {
			if ev.Seq == 5 {
				lastOccurred = ev.OccurredAt.String()
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if seg1.Manifest.CreatedAt != lastOccurred {
		t.Fatalf("created_at = %q, want the last event's occurred_at %q", seg1.Manifest.CreatedAt, lastOccurred)
	}
}

// TestArchiveRetryAfterAnchorFailureConverges reproduces the at-least-once
// crash window of the §8.5 loop: the tail segment's objects are durably
// written, then the anchor fails. The anchors of the EARLIER segments already
// moved the head, so before the fix a retry (1) recomputed the tail boundary
// from the moved head — a different key overlapping the sealed one, a
// permanent "segment-gap" — and (2) rebuilt a byte-different manifest (wall-
// clock created_at) the WORM sink refused, wedging the drain forever. With
// deterministic builds plus the pinned pending boundary, the retry re-puts
// byte-identical objects to the same keys and the drain advances.
func TestArchiveRetryAfterAnchorFailureConverges(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 23, "run1") // head = 24: segments (1-10),(11-20),(21-24)

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Run 1: anchor the first two segments, then crash AFTER the tail
	// segment's Puts but BEFORE its anchor. BeforePut captures the in-flight
	// boundary exactly as the loop persists it.
	var pending audit.SegmentManifest
	crash := errors.New("crash before anchor")
	rep1, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{
		SegmentEvents: 10,
		BeforePut:     func(m audit.SegmentManifest) error { pending = m; return nil },
	}, func(res audit.SegmentResult) error {
		if res.Manifest.ToSeq > 20 { // the tail segment
			return crash
		}
		_, aerr := audit.AnchorSegment(ctx, st, tenant, res)
		return aerr
	})
	if !errors.Is(err, crash) {
		t.Fatalf("run 1 err = %v, want the simulated crash", err)
	}
	if rep1.Segments != 2 || rep1.ToSeq != 20 {
		t.Fatalf("run 1 report = %+v, want 2 anchored segments through seq 20", rep1)
	}
	if pending.FromSeq != 21 || pending.ToSeq != 24 {
		t.Fatalf("pending boundary = %d-%d, want 21-24", pending.FromSeq, pending.ToSeq)
	}

	// Run 2 (the next tick): the head moved (two anchors, seqs 25-26). The
	// pinned pending boundary rebuilds 21-24 byte-identically — the sink
	// absorbs the re-put — and the drain then advances through the anchors.
	rep2, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{
		FromSeq: 21, SegmentEvents: 10, PendingToSeq: pending.ToSeq,
	}, func(res audit.SegmentResult) error {
		_, aerr := audit.AnchorSegment(ctx, st, tenant, res)
		return aerr
	})
	if err != nil {
		t.Fatalf("retry must converge on the sealed objects, got: %v", err)
	}
	if rep2.FromSeq != 21 || rep2.ToSeq != 26 || rep2.Segments != 2 {
		t.Fatalf("run 2 report = %+v, want segments 21-24 (reused) and 25-26", rep2)
	}

	// No orphaned overlap, no gap: the whole chain verifies, signatures included.
	vrep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: pub}}})
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK {
		t.Fatalf("post-retry verify = %+v", vrep)
	}
	if r := vrep.Ranges[tenant.String()]; r.FromSeq != 1 || r.ToSeq != 26 || r.StartsMidChain {
		t.Fatalf("attested range = %+v, want 1-26 from genesis", r)
	}
}

// TestArchiveCheckpointUnverifiableWithOnlyEventKeys closes the checkpoint-
// forgery hole: checkpoint lines are exempt from the per-event signature
// check, and the canon chain re-derives WITHOUT any key — so an attacker could
// forge a whole archive whose every event is dressed as audit.checkpoint and,
// with only event keys pinned, it verified green. A checkpoint line with no
// checkpoint key pinned must now FAIL ("checkpoint-unverifiable"), while the
// no-keys advisory mode keeps verifying chain structure only.
func TestArchiveCheckpointUnverifiableWithOnlyEventKeys(t *testing.T) {
	ctx := context.Background()
	st := testStore(t) // unsigned store: the forger holds no key, canon needs none
	tenant := provisionTenant(t, st)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < 3; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: audit.ActionCheckpoint, TargetKind: "core.audit_checkpoint",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Skip the org.create at seq 1: an unsigned non-checkpoint line would fail
	// event-sig-missing and mask the hole under test.
	if _, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{FromSeq: 2, SegmentEvents: 10}, nil); err != nil {
		t.Fatal(err)
	}

	pub, _, _ := ed25519.GenerateKey(nil)
	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: pub}}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "checkpoint-unverifiable" {
		t.Fatalf("all-checkpoint archive with only event keys = %+v, want checkpoint-unverifiable", rep)
	}

	// No keys at all stays the advisory chain-structure-only mode — and the
	// report is honest that the attested range starts mid-chain.
	rep, err = audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("advisory structural verify = %+v", rep)
	}
	if r := rep.Ranges[tenant.String()]; r.FromSeq != 2 || r.ToSeq != 4 || !r.StartsMidChain {
		t.Fatalf("attested range = %+v, want 2-4 mid-chain", r)
	}
}

// rewriteSegmentFixingManifest rewrites a segment's events file AND patches its
// manifest's events_sha256 to match — the manifest is UNSIGNED, so an attacker
// can always fix that digest; whatever survives this rewrite is what the
// verifier actually guarantees.
func rewriteSegmentFixingManifest(t *testing.T, eventsPath string, body []byte) {
	t.Helper()
	rewrite(t, eventsPath, body)
	mb, err := os.ReadFile(eventsPath + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m audit.SegmentManifest
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	m.EventsSHA256 = hex.EncodeToString(sum[:])
	fixed, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, eventsPath+".manifest.json", fixed)
}

// TestArchiveNonCanonicalLineFails closes the smuggling hole: events_sha256 is
// attacker-fixable (the manifest is unsigned) and the canon hash covers only
// the PARSED fields, so before the byte-binding check a line could carry an
// unknown field or a duplicate key — bytes inside a "verified" archive covered
// by no hash at all. Every line must now be the canonical writer bytes.
func TestArchiveNonCanonicalLineFails(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown-field", func(t *testing.T) {
		dir, _, _, _, _ := exportArchive(t, 50)
		files := segmentFiles(t, dir)
		body, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		doctored := strings.Replace(string(body), `{"seq":`, `{"smuggled":"payload","seq":`, 1)
		if doctored == string(body) {
			t.Fatal("line head not found")
		}
		rewriteSegmentFixingManifest(t, files[0], []byte(doctored))
		rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.OK || rep.Reason != "line-not-canonical" {
			t.Fatalf("smuggled-field verify = %+v, want line-not-canonical", rep)
		}
	})

	t.Run("duplicate-key", func(t *testing.T) {
		dir, _, _, _, _ := exportArchive(t, 50)
		files := segmentFiles(t, dir)
		body, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		// Decoders keep the LAST duplicate, so the canon hash still re-derives;
		// only the byte binding can see the smuggled first value.
		doctored := strings.Replace(string(body), `"actor":"user:x"`, `"actor":"smuggled","actor":"user:x"`, 1)
		if doctored == string(body) {
			t.Fatal("actor field not found")
		}
		rewriteSegmentFixingManifest(t, files[0], []byte(doctored))
		rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.OK || rep.Reason != "line-not-canonical" {
			t.Fatalf("duplicate-key verify = %+v, want line-not-canonical", rep)
		}
	})
}

// TestArchivePrefixTruncationReportsRange pins the verifier's honesty about its
// own scope: a removed PREFIX is undetectable offline (nothing in the directory
// proves where the chain began), so it is NOT a failure — legitimate partial
// exports exist — but the report must state exactly which range was attested
// and flag that it starts mid-chain.
func TestArchivePrefixTruncationReportsRange(t *testing.T) {
	ctx := context.Background()
	dir, tenant, _, _, rep := exportArchive(t, 50)
	files := segmentFiles(t, dir)
	if len(files) < 2 {
		t.Fatalf("want ≥2 segments, got %d", len(files))
	}
	for _, p := range []string{files[0], files[0] + ".manifest.json"} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	vrep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK {
		t.Fatalf("prefix-truncated verify = %+v, want OK (out of scope offline)", vrep)
	}
	r := vrep.Ranges[tenant.String()]
	if r.FromSeq != 51 || r.ToSeq != rep.ToSeq || !r.StartsMidChain {
		t.Fatalf("attested range = %+v, want 51-%d mid-chain", r, rep.ToSeq)
	}
}

func TestDirSinkSemantics(t *testing.T) {
	ctx := context.Background()
	sink, err := audit.NewDirSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload\n")

	// A wrong declared digest is refused before anything is written (fail-closed).
	if _, err := sink.Put(ctx, "t/a.jsonl", body, audit.ArchivePutOptions{ContentSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	rcpt, err := sink.Put(ctx, "t/a.jsonl", body, audit.ArchivePutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Honest receipt: a chmod is not an enforced lock.
	if rcpt.LockVerified || rcpt.Location == "" || rcpt.ETag == "" {
		t.Fatalf("receipt = %+v", rcpt)
	}
	if info, serr := os.Stat(rcpt.Location); serr != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("stat = %v %v, want 0444", info, serr)
	}

	// Idempotent recovery: the same bytes again succeed; different bytes do not.
	if _, err := sink.Put(ctx, "t/a.jsonl", body, audit.ArchivePutOptions{}); err != nil {
		t.Fatalf("idempotent re-put: %v", err)
	}
	if _, err := sink.Put(ctx, "t/a.jsonl", []byte("other"), audit.ArchivePutOptions{}); err == nil {
		t.Fatal("overwrite with different content accepted")
	}

	// Keys may not escape the root.
	for _, key := range []string{"../escape", "/abs", ""} {
		if _, err := sink.Put(ctx, key, body, audit.ArchivePutOptions{}); err == nil {
			t.Fatalf("key %q accepted", key)
		}
	}
}

func TestArchiveKeysRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := audit.LoadArchiveKeys(dir); err != nil || ok {
		t.Fatalf("empty dir: ok=%v err=%v", ok, err)
	}
	want := audit.ArchiveKeys{EventPubKeys: []string{"a2V5"}, CheckpointKeys: []string{"a2V5"}, CreatedAt: "2026-06-10T00:00:00.000000000Z"}
	if _, err := audit.WriteArchiveKeys(ctx, sink, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := audit.LoadArchiveKeys(dir)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.Format != audit.ArchiveKeysFormat || len(got.EventPubKeys) != 1 || got.EventPubKeys[0] != "a2V5" {
		t.Fatalf("keys = %+v", got)
	}
}

func TestVerifyArchiveDirEmptyDoesNotReportSuccess(t *testing.T) {
	rep, err := audit.VerifyArchiveDir(context.Background(), t.TempDir(), audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Events != 0 || rep.Reason != "no-events" {
		t.Fatalf("empty archive reported success: %+v", rep)
	}
}

// mustDecodeHex decodes a hex archive-line field, failing rather than yielding nil:
// a nil blind silently selects the legacy commitment rule.
func mustDecodeHex(t *testing.T, v string) []byte {
	t.Helper()
	b, err := hex.DecodeString(v)
	if err != nil {
		t.Fatalf("decode hex %q: %v", v, err)
	}
	return b
}

// mustLineCommitment resolves an archive line's commitment from the blind the
// LINE carries, preserving the absent/present distinction: an empty hex field is
// an ABSENT blind (nil, the legacy rule), not a zero-length one, which is an
// illegal state the resolver refuses. Decoding "" unconditionally would yield a
// non-nil empty slice and turn every v1 line into a malformed record.
func mustLineCommitment(t *testing.T, blindHex, meta string) []byte {
	t.Helper()
	var blind []byte
	if blindHex != "" {
		blind = mustDecodeHex(t, blindHex)
	}
	c, err := canon.MetaCommitmentFor(blind, meta)
	if err != nil {
		t.Fatalf("resolve line commitment: %v", err)
	}
	return c
}

// mustLineHash hashes a preimage the test expects to be well-formed.
func mustLineHash(t *testing.T, e canon.Event) []byte {
	t.Helper()
	h, err := canon.EventHash(e)
	if err != nil {
		t.Fatalf("EventHash on a well-formed preimage: %v", err)
	}
	return h
}
