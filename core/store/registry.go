// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"io/fs"

	"github.com/olivaresai/olivares/core/model"
)

// ExtensionRegistry is how a module contributes its own entities to the data
// model without touching the engine (ARCHITECTURE.md, "extensible"). It is handed to
// the module's registration hook during store construction (not to a live
// Scope), and it closes once the store has built its schema.
//
// A module declares schema; it cannot opt out of isolation. The engine — not
// the module — creates the registered table, injects the base columns, and
// attaches the unconditional tenant, audit and append-only guards. So a
// third-party entity is tenant-isolated on exactly the same terms as a core
// entity, by construction.
type ExtensionRegistry interface {
	// Register validates the descriptor against the naming, reserved-column,
	// type and isolation rules and records it. It returns ErrInvalidDescriptor
	// (wrapped with the specific violation) on any breach.
	Register(d model.EntityDescriptor) error
	// Migrations attaches a module's embedded migration filesystem under its
	// namespace, for secondary indexes, data backfills and unregistered helper
	// tables — never for creating a registered entity's table (the engine does
	// that from the descriptor). The fsys is expected to contain per-engine
	// subdirectories matching the active backend.
	Migrations(namespace string, fsys fs.FS) error
	// SchemaInvariants declares, per engine, the trigger objects a module's
	// migrations install and that a live store MUST verify after migrations and
	// before Open returns. A missing invariant fails startup deny-closed, exactly
	// like a missing tenant-isolation guard. The declaration must cover EVERY
	// supported engine: a store running on an engine the module did not declare
	// would verify nothing and still report success.
	//
	// This is deliberately part of the interface rather than an optional
	// capability reached by type assertion. As a capability it FAILED OPEN: a
	// registry that did not implement it turned the declaration into a no-op —
	// registration succeeded, nothing was ever verified, and the module still
	// marked its rollout invariant "declared" and reported itself healthy. Here an
	// omission is a compile error. A recorder that never opens a database (a
	// schema manifest, a test fake) implements it by recording.
	SchemaInvariants(namespace string, byEngine map[Engine][]SchemaTrigger) error

	// WorkspaceInitializer registers module-owned state that must exist for every
	// workspace. The engine invokes it through a confined transaction scope in
	// the SAME transaction that creates the workspace, including CreateOrg's
	// default and the legacy EnsureDefaultWorkspaces backfill. This method is
	// deliberately mandatory: a registry fake that forgets the seam must fail to
	// compile instead of silently dropping required bootstrap state.
	WorkspaceInitializer(i WorkspaceInitializer) error

	// RolloutControl declares a control whose default disposition depends on
	// whether this deployment predates it (see RolloutControl). The engine
	// classifies it ONCE, under the migration lock and before it creates any of
	// the module's tables, and records the answer durably; the module reads the
	// resulting mode and interprets it.
	//
	// It lives here, next to the descriptors, because the fact being classified is
	// about the module's OWN history in this deployment — its witness is one of the
	// tables it registers — while the classification itself must happen at a moment
	// only the engine controls. A module that tried to answer the question later,
	// from its own data, would be re-deriving a fact whose whole value is that it
	// was recorded before the data moved on.
	RolloutControl(c RolloutControl) error
}

// SchemaTrigger identifies a trigger a module migration installs. Table is the
// table the trigger is attached to (not a table the trigger function may write).
// Both values are validated as portable SQL identifiers by the concrete store.
// The schema is supplied by the engine, not the module: a module declares what it
// installs, never where the deployment puts it.
type SchemaTrigger struct {
	Name  string
	Table string
	// DefinitionSHA256, when set, is the hex SHA-256 of the EXACT text the catalog
	// reports for this trigger, and the store refuses to open when the live value
	// differs. It is what distinguishes "a trigger with this name exists" from
	// "the trigger that enforces this invariant exists": a same-name trigger whose
	// body was replaced with a no-op passes every structural check ever devised,
	// because structurally it IS the declared trigger.
	//
	// The value must be captured from a real migrated database, never derived from
	// the migration file: SQLite rewrites the statement it stores (uppercased
	// leading keywords, TEMP and database qualifier dropped, leading whitespace
	// stripped, the run after the first two keywords collapsed). PostgreSQL reports
	// an injectively framed composition of pg_get_triggerdef and
	// pg_get_functiondef, so changing either the trigger or its executable function
	// invalidates the same digest.
	//
	// Changing a body therefore requires a NEW migration that recreates the object
	// and updates this digest in the same commit. Editing an already-applied
	// migration would leave deployed databases unable to open.
	DefinitionSHA256 string
	// Transitions declares the forward-only migration history for replacements of
	// this trigger's catalog definition. Each entry names the migration that moves
	// FROM PreviousDefinitionSHA256. Its destination is the next transition's
	// previous digest, or DefinitionSHA256 for the last entry. This chain lets the
	// store prove both sides of every replacement in the migration transaction,
	// before it writes the migration tracking row.
	Transitions []SchemaTriggerTransition
}

// SchemaTriggerTransition identifies one guarded replacement in a
// SchemaTrigger's definition history. MigrationVersion is the exact module-file
// migration version whose Before hook must observe PreviousDefinitionSHA256.
// The corresponding After digest is derived from the trigger's ordered history:
// the next transition's previous digest, or the trigger's current
// DefinitionSHA256 for the final transition.
type SchemaTriggerTransition struct {
	MigrationVersion         int
	PreviousDefinitionSHA256 string
	// PostgresFunctionIdentity declares that this migration moves the trigger
	// from one zero-argument trigger-function identity to a freshly reserved one.
	// PostgreSQL transitions MUST provide it; shared-function replacement is not
	// supported because PostgreSQL cannot fence a concurrent CREATE TRIGGER caller
	// between a final catalog scan and COMMIT. SQLite has no separately addressable
	// trigger function and therefore MUST leave it nil.
	//
	// Both names are relative to the store's effective schema. They stay structured
	// rather than being parsed from a regprocedure rendering, where quoted dots,
	// parentheses and backslashes are ambiguous.
	PostgresFunctionIdentity *SchemaTriggerFunctionIdentityTransition
}

// SchemaTriggerFunctionIdentityTransition is the PostgreSQL-only identity move
// attached to one trigger-definition transition. PreviousName must name the live
// function referenced by the old trigger; NextName is transactionally reserved by
// the store before the migration SQL runs. The two names must differ.
type SchemaTriggerFunctionIdentityTransition struct {
	PreviousName string
	NextName     string
}
