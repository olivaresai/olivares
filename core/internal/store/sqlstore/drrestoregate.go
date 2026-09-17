// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// ErrRestorePublicationFenced means the destination's own durable control forbids
// publishing a store for it. It is the refusal a pending, indeterminate,
// quarantined, damaged or lost control produces.
var ErrRestorePublicationFenced = errors.New("sqlstore: the destination's restore control does not permit publishing a store")

// drGateVerdict is what a destination's control says about publication.
type drGateVerdict uint8

const (
	// drGateAbsent is the ONLY verdict that can permit an ordinary boot, and only
	// when nothing witnesses that this destination was ever enrolled.
	drGateAbsent drGateVerdict = iota
	// drGateUnreadable is a read that FAILED. It is never folded into absent: "I
	// could not look" and "there is nothing there" authorize different actions, and
	// only one of them is publishing a store.
	drGateUnreadable
	// drGateMalformed is a control that exists with a shape, a cardinality or a
	// value this build did not write. It is refused, never adopted and never
	// repaired.
	drGateMalformed
	drGatePending
	drGateIndeterminate
	drGateQuarantined
	// drGateComplete permits publication only after its recorded destination — and
	// its keyset, where local evidence exists — are compared exactly.
	drGateComplete
)

func (v drGateVerdict) String() string {
	switch v {
	case drGateAbsent:
		return "absent"
	case drGateUnreadable:
		return "unreadable"
	case drGateMalformed:
		return "malformed"
	case drGatePending:
		return opgate.StatePending
	case drGateIndeterminate:
		return opgate.StateIndeterminate
	case drGateQuarantined:
		return opgate.StateQuarantined
	case drGateComplete:
		return opgate.StateComplete
	default:
		return "unknown"
	}
}

// drGate is one destination's control as read under the publication fence.
type drGate struct {
	Verdict  drGateVerdict
	Revision int64
	// OpID is present only when a well-formed control was actually read. It is
	// NEVER invented: an exclusive holder that has not yet installed its control
	// leaves this empty, and a busy diagnosis says so rather than substituting a PID
	// or a placeholder.
	OpID             string
	PlanSHA256       string
	KeysetSHA256     string
	Database         string
	Schema           string
	SystemIdentifier string
	Owner            string
	Cause            error
}

// RestoreEnrolmentWitness is durable LOCAL evidence, gathered by the composition
// root, that a destination carries a restore control.
//
// It is a closed value, not a capability. Its only possible effect is to TIGHTEN:
// it turns an absent control from the accepted `legacy_or_lost_unknown` limit into
// a refusal, and it supplies the custody digest a completed control is compared
// against. There is no field here that can turn a refusal into a success, which is
// the property that makes it safe to accept from a caller at all.
type RestoreEnrolmentWitness struct {
	// Enrolled reports that local evidence names this destination as one that
	// carries a control. Malformed or foreign enrolled evidence refuses; it cannot
	// be discarded to recover an unwitnessed legacy admission.
	Enrolled bool
	// Destination is the canonical destination the local evidence names.
	Destination opgate.PostgresDestination
	// KeysetSHA256 is the custody generation local evidence says was authorized,
	// when it says anything. Empty means "no evidence", never "no keyset".
	KeysetSHA256 string
}

// namesDestination reports whether this witness is about the destination in hand.
// A receipt copied from another estate proves nothing about this one.
func (w RestoreEnrolmentWitness) namesDestination(dest drDestination) bool {
	return w.Enrolled && dest.SystemIdentifierKnown && w.Destination == (opgate.PostgresDestination{Database: dest.Database, Schema: dest.Schema, SystemIdentifier: dest.SystemIdentifier}) && w.Destination.Validate() == nil
}

// readDRRestoreControlRow validates row values independently of SQL CHECK constraints.
// Production consumers reach it only through verifyDRRestoreControl.
func readDRRestoreControlRow(ctx context.Context, q rowQuerier) drGate {
	rows, err := q.QueryContext(ctx,
		`SELECT control_key, format, revision, state, op_id,
                pg_catalog.encode(plan_sha256, 'hex'),
                destination_database, destination_schema, destination_system_identifier,
                pg_catalog.encode(keyset_sha256, 'hex'), pg_catalog.encode(report_sha256, 'hex'),
                observed_at::pg_catalog.text
           FROM ONLY `+dialect.EngineSchema+`.`+dialect.DRRestoreControlTable)
	if err != nil {
		return drGate{Verdict: drGateUnreadable, Cause: fmt.Errorf("read the restore control: %w", err)}
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	var (
		g              drGate
		count          int
		key            string
		form           int64
		keyset, report sql.NullString
		observed       string
	)
	for rows.Next() {
		count++
		if count > 1 {
			_ = rows.Close()
			return drGate{Verdict: drGateMalformed, Cause: errors.New("the restore control is not a singleton: more than one row exists")}
		}
		var state string
		if err := rows.Scan(&key, &form, &g.Revision, &state, &g.OpID, &g.PlanSHA256,
			&g.Database, &g.Schema, &g.SystemIdentifier, &keyset, &report, &observed); err != nil {
			_ = rows.Close()
			return drGate{Verdict: drGateUnreadable, Cause: fmt.Errorf("decode the restore control: %w", err)}
		}
		switch state {
		case opgate.StatePending:
			g.Verdict = drGatePending
		case opgate.StateIndeterminate:
			g.Verdict = drGateIndeterminate
		case opgate.StateQuarantined:
			g.Verdict = drGateQuarantined
		case opgate.StateComplete:
			g.Verdict = drGateComplete
		default:
			_ = rows.Close()
			return drGate{Verdict: drGateMalformed, Cause: fmt.Errorf("the restore control carries state %q, which this build never writes", state)}
		}
	}
	if err := rows.Err(); err != nil {
		return drGate{Verdict: drGateUnreadable, Cause: fmt.Errorf("read the restore control: %w", err)}
	}
	// A relation that exists with NO row is damage, not absence. The row is written
	// in the same transaction as the relation, so a control with none has lost it.
	if count == 0 {
		return drGate{Verdict: drGateMalformed, Cause: errors.New("the restore control relation exists with no row, so its state has been lost")}
	}
	if key != dialect.DRRestoreControlKey {
		return drGate{Verdict: drGateMalformed, Cause: fmt.Errorf("the restore control's singleton key is %q, not %q", key, dialect.DRRestoreControlKey)}
	}
	if form != opgate.Format {
		return drGate{Verdict: drGateMalformed, Cause: fmt.Errorf("the restore control's format is %d and this build reads %d", form, opgate.Format)}
	}
	if g.Revision <= 0 {
		return drGate{Verdict: drGateMalformed, Cause: fmt.Errorf("the restore control's revision %d is not a compare-and-set predecessor", g.Revision)}
	}
	if err := validControlHex("operation id", g.OpID, 32); err != nil {
		return drGate{Verdict: drGateMalformed, Cause: err}
	}
	if err := validControlHex("plan digest", g.PlanSHA256, 64); err != nil {
		return drGate{Verdict: drGateMalformed, Cause: err}
	}
	if err := (opgate.PostgresDestination{Database: g.Database, Schema: g.Schema, SystemIdentifier: g.SystemIdentifier}).Validate(); err != nil {
		return drGate{Verdict: drGateMalformed, Cause: err}
	}
	if observed == "" || observed == "infinity" || observed == "-infinity" {
		return drGate{Verdict: drGateMalformed, Cause: errors.New("restore control has no finite observation instant")}
	}
	if report.Valid {
		if err := validControlHex("report digest", report.String, 64); err != nil {
			return drGate{Verdict: drGateMalformed, Cause: err}
		}
	}
	if g.Verdict == drGateComplete {
		if !keyset.Valid {
			return drGate{Verdict: drGateMalformed, Cause: errors.New("complete restore control has no keyset digest")}
		}
		if err := validControlHex("keyset digest", keyset.String, 64); err != nil {
			return drGate{Verdict: drGateMalformed, Cause: err}
		}
		g.KeysetSHA256 = keyset.String
	} else if keyset.Valid {
		return drGate{Verdict: drGateMalformed, Cause: errors.New("noncomplete restore control carries a keyset digest")}
	}
	return g
}

func validControlHex(label, value string, want int) error {
	if len(value) != want {
		return fmt.Errorf("the restore control's %s is %d characters and this build writes %d", label, len(value), want)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("the restore control's %s is not hexadecimal: %w", label, err)
	}
	if strings.ToLower(value) != value {
		return fmt.Errorf("the restore control's %s is not lowercase", label)
	}
	return nil
}

// errDRControlShape marks a relation that carries the control's NAME and a shape
// this build did not write. It is a distinct sentinel from an unreadable catalog so
// the verdict can be malformed rather than unknown.
var errDRControlShape = errors.New("the restore control's shape is not the one this build declares")

// judgeDRRestoreGate turns a verdict into permission or refusal.
//
// The four-row table it implements is the ratified one, and the two absent rows are
// the ones worth stating out loud:
//
//   - absent WITH a surviving witness for this destination is a LOSS. The control
//     was there and is not, so the destination is refused and recovered, never
//     reinstalled as clean.
//   - absent WITHOUT any witness is `legacy_or_lost_unknown`: on a new node that
//     lost the control and every witness, a genuine legacy estate and a destroyed
//     enrolment are indistinguishable. Root accepted that limit explicitly. The
//     ordinary boot continues with ALL of its guards; nothing is rebated, and no
//     "clean" control is written to convert not-knowing into retrospective proof.
func judgeDRRestoreGate(g drGate, dest drDestination, w RestoreEnrolmentWitness) error {
	if w.Enrolled && !w.namesDestination(dest) || !w.Enrolled && (w.Destination != (opgate.PostgresDestination{}) || w.KeysetSHA256 != "") {
		return fmt.Errorf("%w: local restore evidence is not explicitly bound to this PostgreSQL destination", ErrRestorePublicationFenced)
	}
	switch g.Verdict {
	case drGateAbsent:
		if w.namesDestination(dest) {
			return fmt.Errorf("%w: %s.%s carries no restore control, and local evidence says it was enrolled in one — a control that was installed and is now gone is a LOSS, so this destination is refused rather than reinstalled as clean; recover it to a provisioned empty destination",
				ErrRestorePublicationFenced, dest.Database, dest.Schema)
		}
		slog.Debug("this destination carries no restore control and nothing witnesses that it ever did, so its enrolment is legacy_or_lost_unknown; the ordinary boot continues with every guard it already had",
			"database", dest.Database, "schema", dest.Schema)
		return nil
	case drGateUnreadable:
		return fmt.Errorf("%w: %s.%s has a restore control this boot could not read, and a read that failed is not an absence: %v",
			ErrRestorePublicationFenced, dest.Database, dest.Schema, g.Cause)
	case drGateMalformed:
		return fmt.Errorf("%w: %s.%s carries a restore control this build did not write, and it is refused rather than adopted or repaired: %v",
			ErrRestorePublicationFenced, dest.Database, dest.Schema, g.Cause)
	case drGatePending, drGateIndeterminate, drGateQuarantined:
		return fmt.Errorf("%w: %s.%s has a restore control in state %q for operation %s; resolve that operation before a store is published for this destination",
			ErrRestorePublicationFenced, dest.Database, dest.Schema, g.Verdict, g.OpID)
	case drGateComplete:
		return judgeCompleteDRRestoreGate(g, dest, w)
	default:
		return fmt.Errorf("%w: %s.%s produced an unclassified restore control verdict",
			ErrRestorePublicationFenced, dest.Database, dest.Schema)
	}
}

// judgeCompleteDRRestoreGate spends the comparisons a completed control exists to
// make. A control that says "complete" for SOMEWHERE ELSE is the sharpest failure
// this fence can catch: it is what a capsule or a copied estate carries.
func judgeCompleteDRRestoreGate(g drGate, dest drDestination, w RestoreEnrolmentWitness) error {
	if ok, why := dest.matches(g.Database, g.Schema, g.SystemIdentifier); !ok {
		return fmt.Errorf("%w: this session serves %s, and the restore control here records a completed operation for another destination; it is not evidence about this one",
			ErrRestorePublicationFenced, why)
	}
	// The cluster identity is compared when BOTH sides know it. A control that
	// records one while this session could not read its own cannot be compared, and
	// an uncomparable completed control is refused rather than assumed to match —
	// the same rule the DR pre-flight already applies to an unreadable identity.
	if g.SystemIdentifier != "" && !dest.SystemIdentifierKnown {
		return fmt.Errorf("%w: %s.%s carries a completed restore control bound to a cluster identity, and this session could not read its own to compare it (%v); grant EXECUTE on pg_catalog.pg_control_system() or recover to a destination this build can identify",
			ErrRestorePublicationFenced, dest.Database, dest.Schema, dest.SystemIdentifierErr)
	}
	// The custody comparison happens where evidence exists. Local evidence naming a
	// different keyset than the control authorized means this node would serve the
	// destination with custody the completed operation did not publish.
	if err := validControlHex("keyset digest", g.KeysetSHA256, 64); err != nil {
		return fmt.Errorf("%w: %v", ErrRestorePublicationFenced, err)
	}
	if w.KeysetSHA256 != "" && w.KeysetSHA256 != g.KeysetSHA256 {
		return fmt.Errorf("%w: %s.%s completed under keyset %s and the local evidence on this node names %s; the node's custody selection is not the one the completed operation published",
			ErrRestorePublicationFenced, dest.Database, dest.Schema, g.KeysetSHA256, w.KeysetSHA256)
	}
	return nil
}

// judgeLocalRestoreControl is the SQLite half, and it is deliberately the whole
// story there: a SQLite estate's control cannot live inside the database, because
// the database is the file a restore replaces, so the external record IS the gate
// and its own absence IS the absence of a witness.
func judgeLocalRestoreControl(anchor opgate.Anchor, rec opgate.Record, present bool, readErr error) error {
	if readErr != nil {
		return fmt.Errorf("%w: %s has a restore control this boot could not read, and a read that failed is not an absence: %v",
			ErrRestorePublicationFenced, anchor.Canonical(), readErr)
	}
	if !present {
		slog.Debug("this destination carries no local restore control, so its enrolment is legacy_or_lost_unknown; the ordinary boot continues with every guard it already had",
			"destination", anchor.Canonical())
		return nil
	}
	if rec.Blocks() {
		return fmt.Errorf("%w: %s has a restore control in state %q for operation %s; resolve that operation before a store is published for this destination",
			ErrRestorePublicationFenced, anchor.Canonical(), rec.State, rec.OpID)
	}
	return nil
}

// restorePublicationFence is the coordination ONE preparation holds, from before
// its first refusal to after its decision to hand back a store.
//
// It is deliberately not stored on sqlStore and not returned to anybody. These
// locks coordinate CONSTRUCTION; they are not a protocol for revoking stores that
// were already published, and pretending otherwise by keeping the session alive
// inside a serving store would advertise a guarantee the product does not have.
type restorePublicationFence struct {
	coord   *drCoordination
	lease   *opgate.Lease
	anchor  opgate.Anchor
	roles   restoreControlRoles
	witness RestoreEnrolmentWitness
}

// publicationInputs is everything the DR contract contributes to one preparation.
//
// It is ONE parameter rather than five because the five are not independent: the
// witness is the local evidence the admission read, the requirement is frozen from
// it and the control, the observation answers that requirement, and the retained
// PostgreSQL admission is the session all of it was decided on. A caller that could
// supply them separately could supply a requirement from one destination with an
// observation from another.
//
// Its zero value is the ordinary direct Open: no boot above it, no local evidence,
// no requirement and no measurement.
type publicationInputs struct {
	// cfg is the destination configuration. It is carried here for the boot path,
	// whose Config is the one the admission froze at acquisition.
	cfg store.Config
	// witness is durable LOCAL evidence and can only tighten.
	witness RestoreEnrolmentWitness
	// admission is the boot's local lease owner, when a boot handed one down.
	admission *LocalAdmission
	// pub is the RETAINED PostgreSQL admission the boot already acquired. When it is
	// present this preparation does not acquire a second one: the session that read
	// the control before the keys were loaded is the session that decides.
	pub *publicationAdmission
	// req is the frozen custody requirement, and observed is the measurement of the
	// signers this boot actually loaded. Neither is derived from the other.
	req      CustodyRequirement
	observed CustodyObservation
	// serving records whether THIS preparation publishes a serving Store, derived
	// once from the closed preparation purpose and the actual maintenance caller.
	// See servingPublication.
	serving bool
}

// servingPublication derives, from the preparation's own closed purpose and its actual
// maintenance caller, whether this call ends by handing back a SERVING Store.
//
// ⛔ IT IS THE DISTINCTION IR5-C2 EXISTS TO RESTORE, and it is derived rather than
// supplied. There is no field a caller can set, no bypass boolean on store.Config and
// no context value: the two inputs are the private preparePurpose this package already
// owns and whether an internal maintenance callback was passed, and both are decided by
// which internal constructor was entered.
//
// The two requirements it separates are NOT the same requirement:
//
//   - EVERY mutating preparation must pass the destination's restore-control gate under
//     an admission it holds. Schema-only migration and directory maintenance mutate, so
//     they keep the gate, the pre-mutation admission check and an explicit final
//     successful-completion decision. None of that is conditional on this flag.
//   - ONLY a serving Store publication must additionally prove the ACTUAL SELECTED
//     SIGNER CUSTODY a completed enrolled control authorized. That duty belongs to the
//     path that loads the three signing keys and hands back a Store that signs with
//     them. `ApplyMigrations` loads no key and returns no Store; directory maintenance
//     runs a closed callback and returns no Store. Demanding an observation from them
//     was not a stricter reading of the contract, it was a requirement no caller could
//     ever satisfy — `migrate apply` on a restored destination applied its schema and
//     then refused, on every attempt, forever (independent review F1).
//
// It is emphatically NOT permission to skip the final decision. The custody LEG is what
// this gates; `observeHeld`, the control re-verification and reconcileRequirement run
// for every purpose, and a completed record is never discarded for any caller.
func servingPublication(purpose preparePurpose, maintenance func(*sqlStore) error) bool {
	return purpose == prepareThroughReadiness && maintenance == nil
}

type publicationAdmissionState uint8

const (
	admissionAcquired publicationAdmissionState = iota
	admissionRefusedOrLost
	admissionDecided
	admissionClosed
)

// publicationAdmission is the private PostgreSQL publication lifetime. It retains
// the original coordination session through the final decision. Finite state:
// acquired → refused-or-lost | decided (linearization L) → closed.
type publicationAdmission struct {
	mu    sync.Mutex
	state publicationAdmissionState
	fence *restorePublicationFence
	// req is the custody requirement frozen when this admission was acquired — before
	// the caller loaded a key. The final decision compares the boot's measurement
	// against THIS value and never against a control re-read into an "observed"
	// struct, which is the laundering F3-IR-5 names.
	req CustodyRequirement
}

// publicationFinalDecisionTestHook runs at the last readiness boundary, before
// the retained-session observation. Nil outside tests.
var publicationFinalDecisionTestHook func(*publicationAdmission)

// publicationAfterLinearizationTestHook runs after a successful server-side
// decision L and before local success/cleanup. Nil outside tests.
var publicationAfterLinearizationTestHook func(*publicationAdmission)

// publicationCleanupTestHook observes unlock/retirement. afterL distinguishes
// pre-decision failure from post-L cleanup uncertainty. Nil outside tests.
var publicationCleanupTestHook func(afterL, confirmed bool, err error)

// acquireLocalPublicationFence takes a SQLite destination's fence with ONE try and
// judges its control.
//
// ⛔ IT IS CALLED BEFORE THE POOL EXISTS, and that position is F3-IR-9. The old one
// ran at the same place as PostgreSQL's, after openDB — and openSQLite forces its
// first connection to apply the pragmas, so the driver had already CREATED the
// destination. Measured: an ordinary Open against a not-yet-existing target that a
// separate process held exclusively refused correctly and left a 4096-byte SQLite
// file behind. The comment above the old call said everything before it was pool
// construction and catalog reads; for SQLite that was simply false.
//
// So SQLite fences here, before openDB, before the first connection, before the
// query pragmas, before the WAL and SHM sidecars and before the file-mode
// restriction. PostgreSQL keeps its own position: its fence needs a session, and
// its read-only role diagnostics before that point really are non-mutating.
//
// The destination is the RESOLVED target, never the caller's spelling: fencing the
// canonical path and opening an alias of it is F3-IR-2 and it is the same bug from
// the other side.
//
// admission is the shared local ownership a boot already holds. When it is present
// the fence is a DERIVED CHILD of that live shared root — not a second, unrelated
// acquisition, which is what would make the boot conflict with itself. When it is
// absent (a direct Open) the fence takes its own root lease, and if some other
// operation in this process holds the destination exclusively it is refused exactly
// as a separate process would be.
func acquireLocalPublicationFence(target opgate.SQLiteTarget, admission *LocalAdmission) (*restorePublicationFence, CustodyRequirement, error) {
	anchor := target.Anchor()
	if target.Memory() || anchor.Zero() {
		// An in-memory store has no destination to fence and no custody to lose.
		// Returning an empty fence keeps the caller's release unconditional.
		return &restorePublicationFence{}, CustodyRequirement{}, nil
	}
	if admission != nil {
		lease, err := admission.deriveStoreLease(anchor)
		if err != nil {
			return nil, CustodyRequirement{}, err
		}
		f := &restorePublicationFence{lease: lease, anchor: anchor}
		rec, recPresent, readErr := lease.Read(anchor)
		if jerr := judgeLocalRestoreControl(anchor, rec, recPresent, readErr); jerr != nil {
			f.release()
			return nil, CustodyRequirement{}, jerr
		}
		// NO REQUIREMENT IS DERIVED HERE. A boot admission froze its own from the same
		// installation's records before the loaders ran, and that frozen value is the
		// one this preparation is bound to. Deriving a second one from a second read
		// would be exactly the independent local authority source the contract forbids.
		return f, CustodyRequirement{}, nil
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeShared, anchor)
	if err != nil {
		return nil, CustodyRequirement{}, fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown, err)
	}
	if !ok {
		// No operation identity is offered here, and that is deliberate. Reading the
		// record without the lease would report a generation that may already have
		// been replaced, and the ratified contract is explicit that the fenced
		// diagnosis is sufficient on its own.
		//
		// The SPELLING is reported alongside the destination when they differ, because
		// the alias defect is exactly the case where an operator sees a refusal for a
		// path they did not type. It is a diagnostic string; nothing resolves it again.
		if spelling := target.Input(); spelling != "" && spelling != anchor.Canonical() {
			return nil, CustodyRequirement{}, fmt.Errorf("%w: %s (named here as %q) is held by another operation; nothing here waits for it to finish",
				ErrRestorePublicationBusy, anchor.Canonical(), spelling)
		}
		return nil, CustodyRequirement{}, fmt.Errorf("%w: %s is held by another operation; nothing here waits for it to finish",
			ErrRestorePublicationBusy, anchor.Canonical())
	}
	f := &restorePublicationFence{lease: lease, anchor: anchor}
	rec, recPresent, readErr := lease.Read(anchor)
	if jerr := judgeLocalRestoreControl(anchor, rec, recPresent, readErr); jerr != nil {
		f.release()
		return nil, CustodyRequirement{}, jerr
	}
	// THE DIRECT LEG FREEZES ITS REQUIREMENT HERE, from the record this lease just read.
	//
	// A SQLite estate's control cannot live inside the database — the database is the
	// file a restore replaces — so this external record IS the gate, and its completed
	// keyset is the only statement of the custody that operation published. Without
	// this, a direct Open of a completed SQLite target published a store having consulted
	// nothing about custody at all, while the same call on PostgreSQL refused
	// (independent review F2). The contract is engine-neutral: "SQLite uses its resolved
	// local control under the same lease".
	//
	// It is derived from the record already read UNDER THE LEASE, not from a second read
	// and not from a witness. A witness can tighten uncertainty; it can neither supply
	// the actual observation nor authorize complete.
	return f, localCompletedRequirement(anchor, rec, recPresent), nil
}

// localCompletedRequirement freezes the custody a COMPLETE local record authorized.
// Anything else — absent, or a state judgeLocalRestoreControl already refused — yields
// the zero requirement, which demands nothing.
func localCompletedRequirement(anchor opgate.Anchor, rec opgate.Record, present bool) CustodyRequirement {
	if !present || rec.State != opgate.StateComplete || rec.Keyset.SHA256 == "" {
		return CustodyRequirement{}
	}
	return completedCustodyRequirement(anchor.Canonical(), rec.OpID, rec.PlanSHA256, rec.Keyset.SHA256, rec.Keyset)
}

// acquirePostgresPublicationFence takes the destination's own advisory key with ONE
// try and reads the durable control on the very session that holds it.
//
// Its position is unchanged by this correction: everything above it is pool
// construction and catalog reads whose refusals are SHARPER than a fence error,
// and everything below it can commit. This reader uses the closed control verifier.
// The final decision on the original session (IR-3) and the actual selected-custody
// comparison (IR-5) are closed; retained destination agreement is IR-6.
func acquirePostgresPublicationFence(ctx context.Context, cfg store.Config, witness RestoreEnrolmentWitness) (*restorePublicationFence, drGate, error) {
	coord, err := openDRCoordinationShared(ctx, cfg)
	if err != nil {
		return nil, drGate{}, err
	}
	f := &restorePublicationFence{coord: coord, witness: witness}
	bctx, cancel := context.WithTimeout(ctx, drCoordinationTimeout)
	defer cancel()
	roles, rerr := resolveDRControlRoles(bctx, cfg)
	if rerr != nil {
		f.release()
		return nil, drGate{}, fmt.Errorf("%w: resolve restore control roles: %v", ErrRestoreCoordinationUnknown, rerr)
	}
	f.roles = roles
	gate := verifyDRRestoreControl(bctx, coord.querier(), coord.destination(), roles)
	if jerr := judgeDRRestoreGate(gate, coord.destination(), witness); jerr != nil {
		f.release()
		return nil, drGate{}, jerr
	}
	return f, gate, nil
}

// beginPostgresPublicationAdmission acquires the shared session, judges the control
// on it, and FREEZES the custody requirement that control produces.
//
// The requirement is returned to the caller because the boot has to read it before it
// loads a key: it is what selects the strict enrolled load over the ordinary
// mint-on-absent one. It is also retained here, so the final decision compares
// against the expectation taken at acquisition rather than one re-read later.
//
// localComplete is the COMPLETE local record's keyset where local evidence exists.
// It contributes the expected per-purpose selection ONLY — the authoritative digest
// is the destination's own, and judgeDRRestoreGate has already refused if the two
// disagree.
func beginPostgresPublicationAdmission(ctx context.Context, cfg store.Config, witness RestoreEnrolmentWitness, localComplete opgate.Keyset) (*publicationAdmission, CustodyRequirement, error) {
	fence, gate, err := acquirePostgresPublicationFence(ctx, cfg, witness)
	if err != nil {
		return nil, CustodyRequirement{}, err
	}
	var req CustodyRequirement
	if gate.Verdict == drGateComplete {
		dest := fence.coord.destination()
		req = completedCustodyRequirement(dest.Database+"."+dest.Schema, gate.OpID, gate.PlanSHA256, gate.KeysetSHA256, localComplete)
	}
	return &publicationAdmission{state: admissionAcquired, fence: fence, req: req}, req, nil
}

func (a *publicationAdmission) requireMutating(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != admissionAcquired || a.fence == nil || a.fence.coord == nil {
		return fmt.Errorf("%w: publication admission is not held for mutation", ErrRestoreCoordinationUnknown)
	}
	if err := a.fence.coord.observeHeld(ctx); err != nil {
		a.state = admissionRefusedOrLost
		return err
	}
	return nil
}

// decide is the last readiness observation on the original session. A successful
// server response is linearization point L. It never starts another fallible
// preparation phase.
func (a *publicationAdmission) decide(ctx context.Context, observed CustodyObservation, serving bool) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != admissionAcquired || a.fence == nil || a.fence.coord == nil {
		return fmt.Errorf("%w: publication admission is not held for the final decision", ErrRestoreCoordinationUnknown)
	}
	if publicationFinalDecisionTestHook != nil {
		publicationFinalDecisionTestHook(a)
	}
	// THE WHOLE FINAL OBSERVATION SHARES ONE FINITE BOUND, and it is the same one
	// the acquisition already spends on the same question.
	//
	// It is DERIVED from the caller's context, so an earlier deadline and any
	// cancellation still win: this shortens the wait, it never extends it, and it
	// is neither a per-query deadline reset nor a background context that would
	// outlive the boot that owns it. The lock observation and the control
	// verification share it because a bound that covered only the first one would
	// leave the second free to block forever.
	//
	// Without it the boot's context is whatever `serve` passed down — in production
	// cmd.Context(), which carries no deadline — so a wedged server or a blackholed
	// socket at the last readiness boundary would hold the boot open indefinitely
	// WHILE STILL HOLDING THE SHARED PUBLICATION FENCE. That is exactly the state
	// drCoordinationTimeout exists to prevent, and the ratified contract asks for a
	// bounded read-only final observation. Expiry here is a pre-L refusal: the
	// caller closes the would-be Store, elector and pools, and bootOK stays false.
	dctx, cancel := context.WithTimeout(ctx, drCoordinationTimeout)
	defer cancel()
	if err := dctx.Err(); err != nil {
		a.state = admissionRefusedOrLost
		return fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown, err)
	}
	if err := a.fence.coord.observeHeld(dctx); err != nil {
		a.state = admissionRefusedOrLost
		return err
	}
	dest := a.fence.coord.destination()
	gate := verifyDRRestoreControl(dctx, a.fence.coord.querier(), dest, a.fence.roles)
	// The gate is judged against the LOCAL EVIDENCE this admission read, exactly as at
	// acquisition. Passing the measurement here instead would be the laundering: the
	// witness answers "was this destination enrolled", the observation answers "what
	// did this boot load", and one is not a substitute for the other.
	if jerr := judgeDRRestoreGate(gate, dest, a.fence.witness); jerr != nil {
		a.state = admissionRefusedOrLost
		return jerr
	}
	// The control must still be the one the requirement was frozen from. A control
	// replaced under the boot — a different operation, plan or custody generation —
	// is a refusal, never a silently updated expectation.
	if cerr := a.reconcileRequirement(gate); cerr != nil {
		a.state = admissionRefusedOrLost
		return cerr
	}
	// THE CUSTODY LEG, AND ONLY THIS LEG, IS THE SERVING PUBLICATION'S.
	//
	// Everything above ran for every purpose: the retained session still holds the exact
	// key, the control still verifies, and it is still the one this requirement was
	// frozen from. What a schema-only migration and a directory maintenance callback do
	// NOT owe is an observation of loaded signing custody, because neither loads a
	// signing key or returns a Store that signs with one. See servingPublication.
	if serving {
		if cerr := judgeObservedCustody(a.req, observed); cerr != nil {
			a.state = admissionRefusedOrLost
			return cerr
		}
	}
	a.state = admissionDecided
	if publicationAfterLinearizationTestHook != nil {
		publicationAfterLinearizationTestHook(a)
	}
	return nil
}

// reconcileRequirement proves the destination's control at the last readiness
// boundary is still the one this admission froze its expectation from.
//
// It compares; it never adopts. A requirement that followed the control would make
// the whole comparison circular: a restore that completed under different custody
// while this boot was loading keys would simply move the target.
func (a *publicationAdmission) reconcileRequirement(gate drGate) error {
	complete := gate.Verdict == drGateComplete
	if complete != a.req.required {
		return fmt.Errorf("%w: this destination's restore control changed between the admission that read it and the final decision (%q now, custody %s then)",
			ErrRestorePublicationFenced, gate.Verdict, boolWord(a.req.required, "required", "not required"))
	}
	if !complete {
		return nil
	}
	if gate.OpID != a.req.opID || gate.PlanSHA256 != a.req.planSHA256 || gate.KeysetSHA256 != a.req.keysetSHA256 {
		return fmt.Errorf("%w: this destination's completed restore control was replaced between the admission that read it and the final decision; the custody this boot loaded was selected against operation %s and the control now names %s",
			ErrRestorePublicationFenced, a.req.opID, gate.OpID)
	}
	return nil
}

func boolWord(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// decidePublication is the ONE last-readiness-boundary decision, for both engines.
//
// PostgreSQL decides on its retained session (the final server observation is
// linearization point L) and compares custody there. SQLite has no session to
// observe — its control is the external record the lease already fences — so the
// custody comparison is the whole decision.
// The serving flag is the SAME derived value the early check used: it is computed once
// in openPrepared and carried, so the two checks can never disagree about which caller
// this is.
func decidePublication(ctx context.Context, in publicationInputs, pub *publicationAdmission) error {
	if pub != nil {
		return pub.decide(ctx, in.observed, in.serving)
	}
	if !in.serving {
		return nil
	}
	return judgeObservedCustody(in.req, in.observed)
}

func (a *publicationAdmission) close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == admissionClosed {
		return nil
	}
	afterL := a.state == admissionDecided
	a.state = admissionClosed
	// Read the destination BEFORE the release: releaseChecked drops the
	// coordination session, and a cleanup fact that cannot name the destination
	// the next boot may still find fenced is not an operator fact.
	var dest drDestination
	if a.fence != nil && a.fence.coord != nil {
		dest = a.fence.coord.destination()
	}
	confirmed, err := a.fence.releaseChecked()
	if publicationCleanupTestHook != nil {
		publicationCleanupTestHook(afterL, confirmed, err)
	}
	if afterL {
		if err != nil || !confirmed {
			slog.Warn("restore publication cleanup is uncertain after linearization; the session is retired and this is not a pre-decision exclusion failure",
				"database", dest.Database, "schema", dest.Schema, "confirmed", confirmed, "err", err)
		}
		return nil
	}
	if err != nil || !confirmed {
		// THE PRE-DECISION HALF OF THE SAME FACT, and it has to be said HERE.
		//
		// The boot still fails and this error is still returned. But its only caller
		// is openPrepared's deferred close, which discards it, so without this line
		// an unconfirmed release before the decision is SILENT — while the exclusive
		// installer path deliberately joins the same fact and the local lease leg of
		// releaseChecked warns about its own half. One warning per admission: close()
		// runs its body once (the state machine ends at admissionClosed) and a
		// PostgreSQL admission has no lease leg, so nothing below can repeat it.
		//
		// It is deliberately NOT the post-L message above: that one accompanies a
		// store that was published, this one accompanies a boot that was refused.
		// Both carry the destination and the release verdict only — never a DSN,
		// credential or raw connection string.
		slog.Warn("the restore publication fence could not be confirmed released before the final decision; the session is retired and this boot is refused",
			"database", dest.Database, "schema", dest.Schema, "confirmed", confirmed, "err", err)
		return fmt.Errorf("%w: publication fence release failed before the final decision (confirmed=%t): %v",
			ErrRestoreCoordinationUnknown, confirmed, err)
	}
	return nil
}

// release gives the fence back on EVERY path.
//
// A release that could not be confirmed is reported rather than swallowed: it
// leaves a destination that may still look fenced to the next boot, which is an
// operator fact. It does not fail a preparation that already succeeded — the
// session is retired either way, which releases the lock server-side, and
// destroying a healthy store over an unconfirmed unlock would be the worse
// outcome of the two.
func (f *restorePublicationFence) release() {
	_, _ = f.releaseChecked()
}

func (f *restorePublicationFence) releaseChecked() (bool, error) {
	if f == nil {
		return true, nil
	}
	confirmed := true
	var firstErr error
	if f.coord != nil {
		if err := f.coord.close(); err != nil {
			confirmed = false
			firstErr = err
		}
		f.coord = nil
	}
	if f.lease != nil {
		if err := f.lease.Release(); err != nil {
			confirmed = false
			if firstErr == nil {
				firstErr = err
			}
			slog.Warn("the local restore control lease could not be confirmed released",
				"destination", f.anchor.Canonical(), "err", err)
		}
		f.lease = nil
	}
	return confirmed, firstErr
}
