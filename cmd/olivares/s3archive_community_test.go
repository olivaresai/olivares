// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunityS3ArchiveUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.json")
	if err := os.WriteFile(path, []byte(`{"region":"eu-west-1","bucket":"ledger-archive","access_key_id":"AKIDEXAMPLE","secret_access_key":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{"", path} {
		sink, err := buildAuditArchiveSink(auditArchiveConfig{sink: "s3archive", configPath: config}, discardLog())
		if sink != nil || err == nil || !strings.Contains(err.Error(), "Business") {
			t.Errorf("S3 archive = (%T, %v), want Business refusal", sink, err)
		}
	}
	conn, err := buildOutputConnector("s3archive")
	if conn != nil || err == nil || !strings.Contains(err.Error(), "Business") {
		t.Errorf("S3 notifications = (%T, %v), want Business refusal", conn, err)
	}
	t.Run("data export", communityS3ArchiveDataExportBoot)
}

func communityS3ArchiveDataExportBoot(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	t.Setenv(auditArchiveSinkEnv, "")
	seeded, err := boot(ctx, bootConfig{DataDir: data, Engine: "sqlite", NoIngest: true, DemoSeed: true, Logger: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	tenant := seeded.demoTenant
	setOrgSettingKey(t, seeded.store, tenant, archiveLastSeqSettingsKey, "1")
	setOrgSettingKey(t, seeded.store, tenant, archivePendingSettingsKey, "2-3")
	if _, ok, err := seeded.signer.Checkpoint(ctx, seeded.store, tenant); err != nil || !ok {
		t.Fatalf("checkpoint retained ledger: ok=%v err=%v", ok, err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(auditArchiveSinkEnv, "s3archive")
	t.Setenv(auditArchiveConfigEnv, filepath.Join(t.TempDir(), "unavailable-business-config.json"))
	key := filepath.Join(t.TempDir(), "fixture-kek")
	if err := os.WriteFile(key, bytes.Repeat([]byte{42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "existing.drbundle")
	if _, err := runDR("backup", "--data-dir", data, "--out", bundle, "--kek-key-file", key); err != nil {
		t.Fatalf("Community backup of existing S3-configured data: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "export")
	runAudit := func(args ...string) error {
		cmd := newAuditCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := runAudit("archive", "export", "--data-dir", data, "--tenant", tenant.String(), "--out", archive); exitcode.From(err) != exitcode.Edition {
		t.Fatalf("Community directory export must refuse with exit 9: %v", err)
	}
	if err := runAudit("verify", "--data-dir", data, "--tenant", tenant.String(), "--strict"); err != nil {
		t.Fatalf("Community retained ledger did not verify: %v", err)
	}
	eng, err := drBoot(ctx, drFlags{dataDir: data, engineKind: "sqlite"})
	if err != nil {
		t.Fatalf("Community must reopen existing data without the Business sink: %v", err)
	}
	defer eng.Close()
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		if org.Settings[archiveLastSeqSettingsKey] != "1" || org.Settings[archivePendingSettingsKey] != "2-3" {
			t.Errorf("archive cursor/pending boundary changed during Community export: %+v", org.Settings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A historical installation can carry a different module census. Community
// refuses archive export but still verifies its signed ledger without migrating it.
func TestCommunityS3ArchivePreservesOtherEditionLedger(t *testing.T) {
	ctx := t.Context()
	data := t.TempDir()
	key, err := loadAuditSigningKey(data, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadCatalogSigningKey(data, discardLog()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPolicySigningKey(data, discardLog()); err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key.priv)
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(data, "olivares.db")
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: db, SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "archive fixture", Slug: "archive-fixture", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if _, _, err := signer.Checkpoint(ctx, st, tenant); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	files := offlineInstallationFiles(t, data)
	t.Setenv(auditArchiveSinkEnv, "s3archive")
	t.Setenv(auditArchiveConfigEnv, filepath.Join(t.TempDir(), "unavailable-business-config.json"))
	archive := filepath.Join(t.TempDir(), "export")
	out, err := runCLI(t, "audit", "archive", "export", "--data-dir", data, "--tenant", tenant.String(), "--out", archive)
	if exitcode.From(err) != exitcode.Edition {
		t.Fatalf("Community directory export must refuse: %v\n%s", err, out)
	}
	out, err = runCLI(t, "audit", "verify", "--data-dir", data, "--tenant", tenant.String(), "--strict")
	if err != nil || !strings.Contains(out, `"OK": true`) {
		t.Fatalf("historical ledger must still verify: %v\n%s", err, out)
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("edition refusal or ledger verification changed the historical database")
	}
	if got := offlineInstallationFiles(t, data); !reflect.DeepEqual(files, got) {
		t.Fatalf("edition refusal or ledger verification changed installation files: %v -> %v", files, got)
	}
}
