// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrDirectoryWriterActivationIndeterminate means a commit crossed an
// acknowledgement boundary and a subsequent fresh, fully locked inspection
// could not establish which durable state survived.
var ErrDirectoryWriterActivationIndeterminate = errors.New("directory writer activation indeterminate")

// directoryActivationVerificationTimeout is independent of the request
// context. A canceled caller cannot answer an ambiguous commit, but neither may
// the reconciliation wait forever behind a migration or source-table lock.
const directoryActivationVerificationTimeout = 30 * time.Second

// directoryActivationCommitTestHook replaces Commit only in this package's
// tests. A test can commit successfully and return a synthetic transport error,
// exercising acknowledgement loss without weakening a production branch.
var directoryActivationCommitTestHook func(*sql.Tx) error

type directoryActivationAuthority struct {
	ownerDB    *sql.DB
	closeOwner bool
	roles      guardRoles
	hardened   bool
	adminRole  string
}

type directoryActivationWitnesses struct {
	// app is nil only when ownerTx is the application pool's own transaction.
	// admin is nil only on SQLite.
	app   *sql.Tx
	admin *sql.Tx
}

func (w directoryActivationWitnesses) close() {
	if w.admin != nil {
		_ = w.admin.Rollback()
	}
	if w.app != nil {
		_ = w.app.Rollback()
	}
}

func (w directoryActivationWitnesses) appAuthority(
	ownerTx *sql.Tx,
) directoryWriterACLQuerier {
	if w.app != nil {
		return w.app
	}
	return ownerTx
}

type directoryActivationAttempt struct {
	before  store.DirectoryStatus
	after   store.DirectoryStatus
	changed bool
	state   directoryWriterControlState
}

type directoryActivationCommitError struct{ cause error }

func (e *directoryActivationCommitError) Error() string {
	return "directory writer activation commit acknowledgement: " + e.cause.Error()
}

func (e *directoryActivationCommitError) Unwrap() error { return e.cause }

// ActivateDirectoryWriter is the internal implementation of the public
// core/engine ceremony. raw must be the undecorated concrete store: no public
// Store capability and no decorator forwards this mutation authority.
func ActivateDirectoryWriter(
	ctx context.Context,
	raw store.Store,
	cfg store.Config,
	expectedGeneration int64,
) (before, after store.DirectoryStatus, changed bool, retErr error) {
	s, ok := raw.(*sqlStore)
	if !ok {
		return before, after, false, fmt.Errorf(
			"sqlstore: directory writer activation requires the undecorated store returned by core/engine.Open",
		)
	}
	if cfg.Engine != s.engine {
		return before, after, false, fmt.Errorf(
			"sqlstore: directory writer activation engine %q does not match open store engine %q",
			cfg.Engine, s.engine,
		)
	}
	if expectedGeneration <= 0 || expectedGeneration == math.MaxInt64 {
		return before, after, false, fmt.Errorf(
			"sqlstore: directory writer activation expected generation must be between 1 and %d",
			int64(math.MaxInt64-1),
		)
	}

	authority, err := openDirectoryActivationAuthority(ctx, s, cfg)
	if err != nil {
		return before, after, false, err
	}
	if authority.closeOwner {
		defer authority.ownerDB.Close() //nolint:errcheck // transient authority; operation error is authoritative
	}

	attempt, err := runDirectoryActivation(
		ctx, s, authority, expectedGeneration, true,
	)
	if err == nil {
		if !attempt.changed {
			return attempt.before, attempt.after, false, nil
		}
		verified, verifyErr := verifyCommittedDirectoryActivation(
			ctx, s, authority, expectedGeneration,
		)
		if verifyErr != nil || verified.state.Mode != directoryWriterEnforced ||
			verified.state.ExpectedGeneration != expectedGeneration+1 {
			return attempt.before, verified.after, false, fmt.Errorf(
				"%w: activation commit succeeded but a fresh locked postcondition could not be established: mode=%q generation=%d err=%v",
				ErrDirectoryWriterActivationIndeterminate, verified.state.Mode,
				verified.state.ExpectedGeneration, verifyErr,
			)
		}
		return attempt.before, verified.after, true, nil
	}
	var commitErr *directoryActivationCommitError
	if !errors.As(err, &commitErr) {
		return attempt.before, attempt.after, false, err
	}
	if !directoryActivationCommitOutcomeIsAmbiguous(commitErr.cause) {
		return attempt.before, attempt.before, false, fmt.Errorf(
			"directory writer activation commit was rejected and rolled back: %w",
			commitErr.cause,
		)
	}

	// withMigrationLock has returned before this read: its lock-holding session
	// was force-discarded on every path. Re-entering therefore uses a fresh owner
	// session and cannot deadlock against our own migration lock, including with a
	// one-connection single-role pool.
	reconciled, reconcileErr := verifyCommittedDirectoryActivation(
		ctx, s, authority, expectedGeneration,
	)
	if reconcileErr != nil {
		return attempt.before, reconciled.after, false, fmt.Errorf(
			"%w: commit returned %v and fresh verification failed: %w",
			ErrDirectoryWriterActivationIndeterminate, commitErr.cause, reconcileErr,
		)
	}
	switch {
	case reconciled.state.Mode == directoryWriterEnforced &&
		reconciled.state.ExpectedGeneration == expectedGeneration+1:
		return attempt.before, reconciled.after, true, nil
	case reconciled.state.Mode == directoryWriterStaged &&
		reconciled.state.ExpectedGeneration == expectedGeneration:
		return reconciled.before, reconciled.after, false, fmt.Errorf(
			"directory writer activation did not commit; fresh locked state remains staged generation %d: %w",
			expectedGeneration, commitErr.cause,
		)
	default:
		return attempt.before, reconciled.after, false, fmt.Errorf(
			"%w: commit returned %v; fresh state is mode=%q generation=%d",
			ErrDirectoryWriterActivationIndeterminate, commitErr.cause,
			reconciled.state.Mode, reconciled.state.ExpectedGeneration,
		)
	}
}

func directoryActivationCommitOutcomeIsAmbiguous(err error) bool {
	if commitOutcomeIsAmbiguous(err) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlStateQueryCanceled
}

func verifyCommittedDirectoryActivation(
	ctx context.Context,
	s *sqlStore,
	authority directoryActivationAuthority,
	expectedGeneration int64,
) (directoryActivationAttempt, error) {
	verifyCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), directoryActivationVerificationTimeout,
	)
	defer cancel()
	return runDirectoryActivation(verifyCtx, s, authority, expectedGeneration, false)
}

func openDirectoryActivationAuthority(
	ctx context.Context,
	s *sqlStore,
	cfg store.Config,
) (directoryActivationAuthority, error) {
	out := directoryActivationAuthority{ownerDB: s.db, roles: s.directoryGuardRoles}
	if s.engine != store.EnginePostgres {
		if s.directoryStatus.WriterPosture != store.DirectoryWriterSQLiteCapability {
			return out, fmt.Errorf(
				"sqlstore: directory activation SQLite boot posture is %q, want %q",
				s.directoryStatus.WriterPosture, store.DirectoryWriterSQLiteCapability,
			)
		}
		return out, nil
	}
	switch s.directoryStatus.WriterPosture {
	case store.DirectoryWriterSplitOwner:
		out.hardened = true
	case store.DirectoryWriterSingleRoleCapability:
		out.hardened = false
	default:
		return out, fmt.Errorf("sqlstore: directory activation PostgreSQL boot posture is invalid: %q",
			s.directoryStatus.WriterPosture)
	}

	appPosture, err := s.dia.ConnRolePosture(ctx, s.db)
	if err != nil {
		return out, fmt.Errorf("sqlstore: directory activation app role posture: %w", err)
	}
	if appPosture.TriggersDisabled() {
		return out, fmt.Errorf("sqlstore: directory activation app role %q disables ordinary triggers", appPosture.Role)
	}
	if appPosture.RLSUnsafe() {
		return out, fmt.Errorf("sqlstore: directory activation app role %q is %s", appPosture.Role, appPosture.Why())
	}
	if !out.roles.App.Known || appPosture.Role != out.roles.App.Role {
		return out, fmt.Errorf(
			"sqlstore: directory activation app role changed since boot: got %q want %q",
			appPosture.Role, out.roles.App.Role,
		)
	}

	wantsSeparateOwner := strings.TrimSpace(cfg.OwnerDSN) != "" &&
		strings.TrimSpace(cfg.OwnerDSN) != strings.TrimSpace(cfg.DSN)
	if wantsSeparateOwner != out.roles.OwnerConfigured {
		return out, fmt.Errorf(
			"sqlstore: directory activation owner topology does not match boot: configured=%t boot=%t",
			wantsSeparateOwner, out.roles.OwnerConfigured,
		)
	}
	if wantsSeparateOwner {
		ownerDB, err := openOwnerPool(ctx, s.dia, cfg, strings.TrimSpace(cfg.OwnerDSN))
		if err != nil {
			return out, err
		}
		out.ownerDB, out.closeOwner = ownerDB, true
		ownerPosture, err := s.dia.ConnRolePosture(ctx, ownerDB)
		if err != nil {
			_ = ownerDB.Close()
			return directoryActivationAuthority{}, fmt.Errorf(
				"sqlstore: directory activation owner role posture: %w", err,
			)
		}
		if !out.roles.Owner.Known || ownerPosture.Role != out.roles.Owner.Role {
			_ = ownerDB.Close()
			return directoryActivationAuthority{}, fmt.Errorf(
				"sqlstore: directory activation owner role changed since boot: got %q want %q",
				ownerPosture.Role, out.roles.Owner.Role,
			)
		}
		if ownerPosture.TriggersDisabled() || ownerPosture.RLSUnsafe() {
			_ = ownerDB.Close()
			return directoryActivationAuthority{}, fmt.Errorf(
				"sqlstore: directory activation owner role %q has unsafe posture %s with session_replication_role=%q",
				ownerPosture.Role, ownerPosture.Why(), ownerPosture.ReplicationRole,
			)
		}
	}

	if strings.TrimSpace(cfg.AdminDSN) == "" || s.adminDB == nil || s.adminDB == s.db {
		if out.closeOwner {
			_ = out.ownerDB.Close()
		}
		return directoryActivationAuthority{}, fmt.Errorf(
			"sqlstore: directory activation on PostgreSQL requires the boot-attested separate AdminDSN",
		)
	}
	adminPosture, err := s.dia.ConnRolePosture(ctx, s.adminDB)
	if err != nil {
		if out.closeOwner {
			_ = out.ownerDB.Close()
		}
		return directoryActivationAuthority{}, fmt.Errorf(
			"sqlstore: directory activation admin role posture: %w", err,
		)
	}
	if !adminPosture.BypassRLS || adminPosture.Superuser {
		if out.closeOwner {
			_ = out.ownerDB.Close()
		}
		return directoryActivationAuthority{}, fmt.Errorf(
			"sqlstore: directory activation admin role %q is not NOSUPERUSER BYPASSRLS",
			adminPosture.Role,
		)
	}
	if adminPosture.TriggersDisabled() {
		if out.closeOwner {
			_ = out.ownerDB.Close()
		}
		return directoryActivationAuthority{}, fmt.Errorf(
			"sqlstore: directory activation admin role %q disables ordinary triggers",
			adminPosture.Role,
		)
	}
	out.adminRole = adminPosture.Role
	return out, nil
}

// openDirectoryActivationWitnesses pins every pool whose testimony is consumed
// by the ceremony. A *sql.DB may route consecutive transactions to different
// hosts; using one transaction for the identity challenge and another for ACL
// or coverage would prove A and consume B. PostgreSQL witnesses begin only
// after the owner holds SHARE on every source, so the admin repeatable-read
// snapshot includes the final commits of all drained old writers.
func openDirectoryActivationWitnesses(
	ctx context.Context,
	ownerTx *sql.Tx,
	s *sqlStore,
	authority directoryActivationAuthority,
) (directoryActivationWitnesses, error) {
	var out directoryActivationWitnesses
	if s.dia.Name() != store.EnginePostgres {
		return out, nil
	}

	wantOwner := authority.roles.App
	if authority.roles.OwnerConfigured {
		wantOwner = authority.roles.Owner
	}
	ownerPosture, err := s.dia.ConnRolePosture(ctx, ownerTx)
	if err != nil {
		return out, fmt.Errorf("sqlstore: directory activation pinned owner posture: %w", err)
	}
	if err := verifyDirectoryActivationPinnedSearchPath(ctx, ownerTx, "owner"); err != nil {
		return out, err
	}
	if err := verifyDirectoryActivationWriterPosture("owner", ownerPosture, wantOwner); err != nil {
		return out, err
	}

	if authority.ownerDB != s.db {
		out.app, err = s.db.BeginTx(ctx, &sql.TxOptions{
			Isolation: sql.LevelReadCommitted,
			ReadOnly:  true,
		})
		if err != nil {
			return directoryActivationWitnesses{}, fmt.Errorf(
				"sqlstore: directory activation begin pinned application witness: %w", err,
			)
		}
		appPosture, postureErr := s.dia.ConnRolePosture(ctx, out.app)
		if postureErr != nil {
			out.close()
			return directoryActivationWitnesses{}, fmt.Errorf(
				"sqlstore: directory activation pinned application posture: %w", postureErr,
			)
		}
		if postureErr := verifyDirectoryActivationPinnedSearchPath(
			ctx, out.app, "application",
		); postureErr != nil {
			out.close()
			return directoryActivationWitnesses{}, postureErr
		}
		if postureErr := verifyDirectoryActivationWriterPosture(
			"application", appPosture, authority.roles.App,
		); postureErr != nil {
			out.close()
			return directoryActivationWitnesses{}, postureErr
		}
	}

	out.admin, err = s.adminDB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		out.close()
		return directoryActivationWitnesses{}, fmt.Errorf(
			"sqlstore: directory activation begin pinned admin witness: %w", err,
		)
	}
	adminPosture, err := s.dia.ConnRolePosture(ctx, out.admin)
	if err != nil {
		out.close()
		return directoryActivationWitnesses{}, fmt.Errorf(
			"sqlstore: directory activation pinned admin posture: %w", err,
		)
	}
	if err := verifyDirectoryActivationPinnedSearchPath(ctx, out.admin, "admin"); err != nil {
		out.close()
		return directoryActivationWitnesses{}, err
	}
	if adminPosture.Role != authority.adminRole || !adminPosture.BypassRLS ||
		adminPosture.Superuser || adminPosture.TriggersDisabled() {
		out.close()
		return directoryActivationWitnesses{}, fmt.Errorf(
			"sqlstore: directory activation pinned admin posture changed: role=%q want=%q superuser=%t bypassrls=%t replication_role=%q",
			adminPosture.Role, authority.adminRole, adminPosture.Superuser,
			adminPosture.BypassRLS, adminPosture.ReplicationRole,
		)
	}
	return out, nil
}

func verifyDirectoryActivationPinnedSearchPath(
	ctx context.Context,
	q directoryWriterACLQuerier,
	label string,
) error {
	var path string
	if err := q.QueryRowContext(ctx,
		"SELECT pg_catalog.current_setting('search_path')",
	).Scan(&path); err != nil {
		return fmt.Errorf(
			"sqlstore: directory activation read pinned %s search_path: %w", label, err,
		)
	}
	if path != dialect.EngineSchema {
		return fmt.Errorf(
			"sqlstore: directory activation pinned %s search_path is %q, want exactly %q",
			label, path, dialect.EngineSchema,
		)
	}
	return nil
}

func verifyDirectoryActivationWriterPosture(
	label string,
	posture dialect.RolePosture,
	want guardRoleFact,
) error {
	if !want.Known || strings.TrimSpace(want.Role) == "" || posture.Role != want.Role {
		return fmt.Errorf(
			"sqlstore: directory activation pinned %s role changed: got %q want %q known=%t",
			label, posture.Role, want.Role, want.Known,
		)
	}
	if posture.RLSUnsafe() || posture.TriggersDisabled() {
		return fmt.Errorf(
			"sqlstore: directory activation pinned %s role %q has unsafe posture %s with session_replication_role=%q",
			label, posture.Role, posture.Why(), posture.ReplicationRole,
		)
	}
	return nil
}

func runDirectoryActivation(
	ctx context.Context,
	s *sqlStore,
	authority directoryActivationAuthority,
	expectedGeneration int64,
	mutate bool,
) (directoryActivationAttempt, error) {
	var out directoryActivationAttempt
	err := withMigrationLock(ctx, authority.ownerDB, s.dia, func(mdb dialect.Execer) error {
		tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(s.dia))
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation begin: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit

		state, err := acquireDirectoryWriter(ctx, tx, s.dia)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation acquire writer: %w", err)
		}
		out.state = state
		presentationTenant, err := captureDirectoryActivationPresentation(ctx, tx, s.dia)
		if err != nil {
			return err
		}

		if err := lockDirectoryActivationSources(ctx, tx, s.dia); err != nil {
			return err
		}
		witnesses, err := openDirectoryActivationWitnesses(ctx, tx, s, authority)
		if err != nil {
			return err
		}
		defer witnesses.close()
		if err := verifyDirectoryActivationDatabaseIdentity(
			ctx, tx, witnesses,
		); err != nil {
			return err
		}
		if err := verifyCoreDirectoryRelationsExact(ctx, tx, s.dia, coreDescriptors()); err != nil {
			return fmt.Errorf("sqlstore: directory activation exact directory baseline: %w", err)
		}
		if err := verifyDirectoryWriterGuardsExact(
			ctx, tx, witnesses.appAuthority(tx), s.dia,
			authority.hardened, authority.roles,
		); err != nil {
			return err
		}
		if s.dia.Name() == store.EnginePostgres {
			hardened, err := resolveGuardMetadataPosture(ctx, tx, s.dia, authority.roles)
			if err != nil {
				return fmt.Errorf("sqlstore: directory activation role topology: %w", err)
			}
			if hardened != authority.hardened {
				return fmt.Errorf(
					"sqlstore: directory activation role posture changed since boot: hardened=%t want=%t",
					hardened, authority.hardened,
				)
			}
			if err := verifyPostgresDirectoryActivationAdminReadOnly(
				ctx, tx, authority.adminRole,
			); err != nil {
				return err
			}
		}

		if err := verifyDirectoryActivationCoverage(ctx, tx, witnesses.admin, s); err != nil {
			return err
		}
		if err := restoreDirectoryActivationPresentation(
			ctx, tx, s.dia, presentationTenant,
		); err != nil {
			return err
		}
		out.before = directoryActivationStatus(s, state)
		out.after = out.before

		switch state.Mode {
		case directoryWriterEnforced:
			if state.ExpectedGeneration != expectedGeneration &&
				state.ExpectedGeneration != expectedGeneration+1 {
				return fmt.Errorf(
					"%w: directory writer is already enforced at generation %d; expected verify-only %d or retry result %d",
					store.ErrConflict, state.ExpectedGeneration,
					expectedGeneration, expectedGeneration+1,
				)
			}
			return nil
		case directoryWriterStaged:
			if state.ExpectedGeneration != expectedGeneration {
				return fmt.Errorf(
					"%w: directory writer staged generation is %d, expected %d",
					store.ErrConflict, state.ExpectedGeneration, expectedGeneration,
				)
			}
			if !mutate {
				return nil
			}
		default:
			return fmt.Errorf("sqlstore: directory activation invalid control mode %q", state.Mode)
		}

		query := s.dia.Rebind("UPDATE " +
			directoryWriterRelation(s.dia, dialect.DirectoryWriterControlTable) +
			" SET mode = ?, expected_generation = expected_generation + 1" +
			" WHERE control_key = ? AND mode = ? AND expected_generation = ?" +
			" AND expected_generation < ?")
		result, err := tx.ExecContext(
			ctx, query,
			string(directoryWriterEnforced), directoryWriterLockKey,
			string(directoryWriterStaged), expectedGeneration, int64(math.MaxInt64),
		)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation CAS: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation CAS rows affected: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf(
				"%w: directory writer activation CAS affected %d rows, want exactly one",
				store.ErrConflict, rows,
			)
		}
		afterState, err := readDirectoryWriterControlState(ctx, tx, s.dia)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation CAS read-back: %w", err)
		}
		if afterState.Mode != directoryWriterEnforced ||
			afterState.ExpectedGeneration != expectedGeneration+1 {
			return fmt.Errorf(
				"sqlstore: directory activation CAS read-back is mode=%q generation=%d, want enforced/%d",
				afterState.Mode, afterState.ExpectedGeneration, expectedGeneration+1,
			)
		}
		out.state, out.after, out.changed = afterState, directoryActivationStatus(s, afterState), true

		if directoryActivationCommitTestHook != nil {
			if err := directoryActivationCommitTestHook(tx); err != nil {
				return &directoryActivationCommitError{cause: err}
			}
			return nil
		}
		if err := tx.Commit(); err != nil {
			return &directoryActivationCommitError{cause: err}
		}
		return nil
	})
	return out, err
}

// captureDirectoryActivationPresentation runs before coverage performs its
// first bind. In SQLite the scope pin is durable process state. A normal
// tenant-scoped mutation deliberately leaves its canonical business tenant
// there, while system work leaves SYSTEM, so the ceremony must preserve the
// exact valid value it observes rather than normalize it. Structural or value
// drift is rejected before coverage can DELETE+INSERT and thereby heal it.
// PostgreSQL has no durable pin; its transaction-local generation baseline is
// still rechecked here and SYSTEM is returned only as a restoration token.
func captureDirectoryActivationPresentation(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (model.TenantID, error) {
	if err := verifyDirectoryWriterPresentationBaseline(ctx, tx, dia); err != nil {
		return "", fmt.Errorf("sqlstore: directory activation presentation baseline: %w", err)
	}
	if dia.Name() != store.EngineSQLite {
		return model.SystemTenantID, nil
	}
	// #nosec G202 -- only the relation is interpolated, from a dialect constant via directoryWriterRelation; the projection is a fixed column list
	rows, err := tx.QueryContext(ctx, "SELECT tenant_id, typeof(tenant_id) FROM "+
		directoryWriterRelation(dia, dialect.ScopeTenantTable))
	if err != nil {
		return "", fmt.Errorf("sqlstore: directory activation read SQLite presentation pin: %w", err)
	}
	defer rows.Close()
	var count int
	var tenant, storageClass string
	for rows.Next() {
		count++
		if count > 1 {
			_ = rows.Close()
			return "", fmt.Errorf(
				"sqlstore: directory activation SQLite presentation pin contains more than one row",
			)
		}
		if err := rows.Scan(&tenant, &storageClass); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf(
				"sqlstore: directory activation read SQLite presentation pin: %w", err,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf(
			"sqlstore: directory activation read SQLite presentation pin: %w", err,
		)
	}
	if count != 1 || storageClass != "text" {
		return "", fmt.Errorf(
			"sqlstore: directory activation SQLite presentation pin is count=%d tenant=%q storage=%q, want exactly one canonical text tenant row",
			count, tenant, storageClass,
		)
	}
	presentation := model.TenantID(tenant)
	if presentation.IsSystem() {
		return presentation, nil
	}
	parsed, err := uuid.Parse(tenant)
	if err != nil || parsed.String() != tenant || parsed.Version() != uuid.Version(7) ||
		parsed.Variant() != uuid.RFC4122 {
		return "", fmt.Errorf(
			"sqlstore: directory activation SQLite presentation pin tenant %q is not SYSTEM or a canonical RFC 4122 UUIDv7",
			tenant,
		)
	}
	return presentation, nil
}

// restoreDirectoryActivationPresentation closes the privileged coverage loop
// without publishing a new presentation value. SQLite must regain the exact
// durable tenant captured before the first bind. PostgreSQL clears the
// transaction-local generation proof; binding SYSTEM is itself local there.
// A read-back makes restoration an assertion, not a best-effort cleanup.
func restoreDirectoryActivationPresentation(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	presentation model.TenantID,
) error {
	if err := bindDirectoryTenant(ctx, tx, dia, presentation); err != nil {
		return fmt.Errorf("sqlstore: directory activation restore presentation: %w", err)
	}
	if err := finishDirectoryWriter(ctx, tx, dia); err != nil {
		return fmt.Errorf("sqlstore: directory activation finish presentation restore: %w", err)
	}
	if dia.Name() != store.EngineSQLite {
		return nil
	}
	got, err := captureDirectoryActivationPresentation(ctx, tx, dia)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation verify restored presentation: %w", err)
	}
	if got != presentation {
		return fmt.Errorf(
			"sqlstore: directory activation restored SQLite presentation pin is %q, want exact preflight value %q",
			got, presentation,
		)
	}
	return nil
}

func directoryActivationStatus(
	s *sqlStore,
	state directoryWriterControlState,
) store.DirectoryStatus {
	return store.DirectoryStatus{
		Enabled:               false,
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlMode(state.Mode),
		WriterPosture:         s.directoryStatus.WriterPosture,
		ExpectedGeneration:    state.ExpectedGeneration,
	}
}

func directoryActivationAuthorityTables() []string {
	tables := append([]string{
		dialect.DirectoryWriterControlTable,
		directoryEpochDescriptor.Table,
		directoryTombstoneDescriptor.Table,
		userTombstoneDescriptor.Table,
	}, directoryWriterSourceTables...)
	sort.Strings(tables)
	return tables
}

func lockDirectoryActivationSources(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	tables := append([]string(nil), directoryWriterSourceTables...)
	sort.Strings(tables)
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx,
			"LOCK TABLE ONLY "+directoryWriterRelation(dia, table)+" IN SHARE MODE",
		); err != nil {
			return fmt.Errorf("sqlstore: directory activation lock source %q: %w", table, err)
		}
	}
	return nil
}

func verifyDirectoryActivationCoverage(
	ctx context.Context,
	ownerTx *sql.Tx,
	adminTx *sql.Tx,
	s *sqlStore,
) error {
	queryer := directoryTenantEnumerator(ownerTx)
	if s.dia.Name() == store.EnginePostgres {
		if adminTx == nil {
			return directoryUnavailable("authoritative PostgreSQL enumeration requires AdminDSN", nil)
		}
		queryer = adminTx
	}

	tenants, err := enumerateDirectoryTenants(ctx, queryer, s.dia)
	if err != nil {
		return directoryUnavailable("enumerate authoritative organizations", err)
	}
	epochs, err := enumerateDirectoryActivationEpochs(ctx, queryer, s.dia)
	if err != nil {
		return directoryUnavailable("enumerate authoritative epochs", err)
	}
	missing, orphan := compareDirectoryActivationCoverage(tenants, epochs)
	if len(missing) != 0 || len(orphan) != 0 {
		return directoryUnavailable(
			fmt.Sprintf("organization/epoch coverage mismatch missing=%v orphan=%v", missing, orphan),
			nil,
		)
	}
	for _, tenant := range tenants {
		if err := bindDirectoryTenant(ctx, ownerTx, s.dia, tenant); err != nil {
			return directoryUnavailable("bind owner to covered tenant "+tenant.String(), err)
		}
		epoch, found, err := readDirectoryEpochRow(ctx, ownerTx, s.dia, tenant)
		if err != nil {
			return directoryUnavailable("read covered tenant epoch "+tenant.String(), err)
		}
		if !found || epoch.Version != epochs[tenant] {
			return directoryUnavailable(
				fmt.Sprintf("owner epoch for tenant %s found=%t version=%d authoritative=%d",
					tenant, found, epoch.Version, epochs[tenant]),
				nil,
			)
		}
	}
	return nil
}

func enumerateDirectoryActivationEpochs(
	ctx context.Context,
	q directoryTenantEnumerator,
	dia dialect.Dialect,
) (map[model.TenantID]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, tenant_id, version FROM "+
			directoryWriterRelation(dia, directoryEpochDescriptor.Table)+
			" ORDER BY tenant_id, id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[model.TenantID]int64)
	for rows.Next() {
		var rawID, rawTenant string
		var version int64
		if err := rows.Scan(&rawID, &rawTenant, &version); err != nil {
			return nil, err
		}
		tenant := model.TenantID(rawTenant)
		epoch := model.DirectoryEpoch{BaseFields: model.BaseFields{
			ID: model.ID(rawID), TenantID: tenant, Version: version,
		}}
		if err := epoch.Validate(); err != nil {
			return nil, fmt.Errorf("epoch %q/%q version %d is invalid: %w",
				rawID, rawTenant, version, err)
		}
		if _, duplicate := out[tenant]; duplicate {
			return nil, fmt.Errorf("epoch tenant %s appears more than once", tenant)
		}
		out[tenant] = version
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func compareDirectoryActivationCoverage(
	tenants []model.TenantID,
	epochs map[model.TenantID]int64,
) (missing, orphan []string) {
	want := make(map[model.TenantID]struct{}, len(tenants))
	for _, tenant := range tenants {
		want[tenant] = struct{}{}
		if _, ok := epochs[tenant]; !ok {
			missing = append(missing, tenant.String())
		}
	}
	for tenant := range epochs {
		if _, ok := want[tenant]; !ok {
			orphan = append(orphan, tenant.String())
		}
	}
	sort.Strings(missing)
	sort.Strings(orphan)
	return missing, orphan
}

func verifyDirectoryActivationDatabaseIdentity(
	ctx context.Context,
	ownerTx *sql.Tx,
	witnesses directoryActivationWitnesses,
) error {
	if witnesses.admin == nil {
		return nil
	}
	key, err := randomDirectoryActivationLockKey()
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation identity challenge: %w", err)
	}
	if _, err := ownerTx.ExecContext(ctx,
		"SELECT pg_catalog.pg_advisory_xact_lock($1)", key,
	); err != nil {
		return fmt.Errorf("sqlstore: directory activation hold identity challenge: %w", err)
	}
	var database string
	if err := ownerTx.QueryRowContext(ctx,
		"SELECT pg_catalog.current_database()",
	).Scan(&database); err != nil {
		return fmt.Errorf("sqlstore: directory activation owner database identity: %w", err)
	}
	// In single-role mode ownerTx already owns the sole app connection. Asking
	// the application pool for another one deadlocks at MaxConns=1 and proves
	// nothing new.
	if witnesses.app != nil {
		if err := probeDirectoryActivationDatabase(ctx, witnesses.app, database, key, "application"); err != nil {
			return err
		}
	}
	return probeDirectoryActivationDatabase(ctx, witnesses.admin, database, key, "admin")
}

func randomDirectoryActivationLockKey() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(raw[:])), nil
}

func probeDirectoryActivationDatabase(
	ctx context.Context,
	tx *sql.Tx,
	wantDatabase string,
	key int64,
	label string,
) error {
	var database string
	if err := tx.QueryRowContext(ctx,
		"SELECT pg_catalog.current_database()",
	).Scan(&database); err != nil {
		return fmt.Errorf("sqlstore: directory activation %s database identity: %w", label, err)
	}
	var acquired bool
	if err := tx.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_xact_lock($1)", key,
	).Scan(&acquired); err != nil {
		return fmt.Errorf("sqlstore: directory activation %s identity challenge: %w", label, err)
	}
	if database != wantDatabase || acquired {
		return fmt.Errorf(
			"sqlstore: directory activation %s DSN does not address the owner database: database=%q want=%q challenge_acquired=%t",
			label, database, wantDatabase, acquired,
		)
	}
	return nil
}

// verifyPostgresDirectoryActivationAdminReadOnly proves that the BYPASSRLS
// witness can enumerate the complete directory without also being an authority
// that can fabricate that estate. The proof includes every role it can inherit,
// assume, or grant itself through the version-specific SET ROLE closure.
func verifyPostgresDirectoryActivationAdminReadOnly(
	ctx context.Context,
	ownerTx *sql.Tx,
	adminRole string,
) error {
	if strings.TrimSpace(adminRole) == "" {
		return fmt.Errorf("sqlstore: directory activation admin authority is unresolved")
	}
	major, err := postgresMajorVia(ctx, ownerTx)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation admin authority: %w", err)
	}
	tables := directoryActivationAuthorityTables()
	list, args := tableParams([]any{dialect.EngineSchema, adminRole}, tables)
	// #nosec G202 -- list is placeholder-only output and the reachability
	// fragments are closed, version-selected SQL constants.
	query := guardReachableCTE(major) + `SELECT r.rolname, r.rolsuper, r.rolcreaterole,
       c.relname, c.relowner = r.oid,
       pg_catalog.has_schema_privilege(r.oid, n.oid, 'CREATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'SELECT'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'DELETE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'TRUNCATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'REFERENCES'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'TRIGGER'),
       pg_catalog.has_any_column_privilege(r.oid, c.oid, 'INSERT'),
       pg_catalog.has_any_column_privilege(r.oid, c.oid, 'UPDATE'),
       pg_catalog.has_any_column_privilege(r.oid, c.oid, 'REFERENCES'),
       p.proowner = r.oid,
       pg_catalog.has_function_privilege(r.oid, p.oid, 'EXECUTE')
FROM pg_catalog.pg_roles r
CROSS JOIN pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)
  AND pn.nspname = $1 AND p.proname = '` + dialect.DirectoryWriterGuardFunction + `' AND p.pronargs = 0
  AND (r.rolname = $2 OR ` + guardRoleReachability(major) + `)
ORDER BY r.rolname, c.relname`
	rows, err := ownerTx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation admin authority projection: %w", err)
	}
	defer rows.Close()
	adminSeen := 0
	for rows.Next() {
		var role, table string
		var super, createRole, ownsTable, createsSchema, selects bool
		var insertTable, updateTable, deleteTable, truncateTable bool
		var referencesTable, triggerTable bool
		var insertColumn, updateColumn, referencesColumn bool
		var ownsFunction, executesFunction bool
		if err := rows.Scan(
			&role, &super, &createRole, &table, &ownsTable, &createsSchema,
			&selects, &insertTable, &updateTable, &deleteTable, &truncateTable,
			&referencesTable, &triggerTable, &insertColumn, &updateColumn,
			&referencesColumn, &ownsFunction, &executesFunction,
		); err != nil {
			return fmt.Errorf("sqlstore: directory activation admin authority projection: %w", err)
		}
		if role == adminRole {
			adminSeen++
			if !selects {
				return fmt.Errorf("sqlstore: directory activation admin role %q lacks SELECT on %q",
					adminRole, table)
			}
		}
		var reason string
		switch {
		case super:
			reason = "is superuser"
		case createRole:
			reason = "holds CREATEROLE"
		case createsSchema:
			reason = "can CREATE in the engine schema"
		case ownsFunction:
			reason = "owns the directory writer function"
		case executesFunction:
			reason = "can execute the directory writer function"
		case ownsTable:
			reason = "owns " + table
		case insertTable || updateTable || deleteTable || truncateTable ||
			referencesTable || triggerTable || insertColumn || updateColumn || referencesColumn:
			reason = "holds write or administration privilege on " + table
		}
		if reason != "" {
			return fmt.Errorf(
				"sqlstore: directory activation admin authority is not read-only: role %q (reachable from %q) %s",
				role, adminRole, reason,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: directory activation admin authority projection: %w", err)
	}
	if adminSeen != len(tables) {
		return fmt.Errorf(
			"sqlstore: directory activation admin authority projected %d direct table rows, want %d",
			adminSeen, len(tables),
		)
	}
	if major < 17 {
		return nil
	}

	maintainList, maintainArgs := tableParams([]any{dialect.EngineSchema, adminRole}, tables)
	// #nosec G202 -- same closed placeholder/reachability construction as above.
	maintainQuery := guardReachableCTE(major) + `SELECT r.rolname, c.relname,
       pg_catalog.has_table_privilege(r.oid, c.oid, 'MAINTAIN')
FROM pg_catalog.pg_roles r
CROSS JOIN pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + maintainList + `)
  AND (r.rolname = $2 OR ` + guardRoleReachability(major) + `)
ORDER BY r.rolname, c.relname`
	maintainRows, err := ownerTx.QueryContext(ctx, maintainQuery, maintainArgs...)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation admin MAINTAIN authority: %w", err)
	}
	defer maintainRows.Close()
	for maintainRows.Next() {
		var role, table string
		var maintain bool
		if err := maintainRows.Scan(&role, &table, &maintain); err != nil {
			return fmt.Errorf("sqlstore: directory activation admin MAINTAIN authority: %w", err)
		}
		if maintain {
			return fmt.Errorf(
				"sqlstore: directory activation admin authority is not read-only: role %q (reachable from %q) holds MAINTAIN on %q",
				role, adminRole, table,
			)
		}
	}
	if err := maintainRows.Err(); err != nil {
		return fmt.Errorf("sqlstore: directory activation admin MAINTAIN authority: %w", err)
	}
	return nil
}
