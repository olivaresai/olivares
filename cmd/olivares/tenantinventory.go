// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// installTenantInventory installs and attests the closed tenant inventory on a
// database the engine has already migrated (the routine reads the core tables), with
// the maintenance DSN. It grants nothing to the application or owner roles, so it is
// safe to run again. The jobs that must cover every tenant (retention, legal hold,
// audit checkpoints and archival) need it; the admin role stays for DR, retirement
// and eventing egress only (TARGET §9).
func installTenantInventory(ctx context.Context, maintenance string, spec store.PgProvisionSpec) error {
	spec.InstallDirectoryInventory = true
	res, err := coreengine.ProvisionPostgres(ctx, maintenance, spec, true)
	if err != nil {
		return err
	}
	if !res.DirectoryInventoryInstalled {
		return errors.New("the install did not report the inventory as installed")
	}
	return nil
}

// applySchemaAndInstallTenantInventory gives a database db init has just provisioned
// the engine's complete schema, applied as the owner with the registrar boot uses
// (what `olivares migrate apply` does), and then the tenant inventory, which reads
// the core tables. A migration with a narrower registration would not do: the first
// start refuses guard bootstrap receipts that its own migration did not write.
func applySchemaAndInstallTenantInventory(ctx context.Context, init postgresInit) error {
	app, err := resolveDSNRef(ctx, "PostgreSQL application DSN", init.config.DSN, osGetenv)
	if err != nil {
		return err
	}
	owner, err := resolveDSNRef(ctx, "PostgreSQL owner DSN", init.config.OwnerDSN, osGetenv)
	if err != nil {
		return err
	}
	return applySchemaAndInstallTenantInventoryWith(ctx, app, owner, init.maintenance, init.spec)
}

// applySchemaAndInstallTenantInventoryWith is the same for DSNs already in hand
// (setup builds its own from the answers it collected).
func applySchemaAndInstallTenantInventoryWith(ctx context.Context, app, owner, maintenance string, spec store.PgProvisionSpec) error {
	registrar, err := bootSchemaRegistrar(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return fmt.Errorf("prepare the schema registrar: %w", err)
	}
	if err := coreengine.ApplyMigrations(ctx, store.Config{Engine: store.EnginePostgres, DSN: app, OwnerDSN: owner}, registrar); err != nil {
		return fmt.Errorf("apply the schema: %w", err)
	}
	return installTenantInventory(ctx, maintenance, spec)
}

// tenantInventoryNotInstalled is the warning db init and setup print when the
// inventory could not be installed; provisioning itself succeeded.
func tenantInventoryNotInstalled(err error) string {
	return "warning: the tenant inventory was not installed (" + err.Error() + "). Retention, legal hold and audit " +
		"checkpoints do not run until it is: run `olivares db init --superuser-dsn … --data-dir … " +
		"--install-directory-inventory` and restart, or pass --admin-dsn. On managed PostgreSQL without a " +
		"superuser they stay off (deploy/postgres/README.md)."
}

// savedPostgresNames are the database and role names of the PostgreSQL configuration
// db init or quickstart saved in dataDir (postgres/app.dsn, postgres/owner.dsn).
type savedPostgresNames struct{ database, app, owner string }

// readSavedPostgresNames reads them; ok is false when dataDir holds none.
func readSavedPostgresNames(dataDir string) (savedPostgresNames, bool) {
	read := func(name string) (*url.URL, bool) {
		raw, err := os.ReadFile(filepath.Join(dataDir, "postgres", name))
		if err != nil {
			return nil, false
		}
		u, err := url.Parse(strings.TrimSpace(string(raw)))
		if err != nil || u.User == nil {
			return nil, false
		}
		return u, true
	}
	app, ok := read("app.dsn")
	if !ok {
		return savedPostgresNames{}, false
	}
	out := savedPostgresNames{database: strings.TrimPrefix(app.Path, "/"), app: app.User.Username()}
	if owner, ok := read("owner.dsn"); ok {
		out.owner = owner.User.Username()
	}
	return out, out.database != "" && out.app != ""
}

// over fills the names spec does not set; a name the operator passed wins.
func (n savedPostgresNames) over(spec store.PgProvisionSpec, ownerNamed bool) store.PgProvisionSpec {
	if spec.Database == "" {
		spec.Database = n.database
	}
	if spec.App.Name == "" {
		spec.App.Name = n.app
	}
	if !ownerNamed && spec.Owner.Name == "" {
		spec.Owner.Name = n.owner
	}
	return spec
}
