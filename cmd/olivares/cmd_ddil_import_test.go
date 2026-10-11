// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

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
