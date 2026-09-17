// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"fmt"
	"strings"
)

// DRRestoreControlTable is the PostgreSQL restore control: the durable fence that
// says whether a destination may publish a store, held OUTSIDE the payload so it
// survives replacing the payload.
//
// It exists on PostgreSQL ONLY. A SQLite estate's control cannot be a relation in
// the database, because the database is the very file a restore replaces; its
// control is the external sidecar (core/dr/opgate). The only DR relation both
// engines will carry is the product's declaration receipt, which is ordinary
// append-only product data and is NOT this control.
const DRRestoreControlTable = "olv_dr_restore_control_v1"

// DRRestoreControlKey is the singleton key. One row per schema, enforced by the
// primary key plus a CHECK rather than by a partial index, so a shape verifier can
// state "one row and only this key can exist" from pg_constraint alone.
const DRRestoreControlKey = "restore"

// DRCoordinationObjectKind classifies one entry of the closed coordination
// inventory.
type DRCoordinationObjectKind string

const (
	DRCoordinationRelation   DRCoordinationObjectKind = "TABLE"
	DRCoordinationTableData  DRCoordinationObjectKind = "TABLE DATA"
	DRCoordinationConstraint DRCoordinationObjectKind = "CONSTRAINT"
)

// DRCoordinationObject is one exactly-qualified coordination identity.
type DRCoordinationObject struct {
	Schema string
	Name   string
	Kind   DRCoordinationObjectKind
	// Parent is the relation a subordinate belongs to, empty for the relation itself.
	Parent string
}

// String renders the identity the way a dump table of contents names it.
func (o DRCoordinationObject) String() string {
	if o.Parent != "" {
		return string(o.Kind) + " " + o.Schema + "." + o.Parent + " " + o.Name
	}
	return string(o.Kind) + " " + o.Schema + "." + o.Name
}

// DRCoordinationObjects is the CLOSED inventory of operational coordination
// identities: the objects that are never part of a restorable payload and never
// part of a preservation capsule.
//
// ONE inventory, because it is consumed by two different obligations that must not
// drift apart — the exclusion a producer applies and the classification a consumer
// runs over what it received. Two lists would be two things to forget.
//
// It is a LITERAL. Not a prefix rule, not a discovery query, not a name pattern: a
// prefix rule admits whatever an operator happens to name that way, and discovery
// asks the database a question whose answer the database is not entitled to give.
//
// The product's declaration receipts are deliberately absent. They are append-only
// product history, they belong in a backup, and confusing them with this singleton
// operational control is what would make a restore lose the record of the previous
// one.
func DRCoordinationObjects() []DRCoordinationObject {
	objects := []DRCoordinationObject{
		{Schema: EngineSchema, Name: DRRestoreControlTable, Kind: DRCoordinationRelation},
		{Schema: EngineSchema, Name: DRRestoreControlTable, Kind: DRCoordinationTableData},
		{Schema: EngineSchema, Name: DRRestoreControlTable + "_pkey", Kind: DRCoordinationConstraint, Parent: DRRestoreControlTable},
	}
	contract, _ := DRRestoreControlContract(16)
	for _, check := range contract.Checks {
		objects = append(objects, DRCoordinationObject{Schema: EngineSchema, Name: DRRestoreControlTable + "_" + check.Suffix, Kind: DRCoordinationConstraint, Parent: DRRestoreControlTable})
	}
	return objects
}

// DRRestoreControlColumn is one compiled column of the control, in attribute
// order, with the exact catalog type and nullability a shape check compares.
type DRRestoreControlColumn struct {
	Name     string
	Type     string
	NotNull  bool
	Position int
}

// DRRestoreControlColumns is the compiled shape. A relation carrying this name and
// a different shape is REFUSED, never adopted and never altered: adopting it would
// attribute a control nobody in this build wrote to this build.
func DRRestoreControlColumns() []DRRestoreControlColumn {
	cols := []struct {
		name    string
		typ     string
		notNull bool
	}{
		{"control_key", "text", true},
		{"format", "int8", true},
		{"revision", "int8", true},
		{"state", "text", true},
		{"op_id", "text", true},
		{"plan_sha256", "bytea", true},
		{"destination_database", "text", true},
		{"destination_schema", "text", true},
		{"destination_system_identifier", "text", true},
		{"keyset_sha256", "bytea", false},
		{"report_sha256", "bytea", false},
		{"observed_at", "timestamptz", true},
	}
	out := make([]DRRestoreControlColumn, 0, len(cols))
	for i, c := range cols {
		out = append(out, DRRestoreControlColumn{Name: c.name, Type: c.typ, NotNull: c.notNull, Position: i + 1})
	}
	return out
}

// PostgresDRRestoreControlDDL renders the control's CREATE TABLE.
//
// It is a package function rather than a Dialect method ON PURPOSE. Putting it on
// the interface would oblige SQLite to answer, and the only honest SQLite answer is
// "there is no such relation here" — an interface method that one engine implements
// as a lie is how a control ends up believed on an engine that does not have it.
//
// No sequence, no identity column, no serial. That is not style: pg_dump's
// --exclude-table does NOT exclude a sequence owned by an excluded table, so a
// control with a sequence would leak half of itself into every payload the
// exclusion is supposed to keep it out of.
func PostgresDRRestoreControlDDL() string {
	contract, _ := DRRestoreControlContract(16)
	return contract.DDL()
}

// PostgresDRRestoreControlACLStmts renders the control's access posture for ONE
// role: read the fence, never write it.
//
// The grant names an EXACT role and is rendered server-side through format('%I'),
// and the role crosses into SQL once as a dollar-quoted literal whose tag is chosen
// against the value (pgDollarTagNotIn) — the same discipline the append-only revoke
// uses, and for the same measured reason.
//
// It is NEVER granted to PUBLIC, and never to the closed directory-inventory role.
// That is a hard requirement rather than tidiness: the H/G inventory role's posture
// check enumerates every relation in every non-system namespace and asks
// has_column_privilege, which counts table-wide, PUBLIC and inherited grants — so a
// PUBLIC SELECT here would make a correct deployment's inventory posture fail.
//
// The write revoke skips a role that OWNS the relation, because PostgreSQL lets an
// owner revoke from itself and then grant it straight back: on the single-role
// topology the revoke would only disable the one role that has to write the fence,
// and would prove nothing on the split.
func PostgresDRRestoreControlACLStmts(role string) []string {
	if strings.TrimSpace(role) == "" {
		return nil
	}
	rel := EngineSchema + "." + DRRestoreControlTable
	body := fmt.Sprintf(`
DECLARE
  target text := %s;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname OPERATOR(pg_catalog.=) target) THEN
    RETURN;
  END IF;
  EXECUTE pg_catalog.format('GRANT SELECT ON %s TO %%I', target);
END `, pgDollarQuote(role), rel)
	tag := pgDollarTagNotIn(body)
	return []string{
		"DO " + tag + body + tag,
		pgRevokeAllWritesUnlessOwner(DRRestoreControlTable, role),
	}
}
