// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// tasks.go — client support for the io.modelcontextprotocol/tasks extension
// (SEP-2663). In the 2026-07-28 RC, Tasks moved out of the core protocol
// into an extension: a server may answer tools/call with an asynchronous task
// handle (resultType "task") instead of a final result; the client then POLLS
// tasks/get until a terminal status and may answer MRTR input via tasks/update.
//
// tasks/list was REMOVED by the redesign ("can't be scoped safely without
// sessions") and is deliberately not implemented here — the only discovery of a
// task is the handle the server returned. Every request to a tasks/* method
// MUST declare the extension in its per-request capabilities (a server answers
// -32021 otherwise), which the helpers below do.

// Task status values (SEP-2663). completed/failed/canceled are terminal;
// "completed" INCLUDES a tool result carrying isError:true (failed is reserved
// for JSON-RPC execution errors).
const (
	taskStatusWorking       = "working"
	taskStatusInputRequired = "input_required"
	taskStatusCompleted     = "completed"
	taskStatusFailed        = "failed"
	taskStatusCanceled      = "cancelled"
)

const (
	methodTasksGet    = "tasks/get"
	methodTasksUpdate = "tasks/update"
	methodTasksCancel = "tasks/cancel"
)

// taskResultDefect is ONE strict-validation failure of an upstream task body,
// carrying a STABLE CLASS separately from its human-readable detail.
//
// ROUND-6 R6-05 (a real leak path, not a nicety): the detail of a strict
// validation failure quotes UPSTREAM-CONTROLLED text — `duplicate object key
// "…"`, `case-variant alias "…"`, `taskId "…"` — and the audit sites used to
// concatenate the whole error into the PERSISTED reason. A secret encoded as a
// JSON PROPERTY NAME therefore walked straight past the response projection
// (round-5 R5-04) into the durable audit trail, which is exactly the channel that
// projection exists to close. The class below is a closed vocabulary the GATEWAY
// chooses; only the class is persisted, and the detail never leaves the process.
type taskResultDefect struct {
	class  string
	detail string
}

func (d *taskResultDefect) Error() string { return d.detail }

// taskDefect builds a classified validation failure.
func taskDefect(class, format string, args ...any) *taskResultDefect {
	return &taskResultDefect{class: class, detail: fmt.Sprintf(format, args...)}
}

// Stable validation classes. They are the ONLY thing an audit record may carry
// about a rejected upstream body — never the parser text (R6-05).
const (
	taskDefectUnclassified = "unclassified"

	// The authoritative `tasks/get` report (`GetTaskResult`).
	defectGetResultAbsent      = "get-task-result/absent"
	defectGetResultDecode      = "get-task-result/strict-decode"
	defectGetResultShape       = "get-task-result/not-an-object"
	defectGetResultAlias       = "get-task-result/reserved-alias"
	defectGetResultDiscrim     = "get-task-result/discriminator"
	defectGetResultTaskID      = "get-task-result/task-id"
	defectGetResultStatus      = "get-task-result/status"
	defectGetResultTimestamp   = "get-task-result/timestamp"
	defectGetResultTTL         = "get-task-result/ttl-ms"
	defectGetResultPoll        = "get-task-result/poll-interval-ms"
	defectGetResultStatusMsg   = "get-task-result/status-message"
	defectGetResultMeta        = "get-task-result/meta"
	defectGetResultPayload     = "get-task-result/status-payload"
	defectGetResultPayloadType = "get-task-result/status-payload-type"

	// The durable task HANDLE a tools/call may answer with (`CreateTaskResult`).
	defectHandleDecode     = "task-handle/strict-decode"
	defectHandleAlias      = "task-handle/reserved-alias"
	defectHandleResultType = "task-handle/result-type"
	defectHandleTaskID     = "task-handle/task-id"
	defectHandleStatus     = "task-handle/status"
	defectHandleStatusMsg  = "task-handle/status-message"
	defectHandleTTL        = "task-handle/ttl-ms"
	defectHandlePoll       = "task-handle/poll-interval-ms"

	// The `tasks/update` ACKNOWLEDGEMENT (`UpdateTaskResult = Result`).
	//
	// ROUND-7 R7-06: these were missing, so `strictTaskUpdateAck` returned bare
	// `fmt.Errorf` values, `taskDefectClass` called them `unclassified` and the
	// tasks/update handler concatenated the whole error into the PERSISTED audit
	// reason. An upstream property named `ROUND7-UPDATE-SECRET-PROPERTY` therefore
	// reached the durable audit trail verbatim — the same channel R6-05 closed on
	// the other two validators, one function over. Every upstream-body validator in
	// this package now carries a class, and `TestRound7EveryUpstreamBodyValidatorIsClassified`
	// ENUMERATES them from the source so the next one added cannot be silent.
	defectUpdateAckAbsent    = "update-task-result/absent"
	defectUpdateAckDecode    = "update-task-result/strict-decode"
	defectUpdateAckShape     = "update-task-result/not-an-object"
	defectUpdateAckAlias     = "update-task-result/reserved-alias"
	defectUpdateAckDiscrim   = "update-task-result/discriminator"
	defectUpdateAckTaskState = "update-task-result/task-state-member"
)

// taskDefectClass returns the stable class of a task-body validation failure, or
// `unclassified` for an error that did not come from the strict validators. It is
// what the audit trail persists — never `err.Error()` (R6-05).
func taskDefectClass(err error) string {
	if err == nil {
		return ""
	}
	var d *taskResultDefect
	if errors.As(err, &d) {
		return d.class
	}
	return taskDefectUnclassified
}

// Task is the task state the extension reports: the flat CreateTaskResult a
// tools/call returns (resultType "task") and the status-specific variants
// tasks/get returns. Result/TaskError/InputRequests are populated only on the
// matching status.
type Task struct {
	TaskID         string          `json:"taskId"`
	Status         string          `json:"status"`
	StatusMessage  string          `json:"statusMessage"`
	CreatedAt      string          `json:"createdAt"`
	LastUpdatedAt  string          `json:"lastUpdatedAt"`
	TTLMs          *int64          `json:"ttlMs"`
	PollIntervalMs *int64          `json:"pollIntervalMs"`
	Result         json.RawMessage `json:"result"` // completed: what the original request would have returned
	TaskError      *rpcError       `json:"error"`  // failed: the JSON-RPC execution error
	// MRTR fields of an input_required task (keys surfaced, payloads opaque).
	InputRequests map[string]json.RawMessage `json:"inputRequests"`
	RequestState  string                     `json:"requestState"`
}

// terminal reports whether the task reached a final status.
func (t Task) terminal() bool {
	switch t.Status {
	case taskStatusCompleted, taskStatusFailed, taskStatusCanceled:
		return true
	}
	return false
}

// taskResultReservedMembers are the task-result members the gateway reads by
// exact name. A case-variant alias of any of them is refused: encoding/json
// matches case-INSENSITIVELY and keeps the LAST duplicate, so a result carrying
// both `status:"working"` and `Status:"canceled"` would let the local record go
// terminal while an exact-casing client still sees a live task (round-1
// F-10).
var taskResultReservedMembers = []string{
	"resultType", "taskId", "status", "statusMessage", "createdAt", "lastUpdatedAt",
	"ttlMs", "pollIntervalMs", "result", "error", "inputRequests", "requestState",
}

// taskStatusValid reports whether s is one of the SEP-2663 task statuses.
func taskStatusValid(s string) bool {
	switch s {
	case taskStatusWorking, taskStatusInputRequired, taskStatusCompleted,
		taskStatusFailed, taskStatusCanceled:
		return true
	}
	return false
}

// strictTaskFromResult parses a tools/call result that is a durable task handle
// (resultType "task", the flat CreateTaskResult) into ONE immutable normalized
// record — the record the child evidence binding names, the ledger stores and
// the actuator targets, with no post-binding transformation anywhere (round-1 F-10).
//
// Three outcomes:
//
//   - (task, true, nil)  — a strictly valid task handle;
//   - (zero, false, nil) — the result is provably NOT a task handle (the
//     ordinary synchronous tools/call result; relayed unchanged);
//   - (zero, false, err) — the result LOOKS like a task handle (a permissive
//     reader would see one) but fails strict validation: duplicate keys, a
//     case-variant alias, a whitespace-ambiguous or empty id, an unknown status
//     or a malformed TTL. Such a handle can never be governed — the caller must
//     refuse it rather than relay a task identity it cannot bind.
func strictTaskFromResult(raw json.RawMessage) (Task, bool, error) {
	// Round-2 N-04: whether the result CARRIES A TASK MARKER is decided
	// ORDER-INDEPENDENTLY and case-insensitively, never by a first-wins struct
	// unmarshal. The round-1 probe decoded into a struct, so an upstream could
	// order `"ResultType":"task","TaskId":"task-hidden"` BEFORE exact-cased
	// `"resultType":"complete","taskId":""`: encoding/json matched the aliases
	// case-insensitively and the later exact members overwrote them, making
	// looksTask false — which discarded the strict tree's alias error and relayed
	// a live durable task that was never registered, never quarantined and
	// invisible to every sweep.
	looksTask := resultCarriesTaskMarker(raw)

	v, err := decodeStrictJSON(raw)
	if err != nil || v.kind != canonObject {
		if looksTask {
			return Task{}, false, taskDefect(defectHandleDecode,
				"mcp: task result failed strict decoding (ambiguous — refused)")
		}
		return Task{}, false, nil
	}
	// The strict tree is the second, authoritative marker source: a member that
	// case-folds to a task marker makes any reserved-alias/shape error FATAL even
	// if the permissive scan above somehow missed it.
	looksTask = looksTask || strictTreeCarriesTaskMarker(v)
	if aerr := rejectReservedKeyAliases(v, taskResultReservedMembers); aerr != nil {
		if looksTask {
			return Task{}, false, taskDefect(defectHandleAlias, "%s", aerr.Error())
		}
		return Task{}, false, nil
	}
	rt := v.member("resultType")
	isTask := rt != nil && rt.val.kind == canonString && rt.val.str == resultTypeTask
	if !isTask {
		if looksTask {
			// A permissive reader can see a task handle and the strict tree does
			// not: the two consumers disagree about what this result IS, so the
			// gateway refuses rather than relaying an ungovernable task identity.
			return Task{}, false, taskDefect(defectHandleResultType,
				"mcp: task result: resultType is ambiguous or not exact-cased")
		}
		return Task{}, false, nil
	}

	t, verr := validateTaskHandleMembers(v)
	if verr != nil {
		return Task{}, false, verr
	}
	return t, true, nil
}

// validateTaskHandleMembers validates the CreateTaskResult members of a result
// whose task-handle variant has already been SELECTED (exact
// resultType:"task"): the exact-cased identity, status, statusMessage and the
// millisecond hints. Shared by strictTaskFromResult and the context-led
// selectDeclaredTaskHandle so the two can never validate different sets.
func validateTaskHandleMembers(v canonValue) (Task, error) {
	var t Task
	id := v.member("taskId")
	if id == nil || id.val.kind != canonString {
		return Task{}, taskDefect(defectHandleTaskID, "mcp: task result: taskId missing or not a string")
	}
	if err := validateTaskID(id.val.str); err != nil {
		return Task{}, taskDefect(defectHandleTaskID, "mcp: task result: %s", err.Error())
	}
	t.TaskID = id.val.str
	if st := v.member("status"); st != nil {
		if st.val.kind != canonString || !taskStatusValid(st.val.str) {
			return Task{}, taskDefect(defectHandleStatus,
				"mcp: task result: status missing or not a known task status")
		}
		t.Status = st.val.str
	}
	if msg := v.member("statusMessage"); msg != nil {
		if msg.val.kind != canonString {
			return Task{}, taskDefect(defectHandleStatusMsg, "mcp: task result: statusMessage is not a string")
		}
		t.StatusMessage = msg.val.str
	}
	ttl, terr := strictOptionalPositiveInt(v, "ttlMs", defectHandleTTL)
	if terr != nil {
		return Task{}, terr
	}
	t.TTLMs = ttl
	poll, perr := strictOptionalPositiveInt(v, "pollIntervalMs", defectHandlePoll)
	if perr != nil {
		return Task{}, perr
	}
	t.PollIntervalMs = poll
	if entries, ok := strictObjectMembers(&v, "inputRequests"); ok {
		t.InputRequests = entries
	}
	return t, nil
}

// selectDeclaredTaskHandle is the CONTEXT-LED task-handle selection of the
// design adjudication (§1/§2/§6): it is called ONLY when the exact forwarded
// tools/call request DECLARED the Tasks extension in its per-request
// clientCapabilities (canonicalToolCallParams.DeclaresTasks — the pin makes
// capabilities per-request and forbids inference, schema.ts:63-98), and within
// that context the exact strict-tree discriminator `resultType:"task"` alone
// selects the CreateTaskResult contract. The permissive `looksTask` marker scan
// of strictTaskFromResult deliberately plays NO part here: body shape can
// validate a contract already selected, it cannot prove which open contract
// applies, and the adjudication forbids a permissive probe from selecting the
// profile or changing a relay decision.
//
// Three outcomes, mirroring strictTaskFromResult:
//
//   - (task, true, nil)  — exact resultType "task" and a strictly valid handle;
//   - (zero, false, nil) — an exact discriminator other than "task" (including
//     absent, "complete", "input_required" and custom open-enum values): the
//     result is NOT the task variant of the declared union and the core-result
//     contract applies to it. A decodable non-object result is likewise not a
//     task handle (no exact member exists to read);
//   - (zero, false, err) — the declared-union discriminator is UNREADABLE
//     (strict decode failure: duplicate keys at any depth, lossy strings) or
//     the SELECTED task variant fails strict validation (reserved-member
//     alias, malformed identity/status/TTL). In the declared-capability
//     context the gateway cannot relay a result whose contract it cannot
//     determine, and it never relays a task identity it cannot bind — the
//     existing refusals for malformed task identities are preserved
//     (adjudication §4).
func selectDeclaredTaskHandle(raw json.RawMessage) (Task, bool, error) {
	if strings.TrimSpace(string(raw)) == "" {
		// No result bytes at all: nothing to classify (and nothing a task
		// contract could bind). The caller relays its empty-result shape.
		return Task{}, false, nil
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		// Round-4 finding 3: the CLOSED task validator — whose strict decode
		// refuses duplicate keys and lossy strings — must be imposed ONLY on a
		// result that SELECTED the task variant. Reading the whole open result
		// strictly BEFORE checking the exact discriminator turned a duplicate
		// extension key on a `complete` result into a 502, a new unconditional
		// refusal of pin-valid traffic that 276d6d4e relayed. Read the exact
		// discriminator resiliently: exact resultType:"task" means the result DID
		// select the task contract, so an undecodable (ungovernable) task identity is
		// refused exactly as before; any OTHER result did not select the task variant
		// and is NOT refused here — it falls through to the core contract, whose own
		// classifier fails safe on its own governed payload (finding 1).
		if selectsTaskVariant(raw) {
			return Task{}, false, taskDefect(defectHandleDecode,
				"mcp: task result failed strict decoding (ambiguous — refused)")
		}
		return Task{}, false, nil
	}
	if v.kind != canonObject {
		return Task{}, false, nil
	}
	rt := v.member("resultType")
	if rt == nil || rt.val.kind != canonString || rt.val.str != resultTypeTask {
		return Task{}, false, nil
	}
	// The exact discriminator selected the task variant; strict validation of
	// the SELECTED contract applies (a case-variant alias of a reserved member
	// keeps being refused — validating an already selected closed shape never
	// promotes an extension member into a discriminator, adjudication §2).
	if aerr := rejectReservedKeyAliases(v, taskResultReservedMembers); aerr != nil {
		return Task{}, false, taskDefect(defectHandleAlias, "%s", aerr.Error())
	}
	t, verr := validateTaskHandleMembers(v)
	if verr != nil {
		return Task{}, false, verr
	}
	return t, true, nil
}

// selectsTaskVariant reports whether raw's EXACT top-level discriminator selects
// the task-handle variant (exact resultType:"task"), read RESILIENTLY so that a
// duplicate/lossy member ELSEWHERE — which already refused the strict tree — does
// not force the closed task validator onto a result that did NOT select the task
// variant (round-4 finding 3). Consulted only on a strict-decode failure. A
// duplicated (any value types — stage-7 M-1) or non-string exact resultType does
// NOT select the task variant here; the core contract then decides that result's
// disposition, and its own duplicate handling refuses the ambiguity as
// unreadable.
func selectsTaskVariant(raw json.RawMessage) bool {
	vals, occurrences := scanTopLevelExactMember(raw, "resultType")
	return occurrences == 1 && len(vals) == 1 && vals[0] == resultTypeTask
}

// maxTaskDurationMillis is the largest millisecond count that still converts to
// a time.Duration without wrapping (round-2 N-05). `time.Duration(n) *
// time.Millisecond` is a SIGNED 64-bit nanosecond multiplication: an accepted
// ttlMs of 9223372036854775807 wraps NEGATIVE, so a task that registered, bound
// its evidence and relayed its handle was lazily evicted as "already expired" on
// the very next read — a fully governed task turned into a permanent,
// unsweepable orphan by one accepted result field.
const maxTaskDurationMillis = int64(math.MaxInt64 / int64(time.Millisecond))

// taskMarkerKeyKind classifies ONE result-member name as a task marker, using
// EXACTLY the predicate the reserved-alias rejection uses (round-3 R3-06:
// `keyFoldsTo`, Unicode simple folding). Round-2 lowercased the key and compared
// it to an ASCII literal, which is strictly NARROWER than the alias rejector: a
// member spelled `reſultType` (U+017F) is an alias to the rejector and was not a
// marker to this classifier, and it is the classifier that decides whether the
// alias error is fatal. Leading/trailing whitespace in a key is folded away here
// as well — that only WIDENS the marker set, which is the deny-closed direction.
func taskMarkerKeyKind(key string) string {
	trimmed := strings.TrimSpace(key)
	switch {
	case keyFoldsTo(trimmed, "resultType"):
		return "resultType"
	case keyFoldsTo(trimmed, "taskId"):
		return "taskId"
	default:
		return ""
	}
}

// taskMarkerValue reports whether a marker member's VALUE means "this result
// carries a durable task identity" for the round-2 N-04 classification.
func taskMarkerValue(kind, value string) bool {
	switch kind {
	case "resultType":
		return strings.EqualFold(strings.TrimSpace(value), resultTypeTask)
	case "taskId":
		return strings.TrimSpace(value) != ""
	default:
		return false
	}
}

// resultCarriesTaskMarker classifies the result the way a PERMISSIVE consumer
// would read it (a case-insensitive map decode), order-independently.
func resultCarriesTaskMarker(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for key, val := range obj {
		kind := taskMarkerKeyKind(key)
		if kind == "" {
			continue
		}
		var s string
		if json.Unmarshal(val, &s) == nil && taskMarkerValue(kind, s) {
			return true
		}
	}
	return false
}

// strictTreeCarriesTaskMarker is the same classification over the STRICT tree,
// so the decision never depends on member ORDER or on which duplicate a
// permissive decoder happened to keep (round-2 N-04).
func strictTreeCarriesTaskMarker(v canonValue) bool {
	if v.kind != canonObject {
		return false
	}
	for i := range v.obj {
		m := v.obj[i]
		if m.val.kind != canonString {
			continue
		}
		if taskMarkerValue(taskMarkerKeyKind(m.key), m.val.str) {
			return true
		}
	}
	return false
}

// jsonNumberNonNegativeInteger decides, from the EXACT WIRE LEXEME of a JSON
// number, whether its VALUE is finite, integral and non-negative — and returns
// that value as (digits × 10^shift) with shift ≥ 0.
//
// ROUND-7 R7-01, and this is the whole point: `strconv.ParseInt` validates a
// LEXEME, not a value. JSON's number grammar (RFC 8259 §6) is
// `-? int frac? exp?`, so `1e3` and `1000.0` denote the same integral
// millisecond count as `1000` and a conforming server may send any of them —
// base-10 `ParseInt` refuses two of the three. It also conflates "not an
// integer" with "does not fit in int64", which is a STORAGE property this
// gateway may only impose where it actually stores the number.
//
// The decomposition is deliberately structural rather than arithmetic: nothing
// is ever materialized. `1e2000000000` is a conforming, integral, non-negative
// JSON number, and evaluating it (via big.Rat/big.Float) would allocate
// gigabytes on demand from an upstream — a memory bomb reached through the one
// body the deletion rule calls proof. An exponent too large to hold in an int is
// SATURATED: a huge positive one keeps the value integral (a caller that needs a
// magnitude sees `len(digits)+shift` blow past its own bound), and a huge
// negative one leaves a non-zero significand at an astronomically small
// magnitude, which is not an integer.
//
// Callers that STORE the value (the handle parser) apply their own range rule to
// the returned digits/shift; callers that merely validate a report do not.
func jsonNumberNonNegativeInteger(lexeme string) (digits string, shift int, ok bool) {
	s := lexeme
	if s == "" {
		return "", 0, false
	}
	negative := false
	if s[0] == '-' {
		negative = true
		s = s[1:]
	}
	expPart := ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		expPart, s = s[i+1:], s[:i]
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" || !allASCIIDigits(intPart) {
		return "", 0, false
	}
	if strings.ContainsRune(lexeme, '.') && !allASCIIDigits(fracPart) {
		return "", 0, false
	}
	// The significand with its leading zeros removed. An all-zero significand is
	// the value ZERO whatever the exponent says — and zero is non-negative even
	// when the lexeme is spelled `-0` or `-0.0e9`.
	sig := strings.TrimLeft(intPart+fracPart, "0")
	if sig == "" {
		return "0", 0, true
	}
	if negative {
		return "", 0, false
	}
	exp := 0
	if expPart != "" {
		expSign := 1
		switch expPart[0] {
		case '+':
			expPart = expPart[1:]
		case '-':
			expSign, expPart = -1, expPart[1:]
		}
		if expPart == "" || !allASCIIDigits(expPart) {
			return "", 0, false
		}
		const expSaturation = 1 << 30
		// ROUND-8 R8-03: an ALL-ZERO exponent is the exponent ZERO, not a parse
		// failure. `strings.TrimLeft("0","0")` is the empty string, `Atoi` then failed,
		// and the failure branch SATURATED — so `1e-0` (a conforming RFC 8259 spelling
		// of the integer 1) was read as `1e-1073741824` and refused as non-integral,
		// inside the one body the deletion rule calls proof. `1e0` and `1e+0` took the
		// same path with the harmless positive saturation.
		switch trimmed := strings.TrimLeft(expPart, "0"); {
		case trimmed == "":
			exp = 0
		default:
			if n, perr := strconv.Atoi(trimmed); perr != nil || n > expSaturation {
				exp = expSign * expSaturation
			} else {
				exp = expSign * n
			}
		}
	}
	shift = exp - len(fracPart)
	if shift >= 0 {
		return sig, shift, true
	}
	// A negative shift is integral only when exactly that many trailing digits of
	// the significand are zeros: `5000e-3` is 5, `5e-3` is not an integer.
	drop := -shift
	trailing := len(sig) - len(strings.TrimRight(sig, "0"))
	if trailing < drop {
		return "", 0, false
	}
	return sig[:len(sig)-drop], 0, true
}

func allASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// maxInt64Digits is the digit count above which a non-negative integer cannot
// possibly fit in an int64 (math.MaxInt64 has 19 digits). It is a cheap
// pre-check that keeps the handle parser from ever building a huge digit string.
const maxInt64Digits = 19

// strictOptionalPositiveInt reads an optional exact-cased integer member that
// must be a positive whole number when present (a malformed TTL would change
// the local record's lifetime without changing the bound effect) and no larger
// than the largest safely representable millisecond duration (N-05).
//
// ROUND-7 R7-01, applied here too: the VALUE decides, not the lexeme. This path
// genuinely STORES the number, so it keeps its positivity and duration-range
// rules — but `1e3` and `1000.0` are the millisecond count 1000 and are no
// longer refused for their spelling. Deliberately UNCHANGED: this parser still
// treats an explicit `null` as "absent". `CreateTaskResult = Result & Task`
// makes only `ttlMs` nullable, so a null `pollIntervalMs` is not conforming — but
// both members are read here purely as OPTIONAL LOCAL HINTS whose absence and
// whose null mean the identical thing (no hint), and tightening a handle
// rejection is a governance change (an ungovernable handle is refused outright,
// destroying a live upstream task's registration) that no round has asked for.
// It is carried as a stated residual instead of changed silently.
func strictOptionalPositiveInt(v canonValue, key, class string) (*int64, error) {
	m := v.member(key)
	if m == nil || m.val.kind == canonNull {
		return nil, nil
	}
	if m.val.kind != canonNumber {
		return nil, taskDefect(class, "mcp: task result: %s is not a number", key)
	}
	digits, shift, ok := jsonNumberNonNegativeInteger(m.val.num.String())
	if !ok || digits == "0" {
		return nil, taskDefect(class, "mcp: task result: %s is not a positive integer", key)
	}
	tooBig := taskDefect(class,
		"mcp: task result: %s exceeds the largest representable millisecond duration (refused)", key)
	if len(digits)+shift > maxInt64Digits {
		return nil, tooBig
	}
	n, err := strconv.ParseInt(digits+strings.Repeat("0", shift), 10, 64)
	if err != nil || n > maxTaskDurationMillis {
		return nil, tooBig
	}
	return &n, nil
}

// strictReportedMillis validates an INTEGER millisecond hint of a `tasks/get`
// REPORT. The authoritative types are `ttlMs: number | null` and
// `pollIntervalMs?: number` (ext-tasks `Task`), described by SEP-2663 as
// "integer milliseconds"; `nullable` says which of the two this member is.
//
// ROUND-6 R6-01: `strictOptionalPositiveInt` is the HANDLE parser's rule, and it
// is right there — the handle's TTL is STORED, so a zero (an already-expired
// record) or a value that wraps `time.Duration` (round-2 N-05) changes the local
// lifetime of a governed record. This path stores NEITHER value: it reads a
// status and nothing else. Applying the handle's stricter rule here refused a
// conforming report over a field the gateway does not even keep — the round-4
// `tasks/update` over-strictness class, repeated. Negative is still refused: a
// negative duration is not a conforming `Task` field on any reading.
//
// ROUND-7 R7-01 corrects the two remaining errors of that correction. The
// integrality test now reads the VALUE and not the base-10 lexeme (so `1e3` and
// `1000.0` pass and an int64-sized storage bound this path does not have is not
// imposed), and NULL is permitted only where the schema declares it — round-6
// waved every null through, so a `pollIntervalMs: null` that no conforming
// server may send was accepted inside the body the gateway treats as PROOF.
//
// ROUND-8 R8-04: the validated `ttlMs` is no longer discarded, so this returns the
// value as well as the verdict. `Task.ttlMs` is measured from creation and MAY
// CHANGE over the task's lifetime (SEP-2663), and a report that changed it was
// being validated and then thrown away — an owner whose task's TTL had been
// EXTENDED upstream was still evicted at the stale initial deadline and answered
// 403 without the gateway asking the upstream that still held the result.
//
// The returned pointer is nil for BOTH "explicit null" and "a value this process
// cannot represent as a duration": both mean UNBOUNDED to the retention rule. That
// is the conservative direction and it can never wrap the signed
// nanosecond multiplication (round-2 N-05) — a huge exponent is never even
// materialized, because `len(digits)+shift` decides first.
func strictReportedMillis(v canonValue, key, class string, nullable bool) (*int64, error) {
	m := v.member(key)
	if m == nil {
		return nil, nil
	}
	if m.val.kind == canonNull {
		if nullable {
			return nil, nil
		}
		return nil, taskDefect(class,
			"mcp: tasks/get result: %s is null; SEP-2663 declares only ttlMs as `number | null`", key)
	}
	if m.val.kind != canonNumber {
		return nil, taskDefect(class, "mcp: tasks/get result: %s is not a number", key)
	}
	digits, shift, ok := jsonNumberNonNegativeInteger(m.val.num.String())
	if !ok {
		return nil, taskDefect(class, "mcp: tasks/get result: %s is not a non-negative integer millisecond count", key)
	}
	// CONFORMING but not locally representable ⇒ unbounded, never a wrapped or
	// truncated duration. The report stays VALID (this path imposes no storage
	// bound — R6-01); only its local effect is the conservative one.
	if len(digits)+shift > maxInt64Digits {
		return nil, nil
	}
	n, perr := strconv.ParseInt(digits+strings.Repeat("0", shift), 10, 64)
	if perr != nil || n > maxTaskDurationMillis {
		return nil, nil
	}
	return &n, nil
}

// taskTimestampProfile NAMES the ONE date-time profile a conforming
// `Task.createdAt` / `Task.lastUpdatedAt` must use, and `isoTimestamp` below is
// its EXHAUSTIVE predicate. Naming the profile and deciding it in the same place
// is deliberate: the failure mode of the previous four rounds was a validator
// whose stated rule and whose accepted set were different sets.
//
// SEP-2663 says only "ISO 8601 timestamp", and ISO 8601 is a toolkit rather than
// one grammar, so "accepts ISO 8601" is not a decidable statement. The profile
// is therefore stated as a citation PLUS its two named relaxations:
//
//	W3C-DTF date-time (https://www.w3.org/TR/NOTE-datetime, normatively "a
//	profile of ISO 8601") at all three of its time-precision levels —
//	  YYYY-MM-DDThh:mm  /  :ss  /  :ss.s+  with TZD = Z | ±hh:mm
//	RELAXED, for the calendar-date forms only, by:
//	  (1) the ISO 8601 BASIC representation (`20260608T120000Z`, `±hhmm`), and
//	  (2) an OMITTED time-zone designator (a zoneless local time).
//
// Both relaxations are deliberate and both are the anti-over-strictness
// direction. This gateway READS NOTHING out of these two members — it stores no
// timestamp, orders nothing by them and compares nothing against them. Their only
// job is to refuse a body that is not a task report at all (round-5 accepted the
// single character "t" as a mandatory `Task` identity field of the body the
// deletion rule calls PROOF). Refusing a legal ISO 8601 spelling that a
// conforming server may emit would buy nothing and would make that server's
// records permanently undrainable — the R6-01 harm, one field over.
//
// What the profile deliberately EXCLUDES, each an answer to round-7 R7-01:
//
//   - a BARE DATE (`2026-06-08`, `20260608`) is an ISO 8601 *date*, not a
//     *date-time*. Round-6 accepted it as a timestamp while rejecting a
//     reduced-precision date-time — precisely backwards. A mandatory
//     `lastUpdatedAt` with no time of day cannot order two reports of one task;
//   - ORDINAL and WEEK dates (`2026-159T…`, `2026-W24-1T…`): ISO 8601 permits
//     them, this profile is restricted to CALENDAR dates, and that restriction is
//     stated rather than accidental;
//   - SURROUNDING WHITESPACE. Round-6 trimmed the wire text before validating, so
//     `" 2026-06-08T12:00:00Z "` — which no conforming server sends and which no
//     other consumer of this body would read that way — was silently normalized
//     into a valid proof field. The value is now validated EXACTLY as received.
//
// Known, stated residual: the profile permits at most 9 fractional-second digits,
// so a conforming date-time with 10+ is refused. It is recorded rather than
// hidden; no MCP implementation is known to emit one.
const taskTimestampProfile = "W3C-DTF date-time (a profile of ISO 8601), relaxed to the ISO 8601 basic " +
	"representation and to an omitted zone designator: [YYYY-MM-DD|YYYYMMDD]Thh:mm[:ss[.s]][Z|±hh:mm|±hhmm]"

// isoTimestamp reports whether s is a date-time of taskTimestampProfile. The wire
// text is validated EXACTLY as received — no trimming, no normalization (R7-01).
//
// ROUND-8 R8-03: the profile is decided by an EXPLICIT grammar-and-range predicate
// instead of by a list of `time.Parse` layouts. A layout is not an exhaustive
// predicate for the profile it is supposed to enumerate: Go's parser range-checks
// the CLOCK fields but NOT the zone designator (it accepted `+24:00` and `+23:60`)
// and it accepts a COMMA as the fractional separator (`12:00:00,5`). All three are
// outside the profile this file names, and all three were being accepted inside the
// one body the deletion rule calls proof — the same "stated rule ≠ accepted set"
// defect the previous rounds kept producing, one layer down.
//
// `time.Parse` survives for exactly what it is exact about and what a hand-rolled
// check would get wrong: whether the CALENDAR DATE exists (month lengths, leap
// years). Everything the profile states — the two date forms, the three time
// precisions, the `.` fraction and its 9-digit limit, and the offset ranges
// 00..23 / 00..59 — is decided here, positionally, on the text as received.
func isoTimestamp(s string) bool {
	if s == "" {
		return false
	}
	body, ok := trimTimestampZone(s)
	if !ok {
		return false
	}
	i := strings.IndexByte(body, 'T')
	if i < 0 {
		// A bare DATE is an ISO 8601 *date*, not a *date-time* (R7-01): a mandatory
		// `lastUpdatedAt` with no time of day cannot order two reports of one task.
		return false
	}
	date, clock := body[:i], body[i+1:]
	extended, dateOK := validProfileDate(date)
	return dateOK && validProfileTime(clock, extended)
}

// trimTimestampZone strips the OPTIONAL time-zone designator and validates it
// EXPLICITLY: `Z`, or a sign followed by hour 00..23 and minute 00..59 in either
// the extended (`±hh:mm`) or the basic (`±hhmm`) spelling. An absent designator is
// the profile's declared second relaxation (a zoneless local time).
//
// A designator that is present but out of range is NOT silently treated as absent:
// the remainder then still carries its sign and digits, which no accepted time
// shape matches, so `…T12:00:00+24:00` and `…T12:00:00+23:60` are refused.
func trimTimestampZone(s string) (string, bool) {
	if strings.HasSuffix(s, "Z") {
		return s[:len(s)-1], true
	}
	for _, n := range []int{6, 5} { // ±hh:mm, ±hhmm
		if len(s) <= n {
			continue
		}
		off := s[len(s)-n:]
		if off[0] != '+' && off[0] != '-' {
			continue
		}
		hh, mm := off[1:3], off[3:5]
		if n == 6 {
			if off[3] != ':' {
				continue
			}
			mm = off[4:6]
		}
		if !twoDigitInRange(hh, 0, 23) || !twoDigitInRange(mm, 0, 59) {
			continue
		}
		return s[:len(s)-n], true
	}
	return s, true
}

// validProfileDate decides the two CALENDAR date forms of the profile and reports
// which one it is (extended ⇒ the time part must be extended too: the profile pairs
// the representations, it does not mix them). Ordinal and week dates fail the
// positional digit/punctuation test, which is the restriction stated rather than
// accidental.
func validProfileDate(d string) (extended, ok bool) {
	switch len(d) {
	case 10: // YYYY-MM-DD
		if d[4] != '-' || d[7] != '-' ||
			!allASCIIDigits(d[0:4]) || !allASCIIDigits(d[5:7]) || !allASCIIDigits(d[8:10]) {
			return false, false
		}
		_, err := time.Parse("2006-01-02", d)
		return true, err == nil
	case 8: // YYYYMMDD
		if !allASCIIDigits(d) {
			return false, false
		}
		_, err := time.Parse("20060102", d)
		return false, err == nil
	}
	return false, false
}

// validProfileTime decides the three time precisions of the profile in the
// representation the date form selected. Every component is positional and
// range-checked here; the fraction is permitted ONLY at second precision, its
// separator is EXACTLY `.` (a comma can never appear in any accepted position),
// and its length is the profile's declared 1..9 digits.
func validProfileTime(s string, extended bool) bool {
	fraction := false
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frac := s[i+1:]
		if len(frac) == 0 || len(frac) > 9 || !allASCIIDigits(frac) {
			return false
		}
		s, fraction = s[:i], true
	}
	var hh, mm, ss string
	if extended {
		switch len(s) {
		case 5: // hh:mm
			if s[2] != ':' {
				return false
			}
			hh, mm = s[0:2], s[3:5]
		case 8: // hh:mm:ss
			if s[2] != ':' || s[5] != ':' {
				return false
			}
			hh, mm, ss = s[0:2], s[3:5], s[6:8]
		default:
			return false
		}
	} else {
		switch len(s) {
		case 4: // hhmm
			hh, mm = s[0:2], s[2:4]
		case 6: // hhmmss
			hh, mm, ss = s[0:2], s[2:4], s[4:6]
		default:
			return false
		}
	}
	if fraction && ss == "" {
		return false
	}
	if !twoDigitInRange(hh, 0, 23) || !twoDigitInRange(mm, 0, 59) {
		return false
	}
	return ss == "" || twoDigitInRange(ss, 0, 59)
}

// twoDigitInRange is the profile's one numeric-component rule: EXACTLY two ASCII
// digits whose value lies in [lo, hi]. It is what `time.Parse` does not do for a
// zone designator.
func twoDigitInRange(s string, lo, hi int) bool {
	if len(s) != 2 || !allASCIIDigits(s) {
		return false
	}
	n := int(s[0]-'0')*10 + int(s[1]-'0')
	return n >= lo && n <= hi
}

// taskFromResult is the strict handle probe: ok only for a strictly valid task
// handle (an ambiguous one is NOT a handle to this predicate).
func taskFromResult(raw json.RawMessage) (Task, bool) {
	t, ok, err := strictTaskFromResult(raw)
	if err != nil {
		return Task{}, false
	}
	return t, ok
}

// getTaskResultReserved is the reserved-member profile of the authoritative
// read: every member the validator reads by exact name, so a case-variant alias
// (`Status`, `TaskId`, the Unicode fold `ſtatus`) is REFUSED rather than
// silently ignored by a reader that matches case-insensitively.
var getTaskResultReserved = append(append([]string(nil), taskResultReservedMembers...), "_meta")

// taskStatusPayloadMember names the status-specific payload SEP-2663 REQUIRES on
// a `DetailedTask` variant: `CompletedTask.result`, `FailedTask.error`,
// `InputRequiredTask.inputRequests`. `WorkingTask` and `CancelledTask` declare
// none.
//
// ROUND-6 R6-01: this is a REQUIREMENT, not an exclusivity rule. Round-5 also
// refused the other statuses' payloads — but `GetTaskResult = Result &
// DetailedTask`, and the base `Result` carries `[key: string]: unknown`, so a
// member the variant does not declare is a permitted extension, not a
// contradiction. The gateway reads the `status` member and only that; a body
// that satisfies its own variant's requirement is a conforming report whatever
// else rides along.
func taskStatusPayloadMember(status string) string {
	switch status {
	case taskStatusCompleted:
		return "result"
	case taskStatusFailed:
		return "error"
	case taskStatusInputRequired:
		return "inputRequests"
	default:
		return ""
	}
}

// taskReport is EVERYTHING the governance view is allowed to learn from ONE
// validated authoritative `GetTaskResult`. It exists because round 8 needs the
// report to carry two facts a bare `(status, reason)` pair could not (R8-02,
// R8-04), and a widening tuple of returns would have let a caller pick up one and
// drop the other silently.
type taskReport struct {
	// Status is the upstream's own status for this exact task, already validated
	// against the five known statuses.
	Status string
	// Reason is the gateway-safe status text (statusMessage, or a failed task's
	// error.message).
	Reason string
	// Digest is the CANONICAL digest of the validated report — the IDENTITY of this
	// exact authoritative answer, computed over the strict tree (member order and
	// whitespace normalized away) rather than over raw bytes.
	//
	// ROUND-8 R8-02: proof that the owner collected its terminal result must be
	// bound to WHICH terminal report it saw. A bare boolean survived a later,
	// DIFFERENT authoritative terminal report — a different terminal status, a
	// changed result/error, or a changed payload under the same status — and
	// `retire` then deleted a row whose owner had never seen the report that is now
	// authoritative.
	//
	// ROUND-9 R9-02: the digest can only carry that identity if the canonical tree is
	// INJECTIVE over accepted reports. It is — but only because `decodeStrictJSON`
	// now refuses invalid UTF-8 and unpaired UTF-16 surrogate escapes
	// (rejectLossyJSONStrings). Without that refusal `"\ud800"` and `"\ud801"` both
	// decoded to U+FFFD, so two materially different reports the gateway ACCEPTED and
	// relayed raw shared one digest, and the owner's proof for the first satisfied
	// retirement of the second.
	//
	// ROUND-9 R9-04, said precisely: this value is NOT DIRECTLY SERIALIZED — it never
	// appears in a response or in an audit reason, because it is a correlation oracle
	// over upstream content. It is not unobservable: for a report whose raw body is
	// ALREADY in canonical form, this digest EQUALS the raw `resultDigest` that
	// `upstreamResultDigest` and the effect evidence already expose
	// (`taskreconcile.go`). That is the known R6-05 raw-digest oracle, not a new
	// disclosure — but "it never appears outside the ledger" was too strong a claim.
	Digest string
	// TTLMs is the report's CURRENT `ttlMs` (R8-04). nil means UNBOUNDED: either the
	// conforming explicit null, or a conforming value this process cannot represent
	// as a duration. It is never a wrapped or truncated number.
	TTLMs *int64
	// PollIntervalMs is the report's current optional polling cadence. Unlike
	// ttlMs it is never nullable; nil means the member was absent.
	PollIntervalMs *int64
	// InputRequests contains hash-only commitments computed from the exact-cased
	// member of a conforming input_required report.
	InputRequests []DurableTaskInputRef
}

// strictGetTaskResult validates the COMPLETE revision-appropriate SEP-2663
// `GetTaskResult` an authoritative `tasks/get` returned for taskID, and only
// then reports its status. It is the single gate in front of `confirmStatus`,
// the one path that may make a status terminal for reconciliation, for sweeps,
// and — through `retirable()` — for the operator retirement that ends a record's
// governance life (round-6 R6-02: retirement now hands the terminal result off to
// the owner before forgetting the row, but it is still destructive and it is still
// authorized by nothing except this validation).
//
// ROUND-5 R5-01: round-4 read only `status` (and `taskId` when present). A body
// containing nothing but `{"status":"completed"}` therefore passed as the
// "strictly validated authoritative confirmation" the deletion rule claims to
// require: it cleared `CancelUnconfirmed`, made the record retirable, and an
// operator `retire` then deleted a live task's record. A method-correlated but
// schema-invalid body is an upstream protocol FAULT, not proof that the work
// stopped.
//
// ROUND-6 R6-01: round-5 then swung PAST the target. `GetTaskResult = Result &
// DetailedTask`, and the base `Result` is an OPEN extension namespace — the
// official draft schema declares it as
//
//	export interface Result { _meta?: ResultMetaObject; resultType: ResultType;
//	                          [key: string]: unknown; }
//
// (modelcontextprotocol/schema/draft/schema.ts, accessed 2026-07-25). Round-5
// closed that namespace with an allowlist and additionally forbade the OTHER
// statuses' payloads, so a conforming server that answered a canceled task with
// `"com.example/resultExtension":{"version":1}` was told its report was
// "unknown", the reconciliation read returned 502 and the record could not be
// drained. That is exactly the round-4 `tasks/update` interoperability defect,
// one file over.
//
// The rule this settles on: VALIDATE WHAT THE GATEWAY CONSUMES AND WHAT THE
// VARIANT REQUIRES; do not police what the extension deliberately leaves open.
// So, exactly (primary sources: SEP-2663 at
// tasks.extensions.modelcontextprotocol.io and the draft schema above, both
// accessed 2026-07-25):
//
// ROUND-7 R7-01: the scalar rules were still two-sided wrong, so they are now
// transcribed FIELD BY FIELD from the authoritative declarations rather than
// paraphrased. `ext-tasks/schema/draft/schema.ts` (the extension's stated source
// of truth) declares
//
//	export interface Task { taskId: string; status: TaskStatus;
//	                        statusMessage?: string; createdAt: string;
//	                        lastUpdatedAt: string; ttlMs: number | null;
//	                        pollIntervalMs?: number; }
//
// and the core draft schema declares `_meta?: ResultMetaObject`. So: exactly ONE
// member is nullable (`ttlMs`), `_meta` is an object when present, and both
// millisecond members are integer VALUES (SEP-2663: "integer milliseconds") whose
// spelling on the wire — `1000`, `1000.0`, `1e3` — is the JSON grammar's business
// and not this gateway's. Timestamps are one NAMED profile (taskTimestampProfile),
// validated on the wire text exactly as received.
//
//   - `resultType` MANDATORY, exact-cased "complete" (the `Result` discriminator);
//   - `taskId` MANDATORY and EXACTLY the governed identifier (round-4 accepted an
//     absent one, so a body about another task — or about no task — confirmed this
//     one);
//   - `status` MANDATORY and one of the five known statuses;
//   - `createdAt`, `lastUpdatedAt` MANDATORY and date-times of
//     `taskTimestampProfile` (round-5 accepted the single character "t"; round-6
//     accepted a bare date and trimmed whitespace);
//   - `ttlMs` MANDATORY, `number | null`, and when numeric a non-negative INTEGER
//     VALUE with no int64 storage bound (this path stores neither hint);
//     `pollIntervalMs` OPTIONAL, never null, same numeric rule;
//   - `statusMessage` optional and a string; `_meta` optional and an OBJECT
//     (`_meta?: ResultMetaObject` — round-5 accepted any JSON value, round-6
//     still accepted null);
//   - the status-specific payload MANDATORY on its own variant and an object:
//     `result` on completed, `error` on failed, `inputRequests` on
//     input_required. Nothing else is forbidden: any further member is a legal
//     `Result` extension;
//   - duplicate members (rejected by the strict decoder at any depth) and
//     case-variant ALIASES of any member the validator reads are still refused —
//     the open namespace admits NEW names, never a second spelling of a name this
//     gateway resolves by exact case.
//
// Any defect returns a CLASSIFIED error (see taskResultDefect) and mutates
// nothing: the record stays retained and visible to reconciliation and to every
// later sweep, which is the safe direction (F-10).
func strictGetTaskResult(taskID string, raw json.RawMessage) (taskReport, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return taskReport{}, taskDefect(defectGetResultAbsent,
			"mcp: tasks/get result is absent; SEP-2663 requires a GetTaskResult (= Result & DetailedTask)")
	}
	v, derr := decodeStrictJSON(raw)
	if derr != nil {
		return taskReport{}, taskDefect(defectGetResultDecode,
			"mcp: tasks/get result failed strict decoding (ambiguous — refused): %s", derr.Error())
	}
	if v.kind != canonObject {
		return taskReport{}, taskDefect(defectGetResultShape, "mcp: tasks/get result must be a GetTaskResult object")
	}
	if aerr := rejectReservedKeyAliases(v, getTaskResultReserved); aerr != nil {
		return taskReport{}, taskDefect(defectGetResultAlias, "%s", aerr.Error())
	}
	rt := v.member("resultType")
	if rt == nil || rt.val.kind != canonString || rt.val.str != resultTypeComplete {
		return taskReport{}, taskDefect(defectGetResultDiscrim,
			"mcp: tasks/get result must carry the mandatory discriminator resultType %q (SEP-2663 GetTaskResult = Result & DetailedTask)",
			resultTypeComplete)
	}
	id := v.member("taskId")
	if id == nil || id.val.kind != canonString {
		return taskReport{}, taskDefect(defectGetResultTaskID, "mcp: tasks/get result: taskId is missing or not a string")
	}
	if id.val.str != taskID {
		return taskReport{}, taskDefect(defectGetResultTaskID, "mcp: tasks/get result reports another task identifier (refused)")
	}
	st := v.member("status")
	if st == nil || st.val.kind != canonString || !taskStatusValid(st.val.str) {
		return taskReport{}, taskDefect(defectGetResultStatus, "mcp: tasks/get result: status is missing or not a known task status")
	}
	for _, key := range []string{"createdAt", "lastUpdatedAt"} {
		m := v.member(key)
		if m == nil || m.val.kind != canonString || !isoTimestamp(m.val.str) {
			return taskReport{}, taskDefect(defectGetResultTimestamp,
				"mcp: tasks/get result: %s is missing or is not a %s", key, taskTimestampProfile)
		}
	}
	// ttlMs is MANDATORY and explicitly nullable ("number | null"): an absent
	// member is a different statement from "this task never expires", and the
	// difference decides whether a record may ever be forgotten.
	if ttl := v.member("ttlMs"); ttl == nil {
		return taskReport{}, taskDefect(defectGetResultTTL,
			"mcp: tasks/get result: ttlMs is missing (SEP-2663 Task requires it, nullable)")
	}
	ttlMs, terr := strictReportedMillis(v, "ttlMs", defectGetResultTTL, true)
	if terr != nil {
		return taskReport{}, terr
	}
	// ROUND-7 R7-01: `pollIntervalMs?: number` is OPTIONAL, not NULLABLE. Only
	// `ttlMs` carries the `| null` union, and the difference is load-bearing there
	// (null = "this task never expires"); waving every null through blessed a body
	// no conforming server may send.
	pollIntervalMs, perr := strictReportedMillis(v, "pollIntervalMs", defectGetResultPoll, false)
	if perr != nil {
		return taskReport{}, perr
	}
	if msg := v.member("statusMessage"); msg != nil && msg.val.kind != canonString {
		return taskReport{}, taskDefect(defectGetResultStatusMsg, "mcp: tasks/get result: statusMessage is not a string")
	}
	// `Result._meta` is `_meta?: ResultMetaObject` — OPTIONAL and, when present, an
	// OBJECT. Round-5 declared it permitted and then never checked its type, so a
	// scalar `_meta` rode through the one body the deletion rule calls proof; round-6
	// checked the type but kept exempting `null`, which the declaration does not
	// permit either (round-7 R7-01).
	if meta := v.member("_meta"); meta != nil && meta.val.kind != canonObject {
		return taskReport{}, taskDefect(defectGetResultMeta,
			"mcp: tasks/get result: _meta is not an object (core schema: _meta?: ResultMetaObject)")
	}
	// The status-specific payload the VARIANT requires, and only that. A
	// `completed` body with no `result` is not a `CompletedTask` — that is the
	// requirement round-5 got right and this round keeps.
	if want := taskStatusPayloadMember(st.val.str); want != "" {
		m := v.member(want)
		if m == nil || m.val.kind == canonNull {
			return taskReport{}, taskDefect(defectGetResultPayload,
				"mcp: tasks/get result: status %q requires the member %q (SEP-2663 DetailedTask)", st.val.str, want)
		}
		if m.val.kind != canonObject {
			return taskReport{}, taskDefect(defectGetResultPayloadType, "mcp: tasks/get result: %s must be an object", want)
		}
	}
	reason := ""
	if msg := v.member("statusMessage"); msg != nil {
		reason = strings.TrimSpace(msg.val.str)
	}
	if reason == "" {
		if e := v.member("error"); e != nil && e.val.kind == canonObject {
			if m := e.val.member("message"); m != nil && m.val.kind == canonString {
				reason = strings.TrimSpace(m.val.str)
			}
		}
	}
	var inputRequests []DurableTaskInputRef
	if st.val.str == taskStatusInputRequired {
		entries, _ := strictObjectMembers(&v, "inputRequests")
		inputRequests = durableTaskInputRefs(entries)
	}
	return taskReport{
		Status: st.val.str, Reason: reason,
		Digest: resultDigest(encodeCanonical(v)),
		TTLMs:  ttlMs, PollIntervalMs: pollIntervalMs,
		InputRequests: inputRequests,
	}, nil
}

// taskUpdateAckMembers are the ONLY members a conformant `UpdateTaskResult` may
// carry: the MANDATORY `resultType` discriminator and the base-result `_meta`.
//
// Round-4 R4-02: round-3 read SEP-2663's phrase "empty acknowledgement" as "an
// empty object" and made `resultType` FORBIDDEN, which rejected the normative
// success and answered a conformant upstream with 502. The extension calls the
// result empty because it carries no TASK STATE, but it expressly defines
// `UpdateTaskResult = Result` and mandates `resultType:"complete"` on it — `{}`
// is NOT conformant.
var taskUpdateAckMembers = []string{"resultType", "_meta"}

// taskUpdateAckReserved is the reserved-member profile of the ack validation: the
// mandatory discriminator, the base metadata AND every task-state member, so a
// case-variant alias of any of them (`ResultType`, `Status`, `TaskId`, the
// Unicode fold `reſultType`) is refused rather than silently ignored. Without the
// task-state names in the profile, an alias of `status` would simply fall into
// the "unknown member" branch — the right verdict by luck, not by rule — while an
// alias of `resultType` could shadow the discriminator for a case-folding reader.
var taskUpdateAckReserved = append(append([]string(nil), taskResultReservedMembers...), "_meta")

// strictTaskUpdateAck validates the NORMATIVE SEP-2663 `UpdateTaskResult` shape
// (round-3 R3-01, corrected in round-4 R4-02). The extension defines the update
// result as an eventually-consistent acknowledgement that carries NO task state
// and directs clients to observe status through `tasks/get` or task
// notifications — it is never authoritative about task state. The production
// forwarder validates the JSON-RPC ENVELOPE, not the method-specific result
// shape, so a broken or hostile upstream can answer a tasks/update with a
// strictly valid success whose body claims
// `{"resultType":"complete","status":"canceled"}`. Round-2 passed that body to
// the authoritative confirmation function and retired a live task from every
// later kill-switch sweep.
//
// The status synchronization is gone; this predicate additionally refuses to let
// such a body be RELAYED, so the gateway's governance view and the client's view
// of the task cannot diverge. Round-3 over-corrected into the opposite
// interoperability failure — it REJECTED `resultType` and accepted `{}` — so it
// answered the conformant success with 502 while blessing a non-conformant one.
// The rules are therefore, exactly:
//
//   - `resultType` is MANDATORY and must be the exact-cased string "complete";
//   - the only other permitted member is the base result's `_meta`;
//   - a missing, differently-valued, case-variant-aliased or duplicated
//     discriminator is refused, as is EVERY task-state member.
//
// A missing result body is refused for the same reason a missing discriminator
// is: the gateway cannot confirm the upstream produced a conformant `Result`, and
// an unconfirmable acknowledgement is not an acknowledgement.
//
// ROUND-7 R7-06: every refusal below is now a CLASSIFIED `taskResultDefect`. It
// used to return bare `fmt.Errorf` values whose text quotes upstream-controlled
// PROPERTY NAMES — the unpermitted-member branch quotes the key verbatim, and the
// alias rejector quotes the alias — and the tasks/update handler concatenated the
// whole error into the durable audit reason. That is the identical content channel
// R6-05 closed on the two other validators; the taxonomy simply did not reach this
// one, which is why the round-7 probe found `ROUND7-UPDATE-SECRET-PROPERTY` in the
// persisted reason. The DETAIL still exists (it never leaves the process); only
// the class is persisted.
func strictTaskUpdateAck(raw json.RawMessage) error {
	if strings.TrimSpace(string(raw)) == "" {
		return taskDefect(defectUpdateAckAbsent,
			"mcp: tasks/update result is absent; SEP-2663 requires an UpdateTaskResult carrying resultType %q", resultTypeComplete)
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return taskDefect(defectUpdateAckDecode, "mcp: tasks/update result failed strict decoding: %s", err.Error())
	}
	if v.kind != canonObject {
		return taskDefect(defectUpdateAckShape, "mcp: tasks/update result must be an UpdateTaskResult object")
	}
	// Duplicate members at any depth were already refused by the strict decoder;
	// this refuses case-variant ALIASES of the discriminator, of `_meta` and of
	// every task-state member (the same Unicode simple-folding predicate the rest
	// of the connector uses).
	if aerr := rejectReservedKeyAliases(v, taskUpdateAckReserved); aerr != nil {
		return taskDefect(defectUpdateAckAlias, "%s", aerr.Error())
	}
	rt := v.member("resultType")
	if rt == nil || rt.val.kind != canonString || rt.val.str != resultTypeComplete {
		return taskDefect(defectUpdateAckDiscrim,
			"mcp: tasks/update result must carry the mandatory discriminator resultType %q (SEP-2663 UpdateTaskResult = Result)",
			resultTypeComplete)
	}
	for i := range v.obj {
		allowed := false
		for _, member := range taskUpdateAckMembers {
			if v.obj[i].key == member {
				allowed = true
				break
			}
		}
		if !allowed {
			return taskDefect(defectUpdateAckTaskState,
				"mcp: tasks/update result carries %q: the update result is an ACKNOWLEDGEMENT and may not report task state (observe it via tasks/get)",
				v.obj[i].key)
		}
	}
	return nil
}

// taskRequestParams builds the params of ONE Tasks-extension request: the
// per-request `_meta` identity (protocol version, client info) PLUS the
// per-request client-capability declaration of the tasks extension. It is the
// single construction path — the introspection client's `taskCall` and the
// gateway's operator reconciliation read both go through it (round-5 R5-05).
//
// A server MUST NOT honor an extension the request did not declare (-32021), so
// a tasks/* request synthesized without this declaration is not a conforming
// request at all: a strict upstream refuses it, and a record that cannot be read
// cannot be drained.
func taskRequestParams(meta requestMeta, params any) (map[string]any, error) {
	return meta.withExtensions(extensionTasks).inject(params)
}

// tasksRequestMeta is the per-request identity of a Tasks-extension request the
// GATEWAY synthesizes for a given upstream protocol revision (the connector's
// own client name/version — the request is the gateway's, not the caller's).
func tasksRequestMeta(revision string) requestMeta {
	return requestMeta{
		version: revision,
		info:    clientInfo{Name: clientName, Version: clientVersion},
	}
}

// taskCall performs one tasks/* request with the extension declared in the
// per-request capabilities (stateless mode only — the extension rides the RC).
func (c *Client) taskCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !c.stateless() {
		return nil, fmt.Errorf("mcp: %s requires the %s (stateless) mode", method, revision20260728)
	}
	obj, err := taskRequestParams(*c.meta, params)
	if err != nil {
		return nil, err
	}
	raw, err := c.t.roundTrip(ctx, rpcRequest{Method: method, Params: obj})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if _, envErr := checkResultEnvelope(method, raw); envErr != nil {
		return nil, envErr
	}
	return raw, nil
}

// TaskGet fetches one task's current state (tasks/get). Over Streamable HTTP
// the transport mirrors params.taskId into Mcp-Name (SEP-2663 sticky routing).
func (c *Client) TaskGet(ctx context.Context, taskID string) (Task, error) {
	raw, err := c.taskCall(ctx, "tasks/get", struct {
		TaskID string `json:"taskId"`
	}{TaskID: taskID})
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return Task{}, fmt.Errorf("tasks/get result: %w", err)
	}
	if t.TaskID == "" {
		t.TaskID = taskID
	}
	return t, nil
}

// TaskCancel requests cooperative cancellation (tasks/cancel; ack-only — the
// final status may still be non-canceled, observed via TaskGet).
//
// STAGE-7 H-1R3 (r3 contrast, 2026-07-30): a nil return is an ACKNOWLEDGEMENT
// CLAIM to an external consumer, so this exported emitter consumes the same
// conformity decision as the gateway's two internal ones (cancelAckViolation)
// instead of turning the transport error alone into the public contract.
// checkResultEnvelope cannot stand in for it: its last-wins envelope read sees
// "complete" (or nothing) on exactly the duplicated discriminators the shared
// decision refuses, so the raw acknowledgement was being discarded and a
// non-acknowledgement confirmed as success.
func (c *Client) TaskCancel(ctx context.Context, taskID string) error {
	raw, err := c.taskCall(ctx, "tasks/cancel", struct {
		TaskID string `json:"taskId"`
	}{TaskID: taskID})
	if err != nil {
		return err
	}
	if violation := cancelAckViolation(raw); violation != "" {
		return fmt.Errorf("mcp: tasks/cancel result %s; the acknowledgement is not a conforming CancelTaskResult "+
			"and cancellation delivery is unproven", violation)
	}
	return nil
}

// Poll-interval bounds: the server's pollIntervalMs is honored (it MAY
// rate-limit faster pollers) but floored so a hostile 0/negative hint cannot
// turn the poller into a busy-loop; absent, a conservative default applies.
const (
	defaultTaskPollInterval = 2 * time.Second
	minTaskPollInterval     = 100 * time.Millisecond
)

// PollTask polls tasks/get until the task reaches a terminal status or ctx is
// done, honoring the server's pollIntervalMs (which MAY change between polls).
// An input_required task is returned AS-IS, not answered: this read-only
// connector never fulfills MRTR input (deny-closed; an actuation-side caller
// with an approval flow decides whether to tasks/update).
func (c *Client) PollTask(ctx context.Context, taskID string) (Task, error) {
	for {
		t, err := c.TaskGet(ctx, taskID)
		if err != nil {
			return Task{}, err
		}
		if t.terminal() || t.Status == taskStatusInputRequired {
			return t, nil
		}
		interval := defaultTaskPollInterval
		// Round-2 N-05: a hint above the largest representable millisecond
		// duration would wrap the signed multiplication negative and turn the
		// poller into a busy loop; such a hint is ignored, not clamped upward.
		if t.PollIntervalMs != nil && *t.PollIntervalMs > 0 && *t.PollIntervalMs <= maxTaskDurationMillis {
			interval = time.Duration(*t.PollIntervalMs) * time.Millisecond
		}
		if interval < minTaskPollInterval {
			interval = minTaskPollInterval
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(interval):
		}
	}
}
