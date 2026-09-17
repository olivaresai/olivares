// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrRestoreControlConflict means the destination already carries a control that
// is not the one the caller is asking about. It is a refusal, never a repair.
var ErrRestoreControlConflict = errors.New("sqlstore: the destination already carries a different restore control")

// PendingRestoreSpec names the operation whose initial fence is installed.
// State and completed custody are deliberately not caller-selectable.
type PendingRestoreSpec struct {
	// OpID is a durable 128-bit operation identity, encoded as lowercase hex.
	// It is an observation/binding, never a maintenance capability.
	OpID string
	// PlanSHA256 binds the control to the immutable plan of this operation.
	PlanSHA256 string
}

// RestoreControlReport is durable testimony about the control, read back inside
// the transaction that wrote it. It hands out no handle and no capability.
type RestoreControlReport struct {
	Present          bool
	Created          bool
	Revision         int64
	State            string
	OpID             string
	PlanSHA256       string
	KeysetSHA256     string
	Owner            string
	Database         string
	Schema           string
	SystemIdentifier string
}

// InstallPendingRestoreControl is the EARLY, MINIMAL control installer.
//
// It is the operation that fences a destination before a payload is executed on
// it, and its whole point is how little it does. It connects, it takes the
// publication fence exclusively, it takes the migration lock, and in ONE
// transaction it creates or verifies exactly the compiled control, writes or
// verifies its singleton row and applies its access posture — then reads all of it
// back.
//
// What it deliberately does NOT do, because the ratified contract puts these on the
// other side of an acknowledged payload: it opens no Store, runs no migration,
// creates no SYSTEM tenant, no genesis event, no directory authority, no epoch and
// no relation other than the control. On a max0 destination it leaves a database
// that carries the control and nothing else — in particular it does not make an
// empty destination look like an installation, and it claims no coverage.
//
// A control that is already there is VERIFIED, never adopted and never repaired: a
// different operation, a different plan or a different shape is a refusal. Presence
// alone has never been evidence that this build wrote it.
func InstallPendingRestoreControl(ctx context.Context, cfg store.Config, spec PendingRestoreSpec) (RestoreControlReport, error) {
	if err := validatePendingRestoreSpec(spec); err != nil {
		return RestoreControlReport{}, err
	}
	return withPostgresRestoreControl(ctx, cfg, spec, func(op *restoreOperation) (RestoreControlReport, error) {
		return op.installPending(ctx)
	})
}

func (op *restoreOperation) installPending(ctx context.Context) (RestoreControlReport, error) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if err := op.requireLive(ctx); err != nil {
		return RestoreControlReport{}, err
	}
	if err := validatePendingRestoreSpec(op.spec); err != nil {
		return RestoreControlReport{}, err
	}
	tx, roles, dest, spec, now := op.tx, op.roles, op.dest, op.spec, op.now
	existing := verifyDRRestoreControl(ctx, tx, dest, roles)
	if existing.Verdict == drGateUnreadable || existing.Verdict == drGateMalformed {
		return RestoreControlReport{}, fmt.Errorf("%w: existing restore control: %v", ErrRestoreControlConflict, existing.Cause)
	}
	present := existing.Verdict != drGateAbsent
	created := false
	if !present {
		major, err := postgresMajorVia(ctx, tx)
		if err != nil {
			return RestoreControlReport{}, err
		}
		contract, err := dialect.DRRestoreControlContract(major)
		if err != nil {
			return RestoreControlReport{}, err
		}
		if _, err := tx.ExecContext(ctx, contract.DDL()); err != nil {
			return RestoreControlReport{}, fmt.Errorf("sqlstore: create the restore control: %w", err)
		}
		created = true
		if err := insertRestoreControlRow(ctx, tx, spec, dest, now); err != nil {
			return RestoreControlReport{}, err
		}
	} else {
		// A control that is ALREADY there is judged through the same projection the
		// boot uses, and every outcome except an exact match is a refusal.
		gate := existing
		switch gate.Verdict {
		case drGateUnreadable:
			return RestoreControlReport{}, fmt.Errorf("sqlstore: install the restore control: the existing control could not be read, and an unreadable control is never overwritten: %w", gate.Cause)
		case drGateMalformed:
			return RestoreControlReport{}, fmt.Errorf("%w: %v", ErrRestoreControlConflict, gate.Cause)
		}
		// Presence is accepted ONLY when the compiled specification and the
		// operation identity both match. Anything else belongs to another
		// operation and this call must not speak for it.
		if gate.OpID != spec.OpID || gate.PlanSHA256 != spec.PlanSHA256 || gate.Verdict.String() != opgate.StatePending || gate.KeysetSHA256 != "" {
			return RestoreControlReport{}, fmt.Errorf("%w: it names operation %s under plan %s, and this call names operation %s under plan %s",
				ErrRestoreControlConflict, gate.OpID, gate.PlanSHA256, spec.OpID, spec.PlanSHA256)
		}
		if ok, why := dest.matches(gate.Database, gate.Schema, gate.SystemIdentifier); !ok {
			return RestoreControlReport{}, fmt.Errorf("%w: it is bound to another destination (%s)", ErrRestoreControlConflict, why)
		}
	}
	if created {
		if err := applyRestoreControlACL(ctx, tx, roles); err != nil {
			return RestoreControlReport{}, err
		}
	}
	report, err := readBackRestoreControl(ctx, tx, dest, roles)
	if err != nil {
		return RestoreControlReport{}, err
	}
	report.Created = created
	return report, nil
}

// ReadPostgresRestoreControl reports a destination's control under the fence,
// without changing anything. It exists so an operator command and a test can ask
// the same question the boot asks, through the same projection.
func ReadPostgresRestoreControl(ctx context.Context, cfg store.Config) (RestoreControlReport, error) {
	return withPostgresRestoreControl(ctx, cfg, PendingRestoreSpec{}, func(op *restoreOperation) (RestoreControlReport, error) {
		op.mu.Lock()
		defer op.mu.Unlock()
		return readBackRestoreControl(ctx, op.tx, op.dest, op.roles)
	})
}

func validatePendingRestoreSpec(spec PendingRestoreSpec) error {
	if err := validControlHex("operation id", spec.OpID, 32); err != nil {
		return fmt.Errorf("sqlstore: restore control specification: %w", err)
	}
	if err := validControlHex("plan digest", spec.PlanSHA256, 64); err != nil {
		return fmt.Errorf("sqlstore: restore control specification: %w", err)
	}
	return nil
}

// restoreControlRoles is the exact set of role names the control's access posture
// is aimed at. Each is the role a POOL authenticates as, resolved from that pool,
// never a configured string and never a compile-time default.
type restoreControlRoles struct {
	app                        string
	admin                      string
	owner                      string
	appOID, adminOID, ownerOID int64
}

// withPostgresRestoreControl establishes the ratified order and runs fn inside it:
// the publication fence first, the migration lock second, the transaction third.
//
// The order is the contract's, not a preference. The fence is what makes a
// concurrent boot refuse instead of racing; the migration lock is what serializes
// DDL against other nodes; and the transaction is what makes the relation, the row
// and the ACL one fact. Inverting any pair produces a window in which one of the
// three is true and the others are not.
func withPostgresRestoreControl(
	ctx context.Context,
	cfg store.Config,
	spec PendingRestoreSpec,
	fn func(*restoreOperation) (RestoreControlReport, error),
) (report RestoreControlReport, err error) {
	if cfg.Engine != store.EnginePostgres {
		return RestoreControlReport{}, fmt.Errorf("sqlstore: the restore control relation is a PostgreSQL object; engine %q keeps its control in the external sidecar", cfg.Engine)
	}
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		return RestoreControlReport{}, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = model.SystemClock{}
	}
	ddlDSN := strings.TrimSpace(cfg.OwnerDSN)
	if ddlDSN == "" {
		ddlDSN = cfg.DSN
	}
	// The fence is taken on the DDL connection's destination, exclusively, and
	// BEFORE any pool this call opens for work: a control installed while another
	// operation held the fence would be a second operation on one destination.
	actx, cancelAgreement := context.WithTimeout(ctx, drCoordinationTimeout)
	defer cancelAgreement()
	coord, err := openDRCoordinationExclusive(actx, ddlDSN, cfg)
	if err != nil {
		return RestoreControlReport{}, err
	}
	defer func() {
		if cerr := coord.close(); cerr != nil {
			// An unconfirmed release is an operator fact, not a debug detail: the next
			// boot may find the destination still fenced.
			err = errors.Join(err, cerr)
		}
	}()
	appDB, err := openDB(cfg)
	if err != nil {
		return RestoreControlReport{}, err
	}
	defer appDB.Close() //nolint:errcheck // the report carries the diagnosis
	appConn, err := appDB.Conn(actx)
	if err != nil {
		return RestoreControlReport{}, fmt.Errorf("%w: pin the restore application session: %v", ErrRestoreCoordinationUnknown, err)
	}
	defer appConn.Close() //nolint:errcheck // read-only witness

	ownerDB := appDB
	ownerConn := appConn
	if owner := strings.TrimSpace(cfg.OwnerDSN); owner != "" && owner != strings.TrimSpace(cfg.DSN) {
		ownerDB, err = openOwnerPool(actx, dia, cfg, owner)
		if err != nil {
			return RestoreControlReport{}, err
		}
		defer ownerDB.Close() //nolint:errcheck // DDL-only pool
		ownerConn, err = ownerDB.Conn(actx)
		if err != nil {
			return RestoreControlReport{}, fmt.Errorf("%w: pin the restore DDL session: %v", ErrRestoreCoordinationUnknown, err)
		}
		defer ownerConn.Close() //nolint:errcheck // read-only witness; coordination does the DDL
	}

	var adminConn *sql.Conn
	if admin := strings.TrimSpace(cfg.AdminDSN); admin != "" {
		adminDB, aderr := openAdminPool(actx, dia, cfg)
		if aderr != nil {
			return RestoreControlReport{}, aderr
		}
		defer adminDB.Close() //nolint:errcheck // supplied read-only witness
		adminConn, err = adminDB.Conn(actx)
		if err != nil {
			return RestoreControlReport{}, fmt.Errorf("%w: pin the restore admin session: %v", ErrRestoreCoordinationUnknown, err)
		}
		defer adminConn.Close() //nolint:errcheck // read-only witness
	}

	roles, dest, err := agreeRestoreDestination(actx, cfg, coord, appConn, ownerConn, adminConn)
	if err != nil {
		return RestoreControlReport{}, err
	}
	// No witness transaction or borrowed work connection survives into DDL. The
	// extra coordination pool remains outside all configured MaxConns budgets.
	if adminConn != nil {
		_ = adminConn.Close()
	}
	if ownerConn != appConn {
		_ = ownerConn.Close()
	}
	_ = appConn.Close()
	cancelAgreement()
	lerr := withPinnedMigrationLock(ctx, coord.conn, func(mdb dialect.Execer) error {
		tx, terr := mdb.BeginTx(ctx, nil)
		if terr != nil {
			return terr
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		op := &restoreOperation{tx: tx, coord: coord, roles: roles, dest: dest, spec: spec, now: clock.Now().Time()}
		defer op.retire()
		out, ferr := fn(op)
		if ferr != nil {
			return ferr
		}
		if cerr := op.commit(ctx); cerr != nil {
			return cerr
		}
		report = out
		return nil
	})
	if lerr != nil {
		// The named return is what the deferred fence release folds its own failure
		// into, so an unconfirmed release is never lost behind an earlier error.
		return RestoreControlReport{}, lerr
	}
	return report, err
}

// drRestoreControlInsertQuery creates the control row for a pending restore.
// Schema and table names are compile-time constants; runtime values use placeholders.
const drRestoreControlInsertQuery = `INSERT INTO ` + dialect.EngineSchema + `.` + dialect.DRRestoreControlTable + `
 (control_key, format, revision, state, op_id, plan_sha256,
  destination_database, destination_schema, destination_system_identifier,
  keyset_sha256, report_sha256, observed_at)
 VALUES ($1, $2, 1, $3, $4, pg_catalog.decode($5, 'hex'), $6, $7, $8, pg_catalog.decode($9, 'hex'), NULL, $10)`

func insertRestoreControlRow(ctx context.Context, tx *sql.Tx, spec PendingRestoreSpec, dest drDestination, now time.Time) error {
	_, err := tx.ExecContext(ctx, drRestoreControlInsertQuery,
		dialect.DRRestoreControlKey, opgate.Format, opgate.StatePending, spec.OpID, spec.PlanSHA256,
		dest.Database, dest.Schema, dest.SystemIdentifier, nil, now)
	if err != nil {
		return fmt.Errorf("sqlstore: write the restore control's first row: %w", err)
	}
	return nil
}

// applyRestoreControlACL aims the control's access posture at the EXACT roles the
// pools authenticate as.
//
// Never PUBLIC, and never the closed directory-inventory role: that role's posture
// check counts table-wide, PUBLIC and inherited grants over every relation, so a
// PUBLIC SELECT here would make a correct deployment's H/G inventory fail its own
// verification.
func applyRestoreControlACL(ctx context.Context, tx *sql.Tx, roles restoreControlRoles) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.grantee::pg_catalog.int8,COALESCE(r.rolname,'') FROM pg_catalog.pg_class c
 CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner))) a
 LEFT JOIN pg_catalog.pg_roles r ON r.oid=a.grantee
 WHERE c.oid=$1::pg_catalog.regclass`, dialect.EngineSchema+"."+dialect.DRRestoreControlTable)
	if err != nil {
		return err
	}
	var grantees []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = rows.Close()
			return err
		}
		if id == 0 {
			grantees = append(grantees, "PUBLIC")
		} else {
			grantees = append(grantees, quoteIdent(name))
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	for _, grantee := range grantees {
		if _, err := tx.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON TABLE "+dialect.EngineSchema+"."+dialect.DRRestoreControlTable+" FROM "+grantee+" CASCADE"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "GRANT ALL PRIVILEGES ON TABLE "+dialect.EngineSchema+"."+dialect.DRRestoreControlTable+" TO "+quoteIdent(roles.owner)); err != nil {
		return err
	}
	for _, role := range []string{roles.app, roles.admin} {
		if strings.TrimSpace(role) == "" {
			continue
		}
		for _, stmt := range dialect.PostgresDRRestoreControlACLStmts(role) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("sqlstore: apply the restore control's access posture for %q: %w", role, err)
			}
		}
	}
	return nil
}

// readBackRestoreControl re-reads the control through the SAME projection the boot
// uses. A report derived from what this call intended to write would attest its own
// intention; this attests the database.
func readBackRestoreControl(ctx context.Context, tx *sql.Tx, dest drDestination, roles restoreControlRoles) (RestoreControlReport, error) {
	gate := verifyDRRestoreControl(ctx, tx, dest, roles)
	switch gate.Verdict {
	case drGateAbsent:
		return RestoreControlReport{
			Present: false, Database: dest.Database, Schema: dest.Schema,
			SystemIdentifier: dest.SystemIdentifier,
		}, nil
	case drGateUnreadable, drGateMalformed:
		return RestoreControlReport{}, fmt.Errorf("sqlstore: read the restore control back: %w", gate.Cause)
	}
	return RestoreControlReport{
		Present:          true,
		Owner:            gate.Owner,
		Revision:         gate.Revision,
		State:            gate.Verdict.String(),
		OpID:             gate.OpID,
		PlanSHA256:       gate.PlanSHA256,
		KeysetSHA256:     gate.KeysetSHA256,
		Database:         gate.Database,
		Schema:           gate.Schema,
		SystemIdentifier: gate.SystemIdentifier,
	}, nil
}
