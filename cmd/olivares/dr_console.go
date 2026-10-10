// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/store"
)

// registerSchema is the register func the live store opened with: the restore's scratch
// verification opens the snapshot with it (api.DRConfig.RegisterSchema).
func consoleDRConfig(cfg bootConfig, engine store.Engine, registerSchema func(store.ExtensionRegistry) error, quiesce func(context.Context) error) *api.DRConfig {
	config := &api.DRConfig{DataDir: cfg.DataDir, EngineKind: string(engine), PassphraseFile: osGetenv("OLIVARES_DR_PASSPHRASE_FILE"), RegisterSchema: registerSchema, QuiesceStore: quiesce, Getenv: osGetenv}
	config.SealKeys = func(cipher *dr.KeyCipher) (map[string][]byte, []dr.KeyRef, []dr.SealerProbe, error) {
		return sealSigningKeys(cfg.DataDir, cipher)
	}
	if engine != store.EnginePostgres {
		return config
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
