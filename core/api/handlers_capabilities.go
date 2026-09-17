// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// CapabilitySchemaVersion is the only contract version this endpoint accepts. It is
// checked rather than ignored so a future shape cannot be silently answered under the
// current rules.
const CapabilitySchemaVersion = 2

// CapabilitySelectors carries ONLY the declared identification inputs of an operation.
//
// ⛔ IT IS NOT A REQUEST BODY. `body` transports the ONE top-level field the route
// declared as its entity locator (api.EntityRef.BodyIDField); it does not execute a
// write payload, and no other field of a command is admitted or consulted. If a future
// adapter needs another payload value to AUTHORIZE, that input becomes declared and
// required, and its absence is inputs_required — never a guess.
type CapabilitySelectors struct {
	Path map[string]string `json:"path,omitempty"`
	Body map[string]string `json:"body,omitempty"`
}

// CapabilityQuestion is one typed question about ONE registered operation.
//
// ⛔ THERE IS NO SUBJECT FIELD, AND THAT IS THE CONTRACT. The subject is the calling
// credential; a principal, role, group, assurance level, fact, policy or route metadata
// supplied by a client is not an input this endpoint has. Rejecting unknown fields is
// what makes that a mechanism instead of a promise.
type CapabilityQuestion struct {
	// ID correlates this question with its result. The batch keeps results
	// independent per id; it never ANDs or ORs them together.
	ID string `json:"id"`
	// Kind is "surface" (may I load this collection?) or "operation" (may I perform
	// this exact operation on this exact resource, right now?).
	Kind string `json:"kind"`
	// Operation is "METHOD <registered pattern>" — the route as mounted, never a
	// resolved URL and never a second table copied from the published document.
	Operation string `json:"operation"`
	// WorkspaceID is the workspace the question is ASKED about. For a collection it is
	// the selector the route declares and the engine corroborates; for an entity it is
	// an assertion compared with the row's STORED workspace, never a substitute for it.
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Selectors are the declared identification inputs of the operation.
	Selectors *CapabilitySelectors `json:"selectors,omitempty"`
}

// CapabilityQuestionsRequest is the closed request DTO.
type CapabilityQuestionsRequest struct {
	SchemaVersion int                  `json:"schema_version"`
	Questions     []CapabilityQuestion `json:"questions"`
}

// CapabilityResult is one independent answer.
//
// RefreshAfterMS is present ONLY on a positive, and its absence on one is itself an
// instruction: a client that receives a positive without a finite budget must treat the
// result as UNKNOWN rather than believing it indefinitely.
type CapabilityResult struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	State          CapabilityState `json:"state"`
	Code           string          `json:"code"`
	ObservedAt     time.Time       `json:"observed_at"`
	RefreshAfterMS *int64          `json:"refresh_after_ms,omitempty"`
}

// CapabilityResultsResponse is the closed response DTO.
type CapabilityResultsResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Results       []CapabilityResult `json:"results"`
}

const (
	capabilityMaxIDBytes        = 128
	capabilityMaxOperationBytes = 256
	capabilityMaxSelectorBytes  = 256
)

// capabilityBatchBudget is the SERVER-OWNED execution horizon of one capabilities
// request. It is an execution budget, not an evidence TTL: it bounds how long this
// endpoint may spend deciding, and the freshness a positive may claim is still the
// intersection of the windows its own predicates established.
//
// ⛔ IT EXISTS BECAUSE THE REAL TRANSPORT SUPPLIES NO DEADLINE, AND THE TYPED EVIDENCE
// PRODUCERS CORRECTLY REFUSE AN UNBOUNDED ONE. net/http derives every request context
// with context.WithCancel; ReadTimeout/WriteTimeout are connection limits and never
// become ctx.Deadline(). governanceEvidenceWitness requires a finite horizon before it
// will read the transaction clock and the authorization epoch, rather than minting a
// TTL locally — so with no deadline installed here every question came back
// unknown/evidence_unavailable while the real route served 200 for the same caller.
// The focused HTTP battery did not see it because its helper wrapped every request in
// a two-minute deadline that NewHTTPServer does not add.
//
// ⛔ AND IT IS ONE BUDGET FOR THE WHOLE BATCH, NOT ONE PER QUESTION. 32 independent
// five-second horizons could occupy 160 seconds, past the server's 60-second write
// timeout: a later question does not get a renewed window because an earlier one spent
// the budget. An earlier caller deadline or a disconnect still wins, because the
// horizon is DERIVED from the request context rather than replacing it.
const capabilityBatchBudget = 5 * time.Second

// handleAuthCapabilities projects the CALLER'S OWN authority over registered operations.
//
// ⛔ SELF-ONLY, AND NOT BECAUSE A PERMISSION GUARDS IT. There is no subject input at all,
// so there is nothing for a permission like authz:read to protect against; requiring one
// would instead make the console unable to ask about the operator in front of it unless
// that operator held a privileged read. AuthZEN keeps the privileged, subject-resolving
// surface and its permission; this one answers only about the credential that called it.
//
// ⛔ Cache-Control: no-store IS ON EVERY RESPONSE THIS HANDLER WRITES, including its
// own refusals. Authenticate middleware 401 is a different writer and is not covered
// here. These bodies are authorization observations of one credential; a private cache
// replaying one to the next caller is the failure the freshness contract exists to
// prevent.
func (s *Server) handleAuthCapabilities(w http.ResponseWriter, r *http.Request) {
	for name, value := range NoStoreResponseHeaders() {
		w.Header().Set(name, value)
	}
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var request CapabilityQuestionsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		s.writeError(w, r, errBadRequest)
		return
	}
	if err := validateCapabilityRequest(request); err != nil {
		s.writeError(w, r, errBadRequest)
		return
	}
	// ⛔ THE COMMON UNAVAILABILITY IS DECIDED HERE, BEFORE ANY TARGET IS RESOLVED. An
	// engine that cannot decide anything must say so without first probing the ids it
	// was asked about — otherwise the SHAPE of the failure would depend on which rows
	// exist, which is an existence oracle built out of an outage.
	if s.authz == nil || s.st == nil {
		s.writeError(w, r, auth.ErrRouteUndecided)
		return
	}
	// ⛔ THE PRINCIPAL IS RECONSTRUCTED WITH ITS AUTHORITY EVIDENCE, AND WITHOUT THIS
	// THE ENDPOINT COULD NEVER EMIT A POSITIVE. The authenticate middleware produces a
	// principal that carries no sealed directory-epoch fact — only the governed door
	// reconstructs one — and AuthorizeEvidence answers principal_authority_unverified
	// for such a principal, so every question would have come back UNKNOWN while the
	// real route served 200. Measured that way on the first run of the causal battery.
	//
	// It is the SAME principal, not a stronger one: the reconstruction re-reads the
	// caller's own credential and returns it sealed, with identical roles, grants,
	// confinement and ceiling. What it adds is the evidence a positive must carry, and
	// a credential family the producer cannot reconstruct is 503 UNKNOWN here — decided
	// before any target is resolved — rather than a fallback to the boolean.
	var reconstructed bool
	if r, reconstructed = s.prepareGovernedPrincipal(w, r); !reconstructed {
		return
	}
	if p, ok = principalFrom(r.Context()); !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	// ⛔ THE EXECUTION HORIZON IS INSTALLED HERE: AFTER the reconstruction above and
	// BEFORE any question reads a workspace, an availability probe, a lineage row or an
	// outer/inner decision. Deriving it from r.Context() keeps an earlier caller
	// deadline and a client disconnect authoritative; canceling on return releases it
	// as soon as the batch is answered.
	ctx, cancel := context.WithTimeout(r.Context(), capabilityBatchBudget)
	defer cancel()
	r = r.WithContext(ctx)
	// This marker belongs only to non-disclosure responses. Positive budgets still
	// use their own evidence and the clock at the close of each question.
	dispatchedAt := s.clock.Now().Time().UTC()
	results := make([]CapabilityResult, 0, len(request.Questions))
	for _, question := range request.Questions {
		evaluation := s.evaluateCapabilityQuestion(r, p, tenant, question)
		now := s.clock.Now().Time().UTC()
		results = append(results, evaluation.publicResult(question, dispatchedAt, now))
	}
	writeJSON(w, http.StatusOK, CapabilityResultsResponse{
		SchemaVersion: CapabilitySchemaVersion, Results: results,
	})
}

// validateCapabilityRequest judges FORM and CARDINALITY only, and it judges all of it
// before a single target is looked at. A malformed batch is one 400; it never becomes a
// batch of denials.
func validateCapabilityRequest(request CapabilityQuestionsRequest) error {
	if request.SchemaVersion != CapabilitySchemaVersion {
		return errBadRequest
	}
	if len(request.Questions) == 0 || len(request.Questions) > capabilityMaxQuestions {
		return errBadRequest
	}
	seen := make(map[string]struct{}, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || len(question.ID) > capabilityMaxIDBytes {
			return errBadRequest
		}
		if _, duplicate := seen[question.ID]; duplicate {
			return errBadRequest
		}
		seen[question.ID] = struct{}{}
		if question.Kind != capabilityKindSurface && question.Kind != capabilityKindOperation {
			return errBadRequest
		}
		if question.Operation == "" || len(question.Operation) > capabilityMaxOperationBytes ||
			strings.TrimSpace(question.Operation) != question.Operation {
			return errBadRequest
		}
		if len(question.WorkspaceID) > capabilityMaxSelectorBytes {
			return errBadRequest
		}
		if question.Selectors != nil {
			for _, set := range []map[string]string{question.Selectors.Path, question.Selectors.Body} {
				for name, value := range set {
					if name == "" || len(name) > capabilityMaxSelectorBytes ||
						len(value) > capabilityMaxSelectorBytes {
						return errBadRequest
					}
				}
			}
		}
	}
	return nil
}

// capabilityEvaluation keeps the trusted disclosure policy and internal outcome
// together through faults. No field is serialized, including recoveredPanic.
// A panic value is never formatted, logged or returned to the caller.
type capabilityEvaluation struct {
	projection     capabilityProjection
	conceal        bool
	recoveredPanic bool
}

// evaluateCapabilityQuestion buffers one question before publication. The only
// exceptions to non-disclosure are explicit, target-free input/assurance rejections;
// authority failures cannot create an exception merely by returning the same code.
func (s *Server) evaluateCapabilityQuestion(
	r *http.Request, p auth.Principal, tenant model.TenantID, question CapabilityQuestion,
) (evaluation capabilityEvaluation) {
	evaluation.projection = unknownProjection(capabilityCodeEvidenceUnavailable)
	descriptor, known := s.routeDescriptorFor(question.Operation)
	evaluation.conceal = known && question.Kind == capabilityKindOperation &&
		descriptor.entity != nil && descriptor.entity.ConcealDeniedAsNotFound
	defer func() {
		if recover() != nil {
			evaluation.projection = unknownProjection(capabilityCodeEvidenceUnavailable)
			evaluation.recoveredPanic = true
		}
	}()
	if !known {
		evaluation.projection = inputRejectedProjection(capabilityCodeNotSupported)
	} else if descriptor.meta.RequiresStepUp(p.AAL) {
		evaluation.projection = inputRejectedProjection(capabilityCodeStepUpRequired)
	} else {
		switch question.Kind {
		case capabilityKindSurface:
			evaluation.projection = s.projectSurface(r, p, tenant, descriptor, question)
		case capabilityKindOperation:
			evaluation.projection = s.projectOperation(r, p, tenant, descriptor, question)
		default:
			evaluation.projection = inputRejectedProjection(capabilityCodeNotSupported)
		}
	}
	return evaluation
}

// publicResult is the disclosure boundary, after the positive's final expiry check.
// The internal projection stays truthful: public non-disclosure is not a DENY/UNKNOWN
// conversion inside authorization, and a positive still requires its actual window.
func (evaluation capabilityEvaluation) publicResult(
	question CapabilityQuestion, dispatchedAt, now time.Time,
) CapabilityResult {
	projection := evaluation.projection
	result := CapabilityResult{
		ID: question.ID, Kind: question.Kind,
		State: projection.state, Code: projection.code, ObservedAt: now,
	}
	if projection.positive() {
		if !projection.observedAt.IsZero() {
			result.ObservedAt = projection.observedAt
		}
		if budget, usable := refreshAfter(now, projection.freshUntil); usable {
			result.RefreshAfterMS = &budget
		} else {
			result.State, result.Code = CapabilityUnknown, capabilityCodeEvidenceUnavailable
			result.ObservedAt = now
		}
	}
	if evaluation.conceal && !projection.inputRejected && result.State != CapabilityAllowed {
		result.State, result.Code = CapabilityUndisclosed, capabilityCodeNotDisclosed
		result.ObservedAt = dispatchedAt
		result.RefreshAfterMS = nil
	}
	return result
}

// projectSurface answers admission to a registered COLLECTION: steps 1-5 of the ratified
// request order and no further. It does not call the handler, the readiness conjunction,
// the recording gate or the navigation keyring — those are preconditions of the ACT, and
// running them to paint a permission is what turns a projection into a side effect.
//
// ⛔ A COLLECTION THAT DECLARES NO CORROBORATED SCOPE IS not_supported. Answering it from
// the workspace-less resource would be answering a different question from the one the
// route asks, and inventing a workspace for it would authorize a scope nobody named.
func (s *Server) projectSurface(
	r *http.Request,
	p auth.Principal,
	tenant model.TenantID,
	descriptor routeDescriptor,
	question CapabilityQuestion,
) capabilityProjection {
	if descriptor.entity != nil || descriptor.collectionScope == nil {
		return unknownProjection(capabilityCodeNotSupported)
	}
	if question.Selectors != nil &&
		(len(question.Selectors.Path) > 0 || len(question.Selectors.Body) > 0) {
		// A collection has no entity locator. Accepting one would let a caller believe
		// a row was considered.
		return unknownProjection(capabilityCodeInputsRequired)
	}
	workspace, admission := s.corroborateActiveWorkspace(r.Context(), p, tenant, question.WorkspaceID)
	switch admission {
	case workspaceAdmissionOK:
	case workspaceAdmissionInvalid:
		return unknownProjection(capabilityCodeInputsRequired)
	case workspaceAdmissionNotAdmissible:
		// ⛔ THE SAME ANSWER THE GET GIVES, AND THE SAME ONE A DENIAL GIVES. A known
		// absence, a known non-active workspace and a known outer denial are one public
		// result with no distinguishing field and no budget — which is exactly the
		// ratified amendment, and the reason this branch does not have its own code.
		return deniedFor(descriptor, capabilityKindSurface)
	default:
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	resource := auth.ResourceFor(descriptor.perm)
	resource.WorkspaceID = workspace
	projection := s.projectOuterDecision(r.Context(), auth.Request{
		Principal: p, Permission: descriptor.perm, Tenant: tenant,
		Resource: resource, Route: descriptor.meta,
	}, descriptor.governed)
	return asSurface(projection, descriptor)
}

// asSurface renames an operation-shaped outer projection into the collection vocabulary.
// The two vocabularies do not share values, so a client cannot present one as the other.
func asSurface(projection capabilityProjection, descriptor routeDescriptor) capabilityProjection {
	switch projection.state {
	case CapabilityAllowed:
		projection.state, projection.code = CapabilityReachable, capabilityCodeAdmitted
	case CapabilityDenied:
		return deniedFor(descriptor, capabilityKindSurface)
	}
	return projection
}

// projectOperation answers ONE registered operation on ONE resource: the outer stage the
// route wrapper would run, conjoined with the module's OWN stage. Both must be
// established; either denial denies; any unknown is unknown.
//
// Support, selectors and declared scope are checked before any authority/target read.
// Module availability is also checked before lookup, but does not prove the later
// outer evaluation will succeed. Schema 2's final disclosure mapping covers failures
// there without changing the internal authorization outcome.
func (s *Server) projectOperation(
	r *http.Request,
	p auth.Principal,
	tenant model.TenantID,
	descriptor routeDescriptor,
	question CapabilityQuestion,
) capabilityProjection {
	ref := descriptor.entity
	if ref == nil {
		return inputRejectedProjection(capabilityCodeNotSupported)
	}
	// The engine reads a CORE-owned row through its own typed accessor, which needs the
	// live request. No adapter covers that shape here, and guessing one would decide a
	// row this projection never read.
	if ref.CoreKind != CoreKindNone || ref.Kind == "" {
		return inputRejectedProjection(capabilityCodeNotSupported)
	}
	// (1) SUPPORT, from the registered operation alone. A route with no typed adapter is
	// not_supported whether or not its target exists.
	operation := ModuleCapabilityOperation{
		Method: descriptor.method, Pattern: descriptor.pattern, Permission: descriptor.perm,
	}
	if descriptor.projector == nil || !descriptor.projector.SupportsModuleCapability(operation) {
		return inputRejectedProjection(capabilityCodeNotSupported)
	}
	// (2) SELECTOR SHAPE, from the question alone: every supplied key must be one this
	// operation DECLARED, and the locator must be canonical.
	id, selectorCode := capabilityEntityID(descriptor, question.Selectors)
	if selectorCode != "" {
		return inputRejectedProjection(selectorCode)
	}
	// (3) THE DECLARED SCOPE. It is required for an entity operation of this pilot
	// because the availability probe below must be answerable without a row, and a scope
	// is the one thing such a probe needs. Absent or non-canonical is a missing declared
	// authorization input, never a denial.
	if question.WorkspaceID == "" {
		return inputRejectedProjection(capabilityCodeInputsRequired)
	}
	requested, err := model.ParseID(question.WorkspaceID)
	if err != nil || requested.IsZero() || requested.String() != question.WorkspaceID {
		return inputRejectedProjection(capabilityCodeInputsRequired)
	}
	// (4) COMMON AVAILABILITY, still with no target named. When the shared authority is
	// unreadable every question in this scope answers the same way, so an outage cannot
	// tell a hidden row from an absent one.
	if available := descriptor.projector.ModuleCapabilityAvailable(
		r.Context(), operation,
		ModuleCapabilityScope{Tenant: tenant, Workspace: requested, Principal: p},
	); !available.Available {
		return unknownProjection(capabilityAvailabilityCode(available.Code))
	}
	// (5) ONLY NOW the target is resolved.
	resource, found, lineageErr := s.storedEntityLineage(
		r.Context(), tenant, entityBaseResource(descriptor.perm, *ref), *ref, id,
	)
	if lineageErr != nil {
		// Unreadable lineage is internally UNKNOWN. Final publication applies the
		// trusted concealment policy, including for target-only scan failures.
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	resource.ID = id
	if !found {
		if ref.ConcealDeniedAsNotFound {
			// The route unifies absence and denial into one answer, so the projection
			// must too: telling them apart here would rebuild the enumeration oracle
			// the route's concealment exists to close.
			return deniedFor(descriptor, capabilityKindOperation)
		}
		// An ordinary entity route has no lineage to decide against and would answer
		// 404 from its handler. Calling that a denial would invent a decision.
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	if requested != resource.WorkspaceID {
		// The caller named a workspace the row does not live in. The route answers its
		// own concealment for that, and it is never quietly corrected to the stored one.
		return deniedFor(descriptor, capabilityKindOperation)
	}
	outer := s.projectOuterDecision(r.Context(), auth.Request{
		Principal: p, Permission: descriptor.perm, Tenant: tenant,
		Resource: resource, Route: descriptor.meta,
	}, descriptor.governed)
	switch outer.state {
	case CapabilityDenied:
		return deniedFor(descriptor, capabilityKindOperation)
	case CapabilityAllowed:
	default:
		return outer
	}
	inner := descriptor.projector.ProjectModuleCapability(r.Context(), ModuleCapabilityQuestion{
		Method: descriptor.method, Pattern: descriptor.pattern, Permission: descriptor.perm,
		Principal: p, Tenant: tenant, Resource: resource, RequestedWorkspace: requested,
	})
	switch inner.State {
	case CapabilityAllowed:
		return outer.narrowWindow(inner.ObservedAt, inner.FreshUntil)
	case CapabilityDenied:
		return deniedFor(descriptor, capabilityKindOperation)
	default:
		return unknownProjection(capabilityAvailabilityCode(inner.Code))
	}
}

// capabilityAvailabilityCode keeps a module's reported reason inside the published closed
// catalog. A module may only name codes this contract publishes; anything else is
// reported as the honest generic rather than echoed onto the wire.
func capabilityAvailabilityCode(code string) string {
	switch code {
	case capabilityCodeEngineUnready, capabilityCodeNotSupported, capabilityCodeInputsRequired:
		return code
	default:
		return capabilityCodeEvidenceUnavailable
	}
}

// capabilityEntityID validates the COMPLETE selector shape of one operation and returns
// its locator id. It answers "" with no code on success, or a closed-catalog code.
//
// ⛔ THE WHOLE MAP IS JUDGED, NOT JUST THE LOCATOR, and the independent review measured
// why. The earlier version read the one declared locator and DISCARDED every other key,
// so a PATCH question carrying `body.channel_id` for a channel the caller administers
// AND `path.id` for a channel it cannot see was answered `allowed` — from the first,
// silently ignoring the second. Nothing was bypassed, but the answer described a
// question the caller had not asked, and a closed typed contract that quietly drops half
// its input is not closed.
//
// ⛔ AND DisallowUnknownFields CANNOT DO THIS. Strict decoding closes the DTO's named
// fields; `selectors.path` and `selectors.body` are `map[string]string` by the ratified
// wire contract, and no struct tag closes a map. The declaration therefore has to come
// from somewhere the route already states it, which is the next paragraph.
//
// ⛔ THE DECLARED KEYS ARE DERIVED FROM THE REGISTRATION, NOT FROM A TABLE. Path keys are
// the `{…}` placeholders of the REGISTERED pattern; the body key is the route's declared
// BodyIDField. So `POST /channels/{id}/grants/{grant_id}/revoke` keeps `grant_id` as the
// captured intent the contract allows — because its own pattern declares it — while
// `PATCH /channels`, whose pattern has no placeholders at all, accepts no path selector
// whatsoever. A hand-kept list would have had to be remembered; this cannot drift from
// the route it describes.
func capabilityEntityID(descriptor routeDescriptor, selectors *CapabilitySelectors) (string, string) {
	ref := descriptor.entity
	if ref == nil {
		return "", capabilityCodeNotSupported
	}
	if selectors == nil {
		return "", capabilityCodeInputsRequired
	}
	declaredPath := patternPathParameters(descriptor.pattern)
	for name, value := range selectors.Path {
		if !declaredPath[name] || value == "" {
			return "", capabilityCodeInputsRequired
		}
	}
	for name, value := range selectors.Body {
		if name != ref.BodyIDField || ref.BodyIDField == "" || value == "" {
			return "", capabilityCodeInputsRequired
		}
	}
	id := ""
	switch {
	case ref.IDParam != "":
		if !declaredPath[ref.IDParam] {
			return "", capabilityCodeNotSupported
		}
		id = selectors.Path[ref.IDParam]
	case ref.BodyIDField != "":
		id = selectors.Body[ref.BodyIDField]
	default:
		return "", capabilityCodeNotSupported
	}
	if id == "" {
		return "", capabilityCodeInputsRequired
	}
	// The locator names a row, so it must be a canonical id. A non-canonical one is a
	// malformed declared input, never a positive and never a denial.
	if parsed, err := model.ParseID(id); err != nil || parsed.IsZero() || parsed.String() != id {
		return "", capabilityCodeInputsRequired
	}
	return id, ""
}

// patternPathParameters returns the `{name}` placeholders a registered pattern declares.
func patternPathParameters(pattern string) map[string]bool {
	declared := map[string]bool{}
	for _, segment := range strings.Split(pattern, "/") {
		if len(segment) > 2 && strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			declared[segment[1:len(segment)-1]] = true
		}
	}
	return declared
}
