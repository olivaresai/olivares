// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "github.com/olivaresai/olivares/core/model"

// Engine identifies a supported SQL backend.
type Engine string

// AuditSpoolMode controls the audit writer's response when the declared spool
// budget is exhausted (ADR-0024 Q2).
type AuditSpoolMode string

// AuditBlindingMode selects which metadata-commitment rule NEW appends seal
// under. It governs WRITING only: reading always accepts both rules, because
// every row carries its own discriminator (a stored blind, or its absence).
//
// It exists to decouple WRITING the new rule from DEPLOYING the binary that can
// read it. Flipping a hash rule at deploy time is a one-way door on an
// append-only ledger: the moment one node seals a blinded row, every node still
// running the previous binary reports that row as a hash mismatch — a legitimate
// history denounced as forged — and rolling back stops being possible. With the
// write side behind a gate, an operator upgrades the whole fleet first (every
// node now VERIFIES both rules), and only then turns writing on. Until that
// flip, rollback remains exactly what the upgrade contract promises.
//
// Nodes may disagree without harm, which is what makes the gate safe rather than
// merely cautious: a chain that interleaves blinded and unblinded rows verifies
// end to end, because the rule is a property of each row and not of the reader.
type AuditBlindingMode string

// The supported engines: SQLite for single-node/air-gap, Postgres for
// multi-host/scale with row-level security (ARCHITECTURE.md).
const (
	// EngineSQLite is the embedded, pure-Go, single-node engine.
	EngineSQLite Engine = "sqlite"
	// EnginePostgres is the multi-tenant-at-scale engine with RLS.
	EnginePostgres Engine = "postgres"
)

// SupportedEngines lists every engine a store can be opened against. A module
// declaring a security invariant must cover all of them: the boot self-test looks
// up only the ACTIVE engine, so an omitted engine verifies nothing and reports
// success.
func SupportedEngines() []Engine { return []Engine{EngineSQLite, EnginePostgres} }

// The supported audit spool exhaustion modes (ADR-0024 Q2).
const (
	// AuditSpoolBlock refuses governed writes before their evidence is lost.
	AuditSpoolBlock AuditSpoolMode = "block"
	// AuditBlindingAuto is the empty value: decide at boot from the ledger's own
	// state. It is a named constant so call sites read as a choice rather than as a
	// bare "" — the operator-facing spelling is the literal word "auto", which the
	// command's loader maps onto this value.
	AuditBlindingAuto AuditBlindingMode = ""
	// AuditBlindingOn seals new events with a per-record blinded commitment.
	AuditBlindingOn AuditBlindingMode = "on"
	// AuditBlindingOff seals new events under the legacy unblinded rule, so a node
	// still running an older binary can verify them. Reading is unaffected.
	AuditBlindingOff AuditBlindingMode = "off"
)

// The supported audit spool exhaustion modes, continued.
const (
	// AuditSpoolDegrade selects the explicit evidence-drop policy.
	AuditSpoolDegrade AuditSpoolMode = "degrade"
)

// Config configures a Store. Secrets (DSNs) come from the caller; there are no
// default credentials (secure-by-default, docs/SECURITY-HARDENING.md).
type Config struct {
	// Engine selects the backend.
	Engine Engine
	// DSN is the application-role connection string. For SQLite it is a file
	// path or ":memory:"; for Postgres a libpq/pgx URL for the non-owner,
	// non-superuser, no-BYPASSRLS application role.
	DSN string
	// OwnerDSN (Postgres only) is the owner-role URL used for DDL/migrations.
	// Empty falls back to DSN (acceptable for single-role dev setups).
	OwnerDSN string
	// AdminDSN (Postgres only) is a dedicated BYPASSRLS (NOSUPERUSER) role URL used
	// ONLY for genuinely cross-tenant System reads (the org list, multi-tenant
	// checkpoint coverage). It has NO fallback: under FORCE row-level security the
	// app and owner roles do not bypass, so cross-tenant reads on them return an
	// empty set. Empty ⇒ those reads run RLS-scoped on the app pool (correct on
	// SQLite; RLS-limited on Postgres, where boot logs a warning). See
	// deploy/postgres/01-app-role.sql for the role.
	AdminDSN string
	// MaxConns caps the application pool (Postgres). Ignored for SQLite, which
	// is single-writer by design.
	MaxConns int
	// Clock is the time source; nil uses the system UTC clock. Tests inject a
	// deterministic clock.
	Clock model.Clock
	// SignEvent, when set, signs every appended audit event (see AuditEventSigner).
	// The composition root injects the core/audit signer here so the ledger is
	// tamper-evident per event, not only at the periodic checkpoints. nil disables
	// per-event signing.
	SignEvent AuditEventSigner
	// AuditSpoolMaxBytes is the declared logical audit spool budget from ADR-0024
	// Q2. It measures the exact stored event values, not database pages or file
	// size. Zero disables the budget and leaves the guard and accounting inert.
	AuditSpoolMaxBytes int64
	// AuditSpoolOnFull selects the ADR-0024 Q2 exhaustion policy. Empty is the
	// deny-closed default (block); the other accepted values are block and degrade.
	AuditSpoolOnFull AuditSpoolMode
	// AuditMetaBlinding selects the metadata-commitment rule NEW appends seal
	// under (see AuditBlindingMode). Empty is NOT "off": it means "decide at boot"
	// — a ledger with no events yet has nothing to roll back to and no older
	// reader to strand, so it starts blinded; a ledger that already holds events
	// starts unblinded and boot says so out loud, with the one setting that turns
	// it on once the fleet is upgraded.
	AuditMetaBlinding AuditBlindingMode
	// Debug enables extra in-process invariants (e.g. the SQLite statement
	// guard) at a small cost; off in production.
	Debug bool
	// AllowPrivilegedRole opts OUT of the Postgres RLS-bypass boot guard. By
	// default (false) the store REFUSES to open when the connecting Postgres role
	// is a superuser or has BYPASSRLS, because such a role silently bypasses all
	// row-level security and leaves only the application-layer tenant predicate
	// between tenants (docs/SECURITY-HARDENING.md, §4 — the access-graph is the most sensitive
	// asset). Set true only for an explicitly single-tenant or throwaway/dev
	// deployment where the operator accepts that the FORCE-RLS backstop is inert.
	AllowPrivilegedRole bool
	// GuardEventFence declares this deployment's posture toward the deny-closed
	// event-trigger fence that refuses the DDL which would remove an append-only
	// guard. Empty is GuardEventFenceVerify.
	GuardEventFence GuardEventFencePolicy
}

// GuardEventFencePolicy is the operator's declared posture toward the deny-closed
// event-trigger fence.
//
// The fence is a SUPERUSER deployment artifact, not something the engine installs:
// measured on 15.18/16.14/17.10/18.4, `CREATE EVENT TRIGGER` refuses even the database
// owner when that owner is NOSUPERUSER, and every role this product uses is NOSUPERUSER.
// So the engine renders the DDL and verifies the result, and this setting says what the
// verification's answer is allowed to mean for the boot.
//
// Under every policy, a fence that is INSTALLED AND THEN CHANGED refuses the boot. That is
// not configurable, because it is the same observation the rollout already treats as a
// refusal for a row guard, and it is the one case where "somebody had this and it moved" is
// the whole finding.
type GuardEventFencePolicy string

const (
	// GuardEventFenceVerify is the default: project the fence, report which of the four
	// answers is true, and refuse only on DIVERGENT. An ABSENT fence is loud but not
	// fatal — no deployment can have installed it before this edition existed, so
	// refusing would make the fence impossible to adopt.
	GuardEventFenceVerify GuardEventFencePolicy = "verify"
	// GuardEventFenceRequired additionally refuses to serve when the fence is ABSENT or
	// UNVERIFIED. For a deployment that has applied the DDL and wants the boot to say so:
	// a required control that cannot be read is not a control.
	GuardEventFenceRequired GuardEventFencePolicy = "required"
	// GuardEventFenceOff states nothing about the fence. It exists so an operator can silence
	// the check deliberately rather than by ignoring a warning — and a boot under it
	// reports UNVERIFIED, never "installed".
	GuardEventFenceOff GuardEventFencePolicy = "off"
)

// GuardEventFencePolicies lists every accepted value, in declaration order, so a validator and a
// help string cannot disagree about the vocabulary.
func GuardEventFencePolicies() []GuardEventFencePolicy {
	return []GuardEventFencePolicy{GuardEventFenceVerify, GuardEventFenceRequired, GuardEventFenceOff}
}

// Resolve maps the empty value onto the default. It is a method rather than a branch at each
// call site so "empty means verify" is stated once.
func (p GuardEventFencePolicy) Resolve() GuardEventFencePolicy {
	if p == "" {
		return GuardEventFenceVerify
	}
	return p
}

// Valid reports whether p is a value this build understands. An unknown policy must be
// refused rather than silently resolved to the default: a typo that reads as "verify" would
// be a required fence quietly downgraded.
func (p GuardEventFencePolicy) Valid() bool {
	for _, known := range GuardEventFencePolicies() {
		if p.Resolve() == known {
			return true
		}
	}
	return false
}
