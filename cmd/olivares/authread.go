// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
	"github.com/spf13/cobra"
)

type authReadEngine struct {
	reader      coreengine.AuthReader
	sourceStore *auth.SourceStore
	secretStore *auth.SecretStore
	trust       *connectorTrustSpec
}

func (e *authReadEngine) Close() error { return e.reader.Close() }

func authReadBoot(cmd *cobra.Command, dataDir, engineName, dsn string) (*authReadEngine, error) {
	cfg, _, err := offlineStoreConfig(cmd, dataDir, engineName, dsn, "")
	if err != nil {
		return nil, err
	}
	reader, err := coreengine.OpenAuthReader(cmd.Context(), cfg)
	if err != nil {
		return nil, err
	}
	return &authReadEngine{reader: reader, sourceStore: auth.NewSourceReader(reader), secretStore: auth.NewSecretReader(reader)}, nil
}

// rosterReadBoot reads the roster and the configured external-plugin trust policy.
// It does not seed sources, open connectors or start the engine.
func rosterReadBoot(cmd *cobra.Command, dataDir, engineName, dsn string) (*authReadEngine, error) {
	e, err := authReadBoot(cmd, dataDir, engineName, dsn)
	if err != nil {
		return nil, err
	}
	cfg, err := loadSourcesConfig(slog.Default())
	if err != nil {
		_ = e.Close()
		return nil, err
	}
	e.trust = cfg.ConnectorTrust
	return e, nil
}

// offlineStoreConfig resolves only existing store credentials and paths.
func offlineStoreConfig(cmd *cobra.Command, dataDir, engineName, dsn, ownerDSN string) (store.Config, string, error) {
	if engineName == "sqlite" {
		if err := checkCMEKSQLiteStoreRef(cmd.Context(), dsn); err != nil {
			return store.Config{}, "", err
		}
	}
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		return store.Config{}, "", err
	}
	if dsn == "" {
		installed, err := quickstartPostgresConfig(cmd.Context(), dir, "")
		if err != nil {
			return store.Config{}, "", err
		}
		if installed.Engine == store.EnginePostgres {
			if cmd.Flags().Changed("engine") && engineName != string(store.EnginePostgres) {
				return store.Config{}, "", errors.New("this data directory uses PostgreSQL; omit --engine or choose a separate --data-dir for SQLite")
			}
			engineName, dsn = string(installed.Engine), installed.DSN
			if ownerDSN == "" {
				ownerDSN = installed.OwnerDSN
			}
		}
	}
	if engineName == "sqlite" && dsn == "" {
		if err := requireDataDir(dir); err != nil {
			return store.Config{}, "", err
		}
		dsn = filepath.Join(dir, "olivares.db")
		if !fileExistsAt(dsn) {
			return store.Config{}, "", exitcode.New(exitcode.NotFound, fmt.Errorf("no store at %s; point --data-dir at an existing installation", dsn))
		}
	}
	if engineName != "postgres" && ownerDSN != "" {
		return store.Config{}, "", fmt.Errorf("--owner-dsn requires --engine postgres")
	}
	for _, ref := range []struct {
		name string
		dst  *string
	}{{"--dsn", &dsn}, {"--owner-dsn", &ownerDSN}} {
		resolved, err := resolveDSNRef(cmd.Context(), ref.name, *ref.dst, osGetenv)
		if err != nil {
			return store.Config{}, "", err
		}
		*ref.dst = resolved
	}
	return store.Config{Engine: store.Engine(engineName), DSN: dsn, OwnerDSN: ownerDSN, MaxConns: 2}, dir, nil
}
