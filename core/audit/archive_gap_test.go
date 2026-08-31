// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type realGapFixture struct {
	st     store.Store
	tenant model.TenantID
	signer *audit.Signer
	pub    ed25519.PublicKey
}

// newRealGapFixture creates the production sequence shape: a signed physical
// prefix, dropped positions under the explicit one-byte degrade budget, then a
// signed audit.gap marker sealed by an unsigned-draft exempt archive anchor.
func newRealGapFixture(t *testing.T, drops, checkpointsDuringGap int) realGapFixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(t.TempDir(), "gap.db")
	initial, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open initial store: %v", err)
	}
	tenant := provisionTenant(t, initial) // signed org.create at seq 1
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		SignEvent: signer.SignEvent, AuditSpoolMaxBytes: 1,
		AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, nil)
	if err != nil {
		t.Fatalf("open degrade store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for i := 0; i < drops; i++ {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:degrade", ActorKind: model.ActorUser, Action: "agent.update",
				TargetKind: "core.agent", Meta: map[string]any{"drop": i},
			})
			if err == nil && ev.Seq != 0 {
				t.Fatalf("drop %d persisted unexpectedly at seq %d", i+1, ev.Seq)
			}
			return err
		}); err != nil {
			t.Fatalf("drop %d: %v", i+1, err)
		}
	}
	for i := 0; i < checkpointsDuringGap; i++ {
		cp, ok, err := signer.Checkpoint(ctx, st, tenant)
		if err != nil || !ok {
			t.Fatalf("checkpoint during gap: ok=%v err=%v", ok, err)
		}
		if cp.Seq != int64(i+2) || cp.Action != audit.ActionCheckpoint {
			t.Fatalf("checkpoint sealed or moved: %+v", cp)
		}
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: store.ActionAuditArchiveSegment, TargetKind: "core.audit_archive_segment",
			Meta: map[string]any{"archive.fixture": true},
		})
		return err
	}); err != nil {
		t.Fatalf("seal gap: %v", err)
	}
	return realGapFixture{st: st, tenant: tenant, signer: signer, pub: pub}
}

func exportRealGapFixture(t *testing.T, f realGapFixture, segmentEvents int, opts audit.ExportOptions, onSegment func(audit.SegmentResult) error) (string, audit.ExportReport) {
	t.Helper()
	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts.SegmentEvents = segmentEvents
	rep, err := audit.ExportSegments(context.Background(), f.st, f.tenant, sink, opts, onSegment)
	if err != nil {
		t.Fatalf("export gap fixture: %v", err)
	}
	return dir, rep
}

func gapVerifyOptions(t *testing.T, f realGapFixture) audit.ArchiveVerifyOptions {
	t.Helper()
	cpv, err := f.signer.CheckpointVerifier(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return audit.ArchiveVerifyOptions{
		EventKeys: []audit.FencedKey{{Key: f.pub}}, Checkpoints: cpv,
	}
}

func readSegmentManifest(t *testing.T, eventsPath string) audit.SegmentManifest {
	t.Helper()
	b, err := os.ReadFile(eventsPath + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m audit.SegmentManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestArchiveDeclaredGapRoundTripMidSegment(t *testing.T) {
	ctx := context.Background()
	f := newRealGapFixture(t, 3, 0)
	if _, ok, err := f.signer.Checkpoint(ctx, f.st, f.tenant); err != nil || !ok {
		t.Fatalf("post-gap checkpoint: ok=%v err=%v", ok, err)
	}
	dir, rep := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)

	vrep, err := audit.VerifyArchiveDir(ctx, dir, gapVerifyOptions(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK || vrep.DeclaredGaps != 1 || vrep.Checkpoints != 1 {
		t.Fatalf("signed gap verify = %+v", vrep)
	}
	r := vrep.Ranges[f.tenant.String()]
	if r.FromSeq != 1 || r.ToSeq != rep.ToSeq || r.StartsMidChain {
		t.Fatalf("verified range = %+v, export=%+v", r, rep)
	}
	files := segmentFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("segments = %d, want one", len(files))
	}
	m := readSegmentManifest(t, files[0])
	if m.Count == m.ToSeq-m.FromSeq+1 {
		t.Fatalf("gap segment count unexpectedly equals covered span: %+v", m)
	}
	lines := readGapArchiveLines(t, files[0])
	idx := findGapLine(t, lines)
	if idx == 0 || idx == len(lines)-1 {
		t.Fatalf("gap marker is not mid-segment: index=%d lines=%d", idx, len(lines))
	}
}

func TestArchiveDeclaredGapAtSegmentBoundary(t *testing.T) {
	ctx := context.Background()
	f := newRealGapFixture(t, 3, 0)
	dir, rep := exportRealGapFixture(t, f, 1, audit.ExportOptions{}, nil)
	files := segmentFiles(t, dir)
	if len(files) != 3 {
		t.Fatalf("segments = %d, want three physical rows", len(files))
	}
	prev := readSegmentManifest(t, files[0])
	markerManifest := readSegmentManifest(t, files[1])
	markerLines := readGapArchiveLines(t, files[1])
	if len(markerLines) != 1 || markerLines[0].Action != audit.ActionGap || markerLines[0].Seq <= markerManifest.FromSeq {
		t.Fatalf("boundary marker segment = %+v lines=%+v", markerManifest, markerLines)
	}
	if markerManifest.FromSeq != prev.ToSeq+1 || markerManifest.PrevSegmentLastHash != prev.LastHash {
		t.Fatalf("range-covered boundary did not chain: prev=%+v marker=%+v", prev, markerManifest)
	}
	vrep, err := audit.VerifyArchiveDir(ctx, dir, gapVerifyOptions(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK || vrep.DeclaredGaps != 1 {
		t.Fatalf("boundary gap verify = %+v", vrep)
	}
	if r := vrep.Ranges[f.tenant.String()]; r.FromSeq != 1 || r.ToSeq != rep.ToSeq {
		t.Fatalf("boundary range = %+v, export=%+v", r, rep)
	}
}

func TestArchiveDeclaredGapPinnedRebuildDeterministic(t *testing.T) {
	f := newRealGapFixture(t, 3, 0)
	var firstManifests []audit.SegmentManifest
	dir1, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{
		BeforePut: func(m audit.SegmentManifest) error {
			firstManifests = append(firstManifests, m)
			return nil
		},
	}, nil)
	if len(firstManifests) != 1 {
		t.Fatalf("recorded manifests = %d, want one", len(firstManifests))
	}
	dir2, _ := exportRealGapFixture(t, f, 1, audit.ExportOptions{
		FromSeq: firstManifests[0].FromSeq, PendingToSeq: firstManifests[0].ToSeq,
	}, nil)
	files1, files2 := segmentFiles(t, dir1), segmentFiles(t, dir2)
	if len(files1) != 1 || len(files2) != 1 {
		t.Fatalf("rebuilt segment counts = %d/%d", len(files1), len(files2))
	}
	for _, suffix := range []string{"", ".manifest.json"} {
		b1, err := os.ReadFile(files1[0] + suffix)
		if err != nil {
			t.Fatal(err)
		}
		b2, err := os.ReadFile(files2[0] + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("pinned rebuild differs for %q", suffix)
		}
	}
}

func TestArchiveDeclaredGapHonorsHeadSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newRealGapFixture(t, 3, 3)
	var snapshot store.HeadRef
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		var ok bool
		var err error
		snapshot, ok, err = sc.Audit().Head(ctx)
		if err == nil && !ok {
			t.Fatal("missing fixture head")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	appended := false
	dir, rep := exportRealGapFixture(t, f, 4, audit.ExportOptions{}, func(audit.SegmentResult) error {
		if appended {
			return nil
		}
		appended = true
		// Multiple exempt anchors make enough physical rows that a span-as-count
		// cap would tail-chase beyond the already captured snapshot after the hole.
		return f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
			for i := 0; i < 10; i++ {
				if _, err := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: model.ActorSystem, ActorKind: model.ActorSystem,
					Action: store.ActionAuditArchiveSegment,
					Meta:   map[string]any{"after_snapshot": i},
				}); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if rep.ToSeq != snapshot.Seq {
		t.Fatalf("export escaped snapshot: report=%+v snapshot=%+v", rep, snapshot)
	}
	for _, path := range segmentFiles(t, dir) {
		for _, line := range readGapArchiveLines(t, path) {
			if line.Seq > snapshot.Seq {
				t.Fatalf("exported seq %d past snapshot %d", line.Seq, snapshot.Seq)
			}
		}
	}
	vrep, err := audit.VerifyArchiveDir(ctx, dir, gapVerifyOptions(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK || vrep.Ranges[f.tenant.String()].ToSeq != snapshot.Seq {
		t.Fatalf("snapshot-bounded archive verify = %+v", vrep)
	}
}

func TestArchiveDeclaredGapAdversarial(t *testing.T) {
	ctx := context.Background()

	t.Run("undeclared hole", func(t *testing.T) {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := audit.NewSigner(priv)
		if err != nil {
			t.Fatal(err)
		}
		st := signedStore(t, signer)
		tenant := provisionTenant(t, st)
		appendMetaEvents(t, st, tenant, 4, "undeclared")
		dir := t.TempDir()
		sink, err := audit.NewDirSink(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{SegmentEvents: 100}, nil); err != nil {
			t.Fatal(err)
		}
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		lines = append(lines[:1], lines[2:]...) // remove seq 2, retain a canonical signed suffix
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: pub}}}, "seq-gap")
	})

	t.Run("wrong declared range", func(t *testing.T) {
		f := newRealGapFixture(t, 3, 0)
		dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		idx := findGapLine(t, lines)
		meta := decodeCanonicalMeta(t, lines[idx].Meta)
		meta[store.GapMetaFromSeq] = lines[idx].Seq - 1
		meta[store.GapMetaToSeq] = lines[idx].Seq - 1
		meta[store.GapMetaCount] = int64(1)
		lines[idx].Meta = canonicalMeta(t, meta)
		// Simulate a DB-only attacker recomputing the complete structural suffix;
		// stale signatures cannot mask the earlier semantic mismatch.
		rehashGapArchiveLines(t, f.tenant.String(), lines, idx, nil)
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, gapVerifyOptions(t, f), "gap-mismatch")
	})

	t.Run("unsigned marker", func(t *testing.T) {
		f := newRealGapFixture(t, 3, 0)
		dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		lines[findGapLine(t, lines)].Sig = ""
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, gapVerifyOptions(t, f), "event-sig-missing")
	})

	t.Run("marker signed by wrong key", func(t *testing.T) {
		f := newRealGapFixture(t, 3, 0)
		dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		idx := findGapLine(t, lines)
		_, wrongPriv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		wrong, err := audit.NewSigner(wrongPriv)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := hex.DecodeString(lines[idx].Hash)
		if err != nil {
			t.Fatal(err)
		}
		lines[idx].Sig = base64.StdEncoding.EncodeToString(wrong.SignEvent(f.tenant.String(), lines[idx].Seq, hash))
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, gapVerifyOptions(t, f), "event-sig-invalid")
	})

	t.Run("marker meta altered without rehash", func(t *testing.T) {
		f := newRealGapFixture(t, 3, 0)
		dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		idx := findGapLine(t, lines)
		meta := decodeCanonicalMeta(t, lines[idx].Meta)
		meta[store.GapMetaReason] = "tampered"
		lines[idx].Meta = canonicalMeta(t, meta)
		// The outer line remains canonical and the declaration remains exact, so
		// canon re-derivation is the first check that can detect this mutation.
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, gapVerifyOptions(t, f), "hash-mismatch")
	})

	t.Run("in-place marker", func(t *testing.T) {
		f := newRealGapFixture(t, 3, 0)
		dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
		path := segmentFiles(t, dir)[0]
		lines := readGapArchiveLines(t, path)
		idx := findGapLine(t, lines)
		lines[idx].Seq = lines[idx-1].Seq + 1
		meta := decodeCanonicalMeta(t, lines[idx].Meta)
		meta[store.GapMetaFromSeq] = lines[idx].Seq
		meta[store.GapMetaToSeq] = lines[idx].Seq
		meta[store.GapMetaCount] = int64(1)
		lines[idx].Meta = canonicalMeta(t, meta)
		// Re-hash, re-link and legitimately re-sign the full suffix: even a fully
		// consistent marker is invalid when it does not actually cross a hole.
		rehashGapArchiveLines(t, f.tenant.String(), lines, idx, f.signer)
		rewriteGapArchiveSegment(t, path, lines)
		requireArchiveReason(t, dir, gapVerifyOptions(t, f), "gap-mismatch")
	})
}

func TestArchiveCheckpointInsidePendingGapEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newRealGapFixture(t, 2, 1)
	var physical []model.AuditEvent
	var live store.VerifyReport
	var checkpoints audit.CheckpointReport
	var events audit.EventSigReport
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			physical = append(physical, ev)
			return nil
		}); err != nil {
			return err
		}
		var err error
		if live, err = sc.Audit().Verify(ctx, 1); err != nil {
			return err
		}
		if checkpoints, err = audit.VerifyCheckpoints(ctx, sc.Audit(), f.pub); err != nil {
			return err
		}
		events, err = audit.VerifyEvents(ctx, sc.Audit(), f.pub)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(physical) != 4 || physical[0].Seq != 1 || physical[1].Seq != 2 ||
		physical[1].Action != audit.ActionCheckpoint || physical[2].Seq != 5 ||
		physical[2].Action != audit.ActionGap || physical[3].Seq != 6 ||
		!bytes.Equal(physical[1].PrevHash, physical[0].Hash) || !bytes.Equal(physical[2].PrevHash, physical[1].Hash) {
		t.Fatalf("checkpoint/gap chain shape = %+v", physical)
	}
	if !live.OK || live.DeclaredGaps != 1 || !checkpoints.OK || checkpoints.Checkpoints != 1 || !events.OK {
		t.Fatalf("live verification: chain=%+v checkpoints=%+v events=%+v", live, checkpoints, events)
	}

	dir, _ := exportRealGapFixture(t, f, 100, audit.ExportOptions{}, nil)
	vrep, err := audit.VerifyArchiveDir(ctx, dir, gapVerifyOptions(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !vrep.OK || vrep.DeclaredGaps != 1 || vrep.Checkpoints != 1 {
		t.Fatalf("archive checkpoint/gap verify = %+v", vrep)
	}
}

func TestStoreDeclaresGapExactCanonicalContract(t *testing.T) {
	valid := `{"gap.count":3,"gap.from_seq":2,"gap.to_seq":4}`
	if !store.DeclaresGap(valid, 2, 5) {
		t.Fatal("exact gap declaration rejected")
	}
	for _, invalid := range []string{
		`{"gap.count":2,"gap.from_seq":2,"gap.to_seq":4}`,
		`{"gap.count":3.0,"gap.from_seq":2,"gap.to_seq":4}`,
		`{"gap.count":3,"gap.from_seq":2,"gap.to_seq":4} {}`,
		`{"gap.count":3,"gap.from_seq":2,"gap.to_seq":4}`,
	} {
		expected, marker := int64(2), int64(5)
		if invalid == valid {
			marker = expected // markerSeq must be strictly greater than expectedSeq.
		}
		if store.DeclaresGap(invalid, expected, marker) {
			t.Fatalf("invalid declaration accepted: %s", invalid)
		}
	}
}

func TestBuildSegmentRejectsUndeclaredHole(t *testing.T) {
	tenant := model.NewTenantID()
	meta := "{}"
	first := model.AuditEvent{TenantID: tenant, Seq: 1, PrevHash: canon.ZeroHash()}
	sealArchiveFixture(t, &first, meta)
	third := model.AuditEvent{TenantID: tenant, Seq: 3, PrevHash: first.Hash}
	sealArchiveFixture(t, &third, meta)
	log := canonicalArchiveLog{
		walkLog: walkLog{events: []model.AuditEvent{first, third}},
		metas:   []string{meta, meta},
	}
	_, _, err := audit.BuildSegment(context.Background(), log, tenant, 1, 10, 0)
	if err == nil || err.Error() != "audit: archive export: seq-gap at 3 (want 2)" {
		t.Fatalf("undeclared export error = %v", err)
	}
}

type canonicalArchiveLog struct {
	walkLog
	metas []string
}

func (l canonicalArchiveLog) WalkCanonical(_ context.Context, fromSeq int64, fn func(model.AuditEvent, string, []byte) error) error {
	for i, ev := range l.events {
		if ev.Seq < fromSeq {
			continue
		}
		// nil blind: these fixtures stand for records sealed under the unblinded
		// rule, which is what archiveFixtureHash commits to.
		if err := fn(ev, l.metas[i], nil); err != nil {
			return err
		}
	}
	return nil
}

// sealArchiveFixture fills a hand-built fixture's metadata commitment and chain
// hash exactly as the store would, so it is a SEALED event rather than a
// half-populated struct. Setting the hash without the commitment used to be
// harmless; it no longer is, because the export refuses an event whose commitment
// is missing — and a fixture that skipped it would fail with an export error
// instead of exercising the case under test.
func sealArchiveFixture(t *testing.T, ev *model.AuditEvent, meta string) {
	ev.MetaCommitment = canon.MetaDigest(meta) // no blind: the unblinded rule
	ev.Hash = mustArchiveEventHash(t, canon.Event{
		TenantID: ev.TenantID.String(), Seq: ev.Seq, OccurredAt: ev.OccurredAt.String(),
		Actor: ev.Actor, ActorKind: ev.ActorKind, Action: ev.Action,
		TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(),
		MetaCommitment: ev.MetaCommitment, PayloadHash: ev.PayloadHash, PrevHash: ev.PrevHash,
	})
}

type gapArchiveLine struct {
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Meta       string `json:"meta"`
	// Without this the round trip below would DROP the blind and the verifier would
	// fall back to the unblinded rule, reporting a hash mismatch that has nothing to
	// do with the tampering each subtest is actually exercising.
	MetaBlind   string `json:"meta_blind,omitempty"`
	PayloadHash string `json:"payload_hash"`
	PrevHash    string `json:"prev_hash"`
	Hash        string `json:"hash"`
	Sig         string `json:"sig"`
}

func readGapArchiveLines(t *testing.T, path string) []gapArchiveLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rawLines := strings.Split(strings.TrimSpace(string(b)), "\n")
	lines := make([]gapArchiveLine, 0, len(rawLines))
	for _, raw := range rawLines {
		if raw == "" {
			continue
		}
		var line gapArchiveLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return lines
}

func findGapLine(t *testing.T, lines []gapArchiveLine) int {
	t.Helper()
	for i := range lines {
		if lines[i].Action == audit.ActionGap {
			return i
		}
	}
	t.Fatal("archive has no audit.gap marker")
	return -1
}

func decodeCanonicalMeta(t *testing.T, canonical string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(canonical))
	dec.UseNumber()
	var meta map[string]any
	if err := dec.Decode(&meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func canonicalMeta(t *testing.T, meta map[string]any) string {
	t.Helper()
	canonical, err := canon.CanonicalMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

// rehashGapArchiveLines keeps the entire physical suffix hash-continuous. When
// signer is non-nil, all non-checkpoint rows are also re-signed under the real
// event domain; nil models a DB-only attacker without the private key.
func rehashGapArchiveLines(t *testing.T, tenant string, lines []gapArchiveLine, from int, signer *audit.Signer) {
	t.Helper()
	for i := from; i < len(lines); i++ {
		if i > from {
			lines[i].PrevHash = lines[i-1].Hash
		}
		prev, err := hex.DecodeString(lines[i].PrevHash)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := hex.DecodeString(lines[i].PayloadHash)
		if err != nil {
			t.Fatal(err)
		}
		hash := mustArchiveEventHash(t, canon.Event{
			TenantID: tenant, Seq: lines[i].Seq, OccurredAt: lines[i].OccurredAt,
			Actor: lines[i].Actor, ActorKind: lines[i].ActorKind, Action: lines[i].Action,
			TargetKind: lines[i].TargetKind, TargetID: lines[i].TargetID,
			// Resolve from the line's OWN blind, exactly as the offline verifier does.
			// Rehashing everything under the unblinded rule would leave every blinded
			// line hash-broken, and the adversarial subtests built on this helper would
			// pass only because gap semantics are checked before the hash.
			MetaCommitment: mustCommitment(t, mustHex(t, lines[i].MetaBlind), lines[i].Meta),
			PayloadHash:    payload, PrevHash: prev,
		})
		lines[i].Hash = hex.EncodeToString(hash)
		if signer != nil && lines[i].Action != audit.ActionCheckpoint {
			lines[i].Sig = base64.StdEncoding.EncodeToString(signer.SignEvent(tenant, lines[i].Seq, hash))
		}
	}
}

func rewriteGapArchiveSegment(t *testing.T, eventsPath string, lines []gapArchiveLine) {
	t.Helper()
	var body []byte
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, b...)
		body = append(body, '\n')
	}
	m := readSegmentManifest(t, eventsPath)
	m.Count = int64(len(lines))
	if len(lines) > 0 {
		m.FirstHash = lines[0].Hash
		m.LastHash = lines[len(lines)-1].Hash
	}
	sum := sha256.Sum256(body)
	m.EventsSHA256 = hex.EncodeToString(sum[:])
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	rewrite(t, eventsPath, body)
	rewrite(t, eventsPath+".manifest.json", mb)
}

func requireArchiveReason(t *testing.T, dir string, opts audit.ArchiveVerifyOptions, want string) {
	t.Helper()
	rep, err := audit.VerifyArchiveDir(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != want {
		t.Fatalf("archive verify = %+v, want %s", rep, want)
	}
}

func TestBuildSegmentRejectsInPlaceMarker(t *testing.T) {
	// An audit.gap row with NO hole before it is a shape both the live and the
	// archive verifiers reject as gap-mismatch; export must refuse to seal it
	// into WORM rather than mint an archive that can never verify.
	tenant := model.NewTenantID()
	meta := "{}"
	first := model.AuditEvent{TenantID: tenant, Seq: 1, PrevHash: canon.ZeroHash()}
	sealArchiveFixture(t, &first, meta)
	inPlace := model.AuditEvent{TenantID: tenant, Seq: 2, Action: audit.ActionGap, PrevHash: first.Hash}
	sealArchiveFixture(t, &inPlace, meta)
	log := canonicalArchiveLog{
		walkLog: walkLog{events: []model.AuditEvent{first, inPlace}},
		metas:   []string{meta, meta},
	}
	_, _, err := audit.BuildSegment(context.Background(), log, tenant, 1, 10, 0)
	if err == nil || err.Error() != "audit: archive export: gap marker without a hole at seq 2" {
		t.Fatalf("in-place marker export error = %v", err)
	}
}

// mustHex decodes a hex field from an archive line, failing the test rather than
// silently yielding nil — a nil blind would select the legacy rule and quietly
// change which hash rule the fixture is exercising.
func mustHex(t *testing.T, v string) []byte {
	t.Helper()
	b, err := hex.DecodeString(v)
	if err != nil {
		t.Fatalf("decode hex %q: %v", v, err)
	}
	return b
}

// mustArchiveEventHash hashes a preimage the test expects to be well-formed.
func mustArchiveEventHash(t *testing.T, e canon.Event) []byte {
	t.Helper()
	h, err := canon.EventHash(e)
	if err != nil {
		t.Fatalf("EventHash on a well-formed preimage: %v", err)
	}
	return h
}

// mustCommitment resolves a record's commitment from its own blind, failing the
// test on the illegal states rather than papering over them with a fallback rule.
func mustCommitment(t *testing.T, blind []byte, meta string) []byte {
	t.Helper()
	c, err := canon.MetaCommitmentFor(blind, meta)
	if err != nil {
		t.Fatalf("resolve commitment: %v", err)
	}
	return c
}
