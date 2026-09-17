// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// policy_state.go is the state-only half of the policy repository: it changes
// whether a policy is enabled WITHOUT touching its stored Spec.
//
// WHY ITS OWN STATEMENTS AND NOT THE ORDINARY PATH. genericRepo.updateAt builds
// its SET list from every declared field, and typedRepo.Update feeds it the
// result of encoding a decoded entity. Either one rewrites the spec column from
// whatever the decoder produced, which is exactly the loss this capability
// exists to avoid — and on a row whose Spec is malformed the decode fails, so
// the ordinary path cannot even reach the row an operator most needs to
// disable. The statements here name a fixed column set in which `spec` does not
// appear, so byte preservation is a property of the SQL rather than of a
// careful copy some later edit could break.
//
// WHAT IT DOES NOT DO. It grants nothing. A caller keeps its own permission and
// transactional authority obligations; holding the capability is not permission
// to write. No route, module writer or audit path is connected here.

// policyStateColumns is the exact projection both methods read. `spec` is
// deliberately absent, and the order is the scan order.
var policyStateColumns = []string{
	model.ColID,
	"kind",
	"enabled",
	model.ColVersion,
	model.ColUpdatedAt,
}

var (
	errPolicyStateID        = errors.New("policy state: invalid id")
	errPolicyStateKind      = errors.New("policy state: invalid kind")
	errPolicyStateVersion   = errors.New("policy state: invalid version")
	errPolicyStateEnabled   = errors.New("policy state: invalid enabled")
	errPolicyStateUpdated   = errors.New("policy state: invalid updated_at")
	errPolicyStateOverflow  = errors.New("policy state: expected version cannot be incremented")
	errPolicyStateExpectKnd = errors.New("policy state: expected kind must not be empty")
)

var _ store.PolicyStateWriter = (*policyRepo)(nil)

// GetPolicyState reads the state projection of one row in the pinned tenant.
// It never selects spec, so a malformed document cannot make this fail.
func (r *policyRepo) GetPolicyState(ctx context.Context, id model.ID) (store.PolicyState, error) {
	if err := validatePolicyStateID(id); err != nil {
		return store.PolicyState{}, err
	}
	return r.readPolicyState(ctx, id)
}

// SetPolicyEnabled changes only enabled, version and updated_at.
func (r *policyRepo) SetPolicyEnabled(
	ctx context.Context,
	id model.ID,
	expectedKind string,
	expectedVersion int64,
	enabled bool,
) (store.PolicyState, error) {
	// Refuse before any I/O: a read-only scope has no write to contain, and an
	// invalid argument must not consume a statement.
	if r.g.readOnly {
		return store.PolicyState{}, store.ErrReadOnly
	}
	if err := validatePolicyStateID(id); err != nil {
		return store.PolicyState{}, err
	}
	if strings.TrimSpace(expectedKind) == "" {
		return store.PolicyState{}, errPolicyStateExpectKnd
	}
	if expectedVersion < 1 {
		return store.PolicyState{}, errPolicyStateVersion
	}
	// version + 1 is computed by the engine, so the refusal has to happen here:
	// at MaxInt64 the increment would wrap into a negative version that every
	// later optimistic read would reject without explanation.
	if expectedVersion == math.MaxInt64 {
		return store.PolicyState{}, errPolicyStateOverflow
	}
	// The statement below is built here rather than through updateAt, so it
	// reports itself to the custodial write gate explicitly — after the caller's
	// own argument bugs and before every precondition, so a sealed scope refuses
	// the write rather than the observation it would have needed.
	if err := r.g.noteWrite(id); err != nil {
		return store.PolicyState{}, err
	}

	// The stored updated_at must be the ENGINE's time observed through this exact
	// transaction, not the injected application clock ordinary CRUD uses: a
	// deployment or test may deliberately fix or skew that clock, and a state
	// change recorded against it would not be comparable with anything else the
	// same transaction wrote. The observation belongs to the caller — it calls
	// store.TransactionClock.TransactionNow on this scope — so a missing or
	// failed observation is refused here, BEFORE the UPDATE, rather than quietly
	// falling back. There is no second transaction and no clock fallback.
	now, err := r.g.observedTransactionStamp()
	if err != nil {
		return store.PolicyState{}, err
	}
	q := fmt.Sprintf(
		"UPDATE %s SET enabled = ?, updated_at = ?, version = version + 1"+
			" WHERE id = ? AND tenant_id = ? AND kind = ? AND version = ?%s",
		r.g.relation(), r.g.softDeleteClause())
	r.g.guard(q)
	res, execErr := r.g.tx.ExecContext(ctx, r.g.dia.Rebind(q),
		enabled, now.String(), id.String(), r.g.tenant.String(), expectedKind, expectedVersion)
	if execErr != nil {
		return store.PolicyState{}, mapWriteErr(execErr)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return store.PolicyState{}, err
	}
	if n == 0 {
		return store.PolicyState{}, r.explainNoStateWrite(ctx, id, expectedKind)
	}
	// Re-read the same projection. A failure here is returned rather than
	// swallowed: the row is already changed in this transaction, so the caller
	// must be able to abort the surrounding Mutate. Nothing here commits.
	return r.readPolicyState(ctx, id)
}

// explainNoStateWrite decides why the update matched nothing, reading ONLY the
// state projection. A scan or driver failure is returned as itself: reporting
// "not found" for a database that could not answer would be a lie the caller
// cannot distinguish from an absent row.
func (r *policyRepo) explainNoStateWrite(
	ctx context.Context,
	id model.ID,
	expectedKind string,
) error {
	current, err := r.readPolicyState(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Absent, soft-deleted or another tenant's row.
		return store.ErrNotFound
	case err != nil:
		return err
	}
	if current.Kind != expectedKind {
		// A policy of another kind is NOT FOUND under this capability. Saying
		// "conflict" would confirm that a row with this id exists and would let a
		// caller probe kinds it may not address.
		return store.ErrNotFound
	}
	// Same row, same kind, different version: a genuine optimistic conflict.
	return store.ErrConflict
}

// readPolicyState runs the fixed projection through the pinned transaction.
//
// It scans into NEUTRAL driver values on purpose. database/sql's typed
// destinations convert first and report failures second, and those conversion
// errors quote the offending value: driver.Bool.ConvertValue and the integer
// converter both format the input into their message, and Rows.Scan wraps that
// with the column name without removing it. The policy schema declares ordinary
// INTEGER/TEXT columns with no STRICT mode and no metadata CHECK, so SQLite can
// hold a scalar that does not convert — and a typed scan of that row would
// return part of its stored content in an error string. Taking the raw driver
// value and classifying it here means every refusal is one of the constant
// sentinels below, whatever the cell contains.
func (r *policyRepo) readPolicyState(ctx context.Context, id model.ID) (store.PolicyState, error) {
	raw := make([]any, len(policyStateColumns))
	dests := make([]any, len(policyStateColumns))
	for i := range raw {
		dests[i] = &raw[i]
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE id = ? AND tenant_id = ?%s",
		strings.Join(policyStateColumns, ", "), r.g.relation(), r.g.softDeleteClause())
	r.g.guard(q)
	row := r.g.tx.QueryRowContext(ctx, r.g.dia.Rebind(q), id.String(), r.g.tenant.String())
	if err := row.Scan(dests...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.PolicyState{}, store.ErrNotFound
		}
		// A genuine backend or cancellation failure is returned as itself: an
		// unreachable database must never look like an absent row, and nothing
		// here redacts an error this package did not produce.
		return store.PolicyState{}, err
	}
	return policyStateFromDriverValues(raw)
}

// policyStateFromDriverValues classifies the five projected cells against the
// driver scalar forms this capability actually supports, and refuses everything
// else with a constant-field sentinel. No offending value and no conversion
// error is carried into the result.
//
// The accepted forms are the ones the two supported engines really produce:
// text arrives as string or []byte on either engine; SQLite renders a BOOLEAN
// column as INTEGER, so enabled is bool (PostgreSQL) or int64 0/1 (SQLite);
// version is int64 on both.
func policyStateFromDriverValues(raw []any) (store.PolicyState, error) {
	rawID, ok := policyStateText(raw[0])
	if !ok {
		return store.PolicyState{}, errPolicyStateID
	}
	id, err := model.ParseID(rawID)
	if err != nil || id.IsZero() || id.String() != rawID {
		return store.PolicyState{}, errPolicyStateID
	}
	kind, ok := policyStateText(raw[1])
	if !ok || kind == "" {
		return store.PolicyState{}, errPolicyStateKind
	}
	enabled, ok := policyStateBool(raw[2])
	if !ok {
		return store.PolicyState{}, errPolicyStateEnabled
	}
	version, ok := raw[3].(int64)
	if !ok || version < 1 {
		return store.PolicyState{}, errPolicyStateVersion
	}
	rawUpdated, ok := policyStateText(raw[4])
	if !ok {
		return store.PolicyState{}, errPolicyStateUpdated
	}
	updated, err := model.ParseTimestamp(rawUpdated)
	if err != nil {
		// ParseTimestamp's message would carry the cell, so it is discarded.
		return store.PolicyState{}, errPolicyStateUpdated
	}
	return store.PolicyState{
		ID:        id,
		Kind:      kind,
		Enabled:   enabled,
		Version:   version,
		UpdatedAt: updated,
	}, nil
}

// policyStateText accepts the two shapes a driver uses for a text column.
func policyStateText(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	default:
		return "", false
	}
}

// policyStateBool accepts a real boolean and SQLite's INTEGER rendering of one.
// Any other integer is refused rather than coerced: a column holding 7 is not a
// boolean this capability may interpret.
func policyStateBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case int64:
		switch t {
		case 0:
			return false, true
		case 1:
			return true, true
		}
	}
	return false, false
}

func validatePolicyStateID(id model.ID) error {
	if id.IsZero() {
		return errPolicyStateID
	}
	parsed, err := model.ParseID(id.String())
	if err != nil || parsed != id {
		return errPolicyStateID
	}
	return nil
}
