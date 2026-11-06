// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/dr"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The recipe declaration is independent of the optional service-account split.
var readApplianceImage = isApplianceImage

func isApplianceImage(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch string(data) {
	case "almalinux10-server-amd64\n", "almalinux10-desktop-amd64\n", "debian13-server-amd64\n":
		return true
	}
	return false
}

// registerSchema is the register func the live store opened with: the restore's scratch
// verification opens the snapshot with it (api.DRConfig.RegisterSchema).
func consoleDRConfig(cfg bootConfig, engine store.Engine, registerSchema func(store.ExtensionRegistry) error, quiesce func(context.Context) error, bus eventbus.Bus) *api.DRConfig {
	config := &api.DRConfig{DataDir: cfg.DataDir, EngineKind: string(engine), PassphraseFile: osGetenv("OLIVARES_DR_PASSPHRASE_FILE"), RegisterSchema: registerSchema, QuiesceStore: quiesce, Getenv: osGetenv}
	if bus != nil {
		config.Alert = func(ctx context.Context, tenant model.TenantID, title string) error {
			finding := sdkmodel.FindingReport{
				Kind: "dr_rto_exceeded", Severity: sdkmodel.SeverityHigh, SubjectKind: "dr.restore_test", SubjectRef: "scheduled",
				Title: title, OccurredAt: time.Now().UTC(),
			}
			return bus.Publish(ctx, event.FromObservation(tenant.String(), "dr", finding))
		}
	}
	config.Appliance = readApplianceImage("/etc/olivares-appliance/image")
	config.SealKeys = func(cipher *dr.KeyCipher) (map[string][]byte, []dr.KeyRef, []dr.SealerProbe, error) {
		return sealSigningKeys(cfg.DataDir, cipher)
	}
	config.TreeSources = func(ctx context.Context, snapshot string) ([]dr.TreeSource, []dr.NotIncluded, error) {
		return drTreeSources(ctx, cfg.DataDir, snapshot, engine != store.EnginePostgres)
	}
	if engine != store.EnginePostgres {
		return config
	}
	config.PostgresRestore = func(ctx context.Context, bundle, passphrase string, target api.DRPostgresRestoreTarget) (string, error) {
		return restoreConsolePostgres(ctx, cfg, bundle, passphrase, target)
	}
	config.PostgresSnapshot = func(ctx context.Context, out string) (api.DRBackupSnapshot, error) {
		if strings.TrimSpace(cfg.AdminDSN) == "" {
			return api.DRBackupSnapshot{}, fmt.Errorf("PostgreSQL backup needs the configured admin DSN to read every tenant; configure --admin-dsn, or for a data directory prepared before it had an admin role run `olivares db init --data-dir <dir> --superuser-dsn <dsn> --admin-role <name> --admin-password-file <file>`, then restart")
		}
		flags := drFlags{engineKind: string(engine), dataDir: cfg.DataDir, dsn: cfg.DSN, ownerDSN: cfg.OwnerDSN, adminDSN: cfg.AdminDSN}
		if err := preflightPostgresDR(ctx, flags, "backup"); err != nil {
			return api.DRBackupSnapshot{}, err
		}
		if err := runPgDump(ctx, "pg_dump", cfg.AdminDSN, out); err != nil {
			return api.DRBackupSnapshot{}, err
		}
		proof, err := classifyDRPayload(ctx, string(engine), dr.MethodPgDump, out, drRestoreClientForDump("pg_dump"))
		if err != nil {
			return api.DRBackupSnapshot{}, err
		}
		return api.DRBackupSnapshot{
			Store:    dr.StoreSnapshot{Method: dr.MethodPgDump, File: "store/dump.pgcustom", SHA256: proof.sum, SizeBytes: proof.size},
			Validate: proof.unchanged,
		}, nil
	}
	return config
}

func restoreConsolePostgres(ctx context.Context, live bootConfig, bundle, passphrase string, target api.DRPostgresRestoreTarget) (string, error) {
	sf := drFlags{engineKind: "postgres"}
	for _, item := range []struct {
		name, ref string
		dest      *string
	}{
		{"dsn_ref", target.DSNRef, &sf.dsn}, {"owner_dsn_ref", target.OwnerDSNRef, &sf.ownerDSN}, {"admin_dsn_ref", target.AdminDSNRef, &sf.adminDSN},
	} {
		value, err := resolveScratchDSN(ctx, item.name, item.ref)
		if err != nil {
			return "", err
		}
		*item.dest = value
	}
	if sf.dsn == "" || sf.adminDSN == "" {
		return "", fmt.Errorf("restore requires application and admin connection references")
	}
	if err := preflightPostgresDR(ctx, sf, "restore"); err != nil {
		return "", err
	}
	occupied, err := coreengine.ProbeTargetOccupied(ctx, store.Config{Engine: store.EnginePostgres, DSN: sf.dsn})
	if err != nil || occupied {
		return "", fmt.Errorf("target is occupied or cannot be proven empty; use olivares dr restore with --force --operator and --reason for an occupied target")
	}
	// Compare server-reported identity, so aliases cannot name the live database.
	liveAuthority, err := coreengine.ProbeConnAuthority(ctx, store.Config{Engine: store.EnginePostgres, DSN: live.DSN})
	if err != nil {
		return "", fmt.Errorf("cannot establish the serving database identity")
	}
	targetAuthority, err := coreengine.ProbeConnAuthority(ctx, store.Config{Engine: store.EnginePostgres, DSN: sf.dsn})
	if err != nil || liveAuthority.SystemIdentifier == "" || targetAuthority.SystemIdentifier == "" {
		return "", fmt.Errorf("cannot establish separate database identities")
	}
	if liveAuthority.Database == targetAuthority.Database && liveAuthority.SystemIdentifier == targetAuthority.SystemIdentifier {
		return "", fmt.Errorf("the serving database cannot be a console restore target; use a separate empty db init target")
	}
	maintenance, err := resolveScratchDSN(ctx, "scratch_dsn_ref", target.ScratchDSNRef)
	if err != nil {
		return "", err
	}
	if maintenance == "" {
		return "", fmt.Errorf("scratch_dsn_ref is required")
	}
	work, err := os.MkdirTemp("", "dr-console-pg-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	privateBundle := filepath.Join(work, "input.drbundle")
	if err := dr.CopyFile(bundle, privateBundle); err != nil {
		return "", err
	}
	extracted := filepath.Join(work, "extracted")
	if err := os.Mkdir(extracted, 0700); err != nil {
		return "", err
	}
	pf := filepath.Join(work, "passphrase")
	if err := os.WriteFile(pf, []byte(passphrase), 0600); err != nil {
		return "", err
	}
	m, cipher, err := openVerifiedBundle(privateBundle, extracted, kekFlags{passphraseFile: pf}, false)
	if err != nil {
		return "", err
	}
	if m.EngineKind != "postgres" || m.Store.Method != dr.MethodPgDump {
		return "", fmt.Errorf("console PostgreSQL restore requires a logical PostgreSQL bundle; use olivares dr restore for PITR")
	}
	payload, err := classifyDRPayload(ctx, m.EngineKind, m.Store.Method, filepath.Join(extracted, m.Store.File), "pg_restore")
	if err != nil {
		return "", err
	}
	scratch, err := provisionScratchPostgres(ctx, maintenance)
	if err != nil {
		return "", err
	}
	report, verifyErr := restoreVerifyScratch(ctx, extracted, m, cipher, scratch, "pg_restore", payload)
	if err := errors.Join(verifyErr, scratch.drop(ctx)); err != nil {
		return "", err
	}
	if !report.OK {
		return "", fmt.Errorf("scratch restore ledger verification failed: %s", strings.Join(report.Problems, "; "))
	}
	if err := os.MkdirAll(live.DataDir, 0700); err != nil {
		return "", err
	}
	restored, err := os.MkdirTemp(live.DataDir, "restored-postgres-")
	if err != nil {
		return "", err
	}
	// Run the existing restore workflow in process, with resolved DSNs bound once.
	// It supplies pg_restore's transaction, authority repair, keys, files and ledger verification.
	cmd := drRestoreCmd()
	var receipt bytes.Buffer
	cmd.SetOut(&receipt)
	cmd.SetErr(&receipt)
	cmd.SetArgs([]string{})
	for name, value := range map[string]string{"in": privateBundle, "engine": "postgres", "data-dir": restored, "files-root": filepath.Join(restored, "folders"), "dsn": sf.dsn, "owner-dsn": sf.ownerDSN, "admin-dsn": sf.adminDSN, "passphrase-file": pf, "kek-key-file": ""} {
		if err := cmd.Flags().Set(name, value); err != nil {
			return "", err
		}
	}
	if err := cmd.ExecuteContext(ctx); err != nil {
		return "", fmt.Errorf("restore failed; recovery files retained at %s; use olivares dr restore for recovery: %w", restored, err)
	}
	return fmt.Sprintf("Restored ledger verified. Recovery data directory: %s. Stop the engine, configure the approved target connections and this data directory, then restart. The serving database and its keys were left unchanged.\n%s", restored, strings.TrimSpace(receipt.String())), nil
}
