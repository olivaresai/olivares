// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// D-09 durable-apply outcomes. All three are STATE/POLICY denials — NOT evidence faults
// (the frozen sdk.EvidenceFault taxonomy stays closed; sdk/evidence.go). The console maps
// each to a 4xx and re-prompts the operator with the current durable state.
var (
	// ErrPinDriftChanged: the tool's LIVE drift fingerprint no longer equals the
	// ExpectedDriftFingerprint the operator reviewed — the approval is stale (the tool
	// rug-pulled AGAIN between the GET and the durable write). The D-09 CAS threads the
	// reviewed fingerprint into the write so a plain "pin whatever the read returned" can
	// never legitimate a definition the operator never actually saw live. 409, no write.
	ErrPinDriftChanged = errors.New("tool-pin: drift changed since review; re-review required")
	// ErrPinVersionConflict: the durable pin row moved to a newer version than
	// ExpectedVersion (a concurrent operator write won the CAS). 409, no write.
	ErrPinVersionConflict = errors.New("tool-pin: state version conflict; refetch and retry")
	// ErrPinReplay: the SAME OperationID was already recorded with a DIFFERENT EffectDigest
	// — a rebind/replay of an idempotency identity (sdk.FailureReplay). 409, no write.
	ErrPinReplay = errors.New("tool-pin: operation id replay with a different effect")
)

// ToolPinChange is one operator pin mutation the durable applier must apply transactionally
// and idempotently. It carries the frozen-contract identity (OperationID + EffectDigest, so
// a retry with the same binding returns the original outcome and a different EffectDigest is
// a replay) and the compare-and-swap preconditions (ExpectedVersion for every write;
// ExpectedDriftFingerprint additionally for a from_drift approve, compared against the
// DURABLE row inside the apply transaction — never an in-memory snapshot). EvidenceRef binds
// the evidence-intent the caller anchored for THIS operation before dispatch.
type ToolPinChange struct {
	OperationID              string
	EffectDigest             string
	Server                   string
	Tool                     string
	Action                   string // "approve" | "unpin"
	DesiredFingerprint       string // approve: the fingerprint to pin; unpin: ""
	ExpectedVersion          int64
	ExpectedDriftFingerprint string // from_drift CAS precondition ("" = not a from_drift approve)
	EvidenceRef              string
}

// ToolPinApplyResult is the durable applier's answer. AppliedState is the post-apply
// apply_state ("pending" while an async apply/settle is outstanding, "applied" once the
// verifier will honor it); the caller returns it in the 202 body so the console polls GET.
type ToolPinApplyResult struct {
	OperationID  string
	StateVersion int64
	AppliedState string
}

// ToolFingerprint computes a stable, minimal-data cryptographic fingerprint of
// a tool definition: SHA-256 over a canonical representation of name +
// description + inputSchema + annotations. The fingerprint changes when ANY
// part of the definition changes — a rug-pull that mutates the description or
// schema while keeping the version-string constant is detected. The raw
// definition text is NEVER persisted (docs/SECURITY-HARDENING.md); only the hash.
//
// Stability contract: the same (name, description, inputSchema, annotations)
// always produces the same fingerprint across builds and platforms (canonical
// JSON keys are sorted via Go's json.Marshal map-key ordering since Go 1.12,
// applied recursively to all nested maps; whitespace is normalized). A change
// to this function invalidates all existing pins and must bump the version prefix.
func ToolFingerprint(t Tool) string {
	h := sha256.New()
	h.Write([]byte("toolpin-v1\n"))
	h.Write([]byte(strings.TrimSpace(t.Name)))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(t.Description)))
	h.Write([]byte{0})
	h.Write(canonicalJSON(t.InputSchema))
	h.Write([]byte{0})
	h.Write(canonicalAnnotations(t.Annotations))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON normalizes a raw JSON value for stable hashing: roundtrip
// through any so that Go's json.Marshal map-key sort (alphabetical, stable
// since Go 1.12) applies recursively to every nested object. Nil/empty input
// hashes as empty. An unparseable input is hashed as-is (deterministic per
// input, not silently dropped).
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw // unparseable: hash as-is (deterministic per input)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// canonicalAnnotations serializes the non-zero annotation fields to a stable
// map for hashing. Only present fields are included so an absent bool and an
// explicit false produce different hashes. Returns nil when the pointer is nil
// or every field is at its zero value.
func canonicalAnnotations(a *ToolAnnotations) []byte {
	if a == nil {
		return nil
	}
	m := map[string]any{}
	if a.Title != "" {
		m["title"] = a.Title
	}
	if a.ReadOnlyHint != nil {
		m["readOnlyHint"] = *a.ReadOnlyHint
	}
	if a.DestructiveHint != nil {
		m["destructiveHint"] = *a.DestructiveHint
	}
	if a.IdempotentHint != nil {
		m["idempotentHint"] = *a.IdempotentHint
	}
	if a.OpenWorldHint != nil {
		m["openWorldHint"] = *a.OpenWorldHint
	}
	if len(m) == 0 {
		return nil
	}
	out, _ := json.Marshal(m) // map keys sorted alphabetically by json.Marshal
	return out
}

// ToolPinVerifier is the seam: it verifies that a tool's current
// fingerprint matches a previously approved pin BEFORE a tools/call is
// forwarded. Deny-closed: a mismatch (or an error) blocks the call. The
// enterprise implementation stores pins, compares fingerprints, and re-pins
// on operator approval. The community build wires nil (no pin verification,
// tools/call proceeds as before — the gate is additive).
//
// Honesty: pin verification covers only what the PEP introspects. A server
// that serves different definitions per-caller or outside the PEP is not
// pinned. The detection raises the cost of a rug-pull and leaves evidence;
// it does not prove the server is trustworthy (contract).
type ToolPinVerifier interface {
	// Verify checks the tool's current fingerprint against the stored pin.
	// Returns (allowed=true) when the pin matches or no pin exists yet (first
	// seen). Returns (allowed=false, reason) on a mismatch (rug-pull detected).
	// An error is treated as deny-closed by the caller.
	Verify(ctx context.Context, server, toolName, fingerprint string) (allowed bool, reason string, err error)
	// RecordPin stores the fingerprint as the approved pin for a tool (called
	// after a successful tools/list introspection or an operator re-pin).
	RecordPin(ctx context.Context, server, toolName, fingerprint string) error
}

// PinAction (deny/hitl/flag) was REMOVED: it was declared and never
// referenced anywhere — a mismatch has always denied. Declared-but-unwired
// configuration is the dishonesty this codebase refuses; if a hitl/flag
// response is ever built, it returns WITH its wiring and tests.

// ToolPinAttestation is the stable identity of the APPROVED pin a verifier
// checked a call against (round-2): the approved definition fingerprint
// (never the call-time hash — that is a digest of the very params the evidence
// EffectDigest already binds) and a monotonic pin version. Both are bound into
// the EffectDigest so a re-pin (operator-approved definition change) changes the
// effect identity of a keyed retry instead of silently replaying.
type ToolPinAttestation struct {
	Fingerprint string
	Version     string
}

// ToolPinVerifyAttestation is the ATOMIC result of VerifyAndAttest: the
// verification decision AND the approved-pin identity it was made against,
// produced under ONE lock/snapshot of the pin store. Attested=false means no pin
// existed at decision time (first-use TOFU); Attested=true REQUIRES non-empty
// Fingerprint and Version (the caller denies otherwise — an attested identity
// with missing fields would misstate the evidence).
type ToolPinVerifyAttestation struct {
	Allowed  bool
	Reason   string
	Attested bool
	Pin      ToolPinAttestation
}

// ToolPinVerifyAttestor is an OPTIONAL ToolPinVerifier capability (round-3,
// asserted like ToolPinAdmin): the atomic decision+attestation. It replaces the
// removed two-step ApprovedPin/Pins() reads, which were a TOCTOU: a re-pin A→B
// landing between Verify and a separate attestation read authorized under A but
// bound B into the evidence — false evidence. An implementation MUST produce the
// decision and the attestation under the SAME lock or store snapshot, so the
// identity bound is exactly the identity that authorized.
//
// A verifier WITHOUT this capability binds pin POSTURE only ("verified" with
// explicit-absent identity markers): honest absence, never a racy re-read.
// Enterprise-overlay debt (stage-7 re-pin, like the GateAuditor break): the real
// store verifier implements VerifyAndAttest over its pin-row snapshot.
type ToolPinVerifyAttestor interface {
	VerifyAndAttest(ctx context.Context, server, toolName, fingerprint string) (ToolPinVerifyAttestation, error)
}

// PinSnapshot is the durable form of one tool pin, including any CURRENT drift
// (a live fingerprint that no longer matches the pin). Server carries the
// verifier's server key — the resource server passes its bound tenant there, so
// a snapshot can never address another tenant's pins. Fingerprints are hashes
// of tool material, never raw definitions (docs/SECURITY-HARDENING.md).
//
// ⛔ VERSION IS PART OF THE SNAPSHOT BECAUSE A CAS PRECONDITION A CLIENT CANNOT
// BOOTSTRAP IS NOT A PRECONDITION — IT IS A CLOSED DOOR.
//
// ApplyPinChange REQUIRES ExpectedVersion on every write. Until this struct carried
// no version, so the GET — the only read a client has before it has ever written —
// published none, and a client's FIRST write could never satisfy the precondition: 400,
// by construction, for every caller that had not already written.
//
// Stated exactly, because the stronger version of this sentence is false and was here
// first: the 202 body of a SUCCESSFUL write does return the new counter as `version`
// (modules/capabilities/toolpins.go), so a client that had already got one write through
// could carry it forward. That path could never start. The defect was the bootstrap, not
// the whole lifecycle — corrected after the the model contrast refuted the original
// claim that the value was "never handed back".
//
// ⚠ AND WHAT THIS FIELD IS NOT: an ENFORCED identity with ApplyPinChange's counter.
// ToolPinPersistence and ToolPinAdmin are separate seams with no typed link, so an
// implementation can fill this from one source and CAS against another, and a pre
// implementer still compiles and silently reports zero. Treat it as an OBLIGATION on the
// implementer — Pins() must publish the counter ApplyPinChange compares — not as a
// guarantee the types provide. Making it a guarantee needs one durable authority for both
// (see the residue note in sessions/).
//
// Do NOT confuse it with ToolPinAttestation.Version: that is a STRING bound into an
// evidence EffectDigest to identify WHICH approved definition authorized a call. This is
// the int64 compare-and-swap counter of the durable pin row.
type PinSnapshot struct {
	Server      string
	Tool        string
	Fingerprint string
	PinnedAt    time.Time
	UpdatedAt   time.Time
	PinCount    int
	// Version is the durable row's optimistic-concurrency counter. An implementation MUST
	// publish here the counter its ApplyPinChange compares and returns as
	// ToolPinApplyResult.StateVersion; nothing in these types enforces that, and getting
	// it wrong makes a legitimate write lose the CAS or a stale precondition validate
	// against the wrong counter. A client reads it here and echoes it as
	// expected_version, so a concurrent operator write loses the CAS instead of being
	// silently overwritten. The engine-side persistence sources it from the store's
	// injected `version` base column (a module descriptor may not declare one: it is
	// reserved), where a freshly created row starts at 1.
	Version          int64
	DriftFingerprint string    // "" when the tool currently matches its pin
	DriftAt          time.Time // zero when no drift is recorded
}

// ToolPinAdmin is the optional operator surface a pin verifier may implement
//: list current pins (drift included) and actuate the two operator
// verbs — approve (RecordPin) and revoke (Unpin). The console/CLI admin route
// type-asserts the verifier to this; a community build has no verifier and the
// surface answers "enterprise pending" honestly.
type ToolPinAdmin interface {
	// Pins returns the durable form of every pin, current drift included.
	Pins() []PinSnapshot
	// RecordPin approves fingerprint as the pin for (server, tool). It is the
	// UNCONDITIONAL variant (no CAS precondition), used by the verify-path TOFU auto-pin
	// only; the OPERATOR approve/unpin path uses ApplyPinChange so a concurrent rug-pull or
	// racing operator write cannot be silently legitimated.
	RecordPin(ctx context.Context, server, toolName, fingerprint string) error
	// Unpin revokes the pin; the next use is first-use again. Retained for the
	// unconditional revoke; the operator path routes through ApplyPinChange (action=unpin)
	// so the revoke is evidence-gated and CAS-guarded.
	Unpin(ctx context.Context, server, toolName string) error
	// ApplyPinChange is the D-09 durable applier: it applies ch idempotently by
	// OperationID and CAS-guarded on ExpectedVersion (and, for a from_drift approve,
	// ExpectedDriftFingerprint compared against the durable row). The enterprise
	// implementation MUST, in ONE store transaction: dedup by OperationID (same digest →
	// original outcome; different → ErrPinReplay), CAS the durable pin row, commit the
	// evidence-intent + desired-'pending' state + outbox together, and only after a
	// separate settle flip the effective state; the verifier denies the tool while the
	// apply is pending or divergent (never restoring the old snapshot). A CAS miss returns
	// ErrPinDriftChanged / ErrPinVersionConflict and writes nothing (deny-closed).
	ApplyPinChange(ctx context.Context, ch ToolPinChange) (ToolPinApplyResult, error)
}

// ToolPinPersistence is the seam that gives pins a life beyond the
// process: without it a restart clears every pin and the next tools/list
// re-legitimates a rug-pull via TOFU. The engine provides the storage (the
// data belongs to the tenant); the enterprise verifier remains the only
// component that ENFORCES pins — a community build stores nothing because its
// verifier is nil, so the licensing posture is unchanged.
//
// Write-through contract: an operator RecordPin must fail loudly when the
// write fails (the approval did not durably happen); the verify-path auto-pin
// treats persistence as availability, not authorization — the durable TOFU
// audit event remains the gate.
type ToolPinPersistence interface {
	// Load returns every stored pin snapshot across tenants (boot-time rebuild).
	Load(ctx context.Context) ([]PinSnapshot, error)
	// UpsertPin stores or updates the snapshot keyed by (Server, Tool).
	UpsertPin(ctx context.Context, snap PinSnapshot) error
	// DeletePin removes the pin for (server, tool); absent is not an error.
	DeletePin(ctx context.Context, server, tool string) error
}

// toolCallFingerprint computes a lightweight fingerprint for a tools/call
// request from the tool name and the raw request params. This is the
// call-time signal passed to the PinVerifier.Verify; the full definition
// fingerprint (ToolFingerprint) is computed at introspection time and stored
// as the pin by the enterprise implementation.
func toolCallFingerprint(toolName string, params []byte) string {
	h := sha256.Sum256(append([]byte("toolcall-v1\n"+toolName+"\n"), params...))
	return hex.EncodeToString(h[:])
}
