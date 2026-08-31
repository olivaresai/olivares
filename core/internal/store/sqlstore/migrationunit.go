// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// This file is the API of the migration retry runner: the phase a failure
// happened in, what the unit meant to do, the state it found before touching
// anything, the locks it will need, how a failure is classified, and how an
// interrupted unit is reconciled on the next attempt.
//
// These types come BEFORE the runner rather than after it, and that ordering was
// a correction. Writing the runner first and classifying failures later produces
// a runner whose retry decisions are implicit — and an implicit retry decision on
// a schema change is how a committed unit gets applied twice.

// unitPhase names where inside a retry unit something happened. The same
// SQLSTATE means different things in different phases — 57014 is an exhausted
// execution budget in Execute and a canceled caller anywhere — so a decision
// taken without the phase is a coin flip.
type unitPhase string

const (
	// phaseCoordination is the cluster-wide advisory lock that serializes
	// migrating nodes. It never blocks: acquisition polls with pg_try_advisory_lock
	// so the connection stays free to attribute the holder between attempts.
	phaseCoordination unitPhase = "coordination"
	// phaseAcquire takes every lock the unit will need, in a declared total order,
	// each statement clamped to the remaining budget.
	phaseAcquire unitPhase = "acquire"
	// phaseExecute runs the rest of the unit's DDL under the locks already held.
	// It must request no new lock on any relation outside the plan.
	phaseExecute unitPhase = "execute"
	// phaseReceipt writes the receipt in the SAME transaction as the work it
	// attributes. A receipt in its own transaction would be a claim, not evidence.
	phaseReceipt unitPhase = "receipt"
	// phaseCommit is the COMMIT itself, and it is a phase of its own because it is
	// the only one whose failure does not tell you whether it failed.
	//
	// Everywhere else an error means the statement did not take effect. A COMMIT that
	// errors may have been applied on the server with the acknowledgement lost on the
	// way back, so the two possible retries — "run it again" and "skip it, it is
	// done" — are both capable of corrupting the ledger. Folding this into Receipt
	// made that distinction unrepresentable.
	phaseCommit unitPhase = "commit"
)

// unitIntent is what the unit means to do to the target object. It is part of
// reconciliation input because the same observed state means different things
// under different intents: a canonical guard already present is success for an
// adoption and a failed postcondition for a creation.
type unitIntent string

const (
	// intentCreateGuard emits a guard on an object that had none.
	intentCreateGuard unitIntent = "create-guard"
	// intentAdoptLegacy takes an existing, exactly-canonical legacy guard under
	// management without altering it.
	intentAdoptLegacy unitIntent = "adopt-legacy"
	// intentTransitionLegacyOToA promotes an adopted guard from ORIGIN to ALWAYS.
	//
	// This is the ordinary, authorized rollout step and it needs its own intent
	// because it is the one transition that is legitimate to perform on an object
	// that already carries a canonical guard. Without it, an O -> A change is
	// indistinguishable from an unauthorized mutation of an adopted object, and
	// reconciliation has to choose between rejecting the rollout and blessing
	// arbitrary edits.
	//
	// The direction is one-way and load-bearing. Under logical replication the
	// difference is not cosmetic: certified on 15.18, a guard left at 'O' let a
	// publisher UPDATE apply on the subscriber with ZERO errors — the evidence
	// mutated silently — while 'A' preserved the row and raised 3 apply errors.
	intentTransitionLegacyOToA unitIntent = "transition-legacy-o-to-a"
	// intentRepair converges an observed object back to canonical. It is reachable
	// ONLY from the sanctioned repair path, never from boot, and only after the
	// durable capture that records what is about to be overwritten.
	intentRepair unitIntent = "repair"
)

// prestate is the projection taken BEFORE the unit's first reference to the
// target relation.
//
// THE ORIGINAL REASON NO LONGER APPLIES, and the replacement is worth stating because it
// is a different reason. This existed because the acquisition statement used to be allowed
// to mutate — an `ALTER TABLE ... ENABLE ALWAYS TRIGGER` sentinel, which is also the O -> A
// transition, so after it ran "the guard is ALWAYS" was true whether it already was or the
// unit had just made it so. A statement like that destroys the evidence of its own
// precondition. lockPlan.validate() now refuses anything but the inert generated
// `LOCK TABLE`, so acquisition cannot do that any more.
//
// It is still projected first, for two reasons that survive: the pre-lock reading is what
// lets the runner refuse an unauthorized unit WITHOUT taking locks at all, and it is what
// the locked re-read is compared against to detect that the world moved in between. The
// authoritative reading is the locked one — see attempt().
type prestate struct {
	// TargetExists distinguishes a relation that is absent from one that exists without its
	// guard (a half-finished upgrade, or a guard somebody removed).
	//
	// It does NOT by itself reject the second case, and an earlier version of this comment
	// said it did. What the two are actually used for is intent-specific and lives in
	// expectedEnableState: creation requires that no guard be present and does not consult
	// TargetExists; repair requires the relation to exist. Describing the field as carrying
	// a refusal it does not carry is the kind of claim that gets relied on later.
	TargetExists bool
	// GuardPresent is whether the canonical guard object was found at all.
	GuardPresent bool
	// GuardEnableState is the exact tgenabled character observed ('O', 'A', 'D',
	// 'R'). Never normalised to a boolean: "O or A" unclassified was the defect in
	// the verifier that predates this contract.
	GuardEnableState string
	// GuardMatchesCanonical is whether the guard found is byte-for-byte the one the
	// manifest declares, rather than merely a guard with the expected name.
	//
	// Reconciliation cannot work without it. "The object is canonical now" and "the
	// object was already canonical before this unit ran" have identical projections
	// and opposite correct answers, and only the prestate can tell them apart.
	GuardMatchesCanonical bool
	// ReceiptPresent is whether a receipt already attributes this unit.
	//
	// Its ABSENCE is not an anomaly on a fresh database. Core tables are created
	// by the early migrations and the receipt table arrives later, so a canonical
	// object with no receipt is the normal state of a freshly created install and
	// the ordinary entry point of the adoption path.
	ReceiptPresent bool
	// Epoch binds this projection to the manifest revision that authorized the
	// unit. A prestate read under one epoch cannot justify a transition declared
	// under another.
	Epoch int64
	// The rest of the DURABLE BINDING: which rollout, which encoding, which edition and
	// which entry authorized this unit.
	//
	// Epoch alone was not enough, and the gap was not theoretical. Two editions can share
	// an epoch and differ in their canonical bytes — that is precisely
	// ErrGuardManifestDrift — so a receipt compared on the epoch alone would let one
	// edition's approval ratify another edition's change. The whole tuple is compared, with
	// the epoch as its FIRST member rather than as a substitute for the others.
	//
	// The digests are lower-case hex rather than [32]byte so the struct stays comparable
	// with == and printable in a diagnostic, and so the zero value is an empty string —
	// distinguishable from a digest of 32 zero bytes, which is what an uninitialised column
	// or a failed scan would otherwise look like.
	RolloutID        string
	ManifestFormat   int64
	CodeSHA256       string
	RetainedRevision int64
	RetainedSHA256   string
	SpecSHA256       string
	DefinitionSHA256 string
}

// lockPlan enumerates every relation a unit will touch and the mode it needs,
// so acquisition is generated from a declaration instead of being scattered
// through the statements that happen to need it.
//
// Two properties of PostgreSQL make the declaration necessary rather than tidy:
//
//   - LOCK TABLE with several relations takes them ONE AT A TIME, so lock_timeout
//     restarts per relation and a single multi-relation statement can overrun the
//     budget by a factor of its length. Each relation therefore gets its own
//     statement with the remaining budget recomputed before it.
//   - A unit must take the STRONGEST mode it will need FIRST. Escalating mode
//     inside a transaction is a documented deadlock recipe, and DROP TRIGGER
//     escalates to ACCESS EXCLUSIVE.
type lockPlan struct {
	// Metadata are the engine's own bookkeeping relations, in a declared total
	// order that is a common prefix of EVERY unit. A common prefix cannot form a
	// cycle between units, and production writers never touch these.
	//
	// ROW EXCLUSIVE is deliberate and is the only mode available: on a self-revoked
	// append-only table every LOCK TABLE mode above ROW EXCLUSIVE requires UPDATE,
	// DELETE or TRUNCATE and fails 42501 — ownership does not exempt. INSERT
	// authorizes ROW EXCLUSIVE, which is exactly the mode these inserts take anyway.
	Metadata []plannedLock
	// Target is the relation the unit changes, locked LAST because it is the one
	// with real concurrent writers, at the strongest mode the whole unit needs.
	Target plannedLock
	// TargetStatement is the single statement that takes the target lock, and it must be
	// EXACTLY the one this plan generates for its own target and mode.
	//
	// It is therefore not opaque SQL, and it is no longer the place a repair puts its DROP.
	// It used to be both, and that was the hole: a `LOCK TABLE` prefix check let a second,
	// mutating statement ride along and run before the precondition was re-read, with both
	// footprint checks passing because it touched only the declared relation at the
	// declared mode. A footprint compares relations and modes; it cannot attribute which
	// fragment of a string did the mutating.
	//
	// The field is kept rather than derived silently because a declaration that is CHECKED
	// beats one that is implicit: the caller states its intent, and a mismatch is refused
	// instead of overridden. The unit's real work — including a repair's DROP — belongs in
	// Execute, under the locks this statement takes.
	TargetStatement string
	// TargetAcquire is the mode the EXPLICIT sentinel takes, when it cannot be Target.Mode.
	//
	// It exists because PostgreSQL forbids what the plan otherwise wants, and the provenance of
	// that claim is worth separating rather than presenting as one measurement.
	//
	// MEASURED HERE, against a real server on the default single-role topology: a plan whose
	// sentinel asked for SHARE ROW EXCLUSIVE on the evidence ledger failed before any unit
	// could run, as the role that OWNS the table —
	//
	//	permission denied for table audit_events (SQLSTATE 42501)
	//
	// DOCUMENTED, and measured mode by mode by an earlier round of this campaign rather than
	// here: `LOCK TABLE` checks privileges PER MODE. ACCESS SHARE and ROW SHARE need SELECT;
	// ROW EXCLUSIVE needs INSERT/UPDATE/DELETE/TRUNCATE; every mode above that needs UPDATE,
	// DELETE or TRUNCATE, with ownership granting no exemption. An append-only table has exactly
	// those three revoked, on purpose — so ROW EXCLUSIVE is the strongest mode an explicit LOCK
	// TABLE can take on a guarded table at all.
	//
	// DDL is different: ALTER TABLE ... ENABLE ALWAYS TRIGGER requires OWNERSHIP, not UPDATE,
	// and acquires its SHARE ROW EXCLUSIVE implicitly. So a transition CAN hold that mode; it
	// just cannot ASK for it up front.
	//
	// nil means the sentinel takes Target.Mode, which is the ordinary case and keeps every
	// existing plan unchanged. When set it must be COVERED BY Target.Mode: the declaration
	// still states the strongest mode the unit ends up holding, so the pre-commit footprint
	// checks the real thing.
	//
	// WHAT THIS ADMITS, stated rather than buried: setting it means the unit ESCALATES on its
	// own target, which this file elsewhere calls a documented deadlock recipe. The exposure is
	// narrower than that phrase suggests, and the MECHANISM is worth getting right — an earlier
	// version of this comment described a snapshot PostgreSQL cannot produce.
	//
	// Two migrating nodes cannot interleave: the coordination advisory lock serializes them. An
	// ordinary writer holds ROW EXCLUSIVE and wants nothing stronger, so it cannot close a
	// cycle. What CAN deadlock is a concurrent operator doing trigger DDL by hand, and the cycle
	// is formed by the WAIT QUEUE rather than by two incompatible modes being held at once
	// (which is impossible — SHARE ROW EXCLUSIVE and ROW EXCLUSIVE conflict):
	//
	//  1. the runner holds ROW EXCLUSIVE on the target;
	//  2. the operator requests SHARE ROW EXCLUSIVE and QUEUES behind it;
	//  3. the runner escalates to SHARE ROW EXCLUSIVE;
	//  4. the lock manager checks the wait queue before the granted set, sees a conflicting
	//     request already waiting, and queues the escalation BEHIND the operator;
	//  5. the operator cannot proceed while the runner still holds ROW EXCLUSIVE.
	//
	// PostgreSQL detects that as 40P01, which classifies retryable — so the outcome is a retry,
	// not corruption. Given the alternative is a boot that cannot take its lock at all, that is
	// the honest trade. What is NOT claimed: absence of cycles with a multi-object operator
	// transaction that takes locks outside this plan's order.
	TargetAcquire *lockMode
}

// acquireMode is the mode the explicit target sentinel takes.
func (p lockPlan) acquireMode() lockMode {
	if p.TargetAcquire != nil {
		return *p.TargetAcquire
	}
	return p.Target.Mode
}

// targetAcquireStatement is the statement the runner issues for the target.
func (p lockPlan) targetAcquireStatement() string {
	return plannedLock{Schema: p.Target.Schema, Name: p.Target.Name, Mode: p.acquireMode()}.lockStatement()
}

// acquisitionPlan is the plan AS ACQUISITION LEAVES IT: the target at the sentinel's mode.
//
// The two footprint checks ask different questions, and one plan cannot answer both. Right
// after acquisition the claim is "the sentinel took what it said it would" — at which point a
// target declared SHARE ROW EXCLUSIVE is legitimately held at ROW EXCLUSIVE, because the DDL
// that escalates it has not run yet. Before the commit the claim is "the unit ended up holding
// exactly what the plan declares". Checking the second question at the first point reports the
// unit as UNDERSTATING its own plan, which is true and useless.
func (p lockPlan) acquisitionPlan() lockPlan {
	out := p
	out.Target.Mode = p.acquireMode()
	out.TargetStatement = out.Target.lockStatement()
	out.TargetAcquire = nil
	return out
}

// lockMode is a PostgreSQL table-level lock mode.
//
// Typed rather than free text because the plan's whole purpose is to be compared:
// against what Execute actually took, and between relations to find the maximum. A
// string that reaches the catalog as "ACCESS EXCLUSIVE" and comes back as
// "AccessExclusiveLock" cannot be compared by accident, which is exactly the kind of
// silent mismatch that makes a verification vacuous.
//
// THE ORDER IS NOT TOTAL, and treating it as one was a real defect here. PostgreSQL
// defines each mode by the SET OF MODES IT CONFLICTS WITH, not by a scalar strength,
// and two of them are genuinely incomparable: SHARE UPDATE EXCLUSIVE conflicts with
// itself and with SHARE, while SHARE permits another SHARE but conflicts with ROW
// EXCLUSIVE. Neither one's conflict set contains the other's. Comparing the iota
// ordinals therefore authorized taking SHARE UPDATE EXCLUSIVE against a plan that
// declared SHARE — a mode the plan does not cover, blessed by an ordering that does
// not exist.
//
// The ordinals are kept ONLY as identity. Every judgement about "is this mode covered
// by that one" goes through covers(), which is derived from the published conflict
// matrix.
type lockMode int

const (
	lockModeAccessShare lockMode = iota
	lockModeRowShare
	lockModeRowExclusive
	lockModeShareUpdateExclusive
	lockModeShare
	lockModeShareRowExclusive
	lockModeExclusive
	lockModeAccessExclusive
)

// lockModeSQL is the spelling LOCK TABLE accepts.
var lockModeSQL = map[lockMode]string{
	lockModeAccessShare:          "ACCESS SHARE",
	lockModeRowShare:             "ROW SHARE",
	lockModeRowExclusive:         "ROW EXCLUSIVE",
	lockModeShareUpdateExclusive: "SHARE UPDATE EXCLUSIVE",
	lockModeShare:                "SHARE",
	lockModeShareRowExclusive:    "SHARE ROW EXCLUSIVE",
	lockModeExclusive:            "EXCLUSIVE",
	lockModeAccessExclusive:      "ACCESS EXCLUSIVE",
}

// lockModeFromCatalog maps pg_locks.mode back to the ordered type.
//
// The catalog spells the same eight modes differently from the grammar — the names
// here are pg_locks values, not LOCK TABLE syntax — which is precisely why the
// comparison needs a type in the middle rather than string equality.
var lockModeFromCatalog = map[string]lockMode{
	"AccessShareLock":          lockModeAccessShare,
	"RowShareLock":             lockModeRowShare,
	"RowExclusiveLock":         lockModeRowExclusive,
	"ShareUpdateExclusiveLock": lockModeShareUpdateExclusive,
	"ShareLock":                lockModeShare,
	"ShareRowExclusiveLock":    lockModeShareRowExclusive,
	"ExclusiveLock":            lockModeExclusive,
	"AccessExclusiveLock":      lockModeAccessExclusive,
}

func (m lockMode) String() string {
	if s, ok := lockModeSQL[m]; ok {
		return s
	}
	return fmt.Sprintf("lockMode(%d)", int(m))
}

// valid reports whether m is one of the eight modes PostgreSQL defines.
//
// Without this the ordinal comparison authorized anything numerically small. A plan
// declaring lockMode(8) validated cleanly and then covered every real mode, because
// every real mode is numerically less than 8 — a footprint check that authorized
// everything while looking like it authorized one thing.
func (m lockMode) valid() bool {
	_, ok := lockModeSQL[m]
	return ok
}

// lockModeConflicts is PostgreSQL's published table-level conflict matrix: for each
// mode, the set of modes it conflicts with.
//
// Transcribed from the official Table-Level Lock Modes table rather than derived from
// an ordering, because there is no ordering to derive it from. It is the ground truth
// for covers() below.
var lockModeConflicts = map[lockMode]map[lockMode]bool{
	lockModeAccessShare: set(lockModeAccessExclusive),
	lockModeRowShare:    set(lockModeExclusive, lockModeAccessExclusive),
	lockModeRowExclusive: set(lockModeShare, lockModeShareRowExclusive,
		lockModeExclusive, lockModeAccessExclusive),
	lockModeShareUpdateExclusive: set(lockModeShareUpdateExclusive, lockModeShare,
		lockModeShareRowExclusive, lockModeExclusive, lockModeAccessExclusive),
	lockModeShare: set(lockModeRowExclusive, lockModeShareUpdateExclusive,
		lockModeShareRowExclusive, lockModeExclusive, lockModeAccessExclusive),
	lockModeShareRowExclusive: set(lockModeRowExclusive, lockModeShareUpdateExclusive,
		lockModeShare, lockModeShareRowExclusive, lockModeExclusive, lockModeAccessExclusive),
	lockModeExclusive: set(lockModeRowShare, lockModeRowExclusive,
		lockModeShareUpdateExclusive, lockModeShare, lockModeShareRowExclusive,
		lockModeExclusive, lockModeAccessExclusive),
	lockModeAccessExclusive: set(lockModeAccessShare, lockModeRowShare,
		lockModeRowExclusive, lockModeShareUpdateExclusive, lockModeShare,
		lockModeShareRowExclusive, lockModeExclusive, lockModeAccessExclusive),
}

func set(ms ...lockMode) map[lockMode]bool {
	out := make(map[lockMode]bool, len(ms))
	for _, m := range ms {
		out[m] = true
	}
	return out
}

// covers reports whether holding declared authorizes having taken held.
//
// The definition is containment of conflict sets: a declared mode covers a taken mode
// when everything the taken mode excludes is already excluded by the declared one.
// That is the only relation that means what the plan needs — "nothing was blocked
// that the declaration did not already say would be blocked" — and unlike the ordinal
// comparison it correctly refuses the incomparable pairs.
func (declared lockMode) covers(held lockMode) bool {
	if !declared.valid() || !held.valid() {
		return false
	}
	if declared == held {
		return true
	}
	dc, hc := lockModeConflicts[declared], lockModeConflicts[held]
	for m := range hc {
		if !dc[m] {
			return false
		}
	}
	return true
}

// plannedLock is one relation the unit declares it will lock, and how strongly.
//
// The name is held UNQUOTED, in its two catalog parts, and the quoting is applied
// only where SQL needs it. That split is not tidiness: the same value is used for two
// incompatible purposes. Interpolating it into LOCK TABLE requires quoting, and
// comparing it against pg_class.relname requires the raw name, because the catalog
// stores identifiers without quotes. One string doing both means either the statement
// breaks on an identifier that needs quoting, or the footprint comparison silently
// never matches and refuses a unit that did exactly what it declared.
//
// This is not hypothetical here: the repository already carries a regression for a
// role whose name contains a dollar-quote tag, because a legal identifier that needed
// quoting stopped a perfectly ordinary deployment from booting.
type plannedLock struct {
	Schema string
	Name   string
	Mode   lockMode
}

// relation is the comparison key: the two catalog parts, unquoted, joined by a
// separator that CANNOT occur inside a PostgreSQL identifier.
//
// A dot was wrong. PostgreSQL identifiers may contain any character but NUL when
// quoted, so "r3.fp"."target" and "r3"."fp.target" both flatten to r3.fp.target —
// two different relations with one identity. The footprint check compares these
// strings, so a collision means a lock on one relation is authorized by a
// declaration of the other.
//
// NUL is the one byte an identifier can never hold, which makes it the only
// separator that is injective by construction rather than by convention.
func (p plannedLock) relation() string { return p.Schema + "\x00" + p.Name }

// displayRelation is the human form, for messages only. Never compared.
func (p plannedLock) displayRelation() string { return quoteIdent(p.Schema) + "." + quoteIdent(p.Name) }

// sqlName is the interpolation form: every part quoted, embedded quotes doubled.
func (p plannedLock) sqlName() string {
	return quoteIdent(p.Schema) + "." + quoteIdent(p.Name)
}

// quoteIdent renders a PostgreSQL identifier safely.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// lockStatement renders the LOCK TABLE for this relation.
//
// ONLY is deliberate: a partitioned parent would otherwise pull in every partition,
// turning one declared relation into an undeclared footprint the size of the table's
// history.
func (p plannedLock) lockStatement() string {
	return "LOCK TABLE ONLY " + p.sqlName() + " IN " + p.Mode.String() + " MODE"
}

func (p plannedLock) String() string { return p.displayRelation() + ":" + p.Mode.String() }

// validate refuses a plan that cannot be executed safely, BEFORE any statement runs.
//
// Three properties, each with a failure it prevents:
//
//   - No duplicate relation. Two entries for one relation mean the second is either
//     redundant or an escalation, and an escalation mid-transaction is the documented
//     deadlock recipe this ordering exists to avoid.
//   - The metadata prefix must be sorted. The common prefix is what stops two units
//     forming a cycle with each other; a prefix that is common but differently
//     ORDERED provides none of that, and the difference is invisible at a glance.
//   - The target is locked last and at the maximum. Anything else means the unit
//     escalates on the relation with real concurrent writers.
func (p lockPlan) validate() error {
	if strings.TrimSpace(p.Target.Schema) == "" || strings.TrimSpace(p.Target.Name) == "" {
		return errors.New("sqlstore: lock plan has no schema-qualified target relation")
	}
	// The enum is CHECKED, not assumed. An out-of-range mode used to validate cleanly
	// and then cover every real mode, because the comparison was numeric and every
	// real mode is numerically smaller — a footprint check that authorized everything
	// while reading as though it authorized one thing.
	// NUL is the separator that makes the identity injective, and PostgreSQL cannot
	// put one in an identifier — but a plannedLock is built in GO, where a string may
	// hold anything. A schema of "\x00oid" would land in the namespace reserved for
	// relations whose catalog row is gone, so the constructor has to refuse it rather
	// than the separator having to be clever.
	for _, part := range []struct{ what, v string }{
		{"schema", p.Target.Schema}, {"name", p.Target.Name},
	} {
		if strings.ContainsRune(part.v, 0) {
			return fmt.Errorf("sqlstore: lock plan target %s contains a NUL byte, which no PostgreSQL identifier can hold and which is the separator this plan's identity depends on",
				part.what)
		}
	}
	if !p.Target.Mode.valid() {
		return fmt.Errorf("sqlstore: lock plan target %s declares the unknown lock mode %d",
			p.Target.displayRelation(), int(p.Target.Mode))
	}
	if strings.TrimSpace(p.TargetStatement) == "" {
		return fmt.Errorf("sqlstore: lock plan for %s has no target statement", p.Target.displayRelation())
	}
	// THE ACQUISITION STATEMENT MUST NOT MUTATE, and a PREFIX CHECK DOES NOT ESTABLISH
	// THAT. Using a state-changing sentinel to take the lock — an ALTER TABLE ... ENABLE
	// ALWAYS, say — destroys the evidence of its own precondition, which is precisely why
	// the prestate had to be projected beforehand and precisely why that projection was
	// racy. With the precondition re-read under the lock, the acquisition has to be inert
	// for that reading to mean anything.
	//
	// Requiring only a `LOCK TABLE` PREFIX left the hole wide open: pgx accepts a simple
	// query containing several statements, so
	// `LOCK TABLE ONLY "public"."t" IN ROW EXCLUSIVE MODE; INSERT INTO "public"."t" ...`
	// validated cleanly and ran BOTH — before the authoritative re-read, and invisibly to
	// the footprint checks, which can compare relations and modes but cannot attribute
	// which fragment of a string did the mutating. Measured:
	//
	//	ROUND9_MULTI_STATEMENT_ACQUIRE|validate=<nil>|run_err=<nil>|hidden_rows=1|receipts=1
	//
	// The hidden row was durable. So the plan no longer accepts opaque SQL as proof of
	// acquisition: the statement must be EXACTLY the one this plan generates for its own
	// target and mode. Scanning for a semicolon would not have been enough either — that
	// is a guess at a SQL parser, and comments, dollar quotes and string literals all
	// contain semicolons legally.
	//
	// The field is kept rather than dropped because an explicit declaration that is
	// CHECKED is worth more than an implicit one: a caller writing the statement out
	// states its intent, and a mismatch is refused rather than silently overridden.
	if p.TargetAcquire != nil {
		if !p.TargetAcquire.valid() {
			return fmt.Errorf("sqlstore: lock plan for %s declares the unknown acquisition mode %d",
				p.Target.displayRelation(), int(*p.TargetAcquire))
		}
		// The declaration still governs. A sentinel mode the DECLARED mode does not cover would
		// mean the plan's pre-commit footprint authorizes less than the acquisition already
		// took, which is the check reading its own weaker claim.
		if !p.Target.Mode.covers(*p.TargetAcquire) {
			return fmt.Errorf("sqlstore: lock plan for %s acquires %s, which the declared %s does not cover",
				p.Target.displayRelation(), *p.TargetAcquire, p.Target.Mode)
		}
	}
	if want := p.targetAcquireStatement(); strings.TrimSpace(p.TargetStatement) != want {
		return fmt.Errorf("sqlstore: lock plan for %s declares the acquisition statement %q, but the only statement it may issue is %q; an acquisition that carries anything else mutates before the precondition it is about to be judged against is even read",
			p.Target.displayRelation(), p.TargetStatement, want)
	}
	seen := make(map[string]bool, len(p.Metadata)+1)
	prev := ""
	for i, m := range p.Metadata {
		rel := m.relation()
		if strings.TrimSpace(m.Schema) == "" || strings.TrimSpace(m.Name) == "" { //nolint:staticcheck // rel computed above for the message
			return fmt.Errorf("sqlstore: lock plan metadata entry %d has no schema-qualified relation", i)
		}
		if strings.ContainsRune(m.Schema, 0) || strings.ContainsRune(m.Name, 0) {
			return fmt.Errorf("sqlstore: lock plan metadata entry %d contains a NUL byte in its identifier", i)
		}
		if !m.Mode.valid() {
			return fmt.Errorf("sqlstore: lock plan metadata entry %d (%s) declares the unknown lock mode %d",
				i, m.displayRelation(), int(m.Mode))
		}
		if seen[rel] {
			return fmt.Errorf("sqlstore: lock plan names %s twice; a second entry is either redundant or an escalation, and escalating inside a transaction deadlocks", rel)
		}
		seen[rel] = true
		if prev != "" && rel < prev {
			return fmt.Errorf("sqlstore: lock plan metadata is not in a total order (%s after %s); a common prefix only prevents cycles between units if every unit takes it in the SAME order", rel, prev)
		}
		prev = rel
		// The target must be the maximum in the COVERS sense, not the numeric one:
		// the ordinals are identity, and two modes can be incomparable.
		if !p.Target.Mode.covers(m.Mode) {
			return fmt.Errorf("sqlstore: lock plan takes %s at %s, which the target %s at %s does not cover, so the unit escalates on its own target",
				m.displayRelation(), m.Mode, p.Target.displayRelation(), p.Target.Mode)
		}
	}
	if seen[p.Target.relation()] {
		return fmt.Errorf("sqlstore: lock plan names the target %s in its metadata prefix too, which locks it before the strongest mode is known", p.Target.displayRelation())
	}
	return nil
}

// declared reports the strongest mode the plan authorizes for rel.
func (p lockPlan) declared(rel string) (lockMode, bool) {
	if rel == p.Target.relation() {
		return p.Target.Mode, true
	}
	for _, m := range p.Metadata {
		if m.relation() == rel {
			return m.Mode, true
		}
	}
	return 0, false
}

// reconcileOutcome is the verdict on a unit whose result is not known, either
// because a previous attempt was interrupted or because a COMMIT returned an
// error that does not say whether it committed.
type reconcileOutcome string

const (
	// outcomeApplied means the unit is done: the object matches canonical AND the
	// receipt attributes it. Retrying would apply it twice.
	outcomeApplied reconcileOutcome = "applied"
	// outcomeNotApplied means nothing of the unit survived. A new transaction may
	// retry it from the start.
	outcomeNotApplied reconcileOutcome = "not-applied"
	// outcomeDivergent means the observed state is neither "done" nor "untouched"
	// — a receipt without its object, an object that does not match canonical, or
	// a transition that is not the one the intent authorized. It is NOT retryable:
	// the gate stays pending with a stable diagnosis and the exit is the explicit
	// repair path.
	outcomeDivergent reconcileOutcome = "divergent"
	// outcomeUnknown means the reconciliation itself could not be completed. It is
	// fail-closed: never retried, never treated as success.
	outcomeUnknown reconcileOutcome = "unknown"
)

// retryDecision is what the runner does with a failure.
type retryDecision int

const (
	// retryNewTransaction reopens the unit in a NEW transaction. Reusing an
	// aborted one fails 25P02 on every subsequent statement.
	retryNewTransaction retryDecision = iota
	// retryAfterReconcile must re-read the receipt AND re-project the object
	// before deciding anything. Reached when a COMMIT's outcome is unknown.
	retryAfterReconcile
	// retryNever is a permanent failure: the gate stays pending with its
	// diagnosis and a human decides.
	retryNever
	// retryPropagate is the caller's own cancellation, which is not the unit's
	// failure and must not be retried or reclassified.
	retryPropagate
	// retryNewSession means the CONNECTION is no longer trustworthy, so a new
	// transaction on it would be a new transaction on a broken session.
	//
	// It is separate from retryNewTransaction because the two remedies are different
	// and neither substitutes for the other: 57P03 says the server will not accept
	// work on this connection right now, and 40003/08007 say a statement's completion
	// is unknown, which leaves the session's state unknown with it. Reusing the
	// session in either case is how a runner turns a recoverable condition into an
	// unexplainable one.
	retryNewSession
)

// PostgreSQL condition names, spelled once. Comparing against the five-character
// code rather than message text is the only stable form: messages are localized
// and reworded between majors.
const (
	sqlStateLockNotAvailable     = "55P03"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateSerializationFailure = "40001"
	sqlStateQueryCanceled        = "57014"
	sqlStateInFailedTransaction  = "25P02"
	sqlStateInsufficientPriv     = "42501"
	sqlStateUndefinedObject      = "42704"
	// sqlStateCompletionUnknown and sqlStateResolutionUnknown are the two codes that
	// answer "did it happen?" with "I do not know". 40003 is raised for a statement
	// whose completion is unknown, 08007 for a transaction whose resolution is. They
	// cannot be permanent verdicts and they cannot be blind retries: both need the
	// database asked what actually happened.
	sqlStateCompletionUnknown = "40003"
	sqlStateResolutionUnknown = "08007"
	// sqlStateAdminShutdown is the operator stopping the server. It is deliberate
	// human action, and a boot that reconnects around it is fighting the person
	// holding the maintenance window.
	sqlStateAdminShutdown = "57P01"
	// sqlStateCannotConnectNow is a server refusing connections while it starts up or
	// recovers. Transient by definition, and specifically NOT a reason to give up.
	sqlStateCannotConnectNow = "57P03"
)

// cancelOrigin says who canceled a statement that came back 57014.
//
// PostgreSQL reports the same code for a statement_timeout, a lock_timeout on some
// paths, a pg_cancel_backend from anywhere on the system, and a client cancellation.
// The message text does distinguish them, and it is the wrong thing to match on: it
// is localized and reworded between majors, so a classifier built on it silently
// changes behavior when someone sets lc_messages.
//
// The origin is therefore DERIVED from what this runner itself did — it knows the
// statement_timeout it armed and how long the statement ran — rather than parsed out
// of prose.
type cancelOrigin int

const (
	// cancelUnknown is the dangerous one and the reason this type exists: somebody
	// canceled our statement and it was not us and not the caller.
	cancelUnknown cancelOrigin = iota
	cancelCaller
	cancelOwnTimeout
)

// unitFailure is everything the classifier needs to decide, gathered at the point of
// failure rather than reconstructed after it.
//
// Armed and Elapsed are what make the 57014 origin decidable. Without them the
// classifier can only assume, and the assumption it used to make — "the caller is not
// canceling, so it must be our own timeout" — silently swept up every
// pg_cancel_backend on the system and retried against it.
type unitFailure struct {
	Phase unitPhase
	Err   error
	// Armed is the statement_timeout that can be SAFELY ATTRIBUTED to the statement that
	// failed. Zero means "none attributable", which is not the same as "none was set".
	//
	// The distinction is load-bearing at the commit boundary. PostgreSQL enables
	// statement_timeout for the whole command and disables it inside finish_xact_command,
	// before CommitTransactionCommand — so over a COMMIT the timeout covers the early part
	// and not the end-of-transaction work, and no client can tell which side a 57014
	// landed on. Reporting the armed value there would claim a ceiling over a span it does
	// not cover; reporting zero says only that this runner cannot vouch for it, which is
	// what classifyCancel needs in order to fall to cancelUnknown rather than retry over
	// somebody's pg_cancel_backend.
	//
	// A multi-statement callback reports zero for the same reason in a different shape:
	// statement_timeout restarts for every statement it issues, so an Elapsed spanning the
	// whole callback vouches for none of them.
	Armed time.Duration
	// Elapsed is how long the statement actually ran before it came back.
	//
	// Measured on the BUDGET's clock, which is injectable. Under a fake clock that
	// does not advance during a statement, Elapsed is zero and a 57014 this runner
	// armed is therefore classified as unknown-origin. That direction is deliberate:
	// unknown-origin is the more conservative verdict, so a test clock can only make
	// the classifier refuse where production would have retried, never the reverse.
	Elapsed time.Duration
}

// cancelToleranceFraction absorbs the gap between the server's clock and ours, as a
// FRACTION of the armed timeout rather than a fixed span.
//
// A fixed 50ms was wrong in the dangerous direction. With a short armed timeout it
// swallowed the whole budget: at Armed=50ms every cancellation satisfied
// Elapsed+50ms >= 50ms, including Elapsed=0 — so an external pg_cancel_backend
// arriving instantly was filed as our own timeout and retried. The tolerance has to
// scale with what it is tolerating.
//
// It also contradicted the neighboring promise that a fake clock, which reports
// Elapsed=0, can only ever produce cancelUnknown.
const cancelToleranceFraction = 10 // one tenth of the armed timeout

// cancelToleranceFloor keeps the fraction usable for very short timeouts without
// letting it swallow them. It is small enough that Elapsed=0 never satisfies the test
// for any armed value above it, which is the property the fixed span lost.
const cancelToleranceFloor = 2 * time.Millisecond

// cancelTolerance is the slack allowed when deciding whether a statement ran out its
// own armed timeout.
func cancelTolerance(armed time.Duration) time.Duration {
	t := armed / cancelToleranceFraction
	if t < cancelToleranceFloor {
		t = cancelToleranceFloor
	}
	// A CEILING, because the floor alone still swallowed short timeouts. At armed=3ms
	// the 2ms floor accepted a cancellation one millisecond in — two thirds early —
	// as "it ran its whole timeout". The tolerance may never be a large fraction of
	// the thing it is tolerating, so it is capped at a quarter.
	if max := armed / 4; t > max {
		t = max
	}
	// AND AN ABSOLUTE CEILING, because a proportion of a long timeout is still a long
	// time. At armed=60s a quarter is fifteen seconds, so an external cancellation
	// arriving three quarters of the way through was filed as our own timeout and
	// retried. What this is compensating for is clock skew and one network roundtrip;
	// neither grows with the timeout.
	if t > cancelToleranceMax {
		t = cancelToleranceMax
	}
	return t
}

// cancelToleranceMax bounds the slack in absolute terms. It covers a slow roundtrip
// and clock skew between two machines, which is what the tolerance is FOR — not a
// share of however long the statement was allowed to run.
const cancelToleranceMax = 250 * time.Millisecond

// commitOutcomeIsAmbiguous reports whether an error returned BY THE COMMIT leaves the
// transaction's fate unknown.
//
// A canceled caller does not settle any of these, and the first version of this
// exception only covered the transport error — so three SQLSTATEs that explicitly mean
// "I do not know" were still being turned into a propagated cancellation, discarding a
// unit whose fate was undetermined.
//
//   - a NON-server error is a commit with no answer at all;
//   - 08007 says the transaction's resolution is unknown, in as many words;
//   - 40003 says a statement's completion is unknown, which at the boundary is the
//     unit's completion;
//   - 57P01 delivered during the commit may have arrived after the server made it
//     durable.
//
// Every other server error at commit IS settled: the protocol says the transaction
// rolled back, and a cancellation does not erase that.
func commitOutcomeIsAmbiguous(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return true
	}
	switch pg.Code {
	case sqlStateResolutionUnknown, sqlStateCompletionUnknown, sqlStateAdminShutdown:
		return true
	default:
		return false
	}
}

// classifyCancel decides who canceled a 57014.
func classifyCancel(parent context.Context, f unitFailure) cancelOrigin {
	if parent.Err() != nil {
		return cancelCaller
	}
	if f.Armed > 0 && f.Elapsed > 0 && f.Elapsed+cancelTolerance(f.Armed) >= f.Armed {
		return cancelOwnTimeout
	}
	// Elapsed == 0 is never our own timeout. A statement that our own timeout killed
	// necessarily ran for the timeout's duration, and a measured zero means either an
	// instant external cancellation or a clock that cannot see the interval — both of
	// which must fall to unknown rather than be retried.
	return cancelUnknown
}

// classifyFailure decides what to do with err, given the phase it happened in.
//
// The caller's context is checked FIRST and wins over any SQLSTATE. PostgreSQL
// reports a statement canceled by the client as 57014, exactly as it reports one
// killed by statement_timeout, so a classifier that read only the code would turn
// an operator's Ctrl-C into a retry loop.
func classifyFailure(ctx context.Context, f unitFailure) (retryDecision, error) {
	err := f.Err
	if err == nil {
		return retryNever, nil
	}
	// THE CALLER'S CANCELLATION WINS — except at the commit boundary, where it does
	// not make the outcome known.
	//
	// Before COMMIT the rule is absolute: an operator who cancels must not provoke
	// retries against a database they are trying to stop touching. But a cancellation
	// that lands while the driver is already sending COMMIT changes nothing about
	// whether the server applied it. database/sql's Tx.Commit wins its state
	// transition and then calls into the driver, and pgx runs the commit on the
	// context BeginTx was given — so "canceled" and "committed" are not exclusive,
	// and treating the cancellation as the whole story discards a unit whose fate is
	// unknown.
	//
	// A SERVER error at commit is still unambiguous, so it keeps propagating: the
	// protocol says the transaction rolled back, and that is knowledge the
	// cancellation does not erase.
	if cerr := ctx.Err(); cerr != nil {
		if f.Phase == phaseCommit && commitOutcomeIsAmbiguous(err) {
			return retryAfterReconcile, fmt.Errorf(
				"%w (during %s; the caller canceled, which does not make an in-flight COMMIT's outcome known): %w",
				cerr, f.Phase, err)
		}
		return retryPropagate, fmt.Errorf("%w (during %s)", cerr, f.Phase)
	}

	// ONLY COMMIT IS THE BOUNDARY, and folding Receipt in with it was wrong.
	//
	// Writing the receipt is an INSERT inside the transaction. If it fails, COMMIT was
	// never called and nothing of the unit reached durable storage — the deferred
	// rollback sees to the rest. There is no ambiguity to reconcile, and routing it to
	// reconciliation invited the matrix to answer "applied" for a unit whose work was
	// about to be rolled back.
	//
	// COMMIT is different in kind: it is the only statement whose failure does not say
	// whether it failed, because the server may have applied it and lost the answer on
	// the way home.
	atCommit := f.Phase == phaseCommit

	// A COMMIT that came back with a SERVER error is not ambiguous either. The
	// protocol guarantees that an error returned for COMMIT means the transaction was
	// rolled back; what is ambiguous is a commit with NO answer, which arrives as a
	// transport error rather than a PgError. Keeping the two apart is what stops a
	// deadlock at commit — where the server definitively aborted — from being treated
	// as an unknown outcome and reconciled instead of simply retried.

	// THIS RUNNER'S OWN REFUSALS ARE NOT UNCERTAIN OUTCOMES, and they must be
	// separated before the generic non-PgError branch below.
	//
	// That branch exists for errors whose effect on the server is unknown — a broken
	// wire, a lost COMMIT acknowledgement — and it routes them to reconciliation. But
	// these three sentinels are decisions this process made locally, before or instead
	// of doing the work, and their effect is known exactly: nothing happened.
	//
	// Letting them through was a real, demonstrated defect. A budget refusal raised
	// after the locks were taken but before Execute went to reconciliation, and a
	// reconciliation that answered "applied" made run() return nil — measured with
	// execute_calls=0 and receipt_calls=0. A unit that never ran reported success.
	if errors.Is(err, ErrMigrationLockBudgetExceeded) ||
		errors.Is(err, ErrMigrationLockFootprint) ||
		errors.Is(err, ErrMigrationUnauthorised) ||
		errors.Is(err, ErrMigrationPostconditionFailed) ||
		errors.Is(err, ErrLockBudgetStalled) {
		return retryNever, err
	}

	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		// Not a server error: a broken connection, a driver error, a COMMIT whose
		// outcome the client never learned. database/sql documents that a failed
		// Commit leaves the transaction's fate undetermined, so this cannot be
		// retried blind — the unit must be reconciled against the server first.
		if !atCommit {
			// Before COMMIT there is nothing to reconcile against: nothing was made
			// durable and the transaction will abort. The connection is the thing in
			// doubt, so the remedy is a new session, not a new transaction on a session
			// that just failed at the transport.
			return retryNewSession, err
		}
		// A COMMIT with no answer. This is ONE ambiguous case — not, as this line used to
		// claim, "THE ambiguous case, and the only one".
		//
		// CORRECTED BY MEASUREMENT, and the correction is narrower than the first wording of
		// this note. What was measured is an UNANSWERED commit produced by terminating the
		// backend from inside a deferred constraint trigger — an ABORT, not a durable commit
		// whose acknowledgement was lost. That fixture yields a PgError with SQLSTATE 57P01:
		//
		//	AMBIGUOUS_COMMIT|is_pgerror=true|sqlstate="57P01"
		//
		// The DECISION is unaffected, which is why this is a comment fix and not a code one:
		// commitOutcomeIsAmbiguous already treats 57P01, 08007 and 40003 as ambiguous
		// alongside the non-server error, so all four reconcile. What was wrong was the
		// sentence claiming ONE ambiguous case, and a reader who trusted it would look for a
		// transport failure that never comes.
		//
		// STILL NO VERIFICADO: the shape returned when a commit was DURABLE and only its
		// acknowledgement was lost. No fixture in this package can control which side of
		// durability the cut falls on, and the wire test says so of itself. See
		// TestPostgresAnUnansweredCommitIsClassifiedAmbiguous.
		return retryAfterReconcile, err
	}

	switch pg.Code {
	case sqlStateLockNotAvailable:
		// Budget exhausted for one acquisition, not the unit's verdict: the
		// remaining deadline decides whether another attempt is allowed.
		return retryNewTransaction, err
	case sqlStateDeadlockDetected, sqlStateSerializationFailure:
		// PostgreSQL aborted one side of a cycle, or a serialisable conflict. Both
		// are retryable, and both require the WHOLE unit again — a deadlock victim
		// has lost every lock it held. This holds AT COMMIT too, and that is not an
		// oversight: the server answered, so it definitively rolled back, and nothing
		// committed. Reconciling here would spend two catalog reads to be told what
		// the SQLSTATE already said.
		return retryNewTransaction, err
	case sqlStateQueryCanceled:
		switch classifyCancel(ctx, f) {
		case cancelOwnTimeout:
			// A budget this unit set on itself. In Execute that is the execution
			// budget; in acquisition it is a statement that exceeded its own clamp.
			return retryNewTransaction, err
		case cancelCaller:
			// Unreachable via ctx.Err() above, but keep the branch honest rather
			// than letting the fallthrough decide it.
			return retryPropagate, err
		default:
			// SOMEBODY ELSE canceled our statement: a pg_cancel_backend from an
			// operator, a supervisor, another tool. Retrying is arguing with a human
			// who is actively intervening, and doing it in a loop against a database
			// they are trying to quiet down.
			if atCommit {
				// ...except at COMMIT, where the unit's fate is the more urgent
				// question. A cancellation delivered while committing does not say
				// which side of the boundary it landed on, so ask the database before
				// deciding anything.
				return retryAfterReconcile, fmt.Errorf(
					"statement canceled from outside this runner during %s: %w", f.Phase, err)
			}
			return retryNever, fmt.Errorf(
				"statement canceled from outside this runner during %s (not the caller, and not a timeout this unit armed): %w",
				f.Phase, err)
		}
	case sqlStateResolutionUnknown:
		// 08007 is literally "transaction resolution unknown". It is not phase-scoped:
		// the server is saying it cannot tell whether the transaction resolved, and
		// that is the definition of a case to reconcile rather than guess. Asking is
		// harmless when nothing was written — the matrix answers not-applied — and it
		// is the only safe answer when something was.
		return retryAfterReconcile, err
	case sqlStateCompletionUnknown:
		// 40003 is about one STATEMENT's completion. Before COMMIT that leaves the
		// session in doubt but nothing durable behind; at COMMIT it is the boundary
		// case again.
		if atCommit {
			return retryAfterReconcile, err
		}
		return retryNewSession, err
	case sqlStateCannotConnectNow:
		// The server is up but not accepting work — starting up, or in recovery.
		// Transient, and specifically not a reason to give up, but the current
		// session is not usable for the retry.
		return retryNewSession, err
	case sqlStateAdminShutdown:
		if atCommit {
			// A shutdown delivered DURING the commit does not say which side of the
			// boundary it landed on — the server may have made the transaction durable
			// before dropping the connection. Deferring to "the operator meant it" is
			// right about the intent and wrong about the ledger.
			//
			// Not hypothetical: a backend terminated from a DEFERRED constraint
			// trigger, which fires inside COMMIT, arrives exactly here as 57P01.
			return retryAfterReconcile, fmt.Errorf(
				"the connection was terminated during %s, so the unit's outcome is unknown: %w", f.Phase, err)
		}
		// Anywhere else it is unambiguous: nothing was committed, and reconnecting
		// around a maintenance window is fighting the person who opened it.
		return retryNever, fmt.Errorf("the server was shut down by an administrator during %s: %w", f.Phase, err)
	case sqlStateInFailedTransaction:
		// The unit kept using a transaction that had already aborted. That is a
		// programming error in the runner, not a condition to retry around: the
		// real failure was the earlier statement and this one is its shadow.
		return retryNever, fmt.Errorf("runner reused an aborted transaction during %s: %w", f.Phase, err)
	default:
		// Everything else — 42501 on a self-revoked table, 42704 for an object the
		// unit assumed existed — is permanent. Retrying a privilege or a missing
		// object just burns the budget and hides the diagnosis.
		return retryNever, err
	}
}

// lockBudget is the acquisition deadline, measured against a MONOTONIC clock and
// held OUTSIDE the database.
//
// It cannot be expressed as a server-side timeout. lock_timeout applies per
// acquisition and restarts for every relation, so a unit that takes four locks
// can wait four times the value an operator configured. The budget is therefore
// tracked here and each individual statement is clamped to whatever is left.
//
// The clock, the sleeper and the jitter source are injected so a test can drive
// the whole retry loop deterministically instead of sleeping through it.
type lockBudget struct {
	deadline time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	// jitter returns a value in [0,1). Backoff without it synchronizes every node
	// that started together, which is precisely the population that contends.
	jitter func() float64
	// after derives the per-roundtrip context. See budgetTimer for why it is a field.
	after budgetTimer
}

// budgetTimer turns "this much of my budget is left" into a context that ends when it does.
//
// IT IS INJECTED BECAUSE PAIRING AN INJECTED CLOCK WITH THE REAL TIMER IS THE DEFECT, and the
// defect had already been measured rather than imagined. b.deadline is measured on b.now, so a
// derived context built with context.WithTimeout runs on REAL time no matter what that clock
// says: a test whose clock stands still still handed every roundtrip a real deadline of `r`.
// Under load the roundtrips exceeded it and the fixture failed claiming the callback was never
// reached — stricter than intended, and pointing at the wrong culprit. A driven clock NARROWED
// that and could not remove it, because the narrowing was never where the real time entered.
//
// Production passes context.WithTimeout, which is exactly right there: production's clock IS
// real time, so the two agree by construction.
type budgetTimer func(parent context.Context, d time.Duration) (context.Context, context.CancelFunc)

// newLockBudget starts a budget of total duration from the injected clock.
//
// CONTRACT ON THE INJECTED PAIR, which is narrower than the types suggest and is
// enforced rather than merely documented: a sleep that returns nil MUST make an
// advance observable through now(). Every fake clock in this package already works
// that way, and the production pair (time.Now with a monotonic reading, sleepCtx on a
// real timer) satisfies it for any positive wait.
//
// A clock too coarse to observe a one-millisecond wait would therefore be rejected
// after a single sample even though it advances on a longer one. That is accepted
// deliberately: tolerating repeated non-advancing samples means tolerating a budget
// that provably cannot expire, and the constructor is unexported with no such caller.
// Widening the tolerance is the wrong trade until one exists.
// THE TIMER DEFAULTS TO context.WithTimeout, which is correct exactly when `now` is real time.
// A caller that drives its own clock AND depends on when a derived context expires must say so
// with withTimer; every other caller wants this default and should not have to write it.
func newLockBudget(total time.Duration, now func() time.Time, sleep func(context.Context, time.Duration) error, jitter func() float64) *lockBudget {
	return &lockBudget{deadline: now().Add(total), now: now, sleep: sleep, jitter: jitter, after: context.WithTimeout}
}

// withTimer replaces the derivation of per-roundtrip contexts, and returns the budget so the
// pairing is written on one line with the clock it belongs to.
//
// It must be called before the budget derives anything, which the constructor makes natural: a
// budget is built and handed on, not reconfigured mid-flight.
func (b *lockBudget) withTimer(after budgetTimer) *lockBudget {
	b.after = after
	return b
}

// backoffFloor is the shortest wait the backoff will ever ask for while the budget
// is still alive.
//
// It exists because full jitter can legitimately return zero, and a zero-length wait
// used to be indistinguishable from "no budget left": the caller read it as the
// deadline being spent and returned a premature timeout, on a budget with minutes
// remaining. A floor keeps the two answers separate — "wait a very short time" is a
// wait, "there is no time to wait" is the deadline.
const backoffFloor = time.Millisecond

// ErrLockBudgetStalled reports a budget that cannot expire.
//
// The clock and sleeper are injected, so a caller can supply a pair where sleeping
// does not advance time. Nothing in the loop would then ever be able to reach the
// deadline and the boot would poll forever, which is the one failure a deadline
// exists to prevent. Failing loudly on the invariant is the only honest answer: a
// bounded wait built on an unbounded clock is not bounded.
var ErrLockBudgetStalled = errors.New("sqlstore: the migration lock budget's clock did not advance across a wait, so its deadline can never be reached")

// remaining is how much of the budget is left, never negative.
func (b *lockBudget) remaining() time.Duration {
	if d := b.deadline.Sub(b.now()); d > 0 {
		return d
	}
	return 0
}

// expired reports whether the budget is spent.
func (b *lockBudget) expired() bool { return b.remaining() == 0 }

// clampPositive caps a per-statement timeout at what the budget has left, and
// reports whether there is any budget at all.
//
// The boolean is the whole point, and it is why this is not a plain min(). These
// durations become PostgreSQL timeout GUCs, where zero means DISABLED — so a spent
// budget silently rendered as `lock_timeout = 0` would remove the limit at exactly
// the moment the deadline was supposed to bite. A caller that gets false must not
// issue the statement at all.
func (b *lockBudget) clampPositive(d time.Duration) (time.Duration, bool) {
	r := b.remaining()
	if r <= 0 {
		return 0, false
	}
	if d > r {
		d = r
	}
	if d <= 0 {
		return 0, false
	}
	return d, true
}

// context derives a context that cannot outlive the budget.
//
// Without it the deadline governs only the code between roundtrips: a query that
// hangs on a wedged connection obeys the caller's context, which may have no
// deadline at all, and the budget is then a suggestion. Every statement issued
// against the budget goes through here.
//
// It is derived from the remaining DURATION rather than from b.deadline, because
// b.deadline is measured on the INJECTED clock. A test clock starting at the Unix
// epoch would otherwise produce a deadline decades in the past and cancel every
// context immediately.
//
// WHICH CLOCK THE DERIVED DEADLINE RUNS ON, and how that stopped being two answers.
//
// This paragraph used to record an open residual: the returned context ran on REAL time
// regardless of the injected clock, so a test whose clock stood still still handed every
// roundtrip a real deadline of `r`. Under enough load the roundtrips exceeded it and the fixture
// failed saying the callback was never reached — stricter than intended, and pointing at the
// wrong culprit. A driven clock NARROWED the sharing and could not end it, because the narrowing
// was never where the real time entered: it entered here, in the derivation.
//
// The derivation is injected now (budgetTimer), so a budget's roundtrip deadline is measured on
// the same clock as its own deadline. Production passes context.WithTimeout and is unchanged in
// every observable way, because production's clock IS real time.
//
// WHAT IS STILL SHARED, stated rather than implied: roundtrips inside ONE derived context drain
// a single deadline between them, and both the acquisition and the work phase issue several
// before either callback is reached. That is the budget doing its job — it is a budget, not a
// per-statement allowance — and it is a different thing from the clock mismatch above.
//
// A spent budget yields an already-EXPIRED context rather than a canceled one, and
// the difference is not pedantic. Canceled and deadline-exceeded are how the caller
// tells "the operator stopped us" from "we ran out of our own time", and a spent
// budget that presented as cancellation was reported as an opaque driver error
// instead of as a coordination timeout naming the holder. Measured: `try migration
// lock: timeout: context already done: context canceled`.
func (b *lockBudget) context(parent context.Context) (context.Context, context.CancelFunc) {
	r := b.remaining()
	if r <= 0 {
		// A NEGATIVE duration, not zero: it has to be unambiguously past whatever rounding the
		// timer applies, and a timer handed exactly zero is a timer being asked to decide.
		r = -time.Nanosecond
	}
	return b.after(parent, r)
}

// unitWorkContext bounds ONE post-lock operation by the smaller of the execution ceiling
// and whatever the budget has left, and reports false when there is nothing left at all.
//
// It exists because the two bounds a unit's work has are not interchangeable, and using
// only one of them left a hole in each direction. statement_timeout governs each
// STATEMENT and restarts for the next, so it cannot bound a callback that issues several
// or that waits on something other than SQL. The transaction's context aborts SQL issued
// on the transaction, but cannot make a Go function return. Only a context handed to the
// callback itself can do that — and it has to carry the SMALLER of the two, because a
// per-operation ceiling that outlives the whole unit's budget is not a ceiling.
//
// The boolean is the same refusal clampPositive makes, for the same reason: a zero
// duration would produce a context that is already dead, and issuing work against it is
// the caller pretending it had time.
func (b *lockBudget) unitWorkContext(parent context.Context) (context.Context, context.CancelFunc, bool) {
	d, ok := b.clampPositive(unitExecutionTimeout)
	if !ok {
		return nil, nil, false
	}
	// THROUGH THE BUDGET'S OWN TIMER, like context above. Injecting it in one derivation and not
	// the other would leave a callback bounded by the host while its acquisition was bounded by
	// the budget — the same mismatch, one level in, and it would make "the budget's clock governs
	// its deadlines" true only of the half somebody happened to look at.
	ctx, cancel := b.after(parent, d)
	return ctx, cancel, true
}

// backoffDelay is the wait before the next attempt: exponential in the attempt
// number, with full jitter, floored so that a zero sample is still a wait.
//
// attempt counts from 1 and the first backoff is `base`, not `base*2`. The earlier
// form doubled once even on the first attempt, which quietly made the configured
// base the second step of the schedule rather than the first.
func (b *lockBudget) backoffDelay(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if j := time.Duration(b.jitter() * float64(d)); j > backoffFloor {
		d = j
	} else {
		d = backoffFloor
	}
	return d
}

// backoff waits before the next attempt, clamped to the remaining budget so the wait
// itself can never push past the deadline. It returns false when there is no time
// left to wait.
//
// The caller must STILL re-check expiry before acting on a true return. Returning
// true means "a wait happened", not "there is budget left": the wait can consume
// exactly the remainder, and an attempt issued on the strength of that return would
// be an attempt made after the deadline.
func (b *lockBudget) backoff(ctx context.Context, attempt int, base, max time.Duration) (bool, error) {
	d, ok := b.clampPositive(b.backoffDelay(attempt, base, max))
	if !ok {
		return false, nil
	}
	before := b.now()
	if err := b.sleep(ctx, d); err != nil {
		return false, err
	}
	if !b.now().After(before) {
		return false, fmt.Errorf("%w (waited %s at attempt %d)", ErrLockBudgetStalled, d, attempt)
	}
	return true, nil
}
