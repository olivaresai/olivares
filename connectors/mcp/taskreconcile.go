// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// taskreconcile.go (stage 4, round-4) — the OPERATOR RECONCILIATION SURFACE
// of the durable task ledger.
//
// Stage 4 deliberately RETAINS records the gateway cannot prove finished:
//
//   - a quarantined orphan (a created upstream task whose governed registration
//     or compensating cancellation could not be confirmed) never expires;
//   - a cancellation-unconfirmed record (an acknowledged, failed or ambiguous
//     cancellation) never expires either, because a cooperative acknowledgement
//     is not proof that the work stopped;
//   - an ambiguous cancellation attempt installs a BAR that suppresses every
//     later automatic cancellation of that generation.
//
// Those rules are correct — the alternative is forgetting a live external task —
// but they are only half a design without a way OUT. Until this file existed the
// exits (`reconciliationRecords`, `clearCancelBar`) had no production caller at
// all: the retained records could only grow and the bars could only accumulate.
// Automatic TTL deletion is explicitly NOT an acceptable exit; the only
// legitimate ones are an AUTHORITATIVE upstream confirmation and an explicit,
// evidence-bound operator decision.
//
// The surface is a set of JSON-RPC methods on the RS's own authenticated socket
// rather than a control-plane HTTP route, because the cache state it repairs is
// scoped to THIS gateway instance, and because a connector may never import the
// AGPL control plane (`scripts/check-boundary.sh`). It
// therefore reuses the RS's existing authorization substrate exactly as the other
// governed method families do:
//
//   - route registration: `dispatch` routes the `tasks/reconcile/*` family to
//     this handler BEFORE the generic default-deny matrix, like tools/call and
//     the client task methods;
//   - permission constant: `scopeTasksReconcile`, a dedicated privileged scope,
//     enforced deny-closed with the same SEP-835 step-up challenge the other
//     family scopes use — an ordinary task token can never reach it;
//   - evidence/audit on EVERY MUTATING action: claim + anchor before the effect,
//     leadership fence immediately before it, honest settlement before the
//     response, plus the best-effort denial audits;
//   - deny-closed everywhere: an unknown action, an unmatched generation, an
//     unmatched owner assertion, a refused claim or a task that is not provably
//     terminal all refuse without mutating anything.
//
// Every action is GENERATION- and OWNER-bound: the operator must name the exact
// record generation AND the canonical owner digest the listing reported, and both
// are re-verified against the live record under the ledger mutex. That is what
// makes a delayed or mistargeted reconciliation impossible (the round-3 R3-07
// class), and it means the surface can never actuate a REPLACEMENT task that
// merely reuses the same textual identifier.
//
// # Instance targeting and durability (round-5 R5-03)
//
// The surface repairs an IN-MEMORY cache of the durable task authority (see the
// `taskLedger` type comment). Two consequences an operator must plan around:
//
//   - TARGETING: a reconciliation request only ever sees the inventory of the
//     gateway INSTANCE that served it. Behind a load balancer an operator must
//     therefore address one instance at a time (per-pod address, session-pinned
//     route, or a control-plane proxy that fans out). Every `list` response
//     carries the serving instance's identity (`instance`) so a page, a cursor and
//     a drain can be attributed to exactly one process, and a continuation cursor
//     is REFUSED by any other instance rather than silently re-paginating a
//     different inventory.
//   - DRAINING: a traversal is a SNAPSHOT, not a live view. Read the
//     `reconcileCursor` contract before automating on this surface: reaching the
//     end of a cursor chain means "this snapshot is fully traversed", never "the
//     ledger is drained". Rows that arrive mid-traversal are reported as
//     `newerThanSnapshot` and require a further pass from the head.
//   - DURABILITY: registered task rows survive through DurableTaskStore and are
//     rehydrated before the RS starts serving. In-flight reservations,
//     cancellation bars, cursor snapshots and an orphan whose registration
//     failed are process-only; a restart never fabricates those facts from the
//     durable projection.
//
// # The two questions this surface answers, and the one it does not (round-7 R7-04)
//
// Round-6 collapsed these into a single sentence — "a full pass with no actionable
// rows is a completed drain" — and that sentence is false in the direction that
// costs data. They are separate questions with separate answers:
//
//  1. "IS THERE A HUMAN RECONCILIATION BACKLOG?" — this surface answers it, for
//     one snapshot of one instance. The answer is YES while any returned row has
//     `actionable:true`, or while `newerThanSnapshot > 0` (rows exist that this
//     traversal cannot see). It is a question about work an OPERATOR must do.
//
//  2. "IS IT SAFE TO RESTART THIS PROCESS?" — this surface CANNOT answer it, and
//     no field in a list response can. Zero actionable rows is compatible with a
//     cache holding healthy `live` work and `registration-pending` registrations
//     mid-settlement. Registered live rows rehydrate, but the in-flight cache
//     transition and any registration that has not reached DurableTaskStore do
//     not; neither condition is a human backlog.
//     (Round-7's wording listed `terminal-awaiting-owner` here as well, which
//     contradicted this file's own class/action code — that class IS actionable, so
//     it can never appear in a zero-actionable page. Corrected in round-8 R8-06.)
//     A row can also be inserted the instant AFTER the final page is captured, so
//     no already-serialized response can report it.
//
// The restart-safety question has a PREREQUISITE this package does not implement,
// because it lives outside the process: ADMISSION QUIESCENCE. Until task-producing
// requests can no longer arrive at this instance, there is no such thing as a
// final answer — only a stale one. The exact external procedure:
//
//	a. QUIESCE. Stop routing task-producing traffic (tools/call) to this instance:
//	   remove it from the load-balancer pool, or stop the listener. Reconciliation
//	   traffic may continue — it creates no rows.
//	b. TRAVERSE FROM THE HEAD. Start a FRESH traversal (no cursor) so it pins a
//	   watermark taken after the last possible insertion.
//	c. REQUIRE, on the final page of that traversal:
//	     - `newerThanSnapshot == 0` — nothing arrived during the traversal;
//	     - `total == 0`           — NO ROWS AT ALL, not merely no actionable ones;
//	     - `retained == 0`        — no row and no outstanding admission TICKET
//	                                consumes the bound. Tickets are counted but have
//	                                no row yet (round-6 R6-04), so this is the check
//	                                that covers a task-producing forward still in
//	                                flight; the count is a conservative upper bound,
//	                                so zero is sound.
//	d. If any check fails, resolve the rows (owner collection, reconciliation
//	   status/clear/retire) and repeat from (b). Restarting before all three hold
//	   can lose process-only custody state even though registered rows rehydrate.
//
// Nothing in this file, and nothing an operator can query on it, is a substitute
// for step (a). What the surface reports is question 1; question 2 is answered by
// the procedure above, of which this surface supplies three of the four inputs.

// Reconciliation method names. The `tasks/reconcile/` prefix cannot collide with
// any MCP protocol method: the extension defines exactly tasks/get, tasks/update
// and tasks/cancel.
const (
	methodTasksReconcileList   = "tasks/reconcile/list"
	methodTasksReconcileStatus = "tasks/reconcile/status"
	methodTasksReconcileClear  = "tasks/reconcile/clear"
	methodTasksReconcileRetire = "tasks/reconcile/retire"
)

// scopeTasksReconcile is the PERMISSION CONSTANT of the reconciliation surface: a
// dedicated privileged scope, distinct from every tool scope and from the task
// scopes an ordinary agent token carries. Deny-closed — a token without it gets a
// 403 + scope challenge and never learns whether any record exists.
const scopeTasksReconcile = "tasks:reconcile"

// maxReconcileListRecords is the PAGE SIZE of the inventory, not a clip.
//
// Round-4 sorted the whole inventory and returned the first 512 rows with
// `truncated:true` and no way to ask for the rest: the global retained bound can
// greatly exceed 512, and a prefix full of still-working cancellation records
// that cannot yet be retired made every later row permanently undiscoverable —
// while the mutating actions REQUIRE the generation and owner digest only the
// listing hands out (round-5 R5-03). The listing is now a real continuation
// contract: a stable total order plus an opaque cursor, so every row is
// reachable in a bounded number of pages.
const maxReconcileListRecords = 512

// taskReconcileReservedKeys is the reserved-key profile of the reconciliation
// params: every member this surface reads by exact name. A case-variant alias of
// any of them is refused BEFORE the claim, exactly as for the client task methods
// — the surface must never authorize one generation and act on another.
var taskReconcileReservedKeys = []string{"taskId", "generation", "ownerDigest", "cursor", "_meta"}

// canonicalReconcileParams is the strict, canonicalized view of one
// tasks/reconcile/* params payload.
type canonicalReconcileParams struct {
	canonicalTaskParams
	// Generation is the exact-cased record generation the action names.
	Generation string
	// OwnerDigest is the exact-cased canonical owner digest the operator asserts
	// the record carries.
	OwnerDigest string
	// Cursor is the opaque continuation token of a paginated inventory read.
	Cursor string
}

// reconcileCursorVersion is the cursor FORMAT version. It is authenticated
// together with the payload, so a future format can never be confused with this
// one, and a token from an older format is refused rather than reinterpreted.
const reconcileCursorVersion = "v1"

// reconcileCursorMembers is the exact member set of a v1 cursor payload.
var reconcileCursorMembers = []string{"i", "s", "t", "g"}

// reconcileCursor is the decoded continuation position: the issuing instance, the
// SNAPSHOT high-watermark the traversal is pinned to, and the (taskId,
// generation) pair of the LAST row of the previous page.
//
// ROUND-6 R6-03. Round-5 had two defects here, and they are different defects:
//
//  1. AUTHENTICITY. The token was bare base64 JSON with a SELF-REPORTED instance,
//     so any authorized caller who read `instance` out of one list response could
//     construct a cursor for an arbitrary position — start a traversal in the
//     middle, or skip an arbitrary prefix of the inventory it is supposed to be
//     draining. It is now versioned and MAC'd with a per-process key, so a cursor
//     is only ever a position THIS process issued.
//
//  2. COMPLETENESS UNDER MUTATION. A keyset cursor over live state re-derives the
//     whole inventory on every page, so a row INSERTED after page 1 whose key
//     sorts BEFORE that page's last key was never returned — and an operator who
//     read end-of-cursor as "the drain finished" lost it. The cursor now pins the
//     ledger's insertion high-watermark: a traversal is a fixed SET of rows, and
//     rows that arrive later are excluded BY CONSTRUCTION and counted in the
//     response (`newerThanSnapshot`) instead of being silently skipped.
//
// What the cursor now GUARANTEES, exactly — no more, and this is the wording the
// operator contract depends on:
//
//   - within ONE traversal (one cursor chain, one watermark), every row that
//     existed when the traversal began AND still exists when its page is served is
//     returned EXACTLY ONCE, in the stable total order (taskId, generation);
//   - a row inserted AFTER the watermark is never returned by that traversal. It is
//     not skipped silently: every page reports how many such rows exist;
//   - end-of-cursor therefore means "this snapshot is fully traversed", NEVER "the
//     ledger is drained".
//
// ROUND-7 R7-04 replaces the third bullet's old second half, which said a drain is
// complete when a traversal started after the last insertion "returns no
// actionable rows". That conflated two different completion conditions, and the
// weaker one was being used for the stronger purpose:
//
//   - NO HUMAN RECONCILIATION BACKLOG (what this traversal can establish): the
//     final page reports `newerThanSnapshot == 0` and no returned row carries
//     `actionable:true`. Rows may still EXIST — healthy `live` work and
//     `registration-pending` settlements — and neither needs an operator. A
//     `terminal-awaiting-owner` row is NOT in that list, because it is actionable
//     (round-8 R8-06 removes it from the round-7 wording, which contradicted the
//     class/action code below): a result pinned on a delivery that has not happened
//     is precisely something a human must see.
//   - SAFE TO RESTART THIS PROCESS (what NO traversal can establish on its own):
//     it additionally needs `total == 0` and `retained == 0`, and it needs those
//     read AFTER task-producing admissions to this instance have been quiesced —
//     otherwise a row can be inserted immediately after the final page is captured
//     and no field in that already-serialized response can report it. The
//     quiescence fence is EXTERNAL to this package; the exact procedure is in this
//     file's header comment.
//
// Reaching the end of a cursor chain with zero actionable rows is therefore a
// statement about an operator's queue, never a statement that the ledger may be
// destroyed.
type reconcileCursor struct {
	Instance   string `json:"i"`
	Snapshot   uint64 `json:"s"`
	TaskID     string `json:"t"`
	Generation string `json:"g"`
}

// issueReconcileCursor renders the authenticated continuation token
// `v1.<payload>.<mac>`. It is opaque by CONTRACT (operators must not construct
// one) and now also unforgeable in FACT: the MAC covers the version and the whole
// payload, keyed by a secret that never leaves this process.
func (rs *ResourceServer) issueReconcileCursor(cur reconcileCursor) (string, error) {
	raw, err := json.Marshal(cur)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	signed := reconcileCursorVersion + "." + payload
	return signed + "." + base64.RawURLEncoding.EncodeToString(rs.cursorMAC(signed)), nil
}

// cursorMAC is the keyed authenticator of one cursor token.
func (rs *ResourceServer) cursorMAC(signed string) []byte {
	mac := hmac.New(sha256.New, rs.cursorKey)
	mac.Write([]byte(signed))
	return mac.Sum(nil)
}

// parseReconcileCursor authenticates and strictly decodes a continuation token.
// Anything else — wrong version, forged or tampered MAC, malformed or extended
// payload, foreign instance, missing position — is refused. Continuing a
// traversal from a position this instance did not issue is not a paging detail:
// it decides which rows an operator believes it has drained.
func (rs *ResourceServer) parseReconcileCursor(raw string) (reconcileCursor, error) {
	invalid := fmt.Errorf("mcp: reconciliation cursor is not a valid continuation token issued by this gateway instance")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != reconcileCursorVersion {
		return reconcileCursor{}, invalid
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return reconcileCursor{}, invalid
	}
	// Constant-time comparison, and BEFORE anything else looks at the payload.
	if !hmac.Equal(got, rs.cursorMAC(parts[0]+"."+parts[1])) {
		return reconcileCursor{}, invalid
	}
	blob, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return reconcileCursor{}, invalid
	}
	// The same strict decoding the rest of this connector applies to anything it
	// resolves by member name: duplicate keys at any depth and members this format
	// does not define are refused, never last-wins.
	v, derr := decodeStrictJSON(blob)
	if derr != nil || v.kind != canonObject {
		return reconcileCursor{}, invalid
	}
	for i := range v.obj {
		known := false
		for _, member := range reconcileCursorMembers {
			if v.obj[i].key == member {
				known = true
				break
			}
		}
		if !known {
			return reconcileCursor{}, invalid
		}
	}
	var cur reconcileCursor
	for _, field := range []struct {
		key string
		dst *string
	}{{"i", &cur.Instance}, {"t", &cur.TaskID}, {"g", &cur.Generation}} {
		m := v.member(field.key)
		if m == nil || m.val.kind != canonString {
			return reconcileCursor{}, invalid
		}
		*field.dst = m.val.str
	}
	snap := v.member("s")
	if snap == nil || snap.val.kind != canonNumber {
		return reconcileCursor{}, invalid
	}
	n, nerr := strconv.ParseUint(snap.val.num.String(), 10, 64)
	if nerr != nil || n == 0 {
		return reconcileCursor{}, invalid
	}
	cur.Snapshot = n
	if cur.TaskID == "" || cur.Generation == "" {
		return reconcileCursor{}, fmt.Errorf("mcp: reconciliation cursor names no position")
	}
	// Defense in depth: the per-process key already makes a foreign cursor fail the
	// MAC. This turns the one case an operator can actually hit — paging against
	// the wrong instance behind a load balancer — into a message that says so.
	if cur.Instance != rs.instanceID {
		return reconcileCursor{}, fmt.Errorf(
			"mcp: reconciliation cursor was issued by another gateway instance (the cache snapshot is process-local; target the issuing instance)")
	}
	return cur, nil
}

// canonicalizeReconcileParams strictly decodes and canonicalizes the params of a
// reconciliation request. Any error is a PROTOCOL refusal (400/-32602) before any
// claim, ledger read or effect.
func canonicalizeReconcileParams(raw json.RawMessage) (canonicalReconcileParams, error) {
	out := canonicalReconcileParams{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		out.Presence = paramsAbsent
		return out, nil
	}
	if trimmed == "null" {
		out.Presence = paramsNull
		out.Forward = json.RawMessage("null")
		out.Effect = []byte("null")
		return out, nil
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return canonicalReconcileParams{}, err
	}
	if v.kind != canonObject {
		return canonicalReconcileParams{}, fmt.Errorf("mcp: reconciliation params must be a JSON object")
	}
	if err := rejectReservedKeyAliases(v, taskReconcileReservedKeys); err != nil {
		return canonicalReconcileParams{}, err
	}
	out.Presence = paramsPresent
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"taskId", &out.TaskID},
		{"generation", &out.Generation},
		{"ownerDigest", &out.OwnerDigest},
		{"cursor", &out.Cursor},
	} {
		m := v.member(field.key)
		if m == nil {
			continue
		}
		if m.val.kind != canonString {
			return canonicalReconcileParams{}, fmt.Errorf("mcp: reconciliation params: %s must be a string", field.key)
		}
		// The same strictness hardening the task methods apply: an untrimmed
		// spelling could name one record for the authorization and another for the
		// mutation, so the ambiguity is refused outright.
		if m.val.str != strings.TrimSpace(m.val.str) {
			return canonicalReconcileParams{}, fmt.Errorf(
				"mcp: reconciliation params: %s carries leading/trailing whitespace (ambiguous — refused)", field.key)
		}
		*field.dst = m.val.str
	}
	key, kerr := extractOperationKey(&v)
	if kerr != nil {
		return canonicalReconcileParams{}, kerr
	}
	out.OperationKey = key
	out.Forward = encodeCanonical(v)
	stripTraceMembers(&v)
	out.Effect = encodeCanonical(v)
	return out, nil
}

// isTaskReconcileMethod reports whether the method belongs to the operator
// reconciliation family.
func isTaskReconcileMethod(method string) bool {
	switch method {
	case methodTasksReconcileList, methodTasksReconcileStatus,
		methodTasksReconcileClear, methodTasksReconcileRetire:
		return true
	default:
		return false
	}
}

// Stable row CLASSES of the inventory (round-5 R5-03). They are a closed
// vocabulary derived from record state — never operator- or upstream-supplied
// text — so a console can group and prioritize rows without parsing prose.
const (
	taskReconcileRowCollision         = "parked-collision"
	taskReconcileRowReconciling       = "reconciliation-artefact"
	taskReconcileRowQuarantined       = "quarantined-orphan"
	taskReconcileRowCancelUnconfirmed = "cancellation-unconfirmed"
	taskReconcileRowTerminalConfirmed = "terminal-confirmed"
	// taskReconcileRowTerminalUncollected replaces round-6's `terminal-retired`
	// (round-7). Both name a proven-terminal record whose OWNER has not yet received
	// its final tool result — but round-6 reached that state by RETIRING the row
	// into a bounded handoff cache, and round-7 refuses the retirement instead. The
	// row is therefore an ordinary retained row: it counts against the bound, it is
	// listed, and it IS actionable, because a human looking at the inventory needs to
	// know precisely why `retire` will refuse it.
	taskReconcileRowTerminalUncollected = "terminal-awaiting-owner"
	taskReconcileRowPending             = "registration-pending"
	taskReconcileRowLive                = "live"
)

// ROUND-8 R8-01, on what the CLASS alone cannot say: the classes are ordered, so a
// row that is both quarantined and pinned on an uncollected delivery reports
// `quarantined-orphan` — the state an operator must act on first — and not
// `terminal-awaiting-owner`. The delivery obligation is therefore carried by two
// per-row FIELDS that are independent of the class and true for every row:
// `handleRelayed` (the owner MAY hold this task's identifier — see the field doc for
// the exact, weaker fact it records) and `ownerCollected` (the owner has been served
// the currently authoritative terminal report). Together with `retirable` they explain
// every refusal this surface can produce, on any row, without the operator having to
// attempt the action to find out.

// taskReconcileRowClass classifies one row. Order matters: a reconciliation
// artifact is also quarantined, and a cancellation-unconfirmed record may also
// carry an inferred terminal status.
func taskReconcileRowClass(rec TaskRecord, collision bool) string {
	switch {
	case collision:
		return taskReconcileRowCollision
	case rec.Reconciling:
		return taskReconcileRowReconciling
	case rec.Quarantined:
		return taskReconcileRowQuarantined
	case taskCancellationUnconfirmed(rec):
		return taskReconcileRowCancelUnconfirmed
	case rec.retireReady():
		return taskReconcileRowTerminalConfirmed
	case rec.retirable():
		// Proven terminal, but its owner has not collected the result yet, so
		// `retire` refuses (round-7). Distinguishing this from `terminal-confirmed`
		// is what lets the inventory say WHY, instead of an operator meeting a 409.
		return taskReconcileRowTerminalUncollected
	case rec.Pending:
		return taskReconcileRowPending
	default:
		return taskReconcileRowLive
	}
}

// taskReconcileRowActionable reports whether the row REQUIRES OPERATOR ATTENTION.
//
// ROUND-6 R6-07 fixes a contradiction: this function marked `parked-collision`
// actionable while the `Collision` field doc said collision rows are
// "deliberately NOT actionable through this surface". Both statements were about
// different things, and a console automating on the flag could not tell which. The
// ONE meaning, chosen and documented here:
//
//	`actionable` = "a human must look at this row".
//
// It does NOT mean "this surface's mutating actions can resolve it". A
// parked-collision needs attention — the upstream re-issued an identifier that
// already names a governed task, which is a protocol fault worth alerting on — and
// it is deliberately NOT resolvable here: it is not even in the live record map,
// so `clear` and `retire` cannot address it, and canceling the identifier would
// cancel the OTHER, already governed task. That distinction lives on the
// `Collision` field, which says exactly that.
//
// Not actionable: a `live` record (ordinary work in progress; it consumes the
// bound, so it is listed, but nothing needs doing) and a `registration-pending`
// one (mid-flight in its own settlement).
//
// ROUND-7 removes round-6's third exemption. `terminal-retired` was called
// non-actionable because the operator had already retired it; with proof-of-
// delivery retirement that state does not exist, and its successor
// `terminal-awaiting-owner` IS actionable — a human must know that the row is
// pinned on a delivery that has not happened.
//
// ROUND-7 R7-04, stated here because this is the flag operators automate on: a
// page with no actionable rows means NO HUMAN RECONCILIATION BACKLOG IN THIS
// SNAPSHOT. It does NOT mean the ledger is empty and it is NOT a restart-safety
// signal — `live` and `registration-pending` rows are non-actionable, and only
// the durable projection of a completed registration can be reconstructed. See
// the process-drain prerequisite in this file's header comment and in the
// `reconcileCursor` contract.
func taskReconcileRowActionable(class string) bool {
	switch class {
	case taskReconcileRowLive, taskReconcileRowPending:
		return false
	default:
		return true
	}
}

// taskReconcileView is the MINIMAL-DATA projection of one retained record. It
// carries identifiers, governance state and stable classes only: the canonical
// owner appears as its stable DIGEST, never as raw issuer/subject/act-as/client
// values, so an operator console can address a record precisely without the
// surface becoming an identity-disclosure channel (docs/SECURITY-HARDENING.md).
//
// ROUND-5 R5-04: the free-text `statusReason` is GONE. It is the one field of
// this projection whose content came from the UPSTREAM (`statusMessage`, or a
// failed task's `error.message`), so it turned a tenant-wide operational scope
// into a channel for another owner's task content. Its governance value is
// carried by `status` (a closed enum) and `class` (a closed vocabulary); the
// remaining reason fields are the GATEWAY's own stable text about its own
// decisions.
type taskReconcileView struct {
	TaskID      string `json:"taskId"`
	Generation  string `json:"generation"`
	OwnerDigest string `json:"ownerDigest"`
	Tool        string `json:"tool"`
	// Class is the stable row class; Actionable marks the rows a HUMAN must look
	// at — not the rows this surface's mutating actions can resolve (round-6
	// R6-07; see taskReconcileRowActionable for the one meaning and why).
	Class      string `json:"class"`
	Actionable bool   `json:"actionable"`
	// OwnerCollected reports that the OWNER was successfully served the CURRENTLY
	// authoritative terminal report of this task through this gateway (round-7,
	// digest-bound in round-8 R8-02 — a later, different terminal report makes it
	// false again). It is the second half of the retirement precondition, surfaced so
	// an operator can see at a glance why a proven-terminal row is not yet retirable:
	// `retire` refuses while this is false on a row whose handle was relayed, because
	// destroying an uncollected result is the R6-02 defect and evicting one is the
	// R7-02 defect.
	OwnerCollected bool `json:"ownerCollected"`
	// HandleRelayed is the IMMUTABLE provenance the retirement rule actually consults
	// (round-8 R8-01): this gateway must assume the owner MAY hold this task's
	// identifier. A row with `handleRelayed:false` has no owner delivery to protect and
	// retires on the operator's proof alone; a row with `true` and
	// `ownerCollected:false` is pinned on a collection that has not happened. Surfacing
	// it is what makes a refused `retire` explainable from the inventory instead of
	// from a 409.
	//
	// ROUND-11 N11-01, because an operator reads this before authorizing a deletion:
	// `true` does NOT assert remote receipt, and `false` does NOT merely mean "the
	// response write returned an error". `true` is recorded when the local writer
	// accepted the whole body, when it failed AFTER accepting body bytes (or without a
	// usable count), or when the relay unwound abnormally; `false` is recorded only
	// when the relay never ran or the writer PROVABLY accepted zero bytes. See
	// `TaskRecord.HandleRelayed`.
	//
	// It is a BOOLEAN about this gateway's own action, not identity or content —
	// the minimal-data rule of this projection is unchanged.
	HandleRelayed       bool   `json:"handleRelayed"`
	RequiredScope       string `json:"requiredScope,omitempty"`
	Status              string `json:"status"`
	CreatedAt           string `json:"createdAt"`
	Pending             bool   `json:"pending"`
	Quarantined         bool   `json:"quarantined"`
	QuarantineReason    string `json:"quarantineReason,omitempty"`
	Reconciling         bool   `json:"reconciling"`
	TerminalUnconfirmed bool   `json:"terminalUnconfirmed"`
	CancelUnconfirmed   bool   `json:"cancelUnconfirmed"`
	CancelBar           string `json:"cancelBar,omitempty"`
	CancelBarReason     string `json:"cancelBarReason,omitempty"`
	CancelInFlight      bool   `json:"cancelInFlight"`
	// Collision marks a PARKED ambiguous duplicate handle: an upstream task the
	// gateway refuses to govern AND refuses to cancel, because the identifier it
	// would cancel names a different, already governed task.
	//
	// ROUND-6 R6-07: it IS actionable in the one sense this surface uses that word
	// (a human must look at it — an upstream re-issuing a governed identifier is a
	// protocol fault worth alerting on) and it is NOT RESOLVABLE by any action
	// here: a parked collision is not in the live record map at all, so `clear` and
	// `retire` cannot address it, and canceling the identifier would cancel the
	// other, already governed task. Resolution is an upstream/operational matter.
	Collision bool `json:"collision,omitempty"`
	// Retirable reports that tasks/reconcile/retire will accept this row: BOTH the
	// terminal status was CONFIRMED by an authoritative upstream report AND the
	// owner has nothing left to collect (round-7 `retireReady`). It never promises
	// an action the surface would refuse.
	Retirable bool `json:"retirable"`
}

// reconcileView projects one record (identifiers/classes only).
func (rs *ResourceServer) reconcileView(rec TaskRecord, collision bool) taskReconcileView {
	bar, barReason, inFlight := rs.taskLedger.cancelIntentState(rec.Generation)
	class := taskReconcileRowClass(rec, collision)
	return taskReconcileView{
		TaskID: rec.TaskID, Generation: rec.Generation, OwnerDigest: rec.owner().digest(),
		Tool: rec.Tool, RequiredScope: rec.RequiredScope,
		Class: class, Actionable: taskReconcileRowActionable(class),
		OwnerCollected: rec.ownerCollectedCurrentTerminalReport(),
		HandleRelayed:  rec.HandleRelayed,
		Status:         rec.Status,
		CreatedAt:      rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Pending:        rec.Pending,
		Quarantined:    rec.Quarantined, QuarantineReason: rec.QuarantineReason,
		Reconciling:         rec.Reconciling,
		TerminalUnconfirmed: rec.TerminalUnconfirmed,
		CancelUnconfirmed:   rec.CancelUnconfirmed,
		CancelBar:           string(bar), CancelBarReason: barReason, CancelInFlight: inFlight,
		Collision: collision,
		Retirable: rec.retireReady(),
	}
}

// handleTaskReconcile is the entry point of the reconciliation family: scope gate
// (deny-closed) → strict canonicalization → per-action handler.
func (rs *ResourceServer) handleTaskReconcile(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	trace := requestTraceParent(r, req.Params)
	// PERMISSION CONSTANT, deny-closed and FIRST: a token that does not carry the
	// dedicated reconciliation scope gets the SEP-835 step-up challenge and learns
	// nothing about the ledger — not even whether a task id exists.
	if !tok.hasScope(scopeTasksReconcile) {
		rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, "", false,
			"insufficient scope for the task reconciliation surface", "", "MCP02", trace)
		rs.challengeScope(w, req.ID, scopeTasksReconcile)
		return
	}
	canon, cerr := canonicalizeReconcileParams(req.Params)
	if cerr != nil {
		rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, "", false,
			req.Method+" params refused by strict canonicalization (dup/case-alias/malformed keys)", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"malformed reconciliation params (strict decoding refused)")
		return
	}
	if req.Method == methodTasksReconcileList {
		rs.handleTaskReconcileList(ctx, w, req, tok, canon, trace)
		return
	}
	// Every MUTATING action names its target exactly: the task id, the record
	// GENERATION and the canonical OWNER digest. None of the three may be inferred.
	rec, ok := rs.resolveReconcileTarget(ctx, w, req, tok, canon, trace)
	if !ok {
		return
	}
	switch req.Method {
	case methodTasksReconcileStatus:
		rs.handleTaskReconcileStatus(ctx, w, r, req, tok, rec, canon, trace)
	case methodTasksReconcileClear:
		rs.handleTaskReconcileClear(ctx, w, req, tok, rec, canon, trace)
	case methodTasksReconcileRetire:
		rs.handleTaskReconcileRetire(ctx, w, req, tok, rec, canon, trace)
	default:
		rs.writeRPCError(w, http.StatusNotFound, req.ID, -32601, "unknown reconciliation method")
	}
}

// resolveReconcileTarget resolves and VERIFIES the generation- and owner-bound
// target of one mutating reconciliation action. Every refusal is deny-closed and
// answers with the same opaque message, so the surface never becomes a probe for
// which task identifiers, generations or owners exist.
func (rs *ResourceServer) resolveReconcileTarget(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, canon canonicalReconcileParams, trace string) (TaskRecord, bool) {
	refuse := func(reason string) (TaskRecord, bool) {
		rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, canon.TaskID, false,
			"reconciliation target refused: "+reason, "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"reconciliation target not found (taskId, generation and ownerDigest must name a live record)")
		return TaskRecord{}, false
	}
	if canon.TaskID == "" || canon.Generation == "" || canon.OwnerDigest == "" {
		rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, canon.TaskID, false,
			req.Method+" requires params.taskId, params.generation and params.ownerDigest", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			req.Method+" requires params.taskId, params.generation and params.ownerDigest")
		return TaskRecord{}, false
	}
	rec, ok := rs.taskLedger.lookup(canon.TaskID)
	if !ok {
		return refuse("no live record holds this identifier")
	}
	// GENERATION binding: the immutable identity of the registration INSTANCE. A
	// reconciliation scheduled against a retired generation must never act on the
	// replacement that reused the textual identifier (the round-3 R3-07 class).
	if rec.Generation != canon.Generation {
		return refuse("the named generation no longer holds this identifier")
	}
	// OWNER binding: the generation already pins the owner, so this is a
	// confirmation that the operator is acting on the record it believes it is —
	// a stale console page can never mis-target a different principal's task.
	if rec.owner().digest() != canon.OwnerDigest {
		return refuse("the asserted owner digest does not match the record")
	}
	return rec, true
}

// handleTaskReconcileList answers the reconciliation INVENTORY: EVERY record the
// ledger holds — quarantined orphans, reconciliation artifacts, every
// cancellation-unconfirmed record (round-4 R4-04), every proven-terminal record
// that still consumes the bound (round-5 R5-03) and the ordinary live ones —
// plus the parked ambiguous duplicates, in a stable total order, PAGED by an
// opaque cursor.
//
// The completeness rule is deliberately the one that cannot drift from the
// bound: if a record counts toward the retention cap, it appears here. Round-4
// listed a strict subset, so two proven-terminal records could saturate a cap of
// two while the inventory reported an empty backlog and every further tools/call
// was answered 429 — a legitimate owner permanently denied with nothing visible
// to drain. The per-row `class`/`actionable` fields carry what the subset used to
// convey, without hiding rows.
//
// It is a READ of local state: no external effect, so it carries the best-effort
// decision audit rather than a claim (evidence is mandatory on an ALLOWED EFFECT,
// never on a read).
func (rs *ResourceServer) handleTaskReconcileList(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, canon canonicalReconcileParams, trace string) {
	var after reconcileCursor
	if canon.Cursor != "" {
		cur, cerr := rs.parseReconcileCursor(canon.Cursor)
		if cerr != nil {
			rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, "", false,
				"reconciliation inventory cursor refused: "+cerr.Error(), "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams, cerr.Error())
			return
		}
		after = cur
	}
	// ONE read of the inventory AND of the ledger's insertion high-watermark. A
	// continuation stays pinned to the watermark its traversal started from; a fresh
	// traversal takes the current one (round-6 R6-03).
	records, collisions, watermark := rs.taskLedger.reconciliationSnapshot()
	snapshot := watermark
	if canon.Cursor != "" {
		snapshot = after.Snapshot
	}
	views := make([]taskReconcileView, 0, len(records)+len(collisions))
	newer := 0
	for _, rec := range records {
		if rec.Seq > snapshot {
			newer++
			continue
		}
		views = append(views, rs.reconcileView(rec, false))
	}
	for _, rec := range collisions {
		if rec.Seq > snapshot {
			newer++
			continue
		}
		views = append(views, rs.reconcileView(rec, true))
	}
	// The STABLE TOTAL ORDER the cursor walks: (taskId, generation) is unique —
	// one live record per identifier, and a generation is never reused. Combined
	// with the snapshot filter above, this is what the traversal guarantees: within
	// ONE cursor chain no row that existed at the watermark and still exists is
	// skipped or repeated. Rows that arrive LATER are excluded by construction and
	// counted in `newerThanSnapshot` — see the reconcileCursor doc for the exact
	// contract, including why end-of-cursor is not "the ledger is drained".
	sort.Slice(views, func(i, j int) bool {
		if views[i].TaskID != views[j].TaskID {
			return views[i].TaskID < views[j].TaskID
		}
		return views[i].Generation < views[j].Generation
	})
	total := len(views)
	if canon.Cursor != "" {
		start := sort.Search(total, func(i int) bool {
			if views[i].TaskID != after.TaskID {
				return views[i].TaskID > after.TaskID
			}
			return views[i].Generation > after.Generation
		})
		views = views[start:]
	}
	nextCursor := ""
	if len(views) > maxReconcileListRecords {
		views = views[:maxReconcileListRecords]
		last := views[len(views)-1]
		issued, ierr := rs.issueReconcileCursor(reconcileCursor{
			Instance: rs.instanceID, Snapshot: snapshot,
			TaskID: last.TaskID, Generation: last.Generation,
		})
		if ierr != nil {
			// Deny-closed: a page that clips rows without handing back a way to reach
			// them IS the undiscoverable-row failure this contract exists to remove.
			// Refuse the read rather than answer a silently truncated inventory.
			rs.writeRPCError(w, http.StatusInternalServerError, req.ID, rpcUpstreamError,
				"reconciliation inventory could not issue a continuation cursor")
			return
		}
		nextCursor = issued
	}
	sat := rs.taskLedger.admissionState(taskOwnerFromToken(rs.tenant, tok))
	body, err := json.Marshal(struct {
		// Instance identifies the gateway process whose cache snapshot this page
		// came from. Registered rows are durable, but the pagination watermark and
		// process-only reconciliation facts are not shared between instances.
		Instance   string              `json:"instance"`
		Records    []taskReconcileView `json:"records"`
		NextCursor string              `json:"nextCursor,omitempty"`
		Truncated  bool                `json:"truncated"`
		Total      int                 `json:"total"`
		// NewerThanSnapshot counts rows this traversal will NOT return because they
		// were inserted after its snapshot watermark. It is the honest signal that a
		// further traversal from the head is required before a drain is complete
		// (round-6 R6-03) — end-of-cursor alone never means the ledger is empty.
		NewerThanSnapshot int `json:"newerThanSnapshot"`
		// Saturated is computed for the CALLING operator's own owner tuple, while
		// Retained/Cap are gateway-wide (round-6 R6-07: another owner may be denied by
		// its per-owner cap while this flag reads false).
		Saturated bool `json:"saturated"`
		Retained  int  `json:"retained"`
		Cap       int  `json:"retainedCap"`
	}{
		Instance: rs.instanceID, Records: views, NextCursor: nextCursor,
		Truncated: nextCursor != "", Total: total, NewerThanSnapshot: newer,
		Saturated: sat.saturated(), Retained: sat.TotalRetained, Cap: sat.TotalCap,
	})
	if err != nil {
		rs.writeRPCError(w, http.StatusInternalServerError, req.ID, rpcUpstreamError,
			"reconciliation inventory could not be rendered")
		return
	}
	rs.auditTaskTraced(ctx, tok, req.Method, scopeTasksReconcile, "", true,
		fmt.Sprintf("reconciliation inventory listed (%d of %d records in this snapshot, more=%t, arrived-after-snapshot=%d)",
			len(views), total, nextCursor != "", newer),
		"", "MCP07", trace)
	_ = rs.writeResult(w, req.Method, req.ID, body)
}

// reconcileBinding derives the evidence binding of one mutating reconciliation
// action.
func (rs *ResourceServer) reconcileBinding(req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalReconcileParams, class string) (sdk.EvidenceBinding, string, string, error) {
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		return sdk.EvidenceBinding{}, "", "", derr
	}
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveTaskReconcileEffectDigest(
			rs.tenant, rs.resource, req.Method, tok,
			rec.TaskID, rec.Generation, rec.owner().digest(), rs.upstreamDescriptor, class,
			sortedScopeSet(tok.Scopes), canon.canonicalTaskParams)),
	}, opID, idKind, nil
}

// reconcileDecision builds the allow decision of one reconciliation action.
func (rs *ResourceServer) reconcileDecision(tok validatedToken, rec TaskRecord, reason, trace, idKind, action string) ToolDecision {
	return ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: rec.Tool, RequiredScope: scopeTasksReconcile,
		Allowed: true, Reason: reason, TaskID: rec.TaskID,
		MCPTag: "MCP07", TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, EffectAction: action, At: rs.clock(),
	}
}

// handleTaskReconcileStatus performs the AUTHORITATIVE upstream `tasks/get` of
// one retained record and applies its strictly validated status through the one
// confirmation path (confirmStatus). It is the action that RESOLVES a retained
// record: only an upstream report may make a status terminal, and only a
// confirmed terminal status makes the record retirable.
//
// It is a real external effect, so it runs the full stage-4 lifecycle: claim +
// anchor → MayEmit → leadership fence → forward with the operation identity →
// honest settlement → response. A refused claim never reaches the upstream.
func (rs *ResourceServer) handleTaskReconcileStatus(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalReconcileParams, trace string) {
	binding, opID, idKind, derr := rs.reconcileBinding(req, tok, rec, canon, taskReconcileClassStatus)
	if derr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	dec := rs.reconcileDecision(tok, rec,
		"operator reconciliation: authoritative tasks/get", trace, idKind, taskActionReconcileStatusPrefix+idKind)
	gateRec := rs.auditor.Record(ctx, dec, binding)
	if !gateRec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, gateRec, opID)
		return
	}
	if fence := rs.auditor.BeforeEffect(ctx, gateRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}
	// The generation PIN holds the identifier across the external call exactly as
	// it does for a client method: the reconciliation read carries the textual task
	// id on the wire and is equally exposed to an evict-and-replace race. The
	// `reconcile` kind is widened — the whole point is to read records no client
	// method may touch.
	if verr := rs.taskLedger.acquireEffectLease(rec.TaskID, rec.Generation, taskEffectReconcile); verr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"reconciliation target revalidation refused immediately before the effect: "+verr.Error(), "", "MCP07", trace)
		rs.auditor.Settle(ctx, GateOutcome{Record: gateRec, State: DispatchBlocked})
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"reconciliation target not found (the named record changed)")
		return
	}
	// The pin ends with the DISPATCH, exactly as it does for the client tasks/get
	// (handleTaskGet releases it inside enforceTaskDispatch before synchronizing
	// the status). It must not still be held when the authoritative confirmation
	// runs: a confirmed-terminal reconciliation artifact is RELEASED by
	// syncTaskStatusFromResult, and `release` refuses a pinned generation — the
	// record would silently survive its own proof of termination.
	leased := true
	unpin := func() {
		if leased {
			leased = false
			rs.taskLedger.releaseEffectLease(rec.Generation)
		}
	}
	defer unpin()

	// ROUND-5 R5-05: a CONFORMING Tasks-extension request, built through the
	// connector's ONE Tasks request path (taskRequestParams). Round-4 hand-rolled
	// `{"taskId":...}`, which omits the per-request client-capability declaration
	// the extension requires — a server MUST NOT honor an extension the request did
	// not declare (-32021), so a strict upstream refused the read and the record
	// became undrainable. The revision is the OPERATOR-DECLARED upstream revision
	// (round-6 R6-06: configuration, defaulting to the connector baseline when
	// unset — not a negotiation); an upstream declared as a revision whose Tasks
	// extension this connector does not implement is refused here rather than sent a
	// fabricated request.
	params, perr := rs.tasksGetParams(rec.TaskID)
	if perr != nil {
		// Nothing touched the transport: settle not_sent honestly and refuse. The
		// record is untouched, so it stays visible to the operator and to sweeps.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"reconciliation read not synthesized: "+perr.Error(), "", "MCP07", trace)
		rs.auditor.Settle(ctx, GateOutcome{Record: gateRec, State: DispatchNotSent})
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"the authoritative task read could not be synthesized for the configured upstream revision")
		return
	}
	ureq := rs.upstreamReq(r, methodTasksGet, params, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = gateRec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)

	state, relay := classifyDispatch(res, ferr)
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: gateRec, State: state,
		ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
	})
	if settlement.FailureClass != sdk.FailureNone {
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	if !relay {
		if state == DispatchUnknown {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		} else {
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream task fetch failed")
		}
		return
	}
	unpin()
	// The ONE authoritative confirmation path, gated by the COMPLETE GetTaskResult
	// validation (round-5 R5-01). A body that is not a conforming task report is an
	// upstream protocol FAULT, never proof: nothing is confirmed, the record is
	// RETAINED, and the operator is told the upstream is faulty rather than handed a
	// record that now looks provably finished. Round-4 accepted `{"status":
	// "completed"}` here — enough to clear the cancellation ambiguity and let the
	// next `retire` DELETE a live record.
	// ROUND-8: the report's digest is DELIBERATELY not used here. This read is the
	// OPERATOR's, not the owner's, so it may confirm the authoritative answer but can
	// never satisfy the owner's collection — and installing the new digest is exactly
	// what invalidates a proof bound to a previous, different terminal report (R8-02).
	updated, applied, _, serr := rs.syncTaskStatusFromResult(rec, res.Result)
	if serr != nil {
		// ROUND-6 R6-05: the PERSISTED reason carries the stable validation CLASS, not
		// `serr.Error()`. The strict decoder quotes upstream-controlled PROPERTY NAMES
		// ("duplicate object key …", "case-variant alias …"), so raw parser text in the
		// audit trail is a content channel straight around the R5-04 projection.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"upstream tasks/get result is not a conforming SEP-2663 GetTaskResult; nothing confirmed, record RETAINED; validation class: "+
				taskDefectClass(serr), "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream task report is not a conforming GetTaskResult; nothing was confirmed and the record is retained")
		return
	}
	view := rs.reconcileView(rec, false)
	if applied {
		view = rs.reconcileView(updated, false)
	}
	// ROUND-5 R5-04: the GOVERNANCE PROJECTION only. Round-4 embedded the entire
	// raw upstream body as `upstreamResult` — which SEP-2663 says carries the
	// original tool result on a completed task, the execution error on a failed one
	// and the outstanding input requests on an input_required one. A tenant-wide
	// operational scope therefore read arbitrary cross-owner task CONTENT (the
	// reviewer's probe saw a tool output verbatim). Generation and owner digest are
	// a mistarget defense, not an authorization to read a task's content.
	//
	// What reconciliation actually needs is here: the confirmed status, the stable
	// class, the task/generation/owner bindings — plus the DIGEST of the upstream
	// body, so an operator can correlate this read with the evidence journal entry
	// of the same dispatch. There is deliberately NO raw-content path: adding one
	// would need its own explicit content permission and audit contract (documented
	// as a proposal in the session file, not built here).
	//
	// ROUND-6 R6-05, stated without euphemism: `upstreamResultDigest` is an UNKEYED
	// SHA-256 of the raw response bytes. That is a sensitive CORRELATION
	// FINGERPRINT, not anonymization — for a predictable or low-entropy body it is
	// an offline equality/dictionary oracle, so whoever holds these digests can test
	// guesses about task content. It is kept unkeyed DELIBERATELY: its entire
	// purpose is to match the `ResultDigest` the settlement wrote to the evidence
	// journal for this same dispatch, and a keyed or per-tenant digest would be a
	// different value that correlates with nothing. A safer design does not keep the
	// digest and key it; it replaces it with an evidence REFERENCE (the settlement's
	// own identity), which is a separate contract change, not a local tweak here.
	body, merr := json.Marshal(struct {
		Applied      bool              `json:"statusApplied"`
		Record       taskReconcileView `json:"record"`
		ResultDigest string            `json:"upstreamResultDigest,omitempty"`
	}{Applied: applied, Record: view, ResultDigest: resultDigest(res.Result)})
	if merr != nil {
		rs.writeRPCError(w, http.StatusInternalServerError, req.ID, rpcUpstreamError,
			"reconciliation status could not be rendered")
		return
	}
	_ = rs.writeResult(w, req.Method, req.ID, body)
}

// tasksGetParams synthesizes the params of the reconciliation `tasks/get` for the
// CONFIGURED upstream revision, through the connector's single Tasks-request path
// (round-5 R5-05).
//
// The Tasks extension (SEP-2663) exists in the 2026-07-28 revision; the connector
// implements no other Tasks wire shape (`Client.taskCall` likewise refuses any
// non-stateless mode). An upstream configured as an older revision is therefore
// REFUSED here — deny-closed — instead of being sent a guessed legacy request
// whose answer could not be trusted as proof anyway.
//
// ROUND-6 R6-06: "configured" is literal. An empty `UpstreamRevision` ASSUMES the
// connector's baseline; nothing here discovers what the upstream actually speaks.
// The failure direction is safe (a mismatch fails the read and retains the
// record), but the assumption is the operator's to get right. Rebuilding the RS
// rehydrates registered rows when DurableTaskStore is wired, while discarding
// process-only cursor and attempt state.
func (rs *ResourceServer) tasksGetParams(taskID string) ([]byte, error) {
	if rs.upstreamRevision != revision20260728 {
		return nil, fmt.Errorf(
			"the configured upstream revision %q does not carry the %s Tasks extension; the authoritative read is refused rather than guessed",
			rs.upstreamRevision, revision20260728)
	}
	obj, err := taskRequestParams(tasksRequestMeta(rs.upstreamRevision), struct {
		TaskID string `json:"taskId"`
	}{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

// handleTaskReconcileClear lifts the per-generation cancellation BAR so automatic
// cancellation of that generation is re-armed. It is the documented exit from an
// ambiguous cancellation attempt (a client cancel that ended in an error, a sweep
// whose outcome never recorded, an attempt that unwound abnormally).
//
// It mutates governance state, so it is evidence-bound: claim + anchor →
// leadership fence → mutation → settlement. A refused claim or fence NEVER
// mutates, and a refused ledger compare-and-swap settles `blocked` (an honest,
// durable "stopped before the effect").
func (rs *ResourceServer) handleTaskReconcileClear(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalReconcileParams, trace string) {
	rs.applyReconcileMutation(ctx, w, req, tok, rec, canon, trace, taskReconcileClassClear,
		taskActionReconcileClearPrefix, "operator reconciliation: clear the cancellation bar",
		func() (bool, string) {
			if rs.taskLedger.clearCancelBar(rec.TaskID, rec.Generation) {
				return true, ""
			}
			return false, "the cancellation bar could not be cleared (the generation is retired, replaced, or an attempt is in flight)"
		})
}

// handleTaskReconcileRetire retires a record whose TERMINAL status was CONFIRMED
// by an authoritative upstream report. It is the ONLY way a retained record leaves
// the retention bound by operator action, and it refuses anything that is not
// provably finished: an operator may not assert terminality, only act on a
// confirmation the gateway itself read (run tasks/reconcile/status first).
//
// ROUND-7: retirement is a deletion again, gated on PROOF OF DELIVERY. It refuses
// a client-readable record whose owner has not yet been served its terminal result
// — the record simply stays retained, counted and actionable
// (`terminal-awaiting-owner`) until the owner's own `tasks/get` collects it.
// Round-6 instead retired first and kept a bounded handoff cache, which could
// evict an unread result under FIFO pressure (R7-02). `taskLedger.retireReconciled`
// states both preconditions and why the second one scopes only to records whose
// owner ever had a read to lose.
func (rs *ResourceServer) handleTaskReconcileRetire(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalReconcileParams, trace string) {
	rs.applyReconcileMutation(ctx, w, req, tok, rec, canon, trace, taskReconcileClassRetire,
		taskActionReconcileRetirePrefix, "operator reconciliation: retire a confirmed-terminal record",
		func() (bool, string) { return rs.taskLedger.retireReconciled(rec.TaskID, rec.Generation) })
}

// applyReconcileMutation runs the evidence lifecycle of one LOCAL reconciliation
// mutation. `apply` performs the compare-and-swap and is invoked ONLY after a
// fresh claim has anchored and the leadership fence has passed — the same order
// every other stage-4 effect uses, so a reconciliation action can never mutate
// the governance view without a durable, single-use record of who did it.
func (rs *ResourceServer) applyReconcileMutation(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalReconcileParams, trace, class, actionPrefix, reason string, apply func() (bool, string)) {
	binding, opID, idKind, derr := rs.reconcileBinding(req, tok, rec, canon, class)
	if derr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	dec := rs.reconcileDecision(tok, rec, reason, trace, idKind, actionPrefix+idKind)
	gateRec := rs.auditor.Record(ctx, dec, binding)
	if !gateRec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, gateRec, opID)
		return
	}
	if fence := rs.auditor.BeforeEffect(ctx, gateRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}
	ok, why := apply()
	if !ok {
		// Nothing was mutated: settle `blocked` — an honest, durable record that
		// this claim stopped before its effect — and refuse.
		rs.auditor.Settle(ctx, GateOutcome{Record: gateRec, State: DispatchBlocked})
		rs.auditTaskRecordTraced(ctx, tok, rec, false, "reconciliation refused: "+why, "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusConflict, req.ID, rpcAccessDenied, "reconciliation refused: "+why)
		return
	}
	body, merr := json.Marshal(struct {
		Applied    bool   `json:"applied"`
		TaskID     string `json:"taskId"`
		Generation string `json:"generation"`
	}{Applied: true, TaskID: rec.TaskID, Generation: rec.Generation})
	if merr != nil {
		body = json.RawMessage(`{"applied":true}`)
	}
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: gateRec, State: DispatchCompleted, ResultDigest: resultDigest(body),
	})
	if settlement.FailureClass != sdk.FailureNone {
		// The mutation happened but its outcome did NOT durably record: withhold the
		// response. The operation stays claimed/ambiguous (status replay only, never
		// a re-application), and the mutation itself is idempotent — clearing an
		// already-cleared bar and retiring an already-retired record both refuse.
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	_ = rs.writeResult(w, req.Method, req.ID, body)
}
