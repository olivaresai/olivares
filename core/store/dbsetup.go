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

// ConnAuthority is what a Postgres connection actually IS and may DO, answered by
// the server ON THE CONNECTION BEING JUDGED rather than inferred from the DSN that
// opened it. It extends RolePosture with what a pre-flight needs beyond privilege
// attributes: which cluster and database it reached, whether the engine schema is
// there, whether it may create in it, and whether this session can write at all.
//
// It exists because a DSN is a ROUTE, not an identity. Two DSNs differing in host,
// port or sslmode can land on the same role, database and cluster through a proxy,
// a service alias or a connection pooler; two that look alike can land on
// different ones. So a check that compared DSN strings would be comparing the
// operator's typing, not the estate — the same reading the store's boot already
// refuses to make when it resolves the app and owner ROLES instead of diffing
// their URLs.
//
// ⛔ WHAT ONE OF THESE VALUES DOES AND DOES NOT BIND. Every field is read inside
// ONE transaction on ONE pinned connection, so the whole struct describes a single
// real session — that much is guaranteed. It says nothing about the NEXT session
// the same DSN opens: pg_restore and the boot each connect again, and a balancer
// free to route them elsewhere is outside what any probe can bind. Using this to
// gate a later write is therefore sound only under the precondition that every
// backend reachable by that DSN is uniform in role authority, system identity,
// database and writability. That precondition is the caller's to state; binding
// the check to the session that performs the first write is a different design and
// is NOT what this type provides.
type ConnAuthority struct {
	// Posture is the connecting role's privilege posture. When Posture.Reachable
	// is false the connection, the authentication or the posture read failed and
	// NO other field of this struct is meaningful.
	Posture RolePosture
	// Schema is the engine schema SchemaExists and CanCreate are about.
	Schema string
	// SchemaExists reports whether Schema is present in this database. It is
	// separate from CanCreate because "there is no engine schema here" and "you
	// may not create in it" are different operator problems with different
	// remedies, and a single false would name neither.
	SchemaExists bool
	// CanCreate is has_schema_privilege(Schema, 'CREATE') for this connection:
	// whether the ACL grants it CREATE in the engine schema. It is the authority
	// the least-privilege APPLICATION role deliberately does not hold in the
	// owner/app split (deploy/postgres/01-app-role.sql).
	//
	// ⛔ IT IS AN ACL FACT AND NOT A CAPABILITY. A grant is not permission to
	// write: a session under default_transaction_read_only, or any session on a
	// hot standby, holds CREATE in the catalogue and still cannot execute one.
	// MEASURED on PostgreSQL 16.15 — `rls_safe=true schema_exists=true
	// can_create_acl=true transaction_read_only=on` and then
	// `ERROR: cannot execute CREATE TABLE in a read-only transaction`. Anything
	// deciding whether DDL can RUN must read SessionReadOnly and InRecovery too;
	// the two are kept apart so the refusal can name which one bit.
	//
	// It is asked of current_user, the identity PostgreSQL actually checks
	// privileges against. RolePosture already refuses a connection whose
	// current_user differs from its session_user, so the two cannot disagree here.
	CanCreate bool
	// SessionReadOnly is current_setting('transaction_read_only') for this
	// session: true when no statement on it may write, whatever the ACL says. It
	// is inherited from default_transaction_read_only (cluster, database or ROLE
	// scoped) and is forced on for every session on a standby.
	SessionReadOnly bool
	// InRecovery is pg_is_in_recovery(): true on a physical standby. Reported
	// beside SessionReadOnly rather than folded into it because the remedies are
	// opposite — one is a setting to reset, the other is "this is a replica, point
	// the writer at the primary" — and an operator in an outage needs to be told
	// which.
	InRecovery bool
	// Database is current_database(): the database this connection reached.
	Database string
	// SystemIdentifier is pg_control_system().system_identifier — PostgreSQL's own
	// identity for the CLUSTER, generated at initdb and carried by every physical
	// copy of it. This is the field that answers "is this the same estate".
	//
	// Empty means it could not be read, and that is NOT the same as "no match":
	// see SystemIdentifierErr. It is never defaulted, because an unknown identity
	// silently treated as equal is exactly the comparison this field exists to
	// stop.
	//
	// ⚠ WHAT SHARING IT PROVES, EXACTLY — AND WHAT IT CANNOT DECIDE. A physical
	// replica shares it with its primary, and so does any physical CLONE, including
	// one taken months ago and since diverged. It is an identity, NOT a freshness
	// or topology claim: it says "these two connections belong to the same cluster
	// lineage", never "this replica is caught up" or "this is a replica at all".
	//
	// So it cannot separate a cluster from its own standby, and a caller must not
	// read its silence as admission. Whether two connections are on the same LIVE
	// SERVER is a different question with a different instrument —
	// SameServerReport's advisory-lock challenge — and that is the one the engine
	// enforces at Open.
	SystemIdentifier string
	// SystemIdentifierErr is why SystemIdentifier is empty, when it is. The one
	// realistic cause is a hardened catalogue: EXECUTE on pg_control_system() is
	// granted to PUBLIC by initdb — verified against PostgreSQL 16's own
	// system_functions.sql, which revokes 56 functions from PUBLIC and this is not
	// one of them — but an operator may revoke it. A caller must surface this as
	// "identity not verifiable" and name the grant that would fix it; it must
	// never grant anything itself.
	SystemIdentifierErr string
	// InstanceStartedAt is pg_postmaster_start_time(): which RUNNING postmaster
	// answered, not which cluster. It distinguishes a primary from its standby
	// (they started at different instants) and two separately started servers, and
	// it does NOT identify a cluster — two servers started in the same microsecond
	// share it, and one cluster restarted twice does not.
	//
	// It is named for what it is. An earlier version called this field `Cluster`
	// and compared it as though it were the estate's identity; that is the reading
	// SystemIdentifier now carries, and this one is kept only as the finer
	// same-instance discriminator for pools that must be on the same server.
	InstanceStartedAt string
}

// SameServerWitness names one connection to be tested against a holder in the
// live-server challenge.
type SameServerWitness struct {
	// Label is the flag that supplied the DSN, and it is what an error names —
	// never the DSN, which carries a password.
	Label string
	DSN   string
}

// SameServerVerdict is what the challenge answered for one witness.
type SameServerVerdict struct {
	Label string
	// SameServer is true only when the witness was examined AND proved to be on
	// the holder's live server. False is never "probably fine": exactly one of
	// Err, AcquiredHoldersLock or a differing Database says why.
	SameServer bool
	// AcquiredHoldersLock is the PROOF OF A DIFFERENT SERVER. The holder is
	// holding a random advisory key for the life of its transaction; advisory
	// locks are cluster-scoped, so a witness that takes the same key cannot be
	// talking to the same server.
	AcquiredHoldersLock bool
	// Database is current_database() as this witness reported it.
	Database string
	// Err is why the witness could not be examined at all (connection, auth, or a
	// failed statement). A witness that could not be examined is NOT same-server;
	// "I could not look" is never "they match".
	Err string
}

// SameServerReport is the outcome of one live-server challenge.
//
// ⛔ WHY THIS EXISTS AND WHY NOTHING CHEAPER WILL DO. The engine refuses to open
// a store whose pools are not on ONE LIVE SERVER, and it proves that with this
// challenge rather than with anything the servers say about themselves
// (directoryactivation.go, verifyDirectoryActivationDatabaseIdentity). Both
// weaker readings fail on the case that matters: a physical replica reports its
// PRIMARY's system identifier, because pg_basebackup copies it, and a postmaster
// start time is a coincidence away from being shared. Measured — a real
// pg_basebackup standby of a fixture primary reported system_identifier
// 7682585743393632555, exactly its primary's.
//
// So a pre-flight that only compared those values would admit a replica the
// engine then refuses at boot, after the restore had already written. The
// challenge is what makes the pre-flight's verdict the same verdict.
type SameServerReport struct {
	// HolderLabel is the flag naming the connection that held the lock.
	HolderLabel string
	// HolderDatabase is current_database() on the holder.
	HolderDatabase string
	// HolderErr is non-empty when the challenge could not be MOUNTED at all — the
	// holder would not connect, or would not take its own lock. No witness verdict
	// is meaningful then, and the caller must refuse rather than proceed.
	HolderErr string
	// Witnesses is one verdict per supplied witness, in the order given.
	Witnesses []SameServerVerdict
}

// AllSameServer reports whether a challenge has a holder and at least one
// consistent, successful witness. An empty report or an invocation with no
// witnesses has no measured challenge and returns false. A single-pool caller
// can decide that no challenge is required before invoking the probe.
func (r SameServerReport) AllSameServer() bool {
	if r.HolderErr != "" || r.HolderDatabase == "" || len(r.Witnesses) == 0 {
		return false
	}
	for _, w := range r.Witnesses {
		if !w.SameServer || w.Err != "" || w.AcquiredHoldersLock || w.Database != r.HolderDatabase {
			return false
		}
	}
	return true
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
	// InstallDirectoryInventory selects the separate post-migration DBA step.
	// It does not change passwords, memberships, existing role attributes or
	// table-wide grants. Product tables must already exist.
	InstallDirectoryInventory bool
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
	Executed                    bool
	DirectoryInventoryInstalled bool
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
