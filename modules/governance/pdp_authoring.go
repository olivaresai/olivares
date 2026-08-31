// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the C Cedar/OPA policy-as-code AUTHORING surface over the PDP,
// mounted at /v1/m/governance/pdp/. It EXPOSES the PDP (validate/explain/dry-run/
// versions/tests) and ACTIVATES an authored Cedar policy on the live hot path — it does
// NOT rewrite cedar.go/opa.go/the chain (those are ). Since the authored Cedar
// policy is a SCOPED-GRANT policy (grants.go): a permit GRANTS within the scope tree
// (workspace/agent-group/resource), a forbid still RESTRICTS, and it is enforced through
// the auth.ScopedAuthorizer seam — no longer the deny-only base-permit overlay it was.
// The per-tenant engine (m.grants) keeps the isolation guarantee: a tenant's policy
// applies only to that tenant's requests.
//
// OPA/Rego is AUTHORING + VERSIONING + EXPORT ONLY — never evaluated in-process, by
// design and permanently. The embedded
// PDP is Cedar; centralized decisions run through AuthZEN/COAZ. Rego is validated
// structurally here, versioned/audited on publish, and enforced by the operator's OWN
// OPA sidecar; the dry-run for OPA honestly abstains (no in-process restriction). Do NOT
// add a local Rego evaluator: vendoring the OPA engine into the AGPL core, or
// hand-rolling a partial one that silently diverges from real OPA, both cost more than
// they buy — see the contract for the full rationale.

// ReloadActivePDP re-activates a tenant's stored active Cedar policy on the live grant
// engine, so a published policy's activation is DURABLE across a process restart (the
// composition root calls this per tenant at boot — see boot.go). It makes the persisted
// active=true honest: after a restart the engine enforces the same policy the store
// records as active. DENY-CLOSED: a reload/backfill failure installs an operational
// unavailable state for that tenant, so both scoped authorization seams reject rather
// than serving a partial policy or silently omitting a forbid. A coherent later reload
// recovers that state. Safe to call before serving.
func (m *Module) ReloadActivePDP(ctx context.Context, tenant model.TenantID) error {
	if m.grants == nil {
		// A successful return would claim that a live evaluator exists when no
		// evaluator can install or enforce the durable snapshot.
		return errors.New("governance: ReloadActivePDP requires scoped grant evaluator")
	}
	if m.data == nil {
		// A boot/composition wiring error is not an empty policy. Leaving this
		// tenant absent would make Scoped abstain and restrict-view Evaluate allow,
		// which is exactly the cold fail-open C3 closes.
		before, beforeLoaded := m.grants.tenantState(tenant)
		m.grants.markUnavailableIfStillSame(tenant, before, beforeLoaded)
		return errors.New("governance: ReloadActivePDP requires module data")
	}
	// A legacy active policy may predate durable freshness. Under an effective bound,
	// establish that anchor BEFORE loading grants: otherwise a restart would make an
	// absent timestamp look fresh forever. The backfill locks, samples DB time, bumps the
	// generation and persists atomically; it is a once-only create/update of a missing
	// local anchor, never a re-stamp of an existing one.
	before, beforeLoaded := m.grants.tenantState(tenant)
	backfill, err := m.backfillLegacyCedarFreshness(ctx, tenant)
	if err != nil {
		// A failed backfill that acquired the authority lock has a durable
		// generation witness even though its transaction rolled back. It may have
		// failed after observing the policy that still lacks its required anchor;
		// a delayed exact-G replay proves neither freshness nor its audit. Fence
		// that generation rather than treating it as a token-only transient.
		if validPolicyAuthorizationEpochFact(tenant, backfill.lockedGeneration) {
			m.grants.markUnavailableForObservedGenerationFailure(tenant, before, beforeLoaded, backfill.lockedGeneration)
		} else {
			m.grants.markUnavailableIfStillSame(tenant, before, beforeLoaded)
		}
		if m.log != nil {
			m.log.Error("governance: active Cedar policy was not reactivated because bounded freshness could not be established",
				"tenant", tenant.String(), "err", err)
		}
		return err
	}
	// The live set is the UNION of free-form `cedar`, structured `cedar-managed` and
	// signed-bundle `cedar-ddil`. reloadTenantGrants captures union+selection+epoch+
	// freshness in one View and swaps one state; deny-closed failures leave no fabricated
	// applied claim.
	if err := m.reloadTenantGrants(ctx, tenant); err != nil {
		if m.log != nil {
			m.log.Warn("governance: stored active Cedar policy was not fully reactivated on boot (deny-closed; re-publish/reload to restore)",
				"tenant", string(tenant), "err", err)
		}
		return err
	}
	return nil
}

// cedarMutationInputs are the non-authored inputs to a prospective authored Cedar
// mutation. Callers hold lockPolicyAuthorizationEpoch before reading them; `selection`
// is completed with the newly selected authored revision after append/rollback.
type cedarMutationInputs struct {
	managed   string
	adopted   string
	selection activationID
	freshness FreshnessRecord
	signed    bool
}

func readCedarMutationInputs(ctx context.Context, sc store.Scope) (cedarMutationInputs, error) {
	// A publish replaces the authored surface, so its prospective union does not
	// depend on the prior authored bytes. Managed and adopted content do remain
	// authority inputs and must be read strictly under the caller's epoch lock.
	managed, managedRevision, _, err := latestActiveSelection(ctx, sc, surfaceCedarManaged)
	if err != nil {
		return cedarMutationInputs{}, err
	}
	adopted, adoptedRevision, adoptedFound, err := latestActiveSelection(ctx, sc, surfaceCedarDDIL)
	if err != nil {
		return cedarMutationInputs{}, err
	}
	freshness, signed, err := readLocalPolicyFreshnessAuthority(ctx, sc, adopted, adoptedFound)
	if err != nil {
		return cedarMutationInputs{}, err
	}
	return cedarMutationInputs{
		managed: managed, adopted: adopted,
		selection: activationID{managed: managedRevision, adopted: adoptedRevision},
		freshness: freshness, signed: signed,
	}, nil
}

func compileProspectiveCedar(authored string, inputs cedarMutationInputs) error {
	if _, err := compileGrantSet(mergeCedarSources(mergeCedarSources(authored, inputs.managed), inputs.adopted)); err != nil {
		return validationError("cedar policy would not compile in the active authored+managed+adopted union: " + err.Error())
	}
	return nil
}

// cedarExpectedState constructs the durable identity a Cedar writer committed. The
// compiled grantSet itself is intentionally absent: runtime comparison needs the exact
// authority inputs (generation, selection, union digest and full freshness tuple), not a
// mutable/textual approximation.
func (m *Module) cedarExpectedState(
	authored string,
	authoredRevision int64,
	inputs cedarMutationInputs,
	generation store.AuthorizationFactRef,
	freshness FreshnessRecord,
) scopedTenantState {
	selection := inputs.selection
	selection.authored = authoredRevision
	bound := time.Duration(0)
	if m.grants != nil {
		bound = m.grants.maxStaleness
	}
	if freshness.MaxStaleness > 0 {
		bound = freshness.MaxStaleness
	}
	return scopedTenantState{
		selection:      selection,
		generation:     generation,
		authoredDigest: contentDigest(authored),
		managedDigest:  contentDigest(inputs.managed),
		adoptedDigest:  contentDigest(inputs.adopted),
		unionDigest:    contentDigest(mergeCedarSources(mergeCedarSources(authored, inputs.managed), inputs.adopted)),
		freshness:      freshness,
		available:      true,
		freshnessValid: selection == (activationID{}) || bound <= 0 || !freshness.RefreshedAt.IsZero(),
	}
}

// sameCedarAuthorityState compares exactly the durable facts that define an installed
// Cedar snapshot. The opaque runtime operation token and compiled pointer are deliberately
// excluded; they are not durable authority and must not make an exact reload look stale.
func sameCedarAuthorityState(left, right scopedTenantState) bool {
	return left.generation == right.generation && left.sameIdentity(right) &&
		left.available == right.available && left.freshnessValid == right.freshnessValid
}

type cedarLiveState string

const (
	cedarLiveApplied     cedarLiveState = "applied"
	cedarLiveNewer       cedarLiveState = "newer"
	cedarLiveUnavailable cedarLiveState = "unavailable"
	cedarLiveMismatch    cedarLiveState = "mismatch"
	cedarLiveReloadError cedarLiveState = "reload_error"
)

// deferredCedarLiveState turns a failed confirmation into the closed audit/status
// vocabulary. The error itself remains only a local log field: it is not durable
// authorization evidence and is never copied into audit metadata.
func deferredCedarLiveState(outcome cedarLiveState, cause error) cedarLiveState {
	if cause != nil {
		return cedarLiveReloadError
	}
	switch outcome {
	case cedarLiveNewer, cedarLiveUnavailable, cedarLiveMismatch, cedarLiveReloadError:
		return outcome
	default:
		return cedarLiveMismatch
	}
}

// sameCedarRuntimeCapture establishes whether no live install/mark/replay occurred across
// a durable read. The opaque operation token changes for EVERY runtime operation, even an
// exact same-G replay, so equal durable facts alone cannot hide an interleaving.
func sameCedarRuntimeCapture(
	before scopedTenantState,
	beforeLoaded bool,
	after scopedTenantState,
	afterLoaded bool,
) bool {
	return beforeLoaded == afterLoaded &&
		(!beforeLoaded || (before.operation != nil && before.operation == after.operation))
}

// confirmCommittedCedarLive is the only post-commit `applied` proof for a writer. It
// brackets one coherent durable View with two live captures and accepts applied only if
// the runtime operation token stayed unchanged and BOTH captures exactly match the
// committed state. `reload == nil` is insufficient: another writer may commit/reload G+1
// between any two steps, and returning applied for G would be a fabricated claim.
func (m *Module) confirmCommittedCedarLive(
	ctx context.Context,
	tenant model.TenantID,
	expected scopedTenantState,
) (cedarLiveState, error) {
	if m.data == nil || m.grants == nil {
		return cedarLiveUnavailable, nil
	}
	before, beforeLoaded := m.grants.tenantState(tenant)
	var durable cedarDurableSnapshot
	viewErr := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var readErr error
		durable, readErr = readCedarDurableSnapshot(ctx, sc, tenant, m.grants.maxStaleness)
		return readErr
	})
	after, afterLoaded := m.grants.tenantState(tenant)
	if viewErr != nil {
		return cedarLiveReloadError, viewErr
	}
	if !sameCedarAuthorityState(durable.state, expected) {
		if durable.state.generation.Version > expected.generation.Version {
			return cedarLiveNewer, nil
		}
		return cedarLiveMismatch, nil
	}
	if !sameCedarRuntimeCapture(before, beforeLoaded, after, afterLoaded) {
		return cedarLiveMismatch, nil
	}
	if !beforeLoaded || !before.available || !after.available {
		return cedarLiveUnavailable, nil
	}
	if !sameCedarAuthorityState(before, expected) || !sameCedarAuthorityState(after, expected) {
		return cedarLiveMismatch, nil
	}
	if !hasCedarCompiledBinding(before) || !hasCedarCompiledBinding(after) {
		return cedarLiveMismatch, nil
	}
	bound := m.grants.maxStaleness
	if expected.freshness.MaxStaleness > 0 {
		bound = expected.freshness.MaxStaleness
	}
	if bound > 0 && !expected.freshnessValid {
		return cedarLiveUnavailable, nil
	}
	return cedarLiveApplied, nil
}

// cedarBackfillObservation retains the exact witness that was locked before a legacy
// freshness backfill failed. The surrounding Mutate rolls back a failed CAS and anchor,
// but it cannot erase the fact that this reload observed authority generation G while
// it was unable to establish the freshness evidence required to serve it.
type cedarBackfillObservation struct {
	lockedGeneration store.AuthorizationFactRef
}

// backfillLegacyCedarFreshness closes the last reboot-evasion path for policies that
// existed before freshness was introduced. It changes state only when an ACTIVE Cedar
// selection has an effective bound and lacks its local anchor. The epoch CAS is its first
// write; DB time, freshness and the CAS share this one Mutate.
func (m *Module) backfillLegacyCedarFreshness(ctx context.Context, tenant model.TenantID) (cedarBackfillObservation, error) {
	var observed cedarBackfillObservation
	if m.data == nil || m.grants == nil || m.grants.maxStaleness <= 0 {
		return observed, nil
	}
	liveBefore, liveBeforeLoaded := m.grants.tenantState(tenant)
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		locked, err := lockPolicyAuthorizationEpochWitness(ctx, sc)
		if err != nil {
			return err
		}
		// Preserve this witness outside the transaction. If a subsequent policy
		// read/compile/clock/CAS/audit step fails, the transaction rolls back its
		// writes but a delayed same-G replay still cannot establish the missing
		// freshness evidence. ReloadActivePDP uses this fact as an incomplete
		// generation fence.
		observed.lockedGeneration = locked
		// A delayed/decorated boot transaction must not compile or backfill an
		// authority snapshot older than the runtime this process already holds.
		// `locked` is the very witness lockPolicy... read and fenced; do not use
		// a second read here, which could itself be decorable. Returning no-op lets
		// reloadTenantGrants apply its matching lower-G monotonic fence.
		if liveBeforeLoaded && validPolicyAuthorizationEpochFact(tenant, liveBefore.generation) &&
			locked.Version < liveBefore.generation.Version {
			return nil
		}
		free, authoredRevision, _, err := latestActiveSelection(ctx, sc, surfaceCedar)
		if err != nil {
			return err
		}
		inputs, err := readCedarMutationInputs(ctx, sc)
		if err != nil {
			return err
		}
		selection := inputs.selection
		selection.authored = authoredRevision
		if selection == (activationID{}) {
			return nil
		}
		bound := m.grants.maxStaleness
		if inputs.freshness.MaxStaleness > 0 {
			bound = inputs.freshness.MaxStaleness
		}
		if bound <= 0 || !inputs.freshness.RefreshedAt.IsZero() {
			return nil // existing signed/local anchor: never re-stamp on boot
		}
		if err := compileProspectiveCedar(free, inputs); err != nil {
			return err
		}
		sampled, err := sampleLocalPolicyFreshness(ctx, sc, inputs.freshness, inputs.signed)
		if err != nil {
			return err
		}
		generation, err := advancePolicyAuthorizationEpochFrom(ctx, sc, locked)
		if err != nil {
			return err
		}
		if err := persistSampledLocalPolicyFreshness(ctx, sc, sampled, inputs.signed); err != nil {
			return err
		}
		freshnessRepo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			return err
		}
		freshnessRow, found, err := findOne(ctx, freshnessRepo)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("governance: policy freshness backfill did not leave an auditable singleton")
		}
		freshnessID := model.ID(freshnessRow.String(model.ColID))
		if freshnessID.IsZero() {
			return errors.New("governance: policy freshness backfill singleton has no record id")
		}
		event, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      model.ActorSystem,
			ActorKind:  model.ActorSystem,
			Action:     "governance.policy_freshness_backfill",
			TargetKind: policyFreshnessKind,
			TargetID:   freshnessID,
			Meta: map[string]any{
				"authorization_epoch": generation.Version,
				"selection": map[string]int64{
					"authored": selection.authored,
					"managed":  selection.managed,
					"adopted":  selection.adopted,
				},
				"db_timestamp": sampled.RefreshedAt.UTC().Format(time.RFC3339Nano),
			},
		})
		if err != nil {
			return err
		}
		if event.Seq <= 0 {
			return errors.New("governance: policy freshness backfill audit was not durably appended")
		}
		return nil
	})
	return observed, err
}

// --- DTOs (mirror the frontend) ----------------------------------------------

type pdpEngineSourceBody struct {
	Engine string `json:"engine"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

type pdpRequestBody struct {
	Engine  string            `json:"engine"`
	Source  string            `json:"source"`
	Request pdpExampleRequest `json:"request"`
}

type pdpExampleRequest struct {
	Principal struct {
		Kind string `json:"kind"`
		ID   string `json:"id,omitempty"`
	} `json:"principal"`
	Permission string `json:"permission"`
	Tenant     string `json:"tenant,omitempty"`
	Resource   struct {
		Kind        string            `json:"kind"`
		ID          string            `json:"id,omitempty"`
		Sensitivity string            `json:"sensitivity,omitempty"`
		Extra       map[string]string `json:"extra,omitempty"`
	} `json:"resource"`
}

type pdpDiag struct {
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error | warning
}

// pdpValidateResult reports whether a source would be ACCEPTED, plus everything
// the pre-check noticed. OK must agree with the publish gate (handlePdpPublish
// uses hasError on the very same diagnostics): it used to be len(diags)==0, and
// because validateRego always appends its "structural pre-check only" WARNING,
// every Rego document on earth came back ok:false while publish accepted it. That
// made the honest caveat indistinguishable from a real compile failure — the third
// answer served as the second.
type pdpValidateResult struct {
	OK          bool      `json:"ok"`
	Diagnostics []pdpDiag `json:"diagnostics"`
}

type pdpChainEntry struct {
	Rule    string `json:"rule"`
	Effect  string `json:"effect"` // permit | forbid | base
	Matched bool   `json:"matched"`
}

type pdpDecision struct {
	// Evaluated says whether a decision was actually COMPUTED from the source. It
	// is false for OPA, where nothing can be evaluated in-process, and it is the
	// only field that separates "the policy permits this" from "no policy ran".
	//
	// ALWAYS EMITTED (no omitempty) and placed FIRST because Allow alone cannot
	// carry three answers: the OPA branch has to return allow:true (the PDP layer
	// imposes no restriction — RBAC still governs), and a client reading only
	// `allow` therefore cannot tell a grant from an abstention it never measured.
	// A probe that answers the same for every input has not measured anything;
	// this field is what makes that visible in the payload instead of in prose.
	Evaluated bool            `json:"evaluated"`
	Allow     bool            `json:"allow"`
	Reason    string          `json:"reason"`
	Engine    string          `json:"engine"`
	Chain     []pdpChainEntry `json:"chain,omitempty"`
}

type pdpTestStatus struct {
	Engine    string          `json:"engine"`
	Revision  int64           `json:"revision,omitempty"`
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"`
	Passed    int             `json:"passed"`
	Failed    int             `json:"failed"`
	Total     int             `json:"total"`
	Results   []pdpTestResult `json:"results,omitempty"`
}

type pdpTestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// Live-activation outcomes. `active` says which revision the STORE selects; these
// say whether the running evaluator actually took it. They are distinct facts: a
// publish can commit and select a revision while this process cannot prove that exact
// snapshot is live (it may be unavailable, mismatched, or superseded). Callers must be
// able to tell those apart without parsing prose — for an authorization policy that
// difference is the whole point, so it is a field, not a sentence in `note`.
// ⛔ ALL THREE ARE FACTS ABOUT ONE PROCESS, AND THE COPY MUST SAY SO. The swap is
// m.grants.set(tenant, gs) in memory (scopedadmin.go), it emits no event
// (events.go), and the only external recomposition is boot (cmd/olivares/boot.go)
// — while the chart deploys several replicas (deploy/helm/olivares/values.yaml).
// So "applied" NEVER means "the estate is enforcing this"; it means "the process
// that served your request is". Writing it unqualified is a false claim about the
// product, and the canon forbids that without nuance. Whoever makes activation
// propagate between processes may widen this wording — until then it stays narrow.
const (
	// liveApplied: compiled and swapped on THIS PROCESS's evaluator. Says nothing
	// about any other replica.
	liveApplied = "applied"
	// liveDeferred: persisted and selected, but this process did not prove the
	// exact committed snapshot live. It intentionally says nothing about any
	// earlier evaluator still deciding requests.
	liveDeferred = "deferred"
	// liveNotApplicable: nothing is enforced from this process (OPA — the operator's
	// own sidecar owns Rego enforcement).
	liveNotApplicable = "not_applicable"
	// liveNoPolicy: the store selects NO Cedar surface at all, so there is no
	// activation to report. This is the state of every brand-new tenant, and it is
	// NOT `applied` — an adjective needs something to apply to. It was added after
	// a contrast reproduced the fresh-tenant GET answering "applied" with all three
	// surfaces absent, which put a green "enforcing here" on a tenant that had
	// never published anything.
	liveNoPolicy = "no_policy"
)

type pdpPublishResult struct {
	Engine   string `json:"engine"`
	Revision int64  `json:"revision"`
	Active   bool   `json:"active"`
	// LiveActivation is applied|deferred|not_applicable — see the constants above.
	LiveActivation string `json:"live_activation"`
	Note           string `json:"note,omitempty"`
}

type pdpRollbackBody struct {
	Engine   string `json:"engine"`
	Revision int64  `json:"revision"`
}

type pdpRollbackResult struct {
	Engine       string `json:"engine"`
	FromRevision int64  `json:"from_revision,omitempty"`
	ToRevision   int64  `json:"to_revision"`
	Active       bool   `json:"active"`
	// LiveActivation is applied|deferred|not_applicable — see the publish constants.
	LiveActivation string `json:"live_activation"`
	Note           string `json:"note,omitempty"`
}

// pdpActiveSurface is one contributing surface of the enforced policy. Content is
// populated ONLY for the authored surface — see handlePdpActive for why the managed
// and adopted projections disclose presence/revision/digest but not source.
type pdpActiveSurface struct {
	Present   bool   `json:"present"`
	Revision  int64  `json:"revision,omitempty"`
	Content   string `json:"content,omitempty"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type pdpActivePolicy struct {
	Engine   string           `json:"engine"`
	Authored pdpActiveSurface `json:"authored"`
	// Managed is the scoped-grant projection; Adopted is a signed DDIL bundle.
	// Both are cedar-only and both are absent-but-present-in-the-struct so a client
	// always sees the shape and can render "none" honestly.
	Managed     pdpActiveSurface `json:"managed"`
	Adopted     pdpActiveSurface `json:"adopted"`
	UnionSHA256 string           `json:"union_sha256,omitempty"`
	// LiveActivation is applied|deferred|not_applicable|no_policy — the publish
	// vocabulary plus no_policy, which only a READ can be in (a publish always has
	// a revision). It is the SAME vocabulary the
	// publish/rollback results use, and the reason this fact stops being readable
	// only in the response to the POST that caused it. Without it a console can
	// only remember the last publish in browser memory: a reload, a second
	// operator or a different replica all lose the one fact that says whether the
	// revision on screen is the one deciding requests.
	//
	// ALWAYS EMITTED (no omitempty): "" would be indistinguishable from an engine
	// that does not report it, and this field exists precisely so a client never
	// has to guess. It is scoped to THIS PROCESS by construction — see the note.
	LiveActivation string `json:"live_activation"`
	// GrantsExpired reports that this process is past the offline-staleness bound
	// for the tenant, so its POSITIVE grants have degraded to abstain while its
	// forbid rules stay enforced (ADR-0024 Q1, grants.go grantExpired).
	//
	// It is a SEPARATE axis from LiveActivation on purpose: the engine holds
	// exactly the selected policy — "applied" is true — and yet half of what that
	// policy says is not in force. Folding it into the enum would have made one of
	// the two facts unreadable, and reporting only "applied" would have been a
	// green badge over an expired grant. ALWAYS emitted, same reason as above.
	GrantsExpired bool   `json:"grants_expired"`
	Note          string `json:"note,omitempty"`
}

var errPdpRevisionNotFound = errors.New("pdp revision not found")

// normEngine validates and normalizes the engine selector.
func normEngine(e string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case surfaceCedar:
		return surfaceCedar, true
	case surfaceOPA:
		return surfaceOPA, true
	default:
		return "", false
	}
}

// buildAuthRequest maps a frontend example request onto the engine's auth.Request.
// An unknown principal kind defaults to token (the stricter, less-privileged kind).
func buildAuthRequest(in pdpExampleRequest, fallback model.TenantID) auth.Request {
	kind := auth.PrincipalKind(strings.TrimSpace(in.Principal.Kind))
	if kind != auth.KindUser && kind != auth.KindToken {
		kind = auth.KindToken
	}
	tenant := fallback
	if t, err := model.ParseTenantID(strings.TrimSpace(in.Tenant)); err == nil && !t.IsZero() {
		tenant = t
	}
	return auth.Request{
		Principal:  auth.Principal{Kind: kind, CredID: model.ID(in.Principal.ID)},
		Permission: auth.Permission(strings.TrimSpace(in.Permission)),
		Tenant:     tenant,
		Resource: auth.ResourceAttrs{
			Kind: in.Resource.Kind, ID: in.Resource.ID,
			Sensitivity: in.Resource.Sensitivity, Extra: in.Resource.Extra,
		},
	}
}

// --- handlers ----------------------------------------------------------------

// handlePdpValidate compiles a source WITHOUT loading it into the live evaluator. For
// Cedar it reuses NewCedarEvaluator (which fails LOUD on invalid source); for OPA it
// runs a structural Rego pre-check (authoritative compilation is the OPA sidecar's).
// It NEVER publishes.
func (m *Module) handlePdpValidate(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	var in pdpEngineSourceBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	diags := compileDiagnostics(engine, in.Source)
	// Same predicate the publish gate applies (handlePdpPublish): warnings do not
	// block, errors do. Anything else makes this route contradict the one it exists
	// to pre-check.
	writeJSON(w, http.StatusOK, pdpValidateResult{OK: !hasError(diags), Diagnostics: diags})
}

// compileDiagnostics returns the compile diagnostics for a source ([] = ok).
func compileDiagnostics(engine, source string) []pdpDiag {
	if strings.TrimSpace(source) == "" {
		return []pdpDiag{{Message: "policy source is empty", Severity: "error"}}
	}
	if engine == surfaceCedar {
		if _, err := NewCedarEvaluator(source, nil); err != nil {
			return []pdpDiag{{Message: err.Error(), Severity: "error"}}
		}
		return []pdpDiag{}
	}
	return validateRego(source)
}

// validateRego is a structural pre-check for Rego (the embedded engine is Cedar; OPA
// runs as a sidecar, so authoritative Rego compilation is the sidecar's — this catches
// the obvious mistakes locally and says so).
func validateRego(source string) []pdpDiag {
	var diags []pdpDiag
	s := strings.TrimSpace(source)
	if !strings.Contains(s, "package ") {
		diags = append(diags, pdpDiag{Message: "rego must declare a package", Severity: "error"})
	}
	if !bracketsBalanced(s) {
		diags = append(diags, pdpDiag{Message: "unbalanced braces/parentheses/brackets", Severity: "error"})
	}
	diags = append(diags, pdpDiag{
		Message:  "structural pre-check only — authoritative Rego compilation is performed by the OPA sidecar (the deployment's external PDP)",
		Severity: "warning",
	})
	return diags
}

// bracketsBalanced reports whether (), {} and [] are balanced (ignoring strings would
// need a full lexer; this is a cheap structural sanity check, not a parser).
func bracketsBalanced(s string) bool {
	var stack []rune
	pairs := map[rune]rune{')': '(', '}': '{', ']': '['}
	for _, c := range s {
		switch c {
		case '(', '{', '[':
			stack = append(stack, c)
		case ')', '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[c] {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

// handlePdpExplain evaluates an example request against a CANDIDATE source and returns
// the three-valued decision chain: a matched permit GRANTS within scope, a matched
// forbid RESTRICTS, neither abstains (the RBAC decision stands). Scope-tree conditions are
// resolved against the live hierarchy only by the activated engine, not this dry-run.
func (m *Module) handlePdpExplain(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.pdpEvaluateCandidate(w, r, mc)
}

// handlePdpDryRun is identical to explain (both evaluate an unpublished candidate); the
// distinct route exists so the UI can label the intent.
func (m *Module) handlePdpDryRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.pdpEvaluateCandidate(w, r, mc)
}

func (m *Module) pdpEvaluateCandidate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in pdpRequestBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	req := buildAuthRequest(in.Request, mc.Tenant)
	if engine == surfaceOPA {
		// The authored Rego is not deployed to the sidecar, so a CANDIDATE cannot be
		// evaluated in-process. Be honest: the PDP layer imposes no restriction here
		// (deny-only neutral) — RBAC still governs. Never imply the policy grants access.
		// Evaluated:false is the whole point — Allow:true here means "the PDP layer
		// imposes no restriction", NOT "the policy grants access", and without a
		// field saying so this route returns the identical answer for every Rego
		// document ever submitted.
		writeJSON(w, http.StatusOK, pdpDecision{
			Evaluated: false,
			Allow:     true,
			Engine:    surfaceOPA,
			Reason:    "OPA candidate evaluation requires the OPA sidecar (the authored Rego is not deployed there yet); the PDP layer imposes no restriction here — RBAC still governs. Nothing was evaluated: this answer is the same for every Rego source. Validate the syntax, then deploy the policy to OPA to evaluate it against a request.",
		})
		return
	}
	gs, err := compileGrantSet(in.Source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("cedar source does not compile: "+err.Error()))
		return
	}
	// a candidate is dry-run WITHOUT the live scope tree (it is not
	// activated, and an example request carries no real entity to resolve), so a
	// scope-tree condition (`resource in Workspace::…`) is evaluated only against the
	// example's attributes. The effect is the three-valued grant/forbid/abstain.
	effect, _ := evalGrantBasic(gs, req, m.clock.Now().Time())
	writeJSON(w, http.StatusOK, pdpDecision{
		Evaluated: true,
		Allow:     effect != auth.EffectForbid,
		Engine:    surfaceCedar,
		Reason:    grantEffectReason(effect),
		Chain: []pdpChainEntry{
			{Rule: "operator permit rules (scoped grant)", Effect: "permit", Matched: effect == auth.EffectGrant},
			{Rule: "operator forbid rules (restriction)", Effect: "forbid", Matched: effect == auth.EffectForbid},
		},
	})
}

// grantEffectReason frames a dry-run grant decision in the three-valued terms so
// the UI reflects that a policy can now GRANT, not only restrict.
func grantEffectReason(effect auth.Effect) string {
	switch effect {
	case auth.EffectGrant:
		return "a permit rule matched — the policy GRANTS this request within its scope (a positive grant beyond the RBAC baseline). Scope-tree conditions (workspace/agent-group/folder) are evaluated against the live hierarchy only by the activated engine; this dry-run uses the example's attributes."
	case auth.EffectForbid:
		return "a forbid rule matched — the policy RESTRICTS this request (it overrides any RBAC or scoped grant)"
	default:
		return "no permit or forbid matched — the policy abstains; the RBAC decision stands"
	}
}

// handlePdpVersions lists the cedar + opa authored revisions (the shared revision
// store, kinds cedar/opa).
func (m *Module) handlePdpVersions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		for _, surface := range []string{surfaceCedar, surfaceOPA} {
			revs, e := listRevisions(r.Context(), sc, surface)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, revs...)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePdpGetVersion returns ONE stored revision WITH its content. /pdp/versions is
// metadata-only (revision.go:152), so without this a console can only diff against text
// it happens to be holding in the current browser session — never against what is
// actually stored, and never at all after a rollback or from a second operator's machine.
//
// engine is a REQUIRED query param: revision numbers are per-surface (revision.go:87),
// so cedar r1 and opa r1 both exist and are different documents. Read tier — the same
// permission that already lists the revisions.
func (m *Module) handlePdpGetVersion(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	engine, ok := normEngine(r.URL.Query().Get("engine"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	num, perr := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "revision")), 10, 64)
	if perr != nil || num < 1 {
		writeJSON(w, http.StatusBadRequest, errorBody("revision must be a positive integer"))
		return
	}
	var out revisionDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var readErr error
		out, found, readErr = getRevision(r.Context(), sc, engine, num)
		if readErr != nil || !found {
			return readErr
		}
		// getRevision reports the row's LEGACY active flag (revision.go:66), which is a
		// frozen publication fact, not the current selection. Re-derive from the
		// activation stream so this route cannot disagree with /pdp/versions.
		active, hasActive, aerr := activeRevisionNumber(r.Context(), sc, engine)
		if aerr != nil {
			return aerr
		}
		out.Active = hasActive && active == num
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("policy revision not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePdpActive reports what the STORE currently selects for an engine, and — for
// cedar — discloses the other surfaces that are unioned into the enforced policy.
//
// Why the other surfaces are disclosed WITHOUT their content: the enforced Cedar set is
// free-form `cedar` ∪ `cedar-managed` (the RBAC projection) ∪ `cedar-ddil` (an
// adopted signed bundle) — scopedadmin.go:789-806. An operator editing the free-form
// surface is therefore never editing the whole enforced policy, and a diff that implied
// otherwise would be a half-truth. But returning the managed projection's SOURCE here
// would let `governance:policy:read` read scoped-grant detail that today requires
// `governance:rbac:read` (governance.go). Publishing never changes those surfaces, so
// their content is not needed to show what a publish would change — presence, revision
// and digest are enough to say honestly "there is more in force than what you edit".
//
// This reflects the STORE, not this process's loaded set: nothing exposes the in-memory
// engine. When the last publish/rollback returned live_activation "applied", the two
// agreed at that moment; `live_activation: "deferred"` is the signal that they may not.
func (m *Module) handlePdpActive(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	engine, ok := normEngine(r.URL.Query().Get("engine"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	out := pdpActivePolicy{Engine: engine}
	var cedarSnapshot cedarDurableSnapshot
	deploymentBound := time.Duration(0)
	if m.grants != nil {
		deploymentBound = m.grants.maxStaleness
	}
	// Bracket the one durable View with live captures. A cache swap can race a read
	// without touching the store; only an unchanged opaque operation token proves
	// that a point during the View had the exact durable/runtime pairing. The after
	// capture also supplies grants_expired, so GET never combines G's policy with
	// G+1's freshness.
	var liveBefore scopedTenantState
	var liveBeforeLoaded bool
	if engine == surfaceCedar && m.grants != nil {
		liveBefore, liveBeforeLoaded = m.grants.tenantState(mc.Tenant)
	}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if engine != surfaceCedar {
			num, has, e := activeRevisionNumber(r.Context(), sc, engine)
			if e != nil {
				return e
			}
			if !has {
				return nil
			}
			rev, found, e := getRevision(r.Context(), sc, engine, num)
			if e != nil {
				return e
			}
			if !found {
				return fmt.Errorf("selected OPA revision %d is absent", num)
			}
			out.Authored = pdpActiveSurface{Present: true, Revision: num, Content: rev.Content,
				Author: rev.Author, CreatedAt: rev.CreatedAt, SHA256: contentDigest(rev.Content)}
			return nil
		}

		// One coherent durable snapshot supplies every fact that `applied` compares:
		// authored/managed/DDIL content+selection, exact epoch and full freshness tuple.
		// Do not reconstruct a digest or bound in a later View.
		var e error
		cedarSnapshot, e = readCedarDurableSnapshot(r.Context(), sc, mc.Tenant, deploymentBound)
		if e != nil {
			return e
		}
		selection := cedarSnapshot.state.selection
		if selection.authored != 0 {
			// readCedarDurableSnapshot already read this exact selected record while
			// it captured content, epoch, freshness and the compiled-union digest.
			// Do not issue a third revision lookup here: a decorator could return
			// different bytes and make this response display B with A's digest/live
			// proof. Carrying the DTO makes the response one coherent snapshot too.
			rev := cedarSnapshot.authoredRevision
			if rev.Revision != selection.authored || contentDigest(rev.Content) != cedarSnapshot.state.authoredDigest {
				return fmt.Errorf("selected Cedar revision %d is inconsistent with durable snapshot", selection.authored)
			}
			out.Authored = pdpActiveSurface{Present: true, Revision: selection.authored, Content: rev.Content,
				Author: rev.Author, CreatedAt: rev.CreatedAt, SHA256: contentDigest(cedarSnapshot.authored)}
		}
		if selection.managed != 0 {
			out.Managed = pdpActiveSurface{Present: true, Revision: selection.managed, SHA256: contentDigest(cedarSnapshot.managed)}
		}
		if selection.adopted != 0 {
			out.Adopted = pdpActiveSurface{Present: true, Revision: selection.adopted, SHA256: contentDigest(cedarSnapshot.adopted)}
		}
		out.UnionSHA256 = cedarSnapshot.state.unionDigest
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var liveAfter scopedTenantState
	var liveAfterLoaded bool
	if m.grants != nil {
		liveAfter, liveAfterLoaded = m.grants.tenantState(mc.Tenant)
	}
	out.LiveActivation = m.liveActivationForState(
		engine,
		cedarSnapshot.state,
		liveBefore,
		liveBeforeLoaded,
		liveAfter,
		liveAfterLoaded,
	)
	if engine == surfaceCedar && m.grants != nil {
		out.GrantsExpired = m.grants.grantExpiredState(liveAfter, liveAfterLoaded, m.grants.clock())
	}
	if engine == surfaceCedar {
		out.Note = "the enforced Cedar policy is the UNION of the authored, managed and adopted surfaces; publishing replaces only the authored one. live_activation compares the exact durable snapshot selected above against the one THIS PROCESS stably holds: applied means this process is deciding with it, deferred means that exact snapshot is not currently available here (for example, this process is reloading, still holds an earlier selection, or failed closed), and no_policy means nothing is selected to be in force. It is a fact about this process only — another replica answers for itself. grants_expired is a separate axis for a loaded policy: past the offline-staleness bound its positive grants degrade to abstain while its forbid rules stay enforced; an unavailable snapshot remains deferred instead."
	} else {
		out.Note = "the selected revision in the authored OPA history. Nothing here is enforced in-process: your own OPA sidecar owns Rego enforcement."
	}
	writeJSON(w, http.StatusOK, out)
}

// liveActivationFor MEASURES whether this process's evaluator holds the selection
// the store makes, rather than declaring it. reloadTenantGrants records the
// revision numbers it swapped in (scopedadmin.go), and handlePdpActive reads the
// same three numbers out of the store, so the comparison is between two
// activations — not between two strings that might happen to match.
//
// ⛔ IT USED TO COMPARE sha256(source), AND A CONTRAST BROKE IT IN TWO WAYS.
// appendRevision never deduplicates content, so publishing the SAME bytes twice
// and failing the second swap left the loaded text matching the store's union:
// the POST said `deferred` and a later GET said `applied`, about one publish. And
// `contentDigest("")` equals the empty union's digest, so a tenant that had never
// loaded anything was indistinguishable from one loaded with nothing — every
// brand-new tenant read back as `applied`. Both were reproduced before this fix
// and are pinned below by tests. Do not go back to comparing text.
//
// liveActivationForState compares a durable store snapshot with live captures made on
// both sides of that View. The opaque operation token changes for every install, exact
// replay and unavailable mark, so a stable token and matching snapshots establish a
// real point at which durable and runtime authority agreed. grants_expired is derived
// from the after capture by the caller; it is never taken from a separate cache read.
func (m *Module) liveActivationForState(
	engine string,
	stored scopedTenantState,
	before scopedTenantState,
	beforeLoaded bool,
	after scopedTenantState,
	afterLoaded bool,
) string {
	if engine != surfaceCedar {
		return liveNotApplicable
	}
	// A reload/boot failure is represented by an operational unavailable sentinel.
	// It must dominate even an empty store selection: the process cannot claim that
	// the snapshot it just failed to establish is live.
	if (beforeLoaded && !before.available) || (afterLoaded && !after.available) {
		return liveDeferred
	}
	if !sameCedarRuntimeCapture(before, beforeLoaded, after, afterLoaded) {
		return liveDeferred
	}
	if stored.selection == (activationID{}) {
		// The store selects nothing. If this process is nevertheless holding some
		// earlier selection, it is enforcing a policy the store has withdrawn — that
		// is exactly `deferred`, and it is the one case here that must still warn.
		if !afterLoaded {
			return liveNoPolicy
		}
		if !sameCedarAuthorityState(before, stored) || !sameCedarAuthorityState(after, stored) ||
			!hasCedarCompiledBinding(before) || !hasCedarCompiledBinding(after) {
			return liveDeferred
		}
		return liveNoPolicy
	}
	// Never loaded here, while the store DOES select something: this process is not
	// enforcing it. True of a replica that booted before the publish, and the
	// direction that warns.
	if !beforeLoaded || !afterLoaded {
		return liveDeferred
	}
	if !sameCedarAuthorityState(before, stored) || !sameCedarAuthorityState(after, stored) ||
		!hasCedarCompiledBinding(before) || !hasCedarCompiledBinding(after) {
		return liveDeferred
	}
	bound := m.grants.maxStaleness
	if stored.freshness.MaxStaleness > 0 {
		bound = stored.freshness.MaxStaleness
	}
	// A bounded policy without a proven anchor is intentionally installed as
	// unavailable, not as an expired permit set. It is not an applied authority
	// response, and grants_expired remains false until a loaded snapshot can expire.
	if bound > 0 && !stored.freshnessValid {
		return liveDeferred
	}
	return liveApplied
}

// contentDigest returns sha256:<hex> of a policy surface's content, or "" when absent.
func contentDigest(content string) string {
	if content == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// handlePdpTests REFLECTS the stored compile/validation artifact for one immutable
// policy revision; it never runs policy tests in the request path. When revision is
// omitted, the newest revision is selected. Absence is an honest 200 available=false.
func (m *Module) handlePdpTests(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	engine, ok := normEngine(r.URL.Query().Get("engine"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}

	requested := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("revision")); raw != "" {
		var err error
		requested, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || requested <= 0 {
			writeJSON(w, http.StatusBadRequest, errorBody("revision must be a positive integer"))
			return
		}
	}

	var artifact revisionDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if requested > 0 {
			var readErr error
			artifact, found, readErr = getRevision(r.Context(), sc, engine, requested)
			return readErr
		}
		revisions, readErr := listRevisions(r.Context(), sc, engine)
		if readErr != nil {
			return readErr
		}
		if len(revisions) == 0 {
			return nil
		}
		artifact, found = revisions[0], true
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		reason := fmt.Sprintf("no stored test artifact is available for any %s revision", engine)
		if requested > 0 {
			reason = fmt.Sprintf("no stored test artifact is available for %s revision %d", engine, requested)
		}
		writeJSON(w, http.StatusOK, pdpTestStatus{Engine: engine, Revision: requested, Available: false, Reason: reason})
		return
	}

	// validated is the durable, per-revision result written by the
	// compile/validate publish gate. Reflect that stored fact; do not fabricate a
	// broader behavioral suite that was never run.
	result := pdpTestResult{
		Name:   "publish_compile_validate",
		Passed: artifact.Validated,
		Detail: "stored result of the compile/validation gate run before this immutable revision was committed",
	}
	status := pdpTestStatus{
		Engine: engine, Revision: artifact.Revision, Available: true, Total: 1,
		Results: []pdpTestResult{result},
	}
	if result.Passed {
		status.Passed = 1
	} else {
		status.Failed = 1
	}
	writeJSON(w, http.StatusOK, status)
}

// deferredActivationNote states only the measured fact: the exact committed Cedar
// snapshot is not confirmed on this process. It deliberately does NOT claim a previous
// policy is enforcing — an unavailable sentinel or a newer generation makes that unknown.
const deferredActivationNote = "committed and selected in the store, but this process did not confirm the exact committed Cedar snapshot as live. The runtime state is recorded as deferred; there is no HTTP endpoint that reloads the PDP on demand."

// reloadGrants swaps the live grant engine, honoring the test seam.
func (m *Module) reloadGrants(ctx context.Context, tenant model.TenantID) error {
	if m.reloadGrantsFn != nil {
		return m.reloadGrantsFn(ctx, tenant)
	}
	return m.reloadTenantGrants(ctx, tenant)
}

// auditActivationDeferred is compensatory runtime evidence, not an authority writer or
// a condition of `applied`: reload happens only after the authority transaction commits.
// Its closed state enum records why the exact committed snapshot was not confirmed without
// treating an error string or an assumed previous policy as authorization evidence.
func (m *Module) auditActivationDeferred(
	ctx context.Context,
	mc api.ModuleContext,
	engine string,
	revision int64,
	state cedarLiveState,
	cause error,
) {
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	if m.log != nil {
		m.log.Warn("pdp: revision committed but this process did not confirm its exact Cedar snapshot live",
			"tenant", mc.Tenant.String(), "engine", engine, "revision", revision, "state", state, "err", reason)
	}
	// Best-effort, and deliberately NOT in the publish transaction: that tx already
	// committed. A failure here is logged at ERROR because it means the deferral is
	// invisible to the ledger.
	appendDeferred := func(sc store.Scope) error {
		return auditEvent(ctx, sc, mc, "governance.pdp.activation_deferred", revisionKind, "", map[string]any{
			"actor": mc.Principal.Actor(), "engine": engine, "revision": revision,
			"exact_committed_snapshot_enforcing": false, "state": string(state),
		})
	}
	var appendErr error
	if mc.Data != nil {
		// Prefer the request-pinned tenant scope: a handler may have a valid data
		// handle even when Module.UseData was missed, and compensatory evidence must
		// not disappear in exactly that fail-closed runtime state.
		appendErr = mc.Data.Mutate(ctx, appendDeferred)
	} else if m.data != nil {
		appendErr = m.data.Mutate(ctx, mc.Tenant, appendDeferred)
	} else {
		return
	}
	if appendErr != nil && m.log != nil {
		m.log.Error("governance: activation deferral could not be written to the audit ledger; the deferred window is recorded only in this process log",
			"tenant", mc.Tenant.String(), "revision", revision, "err", appendErr)
	}
}

// handlePdpRollback re-activates an existing immutable revision by appending an
// activation record. The target and the full Cedar union are compiled BEFORE that
// record or its audit event is written; any failure leaves the current activation
// unchanged. Activation and audit commit in the same store transaction.
func (m *Module) handlePdpRollback(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in pdpRollbackBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	if in.Revision <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("revision must be a positive integer"))
		return
	}

	var (
		fromRevision        int64
		committedGeneration store.AuthorizationFactRef
		committedState      scopedTenantState
		rollbackNoop        bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var lockedGeneration store.AuthorizationFactRef
		if engine == surfaceCedar {
			// The exact epoch lock comes BEFORE target/current/managed/adopted/freshness
			// reads. Every migrated Cedar writer shares it, so this rollback cannot
			// classify a union across authority snapshots.
			var lockErr error
			lockedGeneration, lockErr = lockPolicyAuthorizationEpochWitness(r.Context(), sc)
			if lockErr != nil {
				return lockErr
			}
		}
		target, found, readErr := getRevision(r.Context(), sc, engine, in.Revision)
		if readErr != nil {
			return readErr
		}
		if !found {
			return errPdpRevisionNotFound
		}
		if len(target.Content) > maxPolicyContentBytes {
			return validationError("policy source too large; activation refused")
		}
		if containsInlineKey(target.Content) {
			return validationError("policy source contains an inline credential; activation refused")
		}

		var hasCurrent bool
		fromRevision, hasCurrent, readErr = activeRevisionNumber(r.Context(), sc, engine)
		if readErr != nil {
			return readErr
		}
		if !hasCurrent {
			fromRevision = 0
		}
		if engine == surfaceCedar {
			inputs, err := readCedarMutationInputs(r.Context(), sc)
			if err != nil {
				return err
			}
			if hasCurrent && fromRevision == in.Revision {
				// A rollback to the selected revision is a real no-op: no epoch CAS,
				// no activation marker, freshness, audit or live reload. We still read
				// the exact lock witness to report the current live fact; rereading
				// after the lock could accept a decorable/superseded generation.
				committedGeneration = lockedGeneration
				committedState = m.cedarExpectedState(target.Content, in.Revision, inputs, committedGeneration, inputs.freshness)
				rollbackNoop = true
				return nil
			}
			if err := compileProspectiveCedar(target.Content, inputs); err != nil {
				return err
			}
			// Sampling DB time before the CAS makes a missing/failed clock zero-write;
			// the CAS remains this transaction's first durable write. DDIL returns its
			// signed tuple without calling TransactionClock.
			sampled, err := sampleLocalPolicyFreshness(r.Context(), sc, inputs.freshness, inputs.signed)
			if err != nil {
				return err
			}
			committedGeneration, err = advancePolicyAuthorizationEpochFrom(r.Context(), sc, lockedGeneration)
			if err != nil {
				return err
			}
			committedState = m.cedarExpectedState(target.Content, in.Revision, inputs, committedGeneration, sampled)
			activationID, activateErr := activateRevision(r.Context(), sc, engine, in.Revision, mc.Principal.Actor())
			if activateErr != nil {
				return activateErr
			}
			if err := persistSampledLocalPolicyFreshness(r.Context(), sc, sampled, inputs.signed); err != nil {
				return err
			}
			return auditEvent(r.Context(), sc, mc, "governance.pdp.rollback", revisionKind, activationID, map[string]any{
				"actor": mc.Principal.Actor(), "engine": engine,
				"from_revision": fromRevision, "to_revision": in.Revision,
				"authorization_epoch": committedGeneration.Version,
			})
		}
		if hasCurrent && fromRevision == in.Revision {
			rollbackNoop = true
			return nil
		}
		if diags := validateRego(target.Content); hasError(diags) {
			return validationError("rego revision failed the structural pre-check; activation refused")
		}
		activationID, activateErr := activateRevision(r.Context(), sc, engine, in.Revision, mc.Principal.Actor())
		if activateErr != nil {
			return activateErr
		}
		return auditEvent(r.Context(), sc, mc, "governance.pdp.rollback", revisionKind, activationID, map[string]any{
			"actor": mc.Principal.Actor(), "engine": engine,
			"from_revision": fromRevision, "to_revision": in.Revision,
		})
	})
	if errors.Is(err, errPdpRevisionNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("policy revision not found; activation refused"))
		return
	}
	if message, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(message))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// activateRevision above ran for BOTH engines, so the store's selection really did
	// move in both cases: `active` must say so. Reporting false for OPA contradicted
	// /pdp/versions, which reads the same activation stream and would show this very
	// revision as active. Enforcement is a separate fact — live_activation carries it.
	res := pdpRollbackResult{
		Engine: engine, FromRevision: fromRevision, ToRevision: in.Revision, Active: true,
		LiveActivation: liveNotApplicable,
	}
	if rollbackNoop {
		if engine == surfaceCedar {
			outcome, confirmErr := m.confirmCommittedCedarLive(r.Context(), mc.Tenant, committedState)
			res.LiveActivation = liveDeferred
			if confirmErr == nil && outcome == cedarLiveApplied {
				res.LiveActivation = liveApplied
				res.Note = "requested revision was already selected; rollback performed zero durable writes and this process already holds the exact committed Cedar generation"
			} else {
				m.auditActivationDeferred(r.Context(), mc, engine, in.Revision, deferredCedarLiveState(outcome, confirmErr), confirmErr)
				res.Note = "requested revision was already selected; rollback performed zero authority writes, but this process does not hold the exact committed Cedar snapshot"
			}
		} else {
			res.Note = "requested revision was already selected; rollback performed zero durable writes and OPA remains externally enforced"
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if engine == surfaceCedar {
		reloadErr := m.reloadGrants(r.Context(), mc.Tenant)
		if reloadErr != nil && m.log != nil {
			m.log.Warn("pdp: post-rollback Cedar reload returned an error; confirming the exact durable/runtime state before responding",
				"tenant", mc.Tenant.String(), "revision", in.Revision, "err", reloadErr)
		}
		// A failed reload is not itself the classification: another writer may have
		// committed/reloaded G+1, or an exact state may already be live. The bracketed
		// confirmation is the only authority for `applied` and for the closed deferred
		// enum written below.
		outcome, confirmErr := m.confirmCommittedCedarLive(r.Context(), mc.Tenant, committedState)
		res.LiveActivation = liveDeferred
		if confirmErr == nil && outcome == cedarLiveApplied {
			res.LiveActivation = liveApplied
			res.Note = "compiled and re-activated on the live grant engine OF THIS PROCESS; immutable policy history was preserved. Other replicas pick it up on their own restart or next recomposition — nothing propagates the swap between processes"
		} else {
			m.auditActivationDeferred(r.Context(), mc, engine, in.Revision, deferredCedarLiveState(outcome, confirmErr), confirmErr)
			res.Note = deferredActivationNote
		}
	} else {
		res.Note = "revision selected in the authored history; OPA enforcement remains the external sidecar's and is not pushed from here"
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePdpPublish persists a versioned Cedar/OPA policy and, for Cedar, ACTIVATES it
// on the live hot path (recomposes the per-tenant overlay). Admin tier, CONFIRMED +
// AUDITED. DENY-CLOSED: a policy that fails compilation is rejected (no persist, no
// activation — the prior policy stands); the live overlay is swapped ONLY after the
// audited persist commits. OPA is versioned but not activated in-process (the OPA
// sidecar owns Rego enforcement) — stated honestly in the response.
func (m *Module) handlePdpPublish(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in pdpEngineSourceBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	if len(in.Source) > maxPolicyContentBytes {
		writeJSON(w, http.StatusBadRequest, errorBody("policy source too large"))
		return
	}
	if containsInlineKey(in.Source) {
		writeJSON(w, http.StatusBadRequest, errorBody("policy source must not contain an inline credential (sk-ant-…)"))
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}

	// Compile FIRST, before any persist. A Cedar policy that does not compile is
	// rejected here — it never reaches the store and never swaps the live engine
	// (deny-closed: the previously-active policy stands). The live swap below goes
	// through reloadTenantGrants, which re-reads the active revisions and unions the
	// free-form policy with `cedar-managed` projection (scopedadmin.go).
	if engine == surfaceCedar {
		if _, err := compileGrantSet(in.Source); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("cedar source does not compile; not activated: "+err.Error()))
			return
		}
	} else if diags := validateRego(in.Source); hasError(diags) {
		writeJSON(w, http.StatusBadRequest, errorBody("rego source failed the structural pre-check; not versioned"))
		return
	}

	// `active` is the STORE's selection and applies to both engines; whether anything
	// ENFORCES it is a separate fact carried by live_activation. Publishing advances
	// the same append-only selection stream rollback uses, so publish-after-rollback
	// selects the new revision without rewriting history.
	//
	// OPA selects too. It used to publish without advancing the stream, which left the
	// authored Rego history with no head: /pdp/active reported nothing selected right
	// after a publish, and a rollback — which always activates — then reported
	// active:false while /pdp/versions showed that same revision active. Those two
	// answers contradicted each other. Selecting on publish makes "which Rego revision
	// is current" answerable, without claiming this process enforces it.
	enforced := engine == surfaceCedar
	var (
		revision       int64
		committedState scopedTenantState
	)
	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		var (
			candidate           int64
			candidateGeneration store.AuthorizationFactRef
			candidateState      scopedTenantState
		)
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			var (
				inputs           cedarMutationInputs
				sampled          FreshnessRecord
				lockedGeneration store.AuthorizationFactRef
				err              error
			)
			if enforced {
				// Lock before ALL managed/adopted/freshness reads. A publish with the
				// same source bytes is still a new authored selection, so it always
				// takes this path and advances the durable authorization generation.
				if lockedGeneration, err = lockPolicyAuthorizationEpochWitness(r.Context(), sc); err != nil {
					return err
				}
				if inputs, err = readCedarMutationInputs(r.Context(), sc); err != nil {
					return err
				}
				if err = compileProspectiveCedar(in.Source, inputs); err != nil {
					return err
				}
				// DB time is sampled before the first write. A failed/missing clock
				// produces zero persisted effects; signed DDIL never samples local time.
				if sampled, err = sampleLocalPolicyFreshness(r.Context(), sc, inputs.freshness, inputs.signed); err != nil {
					return err
				}
				// FIRST write: the CAS fences all subsequent revision/activation/
				// freshness/audit writes in this transaction.
				if candidateGeneration, err = advancePolicyAuthorizationEpochFrom(r.Context(), sc, lockedGeneration); err != nil {
					return err
				}
			}
			num, id, aerr := appendRevision(r.Context(), sc, engine, in.Source, mc.Principal.Actor(), true, true, in.Note)
			if aerr != nil {
				return aerr
			}
			if _, aerr = activateRevision(r.Context(), sc, engine, num, mc.Principal.Actor()); aerr != nil {
				return aerr
			}
			if enforced {
				if err = persistSampledLocalPolicyFreshness(r.Context(), sc, sampled, inputs.signed); err != nil {
					return err
				}
				candidateState = m.cedarExpectedState(in.Source, num, inputs, candidateGeneration, sampled)
			}
			candidate = num
			meta := map[string]any{
				"engine": engine, "revision": num, "active": true, "enforced_here": enforced,
			}
			if enforced {
				meta["authorization_epoch"] = candidateGeneration.Version
			}
			return auditEvent(r.Context(), sc, mc, "governance.pdp.publish", revisionKind, id, meta)
		})
		if err == nil {
			revision = candidate
			committedState = candidateState
			break
		}
		if isConflict(err) {
			continue
		}
		if message, ok := asValidation(err); ok {
			writeJSON(w, http.StatusBadRequest, errorBody(message))
			return
		}
		writeStoreError(w, err)
		return
	}
	if revision == 0 {
		writeJSON(w, http.StatusConflict, errorBody("publish conflicted repeatedly; please retry"))
		return
	}

	// Activate the live grant engine ONLY after the audited persist committed (Cedar only).
	res := pdpPublishResult{Engine: engine, Revision: revision, Active: true, LiveActivation: liveNotApplicable}
	if enforced {
		// Re-read + union the active Cedar surfaces and attempt one atomic runtime swap.
		// A reload result alone cannot prove the exact committed snapshot is enforcing:
		// it may be unavailable, mismatched, or superseded before this response. The
		// live/durable bracket below makes that classification without assuming a
		// previous evaluator remains available.
		reloadErr := m.reloadGrants(r.Context(), mc.Tenant)
		if reloadErr != nil && m.log != nil {
			m.log.Warn("pdp: post-publish Cedar reload returned an error; confirming the exact durable/runtime state before responding",
				"tenant", mc.Tenant.String(), "revision", revision, "err", reloadErr)
		}
		// Do this even after reloadErr. A concurrent writer may have superseded the
		// attempted reload, and only this bracket distinguishes newer/mismatch/
		// unavailable from a durable read failure.
		outcome, confirmErr := m.confirmCommittedCedarLive(r.Context(), mc.Tenant, committedState)
		res.LiveActivation = liveDeferred
		if confirmErr == nil && outcome == cedarLiveApplied {
			res.LiveActivation = liveApplied
			res.Note = "compiled and activated on the live grant engine OF THIS PROCESS (per-tenant scoped grants — permit grants within the scope tree, forbid restricts); the activation is durable — the composition root re-activates the stored active revision on restart. Nothing propagates the swap between processes, so other replicas keep enforcing their loaded policy until they restart or recompose; GET /pdp/active answers that question per process"
		} else {
			m.auditActivationDeferred(r.Context(), mc, engine, revision, deferredCedarLiveState(outcome, confirmErr), confirmErr)
			res.Note = deferredActivationNote
		}
	} else {
		res.Note = "versioned and selected as the current Rego revision in the authored history; OPA enforcement is the sidecar's — this revision is not pushed to OPA from here"
	}
	writeJSON(w, http.StatusOK, res)
}

// hasError reports whether any diagnostic is an error (warnings do not block).
func hasError(diags []pdpDiag) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}
