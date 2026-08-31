// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// defaultLimit and maxLimit bound a List page.
const (
	defaultLimit = 100
	maxLimit     = 1000
)

// genericRepo is the one CRUD/SQL-generation engine, shared by every typed core
// repository and every module GenericRepo. It is always pinned to one tenant:
// it stamps tenant_id on writes from the pinned tenant (never the caller) and
// appends the tenant predicate on every read, update and delete. There is no
// code path through it that omits the tenant scope.
type genericRepo struct {
	tx              *sql.Tx
	tenant          model.TenantID
	dia             dialect.Dialect
	desc            model.EntityDescriptor
	clock           model.Clock
	audit           *auditLog // non-nil only for Audited descriptors
	debug           bool
	readOnly        bool // set in a View scope: writes are rejected
	engineQualified bool // pin closed-inventory engine relations past TEMP/search_path shadows
	// transactionStamp is present on repositories issued by tenantScope. Only the
	// explicit store.TransactionStampedGenericRepo methods consume it; ordinary
	// GenericRepo writes retain their established application-clock behavior.
	transactionStamp func() (model.Timestamp, bool)
}

var _ store.TransactionStampedGenericRepo = (*genericRepo)(nil)

// Descriptor returns the entity's schema declaration (store.GenericRepo). The
// EntityDescriptor value carries slices (Fields, Indexes, Checks); they are
// declaration-time data owned by the registry and never mutated after boot, so
// the value copy Go makes here is the whole isolation this needs.
func (r *genericRepo) Descriptor() model.EntityDescriptor { return r.desc }

// Create stamps the base fields from the pinned tenant and inserts the row,
// allocating a fresh id.
func (r *genericRepo) Create(ctx context.Context, in model.Record) (model.Record, error) {
	return r.insertWithIDAt(ctx, in, model.NewID(), r.clock.Now())
}

func (r *genericRepo) CreateAtTransactionTime(
	ctx context.Context,
	in model.Record,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	now, err := r.observedTransactionStamp()
	if err != nil {
		return nil, err
	}
	return r.insertWithIDAt(ctx, in, model.NewID(), now)
}

// CreateWithID inserts the row under exactly id after validating the operation's
// deliberately narrower identifier contract. ParseID remains permissive for
// legacy read paths; preassigned write ids must be canonical lowercase RFC 4122
// UUIDv7 values and are never silently replaced.
func (r *genericRepo) CreateWithID(
	ctx context.Context, id model.ID, in model.Record,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	if err := validateCreateID(id); err != nil {
		return nil, err
	}
	return r.insertWithIDAt(ctx, in, id, r.clock.Now())
}

func (r *genericRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	in model.Record,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	if err := validateCreateID(id); err != nil {
		return nil, err
	}
	now, err := r.observedTransactionStamp()
	if err != nil {
		return nil, err
	}
	return r.insertWithIDAt(ctx, in, id, now)
}

// insertWithIDAt is the common stamped insert used by the ordinary and
// transaction-time create paths.
// Its callers alone choose the id: Create generates one and CreateWithID first
// proves the supplied value satisfies the strict public contract.
func (r *genericRepo) insertWithIDAt(
	ctx context.Context,
	in model.Record,
	id model.ID,
	now model.Timestamp,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	out := make(model.Record, len(in)+6)
	for _, f := range r.desc.Fields {
		out[f.Name] = redactField(f, in[f.Name])
	}
	base := model.BaseFields{
		ID:        id,
		TenantID:  r.tenant, // stamped from the scope, never the caller
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
	baseToRecord(out, base, r.desc.SoftDelete)

	cols := r.desc.AllColumns()
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = out[c]
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		r.relation(), strings.Join(cols, ", "), placeholders(len(cols)))
	result, err := r.tx.ExecContext(ctx, r.dia.Rebind(q), args...)
	if err != nil {
		return nil, mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, fmt.Errorf("insert into %s affected %d rows, want exactly one",
			r.desc.Table, rows)
	}
	if err := r.maybeAudit(ctx, "create", base.ID); err != nil {
		return nil, err
	}
	return out, nil
}

func validateCreateID(id model.ID) error {
	raw := id.String()
	if id.IsZero() {
		return store.ErrInvalidID
	}
	u, err := uuid.Parse(raw)
	if err != nil || u.String() != raw ||
		u.Version() != uuid.Version(7) || u.Variant() != uuid.RFC4122 {
		return store.ErrInvalidID
	}
	return nil
}

// Get returns the row by id within the pinned tenant, or ErrNotFound.
func (r *genericRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	return r.get(ctx, id, false)
}

// Lock returns a row while fencing concurrent updates until the surrounding
// Mutate transaction commits. PostgreSQL needs an explicit row lock. SQLite's
// single-writer transaction is already the fence once the caller has acquired
// write authority; issuing FOR UPDATE there would be invalid SQL.
func (r *genericRepo) Lock(ctx context.Context, id model.ID) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	if r.dia.Name() == store.EngineSQLite {
		// database/sql starts SQLite transactions deferred. A read alone would not
		// yet own the engine's single writer slot, so another process could update
		// the authorization fact before this transaction commits. Reserve that slot
		// through the engine-owned scope row, not through a semantic no-op UPDATE of
		// the fact itself: directory guards correctly treat every source UPDATE as a
		// write and would otherwise make a read fence require writer generation.
		// #nosec G202 -- the relation comes from dialect.ScopeTenantTable and is quoted; the SET is fixed and the runtime tenant is bound by the ? in the WHERE, not in the SET
		q := "UPDATE main." + quoteIdent(dialect.ScopeTenantTable) +
			" SET tenant_id = tenant_id WHERE tenant_id = ?"
		result, err := r.tx.ExecContext(ctx, q, r.tenant.String())
		if err != nil {
			return nil, mapWriteErr(err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, fmt.Errorf("SQLite read fence found %d tenant scope rows, want exactly one", rows)
		}
	}
	return r.get(ctx, id, true)
}

func (r *genericRepo) get(ctx context.Context, id model.ID, lock bool) (model.Record, error) {
	cols := r.desc.AllColumns()
	q := fmt.Sprintf("SELECT %s FROM %s WHERE id = ? AND tenant_id = ?%s",
		strings.Join(cols, ", "), r.relation(), r.softDeleteClause())
	if lock {
		q += rowLockSuffix(r.dia.Name())
	}
	r.guard(q)
	st, err := newScanState(r.desc, cols)
	if err != nil {
		return nil, err
	}
	row := r.tx.QueryRowContext(ctx, r.dia.Rebind(q), id.String(), r.tenant.String())
	if err := row.Scan(st.dests...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return st.record(), nil
}

func rowLockSuffix(engine store.Engine) string {
	if engine == store.EnginePostgres {
		return " FOR UPDATE"
	}
	return ""
}

// List returns a page of rows in the pinned tenant matching q.
func (r *genericRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	cols := r.desc.AllColumns()
	var where []string
	args := []any{r.tenant.String()}
	where = append(where, "tenant_id = ?")
	if !q.IncludeDeleted && r.desc.SoftDelete {
		where = append(where, "deleted_at IS NULL")
	}
	for _, f := range q.Filters {
		frag, val, err := r.filterFragment(f)
		if err != nil {
			return nil, model.Page{}, err
		}
		where = append(where, frag)
		if f.Op != model.OpIsNull && f.Op != model.OpNotNull {
			args = append(args, val)
		}
	}

	orderBy, customSort, err := r.orderClause(q.Sort)
	if err != nil {
		return nil, model.Page{}, err
	}
	if q.Cursor != "" {
		// The keyset cursor is only meaningful for the default id ordering; a
		// custom sort would make "id > cursor" skip or duplicate rows.
		if customSort {
			return nil, model.Page{}, store.ErrCursorWithSort
		}
		where = append(where, "id > ?")
		args = append(args, q.Cursor)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	sqlText := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d",
		strings.Join(cols, ", "), r.relation(), strings.Join(where, " AND "), orderBy, limit+1)
	r.guard(sqlText)

	rows, err := r.tx.QueryContext(ctx, r.dia.Rebind(sqlText), args...)
	if err != nil {
		return nil, model.Page{}, err
	}
	defer rows.Close()

	var out []model.Record
	for rows.Next() {
		st, err := newScanState(r.desc, cols)
		if err != nil {
			return nil, model.Page{}, err
		}
		if err := rows.Scan(st.dests...); err != nil {
			return nil, model.Page{}, err
		}
		out = append(out, st.record())
	}
	if err := rows.Err(); err != nil {
		return nil, model.Page{}, err
	}

	page := model.Page{}
	if len(out) > limit {
		out = out[:limit]
		page.HasMore = true
		if !customSort {
			page.Cursor = out[len(out)-1].String(model.ColID)
		}
	}
	return out, page, nil
}

// Update modifies a row, enforcing optimistic concurrency. The record must
// carry id and version.
func (r *genericRepo) Update(ctx context.Context, in model.Record) (model.Record, error) {
	return r.updateAt(ctx, in, r.clock.Now())
}

func (r *genericRepo) UpdateAtTransactionTime(
	ctx context.Context,
	in model.Record,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	now, err := r.observedTransactionStamp()
	if err != nil {
		return nil, err
	}
	return r.updateAt(ctx, in, now)
}

func (r *genericRepo) updateAt(
	ctx context.Context,
	in model.Record,
	now model.Timestamp,
) (model.Record, error) {
	if r.readOnly {
		return nil, store.ErrReadOnly
	}
	if r.desc.AppendOnly {
		return nil, store.ErrAppendOnly
	}
	id := model.ID(in.String(model.ColID))
	version := in.Int(model.ColVersion)
	if id.IsZero() {
		return nil, store.ErrNotFound
	}
	set := make([]string, 0, len(r.desc.Fields)+2)
	args := make([]any, 0, len(r.desc.Fields)+5)
	for _, f := range r.desc.Fields {
		set = append(set, f.Name+" = ?")
		args = append(args, redactField(f, in[f.Name]))
	}
	set = append(set, "updated_at = ?", "version = version + 1")
	args = append(args, now.String())
	args = append(args, id.String(), r.tenant.String(), version)

	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ? AND tenant_id = ? AND version = ?%s",
		r.relation(), strings.Join(set, ", "), r.softDeleteClause())
	r.guard(q)
	res, err := r.tx.ExecContext(ctx, r.dia.Rebind(q), args...)
	if err != nil {
		return nil, mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Distinguish a missing/other-tenant row from a stale version.
		if _, gerr := r.Get(ctx, id); errors.Is(gerr, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, store.ErrConflict
	}
	if err := r.maybeAudit(ctx, "update", id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Delete soft-deletes (or hard-deletes) a row in the pinned tenant.
func (r *genericRepo) Delete(ctx context.Context, id model.ID) error {
	if r.readOnly {
		return store.ErrReadOnly
	}
	if r.desc.AppendOnly {
		return store.ErrAppendOnly
	}
	var q string
	var args []any
	if r.desc.SoftDelete {
		now := r.clock.Now()
		q = fmt.Sprintf(
			"UPDATE %s SET deleted_at = ?, updated_at = ?, version = version + 1"+
				" WHERE id = ? AND tenant_id = ? AND deleted_at IS NULL",
			r.relation(),
		)
		args = []any{now.String(), now.String(), id.String(), r.tenant.String()}
	} else {
		q = fmt.Sprintf("DELETE FROM %s WHERE id = ? AND tenant_id = ?", r.relation())
		args = []any{id.String(), r.tenant.String()}
	}
	r.guard(q)
	res, err := r.tx.ExecContext(ctx, r.dia.Rebind(q), args...)
	if err != nil {
		return mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return r.maybeAudit(ctx, "delete", id)
}

func (r *genericRepo) observedTransactionStamp() (model.Timestamp, error) {
	if r.transactionStamp != nil {
		if now, observed := r.transactionStamp(); observed {
			return now, nil
		}
	}
	return model.Timestamp{}, store.ErrTransactionTimeNotObserved
}

// softDeleteClause restricts reads/updates to live rows for soft-deletable
// entities.
func (r *genericRepo) softDeleteClause() string {
	if r.desc.SoftDelete {
		return " AND deleted_at IS NULL"
	}
	return ""
}

func (r *genericRepo) relation() string {
	if r.engineQualified {
		return directoryWriterRelation(r.dia, r.desc.Table)
	}
	return r.desc.Table
}

// filterFragment renders one validated filter predicate and returns its bound
// value. An unknown column or operator is rejected (column-name injection
// guard).
func (r *genericRepo) filterFragment(f model.Filter) (string, any, error) {
	kind, ok := r.desc.KindOfColumn(f.Column)
	if !ok {
		return "", nil, fmt.Errorf("%w: unknown filter column %q", store.ErrUnknownEntity, f.Column)
	}
	// OpEqOrUnset renders "= ? OR IS NULL", plus "OR = ''" for a text column,
	// because an unset ID reaches TEXT storage as the empty string while a
	// nullable UUID column stores NULL (codec_helpers encOptID). Rendering both
	// keeps the predicate correct across the two encodings a lineage column may
	// legitimately use, and it stays in SQL so the keyset page stays honest.
	if f.Op == model.OpEqOrUnset {
		frag := "(" + f.Column + " = ? OR " + f.Column + " IS NULL"
		if kind == model.KindText {
			frag += " OR " + f.Column + " = ''"
		}
		return frag + ")", f.Value, nil
	}
	if f.Op == model.OpIsNull {
		return f.Column + " IS NULL", nil, nil
	}
	if f.Op == model.OpNotNull {
		return f.Column + " IS NOT NULL", nil, nil
	}
	var op string
	switch f.Op {
	case model.OpEq:
		op = "="
	case model.OpNe:
		op = "<>"
	case model.OpLt:
		op = "<"
	case model.OpLte:
		op = "<="
	case model.OpGt:
		op = ">"
	case model.OpGte:
		op = ">="
	case model.OpLike:
		op = "LIKE ? ESCAPE '\\'"
	default:
		return "", nil, fmt.Errorf("invalid filter operator %q", f.Op)
	}
	if f.Op == model.OpLike {
		return f.Column + " " + op, f.Value, nil
	}
	return f.Column + " " + op + " ?", f.Value, nil
}

// orderClause renders the ORDER BY, always ending with id as the stable
// tiebreaker. customSort reports whether the caller supplied any sort term
// (which disables the id keyset cursor).
func (r *genericRepo) orderClause(sorts []model.Sort) (string, bool, error) {
	var terms []string
	hasExplicitID := false
	for _, s := range sorts {
		if _, ok := r.desc.KindOfColumn(s.Column); !ok {
			return "", false, fmt.Errorf("%w: unknown sort column %q", store.ErrUnknownEntity, s.Column)
		}
		dir := "ASC"
		if s.Desc {
			dir = "DESC"
		}
		terms = append(terms, s.Column+" "+dir)
		if s.Column == model.ColID {
			hasExplicitID = true
		}
	}
	custom := len(terms) > 0
	// An explicit id term is already a total, stable tiebreaker. Appending the
	// opposite implicit direction would be redundant semantically and can force a
	// temporary sort instead of an exact reverse index scan.
	if !hasExplicitID {
		terms = append(terms, "id ASC")
	}
	return strings.Join(terms, ", "), custom, nil
}

// guard panics in debug builds if a generated statement against a tenant table
// lacks a tenant predicate — a tripwire for any future code path that forgets
// to scope. It is a no-op in production builds.
func (r *genericRepo) guard(query string) {
	if r.debug && !strings.Contains(query, "tenant_id") {
		panic("sqlstore: tenant-table statement without tenant predicate: " + query)
	}
}

// maybeAudit emits an audit event for a mutation when the descriptor is Audited.
func (r *genericRepo) maybeAudit(ctx context.Context, verb string, id model.ID) error {
	if !r.desc.Audited || r.audit == nil {
		return nil
	}
	_, err := r.audit.Append(ctx, model.AuditDraft{
		Actor:      model.ActorSystem,
		ActorKind:  model.ActorSystem,
		Action:     string(r.desc.Kind.Name()) + "." + verb,
		TargetKind: r.desc.Kind,
		TargetID:   id,
	})
	return err
}

// redactField enforces FieldSpec.Redact on the write path (docs/SECURITY-HARDENING.md, minimal
// data): a field DECLARED sensitive is never persisted raw. The engine replaces
// the value with a one-way SHA-256 digest — a stable token that still supports
// deduplication/correlation but cannot disclose the original. This is the
// engine-level defense-in-depth backstop: even if a connector/module forgets to
// scrub a Redact field in its handler, the store guarantees the raw value never
// lands. A nil/empty value is left untouched (NULL stays NULL). Only KindText/
// KindBytes carry Redact (the registry rejects it elsewhere), so v is a string or
// []byte. A module that needs a usable, partially-scrubbed value must scrub it in
// its handler and NOT set Redact — Redact means "store only a hash".
func redactField(f model.FieldSpec, v any) any {
	if !f.Redact {
		return v
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return x
		}
		sum := sha256.Sum256([]byte(x))
		return "sha256:" + hex.EncodeToString(sum[:])
	case []byte:
		if len(x) == 0 {
			return x
		}
		sum := sha256.Sum256(x)
		return sum[:]
	default:
		return v // nil or NULL: nothing to redact
	}
}

// placeholders returns "?, ?, …" with n placeholders.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// mapWriteErr maps a driver write error to a store sentinel where recognizable
// (a unique-constraint violation becomes ErrConflict; a backend-availability
// failure becomes ErrStoreUnavailable), else returns it as-is.
func mapWriteErr(err error) error {
	if err == nil {
		return nil
	}
	// Availability first: a lost connection is never a constraint, and
	// its text must not fall through the substring matches below. Multi-%w so
	// the ORIGINAL chain (driver.ErrBadConn, net.Error, context deadlines) stays
	// matchable with errors.Is — existing consumers depend on it (round-3 item 3).
	if isBackendUnavailable(err) {
		return fmt.Errorf("%w: %w", store.ErrStoreUnavailable, err)
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate"):
		return fmt.Errorf("%w: %v", store.ErrConflict, err)
	case strings.Contains(msg, "append-only"):
		return fmt.Errorf("%w: %v", store.ErrAppendOnly, err)
	case strings.Contains(msg, "tenant scope violation"):
		return fmt.Errorf("%w: %v", store.ErrTenantViolation, err)
	default:
		return err
	}
}

// isBackendUnavailable reports a connection-level failure at the SQL boundary
// (review P2): the database is unreachable, as opposed to it rejecting a
// statement. Covered shapes: database/sql's driver.ErrBadConn, a Postgres
// SQLSTATE class-08 "Connection Exception", and transport errors implementing
// net.Error (dial/reset/timeout, which pgx surfaces as *net.OpError).
// Constraint, serialization and ordinary statement failures deliberately do
// NOT match — they stay write faults.
func isBackendUnavailable(err error) bool {
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "08")
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// wrapUnavailableErr wraps err in store.ErrStoreUnavailable when it is a
// backend-availability failure (the twin of mapWriteErr's first case, applied
// to reads and to sqlStore.Mutate's own SQL returns); any other error passes
// through untouched, and an already-wrapped error is not double-wrapped.
// Multi-%w keeps the original chain matchable (round-3 item 3).
func wrapUnavailableErr(err error) error {
	if err == nil || errors.Is(err, store.ErrStoreUnavailable) || !isBackendUnavailable(err) {
		return err
	}
	return fmt.Errorf("%w: %w", store.ErrStoreUnavailable, err)
}
