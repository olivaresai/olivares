// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// evidencetask.go (q1-MCP stage 4) — the evidence binding of the durable
// task surface: strict canonicalization of the tasks/get|update|cancel params
// (the same P0 class as the tools/call case-fold smuggling: taskId used to be
// read through a case-INSENSITIVE json.Unmarshal), the task-method
// OperationID/EffectDigest derivations, and the SERVER-INITIATED child bindings
// (task registration tracking, the compensating cancel, the kill-switch sweep)
// that make every durable task effect claim-anchored BEFORE it runs.
//
// Derivation rules follow evidence.go: length-prefixed digests over versioned
// domain labels (evidenceLPDigest — uint64-BE length ‖ bytes per part, never
// delimiter joins, never NUL separators), ONLY stable fields (never reason
// text), and OperationID never derived from method or params (a reused key with
// a different effect keeps the OperationID and changes the EffectDigest — the
// rebind refusal). The task-method OperationID deliberately REUSES
// deriveToolCallOperationID: the operation-key namespace (tenant, resource,
// issuer, client, subject, act-as) is method-agnostic by design — the same key
// on a different method is a rebind, not a new operation.

// Domain-separation labels of the task-surface derivations.
const (
	mcpTaskEffectDomainV1     = "olivares.mcp.task.effect.v1"
	mcpTaskEffectProfileV1    = "mcp-task-binding-v1"
	mcpTaskPolicyDomainV1     = "olivares.mcp.task.policy.v1"
	mcpTaskOwnerDomainV1      = "olivares.mcp.task.owner.v1"
	mcpTaskGenerationDomainV1 = "olivares.mcp.task.generation.v1"

	mcpTaskTrackOperationDomainV1 = "olivares.mcp.task.track.operation.v1"
	mcpTaskTrackEffectDomainV1    = "olivares.mcp.task.track.effect.v1"

	mcpTaskCompensationOperationDomainV1 = "olivares.mcp.task.cancel.compensation.operation.v1"
	mcpTaskCompensationEffectDomainV1    = "olivares.mcp.task.cancel.compensation.effect.v1"

	mcpTaskSweepOperationDomainV1 = "olivares.mcp.task.cancel.sweep.operation.v1"
	mcpTaskSweepEffectDomainV1    = "olivares.mcp.task.cancel.sweep.effect.v1"

	mcpTaskLocalOriginOperationDomainV1 = "olivares.mcp.task.origin.local.operation.v1"
	mcpTaskLocalOriginEffectDomainV1    = "olivares.mcp.task.origin.local.effect.v1"

	// mcpTaskReconcileEffectDomainV1 domain-separates the OPERATOR reconciliation
	// surface from every other task effect (round-4 reconciliation surface): an
	// operator's authoritative read of T/G is not the owner's tasks/get of T/G,
	// and reusing one operation key across the two must REBIND, not replay.
	mcpTaskReconcileEffectDomainV1  = "olivares.mcp.task.reconcile.effect.v1"
	mcpTaskReconcileEffectProfileV1 = "mcp-task-reconcile-binding-v1"
)

// Stable reason CLASSES bound into the compensating-cancel/sweep effect
// digests — classes, never free reason text (design: only stable fields).
const (
	taskCancelClassAdmissionDenied = "admission-denied"
	taskCancelClassLedgerCap       = "ledger-cap"
	taskCancelClassToolRevoked     = "tool-revoked"
	taskCancelClassTrackRefused    = "track-refused"
	taskCancelClassKillSwitch      = "kill-switch-sweep"
)

// Journal action labels of the evidence-enforced task effects (the adapter
// appends ".claim"/".settle" ledger events per the stage-3 convention). The
// client task methods are suffixed with the OperationID provenance kind
// (keyed | request_instance); the server-initiated child operations are not
// (their provenance is the derivation itself).
const (
	taskActionGetPrefix    = "mcp.task.get."
	taskActionCancelPrefix = "mcp.task.cancel."
	taskActionUpdatePrefix = "mcp.task.update."
	taskActionTrack        = "mcp.task.track"
	taskActionCompensation = "mcp.task.cancel.compensation"
	taskActionSweep        = "mcp.task.cancel.sweep"

	// Operator reconciliation actions (round-4). Every MUTATING reconciliation
	// action is evidence-bound exactly like the rest of stage 4 — claim + anchor
	// BEFORE the effect, leadership fence immediately before it, durable
	// settlement before the response — so the operator workflow that repairs the
	// governance view is itself governed.
	taskActionReconcileStatusPrefix = "mcp.task.reconcile.status."
	taskActionReconcileClearPrefix  = "mcp.task.reconcile.clear."
	taskActionReconcileRetirePrefix = "mcp.task.reconcile.retire."
)

// Stable reconciliation effect CLASSES bound into the reconciliation
// EffectDigest — classes, never operator-supplied text.
const (
	taskReconcileClassStatus = "authoritative-status-read"
	taskReconcileClassClear  = "clear-cancellation-bar"
	taskReconcileClassRetire = "retire-confirmed-terminal"
)

// deriveTaskReconcileEffectDigest binds one OPERATOR reconciliation effect: its
// own versioned domain + profile (never the client task-method domain), tenant,
// resource, method, the full operator token identity, the upstream descriptor,
// the mcp.task target as the (taskID, GENERATION) pair the action names, the
// canonical owner digest the operator asserted, the stable action class and the
// canonical effect params. Reusing one operation key across two different
// reconciliation targets — or across a reconciliation and a client method —
// therefore keeps the OperationID and changes the EffectDigest: the rebind
// refusal, not a fresh operation.
func deriveTaskReconcileEffectDigest(tenant, resource, method string, tok validatedToken,
	taskID, generation, ownerDigest, upstreamDescriptor, class string,
	grantedScopes []string, canon canonicalTaskParams) string {
	paramsSum := sha256.Sum256(canon.Effect)
	parts := []string{
		mcpTaskReconcileEffectDomainV1,
		mcpTaskReconcileEffectProfileV1,
		tenant,
		resource,
		method,
		tok.Issuer,
		tok.ClientID,
		tok.Subject,
		tok.ActAs,
		upstreamDescriptor,
		"mcp.task",
		taskID,
		"generation:" + generation,
		"owner:" + ownerDigest,
		"class:" + class,
		fmt.Sprintf("granted_scopes:%d", len(grantedScopes)),
	}
	parts = append(parts, grantedScopes...)
	parts = append(parts, canon.Presence, hex.EncodeToString(paramsSum[:]))
	return evidenceLPDigest(parts...)
}

// taskReservedKeys is the reserved-key profile of the task methods: EVERY task
// member a PEP interprets must be here, so a case-variant alias can never make
// the mediator authorize one logical field while the actuator consumes another
// (review round-1 F-07 — the same parser-differential class as the stage-3
// name/arguments P0). The gate reads the exact-cased "taskId", the MRTR
// mediator reads the exact-cased "inputResponses", and "_meta" carries the
// operation-key extension.
var taskReservedKeys = []string{"taskId", "_meta", "inputResponses"}

// canonicalTaskParams is the strict, canonicalized view of one tasks/get|
// update|cancel params payload.
type canonicalTaskParams struct {
	// Presence distinguishes absent params, explicit null and a present object.
	Presence string
	// TaskID is the exact-cased value of the top-level "taskId" member ("" when
	// absent or not a string). The gate resolves the task record from THIS,
	// never from a case-insensitive struct unmarshal (the smuggling vector).
	TaskID string
	// InputResponses is the exact-cased params.inputResponses object the MRTR
	// mediation PEP inspects, rendered member-by-member from the STRICT tree
	// (F-07). The historical case-INSENSITIVE json.Unmarshal could consume a
	// different member than a case-folding upstream reads out of the very bytes
	// forwarded; extraction from the strict tree removes the differential.
	InputResponses map[string]json.RawMessage
	// OperationKey is the client-supplied idempotency key ("" when none).
	OperationKey string
	// Forward is the canonical params to forward upstream (operation key
	// STRIPPED, everything else — trace correlation included — preserved).
	Forward json.RawMessage
	// Effect is the canonical effect view bound into the EffectDigest (Forward
	// minus the versioned W3C trace-correlation members). It is ALSO the payload
	// view a destructive tasks/update approval binds (F-08).
	Effect []byte
}

// effectHash is the hex SHA-256 of the canonical effect view — the exact
// payload identity shared by the EffectDigest and the approval plan hash.
func (c canonicalTaskParams) effectHash() string {
	sum := sha256.Sum256(c.Effect)
	return hex.EncodeToString(sum[:])
}

// extractOperationKey extracts and STRIPS the params._meta operation-key
// extension from the strict tree. A present key must be a non-empty string.
func extractOperationKey(v *canonValue) (string, error) {
	meta := v.member("_meta")
	if meta == nil || meta.val.kind != canonObject {
		return "", nil
	}
	opKey := meta.val.member(mcpOperationKeyMeta)
	if opKey == nil {
		return "", nil
	}
	if opKey.val.kind != canonString || strings.TrimSpace(opKey.val.str) == "" {
		return "", fmt.Errorf("mcp: params: _meta[%q] must be a non-empty string", mcpOperationKeyMeta)
	}
	key := opKey.val.str
	meta.val.removeMember(mcpOperationKeyMeta)
	return key, nil
}

// strictObjectMembers renders the members of an exact-cased object member of the
// STRICT tree as canonical bytes, keyed exactly. ok=false when the member is
// absent or is not an object. This is the ONLY way a PEP reads a mediated task
// member: no json.Unmarshal, so no case-folding and no last-duplicate-wins
// (F-07 — the strict decoder already rejected duplicate keys at every depth).
func strictObjectMembers(v *canonValue, key string) (map[string]json.RawMessage, bool) {
	m := v.member(key)
	if m == nil || m.val.kind != canonObject {
		return nil, false
	}
	out := make(map[string]json.RawMessage, len(m.val.obj))
	for i := range m.val.obj {
		out[m.val.obj[i].key] = json.RawMessage(encodeCanonical(m.val.obj[i].val))
	}
	return out, true
}

// stripTraceMembers removes the versioned W3C trace-correlation members from
// _meta (the effect view: a retry with a fresh trace id is the SAME effect).
func stripTraceMembers(v *canonValue) {
	if meta := v.member("_meta"); meta != nil && meta.val.kind == canonObject {
		meta.val.removeMember(w3cTraceParent)
		meta.val.removeMember(w3cTraceState)
		meta.val.removeMember(w3cBaggage)
	}
}

// canonicalizeTaskParams strictly decodes and canonicalizes the params of a
// tasks/get|update|cancel request. Any error is a PROTOCOL refusal (invalid
// params, 400/-32602) BEFORE any claim, ledger lookup or forward. Absent/null
// params yield an empty TaskID (the missing-taskId refusal path).
func canonicalizeTaskParams(raw json.RawMessage) (canonicalTaskParams, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return canonicalTaskParams{Presence: paramsAbsent}, nil
	}
	if trimmed == "null" {
		return canonicalTaskParams{
			Presence: paramsNull,
			Forward:  json.RawMessage("null"),
			Effect:   []byte("null"),
		}, nil
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return canonicalTaskParams{}, err
	}
	if v.kind != canonObject {
		return canonicalTaskParams{}, fmt.Errorf("mcp: task params must be a JSON object")
	}
	if err := rejectReservedKeyAliases(v, taskReservedKeys); err != nil {
		return canonicalTaskParams{}, err
	}

	out := canonicalTaskParams{Presence: paramsPresent}
	if id := v.member("taskId"); id != nil && id.val.kind == canonString {
		// The record the gate authorizes MUST be the record the upstream acts on:
		// the in-memory ledger trims lookups, so an untrimmed spelling would
		// resolve one record while the forwarded bytes carry another — refuse the
		// ambiguity outright (strictness hardening, same class as the aliases).
		if id.val.str != strings.TrimSpace(id.val.str) {
			return canonicalTaskParams{}, fmt.Errorf("mcp: params: taskId carries leading/trailing whitespace (ambiguous — refused)")
		}
		out.TaskID = id.val.str
	}
	// The mediated MRTR member is read from the SAME strict tree with exact
	// casing (F-07) — never re-parsed out of the forwarded bytes.
	if members, ok := strictObjectMembers(&v, "inputResponses"); ok {
		out.InputResponses = members
	}
	key, err := extractOperationKey(&v)
	if err != nil {
		return canonicalTaskParams{}, err
	}
	out.OperationKey = key
	out.Forward = encodeCanonical(v)
	stripTraceMembers(&v)
	out.Effect = encodeCanonical(v)
	return out, nil
}

// taskPolicyDigest binds the STABLE task-policy posture of one task-surface
// effect: the tracked record's required scope and destructive classification,
// the CURRENT tool policy's sorted allowed roles (tasks/update re-resolves
// them; nil for get/cancel, which do not run the role gate), and the COAZ
// posture + stable references (tasks/update re-runs COAZ; the "unconsulted"
// state marks the methods that never evaluate it). Only stable fields — never
// reason text (compile-time guarantee: the signature accepts none).
func taskPolicyDigest(requiredScope string, destructive bool, roles []string, coaz coazBinding) string {
	parts := []string{
		mcpTaskPolicyDomainV1,
		requiredScope,
		"destructive:" + boolMark(destructive),
	}
	sorted := append([]string(nil), roles...)
	sort.Strings(sorted)
	parts = append(parts, fmt.Sprintf("roles:%d", len(sorted)))
	parts = append(parts, sorted...)
	parts = append(parts, "coaz:"+coaz.State, coaz.DecisionRef, coaz.PolicyVersion)
	return evidenceLPDigest(parts...)
}

// coazStateUnconsulted is the stable COAZ posture of a task method that never
// evaluates COAZ (tasks/get, tasks/cancel) — distinct from "unwired" (an
// evaluator-less deployment on a method that WOULD consult it).
const coazStateUnconsulted = "unconsulted"

// deriveTaskGeneration is the IMMUTABLE identity of one task registration
// INSTANCE (round-2 N-03). The textual task id is upstream-controlled and may be
// re-issued once the gateway expires or releases its record, so it can never be
// the identity of an effect: a stale cancellation intent would suppress the
// kill-switch cancellation of a REPLACEMENT task, and a stale owner could apply
// its update/cancel to it.
//
// The generation derives from the record's ANCHORED origin (the mcp.task.track
// binding, or the parent that surfaced the task for a record the gateway refused
// to govern), the canonical owner and a never-reused per-ledger sequence. The
// origin makes it unique across process restarts — where the in-memory ledger is
// lost but the durable evidence journal survives, so attempt 1 of a replacement
// task must not derive the same operation identity as attempt 1 of the old one.
// The sequence makes it unique within one process even for records injected
// outside the governed path (zero origin).
func deriveTaskGeneration(rec TaskRecord, seed sdk.EvidenceBinding, seq uint64) string {
	return evidenceLPDigest(
		mcpTaskGenerationDomainV1,
		rec.Tenant,
		rec.TaskID,
		rec.owner().digest(),
		string(seed.OperationID),
		string(seed.EffectDigest),
		"seq:"+strconv.FormatUint(seq, 10),
	)
}

// deriveTaskEffectDigest binds the FULL effective request of one client task
// method (tasks/get | tasks/cancel | tasks/update): its own versioned domain +
// profile, tenant, resource, method, caller identity, the stable upstream
// descriptor, the mcp.task target (the taskId AND the record generation that
// currently holds it — round-2 N-03), normalized granted scopes, params presence
// + the canonical-effect-params hash, the task policy digest, and the approval
// binding (destructive tasks/update only). OperationID, JSON-RPC id, timestamps
// and trace context are deliberately excluded.
//
// The generation binds into the EFFECT, never into the client OperationID: the
// operation-key namespace must stay method- and target-agnostic so that reusing
// a key against a REPLACEMENT task keeps the same OperationID and changes the
// EffectDigest — the rebind REFUSAL. Folding the generation into the OperationID
// would mint a fresh operation instead, which is strictly weaker.
func deriveTaskEffectDigest(tenant, resource, method string, tok validatedToken,
	taskID, generation, upstreamDescriptor string, grantedScopes []string,
	canon canonicalTaskParams, policyDigest, approvalRef, approvedPlanHash string) string {
	paramsSum := sha256.Sum256(canon.Effect)
	parts := []string{
		mcpTaskEffectDomainV1,
		mcpTaskEffectProfileV1,
		tenant,
		resource,
		method,
		tok.Issuer,
		tok.ClientID,
		tok.Subject,
		tok.ActAs,
		upstreamDescriptor,
		"mcp.task",
		taskID,
		"generation:" + generation,
		fmt.Sprintf("granted_scopes:%d", len(grantedScopes)),
	}
	parts = append(parts, grantedScopes...)
	parts = append(parts,
		canon.Presence,
		hex.EncodeToString(paramsSum[:]),
		policyDigest,
		approvalRef,
		approvedPlanHash,
	)
	return evidenceLPDigest(parts...)
}

// taskTTLMark is the stable TTL identity of a registration record. TTL is
// effect-changing (review round-1 F-11): a record with a short ttlMs expires
// locally and becomes unsweepable while an untimed one persists, so two
// otherwise identical registrations are NOT the same governed effect.
func taskTTLMark(ttlMs *int64) string {
	if ttlMs == nil {
		return "ttl:absent"
	}
	return "ttl:" + strconv.FormatInt(*ttlMs, 10)
}

// deriveTaskTrackBinding derives the SERVER-INITIATED child operation that
// registers a durable task handle returned by an enforced tools/call: the
// OperationID chains the PARENT operation (deterministic — a re-claim of the
// same parent/task can never mint a second registration), the EffectDigest
// chains the parent effect digest plus the FULL stable registration record —
// the upstream descriptor and canonical owner the actuator resolves, the task
// id, tool, required scope, destructive classification, TTL semantics and
// initial status (F-11: a full binding, not a forensic label).
func deriveTaskTrackBinding(tenant, upstreamDescriptor string, parent sdk.EvidenceBinding, rec TaskRecord) sdk.EvidenceBinding {
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(evidenceLPDigest(
			mcpTaskTrackOperationDomainV1,
			tenant,
			string(parent.OperationID),
			rec.TaskID,
		)),
		EffectDigest: sdk.EffectDigest(evidenceLPDigest(
			mcpTaskTrackEffectDomainV1,
			tenant,
			upstreamDescriptor,
			rec.owner().digest(),
			string(parent.EffectDigest),
			rec.TaskID,
			rec.Tool,
			rec.RequiredScope,
			"destructive:"+boolMark(rec.Destructive),
			taskTTLMark(rec.TTLMs),
			rec.Status,
		)),
	}
}

// deriveTaskLocalOriginBinding is the STABLE, server-derived origin identity of
// a tracked task that carries NO anchored registration binding (a record
// injected outside the governed track path). It exists so a safety compensation
// can chain from a server-owned identity instead of an UNCLAIMED client request
// (review round-1 F-04: a child operation can never make an unclaimed or
// rebound parent trustworthy). It takes no client input at all.
func deriveTaskLocalOriginBinding(tenant string, rec TaskRecord) sdk.EvidenceBinding {
	owner := rec.owner().digest()
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(evidenceLPDigest(
			mcpTaskLocalOriginOperationDomainV1, tenant, owner, rec.TaskID)),
		EffectDigest: sdk.EffectDigest(evidenceLPDigest(
			mcpTaskLocalOriginEffectDomainV1, tenant, owner, rec.TaskID, rec.Tool, rec.RequiredScope)),
	}
}

// taskOriginBinding returns the record's ANCHORED registration binding when the
// task was registered through the governed mcp.task.track path, and the stable
// server-derived local origin otherwise. Every server-initiated compensation of
// a TRACKED task chains from this — never from a client request identity.
func taskOriginBinding(tenant string, rec TaskRecord) sdk.EvidenceBinding {
	if rec.Origin.Valid() {
		return rec.Origin
	}
	return deriveTaskLocalOriginBinding(tenant, rec)
}

// deriveTaskCancelCompensationBinding derives the SERVER-INITIATED
// compensating tasks/cancel of a task the gateway refuses to govern (admission
// denial, ledger cap, revoked tool, refused registration): a child of the
// operation that surfaced the task, bound to a stable reason CLASS.
// Deterministic on purpose: a same-parent duplicate replays instead of
// re-canceling (a claim never re-forwards).
//
// Round-2 N-03: the record GENERATION binds into both halves. A compensation is
// an actuation on one concrete registration instance; two records that merely
// share a textual identifier are different effects and must never share an
// operation identity.
func deriveTaskCancelCompensationBinding(tenant string, parent sdk.EvidenceBinding, taskID, generation, reasonClass string) sdk.EvidenceBinding {
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(evidenceLPDigest(
			mcpTaskCompensationOperationDomainV1,
			tenant,
			string(parent.OperationID),
			taskID,
			generation,
		)),
		EffectDigest: sdk.EffectDigest(evidenceLPDigest(
			mcpTaskCompensationEffectDomainV1,
			tenant,
			string(parent.EffectDigest),
			taskID,
			generation,
			reasonClass,
		)),
	}
}

// deriveTaskSweepBinding derives one kill-switch sweep cancellation.
//
// The OperationID is DETERMINISTIC in the task's cancellation-attempt
// GENERATION (review round-1 F-01). The first implementation minted a fresh
// RANDOM id per loop pass; that does not enforce the single-use-effect
// invariant, it evades it — two concurrent sweeps, or a sweep after an
// `unknown`/unsettled outcome, obtained two fresh claims and emitted the same
// logical cancellation twice. The generation comes from the ledger's atomic
// per-task cancel intent (taskLedger.beginCancelAttempt), which advances ONLY
// when the previous attempt provably emitted nothing or durably settled
// not_sent/blocked — so a re-attempt is a new operation exactly when the frozen
// law permits one, and is refused as a replay otherwise.
//
// The EffectDigest is the attempt-INDEPENDENT identity of the logical effect and
// binds everything the actuator resolves (F-11): tenant, the upstream
// descriptor, the canonical owner, the task target and the stable reason class.
// Free reason text never binds.
//
// Round-2 N-03: BOTH halves bind the record GENERATION. Without it, attempt 1
// against a task that expired and attempt 1 against the REPLACEMENT task that
// reused the identifier derive the same durable operation identity — so after a
// process restart (in-memory ledger gone, evidence journal intact) the
// replacement's emergency cancellation would be refused as a replay of the old
// task's, fail-open by omission.
func deriveTaskSweepBinding(tenant, upstreamDescriptor string, owner taskOwner, taskID, generation, reasonClass string, attempt uint64) sdk.EvidenceBinding {
	ownerDigest := owner.digest()
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(evidenceLPDigest(
			mcpTaskSweepOperationDomainV1,
			tenant,
			upstreamDescriptor,
			ownerDigest,
			taskID,
			generation,
			reasonClass,
			"attempt:"+strconv.FormatUint(attempt, 10),
		)),
		EffectDigest: sdk.EffectDigest(evidenceLPDigest(
			mcpTaskSweepEffectDomainV1,
			tenant,
			upstreamDescriptor,
			ownerDigest,
			taskID,
			generation,
			reasonClass,
		)),
	}
}
