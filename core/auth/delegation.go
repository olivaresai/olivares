// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// delegation.go — the delegation-handle verifier (the DelegationProof
// "handle" scheme). It mints an opaque, single-use handle whose authority is
// possession of a subject credential, and later verifies a PEP-presented proof
// and claims a durable single-use decision. It is the primitive that makes the
// PDP contract's subject-authority, anti-replay and capability-intersection
// invariants true (sdk/pdp.go invariants #2, #4, #5).
//
// Doctrine (docs/SECURITY-HARDENING.md): the handle is an OPAQUE first-party credential —
// selector + SHA-256(secret), no JWT, no signing surface. Every unknown or
// malformed input fails closed, and an unknown selector is externally
// indistinguishable from a wrong secret. Effective authority is always the
// SUBJECT's CURRENT authority intersected with the ceiling captured at mint, so a
// handle can never grant more than the subject holds at claim time, and never
// more than it held at mint.

// Delegation-scheme wire vocabulary: compile-time aliases of the SDK contract so
// the verifier can never drift from the wire values (the sdk module is zero-dep,
// so no cycle; the sdk.FailureClass mapping stays the composition root's job).
const (
	delegationSchemeHandle     = sdk.DelegationSchemeHandle
	delegationSchemeActAsToken = sdk.DelegationSchemeActAsToken // reserved, rejected in v1
)

// delegationProtocolVersion is the wire-contract id bound into the request
// fingerprint's domain separation.
const delegationProtocolVersion = sdk.ProtocolVersion

// delegationHandleKind is the audit target kind for a delegation handle.
const delegationHandleKind model.Kind = "core.delegation_handle"

// fingerprintDomain domain-separates the request fingerprint so its hash can
// never collide with any other hash the engine computes over similar bytes.
const fingerprintDomain = "olivares.pdp.claim.v1\x00"

// maxDelegationTokenLen caps a presented handle token before any parsing work: a
// well-formed handle is "olvd_<26>_<52>" (84 bytes); the cap is a cheap
// defense-in-depth bound, oversized input is a protocol error.
const maxDelegationTokenLen = 256

// defaultSweepLimit bounds an unbounded SweepDelegation batch.
const defaultSweepLimit = 500

// Delegation TTL / freshness / lifetime defaults (all overridable except the two
// lifetime/grace values, which are v1 constants).
const (
	// DefaultDelegationTTL is a freshly minted handle's lifetime before it is
	// clamped to the subject credential's own expiry. A handle is for one bounded
	// task, not a standing credential.
	DefaultDelegationTTL = 5 * time.Minute
	// DefaultDelegationMaxAge rejects a presented request whose IssuedAt is older
	// than this (anti-replay lower bound).
	DefaultDelegationMaxAge = 5 * time.Minute
	// DefaultDelegationFutureSkew rejects a presented request whose IssuedAt is
	// further in the future than this (clock-skew tolerance / anti-replay upper bound).
	DefaultDelegationFutureSkew = 30 * time.Second
	// DefaultDecisionClaimLifetime is how long a decision claim answers the
	// service-binding check after ClaimedAt (later protocol stages tune this).
	DefaultDecisionClaimLifetime = 1 * time.Hour
	// DefaultDelegationSweepGrace delays reaping an expired unclaimed handle past
	// its ExpiresAt, so an in-flight verify near expiry is never raced by the GC.
	DefaultDelegationSweepGrace = 1 * time.Hour
)

// knownOperationKinds is the set of sdk OperationRef.Kind values a delegation
// handle may authorize in protocol v1. The SDK fixes Kind as a free-form string
// documented only by example (sdk/pdp.go), so there is no shared enumeration to
// import; v1 pins the two inference operations the contract names. New kinds are
// added here as the governed inference surface grows.
var knownOperationKinds = map[string]bool{
	"messages":       true,
	"messages_batch": true,
}

// knownPEPCapabilities is the closed vocabulary of PEP capability keys a delegation
// request may declare. It mirrors the sdk.PEPCapabilities json tags exactly
// (buffer_request/buffer_response/streaming/batch — see sdk/pdp.go), so an
// attacker cannot inflate the request fingerprint or the audit trail with an
// arbitrary declared key. A declared key outside this set is dropped before it can
// enter the effective set, the fingerprint, or the overclaim audit.
var knownPEPCapabilities = map[string]bool{
	"buffer_request":  true,
	"buffer_response": true,
	"streaming":       true,
	"batch":           true,
}

// sortedPEPCapabilityKeys is the deterministic sort order of the capability
// vocabulary, used to encode the declared vector injectively into the fingerprint.
var sortedPEPCapabilityKeys = func() []string {
	keys := make([]string, 0, len(knownPEPCapabilities))
	for k := range knownPEPCapabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}()

// Delegation domain errors. They map 1:1 to sdk.FailureClass in the cmd/olivares
// adapter. ErrDelegationInvalid deliberately covers every
// authority/binding failure with ONE message so an attacker cannot distinguish an
// unknown selector, a wrong secret, an expired/revoked handle, a confused-deputy
// presentation, an out-of-scope operation, a bound-digest mismatch, or a failed
// subject revalidation. Raw store errors propagate unchanged (a plane fault).
var (
	// ErrDelegationProtocol is a scheme/parse/shape failure (→ FailureProtocolError).
	ErrDelegationProtocol = errors.New("auth: delegation protocol error")
	// ErrDelegationSchemeReserved is the reserved "actas-token" scheme, unsupported
	// in protocol v1. It wraps ErrDelegationProtocol so it still maps to
	// FailureProtocolError, while carrying a distinct, stable reason (no wire
	// values) the adapter can surface separately from a malformed-token error.
	ErrDelegationSchemeReserved = fmt.Errorf("%w: scheme reserved, unsupported in v1", ErrDelegationProtocol)
	// ErrDelegationInvalid is any authority or binding failure, indistinguishable
	// by message (→ FailureDelegationInvalid).
	ErrDelegationInvalid = errors.New("auth: delegation proof invalid")
	// ErrDelegationReplay is a stale/future IssuedAt or a nonce-reuse variant
	// (→ FailureReplay).
	ErrDelegationReplay = errors.New("auth: delegation replay")

	// ErrInvalidDelegationRequest is a malformed or refused MINT request
	// (superadmin subject, not exactly one tenant, empty/unknown operation,
	// agent-OBO unavailable). Authority to mint is possession of the subject token.
	ErrInvalidDelegationRequest = errors.New("auth: invalid delegation mint request")
	// ErrDelegationPEPService means the target PEP service is missing, disabled,
	// or in another business tenant than the subject.
	ErrDelegationPEPService = errors.New("auth: delegation target PEP service unavailable")

	// ErrUnknownDecision is a finalize against a decision claim that does not exist.
	ErrUnknownDecision = errors.New("auth: unknown decision claim")
	// ErrInvalidVerdict is a finalize with an empty or non-JSON verdict document.
	ErrInvalidVerdict = errors.New("auth: invalid decision verdict")
	// ErrDecisionExpired is a finalize against a claim whose single-use lifetime
	// has elapsed since it was claimed; a dead claim is never finalized.
	ErrDecisionExpired = errors.New("auth: decision claim expired")
	// ErrDecisionFinalizeConflict is a finalize that contradicts an already-final
	// claim (a different verdict hash OR a different policy version for the same hash).
	ErrDecisionFinalizeConflict = errors.New("auth: contradictory decision finalization")
	// ErrDecisionBindingMismatch is the single indistinguishable error for every
	// service-binding failure (unknown/tenant/service/nonce/expired).
	ErrDecisionBindingMismatch = errors.New("auth: decision service binding mismatch")

	// ErrDelegationEvidenceFault is the deny-closed error when a security-critical
	// delegation effect (a decision claim, a finalize, or an overclaim record)
	// COMMITTED durably but its per-operation audit was dropped by a DEGRADE-mode
	// audit spool (Append returned Seq==0 after committing loss accounting). This
	// mirrors the inference proxy's evidence-or-refuse discipline (sdk/evidence.go,
	// EvidenceFaultSpoolDegraded): the durable loss accounting (audit_spool_gaps) is
	// COMMITTED so the episode is counted and eventually sealed, but the OBSERVABLE
	// decision is refused — the effect is durably recorded, never silently emitted
	// without an anchor. It maps to sdk.FailureEvidenceFault in the cmd/olivares
	// adapter. It is NEVER returned from inside an AuthMutate callback to GATE A
	// DROPPED-AUDIT EFFECT (that would roll back the very gap accounting it exists to
	// preserve) — that classification happens AFTER the transaction commits. It MAY be
	// returned from inside a callback on a pre-transition READ path that has produced no
	// audit, gap, or effect (refusing to finalize an already-tombstoned pending claim),
	// where aborting the read-only work rolls nothing back.
	ErrDelegationEvidenceFault = errors.New("auth: delegation evidence fault")
)

// MintDelegationRequest is the input to MintDelegationHandle. Authority is
// possession of SubjectToken (as with ExchangeToken); the caller principal is
// only the audit actor.
type MintDelegationRequest struct {
	// SubjectToken is the subject credential whose authority the handle carries.
	SubjectToken string
	// PEPServiceID is the one registered PEP service the handle may be presented
	// to. It must exist, be enabled, and belong to the subject's tenant.
	PEPServiceID model.ID
	// Operations is the non-empty allowlist of sdk operation kinds the handle may
	// authorize; each must be a known kind.
	Operations []string
	// TTL optionally overrides DefaultDelegationTTL; the handle is always clamped
	// to the subject credential's own expiry regardless.
	TTL time.Duration
	// BoundDigest optionally binds the handle to one content digest (a per-request
	// handle); empty leaves per-task content binding to the request fingerprint.
	BoundDigest string
	// AgentRef optionally names an agent identity (external_id) the handle
	// delegates to (agent-OBO). When set, the subject must be the agent's sponsor.
	AgentRef string
}

// DelegationProofInput is the PEP-presented proof (mirrors sdk.DelegationProof).
type DelegationProofInput struct {
	// Scheme selects verification; protocol v1 accepts only "handle".
	Scheme string
	// Token is the opaque handle wire token.
	Token []byte
}

// PresentedRequest is the per-request material the verifier binds the claim to.
// ContentDigest is computed by the CALLER over the exact received octets
// (invariant #3) — the verifier never trusts a PEP-asserted content class.
type PresentedRequest struct {
	Nonce                string
	OperationKind        string
	Model                string
	Stream               bool
	ContentDigest        string
	ContentSize          int64
	MediaType            string
	IssuedAt             time.Time
	DeclaredCapabilities map[string]bool
}

// VerifiedDelegation is the sealed result of a successful verify+claim. All
// fields are unexported so only this package (the gated PrincipalForDelegation
// constructor) can turn it into a Principal; callers read immutable snapshots.
type VerifiedDelegation struct {
	decisionID            model.ID
	tenant                model.TenantID
	subjectUserID         model.ID
	subjectCredKind       string
	subjectCredID         model.ID
	effectiveRole         string
	effectiveGroups       []string
	actAs                 model.ID
	agentRef              string
	pepServiceID          model.ID
	effectiveCapabilities map[string]bool
	droppedCapabilities   map[string]bool
	capabilityVersion     int
	retried               bool
	storedVerdictJSON     string
}

// DecisionID returns the server-minted decision id every later phase must match.
func (v VerifiedDelegation) DecisionID() model.ID { return v.decisionID }

// Tenant returns the governed business tenant.
func (v VerifiedDelegation) Tenant() model.TenantID { return v.tenant }

// SubjectUserID returns the governed subject user.
func (v VerifiedDelegation) SubjectUserID() model.ID { return v.subjectUserID }

// EffectiveRole returns the subject's role clamped to the mint-time ceiling.
func (v VerifiedDelegation) EffectiveRole() string { return v.effectiveRole }

// EffectiveGroups returns the subject's current groups intersected with the
// mint-time closure (a defensive copy).
func (v VerifiedDelegation) EffectiveGroups() []string {
	out := make([]string, len(v.effectiveGroups))
	copy(out, v.effectiveGroups)
	return out
}

// ActAs returns the user this delegation acts on behalf of, and whether it does.
func (v VerifiedDelegation) ActAs() (model.ID, bool) { return v.actAs, !v.actAs.IsZero() }

// AgentRef returns the revalidated agent identity, or "" for a non-agent delegation.
func (v VerifiedDelegation) AgentRef() string { return v.agentRef }

// PEPServiceID returns the authenticated PEP service the decision is bound to.
func (v VerifiedDelegation) PEPServiceID() model.ID { return v.pepServiceID }

// EffectiveCapabilities returns declared ∩ registered (a defensive copy).
func (v VerifiedDelegation) EffectiveCapabilities() map[string]bool {
	return clonePEPCapabilities(v.effectiveCapabilities)
}

// DroppedCapabilities returns the declared-but-unregistered overclaims that were
// dropped (a defensive copy). The caller audits these; they are never a deny (a
// missing REQUIRED control is decided at verdict time, not here).
func (v VerifiedDelegation) DroppedCapabilities() map[string]bool {
	return clonePEPCapabilities(v.droppedCapabilities)
}

// CapabilityVersion returns the registered capability version bound to the claim.
func (v VerifiedDelegation) CapabilityVersion() int { return v.capabilityVersion }

// Retried reports whether this verify resolved an existing (idempotent) claim
// rather than creating a new one.
func (v VerifiedDelegation) Retried() bool { return v.retried }

// StoredVerdictJSON returns the finalized verdict document when a retry resolved
// an already-finalized claim; "" while the claim is still pending.
func (v VerifiedDelegation) StoredVerdictJSON() string { return v.storedVerdictJSON }

// SweepCounts reports what one SweepDelegation batch removed.
type SweepCounts struct {
	// ExpiredHandlesDeleted is the number of expired, unclaimed handles reaped.
	ExpiredHandlesDeleted int
}

// MintDelegationHandle mints an opaque single-use delegation handle whose
// authority is possession of req.SubjectToken (mirroring ExchangeToken: the
// caller principal is only the audit actor; a subject token can only ever yield a
// LESSER handle). It refuses a superadmin subject, a subject that does not resolve
// to exactly one tenant, and a workspace-confined subject; validates optional
// agent-OBO through the sponsor check; requires the target PEP service to exist,
// be enabled and share the subject's tenant; and captures the mint-time ceiling
// (role + resolved group closure). The handle is clamped to the subject
// credential's expiry. The returned token is shown once. Audited "delegation.mint"
// in the same transaction.
func (a *Authenticator) MintDelegationHandle(ctx context.Context, caller Principal, req MintDelegationRequest) (string, model.DelegationHandle, error) {
	if caller.IsPurposeRestricted() {
		return "", model.DelegationHandle{}, fmt.Errorf("%w: purpose-restricted callers cannot mint delegation handles", ErrInvalidDelegationRequest)
	}
	ops, err := validateOperations(req.Operations)
	if err != nil {
		return "", model.DelegationHandle{}, err
	}

	subject, err := a.Authenticate(ctx, req.SubjectToken)
	if err != nil {
		return "", model.DelegationHandle{}, fmt.Errorf("%w: subject credential: %v", ErrInvalidDelegationRequest, err)
	}
	if subject.Superadmin {
		return "", model.DelegationHandle{}, fmt.Errorf("%w: cannot delegate a system (superadmin) credential", ErrInvalidDelegationRequest)
	}
	if subject.IsPurposeRestricted() {
		return "", model.DelegationHandle{}, fmt.Errorf("%w: purpose-restricted credentials cannot mint delegation handles", ErrInvalidDelegationRequest)
	}
	tenants := subject.Tenants()
	if len(tenants) != 1 {
		return "", model.DelegationHandle{}, fmt.Errorf("%w: subject must resolve to exactly one tenant (got %d)", ErrInvalidDelegationRequest, len(tenants))
	}
	tenant := tenants[0]
	if _, confined := subject.ConfinedWorkspaceIn(tenant); confined {
		return "", model.DelegationHandle{}, ErrWorkspaceConfined
	}
	subjectRole, _ := subject.RoleIn(tenant)

	sourceKind := "user"
	if subject.Kind == KindToken {
		sourceKind = "token"
	}

	// Agent-OBO: an explicit AgentRef must pass the sponsor check (the subject IS
	// the agent's human sponsor); otherwise carry forward an already agent-bound
	// subject's identity. Both are revalidated at claim time.
	agentRef := subject.AgentIdentity
	if req.AgentRef != "" {
		if a.agentChecker == nil {
			return "", model.DelegationHandle{}, fmt.Errorf("%w: agent-OBO unavailable (no lifecycle checker)", ErrInvalidDelegationRequest)
		}
		sponsorRef, serr := a.resolveUserExternalID(ctx, subject.UserID, tenant)
		if serr != nil {
			return "", model.DelegationHandle{}, fmt.Errorf("%w: cannot resolve sponsor identity: %v", ErrAgentBlocked, serr)
		}
		if err := a.agentChecker.CheckAgentForExchange(ctx, tenant, req.AgentRef, sponsorRef); err != nil {
			return "", model.DelegationHandle{}, fmt.Errorf("%w: %v", ErrAgentBlocked, err)
		}
		agentRef = req.AgentRef
	}

	actAs, _ := subject.ActAs()
	mintGroups := subject.GroupsIn(tenant)

	cred, err := NewCredential(PrefixDelegation)
	if err != nil {
		return "", model.DelegationHandle{}, err
	}
	now := a.clock.Now()
	// The configured TTL is a MAXIMUM: an explicit req.TTL may only SHORTEN it, never
	// extend past it. (The subject-credential expiry clamp below shortens it further.)
	ttl := a.delegationTTL()
	if req.TTL > 0 && req.TTL < ttl {
		ttl = req.TTL
	}
	expiry := now.Time().Add(ttl)

	var stored model.DelegationHandle
	mutErr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		service, err := as.PEPServices().Get(ctx, req.PEPServiceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrDelegationPEPService
			}
			return err
		}
		if service.DisabledAt != nil ||
			service.TargetTenantID != tenant ||
			service.PDPAudience == "" ||
			service.CapabilityVersion < 1 {
			return ErrDelegationPEPService
		}

		// Clamp to the subject credential's expiry so a handle never outlives its
		// subject.
		subExp, e := credentialExpiry(ctx, as, subject)
		if e != nil {
			return e
		}
		if subExp != nil && subExp.Time().Before(expiry) {
			expiry = subExp.Time()
		}

		h, e := as.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: tenant,
			Selector:       cred.Selector,
			SecretHash:     cred.SecretHash,
			SourceCredKind: sourceKind,
			SourceCredID:   subject.CredID,
			SubjectUserID:  subject.UserID,
			ActAsUserID:    actAs,
			AgentRef:       agentRef,
			MintRole:       subjectRole,
			MintGroups:     mintGroups,
			PEPServiceID:   service.ID,
			Audience:       service.PDPAudience,
			Operations:     ops,
			BoundDigest:    req.BoundDigest,
			ExpiresAt:      model.NewTimestamp(expiry),
		})
		if e != nil {
			return e
		}
		stored = h
		return auditAct(ctx, as, caller, "delegation.mint", delegationHandleKind, h.ID)
	})
	if mutErr != nil {
		return "", model.DelegationHandle{}, mutErr
	}
	return cred.Token, stored, nil
}

// RevokeDelegationHandle invalidates a handle before its expiry. The caller must
// hold at least admin in the handle's business tenant (or be superadmin) and not
// be workspace-confined there. It is idempotent. Audited "delegation.revoke".
func (a *Authenticator) RevokeDelegationHandle(ctx context.Context, caller Principal, jti model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		handle, err := as.DelegationHandles().Get(ctx, jti)
		if err != nil {
			return err
		}
		if err := authorizePEPAdmin(caller, handle.TargetTenantID); err != nil {
			return err
		}
		if handle.RevokedAt != nil {
			return nil
		}
		now := a.clock.Now()
		// Flip only revoked_at through the specialized store op: the handle's
		// ceiling, expiry and audience stay immutable (the generic Update is no
		// longer reachable). Audit delegation.revoke ONLY when the guarded UPDATE
		// actually flipped a row — a concurrent second revoke that changed nothing
		// (the loser of the revoked_at IS NULL race) must not emit a duplicate event.
		changed, err := as.RevokeDelegationHandle(ctx, handle.ID, now)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return auditAct(ctx, as, caller, "delegation.revoke", delegationHandleKind, handle.ID)
	})
}

// VerifyAndClaimDelegation verifies a PEP-presented proof and atomically claims a
// single-use decision. It runs the design's deny-closed check order: scheme/parse
// (protocol) → constant-time secret (invalid, indistinguishable) → handle live →
// PEP/tenant/audience (confused deputy) → operation scope → BoundDigest → IssuedAt
// freshness (replay) → SUBJECT REVALIDATION (effective = current ∩ mint ceiling) →
// atomic claim. A uniqueness conflict on the handle JTI or the (service, nonce)
// pair is classified: an exact match on {PEP service, handle JTI, request
// fingerprint, tenant} is an idempotent retry (crash-recoverable); anything else
// is a replay. The handle row is NEVER mutated at claim time — single use is
// enforced by the claim's unique handle_jti, so a pending claim resumes after a
// crash while the handle is never permanently burned.
func (a *Authenticator) VerifyAndClaimDelegation(ctx context.Context, pep PEPIdentity, proof DelegationProofInput, presented PresentedRequest) (VerifiedDelegation, error) {
	// 1. Scheme + parse — pure, pre-transaction.
	if proof.Scheme != delegationSchemeHandle {
		// "actas-token" is RESERVED and unsupported in v1: it maps to the same
		// protocol failure class but carries a distinct, stable reason. Every other
		// value (empty/unknown/case-mismatched) is a generic protocol error.
		if proof.Scheme == delegationSchemeActAsToken {
			return VerifiedDelegation{}, ErrDelegationSchemeReserved
		}
		return VerifiedDelegation{}, ErrDelegationProtocol
	}
	selector, secret, err := parseDelegationToken(string(proof.Token))
	if err != nil {
		return VerifiedDelegation{}, err
	}
	// 1b. Presented-request shape — pure, pre-transaction. Reject a request that
	// lacks an essential binding so a PEP can never register a sealed claim over an
	// under-specified request (empty nonces all hash to sha256("")).
	if err := validatePresentedRequest(presented); err != nil {
		return VerifiedDelegation{}, err
	}

	var verified VerifiedDelegation
	// claimRow captures the RESOLVED claim (freshly inserted or the conflict-reloaded
	// existing row) so the observable decision can be refused AFTER the transaction
	// commits when the row is not evidence-anchored. The anchor state is PERSISTED on
	// the row by ClaimDecision (deny-closed default false), so a degrade-mode audit drop
	// refuses the first call AND every identical retry (which resolves the persisted
	// tombstone) — never gated from inside the callback (that would roll the gap back).
	var claimRow model.PDPDecisionClaim
	mutErr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		now := a.clock.Now()

		// 2. Selector lookup + constant-time secret (indistinguishable failures).
		// Run the compare on BOTH paths (a real hash on a hit, a fixed dummy on a
		// miss) so an unknown selector and a wrong secret are timing-equivalent.
		handle, found, err := lookupDelegationBySelector(ctx, as, selector)
		if err != nil {
			return err // store fault → plane fault
		}
		secretOK := false
		if found {
			secretOK = SecretMatches(secret, handle.SecretHash)
		} else {
			SecretMatches(secret, dummySecretHash[:])
		}
		if !found || !secretOK {
			return ErrDelegationInvalid
		}
		// 3. Handle live: not revoked, not expired (exact boundary = expired).
		if handle.RevokedAt != nil || !handle.ExpiresAt.Time().After(now.Time()) {
			return ErrDelegationInvalid
		}
		// 4. Confused-deputy: bound service, tenant and audience must match the
		// authenticated PEP. The current service registration is authoritative for
		// the audience so a post-mint audience change invalidates the handle.
		if handle.PEPServiceID != pep.ServiceID() || handle.TargetTenantID != pep.Tenant() {
			return ErrDelegationInvalid
		}
		service, err := as.PEPServices().Get(ctx, pep.ServiceID())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrDelegationInvalid
			}
			return err
		}
		if service.DisabledAt != nil ||
			service.TargetTenantID != pep.Tenant() ||
			handle.Audience != service.PDPAudience {
			return ErrDelegationInvalid
		}
		// 4b. Revalidate the PEP transport credential itself: a cached PEPIdentity
		// must not outlive the revocation, expiry, or unbind of the api_token it was
		// minted from. Any failure is indistinguishable from any other invalid proof.
		if err := revalidatePEPCredential(ctx, as, pep, now); err != nil {
			return err
		}
		// 5. Operation within the handle's allowlist.
		if !containsString(handle.Operations, presented.OperationKind) {
			return ErrDelegationInvalid
		}
		// 6. BoundDigest, when set, must equal the server-computed content digest.
		if handle.BoundDigest != "" && handle.BoundDigest != presented.ContentDigest {
			return ErrDelegationInvalid
		}
		// 7. IssuedAt freshness + future skew (replay).
		if err := a.checkFreshness(presented.IssuedAt, now); err != nil {
			return err
		}
		// 8. Subject revalidation (effective = current ∩ mint ceiling), deny-closed.
		effRole, effGroups, err := a.revalidateSubject(ctx, as, handle, pep.Tenant())
		if err != nil {
			return err
		}

		// Effective capabilities are intersected against the CURRENT service
		// registration read INSIDE this transaction (its capabilities + version), not
		// the possibly-stale PEPIdentity snapshot captured at AuthenticatePEP time — so
		// the claim records the caps and version as of claim time.
		effCaps, dropped := EffectiveCapabilities(presented.DeclaredCapabilities, service.Capabilities)

		// 9. Atomic claim.
		fingerprint := requestFingerprint(handle, pep, pep.Tenant(), service.PDPAudience, presented)
		claim := model.PDPDecisionClaim{
			TargetTenantID:        pep.Tenant(),
			HandleJTI:             handle.ID,
			PEPServiceID:          pep.ServiceID(),
			NonceHash:             sha256Hex(presented.Nonce),
			RequestFingerprint:    fingerprint,
			RequestIssuedAt:       model.NewTimestamp(presented.IssuedAt.UTC()),
			CapabilityVersion:     service.CapabilityVersion,
			EffectiveCapabilities: effCaps,
		}
		// ClaimDecision emits the delegation.claim audit AND, when dropped is non-empty,
		// the delegation.capability_overclaim audit INSIDE this transaction, and persists
		// whether they anchored on the row's evidence_anchored column (deny-closed).
		stored, created, err := as.ClaimDecision(ctx, claim, dropped)
		if err != nil {
			return err // store fault → plane fault
		}
		claimRow = stored

		verified = VerifiedDelegation{
			decisionID:            stored.ID,
			tenant:                pep.Tenant(),
			subjectUserID:         handle.SubjectUserID,
			subjectCredKind:       handle.SourceCredKind,
			subjectCredID:         handle.SourceCredID,
			effectiveRole:         effRole,
			effectiveGroups:       effGroups,
			actAs:                 handle.ActAsUserID,
			agentRef:              handle.AgentRef,
			pepServiceID:          pep.ServiceID(),
			effectiveCapabilities: effCaps,
			droppedCapabilities:   dropped,
			capabilityVersion:     service.CapabilityVersion,
		}
		if created {
			// The claim row + its per-operation audits committed. Whether they anchored
			// is PERSISTED on claimRow.EvidenceAnchored; the observable decision is
			// refused evidence-or-refuse AFTER commit (below), never gated here.
			return nil
		}
		// created == false: idempotent ONLY on an exact key match, else replay. The
		// resolved row carries its PERSISTED evidence_anchored, so a retry that resolves a
		// tombstoned claim (its original anchor dropped under degrade) is refused after
		// commit exactly like the first call — closing the allow-on-retry bypass.
		if stored.PEPServiceID != pep.ServiceID() ||
			stored.HandleJTI != handle.ID ||
			stored.RequestFingerprint != fingerprint ||
			stored.TargetTenantID != pep.Tenant() {
			return ErrDelegationReplay
		}
		verified.retried = true
		// A retry surfaces the STORED claim's caps and version (and, when final, its
		// verdict) — never the freshly computed values from this pass — so an
		// idempotent retry can never mix fresh capabilities with a stored verdict.
		verified.effectiveCapabilities = stored.EffectiveCapabilities
		verified.capabilityVersion = stored.CapabilityVersion
		// Recompute the dropped set AGAINST the stored decision, not the current
		// registration: otherwise a capability de-registered between the original
		// claim and this retry would be reported BOTH as stored-effective and freshly
		// dropped. Dropped-on-retry = declared known keys the stored decision did not
		// make effective, which is disjoint from the stored effective set by
		// construction.
		verified.droppedCapabilities = droppedAgainstStored(presented.DeclaredCapabilities, stored.EffectiveCapabilities)
		if stored.State == "final" {
			verified.storedVerdictJSON = stored.VerdictJSON
		}
		return nil
	})
	if mutErr != nil {
		return VerifiedDelegation{}, mutErr
	}
	// The claim row + any overclaim record + the durable gap accounting all COMMITTED.
	// The PERSISTED anchor is the source of truth: an un-anchored row (a per-operation
	// audit dropped by the degrade spool) has no anchor for this decision, so refuse the
	// OBSERVABLE result evidence-or-refuse (sdk/evidence.go). Because the flag is
	// persisted, this refuses the FIRST call (just-written false) AND every identical
	// retry (which resolves the persisted tombstone) — closing the allow-on-retry bypass.
	// The durable single-use claim and the counted gap remain, contract-correct.
	if !claimRow.EvidenceAnchored {
		return VerifiedDelegation{}, ErrDelegationEvidenceFault
	}
	return verified, nil
}

// revalidateSubject resolves the subject's CURRENT authority and intersects it
// with the mint-time ceiling, deny-closed at every step: the source credential
// must still be live; the subject user must be active; the tenant relationship
// (a session's membership or a token's bound grant) must still exist; a NEW
// workspace confinement invalidates; and an agent binding re-runs the lifecycle
// checker. It returns the effective role (lower of current and mint) and the
// effective groups (current ∩ mint closure). Every invalidation returns
// ErrDelegationInvalid; only raw store errors propagate.
func (a *Authenticator) revalidateSubject(ctx context.Context, as store.AuthScope, handle model.DelegationHandle, tenant model.TenantID) (string, []string, error) {
	now := a.clock.Now()

	var tokenRole string
	isToken := false
	switch handle.SourceCredKind {
	case "user":
		s, err := as.Sessions().Get(ctx, handle.SourceCredID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", nil, ErrDelegationInvalid
			}
			return "", nil, err
		}
		if s.Revoked || s.ExpiresAt.Before(now) || s.UserID != handle.SubjectUserID {
			return "", nil, ErrDelegationInvalid
		}
	case "token":
		// A token source revalidates ONLY the token credential itself; it does NOT
		// re-check tenant membership or workspace confinement. This is deliberate and
		// consistent with direct token authentication (authToken, authenticator.go):
		// a token's authority is its own bound tenant+role, not a membership row, and
		// a token has no WorkspaceID to confine. The membership/confinement re-check
		// below applies to the "user" (session) branch, which does derive authority
		// from live memberships.
		t, err := as.Tokens().Get(ctx, handle.SourceCredID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", nil, ErrDelegationInvalid
			}
			return "", nil, err
		}
		if t.Revoked || t.Purpose != "" || t.IsSuperadmin || t.UserID != handle.SubjectUserID {
			return "", nil, ErrDelegationInvalid
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(now) {
			return "", nil, ErrDelegationInvalid
		}
		if t.BoundTenantID != tenant || !IsRole(t.Role) {
			return "", nil, ErrDelegationInvalid
		}
		tokenRole = t.Role
		isToken = true
	default:
		return "", nil, ErrDelegationInvalid
	}

	u, err := as.Users().Get(ctx, handle.SubjectUserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, ErrDelegationInvalid
		}
		return "", nil, err
	}
	if u.Status != model.StatusActive {
		return "", nil, ErrDelegationInvalid
	}

	var curRole string
	var curGroups []string
	if isToken {
		curRole = tokenRole
	} else {
		grants, groupsByTenant, confined, err := loadGrants(ctx, as, handle.SubjectUserID)
		if err != nil {
			return "", nil, err
		}
		role, member := grants[tenant]
		if !member {
			return "", nil, ErrDelegationInvalid // membership removed
		}
		if ws, isConfined := confined[tenant]; isConfined && !ws.IsZero() {
			return "", nil, ErrDelegationInvalid // newly confined
		}
		curRole = role
		curGroups = groupsByTenant[tenant]
	}

	if handle.AgentRef != "" {
		if a.agentChecker == nil {
			return "", nil, ErrDelegationInvalid
		}
		if err := a.agentChecker.CheckAgentForExchange(ctx, tenant, handle.AgentRef, u.ExternalID); err != nil {
			return "", nil, ErrDelegationInvalid
		}
	}

	effRole := lowerRole(curRole, handle.MintRole)
	var effGroups []string
	if !isToken {
		effGroups = intersectStrings(curGroups, handle.MintGroups)
	}
	return effRole, effGroups, nil
}

// FinalizeDecisionClaim transitions a pending claim to final, binding the
// VerdictHash = sha256hex(verdictJSON). It refuses an empty or non-JSON verdict
// (ErrInvalidVerdict), an unknown decision (ErrUnknownDecision), and a claim whose
// single-use lifetime has elapsed (ErrDecisionExpired — a dead claim is never
// finalized). It is idempotent ONLY when BOTH the verdict hash AND the policy
// version match the stored final claim; any other final content — including the
// same hash under a different policy version — is a contradiction
// (ErrDecisionFinalizeConflict). A concurrent identical finalize (the version-
// locked transition lost the race) is reloaded and re-classified rather than
// leaking store.ErrConflict. Audited "delegation.finalize". It takes no caller
// principal: it is a server-internal protocol primitive invoked by the PDP.
func (a *Authenticator) FinalizeDecisionClaim(ctx context.Context, decisionID model.ID, verdictJSON []byte, policyVersion string) error {
	if len(verdictJSON) == 0 || !json.Valid(verdictJSON) {
		return ErrInvalidVerdict
	}
	verdictHash := sha256HexBytes(verdictJSON)
	// evidenceDropped mirrors VerifyAndClaimDelegation: set INSIDE the transaction on a
	// degrade-mode drop (Seq==0) so the state change AND the durable gap accounting
	// COMMIT (return nil), then the observable decision is refused evidence-or-refuse
	// AFTER the commit — never gated from inside the callback (that rolls the gap back).
	var evidenceDropped bool
	mutErr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		evidenceDropped = false
		claim, err := as.PDPDecisionClaims().Get(ctx, decisionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnknownDecision
			}
			return err
		}
		if claim.State == "final" {
			return classifyFinalized(claim, verdictHash, policyVersion)
		}
		// A tombstoned claim (its own delegation.claim/overclaim audit dropped under
		// degrade, so it committed pending && !EvidenceAnchored) can NEVER be finalized:
		// a healthy finalize would otherwise overwrite the flag to true using only the
		// delegation.finalize anchor and resurrect a decision whose claim anchor is still
		// lost. Refusing here is safe inside the callback — this is a pre-transition READ
		// that has produced no audit, gap, or effect, so aborting rolls nothing back.
		if !claim.EvidenceAnchored {
			return ErrDelegationEvidenceFault
		}
		now := a.clock.Now()
		// A dead claim (past its single-use lifetime) is never finalized.
		if now.Time().Sub(claim.ClaimedAt.Time()) > a.decisionClaimLifetime() {
			return ErrDecisionExpired
		}
		// Pass only the minimal fields: the store forces state='final', stamps
		// finalized_at/updated_at from its own clock, self-guards the verdict material,
		// derives the audit actor from the claim's own immutable PEPServiceID, and emits
		// the delegation.finalize audit inside the same transaction (so this wrapper no
		// longer appends its own — a raw store finalize can never skip it, nor forge its
		// actor).
		dropped, err := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, verdictJSON, verdictHash, policyVersion)
		if err != nil {
			// The store's own tombstone backstop (a raw caller path the pre-read above
			// already blocks): an un-anchored pending claim can never be finalized.
			if errors.Is(err, store.ErrEvidenceMissing) {
				return ErrDelegationEvidenceFault
			}
			if errors.Is(err, store.ErrConflict) {
				// A concurrent finalize won the version-locked transition. Reload and
				// re-classify: an identical finalize is idempotent, anything else conflicts.
				reloaded, rerr := as.PDPDecisionClaims().Get(ctx, decisionID)
				if rerr != nil {
					return rerr
				}
				if reloaded.State == "final" {
					return classifyFinalized(reloaded, verdictHash, policyVersion)
				}
				return ErrDecisionFinalizeConflict
			}
			return err
		}
		evidenceDropped = dropped
		return nil
	})
	if mutErr != nil {
		return mutErr
	}
	// The pending→final transition + the durable gap accounting COMMITTED. A dropped
	// finalize audit leaves the decision without a per-operation anchor, so refuse the
	// observable finalization evidence-or-refuse — the final state stays durably recorded.
	if evidenceDropped {
		return ErrDelegationEvidenceFault
	}
	return nil
}

// classifyFinalized decides whether finalizing an already-final claim with the
// given verdict hash and policy version is an idempotent no-op, a contradiction, or a
// deny-closed evidence tombstone. Idempotency requires BOTH the hash AND the policy
// version to match: the same hash under a different policy version is a real
// contradiction, not a silent success. Even on an exact content match, a final row
// whose finalize audit was DROPPED under degrade (evidence_anchored==false) is a
// tombstone — the idempotent retry must keep refusing (ErrDelegationEvidenceFault)
// until the finalize anchors, never return nil. This reads a PERSISTED flag committed
// by a prior transaction, so returning it here does not roll back any gap accounting
// (these already-final paths take no new audit).
func classifyFinalized(claim model.PDPDecisionClaim, verdictHash, policyVersion string) error {
	if claim.VerdictHash != verdictHash || claim.PolicyVersion != policyVersion {
		return ErrDecisionFinalizeConflict
	}
	if !claim.EvidenceAnchored {
		return ErrDelegationEvidenceFault
	}
	return nil
}

// CheckDecisionServiceBinding is the service-binding primitive: it confirms a
// decision exists, belongs to the authenticated PEP's tenant and service, matches
// the presented nonce, and is within the claim lifetime. Every mismatch returns
// ONE indistinguishable error.
//
// This is ONLY the service-binding check. Phase-order enforcement (decide →
// activate → postflight → settle), obligation acknowledgement, and reservation
// settlement are NOT enforced here — they are the program's later protocol
// stages, which build their state machine on this same claim record.
func (a *Authenticator) CheckDecisionServiceBinding(ctx context.Context, decisionID model.ID, nonce string, pep PEPIdentity) error {
	nonceHash := sha256Hex(nonce)
	now := a.clock.Now()
	return a.st.AuthView(ctx, func(as store.AuthScope) error {
		claim, err := as.PDPDecisionClaims().Get(ctx, decisionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrDecisionBindingMismatch
			}
			return err
		}
		if claim.TargetTenantID != pep.Tenant() ||
			claim.PEPServiceID != pep.ServiceID() ||
			claim.NonceHash != nonceHash ||
			now.Time().Sub(claim.ClaimedAt.Time()) > a.decisionClaimLifetime() {
			return ErrDecisionBindingMismatch
		}
		// A decision with no durable per-operation anchor (its claim/finalize audit
		// dropped under degrade) is a deny-closed tombstone, not a usable binding.
		if !claim.EvidenceAnchored {
			return ErrDecisionBindingMismatch
		}
		return nil
	})
}

// SweepDelegation is the HANDLE-reap primitive ONLY: it reaps expired, UNCLAIMED
// handles past the grace period and NOTHING else. A claimed handle is retained, and
// no claim is ever deleted here (a pending claim is never reaped — it is the
// crash-recoverable single-use record). It is bounded by limit and returns the
// counts, and it does NOT run itself.
//
// The claim-retention GC (deleting final claims past the max(request-age, retry,
// decision-lifetime) window, keyed by the pdp_decision_claims (state, claimed_at)
// index) and the leader-gated pump that drives both sweeps on a schedule are wired
// by the later service stage, NOT this one. No background loop is started here.
func (a *Authenticator) SweepDelegation(ctx context.Context, now model.Timestamp, limit int) (SweepCounts, error) {
	if limit <= 0 {
		limit = defaultSweepLimit
	}
	cutoff := model.NewTimestamp(now.Time().Add(-a.delegationSweepGrace()))
	var counts SweepCounts
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		handles, _, err := as.DelegationHandles().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "expires_at", Op: model.OpLt, Value: cutoff.String()}},
			Limit:   limit,
		})
		if err != nil {
			return err
		}
		for _, h := range handles {
			claims, _, err := as.PDPDecisionClaims().List(ctx, byEq("handle_jti", h.ID.String(), 1))
			if err != nil {
				return err
			}
			if len(claims) > 0 {
				continue // claimed → retain (the claim owns the single-use jti)
			}
			if err := as.DelegationHandles().Delete(ctx, h.ID); err != nil {
				return err
			}
			counts.ExpiredHandlesDeleted++
		}
		return nil
	})
	return counts, err
}

// EffectiveCapabilities intersects the PEP's declared capabilities with what is
// registered for its service identity: effective = declared ∧ registered. A
// declared key OUTSIDE the SDK capability vocabulary (knownPEPCapabilities) is
// dropped silently and is never audited — it is attacker-controlled arbitrary
// text with no meaning to the engine. A declared key that IS in the vocabulary but
// is NOT registered is a genuine OVERCLAIM: it is dropped and returned separately
// for the caller to audit (a sanitized, bounded vocabulary), NEVER an automatic
// deny. A deny for a missing control (FailureCapabilityUnmet) is decided at VERDICT
// time when a decision REQUIRES a control absent from the effective set — a
// distinct decision from this pure intersection.
func EffectiveCapabilities(declared, registered map[string]bool) (effective, droppedOverclaims map[string]bool) {
	for name, want := range declared {
		if !want {
			continue
		}
		if !knownPEPCapabilities[name] {
			continue // unknown vocabulary → never effective, never audited
		}
		if registered[name] {
			if effective == nil {
				effective = map[string]bool{}
			}
			effective[name] = true
		} else {
			if droppedOverclaims == nil {
				droppedOverclaims = map[string]bool{}
			}
			droppedOverclaims[name] = true
		}
	}
	return effective, droppedOverclaims
}

// droppedAgainstStored recomputes the dropped-overclaim set for an idempotent
// retry so it is CONSISTENT with the stored decision rather than with the current
// service registration. A declared key from the closed SDK vocabulary that the
// stored decision did NOT make effective was dropped for this decision. The result
// is disjoint from storedEffective by construction, so a retry can never report a
// capability as both stored-effective and freshly dropped (e.g. after the
// capability was de-registered between the original claim and the retry). Unknown
// declared keys are excluded exactly as in EffectiveCapabilities.
func droppedAgainstStored(declared, storedEffective map[string]bool) map[string]bool {
	var dropped map[string]bool
	for _, k := range sortedPEPCapabilityKeys {
		if !declared[k] || storedEffective[k] {
			continue
		}
		if dropped == nil {
			dropped = map[string]bool{}
		}
		dropped[k] = true
	}
	return dropped
}

// PrincipalForDelegation builds the SUBJECT principal from a sealed
// VerifiedDelegation so the governance gates see the subject exactly as an
// equivalent DIRECT credential would present it.
//
// Principal construction justification (verified against
// cmd/olivares/inferenceproxy.go newResolvedIdentity):
//
//   - Kind MIRRORS the source credential kind (SourceCredKind). A user-source
//     delegation is a KindUser principal (the equivalent of the subject's own
//     session); a token-source delegation is a KindToken principal (the equivalent
//     of the subject's own API/exchanged token). newResolvedIdentity derives, for
//     KindUser, subjectKind="user" and subjectID=UserID (the SUBJECT user), and
//     for KindToken, subjectKind="token" and subjectID=CredID (the SUBJECT token)
//     — so the snapshot names the SUBJECT, never the handle row's JTI nor the PEP
//     service. This is the strongest reading of "as the equivalent direct
//     credential would be seen".
//   - UserID = the governed subject user (SubjectUserID). CredID = the subject's
//     revalidated SOURCE credential id (SourceCredID) — never the handle JTI, so
//     Actor()/subjectID can never surface the handle or the PEP.
//   - grants = {tenant: effectiveRole}: the subject's role clamped to the mint
//     ceiling. groups = {tenant: effectiveGroups} for a user principal only (a
//     token principal carries no directory groups — least privilege, exactly as
//     authToken builds it; a token-source mint captured an empty group closure).
//   - Superadmin is always false (mint refuses superadmin subjects — invariant #2).
//   - AAL 0 and AMR nil: a delegated handle carries NO human assurance (like an
//     API token), so any step-up gate treats it as not elevated (fail-closed).
//   - The stored, revalidated agent_ref binds through WithAgentIdentity (the only
//     path modelaccessgate F-01 binds an agent by); ActAs binds through WithActAs
//     for confused-deputy attribution, exactly as a token-exchanged credential does.
//
// ScopedPrincipal is PROHIBITED here: the subject is a full principal at its
// effective authority, not a synthetic scope.
func PrincipalForDelegation(v VerifiedDelegation) Principal {
	kind := KindUser
	if v.subjectCredKind == "token" {
		kind = KindToken
	}
	grants := map[model.TenantID]string{}
	if v.effectiveRole != "" {
		grants[v.tenant] = v.effectiveRole
	}
	var groups map[model.TenantID][]string
	if kind == KindUser && len(v.effectiveGroups) > 0 {
		groups = map[model.TenantID][]string{v.tenant: v.effectiveGroups}
	}
	p := newPrincipal(kind, v.subjectUserID, v.subjectCredID, false, "", grants, groups)
	p.AAL = 0
	p.AMR = nil
	if !v.actAs.IsZero() {
		p = p.WithActAs(v.actAs)
	}
	if v.agentRef != "" {
		p = p.WithAgentIdentity(v.agentRef)
	}
	return p
}

// SetDelegationPolicy configures the delegation-handle TTL, the request-freshness
// max age, and the future-skew tolerance (mirroring SetExchangePolicy). Any zero
// value keeps that dimension's safe default. Wired once from the composition root.
func (a *Authenticator) SetDelegationPolicy(ttl, maxAge, futureSkew time.Duration) {
	a.delegationTTLDur = ttl
	a.delegationMaxAgeDur = maxAge
	a.delegationFutureSkew = futureSkew
}

func (a *Authenticator) delegationTTL() time.Duration {
	if a.delegationTTLDur > 0 {
		return a.delegationTTLDur
	}
	return DefaultDelegationTTL
}

func (a *Authenticator) delegationMaxAge() time.Duration {
	if a.delegationMaxAgeDur > 0 {
		return a.delegationMaxAgeDur
	}
	return DefaultDelegationMaxAge
}

func (a *Authenticator) delegationFutureSkewDur() time.Duration {
	if a.delegationFutureSkew > 0 {
		return a.delegationFutureSkew
	}
	return DefaultDelegationFutureSkew
}

// SetDecisionClaimLifetime configures how long a decision claim answers the
// service-binding check and remains finalizable after ClaimedAt (a later protocol
// stage tunes this per deployment). A zero value keeps DefaultDecisionClaimLifetime.
func (a *Authenticator) SetDecisionClaimLifetime(d time.Duration) {
	a.decisionClaimLifetimeDur = d
}

func (a *Authenticator) decisionClaimLifetime() time.Duration {
	if a.decisionClaimLifetimeDur != 0 {
		return a.decisionClaimLifetimeDur
	}
	return DefaultDecisionClaimLifetime
}

func (a *Authenticator) delegationSweepGrace() time.Duration { return DefaultDelegationSweepGrace }

// checkFreshness rejects a missing, stale, or too-far-future IssuedAt (all replay).
func (a *Authenticator) checkFreshness(issuedAt time.Time, now model.Timestamp) error {
	if issuedAt.IsZero() {
		return ErrDelegationReplay
	}
	n := now.Time()
	if issuedAt.Before(n.Add(-a.delegationMaxAge())) {
		return ErrDelegationReplay
	}
	if issuedAt.After(n.Add(a.delegationFutureSkewDur())) {
		return ErrDelegationReplay
	}
	return nil
}

// validateOperations rejects an empty set and any unknown kind, returning the
// deduped allowlist.
func validateOperations(ops []string) ([]string, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("%w: at least one operation is required", ErrInvalidDelegationRequest)
	}
	out := make([]string, 0, len(ops))
	seen := map[string]bool{}
	for _, op := range ops {
		op = strings.TrimSpace(op)
		if !knownOperationKinds[op] {
			return nil, fmt.Errorf("%w: unknown operation kind %q", ErrInvalidDelegationRequest, op)
		}
		if seen[op] {
			continue
		}
		seen[op] = true
		out = append(out, op)
	}
	return out, nil
}

// maxPresentedNonceLen bounds a presented nonce: a well-behaved PEP uses a compact
// random nonce, so anything larger is malformed (defense against unbounded input).
const maxPresentedNonceLen = 512

// validatePresentedRequest rejects a presented request that lacks an essential
// binding, BEFORE any claim is taken, so a PEP can never register a sealed claim
// over an under-specified request. OperationKind is deliberately NOT checked here:
// it is validated against the handle's own allowlist inside the transaction.
func validatePresentedRequest(pr PresentedRequest) error {
	if pr.Nonce == "" || len(pr.Nonce) > maxPresentedNonceLen {
		return ErrDelegationProtocol
	}
	if pr.Model == "" {
		return ErrDelegationProtocol
	}
	if pr.ContentSize < 0 {
		return ErrDelegationProtocol
	}
	if !isCanonicalSHA256Hex(pr.ContentDigest) {
		return ErrDelegationProtocol
	}
	return nil
}

// isCanonicalSHA256Hex reports whether s is exactly 64 lowercase hex characters —
// the canonical form of a SHA-256 content digest (no algorithm prefix, no
// uppercase), matching sdk.ContentEnvelope.SHA256.
func isCanonicalSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// revalidatePEPCredential re-checks, inside the claim transaction, that the PEP
// transport credential a cached PEPIdentity was minted from is STILL live: the
// api_token exists, is not revoked or expired, still carries Purpose=="pep" and the
// service's tenant, and its service-credential mapping is still active and still
// names this exact service. This closes the reuse-after-revoke window in which an
// AuthenticatePEP result outlives the revocation, expiry, or unbind of its
// credential. Every failure returns ErrDelegationInvalid (indistinguishable).
func revalidatePEPCredential(ctx context.Context, as store.AuthScope, pep PEPIdentity, now model.Timestamp) error {
	token, err := as.Tokens().Get(ctx, pep.CredentialID())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrDelegationInvalid
		}
		return err
	}
	if token.Revoked ||
		token.Purpose != pepCredentialPurpose ||
		token.BoundTenantID != pep.Tenant() {
		return ErrDelegationInvalid
	}
	if token.ExpiresAt != nil && token.ExpiresAt.Before(now) {
		return ErrDelegationInvalid
	}
	bindings, _, err := as.PEPServiceCredentials().List(ctx, byEq("token_id", pep.CredentialID().String(), 1))
	if err != nil {
		return err
	}
	if len(bindings) != 1 ||
		bindings[0].DisabledAt != nil ||
		bindings[0].ServiceID != pep.ServiceID() {
		return ErrDelegationInvalid
	}
	return nil
}

// parseDelegationToken strictly parses an opaque handle token: length-capped,
// prefixed olvd, and selector/secret being exact-length base32 (StdEncoding
// alphabet). Any shape/alphabet/length problem is a protocol error; a well-formed
// token whose selector is unknown or whose secret is wrong is NOT rejected here
// (it fails indistinguishably at the secret comparison).
func parseDelegationToken(token string) (selector, secret string, err error) {
	if l := len(token); l == 0 || l > maxDelegationTokenLen {
		return "", "", ErrDelegationProtocol
	}
	prefix, sel, sec, ok := ParseToken(token)
	if !ok || prefix != PrefixDelegation {
		return "", "", ErrDelegationProtocol
	}
	selBytes, err := b32.DecodeString(sel)
	if err != nil || len(selBytes) != selectorBytes {
		return "", "", ErrDelegationProtocol
	}
	secBytes, err := b32.DecodeString(sec)
	if err != nil || len(secBytes) != secretBytes {
		return "", "", ErrDelegationProtocol
	}
	return sel, sec, nil
}

// lookupDelegationBySelector resolves a handle by its public selector.
func lookupDelegationBySelector(ctx context.Context, as store.AuthScope, selector string) (model.DelegationHandle, bool, error) {
	handles, _, err := as.DelegationHandles().List(ctx, byEq("selector", selector, 1))
	if err != nil {
		return model.DelegationHandle{}, false, err
	}
	if len(handles) == 0 {
		return model.DelegationHandle{}, false, nil
	}
	return handles[0], true, nil
}

// requestFingerprint is the domain-separated, length-framed SHA-256 over the
// canonical decision-binding fields (design §"Request fingerprint"). Length
// framing makes the encoding injective, so no two distinct field vectors can
// collide.
func requestFingerprint(handle model.DelegationHandle, pep PEPIdentity, tenant model.TenantID, audience string, pr PresentedRequest) string {
	h := sha256.New()
	h.Write([]byte(fingerprintDomain))
	writeField := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	writeField(delegationProtocolVersion)
	writeField(handle.ID.String())
	writeField(handle.SourceCredKind)
	writeField(handle.SubjectUserID.String())
	// Bind the FULL normative subject, not just kind + subject user: the exact
	// source credential, the act-as subject, and the agent identity all determine
	// who the decision is for, so a retry is bound to that exact subject.
	writeField(handle.SourceCredID.String())
	writeField(handle.ActAsUserID.String())
	writeField(handle.AgentRef)
	writeField(tenant.String())
	writeField(pep.ServiceID().String())
	writeField(audience)
	writeField(pr.OperationKind)
	writeField(pr.Model)
	writeField(boolField(pr.Stream))
	writeField(pr.ContentDigest)
	writeField(strconv.FormatInt(pr.ContentSize, 10))
	writeField(pr.MediaType)
	writeField(pr.Nonce)
	writeField(pr.IssuedAt.UTC().Format(time.RFC3339Nano))
	// Capability vector — injective over the closed SDK vocabulary. Write the count
	// of declared-true KNOWN capabilities, then each in sorted order as a
	// length-framed key plus a single 1 byte. Unknown declared keys are excluded, so
	// they cannot change the fingerprint; length framing (never a delimiter-joined
	// string) makes two distinct vocabulary vectors collision-free.
	declaredKnown := make([]string, 0, len(sortedPEPCapabilityKeys))
	for _, k := range sortedPEPCapabilityKeys {
		if pr.DeclaredCapabilities[k] {
			declaredKnown = append(declaredKnown, k)
		}
	}
	writeField(strconv.Itoa(len(declaredKnown)))
	for _, k := range declaredKnown {
		writeField(k)
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func boolField(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// lowerRole returns the role with the lower privilege rank — the ceiling holds:
// a subject promoted after mint cannot elevate through the handle.
func lowerRole(a, b string) string {
	if RoleRank(a) <= RoleRank(b) {
		return a
	}
	return b
}

// intersectStrings returns the elements present in both, preserving current's
// order and de-duplicating.
func intersectStrings(current, mint []string) []string {
	if len(current) == 0 || len(mint) == 0 {
		return nil
	}
	want := make(map[string]bool, len(mint))
	for _, m := range mint {
		want[m] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range current {
		if want[c] && !seen[c] {
			out = append(out, c)
			seen[c] = true
		}
	}
	return out
}

// containsString reports membership.
func containsString(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// sha256Hex returns the lowercase-hex SHA-256 of s (used for the persisted nonce
// hash — the raw nonce is never stored).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sha256HexBytes returns the lowercase-hex SHA-256 of b (verdict hash binding).
func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
