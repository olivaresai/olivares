// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

type auditReadEngine struct {
	ledger      store.AuditReader
	signer      *audit.Signer
	auditPriors []ed25519.PublicKey
}

func (e *auditReadEngine) Close() error { return e.ledger.Close() }

func auditVerifyBoot(cmd *cobra.Command, dataDir, engineName, dsn, ownerDSN string) (*auditReadEngine, error) {
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		installed, err := quickstartPostgresConfig(cmd.Context(), dir, "")
		if err != nil {
			return nil, err
		}
		if installed.Engine == store.EnginePostgres {
			if cmd.Flags().Changed("engine") && engineName != string(store.EnginePostgres) {
				return nil, errors.New("this data directory uses PostgreSQL; omit --engine or choose a separate --data-dir for SQLite")
			}
			engineName, dsn = string(installed.Engine), installed.DSN
			if ownerDSN == "" {
				ownerDSN = installed.OwnerDSN
			}
		}
	}
	if engineName == "sqlite" && dsn == "" {
		if err := requireDataDir(dir); err != nil {
			return nil, err
		}
		dsn = filepath.Join(dir, "olivares.db")
		if !fileExistsAt(dsn) {
			return nil, exitcode.New(exitcode.NotFound, fmt.Errorf("no store at %s; point --data-dir at an existing installation", dsn))
		}
	}
	if engineName != "postgres" && ownerDSN != "" {
		return nil, fmt.Errorf("--owner-dsn requires --engine postgres")
	}
	for _, ref := range []struct {
		name string
		dst  *string
	}{{"--dsn", &dsn}, {"--owner-dsn", &ownerDSN}} {
		resolved, err := resolveDSNRef(cmd.Context(), ref.name, *ref.dst, osGetenv)
		if err != nil {
			return nil, err
		}
		*ref.dst = resolved
	}
	key, err := loadAuditSigningKey(dir, slog.Default(), withoutMinting())
	if err != nil {
		return nil, err
	}
	var opts []audit.Option
	checkpointKey, err := buildCheckpointKey(slog.Default())
	if err != nil {
		return nil, err
	}
	if checkpointKey != nil {
		opts = append(opts, audit.WithCheckpointKey(checkpointKey))
	}
	signer, err := audit.NewSigner(key.priv, opts...)
	if err != nil {
		return nil, err
	}
	reader, err := coreengine.OpenAuditReader(cmd.Context(), store.Config{Engine: store.Engine(engineName), DSN: dsn, OwnerDSN: ownerDSN, MaxConns: 2})
	if err != nil {
		return nil, err
	}
	return &auditReadEngine{ledger: reader, signer: signer, auditPriors: key.priors}, nil
}
