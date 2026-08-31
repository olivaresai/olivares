// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestDDILImportPolicyRoundTripAdoptionAndReplayRefusal(t *testing.T) {
	ctx := context.Background()
	dataDir, tenant, _ := seedDDILImportStore(t, ctx, 0, "unused")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())

	olderSource := `permit(principal in Role::"viewer", action == Action::"agent:read", resource);`
	newerSource := `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`
	seedDDILPolicyRevision(t, ctx, dataDir, tenant, 1, olderSource)
	olderBundle := filepath.Join(t.TempDir(), "older-policy.ddil")
	olderExport := exportDDILPolicyForImportTest(t, ctx, dataDir, tenant, olderBundle, signKey)

	// sigbundle authenticates CreatedAt at second precision. Wait only until the next
	// second so the second exported bundle is strictly newer without a flaky sleep.
	wait := time.Until(olderExport.CreatedAt.Truncate(time.Second).Add(time.Second))
	if wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	seedDDILPolicyRevision(t, ctx, dataDir, tenant, 2, newerSource)
	newerBundle := filepath.Join(t.TempDir(), "newer-policy.ddil")
	newerExport := exportDDILPolicyForImportTest(t, ctx, dataDir, tenant, newerBundle, signKey)
	if !newerExport.CreatedAt.Truncate(time.Second).After(olderExport.CreatedAt.Truncate(time.Second)) {
		t.Fatalf("bundle clocks not monotonic: older=%s newer=%s", olderExport.CreatedAt, newerExport.CreatedAt)
	}

	auditOut := filepath.Join(t.TempDir(), "archive")
	stdout, stderr, err := runDDILCommand(ctx,
		"import", "--data-dir", dataDir, "--bundle", newerBundle, "--pubkey", pubB64,
		"--tenant", tenant.String(), "--audit-out", auditOut, "--json")
	if err != nil {
		t.Fatalf("policy import: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var first ddilImportReport
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("decode policy import: %v\n%s", err, stdout)
	}
	if first.Policy.Adoption == nil || !first.Policy.Adoption.Adopted || first.Policy.Adoption.Reason != "adopted" {
		t.Fatalf("policy adoption report = %+v", first.Policy)
	}
	fresh := readDDILPolicyFreshness(t, ctx, dataDir, tenant)
	wantCreated := newerExport.CreatedAt.Truncate(time.Second)
	if fresh.AdoptedRevision != newerExport.Policy.Revision || !fresh.AdoptedCreatedAt.Equal(wantCreated) ||
		!fresh.RefreshedAt.Equal(wantCreated) || fresh.MaxStaleness != 24*time.Hour {
		t.Fatalf("imported freshness = %+v, export=%+v", fresh, newerExport)
	}

	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--data-dir", dataDir, "--bundle", newerBundle, "--pubkey", pubB64,
		"--tenant", tenant.String(), "--audit-out", auditOut, "--json")
	if err != nil {
		t.Fatalf("policy re-import: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var second ddilImportReport
	if err := json.Unmarshal([]byte(stdout), &second); err != nil {
		t.Fatal(err)
	}
	if second.Policy.Adoption == nil || second.Policy.Adoption.Adopted || second.Policy.Adoption.Reason != "already adopted" {
		t.Fatalf("idempotent policy report = %+v", second.Policy)
	}

	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--data-dir", dataDir, "--bundle", olderBundle, "--pubkey", pubB64,
		"--tenant", tenant.String(), "--audit-out", auditOut, "--json")
	if err == nil || !strings.Contains(err.Error(), "replay/rollback refused") {
		t.Fatalf("older policy import error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var refused ddilImportReport
	if jsonErr := json.Unmarshal([]byte(stdout), &refused); jsonErr != nil {
		t.Fatalf("decode refused import report: %v\n%s", jsonErr, stdout)
	}
	if refused.Policy.Adoption == nil || refused.Policy.Adoption.Reason != "refused" || refused.Policy.Error == "" {
		t.Fatalf("refused policy report = %+v", refused.Policy)
	}
	after := readDDILPolicyFreshness(t, ctx, dataDir, tenant)
	if after.AdoptedRevision != fresh.AdoptedRevision || !after.RefreshedAt.Equal(fresh.RefreshedAt) {
		t.Fatalf("refused replay changed freshness: before=%+v after=%+v", fresh, after)
	}
}

func TestDDILImportRoundTripReconcileAndCatchUp(t *testing.T) {
	ctx := context.Background()
	dataDir, tenant, initialHead := seedDDILImportStore(t, ctx, 3, "agent.initial")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	evidenceBody := []byte{0x00, 0x01, 0xfe, 0xff, '\n'}
	evidenceSource := filepath.Join(t.TempDir(), "proof.bin")
	if err := os.WriteFile(evidenceSource, evidenceBody, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "initial.ddil")
	exportDDILForImportTest(t, ctx, dataDir, tenant, bundle, signKey,
		"--segment-events", "2", "--evidence", "proof.bin="+evidenceSource)

	auditOut := filepath.Join(t.TempDir(), "archive")
	evidenceOut := filepath.Join(t.TempDir(), "evidence-out")
	stdout, stderr, err := runDDILCommand(ctx,
		"import", "--bundle", bundle, "--pubkey", pubB64, "--tenant", tenant.String(),
		"--audit-out", auditOut, "--evidence-out", evidenceOut, "--json")
	if err != nil {
		t.Fatalf("initial DDIL import: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var firstReport ddilImportReport
	if err := json.Unmarshal([]byte(stdout), &firstReport); err != nil {
		t.Fatalf("decode initial import report: %v\n%s", err, stdout)
	}
	if firstReport.Audit.CursorBefore != 0 || firstReport.Audit.CursorAfter != initialHead ||
		firstReport.Audit.AppliedSegments == 0 || firstReport.Audit.SkippedSegments != 0 {
		t.Fatalf("unexpected initial audit report: %+v", firstReport.Audit)
	}
	assertDDILArchiveRange(t, ctx, auditOut, tenant.String(), 1, initialHead)
	extracted := filepath.Join(evidenceOut, "evidence", "proof.bin")
	gotEvidence, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("read extracted evidence: %v", err)
	}
	if !bytes.Equal(gotEvidence, evidenceBody) {
		t.Fatalf("extracted evidence = %x, want %x", gotEvidence, evidenceBody)
	}
	info, err := os.Stat(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("evidence permissions = %o, want 400", got)
	}

	beforeReimport := snapshotDDILDir(t, auditOut)
	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--bundle", bundle, "--pubkey", pubB64, "--tenant", tenant.String(),
		"--audit-out", auditOut, "--evidence-out", evidenceOut, "--json")
	if err != nil {
		t.Fatalf("idempotent DDIL re-import: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var reimportReport ddilImportReport
	if err := json.Unmarshal([]byte(stdout), &reimportReport); err != nil {
		t.Fatalf("decode re-import report: %v", err)
	}
	if reimportReport.Audit.AppliedSegments != 0 || reimportReport.Audit.CursorAfter != initialHead {
		t.Fatalf("re-import audit report = %+v, want zero applied and cursor %d", reimportReport.Audit, initialHead)
	}
	if !reflect.DeepEqual(beforeReimport, snapshotDDILDir(t, auditOut)) {
		t.Fatal("idempotent re-import changed archive file listing or bytes")
	}

	newHead := appendDDILImportEvents(t, ctx, dataDir, tenant, 3, "agent.catchup")
	gapEvidenceBody := []byte("evidence survives an audit gap\n")
	gapEvidenceSource := filepath.Join(t.TempDir(), "gap-proof.txt")
	if err := os.WriteFile(gapEvidenceSource, gapEvidenceBody, 0o600); err != nil {
		t.Fatal(err)
	}
	gapBundle := filepath.Join(t.TempDir(), "gap.ddil")
	exportDDILForImportTest(t, ctx, dataDir, tenant, gapBundle, signKey,
		"--from-seq", strconv.FormatInt(initialHead+2, 10), "--segment-events", "1",
		"--evidence", "gap-proof.txt="+gapEvidenceSource)
	beforeGap := snapshotDDILDir(t, auditOut)
	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--bundle", gapBundle, "--pubkey", pubB64, "--tenant", tenant.String(),
		"--audit-out", auditOut, "--evidence-out", evidenceOut, "--json")
	if err == nil || !strings.Contains(err.Error(), "audit gap before apply") {
		t.Fatalf("gap import error = %v, want deny-closed gap\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var gapReport ddilImportReport
	if jsonErr := json.Unmarshal([]byte(stdout), &gapReport); jsonErr != nil {
		t.Fatalf("decode gap report: %v\n%s", jsonErr, stdout)
	}
	if gapReport.Audit.Gap == nil || gapReport.Audit.Gap.Expected != initialHead+1 ||
		gapReport.Audit.Gap.Got != initialHead+2 || gapReport.Audit.AppliedSegments != 0 {
		t.Fatalf("unexpected gap report: %+v", gapReport.Audit)
	}
	if !reflect.DeepEqual(beforeGap, snapshotDDILDir(t, auditOut)) {
		t.Fatal("gap-refused import changed archive file listing or bytes")
	}
	gapEvidence, readErr := os.ReadFile(filepath.Join(evidenceOut, "evidence", "gap-proof.txt"))
	if readErr != nil || !bytes.Equal(gapEvidence, gapEvidenceBody) {
		t.Fatalf("audit gap blocked independent evidence extraction: body=%q err=%v", gapEvidence, readErr)
	}

	catchUpBundle := filepath.Join(t.TempDir(), "catch-up.ddil")
	exportDDILForImportTest(t, ctx, dataDir, tenant, catchUpBundle, signKey,
		"--from-seq", strconv.FormatInt(initialHead+1, 10), "--segment-events", "2")
	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--bundle", catchUpBundle, "--pubkey", pubB64, "--tenant", tenant.String(),
		"--audit-out", auditOut, "--json")
	if err != nil {
		t.Fatalf("sequential catch-up import: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var catchUpReport ddilImportReport
	if err := json.Unmarshal([]byte(stdout), &catchUpReport); err != nil {
		t.Fatalf("decode catch-up report: %v", err)
	}
	if catchUpReport.Audit.CursorBefore != initialHead || catchUpReport.Audit.CursorAfter != newHead ||
		catchUpReport.Audit.AppliedSegments == 0 {
		t.Fatalf("unexpected catch-up report: %+v", catchUpReport.Audit)
	}
	assertDDILArchiveRange(t, ctx, auditOut, tenant.String(), 1, newHead)
}

func TestDDILImportRejectsTamperAndWrongTenant(t *testing.T) {
	ctx := context.Background()
	dataDir, tenant, _ := seedDDILImportStore(t, ctx, 1, "agent.original")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	bundle := filepath.Join(t.TempDir(), "bundle.ddil")
	exportDDILForImportTest(t, ctx, dataDir, tenant, bundle, signKey)

	tamperedBody, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	tamperedBody[len(tamperedBody)/2] ^= 0x01
	tampered := filepath.Join(t.TempDir(), "tampered.ddil")
	if err := os.WriteFile(tampered, tamperedBody, 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedOut := filepath.Join(t.TempDir(), "tampered-archive")
	_, _, err = runDDILCommand(ctx,
		"import", "--bundle", tampered, "--pubkey", pubB64, "--tenant", tenant.String(), "--audit-out", tamperedOut)
	if err == nil {
		t.Fatal("tampered DDIL bundle unexpectedly imported")
	}
	if _, statErr := os.Stat(tamperedOut); !os.IsNotExist(statErr) {
		t.Fatalf("tampered import touched archive output: %v", statErr)
	}

	wrongTenantOut := filepath.Join(t.TempDir(), "wrong-tenant-archive")
	wrongTenant := model.NewTenantID()
	_, _, err = runDDILCommand(ctx,
		"import", "--bundle", bundle, "--pubkey", pubB64, "--tenant", wrongTenant.String(), "--audit-out", wrongTenantOut)
	if err == nil || !strings.Contains(err.Error(), "does not match DDIL bundle tenant") {
		t.Fatalf("wrong-tenant import error = %v, want mismatch refusal", err)
	}
	if _, statErr := os.Stat(wrongTenantOut); !os.IsNotExist(statErr) {
		t.Fatalf("wrong-tenant import touched archive output: %v", statErr)
	}
}

func TestDDILImportDetectsAuditFork(t *testing.T) {
	ctx := context.Background()
	baseDir, tenant, _ := seedDDILImportStore(t, ctx, 0, "unused")
	leftDir := filepath.Join(t.TempDir(), "left")
	rightDir := filepath.Join(t.TempDir(), "right")
	copyDDILTestDir(t, baseDir, leftDir)
	copyDDILTestDir(t, baseDir, rightDir)
	leftHead := appendDDILImportEvents(t, ctx, leftDir, tenant, 2, "agent.left")
	rightHead := appendDDILImportEvents(t, ctx, rightDir, tenant, 3, "agent.right")
	if rightHead <= leftHead {
		t.Fatalf("fork fixture right head %d must exceed left head %d", rightHead, leftHead)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	leftBundle := filepath.Join(t.TempDir(), "left.ddil")
	exportDDILForImportTest(t, ctx, leftDir, tenant, leftBundle, signKey, "--segment-events", "2")
	auditOut := filepath.Join(t.TempDir(), "archive")
	stdout, stderr, err := runDDILCommand(ctx,
		"import", "--bundle", leftBundle, "--pubkey", pubB64, "--tenant", tenant.String(), "--audit-out", auditOut)
	if err != nil {
		t.Fatalf("import left fork baseline: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	beforeFork := snapshotDDILDir(t, auditOut)

	rightBundle := filepath.Join(t.TempDir(), "right.ddil")
	exportDDILForImportTest(t, ctx, rightDir, tenant, rightBundle, signKey,
		"--from-seq", strconv.FormatInt(leftHead+1, 10), "--segment-events", "2")
	stdout, stderr, err = runDDILCommand(ctx,
		"import", "--bundle", rightBundle, "--pubkey", pubB64, "--tenant", tenant.String(), "--audit-out", auditOut)
	if err == nil || !strings.Contains(err.Error(), "audit fork detected: hash mismatch") ||
		!strings.Contains(err.Error(), "staged prev_segment_last_hash=") || !strings.Contains(err.Error(), "local last_hash=") {
		t.Fatalf("fork import error = %v, want both lineage hashes\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !reflect.DeepEqual(beforeFork, snapshotDDILDir(t, auditOut)) {
		t.Fatal("fork-refused import changed archive file listing or bytes")
	}
}

func TestDDILVerifyCommand(t *testing.T) {
	ctx := context.Background()
	dataDir, tenant, head := seedDDILImportStore(t, ctx, 2, "agent.verify")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	bundle := filepath.Join(t.TempDir(), "verify.ddil")
	exportDDILForImportTest(t, ctx, dataDir, tenant, bundle, signKey)

	stdout, stderr, err := runDDILCommand(ctx,
		"verify", "--bundle", bundle, "--pubkey", base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatalf("verify good bundle: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "DDIL bundle verified") || !strings.Contains(stdout, "tenant: "+tenant.String()) ||
		!strings.Contains(stdout, "1.."+strconv.FormatInt(head, 10)) {
		t.Fatalf("verify summary omitted identity or segment range:\n%s", stdout)
	}

	wrongPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runDDILCommand(ctx,
		"verify", "--bundle", bundle, "--pubkey", base64.StdEncoding.EncodeToString(wrongPub))
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("wrong-signature verify error = %v, want signature refusal", err)
	}
	if strings.Contains(stderr, "Usage:") || strings.Contains(stdout, "Usage:") {
		t.Fatalf("verification failure dumped usage\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func seedDDILImportStore(t *testing.T, ctx context.Context, events int, action string) (string, model.TenantID, int64) {
	t.Helper()
	dataDir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot DDIL import fixture: %v", err)
	}
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "DDIL Import", Slug: "ddil-import", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("create DDIL import tenant: %v", err)
	}
	if events > 0 {
		appendDDILImportEventsWithEngine(t, ctx, eng, tenant, events, action)
	}
	head := ddilImportHead(t, ctx, eng, tenant)
	if err := eng.Close(); err != nil {
		t.Fatalf("close DDIL import fixture: %v", err)
	}
	return dataDir, tenant, head
}

func appendDDILImportEvents(t *testing.T, ctx context.Context, dataDir string, tenant model.TenantID, count int, action string) int64 {
	t.Helper()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot DDIL append fixture: %v", err)
	}
	appendDDILImportEventsWithEngine(t, ctx, eng, tenant, count, action)
	head := ddilImportHead(t, ctx, eng, tenant)
	if err := eng.Close(); err != nil {
		t.Fatalf("close DDIL append fixture: %v", err)
	}
	return head
}

func appendDDILImportEventsWithEngine(t *testing.T, ctx context.Context, eng *engine, tenant model.TenantID, count int, action string) {
	t.Helper()
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < count; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:ddil-test", ActorKind: "user", Action: action,
				TargetKind: "core.agent", TargetID: model.NewID(),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append DDIL fixture events: %v", err)
	}
}

func ddilImportHead(t *testing.T, ctx context.Context, eng *engine, tenant model.TenantID) int64 {
	t.Helper()
	var headSeq int64
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(ctx)
		if err == nil && ok {
			headSeq = head.Seq
		}
		return err
	}); err != nil {
		t.Fatalf("read DDIL fixture head: %v", err)
	}
	return headSeq
}

func exportDDILForImportTest(t *testing.T, ctx context.Context, dataDir string, tenant model.TenantID, bundle, signKey string, extra ...string) {
	t.Helper()
	args := []string{
		"export", "--data-dir", dataDir, "--tenant", tenant.String(), "--out", bundle,
		"--sign-key", signKey, "--no-policy",
	}
	args = append(args, extra...)
	stdout, stderr, err := runDDILCommand(ctx, args...)
	if err != nil {
		t.Fatalf("export DDIL fixture: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
}

func exportDDILPolicyForImportTest(t *testing.T, ctx context.Context, dataDir string, tenant model.TenantID, bundle, signKey string) ddilExportReport {
	t.Helper()
	stdout, stderr, err := runDDILCommand(ctx,
		"export", "--data-dir", dataDir, "--tenant", tenant.String(), "--out", bundle,
		"--sign-key", signKey, "--max-staleness", "24h")
	if err != nil {
		t.Fatalf("export DDIL policy fixture: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var report ddilExportReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode DDIL policy export: %v\n%s", err, stdout)
	}
	if !report.Policy.Included || report.Policy.Revision == "" {
		t.Fatalf("policy export omitted policy: %+v", report)
	}
	return report
}

func seedDDILPolicyRevision(t *testing.T, ctx context.Context, dataDir string, tenant model.TenantID, revision int64, source string) {
	t.Helper()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot DDIL policy fixture: %v", err)
	}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"surface": "cedar", "revision": revision, "content": source,
			"author": "ddil-export-test", "validated": true, "active": true, "note": "",
		})
		return err
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("seed DDIL policy revision: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close DDIL policy fixture: %v", err)
	}
}

func readDDILPolicyFreshness(t *testing.T, ctx context.Context, dataDir string, tenant model.TenantID) governance.FreshnessRecord {
	t.Helper()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot DDIL freshness reader: %v", err)
	}
	rec, found, readErr := governance.PolicyFreshness(ctx, eng.store, tenant)
	closeErr := eng.Close()
	if readErr != nil || !found {
		t.Fatalf("read DDIL freshness: found=%t rec=%+v err=%v", found, rec, readErr)
	}
	if closeErr != nil {
		t.Fatalf("close DDIL freshness reader: %v", closeErr)
	}
	return rec
}

func assertDDILArchiveRange(t *testing.T, ctx context.Context, dir, tenant string, from, to int64) {
	t.Helper()
	report, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatalf("verify imported DDIL archive: %v", err)
	}
	if !report.OK {
		t.Fatalf("imported DDIL archive is not valid: %+v", report)
	}
	got, ok := report.Ranges[tenant]
	if !ok || got.FromSeq != from || got.ToSeq != to {
		t.Fatalf("archive range = %+v (present=%t), want %d..%d", got, ok, from, to)
	}
}

func snapshotDDILDir(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	if err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[rel+"/"] = nil
			return nil
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		snapshot[rel] = body
		return nil
	}); err != nil {
		t.Fatalf("snapshot directory %q: %v", root, err)
	}
	return snapshot
}

func copyDDILTestDir(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode().Perm())
	}); err != nil {
		t.Fatalf("copy DDIL test data directory: %v", err)
	}
}
