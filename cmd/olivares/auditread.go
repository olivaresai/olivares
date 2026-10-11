// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"log/slog"

	"github.com/spf13/cobra"

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
	cfg, dir, err := offlineStoreConfig(cmd, dataDir, engineName, dsn, ownerDSN)
	if err != nil {
		return nil, err
	}
	signer, priors, err := loadOfflineAuditSigner(dir)
	if err != nil {
		return nil, err
	}
	reader, err := coreengine.OpenAuditReader(cmd.Context(), cfg)
	if err != nil {
		return nil, err
	}
	return &auditReadEngine{ledger: reader, signer: signer, auditPriors: priors}, nil
}

func loadOfflineAuditSigner(dir string) (*audit.Signer, []ed25519.PublicKey, error) {
	key, err := loadAuditSigningKey(dir, slog.Default(), withoutMinting())
	if err != nil {
		return nil, nil, err
	}
	var opts []audit.Option
	checkpointKey, err := buildCheckpointKey(slog.Default())
	if err != nil {
		return nil, nil, err
	}
	if checkpointKey != nil {
		opts = append(opts, audit.WithCheckpointKey(checkpointKey))
	}
	signer, err := audit.NewSigner(key.priv, opts...)
	if err != nil {
		return nil, nil, err
	}
	return signer, key.priors, nil
}

type drReadEngine struct {
	ledger store.DRReader
	signer *audit.Signer
}

func (e *drReadEngine) Close() error { return e.ledger.Close() }

func drReadBoot(ctx context.Context, dataDir, snapshot string) (*drReadEngine, error) {
	signer, _, err := loadOfflineAuditSigner(dataDir)
	if err != nil {
		return nil, err
	}
	reader, err := coreengine.OpenDRReader(ctx, store.Config{Engine: store.EngineSQLite, DSN: snapshot})
	if err != nil {
		return nil, err
	}
	return &drReadEngine{ledger: reader, signer: signer}, nil
}
