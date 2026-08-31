// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/s3archive"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// auditarchive_test.go pins the loop: the environment contract, the
// deny-closed sink selection, the s3archive→ArchiveSink field mapping, and an
// end-to-end drain (export → anchor → resume bookkeeping → offline verify)
// against a real, signed demo chain.

func TestLoadAuditArchiveConfigDefaultsAndParsing(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	log := discardLog()

	cfg := loadAuditArchiveConfig(getenv, log)
	if cfg.sink != "" || cfg.interval != defaultAuditArchiveInterval ||
		cfg.segmentEvents != audit.DefaultSegmentEvents || cfg.retainDays != defaultAuditArchiveRetainDays {
		t.Fatalf("defaults = %+v", cfg)
	}

	env[auditArchiveSinkEnv] = "s3archive"
	env[auditArchiveConfigEnv] = "/etc/olivares/archive.json"
	env[auditArchiveIntervalEnv] = "6h"
	env[auditArchiveSegmentEventsEnv] = "500"
	env[auditArchiveRetainDaysEnv] = "3650"
	cfg = loadAuditArchiveConfig(getenv, log)
	if cfg.sink != "s3archive" || cfg.configPath != "/etc/olivares/archive.json" ||
		cfg.interval != 6*time.Hour || cfg.segmentEvents != 500 || cfg.retainDays != 3650 {
		t.Fatalf("explicit env not honored: %+v", cfg)
	}

	// retain_days=0 is a legitimate explicit choice: defer to the bucket's
	// default Object Lock retention (zero RetainUntil per Put).
	env[auditArchiveRetainDaysEnv] = "0"
	if cfg = loadAuditArchiveConfig(getenv, log); cfg.retainDays != 0 {
		t.Fatalf("explicit retain_days=0 must be honored, got %d", cfg.retainDays)
	}

	// Typos keep the defaults (a typo must not silently change retention).
	env[auditArchiveIntervalEnv] = "soon"
	env[auditArchiveSegmentEventsEnv] = "-3"
	env[auditArchiveRetainDaysEnv] = "99999"
	cfg = loadAuditArchiveConfig(getenv, log)
	if cfg.interval != defaultAuditArchiveInterval || cfg.segmentEvents != audit.DefaultSegmentEvents || cfg.retainDays != defaultAuditArchiveRetainDays {
		t.Fatalf("invalid env must keep defaults: %+v", cfg)
	}
}

func TestNewAuditArchiveLoopSinkSelectionDenyClosed(t *testing.T) {
	log := discardLog()
	base := auditArchiveConfig{interval: time.Hour, segmentEvents: 10, retainDays: 1}

	// No sink (the shipped default): archival is OFF.
	if l, err := newAuditArchiveLoop(base, nil, nil, nil, log); err != nil || l != nil {
		t.Fatal("no sink must yield no loop")
	}
	// Unknown sink kind is an operator typo: refuse startup rather than silently
	// disabling continuous archival.
	cfg := base
	cfg.sink = "tape"
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("unknown sink must fail startup")
	}
	// A selected dir sink without a usable directory is likewise invalid config.
	cfg = base
	cfg.sink = "dir"
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("dir sink without OLIVARES_AUDIT_ARCHIVE_DIR must fail startup")
	}
	unusableRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(unusableRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.dir = filepath.Join(unusableRoot, "archive")
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("uncreatable dir sink must fail startup")
	}
	cfg.dir = t.TempDir()
	l, err := newAuditArchiveLoop(cfg, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("build directory loop: %v", err)
	}
	if l == nil {
		t.Fatal("dir sink with a directory must build")
	}
	if _, ok := l.sink.(*audit.DirSink); !ok {
		t.Fatalf("sink = %T, want *audit.DirSink", l.sink)
	}

	// s3archive without its config file: OFF.
	cfg = base
	cfg.sink = "s3archive"
	if l, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err != nil || l != nil {
		t.Fatal("s3archive without OLIVARES_AUDIT_ARCHIVE_CONFIG must yield no loop")
	}
	// Unreadable / invalid / connector-rejected configs fail startup closed.
	cfg.configPath = filepath.Join(t.TempDir(), "missing.json")
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("missing config file must return an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.configPath = bad
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("invalid JSON config must return an error")
	}
	incomplete := filepath.Join(t.TempDir(), "incomplete.json")
	if err := os.WriteFile(incomplete, []byte(`{"region":"eu-west-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.configPath = incomplete
	if _, err := newAuditArchiveLoop(cfg, nil, nil, nil, log); err == nil {
		t.Fatal("a config the connector's Open rejects must return an error")
	}
	// A complete connector config builds the loop (Open validates shape only —
	// no network is touched until the first Put).
	good := filepath.Join(t.TempDir(), "good.json")
	if err := os.WriteFile(good, []byte(`{"region":"eu-west-1","bucket":"ledger-archive","access_key_id":"AKIDEXAMPLE","secret_access_key":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.configPath = good
	l, err = newAuditArchiveLoop(cfg, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("build s3archive loop: %v", err)
	}
	if l == nil {
		t.Fatal("a valid s3archive config must build the loop")
	}
	if _, ok := l.sink.(s3ArchiveSinkAdapter); !ok {
		t.Fatalf("sink = %T, want s3ArchiveSinkAdapter", l.sink)
	}
}

// --- the s3archive → ArchiveSink adapter ---------------------------------------

// fakeS3Putter records the connector-side options and returns a canned receipt.
type fakeS3Putter struct {
	key  string
	body []byte
	opts s3archive.PutOptions
	rec  s3archive.Receipt
	err  error
}

func (f *fakeS3Putter) Put(_ context.Context, key string, body []byte, opts s3archive.PutOptions) (s3archive.Receipt, error) {
	f.key, f.body, f.opts = key, body, opts
	return f.rec, f.err
}

func TestS3ArchiveSinkAdapterMapsFields(t *testing.T) {
	retain := time.Date(2033, 6, 10, 0, 0, 0, 0, time.UTC)
	fake := &fakeS3Putter{rec: s3archive.Receipt{
		Bucket: "ledger-archive", Key: "t1/seg-000000000001-000000000010.jsonl",
		ETag: "etag-1", VersionID: "v-9", LockMode: "COMPLIANCE",
		RetainUntil: retain, LockVerified: true,
	}}
	sink := s3ArchiveSinkAdapter{out: fake}

	got, err := sink.Put(context.Background(), "t1/seg-000000000001-000000000010.jsonl", []byte("x"),
		audit.ArchivePutOptions{ContentSHA256: "abc123", RetainUntil: retain, LegalHold: true})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// Options pass through field-for-field.
	if fake.opts.ContentSHA256 != "abc123" || !fake.opts.RetainUntil.Equal(retain) || !fake.opts.LegalHold {
		t.Fatalf("connector options = %+v, want the caller's", fake.opts)
	}
	// The receipt maps field-for-field, Location derived bucket/key (the
	// connector Receipt carries no Location).
	if got.Location != "ledger-archive/t1/seg-000000000001-000000000010.jsonl" {
		t.Fatalf("location = %q", got.Location)
	}
	if got.ETag != "etag-1" || got.VersionID != "v-9" || got.LockMode != "COMPLIANCE" ||
		!got.RetainUntil.Equal(retain) || !got.LockVerified {
		t.Fatalf("receipt = %+v", got)
	}

	// Errors propagate; no fabricated receipt.
	fake.err = errors.New("lock not confirmed")
	if _, err := sink.Put(context.Background(), "k", nil, audit.ArchivePutOptions{}); err == nil {
		t.Fatal("a connector error must propagate")
	}
}

func TestParseArchiveLastSeq(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		// Absent is the ONLY state that means "start at seq 1".
		{nil, 0, true},
		{"42", 42, true}, {float64(7), 7, true}, {int64(9), 9, true}, {3, 3, true},
		// Present-but-corrupt must be ok=false (loud skip), NEVER a reset to 0:
		// a silent restart would mass re-export with boundaries that no longer
		// match the already-sealed WORM objects.
		{"", 0, false}, {"-5", 0, false}, {"garbage", 0, false}, {true, 0, false},
		{float64(-1), 0, false}, {float64(7.5), 0, false},
	}
	for _, c := range cases {
		got, ok := parseArchiveLastSeq(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("parseArchiveLastSeq(%v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseArchivePending(t *testing.T) {
	cases := []struct {
		in       any
		from, to int64
		ok       bool
	}{
		{"21-24", 21, 24, true}, {"1-1", 1, 1, true},
		// Malformed values must be ok=false: a guessed boundary could overlap
		// objects the previous run already sealed.
		{"", 0, 0, false}, {"21", 0, 0, false}, {"-21-24", 0, 0, false},
		{"24-21", 0, 0, false}, {"0-4", 0, 0, false}, {"a-b", 0, 0, false},
		{nil, 0, 0, false}, {21, 0, 0, false},
	}
	for _, c := range cases {
		from, to, ok := parseArchivePending(c.in)
		if from != c.from || to != c.to || ok != c.ok {
			t.Fatalf("parseArchivePending(%v) = (%d, %d, %v), want (%d, %d, %v)", c.in, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

// --- end to end: drain → anchor → resume → offline verify ----------------------

func TestAuditArchiveLoopDrainsAnchorsAndResumes(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tenant := eng.demoTenant
	if tenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}

	outDir := filepath.Join(t.TempDir(), "archive")
	sink, err := audit.NewDirSink(outDir)
	if err != nil {
		t.Fatalf("dir sink: %v", err)
	}
	loop := &auditArchiveLoop{
		st: eng.store, sink: sink, signer: eng.signer, priors: eng.auditPriors,
		interval: time.Hour, segmentEvents: 25, retainDays: 0,
		clock: time.Now, log: discardLog(),
	}

	// lastSeqOf reads the resume point after a clean drain: no pending boundary
	// may be left behind (it is cleared in the same tx as the anchor+advance).
	lastSeqOf := func(tn model.TenantID) int64 {
		t.Helper()
		bk, err := loop.bookkeeping(ctx, tn)
		if err != nil {
			t.Fatalf("bookkeeping(%s): %v", tn, err)
		}
		if bk.hasPending {
			t.Fatalf("pending boundary left behind after a clean drain: %+v", bk)
		}
		return bk.lastSeq
	}

	if err := loop.runOnce(ctx); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	// The advisory keys.json is written once per process.
	if _, err := os.Stat(filepath.Join(outDir, audit.ArchiveKeysName)); err != nil {
		t.Fatalf("keys.json not written: %v", err)
	}
	// The resume point advanced and is persisted in Org.Settings as a string.
	last1 := lastSeqOf(tenant)
	if last1 == 0 {
		t.Fatalf("last_seq after the first tick = %d, want >0", last1)
	}
	// Each durably-written segment was anchored INSIDE the chain it archives.
	if n := countAnchorEvents(t, eng.store, tenant); n == 0 {
		t.Fatal("no audit.archive.segment anchor event found in the chain")
	}
	// The system tenant is covered too (the CheckpointAll coverage).
	if lastSys := lastSeqOf(model.SystemTenantID); lastSys == 0 {
		t.Fatalf("system-tenant last_seq = %d, want >0", lastSys)
	}

	// The second tick drains what the first appended (its own anchors) and
	// advances the resume point — the loop converges instead of chasing its tail
	// within a tick (the export's head snapshot).
	if err := loop.runOnce(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	last2 := lastSeqOf(tenant)
	if last2 <= last1 {
		t.Fatalf("last_seq after the second tick = %d, want > %d", last2, last1)
	}

	// A stale anchorAndAdvance is a FULL no-op: the resume point never
	// regresses (no mass re-export) and no duplicate anchor event is appended.
	anchorsBefore := countAnchorEvents(t, eng.store, tenant)
	if err := loop.anchorAndAdvance(ctx, tenant, audit.SegmentResult{Manifest: audit.SegmentManifest{FromSeq: 1, ToSeq: 1}}); err != nil {
		t.Fatalf("stale anchorAndAdvance: %v", err)
	}
	if got := lastSeqOf(tenant); got != last2 {
		t.Fatalf("resume point regressed to %d, want %d", got, last2)
	}
	if got := countAnchorEvents(t, eng.store, tenant); got != anchorsBefore {
		t.Fatalf("stale call appended an anchor: %d, want %d", got, anchorsBefore)
	}

	// The multi-tick, multi-tenant archive verifies offline: contiguity,
	// per-segment hashes, canon re-derivation AND the per-event signatures
	// against the engine's key.
	rep, err := audit.VerifyArchiveDir(ctx, outDir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: eng.signer.PublicKey()}}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("archive verify failed: %+v", rep)
	}
}

// countAnchorEvents counts audit.archive.segment events in a tenant's chain.
func countAnchorEvents(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	n := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == audit.ActionArchiveSegment {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return n
}

// headSeq reads a tenant's current chain head sequence.
func headSeq(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if ok {
			seq = head.Seq
		}
		return err
	}); err != nil {
		t.Fatalf("head: %v", err)
	}
	return seq
}

// appendChainEvents grows a tenant's chain by n events (the head moves).
func appendChainEvents(t *testing.T, st store.Store, tenant model.TenantID, n int) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.update", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// setOrgSettingKey writes one Org.Settings key read-modify-write (sibling keys
// ride along), simulating hand-edited/corrupt bookkeeping.
func setOrgSettingKey(t *testing.T, st store.Store, tenant model.TenantID, key string, value any) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		org, err := sc.Org(context.Background())
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[key] = value
		_, err = sc.SetOrgSettings(context.Background(), settings)
		return err
	}); err != nil {
		t.Fatalf("set org setting: %v", err)
	}
}

// failOnceSink fails the first Put whose key has suffix, then delegates — the
// §8.5 crash window between a segment's two object writes.
type failOnceSink struct {
	inner  audit.ArchiveSink
	suffix string
	failed bool
}

func (f *failOnceSink) Put(ctx context.Context, key string, body []byte, opts audit.ArchivePutOptions) (audit.ArchiveReceipt, error) {
	if !f.failed && strings.HasSuffix(key, f.suffix) {
		f.failed = true
		return audit.ArchiveReceipt{}, errors.New("simulated sink outage")
	}
	return f.inner.Put(ctx, key, body, opts)
}

// TestAuditArchiveLoopReusesPendingBoundaryAfterCrash reproduces the boundary-
// shift bug end to end: tick 1 writes the tail segment's events object and then
// dies before the manifest lands (the pending boundary is already persisted);
// the head moves before tick 2. Before the fix, tick 2 recomputed the tail
// boundary from the moved head — a byte-different, overlapping segment next to
// the sealed events object, a permanent verify failure on a WORM sink. With
// the protocol, tick 2 reuses the pending boundary verbatim: the events re-put
// is byte-identical (absorbed), the manifest lands, and the drain advances.
func TestAuditArchiveLoopReusesPendingBoundaryAfterCrash(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tenant := eng.demoTenant

	outDir := filepath.Join(t.TempDir(), "archive")
	sink, err := audit.NewDirSink(outDir)
	if err != nil {
		t.Fatalf("dir sink: %v", err)
	}
	// One huge segment per tenant: the demo tenant's first manifest Put is the
	// first manifest Put of the run, so the failure hits its tail segment.
	loop := &auditArchiveLoop{
		st: eng.store, sink: &failOnceSink{inner: sink, suffix: ".manifest.json"},
		signer: eng.signer, priors: eng.auditPriors,
		interval: time.Hour, segmentEvents: 1_000_000, retainDays: 0,
		clock: time.Now, log: discardLog(),
	}

	h1 := headSeq(t, eng.store, tenant)
	if err := loop.runOnce(ctx); err != nil {
		t.Fatalf("first tick: %v", err) // per-tenant failures are logged, not returned
	}
	bk, err := loop.bookkeeping(ctx, tenant)
	if err != nil {
		t.Fatalf("bookkeeping: %v", err)
	}
	// The crash window: events object sealed, manifest missing, nothing
	// anchored or advanced — and the boundary on record.
	if bk.lastSeq != 0 || !bk.hasPending || bk.pendingFrom != 1 || bk.pendingTo != h1 {
		t.Fatalf("bookkeeping after the crash tick = %+v, want last=0 pending=1-%d", bk, h1)
	}
	if n := countAnchorEvents(t, eng.store, tenant); n != 0 {
		t.Fatalf("anchors after the crash tick = %d, want 0 (anchor and advance are one tx)", n)
	}

	// The head moves before the retry — the exact condition that used to shift
	// the recomputed boundary.
	appendChainEvents(t, eng.store, tenant, 7)
	h2 := headSeq(t, eng.store, tenant)

	if err := loop.runOnce(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	bk, err = loop.bookkeeping(ctx, tenant)
	if err != nil {
		t.Fatalf("bookkeeping: %v", err)
	}
	if bk.hasPending || bk.lastSeq != h2 {
		t.Fatalf("bookkeeping after the retry tick = %+v, want last=%d and no pending", bk, h2)
	}

	// Exactly the two intended segments — the reused boundary plus the
	// remainder — and the whole archive verifies (no orphaned overlap).
	for _, want := range []string{
		audit.SegmentKey(tenant.String(), 1, h1),
		audit.SegmentKey(tenant.String(), h1+1, h2),
	} {
		if _, err := os.Stat(filepath.Join(outDir, want)); err != nil {
			t.Fatalf("expected segment %s: %v", want, err)
		}
	}
	rep, err := audit.VerifyArchiveDir(ctx, outDir, audit.ArchiveVerifyOptions{EventKeys: []audit.FencedKey{{Key: eng.signer.PublicKey()}}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("post-retry verify = %+v", rep)
	}
	if r := rep.Ranges[tenant.String()]; r.FromSeq != 1 || r.ToSeq != h2 {
		t.Fatalf("attested range = %+v, want 1-%d", r, h2)
	}
}

// TestAuditArchiveLoopCorruptBookkeepingSkipsLoudly pins the deny-closed
// posture for evidence bookkeeping: a present-but-corrupt last_seq or pending
// value pauses THAT tenant's drain with an error carrying the recovery
// instruction — never a silent reset to 0, which would mass re-export the
// chain with boundaries that no longer match the already-sealed WORM objects.
func TestAuditArchiveLoopCorruptBookkeepingSkipsLoudly(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tenant := eng.demoTenant

	outDir := filepath.Join(t.TempDir(), "archive")
	sink, err := audit.NewDirSink(outDir)
	if err != nil {
		t.Fatalf("dir sink: %v", err)
	}
	loop := &auditArchiveLoop{
		st: eng.store, sink: sink, signer: eng.signer, priors: eng.auditPriors,
		interval: time.Hour, segmentEvents: 25, retainDays: 0,
		clock: time.Now, log: discardLog(),
	}

	cases := []struct {
		name     string
		key      string
		value    any
		fragment string
	}{
		{"corrupt-last-seq", archiveLastSeqSettingsKey, "garbage", "corrupt " + archiveLastSeqSettingsKey},
		{"corrupt-pending", archivePendingSettingsKey, "24-21", "corrupt " + archivePendingSettingsKey},
		{"mismatched-pending", archivePendingSettingsKey, "9-12", "does not resume last_seq"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setOrgSettingKey(t, eng.store, tenant, c.key, c.value)
			defer func() {
				// Restore a clean slate for the next case: absent keys.
				if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
					org, oerr := sc.Org(ctx)
					if oerr != nil {
						return oerr
					}
					delete(org.Settings, archiveLastSeqSettingsKey)
					delete(org.Settings, archivePendingSettingsKey)
					_, serr := sc.SetOrgSettings(ctx, org.Settings)
					return serr
				}); err != nil {
					t.Fatalf("reset settings: %v", err)
				}
			}()

			derr := loop.drainTenant(ctx, tenant)
			if derr == nil {
				t.Fatal("corrupt bookkeeping must pause the drain with an error")
			}
			if !strings.Contains(derr.Error(), c.fragment) || !strings.Contains(derr.Error(), "audit.archive.last_seq") {
				t.Fatalf("error must name the corruption and the recovery instruction, got: %v", derr)
			}
			// Nothing was exported for the paused tenant (no silent restart at 0)
			// and the corrupt value was left in place for the operator to inspect.
			if _, serr := os.Stat(filepath.Join(outDir, tenant.String())); !os.IsNotExist(serr) {
				t.Fatalf("paused tenant must export nothing, stat err = %v", serr)
			}
			if verr := eng.store.View(ctx, tenant, func(sc store.Scope) error {
				org, oerr := sc.Org(ctx)
				if oerr != nil {
					return oerr
				}
				if got := org.Settings[c.key]; got != c.value {
					t.Fatalf("bookkeeping value rewritten to %v, want %v left in place", got, c.value)
				}
				return nil
			}); verr != nil {
				t.Fatalf("view: %v", verr)
			}
		})
	}

	// A whole tick still drains the HEALTHY tenants past the corrupt one.
	setOrgSettingKey(t, eng.store, tenant, archiveLastSeqSettingsKey, "garbage")
	if err := loop.runOnce(ctx); err != nil {
		t.Fatalf("tick with one corrupt tenant: %v", err)
	}
	bk, err := loop.bookkeeping(ctx, model.SystemTenantID)
	if err != nil || bk.lastSeq == 0 {
		t.Fatalf("system tenant must still drain (= %+v, err=%v)", bk, err)
	}
	if _, serr := os.Stat(filepath.Join(outDir, tenant.String())); !os.IsNotExist(serr) {
		t.Fatalf("corrupt tenant must stay paused across a tick, stat err = %v", serr)
	}
}
