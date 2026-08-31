// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

const (
	defaultMaxActiveTasksPerSubject = 32
	maxTrackedTaskSubjects          = 1024
	// maxTrackedCollisions bounds the ambiguous-duplicate parking list (F-05):
	// an upstream that keeps re-issuing an already tracked handle must not grow
	// the process without bound. The bound is generous — a collision is an
	// upstream protocol fault, not a normal event — and overflow is reported to
	// the caller so it can be alerted rather than silently dropped.
	maxTrackedCollisions = 256
	// maxRetiredGenerations bounds the generation TOMBSTONE set (round-2 N-03).
	// A tombstone records that one task GENERATION is gone (released after a
	// proven cancellation, or TTL-evicted), so no late operation can act on it
	// and no stale cancellation bar can be attributed to a REPLACEMENT task that
	// reuses the same textual identifier. Evicting the oldest tombstone is safe:
	// every generation-scoped check also compares against the live record, which
	// no longer exists for a retired generation.
	maxRetiredGenerations = 4096
	// taskRetentionHeadroomFactor bounds the RETAINED inventory (round-4 R4-05).
	//
	// `quarantine` deliberately bypasses the ACTIVE caps and quarantined/
	// reconciling/cancellation-unconfirmed records never expire, so the active cap
	// alone bounds nothing: a caller sitting at its cap produced one fresh,
	// non-expiring orphan per tools/call and `byID` grew without limit (the
	// reviewer's probe reached 513 entries under a cap of 1). The retained bound
	// is the active cap PLUS one full cap of orphan headroom, so:
	//
	//   - a saturated owner can still record every orphan created by calls that
	//     were admitted before saturation (nothing live is ever dropped), and
	//   - once the headroom is spent, further TASK-PRODUCING forwards are refused
	//     BEFORE the upstream can create anything (deny-closed), until the operator
	//     drains the retained records through the reconciliation surface.
	//
	// It is never a license to delete a live orphan: the only exits are a proven
	// terminal confirmation (release) and explicit operator retirement.
	taskRetentionHeadroomFactor = 2
)

var (
	errTaskSubjectCap = errors.New("active task limit reached for subject")
	errTaskGlobalCap  = errors.New("active task limit reached for gateway")
	// errTaskDuplicateID is DISTINCT from the capacity errors (review round-1
	// F-05): a colliding upstream task id means the id the gateway is about to
	// govern already names ANOTHER governed task. Compensating it would cancel
	// the EXISTING task (a wrong-target actuation / authorization bypass), so the
	// caller must quarantine and alert instead of canceling.
	errTaskDuplicateID = errors.New("task id already tracked")
	errTaskMissingID   = errors.New("task id is required")
	// errTaskAmbiguousID refuses a task id whose stored spelling could differ
	// from the bound/forwarded spelling (F-10): the ledger no longer trims, so an
	// untrimmed id can never resolve a differently spelled record.
	errTaskAmbiguousID = errors.New("task id carries leading/trailing whitespace (ambiguous)")
	// errTaskCollisionOverflow reports that an ambiguous duplicate could not even
	// be parked. Never silent: the caller alerts.
	errTaskCollisionOverflow = errors.New("ambiguous task-id collision list is full")
	// errTaskGenerationStale is the compare-and-swap refusal of round-2 N-03: the
	// record the caller authorized is no longer the record that holds this task
	// id (it expired, was released, or a replacement task took the identifier).
	errTaskGenerationStale = errors.New("task record generation is stale (the authorized record no longer holds this id)")
	// errTaskNotOperable refuses an effect against a record that is not a live
	// governance record: a pending (unsettled) registration, a quarantined
	// orphan, or a record retained only for reconciliation (round-2 N-06).
	errTaskNotOperable = errors.New("task record is not operable")
	// errTaskCancellationUnconfirmed refuses NEW INPUT (tasks/update) to a record
	// whose cancellation the gateway requested or dispatched but never had
	// confirmed terminal (round-4 R4-04). Distinct from errTaskNotOperable: such a
	// record is still readable through the authoritative tasks/get, which is the
	// only thing that can resolve it.
	errTaskCancellationUnconfirmed = errors.New("task cancellation is unconfirmed; the task accepts no further input until reconciled")
	// errTaskInventorySaturated refuses a task-PRODUCING forward because the
	// caller's (or the gateway's) RETAINED task inventory is full (round-4 R4-05).
	// It is deliberately raised BEFORE the upstream call: quarantine bypasses the
	// active caps by design — forgetting a live external task is the failure being
	// prevented — so a caller that keeps making calls at its cap produced a fresh
	// orphan per call and grew the ledger without bound. The bound must therefore
	// be enforced where a NEW task can still be prevented, not after one exists.
	errTaskInventorySaturated = errors.New("retained task inventory is saturated; reconcile the retained records before creating more tasks")
)

// TaskRecord is the connector-side governance record for one durable MCP task.
type TaskRecord struct {
	TaskID string
	Tool   string
	// Subject/IsDelegated/ActAs/Issuer/ClientID form the CANONICAL OWNER tuple
	// (review round-1 F-06). The historical record carried the bare `sub` only,
	// so two trusted issuers minting the same subject — or the same agent acting
	// on behalf of two different principals — could operate each other's tasks
	// while producing internally valid evidence. Ownership is now compared over
	// the issuer-qualified, delegation-aware, client-isolated tuple.
	Subject     string
	IsDelegated bool
	ActAs       string
	Issuer      string
	ClientID    string

	Tenant         string
	RequiredScope  string
	Destructive    bool
	CreatedAt      time.Time
	TTLMs          *int64
	PollIntervalMs *int64
	Status         string
	StatusReason   string
	// InputRequests contains hash-only commitments captured from a strictly
	// decoded input_required handle. Raw request keys and values never enter the
	// durable task record.
	InputRequests []DurableTaskInputRef

	// Generation is the IMMUTABLE identity of this registration INSTANCE
	// (round-2 N-03). A textual task id is an upstream-controlled string that
	// can be re-issued after the gateway expires or releases its record; the
	// generation is derived from the record's anchored origin (plus a
	// never-reused ledger sequence) and is bound into every client, sweep and
	// compensation operation identity and effect digest. Every local mutation
	// and every forward is a COMPARE-AND-SWAP against it, so a stale owner can
	// never act on a replacement task and a stale cancellation intent can never
	// suppress the sweep of one.
	Generation string
	// DurableRef is the authority-backed binding for this task. Generation above
	// remains the opaque token used by the existing evidence and lease machinery;
	// when Tasks persistence is wired it is derived from DurableRef.Generation and
	// therefore survives a ResourceServer restart.
	DurableRef      DurableTaskRef
	DurableVerdict  DurableTaskVerdict
	DurableObserved time.Time

	// Origin is the ANCHORED evidence binding of the governed registration
	// (mcp.task.track) that created this record. Server-initiated compensations
	// chain from THIS binding — never from an unclaimed client request (F-04).
	// Zero for records injected outside the governed track path.
	Origin sdk.EvidenceBinding

	// Pending marks a registration whose governed mcp.task.track effect has been
	// claimed and fenced but whose settlement has NOT yet recorded (round-2
	// N-06). A pending record is NOT operable by client task methods: the
	// previous code inserted an immediately operable record and only checked
	// operability once, so a client that predicted the task id could pass the
	// check and then forward against a record the very next instruction
	// quarantined. Pending records stay fully visible to sweeps.
	Pending bool

	// Quarantined marks a record retained for RECONCILIATION and sweep
	// visibility only: a created upstream task whose governed registration or
	// whose compensating cancellation could not be durably confirmed (F-03). It
	// is NEVER a claim that a successful mcp.task.track anchored — it is the
	// honest statement "this external task exists and the gateway could not
	// prove it governed it". A quarantined record is never TTL-evicted: forgetting
	// it is exactly the permanent-orphan failure this field exists to prevent.
	Quarantined      bool
	QuarantineReason string

	// Reconciling marks an UNGOVERNED record whose compensating cancellation was
	// acknowledged by the upstream but NOT confirmed terminal (round-2 N-02).
	// Cooperative cancellation is explicitly non-terminal (tasks.go TaskCancel),
	// so the record may not be deleted on a bare acknowledgement; it is retained
	// for reconciliation and stays visible to every sweep. It is not a tracked
	// record for any client-facing lookup (it never was: a quarantined record is
	// deny-closed for client task methods), so `get` does not return it.
	Reconciling bool

	// TerminalUnconfirmed marks a TERMINAL local status the gateway INFERRED
	// from a cancellation acknowledgement rather than read from an authoritative
	// upstream task report (round-2 N-02). Such a status never removes the task
	// from reconciliation or from a future sweep: only an upstream-confirmed
	// terminal status does.
	TerminalUnconfirmed bool

	// Seq is the ledger's never-reused INSERTION SEQUENCE for this record: the
	// monotone position the operator inventory's snapshot high-watermark is taken
	// against (round-6 R6-03). It orders records by when this process learned about
	// them, which `CreatedAt` (upstream-influenced, coarse) cannot.
	Seq uint64

	// HandleRelayed is the IMMUTABLE HANDLE-RELAY PROVENANCE of this record: this
	// gateway must assume the owner MAY hold the unguessable task id and MAY be able
	// to address the task. It is set exactly once, by the one relay site (`rs.go`,
	// under the generation lease taken with the finalization), and NOTHING ever
	// clears it.
	//
	// ROUND-9 R9-04 + ROUND-11 N11-01, exactly what it does and does NOT assert. It is
	// installed on three facts, none of which is remote receipt:
	//
	//   - this process's `http.ResponseWriter` accepted every byte of the complete
	//     tools/call body (recordHandleRelay). No Go HTTP contract turns that into an
	//     acknowledgement from the remote client; or
	//   - the response write FAILED but the writer had already accepted body bytes, or
	//     reported no usable count (custodyAmbiguousHandleRelay). The accepted prefix
	//     can be the entire JSON value with only the encoder's newline missing, which
	//     JSON-RPC does not require, so possible delivery is ASSUMED; or
	//   - the relay unwound ABNORMALLY and delivery is ambiguous
	//     (custodyAmbiguousHandleRelay), so possible delivery is ASSUMED.
	//
	// Its NEGATION is correspondingly narrow: `!HandleRelayed` on a relayed record means
	// the writer PROVABLY accepted zero bytes (or the relay never ran), never merely
	// "the write returned an error".
	//
	// The flag therefore means "a delivery this gateway may not destroy unread", not
	// "the owner learned the id". Every consumer is written to be safe under the
	// weaker fact: it only ever ADDS an obligation (a collection to serve), never
	// removes one.
	//
	// ROUND-8 R8-01: round-7 derived "the owner has a delivery to lose" from the
	// CURRENT state — `!Pending && !Quarantined && !Reconciling`. Those flags are not
	// history, and they move in BOTH directions:
	//
	//   - a normal task whose handle the owner ALREADY holds can later be quarantined
	//     (a revoked-tool compensation that is not confirmed, a suppressed or failed
	//     kill-switch sweep). Client reads are then 403, and the current-state reading
	//     called the delivery obligation satisfied — so a privileged terminal
	//     confirmation plus `retire` deleted the owner's only authorization WITHOUT
	//     serving the result. The same record reaching `Reconciling` was `release`d
	//     directly, bypassing the retirement check altogether;
	//   - the opposite: a registration that settles and then FAILS to relay its handle
	//     is operable, so it appeared to OWE a collection its owner can never perform
	//     (it never learned the task id). With `ttlMs:null` that row answered 409
	//     forever and permanently consumed capacity.
	//
	// Provenance decides both, because it is a fact about the past that no later
	// transition can rewrite.
	HandleRelayed bool

	// TerminalReportDigest is the canonical digest of the CURRENT authoritative
	// terminal report of this task (taskReport.Digest), written by confirmStatus and
	// cleared by any confirmed NON-terminal or locally inferred status. It is the
	// identity of the answer the owner has to have seen.
	TerminalReportDigest string

	// OwnerCollectedDigest records the ONE fact operator retirement is allowed to
	// depend on: the OWNER of this record was successfully served a conforming
	// TERMINAL `GetTaskResult`, through this gateway — and WHICH one. It holds the
	// digest of that exact report, so the proof is bound to the answer rather than to
	// the mere event.
	//
	// The history matters, because four rounds circled this. Round-5 deleted the row
	// on retirement: the owner's next `tasks/get` got 403 "task not tracked" with no
	// upstream call at all, so an operator draining the inventory destroyed a final
	// tool result whose upstream TTL had not elapsed — through the gateway whose
	// entire job is to be the path to that upstream (R6-02). Round-6 kept the row as
	// a bounded FIFO "handoff cache" instead, and that cache produced its own family
	// of defects: the 513th retirement DELETED an unread oldest result purely for
	// being oldest, a leased row left the FIFO without being deleted and grew the
	// ledger without bound, and the cache was discharged by a response write whose
	// failure nobody checked (R7-02, R7-03, R7-05). Round-7 replaced the cache with
	// proof of delivery — but as a BARE BOOLEAN, which a later, DIFFERENT
	// authoritative terminal report did not clear (`confirmStatus` cleared only on a
	// non-terminal status), so `retire` could still delete a row whose owner had
	// never seen the report that was now authoritative (R8-02).
	//
	// Proof of delivery removes the cache entirely rather than bounding it. A row
	// whose owner has not collected is simply a NORMAL row: it counts against the
	// existing retention cap, it is listed, it is `actionable`, and
	// `retireReconciled` REFUSES it and says why. Nothing has to be evicted, because
	// nothing is being kept beyond the ordinary inventory.
	//
	// It is set ONLY by a successful owner delivery (recordOwnerTerminalCollection),
	// which compare-and-swaps on the generation AND on the digest that is
	// authoritative at that instant, and it stops being proof the moment
	// `TerminalReportDigest` moves.
	OwnerCollectedDigest string

	// CancelUnconfirmed marks that a cancellation of THIS generation was
	// DISPATCHED (client, sweep or compensation) without an authoritative
	// terminal confirmation — including an attempt that unwound abnormally
	// (round-4 R4-01/R4-06). It is MONOTONE and is installed ATOMICALLY with the
	// release of the generation pin (settleCancelAttempt), which is what makes the
	// record TTL-immune from the instant the external effect may have landed.
	//
	// Round-3 derived the same property from the STATUS alone, and the status
	// write happened AFTER the lease had already been released: a TTL eviction in
	// that window deleted a still-`working` record whose cancellation was on the
	// wire, and the later compare-and-swap status write then failed — the exact
	// fail-open R3-02 was meant to close. A status is also not expressive enough:
	// an `unknown`/unsettled cancellation writes no status at all, yet its effect
	// may equally have reached the upstream.
	CancelUnconfirmed bool
}

// taskOwner is the canonical owner tuple of one durable task: the principal the
// gateway will let operate it and the identity the evidence binds. Comparison is
// exact and total (a plain struct equality) — no field may be silently ignored.
//
// ClientID participates by deliberate choice (the deny-closed reading of the
// review's "plus OAuth client identity if client isolation is intended"): the
// OperationID derivation already namespaces the client, so letting a DIFFERENT
// OAuth client drive another client's task would reintroduce exactly the shared
// namespace the derivation removes. Legacy tokens without client_id/azp carry an
// empty ClientID on both sides and are unaffected.
type taskOwner struct {
	Tenant   string
	Issuer   string
	Subject  string
	ActAs    string
	ClientID string
}

// owner returns the record's canonical owner tuple.
func (r TaskRecord) owner() taskOwner {
	return taskOwner{
		Tenant: r.Tenant, Issuer: r.Issuer, Subject: r.Subject,
		ActAs: r.ActAs, ClientID: r.ClientID,
	}
}

// operable reports whether client task methods may act on this record. It is the
// single predicate every lookup AND every pre-forward revalidation uses (N-06).
//
// ROUND-7: there is no longer a post-retirement state to carve out here.
// Retirement DELETES the row, and it may only do so once its owner has already
// collected the terminal result, so "a retired record that its owner can still
// read" no longer exists as a state — which is what removed the whole handoff
// cache and the three findings built on it.
func (r TaskRecord) operable() bool {
	return !r.Pending && !r.Quarantined && !r.Reconciling
}

// updatable reports whether the record may still receive NEW INPUT
// (tasks/update). Round-4 R4-04: `operable()` ignores status, so a normal
// governed record whose cancellation was acknowledged (`cancel_requested`) —
// neither quarantined nor reconciling — still accepted tasks/update. The
// delivered cancellation bar suppresses every later AUTOMATIC cancel, so an
// upstream that never honored the cooperative request could be fed new input and
// keep working with no automatic cancellation path left. A cancellation-
// unconfirmed record is therefore a first-class RECONCILIATION state: an
// authoritative `tasks/get` is still permitted (it is the only thing that can
// resolve the ambiguity), the client's own `tasks/cancel` is still permitted (the
// per-generation intent decides whether it may dispatch), and `tasks/update` is
// denied.
func (r TaskRecord) updatable() bool {
	return r.operable() && !taskCancellationUnconfirmed(r)
}

// retirable reports the FIRST of the two retirement preconditions: the record's
// TERMINAL status was CONFIRMED by an authoritative upstream report. That is the
// proof the WORK STOPPED (round-4: automatic TTL deletion is not an acceptable
// substitute for a proven-terminal retirement).
//
// It is deliberately NOT the whole precondition — see `retireReady`. Keeping the
// two apart is what lets the inventory classify a proven-terminal row that its
// owner has not collected as its own visible, actionable class instead of hiding
// it among ordinary live work.
func (r TaskRecord) retirable() bool {
	return taskStatusTerminal(r.Status) && !r.TerminalUnconfirmed
}

// ownerCollectedCurrentTerminalReport reports that the owner was served EXACTLY
// the terminal report that is authoritative right now (round-8 R8-02). An empty
// digest on either side is never proof: a report with no identity cannot be the
// one the owner saw.
func (r TaskRecord) ownerCollectedCurrentTerminalReport() bool {
	return r.OwnerCollectedDigest != "" && r.OwnerCollectedDigest == r.TerminalReportDigest
}

// ownerCollectionSatisfied reports the SECOND retirement precondition: the owner
// has nothing left to collect through this gateway (round-7, corrected in round-8).
//
// Two ways to satisfy it, and the second is not a loophole:
//
//   - the owner was successfully served the CURRENTLY authoritative terminal report
//     (`ownerCollectedCurrentTerminalReport`); or
//   - no handle byte provably ever reached this task's owner (`!HandleRelayed`), so it
//     cannot know the unguessable task id and has no read to lose. Requiring proof of
//     a delivery that can never happen would make exactly the rows the reconciliation
//     surface exists for PERMANENTLY undrainable — the R4-05/R5-03 saturation defect,
//     reintroduced.
//
// ROUND-11 N11-01: the second arm is only as strong as the negation it rests on, so
// `HandleRelayed` is now false ONLY where the relay never ran or the writer PROVABLY
// accepted zero bytes. A failed write that accepted a positive — or unreportable —
// count records POSSIBLE relay instead (`custodyAmbiguousHandleRelay`), because the
// accepted prefix can be the whole JSON-RPC response minus the encoder's newline.
// Without that split this predicate returned true, and the deletion below ran, for an
// owner that was holding a perfectly usable identifier.
//
// ROUND-8 R8-01: the second condition is HISTORY (`HandleRelayed`), not current
// state. Round-7 asked `!operable()`, which is a snapshot of three mutable flags:
// a record whose handle the owner already held could be quarantined afterwards and
// then read as "never client-readable", so its result was deleted unread; and a
// record whose handle relay FAILED stayed operable and owed a collection nobody
// could ever perform. Both directions are closed by asking what actually happened.
//
// The residual this leaves is stated rather than hidden: a record whose handle WAS
// relayed and which is later quarantined can no longer be collected by its owner
// (client task methods are deny-closed on a quarantined record) and therefore
// cannot be retired either.
//
// ROUND-9 R9-03, the cost stated at its real size: such a row does NOT expire.
// `taskExpired` categorically exempts every quarantined/reconciling record from TTL
// eviction (F-03: forgetting a live external task is the failure), so "until its TTL
// elapses" — which this comment and the session used to say — was simply false. It
// remains until the process ends or a future AUTHORIZED abandon action removes it,
// and it counts against the owner and gateway admission caps the whole time. Enough
// such rows can therefore consume a subject's or the gateway's capacity permanently
// within one process lifetime.
//
// That cost is accepted deliberately: the client cannot read a quarantined row, so
// letting an operator delete it silently would destroy a possibly unread result.
// Inventing an operator "abandon this result" action is a new authorization, not a
// bug fix; it belongs to the durable-persistence work with the evidence-bound
// audit contract it needs.
func (r TaskRecord) ownerCollectionSatisfied() bool {
	return !r.HandleRelayed || r.ownerCollectedCurrentTerminalReport()
}

// retireReady reports that BOTH retirement preconditions hold: the terminal
// status is proven AND the owner has nothing left to collect. It is what the
// inventory reports as `retirable` to an operator, so the flag never promises an
// action that would be refused.
func (r TaskRecord) retireReady() bool {
	return r.retirable() && r.ownerCollectionSatisfied()
}

// taskEffectKind names the governed effect a generation lease is acquired for.
// The kinds are NOT interchangeable: each admits a different set of record
// states, and the widening is always explicit (round-4 R4-04 replaced the
// round-3 `forCancel bool`, which could only express "client method" vs
// "anything").
type taskEffectKind string

const (
	// taskEffectClientRead is an authoritative client `tasks/get`: permitted on
	// any operable record, INCLUDING a cancellation-unconfirmed one (reading is
	// how the ambiguity gets resolved).
	taskEffectClientRead taskEffectKind = "client-read"
	// taskEffectClientUpdate is a client `tasks/update`: additionally refused on a
	// cancellation-unconfirmed record (R4-04).
	taskEffectClientUpdate taskEffectKind = "client-update"
	// taskEffectClientCancel is a client `tasks/cancel`: permitted on any operable
	// record; the per-generation intent decides whether it may dispatch.
	taskEffectClientCancel taskEffectKind = "client-cancel"
	// taskEffectServerCancel is a sweep/compensation cancellation: permitted on
	// ANY live record — a quarantined or reconciling record names a live external
	// task and is a legitimate server-initiated cancellation target.
	taskEffectServerCancel taskEffectKind = "server-cancel"
	// taskEffectReconcile is the operator reconciliation surface's authoritative
	// `tasks/get`: permitted on ANY live record — reconciliation exists precisely
	// for records no client method may touch.
	taskEffectReconcile taskEffectKind = "reconcile"
)

// widened reports whether the kind is a SERVER-initiated effect that may target
// a record no client task method could operate.
func (k taskEffectKind) widened() bool {
	return k == taskEffectServerCancel || k == taskEffectReconcile
}

// admits reports whether the record state permits this effect kind.
func (k taskEffectKind) admits(rec TaskRecord) error {
	switch {
	case k.widened():
		return nil
	case !rec.operable():
		return errTaskNotOperable
	case k == taskEffectClientUpdate && !rec.updatable():
		return errTaskCancellationUnconfirmed
	default:
		return nil
	}
}

// taskOwnerFromToken builds the canonical owner tuple of a validated caller.
func taskOwnerFromToken(tenant string, tok validatedToken) taskOwner {
	return taskOwner{
		Tenant: tenant, Issuer: tok.Issuer, Subject: tok.Subject,
		ActAs: tok.ActAs, ClientID: tok.ClientID,
	}
}

// equals is the ownership predicate of every task method and every
// server-initiated task effect.
func (o taskOwner) equals(other taskOwner) bool { return o == other }

// digest is the stable length-prefixed identity of the owner tuple, bound into
// the server-initiated child EffectDigests (F-11).
func (o taskOwner) digest() string {
	return evidenceLPDigest(mcpTaskOwnerDomainV1, o.Tenant, o.Issuer, o.Subject, o.ActAs, o.ClientID)
}

// taskCancelBar classifies why an automatic server-initiated cancellation
// attempt is not permitted right now. The classes are NOT interchangeable: a
// delivered cancellation is the expected steady state after a successful sweep
// (nothing to report), while an ambiguous or stale one demands reconciliation.
type taskCancelBar string

const (
	taskCancelBarNone      taskCancelBar = ""
	taskCancelBarInFlight  taskCancelBar = "in-flight"
	taskCancelBarAmbiguous taskCancelBar = "ambiguous"
	taskCancelBarDelivered taskCancelBar = "delivered"
	taskCancelBarStale     taskCancelBar = "stale-generation"
)

// taskCancelIntent is the ONE atomic per-GENERATION server-initiated
// cancellation intent (review round-1 F-01, generation-scoped in round-2 N-03).
// A random operation id per loop pass did not enforce the single-use-effect
// invariant, it EVADED it: two concurrent sweeps, or a sweep after an ambiguous
// outcome, could emit the same logical cancellation twice. Keying the intent by
// the textual task id was in turn insufficient: an expired identifier reused by
// a REPLACEMENT task inherited the old task's bar, silently suppressing the
// kill-switch cancellation of a live task. The intent is therefore keyed by the
// record generation and makes the rule explicit:
//
//   - at most ONE attempt may be in flight for a generation at a time;
//   - after an `unknown` or unsettled outcome — or after a cancellation that was
//     actually delivered — every AUTOMATIC re-attempt is barred (the frozen law:
//     never re-forward an ambiguous or already-emitted effect);
//   - a new attempt identity is minted only when the previous attempt provably
//     emitted nothing (claim/fence refusal) or durably settled not_sent/blocked.
type taskCancelIntent struct {
	attempt   uint64
	inFlight  bool
	bar       taskCancelBar
	barReason string
}

// taskCancelReservation is the outcome of reserving one cancellation attempt.
type taskCancelReservation struct {
	attempt uint64
	ok      bool
	bar     taskCancelBar
	reason  string
}

// taskLedger is the connector-side governance ledger of durable MCP tasks.
//
// This structure is always an in-memory cache owned by ONE gateway process. When
// DurableTaskStore is wired, registered task identity, owner, generation and the
// latest protocol observation are written through to that authority and restored
// during ResourceServer construction. The maps below still contain process-only
// custody state — in-flight leases, cancellation bars, tombstones, admission
// reservations and an orphan whose durable registration itself failed — which
// cannot be inferred from a binding after restart.
//
// Consequently the durable inventory survives restart, but an operator must
// still treat the reconciliation cursor and process-only artifacts as belonging
// to the instance that issued them. When DurableTaskStore is nil the Tasks
// capability and task methods are disabled, so this cache is never presented as
// an authority for standalone Tasks.
type taskLedger struct {
	mu   sync.Mutex
	byID map[string]TaskRecord
	// cancels holds the per-GENERATION cancellation intent independently of the
	// record (an intent must survive the record's removal so a late attempt
	// cannot re-emit), keyed by TaskRecord.Generation — never by task id.
	cancels map[string]*taskCancelIntent
	// retired is the generation TOMBSTONE set: generations that were released
	// after a proven cancellation or evicted by TTL. A retired generation can
	// never be acted upon again, and — crucially — its bar can never be
	// inherited by a replacement task that reuses the same textual id (N-03).
	retired      map[string]string
	retiredOrder []string
	// collisions parks ambiguous duplicate handles: an upstream task the gateway
	// refuses to govern AND refuses to cancel, because the id it would cancel
	// belongs to a different, already governed task (F-05).
	collisions []TaskRecord
	// leases pins record GENERATIONS that have a governed effect IN FLIGHT
	// (round-3 R3-03), keyed by generation and refcounted. See acquireEffectLease.
	leases map[string]int
	// admitted counts the RETENTION SLOTS reserved by calls that were admitted
	// but whose task (if any) does not exist yet — one per in-flight
	// task-producing forward, per owner, plus the gateway-wide total (round-5
	// R5-02). Round-4 read the retained counts under the mutex and released it
	// immediately, so the "hard" bound was a SNAPSHOT: every concurrent caller
	// observed the same pre-forward count, all of them passed, and the reviewer's
	// barrier probe reached 12 retained records under an advertised per-owner cap
	// of 2. A bound that only holds when requests are serialized is not a bound.
	admitted      map[taskOwner]int
	admittedTotal int
	// seq is the never-reused record sequence that makes every generation unique
	// within this process even when two records share an origin (records
	// injected outside the governed track path carry a zero origin). It is also
	// the monotone INSERTION POSITION the inventory's snapshot watermark is taken
	// against (round-6 R6-03).
	seq                uint64
	maxActiveBySubject int
	globalActiveCap    int
	clock              func() time.Time
}

func newTaskLedger(maxActiveBySubject int, clock func() time.Time) *taskLedger {
	if maxActiveBySubject <= 0 {
		maxActiveBySubject = defaultMaxActiveTasksPerSubject
	}
	if clock == nil {
		clock = time.Now
	}
	return &taskLedger{
		byID:               map[string]TaskRecord{},
		cancels:            map[string]*taskCancelIntent{},
		retired:            map[string]string{},
		leases:             map[string]int{},
		admitted:           map[taskOwner]int{},
		maxActiveBySubject: maxActiveBySubject,
		globalActiveCap:    maxActiveBySubject * maxTrackedTaskSubjects,
		clock:              clock,
	}
}

// validateTaskID refuses an empty or whitespace-ambiguous task id. The ledger
// deliberately does NOT normalize: the record the evidence bound must be the
// record the ledger stores and the actuator targets (F-10).
func validateTaskID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errTaskMissingID
	}
	if id != strings.TrimSpace(id) {
		return errTaskAmbiguousID
	}
	return nil
}

// normalizeRecordLocked fills the derived fields of a record about to be stored
// and assigns its IMMUTABLE generation from the supplied origin seed.
func (l *taskLedger) normalizeRecordLocked(rec TaskRecord, seed sdk.EvidenceBinding, now time.Time) TaskRecord {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.Status == "" {
		rec.Status = taskStatusWorking
	}
	if rec.Origin.Valid() {
		seed = rec.Origin
	}
	l.seq++
	rec.Seq = l.seq
	rec.Generation = deriveTaskGeneration(rec, seed, l.seq)
	return rec
}

// insert registers a GOVERNED task record (the mcp.task.track effect) and
// returns the stored record, whose immutable Generation the caller MUST use for
// every later mutation, revalidation and binding. It fails with
// errTaskDuplicateID on a colliding id — distinct from the capacity errors,
// because the two demand opposite responses (F-05).
func (l *taskLedger) insert(rec TaskRecord) (TaskRecord, error) {
	if l == nil {
		return rec, nil
	}
	if err := validateTaskID(rec.TaskID); err != nil {
		return TaskRecord{}, err
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	if _, exists := l.byID[rec.TaskID]; exists {
		return TaskRecord{}, errTaskDuplicateID
	}
	stored := l.normalizeRecordLocked(rec, sdk.EvidenceBinding{}, now)
	// ROUND-4 R4-07: the per-principal cap counts the CANONICAL OWNER TUPLE, not
	// the bare `sub`. Two configured trusted issuers can mint the same subject —
	// which this task model otherwise treats as distinct owners everywhere else —
	// so a bare-subject counter let issuer A fill the cap and deny task admission
	// to issuer B (and to a different delegation/OAuth-client owner sharing the
	// subject). The cap must isolate exactly what ownership isolates.
	if l.activeCountLocked(stored.owner(), now) >= l.maxActiveBySubject {
		return TaskRecord{}, errTaskSubjectCap
	}
	if l.activeTotalLocked(now) >= l.globalActiveCap {
		return TaskRecord{}, errTaskGlobalCap
	}
	l.byID[stored.TaskID] = stored
	return stored, nil
}

// insertDurable stores a record whose immutable generation was allocated by
// DurableTaskStore. It preserves all ordinary admission/collision checks but
// never replaces the durable generation with a process-local sequence.
func (l *taskLedger) insertDurable(rec TaskRecord) (TaskRecord, error) {
	if l == nil {
		return TaskRecord{}, errors.New("task ledger is unavailable")
	}
	if err := validateTaskID(rec.TaskID); err != nil {
		return TaskRecord{}, err
	}
	if rec.DurableRef.Generation <= 0 || rec.Generation != durableGenerationToken(rec.DurableRef.Generation) {
		return TaskRecord{}, errors.New("task durable generation is invalid")
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	if _, exists := l.byID[rec.TaskID]; exists {
		return TaskRecord{}, errTaskDuplicateID
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.Status == "" {
		rec.Status = taskStatusWorking
	}
	l.seq++
	rec.Seq = l.seq
	if l.activeCountLocked(rec.owner(), now) >= l.maxActiveBySubject {
		return TaskRecord{}, errTaskSubjectCap
	}
	if l.activeTotalLocked(now) >= l.globalActiveCap {
		return TaskRecord{}, errTaskGlobalCap
	}
	l.byID[rec.TaskID] = rec
	return rec, nil
}

// restoreDurable inserts one startup inventory row without dropping it merely
// because the operator lowered the admission cap. Existing durable work must
// remain visible and drainable; the ordinary admission path will refuse new
// tasks while the restored inventory is above its bound.
func (l *taskLedger) restoreDurable(rec TaskRecord) error {
	if l == nil {
		return errors.New("task ledger is unavailable")
	}
	if err := validateTaskID(rec.TaskID); err != nil {
		return err
	}
	if rec.DurableRef.Generation <= 0 || rec.Generation != durableGenerationToken(rec.DurableRef.Generation) {
		return errors.New("task durable generation is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.byID[rec.TaskID]; exists {
		return errTaskDuplicateID
	}
	l.seq++
	rec.Seq = l.seq
	l.byID[rec.TaskID] = rec
	return nil
}

// refreshDurable makes the durable projection authoritative while retaining
// process-only custody bookkeeping that the store does not own. It refuses to
// replace a different live generation under the same task ID.
func (l *taskLedger) refreshDurable(rec TaskRecord) error {
	if l == nil {
		return errors.New("task ledger is unavailable")
	}
	if rec.DurableRef.Generation <= 0 || rec.Generation != durableGenerationToken(rec.DurableRef.Generation) {
		return errors.New("task durable generation is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.byID[rec.TaskID]; ok {
		if current.Generation != rec.Generation {
			return errTaskGenerationStale
		}
		rec.Seq = current.Seq
		rec.Pending = current.Pending
		rec.Quarantined = current.Quarantined
		rec.QuarantineReason = current.QuarantineReason
		rec.Reconciling = current.Reconciling
		rec.HandleRelayed = current.HandleRelayed || rec.HandleRelayed
		rec.TerminalReportDigest = current.TerminalReportDigest
		rec.OwnerCollectedDigest = current.OwnerCollectedDigest
		rec.CancelUnconfirmed = current.CancelUnconfirmed || rec.CancelUnconfirmed
		rec.TerminalUnconfirmed = current.TerminalUnconfirmed || rec.TerminalUnconfirmed
		l.byID[rec.TaskID] = rec
		return nil
	}
	l.seq++
	rec.Seq = l.seq
	l.byID[rec.TaskID] = rec
	return nil
}

// taskQuarantineKind is the typed outcome of a quarantine attempt (round-2
// N-01). The caller MUST branch on it before compensating: compensating a
// COLLISION would send tasks/cancel for an identifier that names a different,
// already governed task.
type taskQuarantineKind string

const (
	// taskQuarantineRetained: the record is now held under its own generation.
	taskQuarantineRetained taskQuarantineKind = "retained"
	// taskQuarantineCollision: the identifier already names a DIFFERENT record.
	// Never compensate, never mutate the existing record.
	taskQuarantineCollision taskQuarantineKind = "collision"
	// taskQuarantineInvalid: the identifier itself is unusable.
	taskQuarantineInvalid taskQuarantineKind = "invalid"
)

// taskQuarantineResult is the typed quarantine outcome.
type taskQuarantineResult struct {
	kind   taskQuarantineKind
	record TaskRecord // the retained record (kind == retained)
	err    error      // validation / parking failure — always reported, never silent
}

// retained reports whether a compensating cancellation may target this record.
func (r taskQuarantineResult) retained() bool { return r.kind == taskQuarantineRetained }

// quarantine retains a CREATED upstream task whose governed registration or
// whose compensating cancellation could not be durably confirmed (F-03). It
// deliberately BYPASSES the capacity guards: refusing to remember a live
// external task is the very failure being prevented. Quarantined records DO
// count toward the caps, so an accumulation of unreconciled orphans throttles
// new registrations instead of growing without bound — the deny-closed
// direction.
//
// Round-2 N-01: the collision check is ATOMIC with the retention and is typed.
// ANY pre-existing record under this identifier is a collision — including one
// with the same owner, because a second upstream handle naming an already
// governed task is an upstream protocol fault, not the same task. The colliding
// handle is parked for reconciliation, the existing record is left untouched,
// and the caller is told NOT to compensate.
func (l *taskLedger) quarantine(rec TaskRecord, seed sdk.EvidenceBinding, reason string) taskQuarantineResult {
	if l == nil {
		return taskQuarantineResult{kind: taskQuarantineRetained, record: rec}
	}
	if err := validateTaskID(rec.TaskID); err != nil {
		return taskQuarantineResult{kind: taskQuarantineInvalid, err: err}
	}
	now := l.now()
	rec.Quarantined = true
	rec.QuarantineReason = reason
	// A quarantine is never "pending a registration settlement": the governed
	// registration provably did not happen, or could not be confirmed.
	rec.Pending = false

	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	if _, ok := l.byID[rec.TaskID]; ok {
		stored := l.normalizeRecordLocked(rec, seed, now)
		return taskQuarantineResult{kind: taskQuarantineCollision, err: l.parkCollisionLocked(stored)}
	}
	stored := l.normalizeRecordLocked(rec, seed, now)
	l.byID[stored.TaskID] = stored
	return taskQuarantineResult{kind: taskQuarantineRetained, record: stored}
}

// markQuarantine flags an EXISTING record for reconciliation without changing
// its ownership or status (used when a governed registration anchored but its
// settlement did not record, or when a compensation left the task ambiguous).
// Compare-and-swap on the generation: a stale caller can never quarantine a
// replacement task (N-03).
// Round-3 R3-04: quarantining does NOT clear Pending. `Pending` is monotonic —
// only the track-settlement owner may finalize it (settleRegistrationAndPin) — and a
// quarantined record is non-operable regardless, so clearing it here bought
// nothing and broke the invariant.
func (l *taskLedger) markQuarantine(taskID, generation, reason string) bool {
	return l.mutate(taskID, generation, func(rec *TaskRecord) {
		rec.Quarantined = true
		rec.QuarantineReason = reason
	})
}

// settleRegistrationAndPin is the ONLY finalizer of a pending registration and the
// ONLY writer that clears `Pending` on a live record: the governed mcp.task.track
// effect settled durably (N-06), so the record becomes operable for client task
// methods — AND, in the SAME transition, the generation is PINNED for the handle
// relay that follows.
//
// Round-3 R3-04: it is an ATOMIC state transition that fails if the generation
// OR the record state changed while the settlement was in flight. Round-2 it was
// a bare compare-and-swap on the generation whose result the caller ignored, and
// `markCancelRequested` cleared `Pending` unconditionally — so a sweep that
// canceled the still-pending registration made it operable before its owner had
// finished, and the owner then relayed the handle regardless of what the record
// had become. The transition therefore refuses when the record is no longer
// pending, has been quarantined, has become a reconciliation artifact, or
// carries an unconfirmed cancellation: none of those may be presented to the
// client as a successfully governed task handle.
//
// ROUND-9 R9-01 — why the PIN is part of the same transition. Round-8 finalized
// under this mutex, RELEASED it, and only then called `acquireEffectLease`. Two
// things can happen in that window, and both did:
//
//   - the acquisition's own `evictExpiredLocked` crosses the row's TTL and EVICTS
//     the record it was supposed to pin. The caller treated the acquisition error as
//     "no defer to install" and wrote the handle anyway, so the client ended up
//     holding a handle for which this gateway has no governance row at all;
//   - a sweep quarantines (or otherwise changes) the row between the two calls. The
//     handle was still written, and the later provenance compare-and-swap either
//     installed delivery history without the promised pin or lost it entirely.
//
// A relay precondition that the relay does not have to hold is not a precondition.
// Finalization now either yields BOTH the operable record and its pin, or yields
// nothing and the caller must not write. The record state is committed together
// with the pin: the effect kind is checked against the POST-settlement copy, so a
// refusal cannot leave a half-finalized row behind.
//
// It returns the record as it now stands so the caller can verify it is EXACTLY
// the generation it inserted before relaying anything. Every successful call MUST
// be matched by a `releaseEffectLease` on the returned generation.
func (l *taskLedger) settleRegistrationAndPin(taskID, generation string, kind taskEffectKind) (TaskRecord, bool) {
	if l == nil {
		return TaskRecord{}, false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// The TTL boundary is INSIDE the transition (R9-01). Evicting here and pinning
	// below under one mutex is what makes "this row survived its own finalization"
	// a fact rather than a hope: after the pin, evictExpiredLocked and every
	// compare-delete refuse this generation.
	l.evictExpiredLocked(now)
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return TaskRecord{}, false
	}
	if _, retired := l.retired[generation]; retired {
		return rec, false
	}
	if !rec.Pending || rec.Quarantined || rec.Reconciling || taskCancellationUnconfirmed(rec) {
		return rec, false
	}
	// ROUND-4 R4-03: the record fields are not the whole state. A cancellation
	// intent and a generation pin live in SEPARATE maps, so a sweep that had
	// already reserved this generation and was blocked inside the upstream forward
	// left the record looking like a clean pending registration — and finalization
	// then relayed the handle while a kill-switch cancellation was on the wire.
	// Until that cancellation returns the client can drive the task; if it ends
	// ambiguously or fails, the client is holding a handle the finalizer meant to
	// withhold. Any reserved, in-flight, barred or pinned cancellation of this
	// generation therefore refuses the finalization outright (deny-closed: the
	// record stays pending and the caller quarantines it).
	if in := l.cancels[generation]; in != nil && (in.inFlight || in.bar != taskCancelBarNone) {
		return rec, false
	}
	if l.leasedLocked(generation) {
		return rec, false
	}
	settled := rec
	settled.Pending = false
	// The pin is taken for a NAMED effect kind, and the kind is asked about the
	// record as it will stand — never about the pending one it replaces.
	if err := kind.admits(settled); err != nil {
		return rec, false
	}
	l.byID[taskID] = settled
	if l.leases == nil {
		l.leases = map[string]int{}
	}
	l.leases[generation]++
	return settled, true
}

// mutate is the single compare-and-swap primitive: it applies fn to the record
// that currently holds taskID if and only if its generation matches.
func (l *taskLedger) mutate(taskID, generation string, fn func(*TaskRecord)) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false
	}
	fn(&rec)
	l.byID[taskID] = rec
	return true
}

// parkCollision parks an ambiguous duplicate handle for reconciliation.
func (l *taskLedger) parkCollision(rec TaskRecord) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec.Generation == "" {
		rec = l.normalizeRecordLocked(rec, sdk.EvidenceBinding{}, l.now())
	}
	return l.parkCollisionLocked(rec)
}

func (l *taskLedger) parkCollisionLocked(rec TaskRecord) error {
	if len(l.collisions) >= maxTrackedCollisions {
		return errTaskCollisionOverflow
	}
	rec.Quarantined = true
	rec.Pending = false
	l.collisions = append(l.collisions, rec)
	return nil
}

// collisionRecords returns the parked ambiguous duplicates (reconciliation).
func (l *taskLedger) collisionRecords() []TaskRecord {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]TaskRecord(nil), l.collisions...)
}

// compareDeleteTerminal is THE ONE compare-and-delete of a task record, and every
// terminal deletion goes through it: the automatic release of a confirmed-terminal
// reconciliation artifact (`release`) and the explicit operator retirement
// (`retireReconciled`) alike.
//
// ROUND-8 R8-01: those were two rules. `retireReconciled` demanded proof that the
// owner had nothing left to collect; `release` demanded nothing but the generation
// and the lease — and a normal, handle-delivered record CAN reach `Reconciling` (a
// sweep whose cancellation is acknowledged on an already-quarantined record), so a
// conforming terminal report deleted its owner's only authorization through the
// second door while the first one was locked. A deletion rule that a second call
// site can bypass is not a rule.
//
// It refuses, in this order and with a reason the operator surface relays verbatim:
//
//   - the generation is already tombstoned, or no longer holds this identifier
//     (round-2 N-01/N-03: deleting by bare id could erase a REPLACEMENT task's
//     governance record);
//   - the terminal status is not CONFIRMED by an authoritative upstream report
//     (`retirable`) — an inferred one never suffices;
//   - the owner still has an uncollected terminal result (`ownerCollectionSatisfied`);
//   - a cancellation of the generation is in flight, or a governed effect pins it
//     (round-3 R3-03: freeing the textual identifier while a call carrying it is on
//     the wire lets a replacement take it).
func (l *taskLedger) compareDeleteTerminalLocked(taskID, generation, tombstone string) (bool, string) {
	if generation == "" {
		return false, "no task generation named"
	}
	if _, retired := l.retired[generation]; retired {
		return false, "the named generation is already retired"
	}
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false, "the named generation no longer holds this task identifier"
	}
	if !rec.retirable() {
		return false, "the task has no CONFIRMED terminal status; read it with the reconciliation status action first"
	}
	if !rec.ownerCollectionSatisfied() {
		return false, "the OWNER has not yet collected this terminal result; the record stays retained and " +
			"capacity-counting until its owner's tasks/get delivers it (retiring now would destroy a result this " +
			"gateway is the only route to)"
	}
	if in := l.cancels[generation]; in != nil && in.inFlight {
		return false, "a cancellation of this generation is still in flight"
	}
	if l.leasedLocked(generation) {
		return false, "a governed effect against this generation is still in flight"
	}
	delete(l.byID, taskID)
	l.retireLocked(rec.Generation, tombstone)
	return true, ""
}

// release drops a record after the upstream task reached a CONFIRMED terminal
// state. ROUND-8: it is the same compare-and-delete the operator retirement uses,
// so an artifact whose handle its owner holds is NOT deleted before that owner has
// collected the result (R8-01).
func (l *taskLedger) release(taskID, generation string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ok, _ := l.compareDeleteTerminalLocked(taskID, generation,
		"record released after a confirmed terminal state its owner had nothing left to collect from")
	return ok
}

// retireLocked tombstones one generation and drops its cancellation intent.
func (l *taskLedger) retireLocked(generation, reason string) {
	if generation == "" {
		return
	}
	if l.retired == nil {
		l.retired = map[string]string{}
	}
	if _, exists := l.retired[generation]; !exists {
		l.retired[generation] = reason
		l.retiredOrder = append(l.retiredOrder, generation)
		for len(l.retiredOrder) > maxRetiredGenerations {
			delete(l.retired, l.retiredOrder[0])
			l.retiredOrder = l.retiredOrder[1:]
		}
	}
	delete(l.cancels, generation)
}

// get returns the TRACKED governance record for taskID. A record retained only
// for reconciliation (its compensating cancellation was acknowledged but not
// confirmed terminal) is NOT a tracked task for any client-facing lookup and is
// deliberately not returned; it remains fully visible to `active` and to
// `reconciliationRecords` (round-2 N-02).
func (l *taskLedger) get(taskID string) (TaskRecord, bool) {
	rec, ok := l.lookup(taskID)
	if !ok || rec.Reconciling {
		return TaskRecord{}, false
	}
	return rec, true
}

// lookup is the RAW record view, reconciliation artifacts included.
func (l *taskLedger) lookup(taskID string) (TaskRecord, bool) {
	if l == nil {
		return TaskRecord{}, false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	rec, ok := l.byID[taskID]
	return rec, ok
}

// reconciliationRecords returns EVERY record the ledger holds — the complete
// inventory the retention bound is computed from.
//
// Round-4 R4-04 widened it from "quarantined or reconciling" to "plus every
// cancellation-unconfirmed record". Round-5 R5-03 showed that was still a SUBSET
// of what consumes the bound: a normal governed record whose terminal status was
// authoritatively CONFIRMED is not released (only a `Reconciling` artifact is),
// and with `ttlMs: null` it never expires either — so two such records saturated
// a cap of 2 while the operator inventory reported an EMPTY backlog. A legitimate
// high-volume owner was then denied every further tools/call with no visible row
// to drain.
//
// The rule is therefore the only one that cannot drift from the bound: if a
// record counts against `retainedCountLocked`, it is LISTED. The per-row class
// (taskReconcileRowClass) tells the operator which rows demand action and which
// are simply live work; the ACTIONS remain deny-closed regardless — retirement
// still requires a confirmed terminal status.
//
// The rule is ONE-WAY, and round-6 states it that way rather than claiming a
// biconditional the code does not have. Listed-but-not-counted rows exist: a
// parked COLLISION is inventoried without counting against this cap. (Round-6's
// other example, the retired terminal-result handoff, is gone: round-7 replaced
// the handoff cache with proof-of-delivery retirement, so a retired record is
// DELETED and every surviving row counts.) Counted-but-unlisted state also exists
// for as long as an admission TICKET is held, because that reservation has no row
// yet (round-6 R6-04). What must never happen — and does not — is a row that
// consumes the bound while being invisible to the operator who has to drain it.
//
// DURABILITY: registered rows are projections of DurableTaskStore and are
// rehydrated at startup. The snapshot sequence, cancellation bars and any orphan
// whose durable registration failed remain process-local; see `taskLedger`.
func (l *taskLedger) reconciliationRecords() []TaskRecord {
	if l == nil {
		return nil
	}
	records, _, _ := l.reconciliationSnapshot()
	return records
}

// reconciliationSnapshot returns the complete inventory AND the ledger's current
// insertion HIGH-WATERMARK, read under ONE mutex acquisition (round-6 R6-03).
//
// The watermark is what makes a paginated traversal complete under mutation. Every
// stored record carries the never-reused `Seq` it was inserted with, so a cursor
// chain pinned to one watermark defines a fixed SET of rows: a row inserted after
// the traversal began has a larger `Seq` and is excluded from that traversal by
// construction, instead of being silently skipped because its key happened to sort
// before the previous page's last key. Reading the records and the watermark in
// separate calls would reintroduce exactly that race, so they are one read.
func (l *taskLedger) reconciliationSnapshot() (records, collisions []TaskRecord, watermark uint64) {
	if l == nil {
		return nil, nil, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	records = make([]TaskRecord, 0, len(l.byID))
	for _, rec := range l.byID {
		records = append(records, rec)
	}
	collisions = append([]TaskRecord(nil), l.collisions...)
	return records, collisions, l.seq
}

// acquireEffectLease is the pre-effect compare-and-swap of every governed task
// effect AND the in-flight PIN that makes it hold across the external call
// (round-2 N-03/N-06, made a real lease in round-3 R3-03).
//
// Revalidating the generation and then releasing the ledger mutex before the
// transport write was still a check-then-use race: between the check and the
// write another goroutine could cross the record's TTL, evict it and register a
// REPLACEMENT under the same textual identifier — and the forward carries the
// upstream's textual task id, not the gateway's generation, so the
// already-admitted update/cancel landed on the replacement. Later
// compare-and-swap bookkeeping can refuse to mutate the replacement; it cannot
// undo the wrong-target upstream effect.
//
// The lease closes the window atomically: while at least one lease is held for a
// generation the ledger refuses to TTL-evict it (evictExpiredLocked) and refuses
// to release it (release). Because `insert` refuses any identifier that is still
// present, no replacement can take the identifier either. The lease is a
// REFCOUNT, not a mutex: two concurrent governed reads of the same record are
// legitimate; what must never happen is the record disappearing — or being
// replaced — underneath an effect that was authorized against it.
//
// The effect KIND decides which record states are acceptable (round-4 R4-04 —
// see taskEffectKind.admits): a server-initiated cancellation or a
// reconciliation read may target a quarantined/reconciling record that names a
// live external task; a client update may not target a cancellation-unconfirmed
// one.
//
// Every successful acquisition MUST be matched by a release once the dispatch has
// been classified — never before. A CANCELLATION releases its pin through
// settleCancelAttempt, which installs the cancellation-unconfirmed state under
// the SAME mutex (round-4 R4-01); everything else uses releaseEffectLease.
func (l *taskLedger) acquireEffectLease(taskID, generation string, kind taskEffectKind) error {
	if l == nil {
		if kind.widened() {
			return nil
		}
		return errTaskGenerationStale
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	if _, retired := l.retired[generation]; retired {
		return errTaskGenerationStale
	}
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return errTaskGenerationStale
	}
	if err := kind.admits(rec); err != nil {
		return err
	}
	if l.leases == nil {
		l.leases = map[string]int{}
	}
	l.leases[generation]++
	return nil
}

// releaseEffectLease drops one in-flight pin. It is called only AFTER the
// dispatch has been classified (settled or definitively refused), and only for
// NON-cancellation effects — a cancellation drops its pin inside
// settleCancelAttempt so the pin release and the cancellation-unconfirmed state
// install share one mutex (round-4 R4-01).
func (l *taskLedger) releaseEffectLease(generation string) {
	if l == nil || generation == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseEffectLeaseLocked(generation)
}

func (l *taskLedger) releaseEffectLeaseLocked(generation string) {
	if generation == "" {
		return
	}
	if n := l.leases[generation]; n > 1 {
		l.leases[generation] = n - 1
	} else {
		delete(l.leases, generation)
	}
}

// leasedLocked reports whether a governed effect currently pins this generation.
func (l *taskLedger) leasedLocked(generation string) bool {
	return generation != "" && l.leases[generation] > 0
}

// confirmStatus applies an AUTHORITATIVE upstream-reported status: it is the ONLY
// path that may make a terminal status terminal for reconciliation and sweeps
// (round-2 N-02). Every LOCALLY INFERRED status is written by
// settleCancelAttempt, atomically with the cancellation transition that inferred
// it (round-4 R4-01) — there is deliberately no free-standing "write an inferred
// status" primitive any more, because every caller of the old one performed a
// cancellation bookkeeping step that must not be separable from its bar, its
// reservation and its generation pin.
// ROUND-8: it takes the whole validated report, not a loose (status, reason) pair.
// Two of the report's facts are load-bearing and were being dropped here:
//
//   - its canonical DIGEST, which is what the owner's collection proof is bound to
//     (R8-02). A later authoritative terminal report that is not byte-for-byte the
//     same answer invalidates the proof, because the owner has not seen THIS one;
//   - its `ttlMs`, which SEP-2663 measures from creation and explicitly allows to
//     CHANGE over the task's lifetime (R8-04). Validating it and then discarding it
//     evicted an owner at a stale initial deadline while the upstream still held the
//     result.
func (l *taskLedger) confirmStatus(taskID, generation string, rep taskReport) (TaskRecord, bool) {
	if l == nil {
		return TaskRecord{}, false
	}
	var out TaskRecord
	ok := l.mutate(taskID, generation, func(rec *TaskRecord) {
		rec.Status = strings.TrimSpace(rep.Status)
		rec.StatusReason = strings.TrimSpace(rep.Reason)
		rec.TerminalUnconfirmed = false
		rec.applyReportedTTL(rep.TTLMs)
		if taskStatusTerminal(rec.Status) {
			// ROUND-4 R4-01: an AUTHORITATIVE upstream report of a TERMINAL status is
			// exactly the proof `CancelUnconfirmed` waits for — the work provably
			// stopped — so it is the ONE thing that clears it and lets the record
			// resume its ordinary lifecycle (TTL eviction, retirement). A confirmed
			// NON-terminal status is the opposite evidence (the upstream is still
			// working despite the cancellation) and deliberately clears nothing.
			rec.CancelUnconfirmed = false
			// ROUND-8 R8-02: the CURRENT authoritative answer. Installing it is what
			// invalidates a collection proof bound to a different one — no explicit
			// "clear the proof" step exists or is needed, because the proof is an
			// EQUALITY against this field rather than a flag beside it.
			rec.TerminalReportDigest = rep.Digest
		} else {
			// ROUND-7: a confirmed NON-terminal status INVALIDATES any earlier proof of
			// delivery. Terminal statuses are terminal in SEP-2663, so this is a broken
			// or hostile upstream — but the deny-closed reading is the only safe one:
			// whatever the owner already collected is not this record's final result,
			// so retirement must earn its proof again.
			rec.TerminalReportDigest = ""
			rec.OwnerCollectedDigest = ""
		}
		out = *rec
	})
	return out, ok
}

// applyReportedTTL updates the record's EFFECTIVE RETENTION from an authoritative
// report (round-8 R8-04).
//
// `Task.ttlMs` is measured from creation and MAY change over the task's lifetime
// (SEP-2663, repeated for `tasks/get`), so a report is the current statement of how
// long the upstream keeps this task — and the gateway is the owner's only route to
// it. Round-7 validated the value and then applied nothing: an initial `ttlMs:1000`
// followed by a conforming `ttlMs:60000` (or `null`) still evicted the record at
// local age 1000ms and answered the owner 403 without asking an upstream that still
// held the result.
//
// The update is deliberately MONOTONE — the effective expiry never moves EARLIER:
//
//   - nil (explicit null, or a value outside this process's duration range) means
//     UNBOUNDED and always applies;
//   - a larger value extends;
//   - a smaller value is IGNORED. A shortened TTL would let a broken or hostile
//     upstream delete the owner's only authorization ahead of time, which is the
//     R6-02 harm with an upstream holding the trigger. Retaining longer than the
//     upstream requires costs one visible, counted, operator-drainable row.
func (r *TaskRecord) applyReportedTTL(ttl *int64) {
	if ttl == nil {
		r.TTLMs = nil
		return
	}
	if r.TTLMs == nil || *ttl <= *r.TTLMs {
		return
	}
	extended := *ttl
	r.TTLMs = &extended
}

// active returns every non-terminal record, reconciliation artifacts INCLUDED:
// a task the gateway could not prove canceled must never leave the sweep's
// field of view.
func (l *taskLedger) active(match func(TaskRecord) bool) []TaskRecord {
	if l == nil {
		return nil
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	out := make([]TaskRecord, 0)
	for _, rec := range l.byID {
		if taskRecordActive(rec, now) && (match == nil || match(rec)) {
			out = append(out, rec)
		}
	}
	return out
}

// beginCancelAttempt reserves the single in-flight server-initiated cancellation
// of one task GENERATION and returns its attempt number (the operation-identity
// generation of the sweep binding). ok=false means NO automatic dispatch is
// permitted right now — the generation is stale/retired, another attempt is in
// flight, or a previous attempt ended ambiguously or already delivered — and the
// caller must skip and report instead of minting a fresh identity (F-01, N-03).
func (l *taskLedger) beginCancelAttempt(taskID, generation string) taskCancelReservation {
	if l == nil {
		return taskCancelReservation{attempt: 1, ok: true}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancels == nil {
		l.cancels = map[string]*taskCancelIntent{}
	}
	if reason, retired := l.retired[generation]; retired {
		return taskCancelReservation{bar: taskCancelBarStale, reason: "task generation retired: " + reason}
	}
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return taskCancelReservation{
			bar:    taskCancelBarStale,
			reason: "the authorized task record no longer holds this identifier",
		}
	}
	in := l.cancels[generation]
	if in == nil {
		in = &taskCancelIntent{}
		l.cancels[generation] = in
	}
	if in.inFlight {
		return taskCancelReservation{
			bar:    taskCancelBarInFlight,
			reason: "another server-initiated cancellation of this task is already in flight",
		}
	}
	if in.bar != taskCancelBarNone {
		return taskCancelReservation{bar: in.bar, reason: in.barReason}
	}
	in.attempt++
	in.inFlight = true
	return taskCancelReservation{attempt: in.attempt, ok: true}
}

// endCancelAttempt releases the in-flight reservation. retryable=false BARS
// every future automatic attempt for this generation: the outcome was ambiguous,
// or the cancellation was actually emitted. Only explicit reconciliation
// (clearCancelBar) may lift the bar.
//
// Round-4: this is the intent-only entry point onto endCancelAttemptLocked, the
// ONE intent transition. settleCancelAttempt — the production path — calls the
// same primitive, so the rule below cannot drift away from the rule production
// applies.
func (l *taskLedger) endCancelAttempt(generation string, retryable bool, bar taskCancelBar, reason string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.endCancelAttemptLocked(generation, false, retryable, bar, reason)
}

// endCancelAttemptLocked is THE per-generation intent transition: it clears the
// in-flight reservation and records the resulting bar.
//
// ROUND-3 R3-05: a DELIVERED cancellation is never re-armed by a later retryable
// verdict and never downgraded to a weaker bar. The delivered bar may be recorded
// by the very caller that observed the acknowledgement WHILE its own attempt is
// still in flight; dropping it would leave the generation armed for a duplicate
// cooperative cancellation of a task the upstream was already told to cancel.
func (l *taskLedger) endCancelAttemptLocked(generation string, acked, retryable bool, bar taskCancelBar, reason string) {
	in := l.cancels[generation]
	if in == nil {
		return
	}
	in.inFlight = false
	switch {
	case acked:
		in.bar = taskCancelBarDelivered
		in.barReason = reason
	case in.bar == taskCancelBarDelivered:
		// Never re-armed and never downgraded.
	case !retryable:
		in.bar = bar
		in.barReason = reason
	}
}

// taskCancelBookkeeping is the caller's DECLARATIVE record-state policy for one
// settled cancellation attempt. It is data, not a callback that runs under the
// ledger mutex: the caller computes it from the dispatch outcome and the ledger
// applies it atomically (round-4 R4-01).
type taskCancelBookkeeping struct {
	// status ("" ⇒ leave untouched) is a locally INFERRED status; it is never
	// authoritative (only confirmStatus is), so a terminal value here always sets
	// TerminalUnconfirmed.
	status       string
	statusReason string
	// reconcileIfQuarantined turns an already-QUARANTINED (ungoverned) record into
	// a pure reconciliation artifact: it was never a tracked task for client
	// purposes and must not be resurrected as one.
	reconcileIfQuarantined bool
	// quarantine ("" ⇒ none) marks the record for reconciliation.
	quarantine string
}

// taskCancelSettlement is the ATOMIC post-dispatch transition of ONE cancellation
// attempt (round-4 R4-01): the cancellation-unconfirmed record state, the
// per-generation intent bar, the end of the in-flight reservation AND the release
// of the generation pin, all under a single ledger mutex.
//
// Round-3 performed these four steps in four separate calls with the pin released
// FIRST — by a `defer` inside the dispatch helper, before the caller had even
// computed its verdict. A concurrent lookup/insert in that window ran
// evictExpiredLocked against a record that was still `working`, no longer pinned
// and not yet cancellation-unconfirmed: it was deleted and tombstoned, the later
// generation-CAS status write failed, and a task whose cancellation was merely
// ACKNOWLEDGED — never proven terminal — disappeared from every later sweep.
type taskCancelSettlement struct {
	taskCancelBookkeeping
	// dispatched reports that the upstream forward was INVOKED, so the effect may
	// have reached the upstream. It is what installs CancelUnconfirmed, and it is
	// deliberately conservative: an attempt that unwound abnormally sets it too.
	dispatched bool
	// acked reports a CONFORMING acknowledgement round trip (conformingCancelAck):
	// the cancellation was DELIVERED. A relayable completed round trip alone never
	// sets it — stage-7 H-1/H-1R2: a body the extension's CancelTaskResult
	// contract forbids proves no delivery, whatever the transport says. It records
	// the strongest bar, which is never downgraded.
	acked bool
	// retryable permits a further AUTOMATIC attempt (nothing was emitted, or the
	// outcome proves the request never reached the upstream).
	retryable bool
	bar       taskCancelBar
	barReason string
	// releaseLease drops the generation pin this attempt held. Set by the guard
	// that acquired it, never by the caller.
	releaseLease bool
}

// settleCancelAttempt applies one taskCancelSettlement atomically. It is the ONLY
// way a cancellation attempt ends: no caller may release the pin, end the
// reservation or write the cancellation status separately (round-4 R4-01).
func (l *taskLedger) settleCancelAttempt(taskID, generation string, s taskCancelSettlement) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. The RECORD transition — compare-and-swap on the generation. Deliberately
	//    NO evictExpiredLocked first: this call exists to make the record
	//    TTL-immune, so it must never run the eviction it is racing.
	if rec, ok := l.byID[taskID]; ok && rec.Generation == generation {
		if s.dispatched {
			rec.CancelUnconfirmed = true
		}
		if s.status != "" {
			rec.Status = strings.TrimSpace(s.status)
			rec.StatusReason = strings.TrimSpace(s.statusReason)
			// A locally inferred terminal status is ALWAYS unconfirmed.
			rec.TerminalUnconfirmed = taskStatusTerminal(rec.Status)
			// ROUND-7: an INFERRED status is by definition not a confirmed terminal
			// report the owner read, so it drops any earlier proof of delivery. The
			// record must be re-confirmed AND re-collected before it may be retired.
			// ROUND-8 R8-02: dropping the AUTHORITATIVE REPORT IDENTITY is what does it
			// — the proof is an equality against that field, not a flag beside it.
			rec.TerminalReportDigest = ""
			rec.OwnerCollectedDigest = ""
			if s.reconcileIfQuarantined && rec.Quarantined {
				rec.Reconciling = true
			}
		}
		if s.quarantine != "" {
			rec.Quarantined = true
			rec.QuarantineReason = s.quarantine
		}
		l.byID[taskID] = rec
	}

	// 2. The INTENT transition — the SAME primitive endCancelAttempt uses, so the
	//    round-3 R3-05 rule cannot drift between the two entry points (it unifies
	//    the old endCancelAttempt/barCancelIntent pair, so a delivered bar can
	//    never be dropped between them).
	l.endCancelAttemptLocked(generation, s.acked, s.retryable, s.bar, s.barReason)

	// 3. The PIN — dropped LAST, still under this mutex, so no eviction can
	//    interleave between the state install and the unpin.
	if s.releaseLease {
		l.releaseEffectLeaseLocked(generation)
	}
}

// cancelIntentState reports the per-generation cancellation intent for the
// operator reconciliation inventory (identifiers and classes only).
func (l *taskLedger) cancelIntentState(generation string) (bar taskCancelBar, reason string, inFlight bool) {
	if l == nil {
		return taskCancelBarNone, "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	in := l.cancels[generation]
	if in == nil {
		return taskCancelBarNone, "", false
	}
	return in.bar, in.barReason, in.inFlight
}

// retireReconciled is the operator RETIREMENT of one reconciled record: a
// compare-and-delete by generation that refuses unless the record's TERMINAL
// status was CONFIRMED by an authoritative upstream report. Automatic TTL
// deletion is not an acceptable substitute for this (round-4): a record may only
// leave the ledger when the external task is PROVEN finished, and the proof is
// the confirmStatus a reconciliation `tasks/get` produced.
//
// It also refuses while a cancellation of the generation is in flight or pinned —
// retiring underneath a live external effect is the same class of defect
// release() already refuses.
//
// ROUND-5 R5-03 (SAFE DRAIN), stated exactly, because "an operator may drain a
// record" deserves no vagueness. What retirement ends is the LOCAL governance
// record of a task the upstream has PROVEN finished, and nothing else:
//
//   - it cannot touch a record whose terminal status is merely INFERRED
//     (`TerminalUnconfirmed`), nor a pending, working or cancellation-unconfirmed
//     one — `retirable()` is false for all of them;
//   - it cannot touch a record with a cancellation in flight or a governed effect
//     pinned (refused below), so no admitted effect ever loses the identity it was
//     authorized against;
//   - what it withdraws is the authorization to OPERATE a task that provably has
//     nothing left to operate: a terminal task takes no further input and cannot
//     be canceled.
//
// ROUND-6 R6-02, the correction: round-5 also DELETED the row, and the row was
// the owner's only authorization to READ its own task. The owning client's next
// `tasks/get` then received 403 "task not tracked" with no upstream call at all,
// so an operator draining the inventory destroyed the final tool result of a task
// whose upstream TTL had not elapsed — through the gateway whose entire job is to
// be the path to that upstream. The round-5 comment claiming the upstream "serves
// it to any holder of the handle" was false FOR THIS GATEWAY: the client does not
// hold a route to the upstream, only to the PEP.
//
// ROUND-7, the design change: retirement is a compare-and-delete again, and it is
// GATED ON PROOF OF DELIVERY instead of compensated by a cache.
//
// Round-6's answer to R6-02 was to keep the row as a bounded "handoff" the owner
// could still read. That cache then generated its own family of findings, all of
// them consequences of the cache itself and not of the problem: the 513th
// retirement deleted an UNREAD oldest result purely for being oldest (R7-02), a
// leased row left the FIFO without being deleted and grew the ledger past its own
// bound (R7-02), the cache was discharged by a response write whose failure nobody
// checked (R7-03), and a privileged status read could rewrite a supposedly final
// retired row (R7-05). Patching each produced the next.
//
// So the obligation is enforced BEFORE the destructive step rather than repaired
// after it:
//
//   - `retirable()` — the upstream PROVED the work stopped; and
//   - `ownerCollectionSatisfied()` — the owner has nothing left to collect,
//     either because it was successfully served the CURRENTLY authoritative terminal
//     report through this gateway, or because no handle byte provably ever reached it
//     (`!HandleRelayed`), so there is no delivery to protect.
//
// ROUND-9 R9-03/N10-02: the second arm is HANDLE-RELAY PROVENANCE, not "the record
// was never client-readable". Round-7's current-state reading is exactly the R8-01
// defect — a delivered row that a later quarantine made unreadable was treated as
// owing nothing and deleted unread.
//
// ROUND-11 N11-01: "no delivery to protect" is a claim about ACCEPTED BYTES, not about
// the response write returning an error. `!HandleRelayed` is now recorded only for a
// relay that never ran or that the writer reported accepting ZERO bytes for; a write
// error with a positive or unreportable count records possible relay, so this arm can
// no longer authorize deleting a result whose identifier its owner is holding.
//
// A row that fails the second condition is not evicted, cached or downgraded: it
// stays an ORDINARY row. It counts against the retention bound, it is listed, it
// is `actionable`, and this function refuses it with a reason that names the exact
// missing precondition. That is strictly better than either predecessor — round-5
// destroyed the result, round-6 could evict it unread, and this can do neither,
// because nothing is retired until the result has demonstrably arrived.
//
// The bounded, visible cost, stated at its real size (round-9 R9-03; round-11 N11-02
// removed the remaining overgeneralization): an owner that never polls pins ONE
// retention slot per uncollected task.
//
// For a NORMAL row the slot lasts until the record TTL-expires — which is NOT
// unconditional. `taskExpired` (`:2000-2032`) never expires a row whose `TTLMs` is nil
// (including a report that set it back to unbounded), whose TTL is zero, negative or
// beyond `maxTaskDurationMillis`, or whose cancellation is unconfirmed
// (`taskCancellationUnconfirmed`). Any of those makes even a normal row hold its slot
// exactly like a quarantined one.
//
// For a row that is quarantined or reconciling — including BOTH ambiguous-relay
// classes, the abnormal unwind and the round-11 partial write — there is categorically
// no TTL: the quarantine exemption is the first rule of `taskExpired` (`:2000-2007`),
// so the slot is held until process loss or a future audited abandon action.
// Either way it is the existing cap doing its job on the owner that caused it, in full
// view of the operator — not new unbounded state.
//
// ROUND-8: the whole decision is `compareDeleteTerminalLocked`, which the automatic
// terminal-artifact `release` now shares (R8-01). This function contributes the
// evidence lifecycle and the operator-facing reason string, nothing else — there is
// no second deletion rule left to diverge from this one.
func (l *taskLedger) retireReconciled(taskID, generation string) (bool, string) {
	if l == nil {
		return false, "no task ledger"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.compareDeleteTerminalLocked(taskID, generation,
		"record retired by operator reconciliation after a confirmed terminal status its owner had collected")
}

// recordOwnerTerminalCollection installs the PROOF OF DELIVERY of round-7: the
// owner of this exact generation was successfully served a conforming TERMINAL
// report of its own task. It is a compare-and-swap on the generation and it
// refuses to record a delivery against a record whose own confirmed status is not
// terminal, so the proof can never outlive the belief it is about.
//
// It is called only AFTER the response write to the owner succeeded (rs.go). A
// write that failed is not a delivery — that is R7-03, and it is why the write
// path reports its error instead of discarding it.
//
// ROUND-8 R8-02: the caller names the DIGEST of the report it actually served, and
// the swap additionally compares it against the report that is authoritative under
// this same mutex. That closes the interleaving a bare boolean could not see: the
// owner reads report A, a concurrent privileged status read confirms a DIFFERENT
// terminal report B, and the owner's late write-through would otherwise have
// recorded "collected" against B — a report it never saw. An empty digest is never
// proof.
func (l *taskLedger) recordOwnerTerminalCollection(taskID, generation, reportDigest string) bool {
	if l == nil || generation == "" || reportDigest == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false
	}
	if !rec.retirable() || rec.TerminalReportDigest != reportDigest {
		return false
	}
	rec.OwnerCollectedDigest = reportDigest
	l.byID[taskID] = rec
	return true
}

// recordHandleRelay installs the IMMUTABLE handle-relay provenance of round-8
// (R8-01): the gateway's response writer accepted the complete tools/call body
// carrying this task's handle, so the gateway must from now on assume its owner
// MAY hold the unguessable task id.
//
// It is a compare-and-swap on the generation, it is called ONLY after a successful
// write (rs.go, under the generation lease that keeps the row from being retired or
// evicted in between), and it is WRITE-ONCE: no later transition — quarantine,
// reconciliation, cancellation, a replacement status — may clear it, because it
// records something that already happened.
//
// ROUND-9 R9-01: its RESULT is enforced by the caller. A refusal means the row this
// relay was authorized against is gone or was replaced — the client is holding a
// handle this gateway has no governance row for — and that is an audited
// reconciliation fact, not something to discard.
//
// Honest limit of what the write result proves, stated exactly (R8-06, narrowed in
// round-9 R9-04): a nil encoder error means this process's `http.ResponseWriter`
// accepted every byte of the encoded body. It is NOT an acknowledgement from the
// remote client — this provenance is the conservative ASSUMPTION that the owner may
// have received the id, never proof that the remote process did — and no Go HTTP
// contract offers such proof. The governance consequence follows from the
// assumption: a record whose handle may have been relayed owes a collection.
func (l *taskLedger) recordHandleRelay(taskID, generation string) bool {
	if l == nil || generation == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false
	}
	rec.HandleRelayed = true
	l.byID[taskID] = rec
	return true
}

// custodyAmbiguousHandleRelay is the ONE atomic close-out of a DELIVERY-AMBIGUOUS
// handle relay. Two distinct situations reach it:
//
//   - the relay unwound ABNORMALLY (round-9 R9-01): a panic in the response writer,
//     the encoder or anything else between the pinned finalization and the classified
//     outcome; or
//   - the response write returned an error AFTER the writer had accepted body bytes,
//     or without a usable count (round-11 N11-01). `json.Encoder` makes one `Write`
//     call and discards its count, and `io.Writer` lets a conforming writer accept
//     `len(p)-1` bytes and still fail — and that prefix can be the entire JSON-RPC
//     response minus the encoder's newline, which JSON-RPC does not require.
//
// Both are DELIVERY-AMBIGUOUS. Bytes may already be on the wire, and no Go contract
// says how many. So the transition is conservative on BOTH axes at once:
//
//   - `HandleRelayed` is set TRUE — the owner MAY hold the identifier. Treating an
//     ambiguous unwind as "certainly never delivered" would be the dangerous
//     direction: `ownerCollectionSatisfied` would then authorize an operator to
//     delete a result the owner is entitled to read (the R8-01 failure, re-entered
//     through the panic path);
//   - the row is QUARANTINED — the gateway cannot prove the registration response
//     completed, so the record stops being client-operable and stops being
//     TTL-forgettable, and it is drained by the reconciliation surface instead.
//
// The two together mean the row is retained, visible, counted, and NOT retirable
// until an operator proves terminality and the owner's obligation is discharged.
// That is the declared availability cost of an ambiguous relay — BOTH callers land in
// residual **11** (delivery-ambiguous / delivered-then-quarantined, TTL-immune), NOT
// in residual 12, which after round-11 N11-01 covers only the pressure of writes that
// PROVABLY accepted zero bytes, where `HandleRelayed` stays false. It is one atomic
// compare-and-swap on the generation — never two mutations another goroutine could
// interleave.
func (l *taskLedger) custodyAmbiguousHandleRelay(taskID, generation, reason string) bool {
	return l.mutate(taskID, generation, func(rec *TaskRecord) {
		rec.HandleRelayed = true
		rec.Quarantined = true
		rec.QuarantineReason = reason
	})
}

// barCancelIntent records a bar on a generation from a caller that OBSERVED the
// cancellation outcome outside the sweep/compensation actuator — the client's own
// tasks/cancel (round-2 N-02: an acknowledged cooperative cancellation must not
// be re-emitted automatically).
//
// Round-3 R3-05: a DELIVERED bar is recorded even while an attempt is in flight.
// The round-2 guard `if !in.inFlight` silently DROPPED it in exactly the case
// that matters — the caller is inside its own reservation when it observes the
// acknowledgement — and a later retryable verdict then left the generation
// re-armed for a duplicate dispatch. `delivered` is the strongest bar (the effect
// provably reached the upstream) and is never lost or downgraded.
func (l *taskLedger) barCancelIntent(generation string, bar taskCancelBar, reason string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancels == nil {
		l.cancels = map[string]*taskCancelIntent{}
	}
	in := l.cancels[generation]
	if in == nil {
		in = &taskCancelIntent{}
		l.cancels[generation] = in
	}
	if bar == taskCancelBarDelivered || !in.inFlight {
		in.bar = bar
		in.barReason = reason
	}
}

// clearCancelBar is the explicit reconciliation entry point: it re-arms
// automatic cancellation for ONE named generation of taskID.
//
// Round-3 R3-07: it takes the EXPECTED generation and refuses unless the live
// record AND the cancellation intent both match it. Resolving the record by bare
// task id was the one mutation site outside the compare-and-swap discipline: a
// reconciliation action scheduled against retired `T/G1` cleared the bar of an
// unrelated replacement `T/G2`, re-arming automatic cancellation of a task
// nobody reconciled. It reports whether the bar was actually cleared, so a
// caller can never mistake a refusal for a successful reconciliation.
func (l *taskLedger) clearCancelBar(taskID, generation string) bool {
	if l == nil || generation == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, retired := l.retired[generation]; retired {
		return false
	}
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false
	}
	in := l.cancels[generation]
	if in == nil || in.inFlight {
		return false
	}
	in.bar = taskCancelBarNone
	in.barReason = ""
	return true
}

func (l *taskLedger) activeCountLocked(owner taskOwner, now time.Time) int {
	count := 0
	for _, rec := range l.byID {
		if rec.owner().equals(owner) && taskRecordActive(rec, now) {
			count++
		}
	}
	return count
}

func (l *taskLedger) activeTotalLocked(now time.Time) int {
	count := 0
	for _, rec := range l.byID {
		if taskRecordActive(rec, now) {
			count++
		}
	}
	return count
}

// retainedCountLocked counts every record the ledger holds for one owner that
// still CONSUMES the retention bound — active, pending, quarantined, reconciling
// and cancellation-unconfirmed alike. The whole point of the retention bound is
// that the states which BYPASS the active caps are the ones that grow (round-4
// R4-05).
//
// ROUND-7: there is no exemption. Round-6 exempted `Retired` rows because
// retirement kept them alive as a separately-bounded handoff cache; with proof-of-
// delivery retirement a retired record is DELETED, so every row in `byID` is a row
// an owner (or the operator) still has to resolve, and every one of them counts.
// A proven-terminal record whose owner has not collected it therefore consumes the
// bound of the owner who created it, exactly like any other retained row.
func (l *taskLedger) retainedCountLocked(owner taskOwner) int {
	count := 0
	for _, rec := range l.byID {
		if rec.owner().equals(owner) {
			count++
		}
	}
	return count
}

// retainedTotalLocked counts every bound-consuming record of every owner (the
// gateway-wide half of the same rule).
func (l *taskLedger) retainedTotalLocked() int {
	return len(l.byID)
}

// retainedCapPerOwner is the hard per-owner retention bound.
func (l *taskLedger) retainedCapPerOwner() int {
	return l.maxActiveBySubject * taskRetentionHeadroomFactor
}

// retainedGlobalCap is the hard gateway-wide retention bound.
func (l *taskLedger) retainedGlobalCap() int {
	return l.globalActiveCap * taskRetentionHeadroomFactor
}

// taskAdmissionState reports the retained-inventory pressure of one owner. It is
// minimal-data by construction: counts and the bounds, never identities. The
// retained counts INCLUDE the slots reserved by already-admitted in-flight calls
// (round-5 R5-02) — an admitted call whose task does not exist yet consumes the
// bound exactly as a stored record does, because that is precisely the window in
// which the record cannot be seen.
type taskAdmissionState struct {
	OwnerRetained int
	OwnerCap      int
	TotalRetained int
	TotalCap      int
}

// saturated reports that no further TASK-PRODUCING forward may be admitted.
func (s taskAdmissionState) saturated() bool {
	return s.OwnerRetained >= s.OwnerCap || s.TotalRetained >= s.TotalCap
}

// taskAdmissionTicket is ONE reserved retention slot, held from before a
// task-producing forward until the task it may create is stored (or provably not
// created). It is not a lock: it is an accounting claim on the bound, so the
// bound holds under concurrency instead of only under serialization.
//
// Every successful reservation MUST end exactly once, through `consume` (the
// created task is now retained in `byID`, which accounts for the slot from here
// on) or `release` (the result was synchronous, or no task exists). Both are the
// same decrement — the distinction is WHEN it is safe to call: `consume` only
// after the record is stored, `release` any time. Ending twice is a no-op.
type taskAdmissionTicket struct {
	ledger *taskLedger
	owner  taskOwner
	held   bool
}

// consume ends the reservation after the created task has been RETAINED in the
// ledger: the stored record now occupies the slot the reservation was holding.
func (t *taskAdmissionTicket) consume() { t.end() }

// release ends the reservation when NO task was created (a synchronous tools/call
// result, a refused/failed dispatch, an ungovernable handle): the slot goes back.
func (t *taskAdmissionTicket) release() { t.end() }

func (t *taskAdmissionTicket) end() {
	if t == nil || !t.held || t.ledger == nil {
		return
	}
	t.held = false
	l := t.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.admitted[t.owner]; n > 1 {
		l.admitted[t.owner] = n - 1
	} else {
		delete(l.admitted, t.owner)
	}
	if l.admittedTotal > 0 {
		l.admittedTotal--
	}
}

// reserveAdmission is the PRE-FORWARD retention ADMISSION of round-4 R4-05, made
// atomic in round-5 R5-02. It is consulted BEFORE a tools/call that could return
// a durable task handle, because after the upstream has created one the only
// remaining choices are to retain it (unbounded growth) or to forget it (a
// permanent invisible orphan).
//
// Round-4 only READ the counts (`admissionState`) and released the mutex
// immediately, which made the check a snapshot every concurrent caller passed.
// The check and the claim on the bound are now ONE critical section: the caller
// leaves holding a ticket that counts against the bound for the whole window in
// which its task may be created, and the ticket ends only once that task is
// stored (or provably absent).
//
// A nil ledger is an unwired gateway: it tracks nothing, so it saturates nothing.
func (l *taskLedger) reserveAdmission(owner taskOwner) (*taskAdmissionTicket, taskAdmissionState, bool) {
	if l == nil {
		return nil, taskAdmissionState{}, true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	state := l.admissionStateLocked(owner)
	if state.saturated() {
		return nil, state, false
	}
	if l.admitted == nil {
		l.admitted = map[taskOwner]int{}
	}
	l.admitted[owner]++
	l.admittedTotal++
	return &taskAdmissionTicket{ledger: l, owner: owner, held: true}, state, true
}

// admissionState reports the current retention pressure of one owner WITHOUT
// reserving anything (the operator inventory's saturation view).
func (l *taskLedger) admissionState(owner taskOwner) taskAdmissionState {
	if l == nil {
		return taskAdmissionState{}
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpiredLocked(now)
	return l.admissionStateLocked(owner)
}

func (l *taskLedger) admissionStateLocked(owner taskOwner) taskAdmissionState {
	return taskAdmissionState{
		OwnerRetained: l.retainedCountLocked(owner) + l.admitted[owner],
		OwnerCap:      l.retainedCapPerOwner(),
		TotalRetained: l.retainedTotalLocked() + l.admittedTotal,
		TotalCap:      l.retainedGlobalCap(),
	}
}

// evictExpiredLocked drops TTL-expired records and TOMBSTONES their generations,
// so a replacement task that reuses the identifier can never inherit the evicted
// record's cancellation intent (round-2 N-03 scenario A). A generation pinned by
// an in-flight governed effect is NEVER evicted (round-3 R3-03): expiring it
// mid-flight is exactly what let a replacement take the identifier under an
// already-admitted forward.
func (l *taskLedger) evictExpiredLocked(now time.Time) {
	for id, rec := range l.byID {
		if l.leasedLocked(rec.Generation) {
			continue
		}
		if taskExpired(rec, now) {
			delete(l.byID, id)
			l.retireLocked(rec.Generation, "record TTL-expired")
		}
	}
}

func (l *taskLedger) now() time.Time {
	if l.clock == nil {
		return time.Now().UTC()
	}
	return l.clock().UTC()
}

func taskRecordActive(rec TaskRecord, now time.Time) bool {
	if taskExpired(rec, now) {
		return false
	}
	// A terminal status the gateway merely INFERRED from a cancellation
	// acknowledgement never removes the task from reconciliation or from a
	// future sweep (round-2 N-02).
	return !taskStatusTerminal(rec.Status) || rec.TerminalUnconfirmed
}

// taskCancellationUnconfirmed reports that the gateway REQUESTED or INFERRED a
// cancellation of this task but never had a terminal status confirmed by an
// authoritative upstream report (round-3 R3-02). Three shapes reach this state:
//
//   - `cancel_requested` — a client cancel, a sweep or a compensation received an
//     empty acknowledgement, which SEP-2663 explicitly does not make terminal;
//   - `cancel_failed` — the cancellation provably did NOT succeed, so the task is
//     presumed still running;
//   - a TERMINAL status carrying TerminalUnconfirmed — the historical `canceled`
//     label of the tool-revoked path, inferred from an acknowledgement.
//
// Round-4 R4-01 adds a fourth, which is the one the status shapes could not
// express: `CancelUnconfirmed`, installed atomically with the release of the
// generation pin the instant a cancellation was DISPATCHED. An `unknown` or
// unsettled cancellation writes no status at all, yet its effect may equally have
// reached the upstream — and the three status shapes above were only written
// AFTER the pin had already been dropped.
//
// In all four the external task may still be alive. Such a record must stay in
// the ledger until `tasks/get` (or explicit reconciliation) proves otherwise.
func taskCancellationUnconfirmed(rec TaskRecord) bool {
	if rec.CancelUnconfirmed {
		return true
	}
	switch rec.Status {
	case taskCancelRequestedStatus, taskCancelFailedStatus:
		return true
	}
	return rec.TerminalUnconfirmed && taskStatusTerminal(rec.Status)
}

func taskExpired(rec TaskRecord, now time.Time) bool {
	// A quarantined or reconciling record names a live external task the gateway
	// could not prove it governed (or could not prove it canceled). Expiring it
	// would recreate the permanent-orphan failure (F-03), so TTL eviction never
	// applies to it.
	if rec.Quarantined || rec.Reconciling {
		return false
	}
	// ROUND-3 R3-02: the same reasoning covers every NORMAL governed record whose
	// cancellation is unconfirmed. Round-2 only exempted quarantined/reconciling
	// artifacts, so a plain governed task that a client cancel or a sweep had
	// merely REQUESTED still vanished at `CreatedAt + ttlMs` — deleted and
	// tombstoned without any authoritative confirmation that the work stopped,
	// and therefore absent from every later kill-switch sweep. A
	// cancellation-unconfirmed record is retained until an authoritative
	// `tasks/get` confirms its terminal status (confirmStatus) or an operator
	// reconciles it explicitly.
	if taskCancellationUnconfirmed(rec) {
		return false
	}
	if rec.TTLMs == nil || rec.CreatedAt.IsZero() {
		return false
	}
	// Defense in depth against round-2 N-05: a TTL beyond the largest safely
	// representable millisecond duration would wrap the signed multiplication
	// negative and evict a live, freshly registered task on its very next read.
	// Strict parsing already refuses such a value; a record injected by any other
	// path is treated as UNBOUNDED rather than silently forgotten.
	if *rec.TTLMs <= 0 || *rec.TTLMs > maxTaskDurationMillis {
		return false
	}
	return !now.Before(rec.CreatedAt.Add(time.Duration(*rec.TTLMs) * time.Millisecond))
}

func taskStatusTerminal(status string) bool {
	switch status {
	case taskStatusCompleted, taskStatusFailed, taskStatusCanceled:
		return true
	}
	return false
}

// taskCancelFailedStatus is the NON-terminal status of a task whose
// server-initiated cancellation did not provably succeed: it stays visible to
// reconciliation and to future sweeps instead of disappearing (F-02).
const taskCancelFailedStatus = "cancel_failed"

// taskCancelRequestedStatus is the NON-terminal status of a task whose
// cancellation the upstream ACKNOWLEDGED without confirming a terminal state
// (round-2 N-02). The MCP tasks extension documents cooperative cancellation
// explicitly: an empty acknowledgement leaves the final status open, so the
// gateway records the REQUEST, keeps the task, bars automatic re-dispatch and
// waits for `tasks/get` (or explicit reconciliation) to prove the terminal
// status. Treating the acknowledgement as terminal was the round-1 fail-open
// that hid a live task from every later kill-switch sweep.
const taskCancelRequestedStatus = "cancel_requested"

// quarantineNote renders a stable reconciliation note for the audit trail.
func quarantineNote(taskID, reason string) string {
	return fmt.Sprintf("task %s retained for reconciliation (%s)", taskID, reason)
}
