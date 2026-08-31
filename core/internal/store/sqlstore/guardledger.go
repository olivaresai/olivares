// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardledger.go is the Go side of the guard control plane: appending to the three logs,
// and folding them back into the projection the coordinator decides from.
//
// THERE IS NO MUTABLE STATE ROW, and that is the load-bearing choice. A row saying
// `ready` can be set by anything able to write it; a fold can only reach `ready` through
// an event that had to be appended carrying its predecessor's digest. So `pending` and
// `ready` are not nouns stored somewhere — they are what the events add up to.
//
// The chain is VERIFIED as it is read, not merely stored. Every fold recomputes each
// event's digest from its own body and its predecessor and refuses the stream if one
// disagrees. Storing a hash nobody recomputes is a checksum with no reader: it detects
// nothing, and it reads as though it detects everything.

// Names borrowed once, so this package never repeats a literal the dialect owns.
const (
	guardSchema               = dialect.EngineSchema
	guardBlockMutationFn      = dialect.BlockMutationFn
	guardGateEventsTable      = dialect.GuardGateEventsTable
	guardInventoryEventsTable = dialect.GuardInventoryEventsTable
	guardReceiptsTable        = dialect.GuardReceiptsTable
)

// The guard control plane's named refusals. Each is a distinct condition an operator has
// a distinct move for, which is why they are separate errors rather than one wrapped
// message.
var (
	// ErrGuardControlPlaneBootstrapInconsistent is the all-or-none refusal: the version
	// tracking and the three relations disagree about whether the control plane exists.
	// Recreating them would be the laundering this ledger exists to detect.
	ErrGuardControlPlaneBootstrapInconsistent = errors.New("sqlstore: the guard control plane's bootstrap state is inconsistent")
	// ErrGuardReceiptLedgerUnavailable is a receipt projection that could not read the
	// ledger. It is emphatically NOT an absent receipt: collapsing the two is how a
	// committed unit gets applied twice.
	ErrGuardReceiptLedgerUnavailable = errors.New("sqlstore: the guard receipt ledger could not be read")
	// ErrGuardReceiptConflict is a receipt whose key already exists with DIFFERENT bytes.
	// An identical row is idempotent; a different one means two attempts disagree about
	// what happened, and neither may win silently.
	ErrGuardReceiptConflict = errors.New("sqlstore: a guard receipt already exists for this unit with different bytes")
	// ErrGuardBootstrapFunctionDivergent is a pre-existing olivares_block_mutation whose
	// projection is not the canonical one. The bootstrap reuses an exact function and
	// refuses a lookalike; it never runs CREATE OR REPLACE over one.
	ErrGuardBootstrapFunctionDivergent = errors.New("sqlstore: the existing mutation-blocking function is not the canonical one")
	// ErrGuardManifestDrift is the same epoch with different canonical bytes: somebody
	// changed an active definition without changing the edition that authorizes it.
	ErrGuardManifestDrift = errors.New("sqlstore: the guard manifest drifted from the edition recorded for this epoch")
	// ErrGuardManifestAhead is a database whose recorded edition is NEWER than this
	// binary's. Refused before any DDL: an older binary cannot know which edges a newer
	// edition authorizes.
	ErrGuardManifestAhead = errors.New("sqlstore: the database records a guard edition newer than this binary declares")
	// ErrGuardFormatAhead is a database whose canonical ENCODING this binary does not
	// understand. Distinct from the edition being ahead, because the remedy differs: an
	// unknown format means every digest comparison would be meaningless rather than
	// merely unauthorized.
	ErrGuardFormatAhead = errors.New("sqlstore: the database records a guard manifest format newer than this binary understands")
	// ErrGuardUnsupportedPostgresMajor refuses a major this projection has not been
	// reasoned about on. A field the comparator does not read is a field in which two
	// different objects compare equal.
	ErrGuardUnsupportedPostgresMajor = errors.New("sqlstore: this PostgreSQL major is outside the range the guard manifest's catalog projection covers")
	// ErrGuardGateChainBroken is an event stream whose digests do not recompute, or whose
	// ordinals skip. It is fail-closed: a history that cannot be verified authorizes
	// nothing.
	ErrGuardGateChainBroken = errors.New("sqlstore: the guard gate's event chain does not verify")
	// ErrGuardGateBlocked is a gate whose projection forbids mutation. It carries the
	// durable diagnostic rather than a fresh guess.
	ErrGuardGateBlocked = errors.New("sqlstore: the guard rollout is blocked")
	// ErrGuardInventoryUnsupported is an inventory this edition cannot reason about: a lifecycle
	// kind it has no fold for, a retained pair its own history does not produce, or activations
	// that are not this manifest's.
	//
	// Refusing is the only honest answer. The column's CHECK admits `retain`, `reactivate` and
	// `tombstone` so a later edition needs no migration to write them; an edition without their
	// fold accepting them would be deciding what they mean without implementing it.
	ErrGuardInventoryUnsupported = errors.New("sqlstore: the guard inventory holds history this edition cannot interpret")
	// ErrGuardGateIllegalTransition is a history this engine could not have written: a rollout
	// opened twice, or closed while it was blocked.
	//
	// It is separate from a broken chain because the chain is INTACT — every digest recomputes.
	// What is wrong is the SEQUENCE, and a fold that accepted it would be reasoning from a
	// state machine nothing enforces. Verifying hashes without verifying transitions is
	// checking that somebody wrote the events carefully, not that they were allowed to.
	ErrGuardGateIllegalTransition = errors.New("sqlstore: the guard gate's event sequence is not one this engine can produce")
	// ErrGuardControlPlaneShapeDivergent is a control-plane relation that exists under the
	// right name and is not the right relation.
	//
	// It is separate from the all-or-none bootstrap refusal because the two say different
	// things to an operator: that one means the RECORD and the objects disagree about whether
	// the bootstrap happened, and this one means the objects are there and cannot hold the
	// invariants the ledger argues from — a lost uniqueness, an unlogged table, a policy that
	// hides rows from the fold.
	ErrGuardControlPlaneShapeDivergent = errors.New("sqlstore: a guard control-plane relation is not the declared one")
	// ErrGuardBootstrapReceiptsInvalid is the bootstrap's own attribution missing, duplicated,
	// extra, or claiming something other than what this migration does.
	//
	// The three metadata bootstrap receipts are what the control plane's guards are verified
	// AGAINST; core v7 additionally requires its exact completion/transition witness. Without
	// the metadata receipts the catalog comparison compares the objects with a spec the binary
	// rebuilt from itself, which is self-certification with extra steps.
	ErrGuardBootstrapReceiptsInvalid = errors.New("sqlstore: the guard control plane's bootstrap receipts are not the ones this migration writes")
)

// gateEventKind is one of the seven events the gate's projection is folded from.
type gateEventKind string

const (
	// gateEventPendingOpened opens a rollout: the target edition, the retained pair it
	// was opened against, and the ordered set of unit identities it expects.
	gateEventPendingOpened gateEventKind = "pending-opened"
	// gateEventAttemptStarted is written and COMMITTED before an attempt, so an
	// interrupted process leaves proof that it began.
	gateEventAttemptStarted gateEventKind = "attempt-started"
	// gateEventAttemptJudged carries the AUTHORITATIVE prestate: the one re-read under the
	// target lock, with its canonical bytes and digest. It is written inside the unit's own
	// transaction, before any mutation, so it commits with the work or not at all.
	//
	// The distinction from attempt-started is not bookkeeping. The pre-lock reading can be
	// stale by the time the unit commits — measured, an adopt-legacy unit projected 'O',
	// another session moved the guard, and reconciling against the stale reading declared a
	// perfectly good durable unit divergent. The judged reading is the one every later boot
	// must reason from.
	gateEventAttemptJudged gateEventKind = "attempt-judged"
	// gateEventAttemptFailed is the terminal for a failure, written AFTER the rollback so
	// it survives it.
	gateEventAttemptFailed gateEventKind = "attempt-failed"
	// gateEventVerificationFailed is drift found by verification rather than by an attempt.
	// Under `ready` it produces ready/blocked and does NOT open a repair rollout by itself.
	gateEventVerificationFailed gateEventKind = "verification-failed"
	// gateEventReconciled records a READ-ONLY resolution of an unknown outcome. It is the
	// only thing that can move a unit out of blocked without an operator, and it can do so
	// only because it mutates nothing.
	gateEventReconciled gateEventKind = "reconciled"
	// gateEventReady closes an edition. It is appended by the coordinator only after every
	// expected unit has a correct receipt AND the objects have been re-projected — never
	// from the presence of receipts alone.
	gateEventReady gateEventKind = "ready"
)

// gatePhase is whether the concrete transition is still open or was closed.
type gatePhase string

const (
	gatePhasePending gatePhase = "pending"
	gatePhaseReady   gatePhase = "ready"
)

// gateCondition is what the coordinator knows, and whether it may advance.
//
// It is a SEPARATE axis from the phase, which is the correction the step-2 design makes to
// treating `pending` as authority: "still open" and "safe to act" are different questions,
// and a single word answering both is how a blocked rollout gets retried.
type gateCondition string

const (
	gateConditionClean     gateCondition = "clean"
	gateConditionRetryable gateCondition = "retryable"
	gateConditionBlocked   gateCondition = "blocked"
	gateConditionVerified  gateCondition = "verified"
)

// Retry classes, as recorded durably. They are the runner's own classification spelled in
// the ledger's vocabulary, so a later boot routes on the class rather than on message text.
const (
	guardRetryClassRetryable = "retryable"
	guardRetryClassPermanent = "permanent"
	guardRetryClassUnknown   = "unknown"
)

// Unblock policies. The difference must not depend on a human-readable message.
const (
	guardUnblockNone          = ""
	guardUnblockReadReconcile = "read_reconcile"
	guardUnblockOperator      = "operator"
)

// optDigest is a digest that may be absent, kept comparable with == so a whole row can be
// compared as a value.
type optDigest struct {
	Valid bool
	D     [32]byte
}

func someDigest(d [32]byte) optDigest { return optDigest{Valid: true, D: d} }

func (o optDigest) String() string {
	if !o.Valid {
		return "NULL"
	}
	return hexDigest(o.D)
}

// bytes renders the digest for a bound parameter: nil when absent, so the column is NULL
// rather than 32 zero bytes.
//
// Zero bytes would be the wrong encoding in a way that matters: the CHECK constraints
// accept a 32-byte value, so a zero digest would be stored as a legal predecessor and the
// chain would verify against a hash nobody produced.
func (o optDigest) bytes() any {
	if !o.Valid {
		return nil
	}
	b := make([]byte, len(o.D))
	copy(b, o.D[:])
	return b
}

func (o optDigest) canon(w *canonWriter) {
	if !o.Valid {
		w.opt(optText{})
		return
	}
	w.bytes32(o.D)
}

// digestBytes renders a present digest as a bound parameter.
func digestBytes(d [32]byte) []byte {
	b := make([]byte, len(d))
	copy(b, d[:])
	return b
}

// scanDigest turns a scanned column into a digest, refusing a width the CHECK constraints
// would not have allowed.
func scanDigest(raw []byte, what string) ([32]byte, error) {
	var out [32]byte
	if len(raw) != len(out) {
		return out, fmt.Errorf("sqlstore: %s is %d bytes, want %d", what, len(raw), len(out))
	}
	copy(out[:], raw)
	return out, nil
}

// scanOptDigest turns a nullable column into an optDigest.
func scanOptDigest(raw []byte, what string) (optDigest, error) {
	if raw == nil {
		return optDigest{}, nil
	}
	d, err := scanDigest(raw, what)
	if err != nil {
		return optDigest{}, err
	}
	return someDigest(d), nil
}

// guardDiagnostic is the STRUCTURED half of a durable failure: the fields later decisions
// read.
//
// The human message rides along in Details as a snapshot, and nothing routes on it. That
// separation is the whole point — a message is localized and reworded between releases, so
// a boot that decided from prose would change behavior when somebody improved a sentence.
type guardDiagnostic struct {
	Code          string
	RetryClass    string
	UnblockPolicy string
	SQLState      string
	ExpectedSHA   string
	ObservedSHA   string
	Details       string
}

// fingerprint is the IDENTITY of a failure, and it deliberately excludes everything that
// makes the same failure look new.
//
// Time, backend PID, attempt id, build id and the localized message are all absent: with
// any of them in, a permanent failure would produce a different fingerprint on every boot,
// the unique constraint would never fire, and the ledger would accumulate one row per
// restart for a condition that never changed. What IS in is what makes two failures the
// same failure — the rollout, the unit, the edition, the condition, the class, the
// SQLSTATE and the two hashes that disagreed.
func (d guardDiagnostic) fingerprint(rolloutID, unitID string, epoch int64, codeSHA [32]byte, cond gateCondition) ([32]byte, error) {
	w := newCanonWriter(canonDomainDiagnostic, guardManifestFormat)
	w.str(rolloutID)
	w.str(unitID)
	w.i64(epoch)
	w.bytes32(codeSHA)
	w.str(string(cond))
	w.str(d.Code)
	w.str(d.RetryClass)
	w.str(d.SQLState)
	w.str(d.ExpectedSHA)
	w.str(d.ObservedSHA)
	return w.sum()
}

// empty reports whether there is no diagnostic at all.
func (d guardDiagnostic) empty() bool { return d == guardDiagnostic{} }

// guardCheckpoint is the head and the count of the two logs a `ready` attests.
//
// It is what turns "the prefix that still exists verifies" into "the history is the history that
// was closed". A chain authenticates each row against its predecessor, so the LAST row of a
// stream has nothing behind it: remove it and what remains is a shorter, perfectly valid chain.
// The checkpoint gives that last row a successor in a DIFFERENT stream.
//
// WHAT IT DOES NOT COVER, stated because the gap is the interesting part: the gate's OWN tail. A
// `ready` cannot attest itself, so deleting it leaves a `pending` prefix whose receipts are all
// present — indistinguishable from a boot that crashed after its last unit and before closing.
// The consequence is re-attestation rather than a silent downgrade: the next boot re-reads every
// object, re-verifies every receipt and writes a new `ready`, or refuses. Closing that last gap
// needs an anchor outside this database, which this edition does not have.
type guardCheckpoint struct {
	InventoryHead  [32]byte
	InventoryCount int64
	ReceiptHead    [32]byte
	ReceiptCount   int64
}

// gateEvent is one row of the gate log.
type gateEvent struct {
	RolloutID string
	Kind      gateEventKind
	// UnitID is empty for the two rollout-level events, and a CHECK constraint enforces
	// exactly that correspondence rather than trusting the writer.
	UnitID    string
	AttemptID string
	Intent    unitIntent
	Key       guardKey

	Format           int64
	CodeEpoch        int64
	CodeSHA256       [32]byte
	RetainedRevision int64
	RetainedSHA256   [32]byte

	SpecSHA256       optDigest
	DefinitionSHA256 optDigest
	PrestateSHA256   optDigest
	// PrestatePresent says whether this event carries a reading at all. The two attempt
	// events do; the rollout-level ones do not.
	PrestatePresent bool
	// Prestate is the reading ITSELF, stored field by field rather than only as a digest.
	//
	// A digest cannot be reconstructed from, and reconstruction is exactly what a later boot
	// needs: the prestate that authorized an O -> A transition says 'O' while the catalog now
	// says 'A', so the authority must be re-readable rather than re-derivable. Storing the
	// fields AND the digest makes the reconstruction verifiable — the fold re-hashes what it
	// read and refuses the stream if the two disagree.
	Prestate prestate
	// PrestateBytes is the same reading rendered for a human. Diagnostic: it is NOT the
	// digest's preimage, which is the canonical binary encoding.
	PrestateBytes string

	Phase      gatePhase
	Condition  gateCondition
	Diagnostic guardDiagnostic
	// ExpectedUnits is the ordered set a pending-opened enumerates. Empty elsewhere.
	ExpectedUnits []string
	// Checkpoint is present on a `ready` and nowhere else, which a CHECK constraint enforces.
	Checkpoint        guardCheckpoint
	CheckpointPresent bool

	Actor   string
	BuildID string

	// Assigned by the writer, read by the fold.
	EventOrdinal    int64
	PrevEventSHA256 optDigest
	EventSHA256     [32]byte
	RecordedAt      string
}

// body is everything about the event EXCEPT its position in the chain.
//
// Split out because the chain digest is a function of (body, predecessor): keeping them
// separate is what lets the fold recompute the stored digest instead of trusting it.
func (e gateEvent) body() (*canonWriter, error) {
	w := newCanonWriter(canonDomainEvent, e.Format)
	w.str("gate")
	w.str(e.RolloutID)
	w.str(string(e.Kind))
	w.str(e.UnitID)
	w.str(e.AttemptID)
	w.str(string(e.Intent))
	e.Key.canon(w)
	w.i64(e.CodeEpoch)
	w.bytes32(e.CodeSHA256)
	w.i64(e.RetainedRevision)
	w.bytes32(e.RetainedSHA256)
	e.SpecSHA256.canon(w)
	e.DefinitionSHA256.canon(w)
	e.PrestateSHA256.canon(w)
	w.boolean(e.PrestatePresent)
	if e.PrestatePresent {
		e.Prestate.canon(w)
	}
	w.str(e.PrestateBytes)
	w.str(string(e.Phase))
	w.str(string(e.Condition))
	w.str(e.Diagnostic.Code)
	w.str(e.Diagnostic.RetryClass)
	w.str(e.Diagnostic.UnblockPolicy)
	w.str(e.Diagnostic.SQLState)
	w.str(e.Diagnostic.ExpectedSHA)
	w.str(e.Diagnostic.ObservedSHA)
	w.str(e.Diagnostic.Details)
	w.list(len(e.ExpectedUnits))
	for _, u := range e.ExpectedUnits {
		w.str(u)
	}
	w.boolean(e.CheckpointPresent)
	if e.CheckpointPresent {
		w.bytes32(e.Checkpoint.InventoryHead)
		w.i64(e.Checkpoint.InventoryCount)
		w.bytes32(e.Checkpoint.ReceiptHead)
		w.i64(e.Checkpoint.ReceiptCount)
	}
	w.str(e.Actor)
	w.str(e.BuildID)
	w.str(e.RecordedAt)
	if _, err := w.bytes(); err != nil {
		return nil, err
	}
	return w, nil
}

// chainDigest is the event's identity: its body hashed together with its ordinal and its
// predecessor.
//
// The ordinal is INSIDE the digest. Without it, two identical bodies at different
// positions would hash the same, and a stream could be reordered without any digest
// changing — which is exactly the tampering the chain exists to catch.
func (e gateEvent) chainDigest() ([32]byte, error) {
	body, err := e.body()
	if err != nil {
		return [32]byte{}, err
	}
	raw, err := body.bytes()
	if err != nil {
		return [32]byte{}, err
	}
	w := newCanonWriter(canonDomainEvent, e.Format)
	w.str("gate-chain")
	w.i64(e.EventOrdinal)
	e.PrevEventSHA256.canon(w)
	w.str(string(raw))
	return w.sum()
}

// guardActor is what the engine records as the author of an event it writes itself.
//
// Named rather than blank so an operator-authored repair event is distinguishable from a
// boot-authored one without consulting anything else.
const guardActor = "engine.boot"

// guardBuildID is a best-effort identifier of the binary, for diagnostics only.
//
// It comes from the embedded build info and is EMPTY when the binary was built without
// VCS stamping. Empty is the honest answer there: inventing a value would put a claim in
// the ledger that nothing supports. Nothing routes on it, and it is excluded from every
// diagnostic fingerprint precisely so that rebuilding does not make an old failure look
// new.
func guardBuildID() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + dirty
}

// nowRFC3339 is the wall-clock stamp events carry.
//
// Diagnostic ONLY, and the ledger says so by never comparing it: authority lives in the
// epoch, the digests and the chain. A clock that jumps backwards — a VM restored from a
// snapshot, an NTP correction — must not be able to reorder history, so nothing reads
// this to decide anything.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// gateStreamHead returns the rollout's current ordinal and terminal digest.
// gateUnitAttemptOrdinal is WHICH ATTEMPT THIS ONE IS, according to the LEDGER rather than
// according to whoever is asking.
//
// THE DEFECT IT CLOSES (C4-01, C4-02), because this function only makes sense against it. The
// attempt id is a pure hash of (rollout, unit, ordinal, prestate digest) and the ordinal used to
// come from a PROCESS-LOCAL counter — `for attempt := 1; ; attempt++` in migrationretry.go, rebuilt
// from scratch on every boot. Cross a process boundary after a rolled-back attempt and all four
// inputs repeat: same rollout, same unit, ordinal back to 1, and an unchanged database re-projects
// a byte-identical prestate. So the next boot re-announced an attempt id already on the record, and
// the grammar — correctly, by its own rule that a retry announces a NEW attempt — refused the whole
// history. Measured: the rollout then fails to fold on EVERY subsequent boot, `reconciled` is
// refused outright by this edition, the three logs are INSERT-only behind ALWAYS guards, and there
// is no repair CLI. A deployment that met that window never started again.
//
// The contradiction was between the WRITER and the GRAMMAR, and the grammar was the half that was
// right. The ledger is the only thing that knows how many attempts have actually happened, because
// it is the only thing that survives the process. So it is asked.
//
// WHY THE CALLER MUST PASS THE APPENDING TRANSACTION, and this is the load-bearing part: counting
// in one transaction and appending in another lets two writers both read N and both announce N+1.
// Inside the append's own transaction the existing UNIQUE(rollout_id, event_ordinal) serializes
// them — the second writer's insert conflicts on the stream ordinal and its whole attempt rolls
// back, count included. The count is safe because the append is serialized, not on its own.
//
// The process-local counter is NOT removed: it still governs backoff and the retry budget, which
// are properties of THIS run. What it stopped being is an identity.
func gateUnitAttemptOrdinal(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID, unitID string) (int, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT count(*) FROM "+guardOnly(dia)+guardGateEventsTable+
			" WHERE rollout_id = ? AND unit_id = ? AND kind = ?"),
		rolloutID, unitID, string(gateEventAttemptStarted))
	if err != nil {
		return 0, fmt.Errorf("sqlstore: count the attempts already announced for unit %s: %w", unitID, err)
	}
	defer rows.Close()
	var announced int64
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("sqlstore: count the attempts already announced for unit %s: %w", unitID, err)
		}
		// A count query that returns NO row is not "zero attempts": it is a query that did not
		// answer. Treating it as zero would re-announce attempt 1 — precisely the failure this
		// function exists to prevent — so it fails instead.
		return 0, fmt.Errorf("sqlstore: counting the attempts announced for unit %s returned no row", unitID)
	}
	if err := rows.Scan(&announced); err != nil {
		return 0, fmt.Errorf("sqlstore: count the attempts already announced for unit %s: %w", unitID, err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sqlstore: count the attempts already announced for unit %s: %w", unitID, err)
	}
	return int(announced) + 1, nil
}

func gateStreamHead(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string) (int64, optDigest, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT event_ordinal, event_sha256 FROM "+guardOnly(dia)+guardGateEventsTable+
			" WHERE rollout_id = ? ORDER BY event_ordinal DESC LIMIT 1"), rolloutID)
	if err != nil {
		return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard gate head: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard gate head: %w", err)
		}
		return 0, optDigest{}, nil
	}
	var ordinal int64
	var raw []byte
	if err := rows.Scan(&ordinal, &raw); err != nil {
		return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard gate head: %w", err)
	}
	d, err := scanDigest(raw, "a guard gate event digest")
	if err != nil {
		return 0, optDigest{}, err
	}
	return ordinal, someDigest(d), rows.Err()
}

// appendGateEvent chains and inserts one gate event.
//
// It reads the head inside the caller's transaction, which is what makes the ordinal
// correct under concurrency: every writer holds the migration advisory lock, and the
// UNIQUE(rollout_id, event_ordinal) constraint is the backstop for the case that
// assumption is ever wrong.
func appendGateEvent(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, ev gateEvent) (gateEvent, error) {
	ordinal, prev, err := gateStreamHead(ctx, tx, dia, ev.RolloutID)
	if err != nil {
		return gateEvent{}, err
	}
	ev.EventOrdinal = ordinal + 1
	ev.PrevEventSHA256 = prev
	if ev.RecordedAt == "" {
		ev.RecordedAt = nowRFC3339()
	}
	if ev.Actor == "" {
		ev.Actor = guardActor
	}
	if ev.BuildID == "" {
		ev.BuildID = guardBuildID()
	}
	if ev.EventSHA256, err = ev.chainDigest(); err != nil {
		return gateEvent{}, err
	}

	encodedUnits, err := encodeUnitList(ev.ExpectedUnits)
	if err != nil {
		return gateEvent{}, err
	}

	var fingerprint any
	if !ev.Diagnostic.empty() {
		fp, ferr := ev.Diagnostic.fingerprint(ev.RolloutID, ev.UnitID, ev.CodeEpoch, ev.CodeSHA256, ev.Condition)
		if ferr != nil {
			return gateEvent{}, ferr
		}
		fingerprint = digestBytes(fp)
	}

	// unit_id, attempt_id and the three key columns are NULL rather than '' for the two
	// rollout-level events, because the CHECK constraint distinguishes them and because
	// an empty string is a value while NULL is the absence of one.
	var unitID, attemptID, intent, relSchema, relName, trigName any
	if ev.UnitID != "" {
		unitID = ev.UnitID
	}
	if ev.AttemptID != "" {
		attemptID = ev.AttemptID
	}
	if ev.Intent != "" {
		intent = string(ev.Intent)
	}
	if ev.Key.Schema != "" {
		relSchema, relName, trigName = ev.Key.Schema, ev.Key.Relation, ev.Key.Trigger
	}
	var prestateBytes any
	if ev.PrestateBytes != "" {
		prestateBytes = ev.PrestateBytes
	}
	// The five typed prestate columns are NULL as a GROUP or present as a group. Writing
	// some of them would produce a row that describes half a reading, and half a reading is
	// exactly what prestate.validate() exists to refuse.
	var preTargetExists, preGuardPresent, preGuardState, preGuardCanonical, preReceiptPresent any
	if ev.PrestatePresent {
		preTargetExists = ev.Prestate.TargetExists
		preGuardPresent = ev.Prestate.GuardPresent
		preGuardState = ev.Prestate.GuardEnableState
		preGuardCanonical = ev.Prestate.GuardMatchesCanonical
		preReceiptPresent = ev.Prestate.ReceiptPresent
	}

	// The four checkpoint columns travel as a GROUP: all four or all NULL, which the table's own
	// CHECK also enforces. A half-written checkpoint would attest half a history.
	var cpInvSHA, cpRcptSHA any
	var cpInvCount, cpRcptCount any
	if ev.CheckpointPresent {
		cpInvSHA, cpRcptSHA = digestBytes(ev.Checkpoint.InventoryHead), digestBytes(ev.Checkpoint.ReceiptHead)
		cpInvCount, cpRcptCount = ev.Checkpoint.InventoryCount, ev.Checkpoint.ReceiptCount
	}

	q := dia.Rebind("INSERT INTO " + guardGateEventsTable + ` (
  event_sha256, rollout_id, event_ordinal, prev_event_sha256, kind,
  unit_id, attempt_id, intent, relation_schema, relation_name, trigger_name,
  manifest_format, code_epoch, code_sha256, retained_revision, retained_sha256,
  spec_sha256, definition_sha256, prestate_sha256,
  prestate_target_exists, prestate_guard_present, prestate_guard_state,
  prestate_guard_canonical, prestate_receipt_present, prestate_bytes,
  phase, gate_condition,
  diagnostic_code, retry_class, unblock_policy, sqlstate, expected_sha, observed_sha,
  diagnostic_fingerprint, details, expected_units,
  checkpoint_inventory_sha256, checkpoint_inventory_count,
  checkpoint_receipt_sha256, checkpoint_receipt_count,
  build_id, actor, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, q,
		digestBytes(ev.EventSHA256), ev.RolloutID, ev.EventOrdinal, ev.PrevEventSHA256.bytes(), string(ev.Kind),
		unitID, attemptID, intent, relSchema, relName, trigName,
		ev.Format, ev.CodeEpoch, digestBytes(ev.CodeSHA256), ev.RetainedRevision, digestBytes(ev.RetainedSHA256),
		ev.SpecSHA256.bytes(), ev.DefinitionSHA256.bytes(), ev.PrestateSHA256.bytes(),
		preTargetExists, preGuardPresent, preGuardState, preGuardCanonical, preReceiptPresent,
		prestateBytes,
		string(ev.Phase), string(ev.Condition),
		ev.Diagnostic.Code, ev.Diagnostic.RetryClass, ev.Diagnostic.UnblockPolicy,
		ev.Diagnostic.SQLState, ev.Diagnostic.ExpectedSHA, ev.Diagnostic.ObservedSHA,
		fingerprint, ev.Diagnostic.Details, encodedUnits,
		cpInvSHA, cpInvCount, cpRcptSHA, cpRcptCount,
		ev.BuildID, ev.Actor, ev.RecordedAt,
	); err != nil {
		return gateEvent{}, fmt.Errorf("sqlstore: append a guard gate %s event: %w", ev.Kind, err)
	}
	return ev, nil
}

// encodeUnitList renders an ordered set of unit identities for storage.
//
// Unit identities are hex digests, so a newline separator cannot occur inside one — which
// is why this is safe where a comma-joined list of TABLE names was not: a legal table name
// containing the delimiter silently dropped a table from the ACL scope, measured. The
// invariant is checked rather than assumed: appendUnitList refuses a member that contains
// the separator.
func encodeUnitList(units []string) (string, error) {
	// CHECKED, not assumed, because the previous version of this comment claimed a check that
	// did not exist — and this repository has already paid for exactly that: a legal table name
	// containing the delimiter was silently dropped from the ACL scope by a comma-joined list.
	// A member carrying the separator would decode as two members, so the enumeration a later
	// boot verifies would not be the one that was recorded.
	for i, u := range units {
		if strings.ContainsAny(u, "\n") {
			return "", fmt.Errorf("sqlstore: list member %d (%q) contains the separator, so the encoding would not round-trip", i, u)
		}
	}
	return strings.Join(units, "\n"), nil
}

func decodeUnitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// unitGateState is what the gate's history says about ONE unit.
//
// Separate from the rollout's condition on purpose: a confirmed unit does not become
// "never" because a later unit has not started yet.
type unitGateState string

const (
	unitGateNever   unitGateState = "never"
	unitGateStarted unitGateState = "started"
	unitGateJudged  unitGateState = "judged"
	unitGateFailed  unitGateState = "failed"
)

// unitGateFold is the folded history of one unit.
type unitGateFold struct {
	State     unitGateState
	AttemptID string
	// SeenAttempts is EVERY attempt id this unit has ever announced, not just the last one.
	//
	// THE DEFECT IT CLOSES (C4-04), which is a SEPARATE defect from the repeated-identity one and
	// needs this fix even after that one lands. The duplicate-announcement guard compared the
	// incoming id against AttemptID — the LAST folded attempt — so a history that had moved on
	// could still be re-entered: started(A1), failed(A1), started(A2), and then a re-announcement
	// of A1 passes, because A1 is no longer the last. Worse than the brick, because it does not
	// stop: the fold resets State to started/A1, and the rule that refuses a judged reading for an
	// already-failed attempt keys on that same overwritten State, so judged(A1) passes too. The
	// ledger then carries one attempt id recorded BOTH as terminally failed AND as judged with a
	// receipt, and validateGuardReceipt compares against the last attempt, so it validates.
	//
	// A set is the honest shape of "announced once". The membership test is linear because the
	// number of attempts per unit is bounded by the retry budget, and a map would alias across the
	// value copies this fold is passed around as.
	SeenAttempts   []string
	Intent         unitIntent
	Key            guardKey
	JudgedPrestate optDigest
	// JudgedReading is the reconstructed prestate the judged event recorded, verified against
	// its own digest by the fold. It is what a later boot reasons from: the catalog now shows
	// the POSTstate, and the authority is what was judged.
	JudgedReading      prestate
	JudgedReadingValid bool
	// JudgedPrestateBytes is the canonical rendering that accompanied the judged digest.
	JudgedPrestateBytes string
	// JudgedSpecSHA256 and JudgedDefinitionSHA256 are the entry and the object the judged
	// event says it was acting on.
	//
	// They are carried because a receipt and a judged event were previously bound by attempt
	// and prestate digest ALONE — and a prestate digest is computed from the reading, not from
	// which entry the reading belongs to. So a judged event could name one target's spec while
	// the receipt named another's, and each half was internally consistent. Comparing these two
	// with the plan is what makes the two halves one statement.
	JudgedSpecSHA256       optDigest
	JudgedDefinitionSHA256 optDigest
	RetryClass             string
	UnblockPolicy          string
	Diagnostic             guardDiagnostic
	Reconciled             bool
}

// gateProjection is the fold of one rollout's whole history.
type gateProjection struct {
	Found     bool
	RolloutID string
	Phase     gatePhase
	Condition gateCondition

	Format           int64
	CodeEpoch        int64
	CodeSHA256       [32]byte
	RetainedRevision int64
	RetainedSHA256   [32]byte

	ExpectedUnits []string
	Units         map[string]unitGateFold
	// FirstBlocking is the diagnostic that put the rollout in blocked, kept so a refusal
	// can name the original cause rather than the last thing that happened.
	FirstBlocking guardDiagnostic
	Events        int
	// Checkpoint is what the closing `ready` attested about the other two logs.
	Checkpoint        guardCheckpoint
	CheckpointPresent bool
}

// mayMutate reports whether the projection authorizes any DDL at all.
//
// Only two conditions do, and both are inside `pending`. `ready` never authorizes
// mutation: a rollout that closed is VERIFY-only, and re-creating an object observed to be
// missing under `ready` would launder the sabotage rather than report it.
func (p gateProjection) mayMutate() bool {
	if !p.Found || p.Phase != gatePhasePending {
		return false
	}
	return p.Condition == gateConditionClean || p.Condition == gateConditionRetryable
}

// foldGateEvents reads a rollout's whole history and folds it, verifying the chain as it
// goes.
//
// Every event's digest is RECOMPUTED from its own body, its ordinal and its predecessor.
// A stored digest nobody recomputes detects nothing while reading as though it detects
// everything, and this ledger's entire claim is that a history cannot be quietly
// rewritten.
func foldGateEvents(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string) (gateProjection, error) {
	out := gateProjection{RolloutID: rolloutID, Units: map[string]unitGateFold{}}
	rows, err := q.QueryContext(ctx, dia.Rebind(`SELECT
  event_sha256, event_ordinal, prev_event_sha256, kind,
  unit_id, attempt_id, intent, relation_schema, relation_name, trigger_name,
  manifest_format, code_epoch, code_sha256, retained_revision, retained_sha256,
  spec_sha256, definition_sha256, prestate_sha256,
  prestate_target_exists, prestate_guard_present, prestate_guard_state,
  prestate_guard_canonical, prestate_receipt_present, prestate_bytes,
  phase, gate_condition,
  diagnostic_code, retry_class, unblock_policy, sqlstate, expected_sha, observed_sha,
  details, expected_units,
  checkpoint_inventory_sha256, checkpoint_inventory_count,
  checkpoint_receipt_sha256, checkpoint_receipt_count,
  build_id, actor, recorded_at
FROM `+guardOnly(dia)+guardGateEventsTable+` WHERE rollout_id = ? ORDER BY event_ordinal`), rolloutID)
	if err != nil {
		return gateProjection{}, fmt.Errorf("sqlstore: read the guard gate history: %w", err)
	}
	defer rows.Close()

	var prev optDigest
	var expectedOrdinal int64 = 1
	for rows.Next() {
		var (
			ev                           gateEvent
			storedDigest, prevRaw        []byte
			specRaw, defRaw, prestateRaw []byte
			kind, phase, condition       string
			unitID, attemptID, intent    sql.NullString
			relSchema, relName, trigName sql.NullString
			preExists, prePresent        sql.NullBool
			preState                     sql.NullString
			preCanonical, preReceipt     sql.NullBool
			prestateBytes                sql.NullString
			expectedUnits                string
			cpInvRaw, cpRcptRaw          []byte
			cpInvCount, cpRcptCount      sql.NullInt64
		)
		if err := rows.Scan(
			&storedDigest, &ev.EventOrdinal, &prevRaw, &kind,
			&unitID, &attemptID, &intent, &relSchema, &relName, &trigName,
			&ev.Format, &ev.CodeEpoch, &specSink{&ev.CodeSHA256}, &ev.RetainedRevision, &specSink{&ev.RetainedSHA256},
			&specRaw, &defRaw, &prestateRaw,
			&preExists, &prePresent, &preState, &preCanonical, &preReceipt, &prestateBytes,
			&phase, &condition,
			&ev.Diagnostic.Code, &ev.Diagnostic.RetryClass, &ev.Diagnostic.UnblockPolicy,
			&ev.Diagnostic.SQLState, &ev.Diagnostic.ExpectedSHA, &ev.Diagnostic.ObservedSHA,
			&ev.Diagnostic.Details, &expectedUnits,
			&cpInvRaw, &cpInvCount, &cpRcptRaw, &cpRcptCount,
			&ev.BuildID, &ev.Actor, &ev.RecordedAt,
		); err != nil {
			return gateProjection{}, fmt.Errorf("sqlstore: read the guard gate history: %w", err)
		}
		ev.RolloutID = rolloutID
		ev.Kind = gateEventKind(kind)
		ev.Phase, ev.Condition = gatePhase(phase), gateCondition(condition)
		ev.UnitID, ev.AttemptID, ev.Intent = unitID.String, attemptID.String, unitIntent(intent.String)
		if relSchema.Valid {
			ev.Key = guardKey{Schema: relSchema.String, Relation: relName.String, Trigger: trigName.String}
		}
		ev.PrestateBytes = prestateBytes.String
		ev.ExpectedUnits = decodeUnitList(expectedUnits)
		if ev.SpecSHA256, err = scanOptDigest(specRaw, "a guard gate spec digest"); err != nil {
			return gateProjection{}, err
		}
		if ev.DefinitionSHA256, err = scanOptDigest(defRaw, "a guard gate definition digest"); err != nil {
			return gateProjection{}, err
		}
		if ev.PrestateSHA256, err = scanOptDigest(prestateRaw, "a guard gate prestate digest"); err != nil {
			return gateProjection{}, err
		}
		if ev.PrevEventSHA256, err = scanOptDigest(prevRaw, "a guard gate predecessor digest"); err != nil {
			return gateProjection{}, err
		}
		if cpInvRaw != nil || cpRcptRaw != nil || cpInvCount.Valid || cpRcptCount.Valid {
			if cpInvRaw == nil || cpRcptRaw == nil || !cpInvCount.Valid || !cpRcptCount.Valid {
				return gateProjection{}, fmt.Errorf("%w: rollout %s event %d carries a partial checkpoint",
					ErrGuardGateChainBroken, rolloutID, ev.EventOrdinal)
			}
			if ev.Checkpoint.InventoryHead, err = scanDigest(cpInvRaw, "a checkpoint inventory head"); err != nil {
				return gateProjection{}, err
			}
			if ev.Checkpoint.ReceiptHead, err = scanDigest(cpRcptRaw, "a checkpoint receipt head"); err != nil {
				return gateProjection{}, err
			}
			ev.Checkpoint.InventoryCount, ev.Checkpoint.ReceiptCount = cpInvCount.Int64, cpRcptCount.Int64
			ev.CheckpointPresent = true
		}

		// THE JUDGED READING IS RECONSTRUCTED AND RE-HASHED, not merely carried.
		//
		// The five typed columns are NULL as a group or present as a group, which the writer
		// guarantees and this checks: a partial reading would describe half a database. When
		// they are present the reading is rebuilt — observable half from the columns, binding
		// half from the event's own edition fields — and its digest recomputed. If that does
		// not equal the stored prestate_sha256, the row and its hash describe different
		// readings, and NEITHER may be used to authorize anything.
		present := []bool{preExists.Valid, prePresent.Valid, preState.Valid, preCanonical.Valid, preReceipt.Valid}
		allPresent, anyPresent := true, false
		for _, v := range present {
			allPresent = allPresent && v
			anyPresent = anyPresent || v
		}
		if anyPresent && !allPresent {
			return gateProjection{}, fmt.Errorf("%w: rollout %s event %d carries a partial prestate (%v), which describes half a database",
				ErrGuardGateChainBroken, rolloutID, ev.EventOrdinal, present)
		}
		if allPresent {
			ev.PrestatePresent = true
			ev.Prestate = prestate{
				TargetExists:          preExists.Bool,
				GuardPresent:          prePresent.Bool,
				GuardEnableState:      preState.String,
				GuardMatchesCanonical: preCanonical.Bool,
				ReceiptPresent:        preReceipt.Bool,
				Epoch:                 ev.CodeEpoch,
				RolloutID:             ev.RolloutID,
				ManifestFormat:        ev.Format,
				CodeSHA256:            hexDigest(ev.CodeSHA256),
				RetainedRevision:      ev.RetainedRevision,
				RetainedSHA256:        hexDigest(ev.RetainedSHA256),
			}
			if ev.SpecSHA256.Valid {
				ev.Prestate.SpecSHA256 = hexDigest(ev.SpecSHA256.D)
			}
			if ev.DefinitionSHA256.Valid {
				ev.Prestate.DefinitionSHA256 = hexDigest(ev.DefinitionSHA256.D)
			}
			rebuilt, derr := prestateDigest(ev.Prestate)
			if derr != nil {
				return gateProjection{}, derr
			}
			if !ev.PrestateSHA256.Valid || rebuilt != ev.PrestateSHA256.D {
				return gateProjection{}, fmt.Errorf("%w: rollout %s event %d stores prestate digest %s but the reading it records hashes to %s",
					ErrGuardGateChainBroken, rolloutID, ev.EventOrdinal, ev.PrestateSHA256, hexDigest(rebuilt))
			}
		}

		if ev.EventOrdinal != expectedOrdinal {
			return gateProjection{}, fmt.Errorf("%w: rollout %s jumps from ordinal %d to %d",
				ErrGuardGateChainBroken, rolloutID, expectedOrdinal-1, ev.EventOrdinal)
		}
		if ev.PrevEventSHA256 != prev {
			return gateProjection{}, fmt.Errorf("%w: rollout %s event %d records predecessor %s, but its predecessor hashes to %s",
				ErrGuardGateChainBroken, rolloutID, ev.EventOrdinal, ev.PrevEventSHA256, prev)
		}
		recomputed, cerr := ev.chainDigest()
		if cerr != nil {
			return gateProjection{}, cerr
		}
		stored, serr := scanDigest(storedDigest, "a guard gate event digest")
		if serr != nil {
			return gateProjection{}, serr
		}
		if recomputed != stored {
			return gateProjection{}, fmt.Errorf("%w: rollout %s event %d (%s) stores digest %s but its contents hash to %s",
				ErrGuardGateChainBroken, rolloutID, ev.EventOrdinal, ev.Kind, hexDigest(stored), hexDigest(recomputed))
		}
		prev = someDigest(stored)
		expectedOrdinal++
		out.Events++
		if ferr := out.foldOne(ev); ferr != nil {
			return gateProjection{}, ferr
		}
	}
	if err := rows.Err(); err != nil {
		return gateProjection{}, fmt.Errorf("sqlstore: read the guard gate history: %w", err)
	}
	return out, nil
}

// specSink adapts a [32]byte field to sql.Scanner so a NOT NULL digest column can be
// scanned straight into it, with the width enforced at the boundary.
//
// It exists so a short or long value is refused where it is READ rather than compared: two
// truncated digests agreeing is two wrong answers agreeing.
type specSink struct{ into *[32]byte }

func (s *specSink) Scan(src any) error {
	raw, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("sqlstore: expected a 32-byte digest, got %T", src)
	}
	d, err := scanDigest(raw, "a guard digest column")
	if err != nil {
		return err
	}
	*s.into = d
	return nil
}

// foldOne applies one event to the projection.
//
// The rules are the step-2 design's, and two of them are the ones worth stating:
//
//   - Once blocked, nothing but an explicit read-only reconciliation or an operator event
//     leaves it. A retryable failure arriving after a permanent one must not relax the
//     condition, or a boot loop would eventually find an ordering that lets it through.
//   - `ready` sets verified, and a verification failure AFTER it produces ready/blocked
//     rather than reopening `pending`. Reopening would let drift authorize the very DDL
//     that drift is evidence against.
func (p *gateProjection) foldOne(ev gateEvent) error {
	// THE GRAMMAR RUNS BEFORE ANY EFFECT. What follows used to be a bare switch that wrote
	// p.Units[ev.UnitID] for whatever arrived, so an `attempt-judged` could appear before the
	// rollout was opened, for a unit nobody enumerated, carrying a different rollout tuple,
	// after `ready`, and with an attempt no `attempt-started` had ever announced — and every one
	// of those simply overwrote the unit's fold. The SQL CHECK constraints validate vocabulary
	// and nullability; only this validates a HISTORY.
	if err := p.checkGateEventGrammar(ev); err != nil {
		return err
	}
	switch ev.Kind {
	case gateEventPendingOpened:
		p.Found = true
		p.Phase, p.Condition = gatePhasePending, gateConditionClean
		p.Format, p.CodeEpoch, p.CodeSHA256 = ev.Format, ev.CodeEpoch, ev.CodeSHA256
		p.RetainedRevision, p.RetainedSHA256 = ev.RetainedRevision, ev.RetainedSHA256
		p.ExpectedUnits = ev.ExpectedUnits
		for _, u := range ev.ExpectedUnits {
			if _, ok := p.Units[u]; !ok {
				p.Units[u] = unitGateFold{State: unitGateNever}
			}
		}
	case gateEventAttemptStarted:
		f := p.Units[ev.UnitID]
		f.State, f.AttemptID, f.Intent, f.Key = unitGateStarted, ev.AttemptID, ev.Intent, ev.Key
		// A FRESH slice every time, never append-in-place. This fold is stored and read back as a
		// VALUE, so an in-place append could write through a backing array another copy still
		// refers to — which for a set that exists to refuse duplicates is the one bug it must not
		// have.
		f.SeenAttempts = append(append([]string(nil), f.SeenAttempts...), ev.AttemptID)
		p.Units[ev.UnitID] = f
	case gateEventAttemptJudged:
		f := p.Units[ev.UnitID]
		f.State, f.AttemptID, f.Intent, f.Key = unitGateJudged, ev.AttemptID, ev.Intent, ev.Key
		f.JudgedPrestate, f.JudgedPrestateBytes = ev.PrestateSHA256, ev.PrestateBytes
		f.JudgedReading, f.JudgedReadingValid = ev.Prestate, ev.PrestatePresent
		f.JudgedSpecSHA256, f.JudgedDefinitionSHA256 = ev.SpecSHA256, ev.DefinitionSHA256
		p.Units[ev.UnitID] = f
	case gateEventAttemptFailed:
		f := p.Units[ev.UnitID]
		f.State = unitGateFailed
		f.RetryClass, f.UnblockPolicy, f.Diagnostic = ev.Diagnostic.RetryClass, ev.Diagnostic.UnblockPolicy, ev.Diagnostic
		p.Units[ev.UnitID] = f
		p.applyFailure(ev)
	case gateEventVerificationFailed:
		if ev.UnitID != "" {
			f := p.Units[ev.UnitID]
			f.Diagnostic, f.UnblockPolicy = ev.Diagnostic, ev.Diagnostic.UnblockPolicy
			p.Units[ev.UnitID] = f
		}
		// The PHASE is preserved: drift found under `ready` produces ready/blocked, which is
		// a different situation from a transition that never closed, and conflating them
		// would let drift reopen a rollout.
		p.Condition = gateConditionBlocked
		if p.FirstBlocking.empty() {
			p.FirstBlocking = ev.Diagnostic
		}
	case gateEventReady:
		// NOTHING CLOSES A BLOCKED ROLLOUT. `ready` is the coordinator's attestation that every
		// expected unit has a correct receipt and every terminal object was re-projected — a
		// claim that cannot be true of a rollout whose own history records drift or a permanent
		// failure. Accepting it would let one appended row erase the condition, which is
		// exactly the authority the fold exists to deny.
		//
		// Only a read-only `reconciled` may relax blocked, and only to the condition it
		// computed while mutating nothing.
		if p.Condition == gateConditionBlocked {
			return fmt.Errorf("%w: rollout %s is closed by event %d while its history records it as blocked (%s)",
				ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal, p.FirstBlocking.Code)
		}
		p.Phase, p.Condition = gatePhaseReady, gateConditionVerified
		p.FirstBlocking = guardDiagnostic{}
		p.Checkpoint, p.CheckpointPresent = ev.Checkpoint, ev.CheckpointPresent
	}
	return nil
}

// checkGateEventGrammar is the gate's state machine, expressed once.
//
// EVERY RULE HERE CLOSES A CONCRETE ACCEPTANCE, not a hypothetical one. Each was reachable by
// appending a single self-consistent row, and each simply overwrote part of the fold:
//
//  1. an attempt event BEFORE the rollout was opened;
//  2. an event for a unit the opening did not enumerate;
//  3. an event whose own (key, intent) does not produce the unit id it is filed under;
//  4. an event carrying a different edition/retained tuple from the opening;
//  5. `attempt-judged` or `attempt-failed` with no `attempt-started` for that attempt;
//  6. any attempt after `ready`; and
//  7. a `ready` whose expected units differ from the opening's — the fold kept the opening's
//     list and discarded the closing one, so the difference was invisible.
//
// AND `reconciled` IS REFUSED OUTRIGHT in this edition. It is a vocabulary the column's CHECK
// accepts so a later edition needs no migration, and interpreting it today was a live hole: the
// fold let any `reconciled` whose `condition` was not `blocked` clear a block, and this edition
// ships no authenticated writer for it and no repair flow behind it. Refusing is fail-closed;
// the word stays in the vocabulary for the edition that earns it.
func (p *gateProjection) checkGateEventGrammar(ev gateEvent) error {
	if ev.Kind == gateEventReconciled {
		return fmt.Errorf("%w: rollout %s carries a `reconciled` event (%d) and this edition has no writer for one; a row that can clear a block must come from a repair flow that does not exist yet",
			ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal)
	}
	if ev.Kind == gateEventPendingOpened {
		// ONCE, AND FIRST. A rollout's identity is derived from its edition and its retained
		// pair, so a second opening under the same identity is not a new rollout — it is a reset
		// of one that already exists, and it would silently discard whatever condition the first
		// had reached. Requiring ordinal 1 is the other half: an opening appended AFTER a
		// `verification-failed` would otherwise reset a blocked history to clean.
		if p.Found {
			return fmt.Errorf("%w: rollout %s is opened twice (event %d)",
				ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal)
		}
		if ev.EventOrdinal != 1 {
			return fmt.Errorf("%w: rollout %s is opened by event %d; the opening is the first event of a rollout or the events before it were never authorized by one",
				ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal)
		}
		return p.checkGateEventShape(ev)
	}
	if !p.Found {
		return fmt.Errorf("%w: rollout %s records a %s event (%d) before any opening; nothing authorized it",
			ErrGuardGateIllegalTransition, p.RolloutID, ev.Kind, ev.EventOrdinal)
	}
	// THE SHAPE OF THE KIND. See guardGateEventShapes: the SQL CHECKs pin a vocabulary and the
	// nullability of unit_id, and nothing else — so an opening declaring `phase=ready,
	// condition=blocked`, or a `ready` declaring `phase=pending`, or an attempt carrying a
	// checkpoint, all reached the fold and were silently NORMALISED by it.
	if err := p.checkGateEventShape(ev); err != nil {
		return err
	}
	// THE EDITION AND THE RETAINED PAIR ARE THE OPENING'S. The rollout id is derived from that
	// tuple, so an event under this id carrying another one describes a rollout it is not part
	// of — and every callback downstream reads these fields as the authorisation.
	if ev.Format != p.Format || ev.CodeEpoch != p.CodeEpoch || ev.CodeSHA256 != p.CodeSHA256 ||
		ev.RetainedRevision != p.RetainedRevision || ev.RetainedSHA256 != p.RetainedSHA256 {
		return fmt.Errorf("%w: event %d (%s) of rollout %s carries edition %d/%d/%s over retained %d/%s, and the opening records %d/%d/%s over %d/%s",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID,
			ev.Format, ev.CodeEpoch, hexDigest(ev.CodeSHA256), ev.RetainedRevision, hexDigest(ev.RetainedSHA256),
			p.Format, p.CodeEpoch, hexDigest(p.CodeSHA256), p.RetainedRevision, hexDigest(p.RetainedSHA256))
	}
	// NOTHING FOLLOWS `ready` BUT DRIFT. A closed rollout has attested every terminal object; an
	// attempt after that would be work nothing authorized, and a second `ready` would re-attest
	// a checkpoint over rows the first one never saw.
	if p.Phase == gatePhaseReady && ev.Kind != gateEventVerificationFailed {
		return fmt.Errorf("%w: rollout %s is closed and event %d is a %s; only a verification failure may follow `ready`",
			ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal, ev.Kind)
	}
	if ev.Kind == gateEventReady {
		if !equalStringSlices(ev.ExpectedUnits, p.ExpectedUnits) {
			return fmt.Errorf("%w: rollout %s is closed by event %d enumerating %d units, and its opening enumerated %d",
				ErrGuardGateIllegalTransition, p.RolloutID, ev.EventOrdinal, len(ev.ExpectedUnits), len(p.ExpectedUnits))
		}
		return nil
	}
	if ev.UnitID == "" {
		return nil
	}
	// THE UNIT BELONGS TO THE ENUMERATION. p.Units is seeded from the opening's expected units,
	// so an id absent from it is an id the rollout never authorized anybody to work on.
	f, enumerated := p.Units[ev.UnitID]
	if !enumerated {
		return fmt.Errorf("%w: event %d (%s) of rollout %s names unit %s, which its opening did not enumerate",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.UnitID)
	}
	switch ev.Kind {
	case gateEventVerificationFailed:
		// It carries a key but no intent — it is written about a target, not about an edge — so
		// the identity recomputation below does not apply to it.
		return nil
	case gateEventAttemptStarted, gateEventAttemptJudged, gateEventAttemptFailed:
	default:
		return nil
	}
	// THE IDENTITY IS RECOMPUTED FROM THE EVENT'S OWN FIELDS. A unit id is the digest of
	// (format, key, intent), so requiring the event's key and intent to reproduce the id it is
	// filed under closes "intent/key distintos entre eventos" completely and in one comparison,
	// rather than as three separate equality checks that a fourth field could still escape.
	want, err := guardUnitID(ev.Format, ev.Key, ev.Intent)
	if err != nil {
		return err
	}
	if want != ev.UnitID {
		return fmt.Errorf("%w: event %d (%s) of rollout %s is filed under unit %s while its own %s of %s identifies unit %s",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.UnitID, ev.Intent, ev.Key, want)
	}
	// AND AN ATTEMPT IS ANNOUNCED BEFORE IT IS JUDGED. `attempt-started` commits in its own
	// transaction before the unit's, so a judged reading with no started event for the same
	// attempt is a history in which the reading appeared without the attempt that took it.
	if ev.Kind == gateEventAttemptStarted {
		// ANNOUNCED ONCE, TOO. The previous shape returned here without looking at the fold at
		// all, so the same attempt could be announced any number of times and the history still
		// read as legal — measured in round four by folding two identical `attempt-started`
		// events after an opening and having them accepted.
		//
		// The comparison is on the ATTEMPT ID rather than on the state, because those are
		// different histories: a unit that failed and is retried announces a NEW attempt, and
		// refusing that would forbid the retry this ledger is built around. What no honest
		// writer does is announce the SAME attempt twice — a second announcement of an attempt
		// already on the record either re-dates work that already happened or hides a writer
		// that lost track of its own attempt.
		//
		// AGAINST THE WHOLE SET, NOT AGAINST THE LAST ONE. Comparing with f.AttemptID alone let a
		// history that had moved past an attempt re-enter it: started(A1) → failed(A1) →
		// started(A2) and then A1 again, which is not the last and so was accepted. See
		// unitGateFold.SeenAttempts for what that bought an attacker or a confused writer.
		if seen, where := gateAttemptAlreadySeen(f, ev.AttemptID); seen {
			return fmt.Errorf("%w: event %d announces attempt %q of unit %s, which is already on the record %s; an attempt is announced once and a retry announces a new one",
				ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.AttemptID, ev.UnitID, where)
		}
		return nil
	}
	if f.State == unitGateNever {
		return fmt.Errorf("%w: event %d (%s) of rollout %s judges unit %s, which no attempt ever started",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.UnitID)
	}
	if f.AttemptID != ev.AttemptID {
		return fmt.Errorf("%w: event %d (%s) of rollout %s carries attempt %q for unit %s, whose last announced attempt is %q",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.AttemptID, ev.UnitID, f.AttemptID)
	}
	// AND THE TRANSITION ITSELF. Matching the attempt id is not the same as the attempt being in
	// a state this event may follow: a judged reading appended AFTER that attempt already failed
	// would overwrite the failure's fold with a success-shaped one, and the condition the failure
	// set is the only thing standing between a blocked rollout and another run.
	if ev.Kind == gateEventAttemptJudged && f.State == unitGateFailed {
		return fmt.Errorf("%w: event %d judges attempt %q of unit %s, which that same attempt already recorded as failed; a retry announces a NEW attempt",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.AttemptID, ev.UnitID)
	}
	return nil
}

// gateAttemptAlreadySeen answers whether this unit has announced this attempt id before, and says
// WHERE it saw it so the refusal names something an operator can go and look at.
//
// The two answers are deliberately different sentences. "as started/failed/judged" means the id is
// the one the fold is currently on, which is the ordinary duplicate. "earlier in this unit's
// history" means the writer reached back past a more recent attempt, which is the C4-04 shape and a
// different kind of wrong — it is the one that ends with a single id recorded as both failed and
// receipted. Both keep the substring "already on the record", which is what the committed
// regressions match on.
func gateAttemptAlreadySeen(f unitGateFold, attemptID string) (bool, string) {
	if f.State == unitGateNever {
		return false, ""
	}
	if f.AttemptID == attemptID {
		return true, "as " + string(f.State)
	}
	for _, seen := range f.SeenAttempts {
		if seen == attemptID {
			return true, "earlier in this unit's history, before attempt " + f.AttemptID
		}
	}
	return false, ""
}

// checkGateEventShape refuses an event whose FIELDS are not the shape its kind has.
//
// WHY IT CANNOT LIVE IN SQL. A CHECK constraint sees one row and can express "kind is one of
// these seven" and "unit_id is null exactly for these two". It cannot express "the phase this
// event declares must be the phase the history is in", because that is a property of the fold.
// So the previous version accepted, and quietly normalised:
//
//   - `pending-opened` declaring phase=ready/condition=blocked — folded to pending/clean;
//   - `ready` declaring phase=pending/condition=clean — folded to ready/verified;
//   - a `verification-failed` declaring a phase its rollout is not in, which is the row an
//     operator reads to find out what happened.
//
// Normalising is worse than rejecting: the projection stays right while the DURABLE ROW lies,
// and the row is the evidence.
func (p *gateProjection) checkGateEventShape(ev gateEvent) error {
	shape, ok := guardGateEventShapes()[ev.Kind]
	if !ok {
		return fmt.Errorf("%w: event %d of rollout %s is a %q, which this edition's fold has no shape for",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, p.RolloutID, ev.Kind)
	}
	// THE PHASE. For every kind but the verification failure it is a constant of the kind; for
	// that one it must be the phase the fold is actually in, because it is written ABOUT that
	// history rather than moving it.
	wantPhase := shape.Phase
	if ev.Kind == gateEventVerificationFailed {
		wantPhase = p.Phase
	}
	if ev.Phase != wantPhase {
		return fmt.Errorf("%w: event %d (%s) of rollout %s declares phase %q where its kind requires %q",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.Phase, wantPhase)
	}
	allowed := false
	for _, c := range shape.Conditions {
		if ev.Condition == c {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: event %d (%s) of rollout %s declares condition %q where its kind allows %v",
			ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, ev.Condition, shape.Conditions)
	}
	for _, f := range gateEventFields() {
		switch rule := f.Rule(shape); rule {
		case fieldOptional:
			continue
		case fieldRequired:
			if f.Present(ev) {
				continue
			}
			return fmt.Errorf("%w: event %d (%s) of rollout %s must carry %s",
				ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, f.What)
		default:
			if !f.Present(ev) {
				continue
			}
			return fmt.Errorf("%w: event %d (%s) of rollout %s must not carry %s",
				ErrGuardGateIllegalTransition, ev.EventOrdinal, ev.Kind, p.RolloutID, f.What)
		}
	}
	return nil
}

// fieldRule is what one kind declares about ONE durable field.
//
// THE ZERO VALUE IS "FORBIDDEN", and it is what makes an undeclared RULE fail closed — not what
// makes the list complete. The previous table was a set of booleans, so a field nobody thought to
// mention was a field nobody checked — and round four
// measured the consequence: deleting four of the production checks left forty generated cases
// green, because the generator only ever mutated the four flags the struct happened to have.
// With a total field list and a fail-closed zero value, a field added to gateEvent and not
// declared here is REFUSED on every kind rather than silently unconstrained.
type fieldRule uint8

const (
	fieldForbidden fieldRule = iota
	fieldRequired
	// fieldOptional is used where the ledger genuinely cannot distinguish absent from empty,
	// and each use says which case that is. It is not a way to avoid deciding.
	fieldOptional
)

// gateEventField is one durable field, with how to read its presence from an event and how to
// read its rule from a shape.
//
// The pair of closures is what keeps the fold and the generator from holding two DIFFERENT
// tables — worth having, and NOT totality: a shared list shares its omissions, which is what
// round five measured. Totality comes from the reflective inventory in
// guardshapetotality_test.go, not from here. (This paragraph used to claim the strong version
// while the one below the constructor explained why it was false.)
type gateEventField struct {
	What string
	// Fields are the paths into gateEvent whose values this predicate observes, dotted for a
	// member of an embedded struct.
	//
	// IT IS WHAT TURNS A HAND-WRITTEN LIST INTO A CHECKED ONE. The names are not documentation:
	// TestGuardGateShapeTableCoversEveryDurableField walks gateEvent by reflection and requires
	// every field to be named here or exempted by name, so DELETING a descriptor now leaves a
	// durable field uncovered and reddens — which round five measured that it did not.
	//
	// WHAT THE PATH CHECK DOES AND DOES NOT DO, stated because the first version of this comment
	// claimed more. TestGuardGateShapeFieldPathsMatchTheirPredicates sets the paths of one
	// descriptor TOGETHER and requires Present to answer true, and requires false on a zero
	// event. It does NOT set or clear them one at a time, and it could not: "a prestate" names
	// both PrestatePresent and Prestate while the predicate reads only the first, so an
	// individual path is not required to be individually sufficient. What is caught is a
	// descriptor whose declared paths, as a set, do not drive its own predicate — which is how a
	// path pointing at the wrong field is found.
	Fields  []string
	Present func(gateEvent) bool
	Rule    func(gateEventShape) fieldRule
}

// gateEventFields enumerates the durable fields of gateEvent that the shape table governs.
//
// IT WAS A HAND-WRITTEN LIST AND NOT A TOTALITY PROOF, which is the correction round five forced:
// this list and the generator's setters were two manual inventories sharing their omissions, so
// deleting a descriptor turned nothing red — the generator simply stopped producing that field's
// cases. THAT HOLE IS NOW CLOSED by the Fields paths above and the structural check that walks
// gateEvent by reflection against them; a deleted descriptor leaves a durable field neither
// covered nor exempted, and the check names it.
//
// What is deliberately NOT here: the chain fields (ordinal, predecessor, digest), which the
// chain verifies; the edition tuple, which checkGateEventGrammar compares against the opening;
// RecordedAt, which is a wall clock nothing may depend on; and Phase/Condition, which have their
// own comparisons above because their rule is not presence but VALUE.
func gateEventFields() []gateEventField {
	return []gateEventField{
		{"a unit id", []string{"UnitID"}, func(e gateEvent) bool { return e.UnitID != "" },
			func(s gateEventShape) fieldRule { return s.UnitID }},
		{"an attempt id", []string{"AttemptID"}, func(e gateEvent) bool { return e.AttemptID != "" },
			func(s gateEventShape) fieldRule { return s.Attempt }},
		{"an intent", []string{"Intent"}, func(e gateEvent) bool { return e.Intent != "" },
			func(s gateEventShape) fieldRule { return s.Intent }},
		// THE KEY IS THREE FIELDS AND IS CHECKED AS THREE. `e.Key != guardKey{}` is a presence
		// test on an AGGREGATE: it accepts a key with one member set, which is exactly the shape
		// the reader can produce when only relation_schema comes back non-empty. Splitting it
		// means a partial key is refused member by member instead of passing as "present".
		{"a target schema", []string{"Key.Schema"}, func(e gateEvent) bool { return e.Key.Schema != "" },
			func(s gateEventShape) fieldRule { return s.Key }},
		{"a target relation", []string{"Key.Relation"}, func(e gateEvent) bool { return e.Key.Relation != "" },
			func(s gateEventShape) fieldRule { return s.Key }},
		{"a target trigger", []string{"Key.Trigger"}, func(e gateEvent) bool { return e.Key.Trigger != "" },
			func(s gateEventShape) fieldRule { return s.Key }},
		{"an entry digest", []string{"SpecSHA256"}, func(e gateEvent) bool { return e.SpecSHA256.Valid },
			func(s gateEventShape) fieldRule { return s.SpecDigest }},
		{"an object digest", []string{"DefinitionSHA256"}, func(e gateEvent) bool { return e.DefinitionSHA256.Valid },
			func(s gateEventShape) fieldRule { return s.DefinitionDigest }},
		{"a prestate", []string{"PrestatePresent", "Prestate"}, func(e gateEvent) bool { return e.PrestatePresent },
			func(s gateEventShape) fieldRule { return s.Prestate }},
		{"a prestate digest", []string{"PrestateSHA256"}, func(e gateEvent) bool { return e.PrestateSHA256.Valid },
			func(s gateEventShape) fieldRule { return s.PrestateDigest }},
		{"a rendered prestate", []string{"PrestateBytes"}, func(e gateEvent) bool { return e.PrestateBytes != "" },
			func(s gateEventShape) fieldRule { return s.PrestateRendering }},
		{"a checkpoint", []string{"CheckpointPresent", "Checkpoint"}, func(e gateEvent) bool { return e.CheckpointPresent },
			func(s gateEventShape) fieldRule { return s.Checkpoint }},
		{"an enumeration", []string{"ExpectedUnits"}, func(e gateEvent) bool { return len(e.ExpectedUnits) > 0 },
			func(s gateEventShape) fieldRule { return s.ExpectedUnits }},
		// The BODY is every diagnostic field OTHER than the code, so the two are independent:
		// "carries a code" and "carries the rest of a diagnosis" can each be true without the
		// other, and a regression can move one without disturbing the other's rule.
		{"a diagnostic body", []string{"Diagnostic"}, func(e gateEvent) bool {
			d := e.Diagnostic
			d.Code = ""
			return d != guardDiagnostic{}
		}, func(s gateEventShape) fieldRule { return s.Diagnostic }},
		{"a diagnostic code", []string{"Diagnostic.Code"}, func(e gateEvent) bool { return e.Diagnostic.Code != "" },
			func(s gateEventShape) fieldRule { return s.Diagnostic }},
		{"an actor", []string{"Actor"}, func(e gateEvent) bool { return e.Actor != "" },
			func(s gateEventShape) fieldRule { return s.Actor }},
		{"a build id", []string{"BuildID"}, func(e gateEvent) bool { return e.BuildID != "" },
			func(s gateEventShape) fieldRule { return s.BuildID }},
	}
}

// gateEventShape is one kind's declared field shape. Every field of gateEventFields has an entry
// here, and an undeclared one is FORBIDDEN rather than free.
type gateEventShape struct {
	Phase      gatePhase
	Conditions []gateCondition

	UnitID            fieldRule
	Attempt           fieldRule
	Intent            fieldRule
	Key               fieldRule
	SpecDigest        fieldRule
	DefinitionDigest  fieldRule
	Prestate          fieldRule
	PrestateDigest    fieldRule
	PrestateRendering fieldRule
	Checkpoint        fieldRule
	ExpectedUnits     fieldRule
	Diagnostic        fieldRule
	Actor             fieldRule
	BuildID           fieldRule
}

// guardGateEventShapes is the table, declared ONCE so the fold and its regressions cannot
// disagree about it. `reconciled` is deliberately absent: this edition refuses it outright.
//
// EVERY ENTRY IS WHAT PRODUCTION ACTUALLY WRITES, read off the constructors AND off the writer:
// guardUnitRunner.gateEvent for the three attempt kinds, openOrVerifyGuardRollout for the
// opening, attemptGuardClose for `ready`, recordGuardVerificationFailure for the verification
// failure — and appendGateEvent, which fills in the attribution none of them set.
//
// READING ONLY THE CONSTRUCTORS IS HOW THE FIRST VERSION OF THIS TABLE GOT IT WRONG. None of
// them mentions Actor, so the table declared it forbidden — and appendGateEvent defaults it to
// `engine.boot` on every event ever written, which an existing regression noticed immediately.
// The attribution pair is therefore declared where the writer decides it, not where the callers
// are silent about it.
func guardGateEventShapes() map[gateEventKind]gateEventShape {
	// The three attempt kinds share a prestate posture: the runner captures a reading before it
	// acts and carries it, its digest and its rendering on every event it writes about that
	// attempt. The rendering is OPTIONAL because it is a human-facing projection of the reading
	// and an empty reading renders to the empty string, which the column cannot tell from absent.
	attempt := gateEventShape{
		Phase: gatePhasePending, Conditions: []gateCondition{gateConditionClean},
		UnitID: fieldRequired, Attempt: fieldRequired, Intent: fieldRequired, Key: fieldRequired,
		SpecDigest: fieldRequired, DefinitionDigest: fieldRequired,
		Prestate: fieldRequired, PrestateDigest: fieldRequired, PrestateRendering: fieldOptional,
	}
	failed := attempt
	failed.Conditions = []gateCondition{gateConditionRetryable, gateConditionBlocked}
	failed.Diagnostic = fieldRequired
	out := map[gateEventKind]gateEventShape{
		gateEventPendingOpened: {
			Phase: gatePhasePending, Conditions: []gateCondition{gateConditionClean},
			ExpectedUnits: fieldRequired,
		},
		gateEventAttemptStarted: attempt,
		gateEventAttemptJudged:  attempt,
		gateEventAttemptFailed:  failed,
		gateEventVerificationFailed: {
			// Phase is the FOLDED one — see checkGateEventShape. It is written ABOUT a target
			// rather than about an edge, so it carries the unit it is filed under and the key,
			// and no attempt, intent, digest or prestate.
			Conditions: []gateCondition{gateConditionBlocked},
			UnitID:     fieldRequired, Key: fieldRequired, Diagnostic: fieldRequired,
		},
		gateEventReady: {
			Phase: gatePhaseReady, Conditions: []gateCondition{gateConditionVerified},
			Checkpoint: fieldRequired, ExpectedUnits: fieldRequired,
		},
	}
	// THE ATTRIBUTION PAIR, applied to every kind because appendGateEvent applies it to every
	// event — and the two halves are different rules for a reason the writer states itself:
	//
	//   - the ACTOR is defaulted to `engine.boot` whenever a caller leaves it blank, so no event
	//     this engine writes lacks one, and the column is NOT NULL. Required.
	//   - the BUILD ID comes from the embedded build info and is EMPTY when the binary was built
	//     without VCS stamping. Requiring it would make a legitimately unstamped build's history
	//     illegal, and forbidding it would make a stamped one's illegal. Optional is the only
	//     rule that is true of both, and it is safe because nothing routes on it.
	for kind, shape := range out {
		shape.Actor = fieldRequired
		shape.BuildID = fieldOptional
		out[kind] = shape
	}
	return out
}

// equalStringSlices compares two ordered lists element by element.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyFailure moves the rollout's condition for a terminal failure.
func (p *gateProjection) applyFailure(ev gateEvent) {
	if p.Condition == gateConditionBlocked {
		// Already blocked. A retryable failure arriving afterwards must not relax it.
		return
	}
	switch ev.Diagnostic.RetryClass {
	case guardRetryClassRetryable:
		p.Condition = gateConditionRetryable
	default:
		// Permanent AND unknown both block. Unknown is not "probably fine": it is the one
		// state where both guesses — retry and skip — can corrupt the ledger.
		p.Condition = gateConditionBlocked
		if p.FirstBlocking.empty() {
			p.FirstBlocking = ev.Diagnostic
		}
	}
}

// inventoryEventKind is the entry lifecycle vocabulary. Only `activate` is produced by
// this edition; the other three exist because the column's CHECK is a closed vocabulary
// and a later edition must not need a migration to say what it already means.
type inventoryEventKind string

const (
	inventoryActivate   inventoryEventKind = "activate"
	inventoryRetain     inventoryEventKind = "retain"
	inventoryReactivate inventoryEventKind = "reactivate"
	inventoryTombstone  inventoryEventKind = "tombstone"
)

// inventoryEvent is one row of the entry-lifecycle log.
type inventoryEvent struct {
	Kind                inventoryEventKind
	Key                 guardKey
	Producer            guardProducer
	Format              int64
	CodeEpoch           int64
	DefinitionSHA256    [32]byte
	SpecSHA256          [32]byte
	DesiredEnableState  string
	LegacyAllowedStates []string
	RetainedRevision    int64
	RetainedSHA256      [32]byte

	EventOrdinal    int64
	PrevEventSHA256 optDigest
	EventSHA256     [32]byte
	RecordedAt      string
}

func (e inventoryEvent) chainDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainEvent, e.Format)
	w.str("inventory")
	w.i64(e.EventOrdinal)
	e.PrevEventSHA256.canon(w)
	w.str(string(e.Kind))
	e.Key.canon(w)
	w.str(string(e.Producer))
	w.i64(e.CodeEpoch)
	w.bytes32(e.DefinitionSHA256)
	w.bytes32(e.SpecSHA256)
	w.str(e.DesiredEnableState)
	w.list(len(e.LegacyAllowedStates))
	for _, s := range e.LegacyAllowedStates {
		w.str(s)
	}
	w.i64(e.RetainedRevision)
	w.bytes32(e.RetainedSHA256)
	w.str(e.RecordedAt)
	return w.sum()
}

// inventoryStreamHead returns the deployment-wide inventory head.
//
// One stream, not one per entry: the retained revision is a property of the DATABASE'S
// history, so its ordering must be total across entries rather than per key.
func inventoryStreamHead(ctx context.Context, q dialect.Querier, dia dialect.Dialect) (int64, optDigest, int64, [32]byte, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT event_ordinal, event_sha256, retained_revision, retained_sha256 FROM "+
			guardInventoryEventsTable+" ORDER BY event_ordinal DESC LIMIT 1"))
	if err != nil {
		return 0, optDigest{}, 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory head: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, optDigest{}, 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory head: %w", err)
		}
		empty, eerr := emptyRetainedDigest()
		return 0, optDigest{}, 0, empty, eerr
	}
	var ordinal, revision int64
	var digestRaw, retainedRaw []byte
	if err := rows.Scan(&ordinal, &digestRaw, &revision, &retainedRaw); err != nil {
		return 0, optDigest{}, 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory head: %w", err)
	}
	d, err := scanDigest(digestRaw, "a guard inventory event digest")
	if err != nil {
		return 0, optDigest{}, 0, [32]byte{}, err
	}
	retained, err := scanDigest(retainedRaw, "a guard retained digest")
	if err != nil {
		return 0, optDigest{}, 0, [32]byte{}, err
	}
	return ordinal, someDigest(d), revision, retained, rows.Err()
}

// appendInventoryEvent chains and inserts one inventory event.
func appendInventoryEvent(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, ev inventoryEvent) (inventoryEvent, error) {
	ordinal, prev, _, _, err := inventoryStreamHead(ctx, tx, dia)
	if err != nil {
		return inventoryEvent{}, err
	}
	ev.EventOrdinal = ordinal + 1
	ev.PrevEventSHA256 = prev
	if ev.RecordedAt == "" {
		ev.RecordedAt = nowRFC3339()
	}
	if ev.EventSHA256, err = ev.chainDigest(); err != nil {
		return inventoryEvent{}, err
	}
	encodedStates, err := encodeUnitList(ev.LegacyAllowedStates)
	if err != nil {
		return inventoryEvent{}, err
	}
	q := dia.Rebind("INSERT INTO " + guardInventoryEventsTable + ` (
  event_sha256, event_ordinal, prev_event_sha256, kind,
  relation_schema, relation_name, trigger_name, producer,
  manifest_format, code_epoch, definition_sha256, spec_sha256,
  desired_enable_state, legacy_allowed_states,
  retained_revision, retained_sha256, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, q,
		digestBytes(ev.EventSHA256), ev.EventOrdinal, ev.PrevEventSHA256.bytes(), string(ev.Kind),
		ev.Key.Schema, ev.Key.Relation, ev.Key.Trigger, string(ev.Producer),
		ev.Format, ev.CodeEpoch, digestBytes(ev.DefinitionSHA256), digestBytes(ev.SpecSHA256),
		ev.DesiredEnableState, encodedStates,
		ev.RetainedRevision, digestBytes(ev.RetainedSHA256), ev.RecordedAt,
	); err != nil {
		return inventoryEvent{}, fmt.Errorf("sqlstore: append a guard inventory %s event: %w", ev.Kind, err)
	}
	return ev, nil
}

// guardReceipt is one attribution row.
type guardReceipt struct {
	RolloutID string
	UnitID    string
	Kind      string
	Intent    unitIntent
	Key       guardKey

	Epoch            int64
	Format           int64
	CodeSHA256       [32]byte
	RetainedRevision int64
	RetainedSHA256   [32]byte
	SpecSHA256       [32]byte
	DefinitionSHA256 [32]byte
	PrestateSHA256   [32]byte

	FromEnableState      optText
	ToEnableState        string
	PredecessorReceiptID optDigest
	AttemptID            string

	// ReceiptID is derived from the body, never supplied.
	ReceiptID       [32]byte
	EventOrdinal    int64
	PrevEventSHA256 optDigest
	EventSHA256     [32]byte
	AppliedAt       string
}

// Receipt kinds.
const (
	guardReceiptKindBootstrap = "bootstrap"
	guardReceiptKindUnit      = "unit"
	// guardIntentBootstrap is the intent a bootstrap receipt carries. It is not a
	// unitIntent: no runner ever executes it, and the CHECK constraint keeps the two
	// vocabularies apart so a bootstrap row can never claim a unit's intent.
	guardIntentBootstrap = "bootstrap"
)

// bodyDigest is receipt_id: WHAT was attributed, with no chain fields and no wall clock.
//
// Excluding the chain position is what lets a predecessor link point at a receipt's
// meaning rather than at its position in a stream, and excluding applied_at is what makes
// the same attribution produce the same id on a retry — which is the whole basis of the
// idempotent insert below.
func (r guardReceipt) bodyDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainReceipt, r.Format)
	w.str(r.RolloutID)
	w.str(r.UnitID)
	w.str(r.Kind)
	w.str(string(r.Intent))
	r.Key.canon(w)
	w.i64(r.Epoch)
	w.bytes32(r.CodeSHA256)
	w.i64(r.RetainedRevision)
	w.bytes32(r.RetainedSHA256)
	w.bytes32(r.SpecSHA256)
	w.bytes32(r.DefinitionSHA256)
	w.bytes32(r.PrestateSHA256)
	w.opt(r.FromEnableState)
	w.str(r.ToEnableState)
	r.PredecessorReceiptID.canon(w)
	w.str(r.AttemptID)
	return w.sum()
}

func (r guardReceipt) chainDigest() ([32]byte, error) {
	w := newCanonWriter(canonDomainReceipt, r.Format)
	w.str("receipt-chain")
	w.i64(r.EventOrdinal)
	r.PrevEventSHA256.canon(w)
	w.bytes32(r.ReceiptID)
	return w.sum()
}

// receiptStreamHead returns the rollout's receipt-stream head.
func receiptStreamHead(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string) (int64, optDigest, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT event_ordinal, event_sha256 FROM "+guardOnly(dia)+guardReceiptsTable+
			" WHERE rollout_id = ? ORDER BY event_ordinal DESC LIMIT 1"), rolloutID)
	if err != nil {
		return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard receipt head: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard receipt head: %w", err)
		}
		return 0, optDigest{}, nil
	}
	var ordinal int64
	var raw []byte
	if err := rows.Scan(&ordinal, &raw); err != nil {
		return 0, optDigest{}, fmt.Errorf("sqlstore: read the guard receipt head: %w", err)
	}
	d, err := scanDigest(raw, "a guard receipt event digest")
	if err != nil {
		return 0, optDigest{}, err
	}
	return ordinal, someDigest(d), rows.Err()
}

// guardReceiptColumns is the full projection of a receipt, in one place so the insert, the
// conflict re-read and the bulk VERIFY cannot disagree about which columns constitute a
// receipt.
const guardReceiptColumns = `receipt_id, rollout_id, unit_id, receipt_kind, intent,
  relation_schema, relation_name, trigger_name,
  epoch, manifest_format, code_sha256, retained_revision, retained_sha256,
  spec_sha256, definition_sha256, prestate_sha256,
  from_enable_state, to_enable_state, predecessor_receipt_id, attempt_id,
  event_ordinal, prev_event_sha256, event_sha256, applied_at`

// scanGuardReceipt reads guardReceiptColumns in order.
func scanGuardReceipt(sc func(...any) error) (guardReceipt, error) {
	var (
		r                                 guardReceipt
		receiptRaw, codeRaw, retainedRaw  []byte
		specRaw, defRaw, prestateRaw      []byte
		fromState                         sql.NullString
		predecessorRaw, prevRaw, eventRaw []byte
		intent                            string
	)
	if err := sc(&receiptRaw, &r.RolloutID, &r.UnitID, &r.Kind, &intent,
		&r.Key.Schema, &r.Key.Relation, &r.Key.Trigger,
		&r.Epoch, &r.Format, &codeRaw, &r.RetainedRevision, &retainedRaw,
		&specRaw, &defRaw, &prestateRaw,
		&fromState, &r.ToEnableState, &predecessorRaw, &r.AttemptID,
		&r.EventOrdinal, &prevRaw, &eventRaw, &r.AppliedAt); err != nil {
		return guardReceipt{}, err
	}
	r.Intent = unitIntent(intent)
	if fromState.Valid {
		r.FromEnableState = someText(fromState.String)
	}
	var err error
	if r.ReceiptID, err = scanDigest(receiptRaw, "a guard receipt id"); err != nil {
		return guardReceipt{}, err
	}
	if r.CodeSHA256, err = scanDigest(codeRaw, "a guard receipt code digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.RetainedSHA256, err = scanDigest(retainedRaw, "a guard receipt retained digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.SpecSHA256, err = scanDigest(specRaw, "a guard receipt spec digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.DefinitionSHA256, err = scanDigest(defRaw, "a guard receipt definition digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.PrestateSHA256, err = scanDigest(prestateRaw, "a guard receipt prestate digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.PredecessorReceiptID, err = scanOptDigest(predecessorRaw, "a guard receipt predecessor id"); err != nil {
		return guardReceipt{}, err
	}
	if r.PrevEventSHA256, err = scanOptDigest(prevRaw, "a guard receipt predecessor digest"); err != nil {
		return guardReceipt{}, err
	}
	if r.EventSHA256, err = scanDigest(eventRaw, "a guard receipt event digest"); err != nil {
		return guardReceipt{}, err
	}
	return r, nil
}

// insertGuardReceipt writes one receipt idempotently.
//
// The shape is deliberate and each half of it is a refusal:
//
//   - ON CONFLICT DO NOTHING RETURNING costs ONE roundtrip in the ordinary case, where
//     nothing conflicts.
//   - Zero rows returned means the key already exists, and that is NOT accepted on the
//     strength of the key. The existing row is re-read and compared FIELD BY FIELD against
//     what this attempt would have written. Identical is idempotence — a lost
//     acknowledgement, a retried boot. Anything different means two attempts disagree about
//     what happened, and neither may win silently: that is ErrGuardReceiptConflict.
//
// A bare ON CONFLICT DO NOTHING would have made the second case indistinguishable from the
// first, which is how a receipt written under one epoch comes to attest a unit executed
// under another.
func insertGuardReceipt(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, r guardReceipt) (guardReceipt, error) {
	var err error
	if r.ReceiptID, err = r.bodyDigest(); err != nil {
		return guardReceipt{}, err
	}
	ordinal, prev, err := receiptStreamHead(ctx, tx, dia, r.RolloutID)
	if err != nil {
		return guardReceipt{}, err
	}
	r.EventOrdinal = ordinal + 1
	r.PrevEventSHA256 = prev
	if r.AppliedAt == "" {
		r.AppliedAt = nowRFC3339()
	}
	if r.EventSHA256, err = r.chainDigest(); err != nil {
		return guardReceipt{}, err
	}

	var fromState any
	if r.FromEnableState.Valid {
		fromState = r.FromEnableState.V
	}
	ins := dia.Rebind("INSERT INTO " + guardReceiptsTable + " (" + guardReceiptColumns + `
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (rollout_id, unit_id, receipt_kind) DO NOTHING
RETURNING receipt_id`)
	var got []byte
	err = tx.QueryRowContext(ctx, ins,
		digestBytes(r.ReceiptID), r.RolloutID, r.UnitID, r.Kind, string(r.Intent),
		r.Key.Schema, r.Key.Relation, r.Key.Trigger,
		r.Epoch, r.Format, digestBytes(r.CodeSHA256), r.RetainedRevision, digestBytes(r.RetainedSHA256),
		digestBytes(r.SpecSHA256), digestBytes(r.DefinitionSHA256), digestBytes(r.PrestateSHA256),
		fromState, r.ToEnableState, r.PredecessorReceiptID.bytes(), r.AttemptID,
		r.EventOrdinal, r.PrevEventSHA256.bytes(), digestBytes(r.EventSHA256), r.AppliedAt,
	).Scan(&got)
	switch {
	case err == nil:
		return r, nil
	case !errors.Is(err, sql.ErrNoRows):
		return guardReceipt{}, fmt.Errorf("sqlstore: write the guard receipt for %s: %w", r.Key, err)
	}

	// The key already exists. Read it and compare what it says with what this attempt
	// would have said.
	existing, rerr := readGuardReceipt(ctx, tx, dia, r.RolloutID, r.UnitID, r.Kind)
	if rerr != nil {
		return guardReceipt{}, rerr
	}
	if diff := receiptDifference(existing, r); diff != "" {
		return guardReceipt{}, fmt.Errorf("%w: %s already has a %s receipt that differs (%s)",
			ErrGuardReceiptConflict, r.Key, r.Kind, diff)
	}
	return existing, nil
}

// readGuardReceipt reads one receipt by its unique key.
func readGuardReceipt(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID, unitID, kind string) (guardReceipt, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind("SELECT "+guardReceiptColumns+" FROM "+guardOnly(dia)+guardReceiptsTable+
		" WHERE rollout_id = ? AND unit_id = ? AND receipt_kind = ?"), rolloutID, unitID, kind)
	if err != nil {
		return guardReceipt{}, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return guardReceipt{}, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
		}
		return guardReceipt{}, sql.ErrNoRows
	}
	r, err := scanGuardReceipt(rows.Scan)
	if err != nil {
		return guardReceipt{}, err
	}
	return r, rows.Err()
}

// receiptDifference names the first field in which two receipts disagree, or "" when they
// are the same attribution.
//
// It compares the ATTRIBUTION and not the chain position, because a receipt re-written
// after a lost acknowledgement legitimately lands at the ordinal the first attempt already
// used — the row is the same claim about the same work. applied_at is likewise excluded: it
// is diagnostic, and comparing a wall clock would make every idempotent retry a conflict.
func receiptDifference(a, b guardReceipt) string {
	for _, f := range []struct {
		what string
		x, y string
	}{
		{"rollout_id", a.RolloutID, b.RolloutID},
		{"unit_id", a.UnitID, b.UnitID},
		{"receipt_kind", a.Kind, b.Kind},
		{"intent", string(a.Intent), string(b.Intent)},
		{"relation_schema", a.Key.Schema, b.Key.Schema},
		{"relation_name", a.Key.Relation, b.Key.Relation},
		{"trigger_name", a.Key.Trigger, b.Key.Trigger},
		{"epoch", fmt.Sprint(a.Epoch), fmt.Sprint(b.Epoch)},
		{"manifest_format", fmt.Sprint(a.Format), fmt.Sprint(b.Format)},
		{"code_sha256", hexDigest(a.CodeSHA256), hexDigest(b.CodeSHA256)},
		{"retained_revision", fmt.Sprint(a.RetainedRevision), fmt.Sprint(b.RetainedRevision)},
		{"retained_sha256", hexDigest(a.RetainedSHA256), hexDigest(b.RetainedSHA256)},
		{"spec_sha256", hexDigest(a.SpecSHA256), hexDigest(b.SpecSHA256)},
		{"definition_sha256", hexDigest(a.DefinitionSHA256), hexDigest(b.DefinitionSHA256)},
		{"prestate_sha256", hexDigest(a.PrestateSHA256), hexDigest(b.PrestateSHA256)},
		{"from_enable_state", a.FromEnableState.String(), b.FromEnableState.String()},
		{"to_enable_state", a.ToEnableState, b.ToEnableState},
		{"predecessor_receipt_id", a.PredecessorReceiptID.String(), b.PredecessorReceiptID.String()},
		{"attempt_id", a.AttemptID, b.AttemptID},
		// receipt_id LAST, and that ordering is the whole usefulness of this function.
		//
		// The id is a pure function of every field above it, so any substantive difference
		// changes it too — and reporting it first meant every message said "the ids differ",
		// which is the one thing the reader already knew and the one thing they cannot act on.
		// It stays in the list because a row whose id does NOT reproduce from its own body is a
		// real defect, and this is where that would surface if the callers' digest checks ever
		// stopped running.
		{"receipt_id", hexDigest(a.ReceiptID), hexDigest(b.ReceiptID)},
	} {
		if f.x != f.y {
			return fmt.Sprintf("%s: stored %q, this attempt %q", f.what, f.x, f.y)
		}
	}
	return ""
}

// guardRolloutReceipts reads every receipt of one rollout, keyed by unit.
func guardRolloutReceipts(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string) (map[string]guardReceipt, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind("SELECT "+guardReceiptColumns+" FROM "+guardOnly(dia)+guardReceiptsTable+
		" WHERE rollout_id = ? ORDER BY event_ordinal"), rolloutID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	defer rows.Close()
	out := map[string]guardReceipt{}
	var prev optDigest
	var expectedOrdinal int64 = 1
	for rows.Next() {
		r, serr := scanGuardReceipt(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		// The receipt stream is a chain like the other two, and it is verified on the same
		// terms: a stored digest nobody recomputes proves nothing.
		if r.EventOrdinal != expectedOrdinal {
			return nil, fmt.Errorf("%w: the receipt stream of rollout %s jumps from ordinal %d to %d",
				ErrGuardGateChainBroken, rolloutID, expectedOrdinal-1, r.EventOrdinal)
		}
		if r.PrevEventSHA256 != prev {
			return nil, fmt.Errorf("%w: receipt %d of rollout %s records predecessor %s, but its predecessor hashes to %s",
				ErrGuardGateChainBroken, r.EventOrdinal, rolloutID, r.PrevEventSHA256, prev)
		}
		body, berr := r.bodyDigest()
		if berr != nil {
			return nil, berr
		}
		if body != r.ReceiptID {
			return nil, fmt.Errorf("%w: receipt %s of rollout %s stores an id its own contents do not produce (%s)",
				ErrGuardGateChainBroken, hexDigest(r.ReceiptID), rolloutID, hexDigest(body))
		}
		chain, cerr := r.chainDigest()
		if cerr != nil {
			return nil, cerr
		}
		if chain != r.EventSHA256 {
			return nil, fmt.Errorf("%w: receipt %d of rollout %s stores chain digest %s but hashes to %s",
				ErrGuardGateChainBroken, r.EventOrdinal, rolloutID, hexDigest(r.EventSHA256), hexDigest(chain))
		}
		prev = someDigest(r.EventSHA256)
		expectedOrdinal++
		out[r.UnitID+"\x00"+r.Kind] = r
	}
	return out, rows.Err()
}

// guardOnly is the ONLY qualifier every read of the three control-plane logs carries.
//
// CLASSICAL INHERITANCE DEFEATS THIS LEDGER, and a bare table name is what lets it. Measured on
// PostgreSQL 15.18:
//
//	CREATE TABLE inh_child () INHERITS (inh_parent);
//	INSERT INTO inh_parent VALUES (1,'real');
//	INSERT INTO inh_child  VALUES (1,'forged');   -- SAME primary key, ACCEPTED
//	SELECT count(*) FROM inh_parent       -> 2
//	SELECT count(*) FROM ONLY inh_parent  -> 1
//	indexes on the child                  -> 0
//
// A child inherits the COLUMNS and the CHECK constraints and inherits neither the unique
// indexes nor the triggers. So an owner who creates one can append rows that every fold READS —
// because a bare name means "this table and its descendants" — while those rows carry no
// uniqueness, so the ordinal the chains are ordered by can repeat, and no immutability guard, so
// they can be updated or deleted at will. That is the ledger's entire ordering argument
// undone by one CREATE TABLE.
//
// TWO LAYERS, because each answers a different question. This one makes every READ describe the
// relation the shape verifies. The shape check refuses a relation that HAS a child, so the
// anomaly is reported rather than silently stepped over. Neither alone is enough: reading with
// ONLY and saying nothing would hide it, and refusing without ONLY would still have folded the
// child's rows on the way to the refusal.
//
// SQLite has no table inheritance and no ONLY keyword, so it is the empty string there.
func guardOnly(dia dialect.Dialect) string {
	if dia.Name() == store.EnginePostgres {
		return "ONLY "
	}
	return ""
}

// receiptLookupKey is how guardRolloutReceipts keys its map: unit and kind, separated by
// the one byte neither can contain.
func receiptLookupKey(unitID, kind string) string { return unitID + "\x00" + kind }

// sortedUnitIDs returns the map's keys in a deterministic order, for messages and for the
// ordered arrays the bulk query binds.
func sortedUnitIDs(m map[string]unitGateFold) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
