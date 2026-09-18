// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The attempt lifecycle — types, exact canonical framing, digests and typed
// errors for legacy import, the durable activation frontier, private T0 admission
// and their shared group/hold reader. No dispatch grant, activation operation,
// scheduler or T1–T4 operation is materialized. New/boot retains a nil verifier.
//
// The codec here is EXPLICIT and closed. The durable format is never
// json.Marshal of these Go structs: monetary and version quantities travel as
// canonical decimal ASCII strings, every optional value carries an explicit
// presence marker, and decoding rejects an unknown field instead of ignoring it.
// A float64 never touches a monetary value on either the digest path or the
// persistence path.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// Identities and closed vocabularies
// -----------------------------------------------------------------------------

// AttemptRef is the stable external accounting identity of one attempt: exactly
// 32 lowercase hex characters, never all zero. It is NOT a bearer permit — holding
// one authorizes nothing (see the authority note on AttemptEvidenceVerifier).
type AttemptRef string

// Digest is a SHA-256 in exactly 64 lowercase hex characters.
type Digest string

// AttemptPhase is the parent's lifecycle phase (contract enums.phase).
type AttemptPhase string

// The phase vocabulary. Writers produce outcome_unknown (import) and prepared (T0);
// the rest are declared because the readers must classify a row they did not write
// rather than guess at it.
const (
	phasePrepared         AttemptPhase = "prepared"
	phaseDispatchPossible AttemptPhase = "dispatch_possible"
	phaseOutcomeUnknown   AttemptPhase = "outcome_unknown"
	phaseUsageKnown       AttemptPhase = "usage_known"
	phaseCostReady        AttemptPhase = "cost_ready"
	phaseSettled          AttemptPhase = "settled"
	phaseReleased         AttemptPhase = "released"
	phaseExpired          AttemptPhase = "expired"
)

// heldPhase reports whether a phase still holds its reservation children, i.e.
// whether its targets are money the tenant has not been released from
// (contract enums.held_phases). Terminal phases hold nothing.
func heldPhase(p AttemptPhase) bool {
	switch p {
	case phasePrepared, phaseDispatchPossible, phaseOutcomeUnknown, phaseUsageKnown, phaseCostReady:
		return true
	}
	return false
}

// knownPhase reports whether p is in the closed vocabulary at all. An unknown
// phase is corruption or a writer this binary does not have; either way it is an
// integrity fault, never "not held".
func knownPhase(p AttemptPhase) bool {
	switch p {
	case phasePrepared, phaseDispatchPossible, phaseOutcomeUnknown, phaseUsageKnown,
		phaseCostReady, phaseSettled, phaseReleased, phaseExpired:
		return true
	}
	return false
}

// FactState classifies one attribution dimension (contract enums.fact_state).
type FactState string

// The fact-state vocabulary.
const (
	factKnown         FactState = "known"
	factMissing       FactState = "missing"
	factNotApplicable FactState = "not_applicable"
)

// Binding statuses (contract enums.binding_status).
const (
	bindingResolved      = "resolved"
	bindingLegacyUnbound = "legacy_unbound"
)

// Accounting-basis kinds (contract enums.accounting_basis).
const (
	basisAttemptAdmission = "attempt_admission"
	basisLegacyEvidenced  = "legacy_evidenced"
	basisLegacyUnknown    = "legacy_unknown"
)

// Publication states (contract enums.publication_state). This cut writes only
// "none": an import produces no cost sample and nothing to publish.
const (
	publicationNone      = "none"
	publicationPending   = "pending"
	publicationAttempted = "attempted"
)

// Lifecycle scope states (contract enums.lifecycle_scope_state). An ABSENT row is
// the third, unnamed state — legacy/inactive — and it is established only by a
// successful lookup that found nothing.
const (
	lifecycleQuiescing = "quiescing"
	lifecycleActive    = "active"
)

// attributionDimensions is the closed, ordered set of attribution dimensions
// (contract attribution_dimensions). Every entry is REQUIRED in a canonical
// binding: a dimension that is not known is represented explicitly as missing,
// because "absent from the map" and "known to be absent" are different facts and
// only one of them can be audited.
var attributionDimensions = []string{
	"provider", "model", "agent", "session", "team", "project", "workspace",
	"api_key", "actor", "identity", "routine", "cost_center", "user_group",
	"agent_group", "service_tier", "context_window", "inference_geo", "gateway",
	"cost_type",
}

// Canonical framing domains (contract canonicalization.domains plus the import
// identity domain of keys.legacy_import).
const (
	domainBinding        = "olivares.finops.attempt-binding.v1"
	domainReservation    = "olivares.finops.attempt-reservation.v1"
	domainImportRequest  = "olivares.finops.import-request.v1"
	domainImportGroup    = "olivares.finops.import-group.v1"
	domainImportSnapshot = "olivares.finops.import-snapshot.v1"
	domainScopeFrontier  = "olivares.finops.lifecycle-frontier.v1"
	domainLegacyImportID = "olivares.finops.legacy-import.v1"
	// domainScopeProposal frames the proposed scope transition a Begin presents to
	// the verifier (EvidenceCheck.ProposedDigest). The contract fixes the field, not
	// a domain string for it; this one is declared here so the value cannot collide
	// with any other framing in the table above.
	domainScopeProposal = "olivares.finops.lifecycle-proposal.v1"
)

// Bounds (contract canonicalization.limits). Exceeding one is a typed refusal,
// never a truncation reported as complete.
const (
	maxEvidenceRefsPerOperation = 32
	maxDelegationRefs           = 32
	maxTargetsPerAttempt        = 256
	maxOpaqueRefBytes           = 512
	maxAttemptPayloadBytes      = 262144
	// maxFrontierPayloadBytes bounds the encoded activation frontier. It is the
	// SAME explicit byte bound the contract fixes for an attempt payload, chosen so
	// the two durable JSON documents this cut writes have one declared limit rather
	// than two arbitrary ones. What it buys, stated concretely so it can be
	// re-judged rather than inherited: a pending-group entry is a 36-character
	// handle, a 64-character digest and a decimal child count, ~140 bytes encoded,
	// so the bound admits roughly 1,800 pending legacy groups for one tenant.
	// EXCEEDING IT REFUSES THE WHOLE BEGIN. There is deliberately no chunking: a
	// frontier written in parts would be a census that is partly committed, which is
	// exactly the thing a frontier exists to make impossible.
	maxFrontierPayloadBytes = 262144
)

// -----------------------------------------------------------------------------
// Typed errors
// -----------------------------------------------------------------------------

// The closed error vocabulary this cut can produce (contract errors[]). Codes for
// operations that do not exist here are not declared: an unused code is a claim
// that something can happen, and nothing in this package can produce them.
const (
	errCodeInvalidAttempt          = "invalid_attempt"
	errCodeAttemptNotFound         = "attempt_not_found"
	errCodeAttemptIdentityConflict = "attempt_identity_conflict"
	errCodeLedgerIncomplete        = "ledger_incomplete"
	errCodeLedgerIndeterminate     = "ledger_indeterminate"
	errCodeArithmetic              = "arithmetic_unrepresentable"
	errCodeCapabilityUnavailable   = "capability_unavailable"
	errCodeStoreUnavailable        = "store_unavailable"
	errCodeWriteOutcomeUnknown     = "write_outcome_unknown"
	errCodeStaleAttempt            = "stale_attempt"
	errCodeOwnerMismatch           = "owner_mismatch"
	errCodeEvidenceIncomplete      = "evidence_incomplete"
	errCodeEvidenceNotAnchored     = "evidence_not_anchored"
	errCodeLifecycleAPIRequired    = "lifecycle_api_required"
	errCodeLegacyUnresolved        = "legacy_unresolved"
	errCodeBindingConflict         = "binding_conflict"
	errCodeImportConflict          = "import_conflict"
	errCodeLifecycleActivation     = "lifecycle_activation_required"
	errCodeDimensionRequired       = "dimension_required"
	errCodeBudgetDenied            = "budget_denied"
	errCodeConcurrencyExhausted    = "concurrency_exhausted"
)

// Retry classes (contract AttemptError.Retry).
const (
	retryNever            = "never"
	retryReadByIdentity   = "read_by_identity"
	retryWholeTransaction = "whole_transaction"
	retryAfterEvidence    = "after_evidence"
)

// attemptRetryClass is the contract's retry column, kept beside the codes rather
// than at each call site: a caller must not have to guess whether a refusal is
// worth repeating, and two call sites answering differently for one code would be
// the defect.
var attemptRetryClass = map[string]string{
	errCodeInvalidAttempt:          retryNever,
	errCodeAttemptNotFound:         retryNever,
	errCodeAttemptIdentityConflict: retryNever,
	errCodeLedgerIncomplete:        retryAfterEvidence,
	errCodeLedgerIndeterminate:     retryAfterEvidence,
	errCodeArithmetic:              retryNever,
	errCodeCapabilityUnavailable:   retryAfterEvidence,
	errCodeStoreUnavailable:        retryWholeTransaction,
	errCodeWriteOutcomeUnknown:     retryReadByIdentity,
	errCodeStaleAttempt:            retryReadByIdentity,
	errCodeOwnerMismatch:           retryNever,
	errCodeEvidenceIncomplete:      retryAfterEvidence,
	errCodeEvidenceNotAnchored:     retryAfterEvidence,
	errCodeLifecycleAPIRequired:    retryNever,
	errCodeLegacyUnresolved:        retryAfterEvidence,
	errCodeBindingConflict:         retryAfterEvidence,
	errCodeImportConflict:          retryNever,
	errCodeLifecycleActivation:     retryAfterEvidence,
	errCodeDimensionRequired:       retryAfterEvidence,
	errCodeBudgetDenied:            retryNever,
	errCodeConcurrencyExhausted:    retryReadByIdentity,
}

// AttemptError is the typed refusal of an attempt-lifecycle operation.
//
// Error() text is FIXED and derived only from the code: it never prints the cause,
// because these errors travel to seams that log them and the cause can carry a
// store's raw message. Unwrap still exposes the cause, so errors.Is against
// store.ErrConflict, store.ErrNotFound and store.ErrStoreUnavailable keeps working
// for the callers that already depend on it.
//
// MayHaveCommitted is the field that must never be inferred: it is true only for
// write_outcome_unknown, the one shape in which the transaction callback finished
// and the commit acknowledgement did not arrive.
type AttemptError struct {
	Code             string
	AttemptRef       *AttemptRef
	MayHaveCommitted bool
	Retry            string
	cause            error
}

// Error returns the safe fixed text for the code. It contains no raw error, no
// tenant data and no secret.
func (e *AttemptError) Error() string {
	if e == nil {
		return "finops: attempt error"
	}
	return "finops: attempt operation refused: " + e.Code
}

// Unwrap exposes the private cause for errors.Is/As without printing it.
func (e *AttemptError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// attemptErr builds a typed refusal, filling Retry from the contract table and
// MayHaveCommitted from the one code that carries it.
func attemptErr(code string, cause error) *AttemptError {
	return &AttemptError{
		Code:             code,
		Retry:            attemptRetryClass[code],
		MayHaveCommitted: code == errCodeWriteOutcomeUnknown,
		cause:            cause,
	}
}

// attemptErrRef is attemptErr naming the attempt the refusal is about.
func attemptErrRef(code string, ref AttemptRef, cause error) *AttemptError {
	e := attemptErr(code, cause)
	if ref != "" {
		r := ref
		e.AttemptRef = &r
	}
	return e
}

// attemptCode returns the code of an AttemptError anywhere in err's chain, or "".
// It is how a caller (and a test) classifies a refusal without string matching.
func attemptCode(err error) string {
	var ae *AttemptError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// storeErr maps a store failure observed BEFORE any write of this operation into
// the contract's store_unavailable, preserving errors.Is on the cause.
func storeErr(err error) *AttemptError { return attemptErr(errCodeStoreUnavailable, err) }

// -----------------------------------------------------------------------------
// Identity validation
// -----------------------------------------------------------------------------

// validAttemptRef reports whether r is exactly 32 lowercase hex characters and
// not all zero. The all-zero value is refused because it is what an uninitialized
// field encodes to, and an identity that a bug can produce by accident is not an
// identity.
func validAttemptRef(r AttemptRef) bool {
	if len(r) != 32 {
		return false
	}
	allZero := true
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
		if c != '0' {
			allZero = false
		}
	}
	return !allZero
}

// validDigest reports whether d is exactly 64 lowercase hex characters. Unlike an
// AttemptRef, the all-zero digest is not special-cased: it is a legitimate (if
// astronomically unlikely) hash value, and refusing it would be a rule about the
// hash rather than about the format.
func validDigest(d Digest) bool {
	if len(d) != 64 {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// Canonical framing
// -----------------------------------------------------------------------------

// canonWriter builds the contract's canonical byte framing: every field is its
// UTF-8 byte length as a big-endian uint64 followed by its exact bytes, in
// DECLARATION ORDER; a vector is preceded by a raw uint64 item count; an optional
// value is preceded by a single presence byte 0 or 1.
//
// The framing is what makes the digests unambiguous: without the length prefix,
// two different field splits could produce the same byte stream, so a digest could
// be replayed against a payload it does not describe.
type canonWriter struct{ buf bytes.Buffer }

// field writes one length-framed field.
func (w *canonWriter) field(b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	w.buf.Write(n[:])
	w.buf.Write(b)
}

// str writes a string field.
func (w *canonWriter) str(s string) { w.field([]byte(s)) }

// num writes a signed quantity as canonical decimal ASCII: "0", or an optional
// minus followed by a nonzero leading digit. strconv.FormatInt produces exactly
// that form — no plus sign, no exponent, no leading zero, no negative zero — and
// no float ever reaches this path.
func (w *canonWriter) num(v int64) { w.str(strconv.FormatInt(v, 10)) }

// count writes a vector's item count as a RAW big-endian uint64 (not framed): the
// count is a header for the items that follow, not a field of its own.
func (w *canonWriter) count(n int) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	w.buf.Write(b[:])
}

// presence writes an optional value's single presence byte.
func (w *canonWriter) presence(p bool) {
	if p {
		w.buf.WriteByte(1)
		return
	}
	w.buf.WriteByte(0)
}

// boolean writes a bool as the canonical decimal quantity 0 or 1. The contract
// fixes the encoding of quantities and of optional presence, and leaves plain
// booleans to the implementation; encoding them as the same canonical decimal
// values keeps ONE numeric form in the framing instead of introducing a second
// spelling ("true"/"false") that would have to be kept in step with it.
func (w *canonWriter) boolean(b bool) {
	if b {
		w.num(1)
		return
	}
	w.num(0)
}

// optNum writes an optional signed quantity.
func (w *canonWriter) optNum(v *int64) {
	w.presence(v != nil)
	if v != nil {
		w.num(*v)
	}
}

// ts writes a timestamp as its canonical UTC text.
func (w *canonWriter) ts(t model.Timestamp) { w.str(t.String()) }

// optTS writes an optional timestamp.
func (w *canonWriter) optTS(t *model.Timestamp) {
	w.presence(t != nil)
	if t != nil {
		w.ts(*t)
	}
}

// optDigest writes an optional digest.
func (w *canonWriter) optDigest(d *Digest) {
	w.presence(d != nil)
	if d != nil {
		w.str(string(*d))
	}
}

// bytes returns the framed stream.
func (w *canonWriter) bytes() []byte { return w.buf.Bytes() }

// canonDigest frames a domain and a body and returns the SHA-256 as 64 lowercase
// hex characters. The domain goes FIRST, as an ordinary framed field, so two
// payloads with identical bodies under different domains cannot collide.
func canonDigest(domain string, body func(w *canonWriter)) Digest {
	var w canonWriter
	w.str(domain)
	body(&w)
	sum := sha256.Sum256(w.bytes())
	return Digest(hex.EncodeToString(sum[:]))
}

// canonBytes frames a domain and a body and returns the exact stream, for the
// places that must compare BYTES rather than a digest (the import replay keeps the
// original request bytes so a collision and a conflict stay distinguishable).
func canonBytes(domain string, body func(w *canonWriter)) []byte {
	var w canonWriter
	w.str(domain)
	body(&w)
	return w.bytes()
}

// -----------------------------------------------------------------------------
// Contract shapes
// -----------------------------------------------------------------------------

// EvidenceRef is a bounded opaque locator for a durable fact. It never carries
// content or a secret: Ref is a locator, and the digest is what binds it.
type EvidenceRef struct {
	Kind     string // audit_event | store_row | operator_statement
	Ref      string // bounded opaque locator, never content or secret
	Digest   Digest
	AuditSeq int64 // 0 for non-audit evidence
	Version  int64 // 0 if not a versioned store row
}

// Evidence kinds (contract EvidenceRef.Kind).
const (
	evidenceAuditEvent        = "audit_event"
	evidenceStoreRow          = "store_row"
	evidenceOperatorStatement = "operator_statement"
)

func (e EvidenceRef) canon(w *canonWriter) {
	w.str(e.Kind)
	w.str(e.Ref)
	w.str(string(e.Digest))
	w.num(e.AuditSeq)
	w.num(e.Version)
}

// validEvidenceRef checks shape only. It is NOT a claim that the referenced fact
// exists, is anchored or says what the caller thinks: that is the verifier's work,
// and conflating the two would let a well-formed reference pass for proof.
func (e EvidenceRef) valid() bool {
	switch e.Kind {
	case evidenceAuditEvent, evidenceStoreRow, evidenceOperatorStatement:
	default:
		return false
	}
	if e.Ref == "" || len(e.Ref) > maxOpaqueRefBytes {
		return false
	}
	if !validDigest(e.Digest) {
		return false
	}
	return e.AuditSeq >= 0 && e.Version >= 0
}

// canonEvidenceRefs frames a bounded evidence vector in the caller's order.
func canonEvidenceRefs(w *canonWriter, refs []EvidenceRef) {
	w.count(len(refs))
	for _, r := range refs {
		r.canon(w)
	}
}

// validEvidenceRefs checks the per-operation bound and every element.
func validEvidenceRefs(refs []EvidenceRef) bool {
	if len(refs) > maxEvidenceRefsPerOperation {
		return false
	}
	for _, r := range refs {
		if !r.valid() {
			return false
		}
	}
	return true
}

// evidenceDigests returns the sorted evidence digests, for the framings that
// commit to WHICH facts were presented without committing to their locators.
func evidenceDigests(refs []EvidenceRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, string(r.Digest))
	}
	sortStrings(out)
	return out
}

// AttributionFact is one attribution dimension's state and evidence.
type AttributionFact struct {
	State             FactState
	Values            []string
	Evidence          []EvidenceRef
	NotApplicableRule string
}

func (a AttributionFact) canon(w *canonWriter) {
	w.str(string(a.State))
	w.count(len(a.Values))
	for _, v := range a.Values {
		w.str(v)
	}
	canonEvidenceRefs(w, a.Evidence)
	w.str(a.NotApplicableRule)
}

// AttemptSubject is the authenticated principal side of a binding. Every field is
// empty on a legacy import: the historical subject is not reconstructed, and the
// import's own authority does not stand in for it.
type AttemptSubject struct {
	ActorRef, ActorKind            string
	UserID, CredentialID           model.ID
	AgentIdentity, SessionIdentity string
	SessionWorkspaceID             model.ID
	SessionRunRef                  string
	SessionFence                   int64
	DelegationRefs                 []EvidenceRef
	UntrustedSessionRef            string
}

func (s AttemptSubject) canon(w *canonWriter) {
	w.str(s.ActorRef)
	w.str(s.ActorKind)
	w.str(s.UserID.String())
	w.str(s.CredentialID.String())
	w.str(s.AgentIdentity)
	w.str(s.SessionIdentity)
	w.str(s.SessionWorkspaceID.String())
	w.str(s.SessionRunRef)
	w.num(s.SessionFence)
	canonEvidenceRefs(w, s.DelegationRefs)
	w.str(s.UntrustedSessionRef)
}

// AttemptDestination is the resolved destination side of a binding.
type AttemptDestination struct {
	ProfileRef, ProfileRevision                 string
	ProviderRef, ModelRef                       string
	Action, Protocol, AdapterID, AdapterVersion string
	Surface, InferenceGeo, CredentialAudience   string
	AuthScheme                                  string
	EndpointDigest, TransportDigest             Digest
	PolicyID                                    model.ID
	PolicyVersion                               int64
	PolicySpecDigest, ProxyPolicyDigest         Digest
	PreparedDigest                              Digest
	PreparedBytes, MaxOutputTokens              int64
	MaxRequestBytes, MaxResponseBytes           int64
	TimeoutNanos                                int64
}

func (d AttemptDestination) canon(w *canonWriter) {
	w.str(d.ProfileRef)
	w.str(d.ProfileRevision)
	w.str(d.ProviderRef)
	w.str(d.ModelRef)
	w.str(d.Action)
	w.str(d.Protocol)
	w.str(d.AdapterID)
	w.str(d.AdapterVersion)
	w.str(d.Surface)
	w.str(d.InferenceGeo)
	w.str(d.CredentialAudience)
	w.str(d.AuthScheme)
	w.str(string(d.EndpointDigest))
	w.str(string(d.TransportDigest))
	w.str(d.PolicyID.String())
	w.num(d.PolicyVersion)
	w.str(string(d.PolicySpecDigest))
	w.str(string(d.ProxyPolicyDigest))
	w.str(string(d.PreparedDigest))
	w.num(d.PreparedBytes)
	w.num(d.MaxOutputTokens)
	w.num(d.MaxRequestBytes)
	w.num(d.MaxResponseBytes)
	w.num(d.TimeoutNanos)
}

// EstimateBasis is the accounted estimate of a new attempt. On a legacy import it
// is empty: the historical estimate is not known and is not invented from the
// reserved amount, which is a different fact.
type EstimateBasis struct {
	AmountMicroUSD   int64
	Method, Revision string
	InputBound       *int64
	OutputBound      *int64
	RateRefs         []EvidenceRef
	PriceDigest      Digest
	QualificationRef EvidenceRef
}

func (e EstimateBasis) canon(w *canonWriter) {
	w.num(e.AmountMicroUSD)
	w.str(e.Method)
	w.str(e.Revision)
	w.optNum(e.InputBound)
	w.optNum(e.OutputBound)
	canonEvidenceRefs(w, e.RateRefs)
	w.str(string(e.PriceDigest))
	e.QualificationRef.canon(w)
}

// ResolvedEntityIDs are the canonical entity references frozen at admission.
// Empty means legitimately unresolved; an import never fabricates one.
type ResolvedEntityIDs struct {
	ProviderID, ModelID, SessionID, AgentID model.ID
}

func (r ResolvedEntityIDs) canon(w *canonWriter) {
	w.str(r.ProviderID.String())
	w.str(r.ModelID.String())
	w.str(r.SessionID.String())
	w.str(r.AgentID.String())
}

// AttemptBinding is the pre-effect binding of an attempt.
type AttemptBinding struct {
	Status          string
	RequestRef      AttemptRef
	PredecessorRef  *AttemptRef
	Subject         AttemptSubject
	Entities        ResolvedEntityIDs
	Destination     AttemptDestination
	Attribution     map[string]AttributionFact
	Estimate        EstimateBasis
	ApplySeatLimits bool
	AuthorityRefs   []EvidenceRef
}

func (b AttemptBinding) canon(w *canonWriter) {
	w.str(b.Status)
	w.str(string(b.RequestRef))
	w.presence(b.PredecessorRef != nil)
	if b.PredecessorRef != nil {
		w.str(string(*b.PredecessorRef))
	}
	b.Subject.canon(w)
	b.Entities.canon(w)
	b.Destination.canon(w)
	// The attribution map is framed over the CLOSED dimension list in its declared
	// order, not over the map's keys: a missing key is framed as the explicit
	// missing fact it means, so two bindings that disagree only about whether a
	// dimension was recorded cannot produce the same digest.
	w.count(len(attributionDimensions))
	for _, dim := range attributionDimensions {
		w.str(dim)
		f, ok := b.Attribution[dim]
		if !ok {
			f = AttributionFact{State: factMissing}
		}
		f.canon(w)
	}
	b.Estimate.canon(w)
	w.boolean(b.ApplySeatLimits)
	canonEvidenceRefs(w, b.AuthorityRefs)
}

// unboundAttribution is the attribution of a legacy import: every dimension
// EXPLICITLY missing. It is not an empty map — the difference between "we did not
// record this dimension" and "this dimension is not known" is the whole point of
// the fact-state vocabulary.
func unboundAttribution() map[string]AttributionFact {
	out := make(map[string]AttributionFact, len(attributionDimensions))
	for _, d := range attributionDimensions {
		out[d] = AttributionFact{State: factMissing}
	}
	return out
}

// legacyUnboundBinding is the binding of an imported hold: status legacy_unbound,
// the synthetic request ref (import lineage only, never a dispatch identity), and
// explicit absences everywhere else.
func legacyUnboundBinding(ref AttemptRef) AttemptBinding {
	return AttemptBinding{
		Status:      bindingLegacyUnbound,
		RequestRef:  ref,
		Attribution: unboundAttribution(),
	}
}

// TargetSnapshot is one reservation child as the attempt records it. The nullable
// policy facts stay nil on an import: an unknown historical fact is never filled
// in from the policy as it stands today.
type TargetSnapshot struct {
	ChildID                model.ID
	PolicyID               model.ID
	PolicyKind, Dimension  string
	ScopeKey, Period       string
	PolicyVersion          *int64
	PolicySpecDigest       *Digest
	PeriodStart, PeriodEnd model.Timestamp
	HasPeriodBounds        bool
	LimitMicroUSD          *int64
	StaticReservedMicroUSD *int64
	Action                 string
	Membership             []EvidenceRef
}

func (t TargetSnapshot) canon(w *canonWriter) {
	w.str(t.ChildID.String())
	w.str(t.PolicyID.String())
	w.str(t.PolicyKind)
	w.str(t.Dimension)
	w.str(t.ScopeKey)
	w.str(t.Period)
	w.optNum(t.PolicyVersion)
	w.optDigest(t.PolicySpecDigest)
	w.ts(t.PeriodStart)
	w.ts(t.PeriodEnd)
	w.boolean(t.HasPeriodBounds)
	w.optNum(t.LimitMicroUSD)
	w.optNum(t.StaticReservedMicroUSD)
	w.str(t.Action)
	canonEvidenceRefs(w, t.Membership)
}

// AccountingBasis records what the accounting instant MEANS, so a NULL instant is
// a stated fact rather than a gap: legacy_unknown is "history does not establish
// it", not "it happened at import time".
type AccountingBasis struct {
	Kind     string
	Evidence []EvidenceRef
}

func (a AccountingBasis) canon(w *canonWriter) {
	w.str(a.Kind)
	canonEvidenceRefs(w, a.Evidence)
}

// LegacyChildVersion is one requested child and the version the caller believes it
// is at. The version is what makes the import's OCC exact.
type LegacyChildVersion struct {
	ID      model.ID
	Version int64
}

// ImportLegacyHoldRequest is the caller's complete import request. Its canonical
// bytes are stored verbatim: an exact replay is decided by comparing them, so a
// collision and a conflict never look alike.
type ImportLegacyHoldRequest struct {
	Handle             model.ID
	AccountingAt       *model.Timestamp
	AccountingEvidence []EvidenceRef
	Children           []LegacyChildVersion
	OwnerRef           string
	ReviewAfter        model.Timestamp
	Evidence           []EvidenceRef
}

// canon frames the request in declaration order.
//
// THE CHILD SET IS SORTED — on a COPY, so the caller's slice is untouched — because
// the contract calls it a complete SORTED set of ids and versions, not a list. The
// first cut framed the caller's order, which made {A,B} and {B,A} two different
// requests: a recovery caller that rebuilt its set from a map could not replay its
// own import, and got import_conflict for the identical intention. Sorting a
// permutation into the same bytes does NOT make two different sets alike — the ids
// and versions still differ — and duplicates are refused before this point.
//
// Every other vector keeps its order, because in those order carries meaning: an
// evidence list is what the caller presented, in the sequence it presented it.
func (r ImportLegacyHoldRequest) canon(w *canonWriter) {
	w.str(r.Handle.String())
	w.optTS(r.AccountingAt)
	canonEvidenceRefs(w, r.AccountingEvidence)
	children := append([]LegacyChildVersion(nil), r.Children...)
	sortChildVersions(children)
	w.count(len(children))
	for _, c := range children {
		w.str(c.ID.String())
		w.num(c.Version)
	}
	w.str(r.OwnerRef)
	w.ts(r.ReviewAfter)
	canonEvidenceRefs(w, r.Evidence)
}

// sortChildVersions orders a child set by (id, version) — the declared order of the
// contract's complete sorted set.
func sortChildVersions(cs []LegacyChildVersion) {
	less := func(a, b LegacyChildVersion) bool {
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Version < b.Version
	}
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && less(cs[j], cs[j-1]); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// LifecycleActivationRequest is the Begin/Activate request. ExpectedVersion is
// zero only when no frontier row exists.
type LifecycleActivationRequest struct {
	ExpectedVersion int64
	Evidence        []EvidenceRef
}

func (r LifecycleActivationRequest) canon(w *canonWriter) {
	w.num(r.ExpectedVersion)
	canonEvidenceRefs(w, r.Evidence)
}

// LegacyImportSnapshot is the immutable original of one imported handle. It is
// written once and never rewritten: a later enrichment of the current binding does
// not touch these digests or these rows.
type LegacyImportSnapshot struct {
	RequestDigest, GroupDigest, ImportDigest Digest
	OriginalRequest                          ImportLegacyHoldRequest
	OriginalChildren                         []model.Record
	FrontierRef                              *EvidenceRef
}

// LifecycleScopeView is the durable activation boundary of one tenant. An ABSENT
// row is legacy/inactive and has no view; found=false says so.
type LifecycleScopeView struct {
	ID                       model.ID
	Version                  int64
	State                    string
	FrontierAt               model.Timestamp
	FrontierDigest           Digest
	ActivatedAt              *model.Timestamp
	PendingGroupCount        int64
	PendingGroupDigest       Digest
	HistoricalTerminalDigest Digest
	Evidence                 []EvidenceRef
}

// AttemptView is the read model of one attempt parent.
//
// The contract's Outcome field is deliberately NOT materialized in this cut: no
// operation here can write an outcome, so a nil pointer on every returned view
// would be a shape promising a lifecycle that does not exist. The digests and
// links that DO discriminate a row's phase are present, because the readers
// validate them.
type AttemptView struct {
	ID                model.ID
	AttemptRef        AttemptRef
	Handle            model.ID
	Version           int64
	Phase             AttemptPhase
	BindingDigest     Digest
	ReservationDigest Digest
	EffectDigest      *Digest
	AccountingAt      *model.Timestamp
	AccountingBasis   AccountingBasis
	LegacyImport      *LegacyImportSnapshot
	ReviewAfter       model.Timestamp
	OwnerRef          string
	OwnerEpoch        int64
	OutcomeDigest     *Digest
	SettlementDigest  *Digest
	SampleID          *model.ID
	CostRecordID      *model.ID
	PublicationState  string
	Targets           []TargetSnapshot
	Binding           AttemptBinding
}

// -----------------------------------------------------------------------------
// The evidence verifier
// -----------------------------------------------------------------------------

// EvidenceCheck is the closed request the module puts to its configured verifier.
// The fields a given Operation may fill are fixed (see validateEvidenceCheck): the
// absences of the query form are DELIBERATE and are not a relaxation of the
// monetary checks of a mutation.
type EvidenceCheck struct {
	Operation          string
	LegacyImportDigest *Digest
	ScopeFrontierRef   *EvidenceRef
	AttemptRef         AttemptRef
	BindingDigest      Digest
	ReservationDigest  Digest
	EffectDigest       *Digest
	OwnerRef           string
	ReasonCode         string
	ProposedDigest     Digest
	ProposedBinding    *AttemptBinding
	ProposedOutcome    *struct{} // not materialized in this cut; always nil
	Evidence           []EvidenceRef
}

// The operations this cut puts to the verifier.
const (
	opQuery           = "query"
	opImport          = "import"
	opBeginActivation = "begin_activation"
	opPrepareBinding  = "prepare_binding"
)

// VerifiedAttemptActor is the CURRENT verified attribution of the operation being
// performed, for the audit this operation writes. It is valid only for that
// operation inside that scope.
//
// It is NOT a permission and NOT transferable: it does not come from the request,
// from an OwnerRef, from an EvidenceRef or from the historical subject of an
// imported hold, and a value returned by an earlier call is never reused as
// authority for a later one.
type VerifiedAttemptActor struct {
	Actor, ActorKind string
}

// AttemptEvidenceVerifier is the ONE small dependency this cut adds. It is a
// trusted in-process dependency configured once, before serving.
//
// prepare_binding additionally attests the proposed principal, route, surface's
// ApplySeatLimits, entity/runtime associations, estimate and all known or
// not-applicable facts, including COMPLETE tenant membership and ancestor closure
// for the same actor and directory revision. A shaped reference or digest is not
// authority. Only the test attestation table implements this dependency here.
//
// What it must do on every call, and what an implementation that merely returns a
// constant is NOT doing: establish that the CONFIGURED executing/reconciliation
// component still has recovery access to sc.Tenant(), through its own trusted
// dependency and bounded authority reads in the scope it was handed. Being
// installed in New, having authorized an earlier call, or the request naming an
// owner are none of them that check.
//
// It performs no network, secret or provider call and opens no nested
// View/Mutate: it reads, at most, bounded rows in the scope it is given.
//
// In this internal cut a laboratory implementation proves the DEPENDENCY CONTRACT
// only. It is not operational authentication, and no adapter in New()/boot fakes
// one.
type AttemptEvidenceVerifier interface {
	VerifyAttemptEvidence(context.Context, store.Scope, EvidenceCheck) (VerifiedAttemptActor, error)
}

// queryCheck builds the closed query form: Operation and, for a single-attempt
// lookup, a validated AttemptRef. EVERY other field stays at its zero value, which
// is what the contract means by "empty exactly as specified" — no zero digest, no
// historical owner, no evidence read from the parent.
func queryCheck(ref AttemptRef) EvidenceCheck {
	return EvidenceCheck{Operation: opQuery, AttemptRef: ref}
}

// validateEvidenceCheck enforces the shape of each operation BEFORE the verifier
// is called, so a mutation's monetary fields cannot be smuggled through the query
// form and a query cannot arrive carrying fields that would make it look like a
// transition.
func validateEvidenceCheck(c EvidenceCheck) error {
	if !validEvidenceRefs(c.Evidence) {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	if c.ProposedOutcome != nil {
		// No operation in this cut has an outcome to propose.
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	switch c.Operation {
	case opPrepareBinding:
		if !validAttemptRef(c.AttemptRef) || !validDigest(c.BindingDigest) ||
			!validDigest(c.ReservationDigest) || c.ScopeFrontierRef == nil ||
			!c.ScopeFrontierRef.valid() || c.ProposedBinding == nil || c.OwnerRef == "" ||
			len(c.OwnerRef) > maxOpaqueRefBytes || len(c.Evidence) == 0 ||
			c.LegacyImportDigest != nil || c.EffectDigest != nil || c.ReasonCode != "" ||
			c.ProposedDigest != "" {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		return nil
	case opQuery:
		// The closed empty form. AttemptRef is either a valid ref (one attempt) or
		// empty (this tenant's lifecycle_scope row, and nothing else).
		if c.AttemptRef != "" && !validAttemptRef(c.AttemptRef) {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		if c.LegacyImportDigest != nil || c.ScopeFrontierRef != nil || c.EffectDigest != nil ||
			c.ProposedBinding != nil || len(c.Evidence) != 0 ||
			c.BindingDigest != "" || c.ReservationDigest != "" || c.ProposedDigest != "" ||
			c.OwnerRef != "" || c.ReasonCode != "" {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		return nil
	case opImport:
		if !validAttemptRef(c.AttemptRef) ||
			!validDigest(c.BindingDigest) || !validDigest(c.ReservationDigest) ||
			!validDigest(c.ProposedDigest) ||
			c.LegacyImportDigest == nil || !validDigest(*c.LegacyImportDigest) ||
			c.EffectDigest != nil || c.ProposedBinding == nil || c.OwnerRef == "" {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		if c.ScopeFrontierRef != nil && !c.ScopeFrontierRef.valid() {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		return nil
	case opBeginActivation:
		// No imaginary attempt is constructed for a scope transition.
		if c.AttemptRef != "" || c.BindingDigest != "" || c.ReservationDigest != "" ||
			c.LegacyImportDigest != nil || c.EffectDigest != nil ||
			c.ProposedBinding != nil || c.ScopeFrontierRef != nil ||
			!validDigest(c.ProposedDigest) {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		return nil
	}
	return attemptErr(errCodeInvalidAttempt, nil)
}

// verifyAttempt validates the check's shape, calls the configured verifier and
// classifies its refusal.
//
// A missing verifier is capability_unavailable BEFORE any read: the module does
// not fall back to "no verifier configured means everything is allowed", which is
// the one failure mode a fixed dependency exists to remove. A verifier refusal is
// owner_mismatch and carries NO existence information about the subject.
func (m *Module) verifyAttempt(ctx context.Context, sc store.Scope, c EvidenceCheck) (VerifiedAttemptActor, error) {
	if m.attemptVerifier == nil {
		return VerifiedAttemptActor{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := validateEvidenceCheck(c); err != nil {
		return VerifiedAttemptActor{}, err
	}
	actor, err := m.attemptVerifier.VerifyAttemptEvidence(ctx, sc, c)
	if err != nil {
		return VerifiedAttemptActor{}, attemptErr(errCodeOwnerMismatch, err)
	}
	// An unattributable identity is refused before writes: an audit that cannot
	// name who acted is not the audit the contract requires.
	if actor.Actor == "" || actor.ActorKind == "" {
		return VerifiedAttemptActor{}, attemptErr(errCodeEvidenceIncomplete, nil)
	}
	return actor, nil
}

// sortStrings sorts a small string slice bytewise. It is here rather than in a
// helper package so the canonical ordering rule lives beside the framing it
// serves.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// legacyImportRef derives the deterministic import identity of one tenant+handle:
// the first 32 lowercase hex characters of the framed SHA-256. The full binding is
// retained beside it (the stored handle and request), so a collision is
// detectable rather than silently accepted as a replay.
func legacyImportRef(tenant model.TenantID, handle model.ID) AttemptRef {
	d := canonDigest(domainLegacyImportID, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(handle.String())
	})
	return AttemptRef(string(d)[:32])
}

// -----------------------------------------------------------------------------
// Operation digests
// -----------------------------------------------------------------------------

// bindingDigestOf is the digest of the current canonical pre-effect binding
// (contract canonicalization.binding_digest_input): tenant, attempt_ref and the
// binding in declared order. Owner and review scheduling are EXCLUDED because they
// change without the binding changing; the effect digest is excluded because a
// later phase binds it once.
func bindingDigestOf(tenant model.TenantID, ref AttemptRef, b AttemptBinding) Digest {
	return canonDigest(domainBinding, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(string(ref))
		b.canon(w)
	})
}

// The complete target set is framed inside the reservation digest below. This
// cut needs no separate target digest or unused domain constant.

// reservationDigestOf is the digest of the reservation facts of an attempt
// (contract canonicalization.reservation_digest_input): tenant, attempt_ref,
// handle, the binding digest, the import digest when there is one, the accounting
// basis and its explicitly nullable instant, and the complete sorted target set
// with the original child amounts and sequences.
//
// The CURRENT child state, actual and version are excluded on purpose: they move
// with settlement, and a digest that moved with them could not certify the
// original obligation it exists to certify.
func reservationDigestOf(
	tenant model.TenantID,
	ref AttemptRef,
	handle model.ID,
	binding Digest,
	imported *Digest,
	basis AccountingBasis,
	accountingAt *model.Timestamp,
	targets []TargetSnapshot,
	amounts []int64,
	seqs []int64,
) Digest {
	return canonDigest(domainReservation, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(string(ref))
		w.str(handle.String())
		w.str(string(binding))
		w.optDigest(imported)
		basis.canon(w)
		w.optTS(accountingAt)
		w.count(len(targets))
		for _, t := range targets {
			t.canon(w)
		}
		w.count(len(amounts))
		for _, a := range amounts {
			w.num(a)
		}
		w.count(len(seqs))
		for _, s := range seqs {
			w.num(s)
		}
	})
}

// sortTargets orders the target set by the contract's child order
// (policy_ref, scope_key, period_start, child id).
func sortTargets(ts []TargetSnapshot) {
	less := func(a, b TargetSnapshot) bool {
		if a.PolicyID != b.PolicyID {
			return a.PolicyID < b.PolicyID
		}
		if a.ScopeKey != b.ScopeKey {
			return a.ScopeKey < b.ScopeKey
		}
		if sa, sb := a.PeriodStart.String(), b.PeriodStart.String(); sa != sb {
			return sa < sb
		}
		return a.ChildID < b.ChildID
	}
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && less(ts[j], ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}
