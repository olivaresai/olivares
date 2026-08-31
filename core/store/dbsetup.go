// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

// This file holds the PUBLIC data-transfer types for the database-onboarding
// surface (`olivares db check` / `olivares db init`). They live in the public
// store package — not the internal dialect — because the `olivares` binary is a
// SEPARATE module that may not import core/internal: the CLI consumes these
// types, the internal sqlstore implementation produces them, and core/engine
// re-exports the constructors. The implementation (the real pgx connections and
// the privilege SQL) stays internal; only these inert shapes cross the boundary.

// RolePosture is the privilege level of a Postgres connection's role, as probed
// by `olivares db check` BEFORE the engine boots. RLS-safe means: not a superuser
// and not BYPASSRLS — the only posture under which FORCE row-level security is a
// real cross-tenant backstop (docs/SECURITY-HARDENING.md). SQLite has no roles, so it always
// reports RLS-safe. A probe never opens the schema or runs a migration; it only
// reads the connecting role's catalog attributes (or records why it could not
// connect), so it is safe to run against a live database.
type RolePosture struct {
	// Engine is the backend that was probed.
	Engine Engine
	// Reachable is false when the connection or authentication failed; Err then
	// explains. The other fields are only meaningful when Reachable is true.
	Reachable bool
	// Err is the (non-secret) connection/auth failure reason when Reachable is
	// false. It never embeds the DSN's password (the driver's error does not).
	Err string
	// Role is the connecting role name (current_user).
	Role string
	// Superuser is true when the role is a Postgres superuser (bypasses RLS).
	Superuser bool
	// BypassRLS is true when the role has the BYPASSRLS attribute.
	BypassRLS bool
	// ReplicationRole is the connection's session_replication_role. Under
	// "replica" PostgreSQL skips every ORDINARY trigger — which is every
	// trigger-based guard this schema installs — so boot refuses it. It is
	// reported here because this probe claims to predict what boot will do:
	// omitting it made `db check` answer OK for a database Open would reject.
	ReplicationRole string
}

// RLSUnsafe reports whether this role would silently bypass row-level security,
// making the FORCE-RLS tenant backstop inert. Mirrors the internal dialect guard
// so `db check` predicts exactly what boot would refuse.
func (p RolePosture) RLSUnsafe() bool { return p.Superuser || p.BypassRLS }

// TriggersDisabled reports whether this session would silently skip ordinary
// triggers, making every trigger-based guard inert. Mirrors the internal dialect
// guard for the same reason RLSUnsafe does: a preflight that does not model a
// boot refusal is worse than no preflight, because it is believed.
func (p RolePosture) TriggersDisabled() bool {
	switch p.ReplicationRole {
	case "", "origin", "local":
		return false
	default:
		return true
	}
}

// Why renders the privilege reason, matching the boot-guard wording.
func (p RolePosture) Why() string {
	switch {
	case p.Superuser && p.BypassRLS:
		return "a SUPERUSER with BYPASSRLS"
	case p.Superuser:
		return "a SUPERUSER"
	case p.BypassRLS:
		return "a BYPASSRLS role"
	default:
		return "RLS-safe"
	}
}

// PgRole describes one Postgres role `olivares db init` provisions.
type PgRole struct {
	// Name is the role name. It MUST be a plain lower-case SQL identifier
	// ([a-z_][a-z0-9_]*, ≤63 chars); provisioning rejects anything else rather
	// than attempting to quote an arbitrary identifier.
	Name string
	// Password is the login password. An empty password on an EXISTING role keeps
	// the stored password (attributes are still re-asserted); on a NEW role an
	// empty password is rejected (no passwordless login role is created).
	Password string
}

// PgProvisionSpec is the idempotent Postgres provisioning request for
// `olivares db init`. It encodes the least-privilege role model the engine and
// deploy/postgres/01-app-role.sql document: an application role that runs traffic
// (NOSUPERUSER NOBYPASSRLS), optionally a SEPARATE owner role that owns the
// database and runs DDL/migrations (the --owner-dsn role, reachable at last via
// store.Config.OwnerDSN), and an optional cross-tenant admin role (BYPASSRLS,
// NOSUPERUSER) for --admin-dsn.
type PgProvisionSpec struct {
	// Database is the application database name (e.g. "olivares"). Validated as a
	// plain SQL identifier like the role names.
	Database string
	// App is the runtime-traffic role (the --dsn role). Always provisioned.
	App PgRole
	// Owner, when its Name differs from App.Name, is provisioned as a SEPARATE
	// least-privilege role that owns the database and runs DDL — the app role then
	// gets only DML via ALTER DEFAULT PRIVILEGES (the split). When Owner.Name is
	// empty or equals App.Name, the app role owns the database (single-role mode,
	// today's deploy/postgres/01-app-role.sql posture).
	Owner PgRole
	// Admin, when non-nil, provisions the dedicated cross-tenant read role
	// (NOSUPERUSER BYPASSRLS) for --admin-dsn and grants it read-only on the
	// app/owner-owned tables.
	Admin *PgRole
	// SSLMode is the libpq sslmode for the ready-to-use DSN hints in the result
	// (e.g. "verify-full"). It does not affect provisioning, only the printed hints.
	SSLMode string
}

// HasSplitOwner reports whether a distinct owner role is requested.
func (s PgProvisionSpec) HasSplitOwner() bool {
	return s.Owner.Name != "" && s.Owner.Name != s.App.Name
}

// PgProvisionStep is one provisioning statement with a human label. The SQL is
// the form rendered for DISPLAY (`db init --print-sql`): every dynamic identifier
// is already validated/inlined and every password literal appears REDACTED as
// '********'. The executor renders password literals server-side via format('%L')
// from a bound parameter, so a real password never transits a Go-assembled string
// and never appears in this Step.
type PgProvisionStep struct {
	// Label is a short description of what the step does.
	Label string
	// SQL is the redacted statement text (safe to print and log).
	SQL string
	// Secret marks a step whose executed form carries a password literal, so a
	// renderer knows the printed SQL is redacted (purely informational).
	Secret bool
}

// PgProvisionResult reports what `db init` did (or, for a dry run, would do) plus
// ready-to-use DSN hints with the password redacted.
type PgProvisionResult struct {
	// Steps are the provisioning statements in execution order (redacted SQL).
	Steps []PgProvisionStep
	// Executed is true when the steps actually ran; false for --print-sql.
	Executed bool
	// AppPosture / OwnerPosture are the verification probes run AFTER execution
	// (zero value for a dry run): db init reconnects as each provisioned role and
	// confirms the engine will accept it. Nil when not verified.
	AppPosture   *RolePosture
	OwnerPosture *RolePosture
	AdminPosture *RolePosture
	// AppDSNHint / OwnerDSNHint / AdminDSNHint are ready-to-use, PASSWORD-FREE DSNs
	// (host/port/sslmode taken from the superuser DSN) for --dsn / --owner-dsn /
	// --admin-dsn. They omit the password on purpose: store it in a 0600 file and
	// reference it as --dsn=file:<path>. OwnerDSNHint/AdminDSNHint are empty unless
	// that role was provisioned.
	AppDSNHint   string
	OwnerDSNHint string
	AdminDSNHint string
}

// SafeIdentPattern is the regular expression a Postgres role or database name must
// match before this codebase will interpolate it into DDL. Postgres cannot BIND an
// identifier, so the guard is what makes direct interpolation safe: nothing outside
// [a-z_][a-z0-9_]* (≤63, the NAMEDATALEN-1 limit) can break out of the identifier
// position.
//
// It lives here, in the inert DTO package, so the provisioner (core/internal/store/
// sqlstore) and the test-database helper (core/internal/pgtest) validate against
// ONE pattern. They used to carry separate copies, which meant a test could build a
// name its own copy accepted and provisioning then rejected — or worse, drift the
// other way.
const SafeIdentPattern = `^[a-z_][a-z0-9_]{0,62}$`
