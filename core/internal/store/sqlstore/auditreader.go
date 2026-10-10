// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type auditReader struct {
	db  *sql.DB
	dia dialect.Dialect
}

// OpenAuditReader performs catalog reads only. Normal serving admission and
// migrations remain in Open; this handle cannot publish a runtime or a writer.
func OpenAuditReader(ctx context.Context, cfg store.Config) (store.AuditReader, error) {
	reader, err := openOfflineReader(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

// OpenAuthReader shares the offline pool's database identity and role checks.
func OpenAuthReader(ctx context.Context, cfg store.Config) (*auditReader, error) {
	return openOfflineReader(ctx, cfg)
}

func openOfflineReader(ctx context.Context, cfg store.Config) (*auditReader, error) {
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		return nil, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
	var db *sql.DB
	var err error
	if cfg.Engine == store.EngineSQLite {
		target, resolveErr := opgate.ResolveSQLiteTarget(cfg.DSN)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if target.Memory() {
			return nil, fmt.Errorf("%w: offline verification requires an existing ledger", store.ErrNotFound)
		}
		info, statErr := os.Stat(target.CanonicalPath())
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: no ledger at %s", store.ErrNotFound, target.CanonicalPath())
		}
		// Rebuild from the resolved identity so caller pragmas cannot enable writes
		// or replace the existing file. Do not set journal mode or chmod its files.
		u := url.URL{Scheme: "file", Path: target.CanonicalPath()}
		q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(5000)"}}
		u.RawQuery = q.Encode()
		db, err = sql.Open("sqlite", u.String())
		if err == nil {
			db.SetMaxOpenConns(1)
		}
	} else {
		db, err = openPostgres(cfg)
	}
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*auditReader, error) {
		_ = db.Close()
		return nil, err
	}
	if cfg.Engine == store.EnginePostgres {
		posture, err := dia.ConnRolePosture(ctx, db)
		if err != nil {
			return fail(err)
		}
		if posture.RLSUnsafe() {
			return fail(fmt.Errorf("sqlstore: offline verification requires a NOSUPERUSER NOBYPASSRLS application role"))
		}

		if cfg.OwnerDSN != "" {
			owner, err := openOwnerPool(ctx, dia, cfg, cfg.OwnerDSN)
			if err != nil {
				return fail(err)
			}
			defer owner.Close() //nolint:errcheck // transient topology reads only
			appID, err := auditDatabaseIdentity(ctx, db)
			if err != nil {
				return fail(err)
			}
			ownerID, err := auditDatabaseIdentity(ctx, owner)
			if err != nil {
				return fail(err)
			}
			if appID != ownerID {
				return fail(fmt.Errorf("sqlstore: --owner-dsn must name the application's database and cluster"))
			}
			role, err := dia.ConnRoleIdentity(ctx, owner)
			if err != nil {
				return fail(err)
			}
			// The full relation verifier calibrates CHECK constraints with DDL;
			// offline verification uses only the catalog-based ACL verifier.
			tx, err := db.BeginTx(ctx, viewTxOptions(cfg.Engine))
			if err != nil {
				return fail(err)
			}
			var oid, ownerOID int64
			var actualOwner string
			err = tx.QueryRowContext(ctx, `SELECT c.oid::pg_catalog.int8, c.relowner::pg_catalog.int8, r.rolname
				FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
				JOIN pg_catalog.pg_roles r ON r.oid=c.relowner
				WHERE n.nspname=$1 AND c.relname=$2`, dialect.EngineSchema, dialect.LoginCapabilityObservationTable).Scan(&oid, &ownerOID, &actualOwner)
			if err == nil && actualOwner != role {
				err = fmt.Errorf("sqlstore: login capability owner differs from --owner-dsn")
			}
			if err == nil {
				app := posture.Role
				if app == role {
					app = ""
				}
				err = verifyPostgresLoginCapabilityACL(ctx, tx, oid, ownerOID, app)
			}
			_ = tx.Rollback()
			if err != nil {
				return fail(err)
			}
		}
		if err := verifyAppendOnlyACL(ctx, db, dia, []string{auditTable}); err != nil {
			return fail(err)
		}
	}
	return &auditReader{db: db, dia: dia}, nil
}

func auditDatabaseIdentity(ctx context.Context, db *sql.DB) (string, error) {
	var identity string
	err := db.QueryRowContext(ctx, `SELECT system_identifier::text || ':' ||
		(SELECT oid::text FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database())
		FROM pg_catalog.pg_control_system()`).Scan(&identity)
	return identity, err
}

func (r *auditReader) Close() error { return r.db.Close() }

func (r *auditReader) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	s := &sqlStore{engine: r.dia.Name(), db: r.db, dia: r.dia, clock: model.SystemClock{}}
	return s.authView(ctx, fn, false)
}

func (r *auditReader) ViewAudit(ctx context.Context, tenant model.TenantID, fn func(store.AuditLog) error) error {
	if tenant.IsZero() {
		return store.ErrNoTenant
	}
	tx, err := r.db.BeginTx(ctx, viewTxOptions(r.dia.Name()))
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read snapshot never commits
	if r.dia.Name() == store.EnginePostgres {
		if err := r.dia.BindTenant(ctx, tx, tenant); err != nil {
			return err
		}
	}
	// SQLite audit reads have explicit tenant predicates and need no scope-table write.
	return fn(&auditLog{tx: tx, tenant: tenant, dia: r.dia, readOnly: true, clock: model.SystemClock{}})
}
