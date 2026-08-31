// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the OpenID AuthZEN Authorization API 1.0 surface: a conformant WIRE
// adapter over the engine's existing PDP (auth.Authorizer), NOT a new decision
// engine. Every endpoint here maps an AuthZEN request onto auth.Request, calls the
// SAME Authorizer.Authorize the REST/gRPC enforcement path calls, and maps the
// auth.Decision back — so a PDP answer can never diverge from what is enforced.
//
// The surface (AuthZEN 1.0 Final, openid.net/specs/authorization-api-1_0.html):
//   - POST /access/v1/evaluation          — single decision (the one REQUIRED endpoint)
//   - POST /access/v1/evaluations         — batch decisions (§ boxcar, OPTIONAL)
//   - POST /access/v1/search/subject      — "who can do A on R?"      (Search APIs, §8)
//   - POST /access/v1/search/resource     — "what can S do A on?"
//   - POST /access/v1/search/action       — "what can S do on R?"
//   - GET  /.well-known/authzen-configuration — PDP metadata discovery (§9)
//
// The two Search endpoints (subject + resource) ARE the reverse/enumeration
// queries access-reviews need — Cedar's one missing Zanzibar capability — realized,
// per the master-plan §6.1 decision, as enumeration over the candidate set +
// batch-authorize, never a parallel Zanzibar engine. The sealed access-review export
// built on the same enumeration lives in handlers_accessreview.go.
//
// HONESTY — the subject is resolved from the STORE, never the caller. The AuthZEN
// `subject` carries a type+id; the engine looks the real principal up (its roles and
// S256 nested-group memberships) via the Authenticator, exactly as a login would, so
// a PEP can never widen a decision by asserting roles/attributes about the subject it
// asks about. Caller-supplied resource `properties` apply only where there is no
// stored row to override them (the scoped engine reads sensitivity/workspace from the
// row for agent/session/resource entities — uncheatable).
//
// We implement the wire contract of 1.0 Final (evaluation + batch + all three
// searches + discovery). "Conformant to the wire format" is an accurate claim;
// OpenID *certification* is a separate program and is NOT claimed.

// AuthZEN route paths (the spec's conventional defaults, advertised in discovery).
const (
	authzenBasePath       = "/access/v1"
	authzenEvalPath       = authzenBasePath + "/evaluation"
	authzenEvalsPath      = authzenBasePath + "/evaluations"
	authzenSearchSubjPath = authzenBasePath + "/search/subject"
	authzenSearchResPath  = authzenBasePath + "/search/resource"
	authzenSearchActPath  = authzenBasePath + "/search/action"
	authzenExportPath     = authzenBasePath + "/access-review/export"
	// AuthZenConfigPath is the PDP metadata discovery document (RFC-8414-style
	// well-known). It is unauthenticated + setup-exempt (public metadata), so it is
	// listed in RootEnginePaths.
	AuthZenConfigPath = "/.well-known/authzen-configuration"
	// AuthZenPathPrefix is the top-level prefix of the AuthZEN decision/search/export
	// routes (not under /v1). The single-binary SPA router (cmd/olivares webui.go)
	// routes this prefix to the engine API, not the SPA — the counterpart to the /v1
	// tree and RootEnginePaths.
	AuthZenPathPrefix = "/access/"
)

// Search/batch bounds.
const (
	authzenDefaultLimit = 100  // default page size (candidates scanned per page)
	authzenMaxLimit     = 1000 // hard cap, matching the store's per-page cap
	authzenMaxBatch     = 1000 // max evaluations in one batch request
)

// AuthZEN batch evaluation semantics (options.evaluations_semantic).
const (
	semExecuteAll  = "execute_all"            // evaluate every item (default)
	semDenyFirst   = "deny_on_first_deny"     // stop at the first deny (&&)
	semPermitFirst = "permit_on_first_permit" // stop at the first permit (||)
)

// --- AuthZEN wire types (field names are the spec's; do not rename) ---------------

type azSubject struct {
	Type       string         `json:"type"`
	ID         string         `json:"id,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type azAction struct {
	Name       string         `json:"name"`
	Properties map[string]any `json:"properties,omitempty"`
}

type azResource struct {
	Type       string         `json:"type"`
	ID         string         `json:"id,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type azContext map[string]any

type azEvalRequest struct {
	Subject  azSubject  `json:"subject"`
	Action   azAction   `json:"action"`
	Resource azResource `json:"resource"`
	Context  azContext  `json:"context,omitempty"`
}

// azDecision is the evaluation response (and a batch item result). `decision` is
// REQUIRED and always serialized; `context` optionally carries the reason / errors.
type azDecision struct {
	Decision bool      `json:"decision"`
	Context  azContext `json:"context,omitempty"`
}

type azEvalItem struct {
	Subject  *azSubject  `json:"subject,omitempty"`
	Action   *azAction   `json:"action,omitempty"`
	Resource *azResource `json:"resource,omitempty"`
	Context  azContext   `json:"context,omitempty"`
}

type azOptions struct {
	EvaluationsSemantic string `json:"evaluations_semantic,omitempty"`
}

type azEvalsRequest struct {
	Subject     *azSubject   `json:"subject,omitempty"`
	Action      *azAction    `json:"action,omitempty"`
	Resource    *azResource  `json:"resource,omitempty"`
	Context     azContext    `json:"context,omitempty"`
	Evaluations []azEvalItem `json:"evaluations"`
	Options     *azOptions   `json:"options,omitempty"`
}

type azEvalsResponse struct {
	// decision is RECOMMENDED-omitted when evaluations is present (spec §); we omit it.
	Evaluations []azDecision `json:"evaluations"`
}

type azPageRequest struct {
	Token      string         `json:"token,omitempty"`
	Limit      int            `json:"limit,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type azSearchRequest struct {
	Subject  *azSubject     `json:"subject,omitempty"`
	Action   *azAction      `json:"action,omitempty"`
	Resource *azResource    `json:"resource,omitempty"`
	Context  azContext      `json:"context,omitempty"`
	Page     *azPageRequest `json:"page,omitempty"`
}

// azEntityResult is one search result: {type,id} for a subject/resource, {name} for
// an action (each omits the other's fields).
type azEntityResult struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type azPageResponse struct {
	NextToken string `json:"next_token"` // REQUIRED; "" means no more results
	Count     int    `json:"count,omitempty"`
}

type azSearchResponse struct {
	Results []azEntityResult `json:"results"`
	Page    azPageResponse   `json:"page"`
	Context azContext        `json:"context,omitempty"`
}

type azConfig struct {
	PolicyDecisionPoint       string   `json:"policy_decision_point"`
	AccessEvaluationEndpoint  string   `json:"access_evaluation_endpoint"`
	AccessEvaluationsEndpoint string   `json:"access_evaluations_endpoint"`
	SearchSubjectEndpoint     string   `json:"search_subject_endpoint,omitempty"`
	SearchResourceEndpoint    string   `json:"search_resource_endpoint,omitempty"`
	SearchActionEndpoint      string   `json:"search_action_endpoint,omitempty"`
	Profiles                  []string `json:"profiles,omitempty"`
}

// --- discovery -------------------------------------------------------------------

// handleAuthzenConfig serves the AuthZEN PDP metadata (spec §9). It is public and
// setup-exempt (advertised in RootEnginePaths) so a PEP can discover the endpoints
// before presenting a credential. URLs are absolute, derived from the request honoring
// a trusted reverse proxy's X-Forwarded-* headers (schemeHost).
func (s *Server) handleAuthzenConfig(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindConfig) {
		return
	}
	base := schemeHost(r)
	cfg := azConfig{
		PolicyDecisionPoint:       base,
		AccessEvaluationEndpoint:  base + authzenEvalPath,
		AccessEvaluationsEndpoint: base + authzenEvalsPath,
	}
	// Honest discovery: advertise only the endpoints actually enabled by the operator.
	if s.authzen.searchEnabled() {
		cfg.SearchSubjectEndpoint = base + authzenSearchSubjPath
		cfg.SearchResourceEndpoint = base + authzenSearchResPath
		cfg.SearchActionEndpoint = base + authzenSearchActPath
	}
	// advertise the COAZ profile (MCP tool authorization vocabulary) support
	// so a COAZ-aware PEP knows it can send mcp_client/mcp_tool/mcp_server typed
	// entities. The profile identifiers are provisional until the AuthZEN WG publishes
	// the COAZ draft (the vocabulary is stable; the profile URI is advisory).
	cfg.Profiles = []string{"coaz-mcp-tool-authorization"}
	writeJSON(w, http.StatusOK, cfg)
}

// --- evaluation (single) ---------------------------------------------------------

// handleAuthzenEvaluation answers one AuthZEN access-evaluation: may `subject` do
// `action` on `resource`? It authorizes the CALLER (authz:read), resolves the SUBJECT
// from the store, and returns the verbatim Authorizer decision. Default assurance is
// AAL1 (a PDP must not assume a step-up the PEP did not assert; the PEP raises it via
// context.aal) — the conservative direction for enforcement.
func (s *Server) handleAuthzenEvaluation(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindEval) {
		return
	}
	_, tenant, ok := s.authzTenant(w, r, auth.PermAuthzRead)
	if !ok {
		return
	}
	var in azEvalRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	// AuthZEN requires subject, action and resource. A MALFORMED request (missing a
	// required field) is a 400; a well-formed request whose subject simply does not
	// exist is a legitimate 200 deny (handled in authzenDecide). This matches the
	// search endpoints' validation.
	if strings.TrimSpace(in.Subject.Type) == "" || strings.TrimSpace(in.Subject.ID) == "" {
		s.badRequest(w, r, "subject (type and id) is required")
		return
	}
	if strings.TrimSpace(in.Action.Name) == "" {
		s.badRequest(w, r, "action.name is required (an Olivares permission, e.g. \"agent:read\")")
		return
	}
	if strings.TrimSpace(in.Resource.Type) == "" {
		s.badRequest(w, r, "resource.type is required")
		return
	}
	cache := authzenSubjectCache{}
	dec := s.authzenDecide(r.Context(), tenant, in.Subject, in.Action, in.Resource, in.Context, auth.AAL1, cache)
	writeJSON(w, http.StatusOK, dec)
}

// --- evaluations (batch) ---------------------------------------------------------

// handleAuthzenEvaluations answers a batch of evaluations in one call (spec boxcar).
// Top-level subject/action/resource/context are defaults; each item overrides them
// (object-level). options.evaluations_semantic selects execute_all (default),
// deny_on_first_deny or permit_on_first_permit (short-circuit returns a shorter
// array, per spec). Results are in request order. A resolved-but-incomplete item, or
// one whose subject cannot be resolved, defaults CLOSED with a context reason.
func (s *Server) handleAuthzenEvaluations(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindEval) {
		return
	}
	_, tenant, ok := s.authzTenant(w, r, auth.PermAuthzRead)
	if !ok {
		return
	}
	var in azEvalsRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if len(in.Evaluations) == 0 {
		s.badRequest(w, r, "evaluations array is required and must be non-empty")
		return
	}
	if len(in.Evaluations) > authzenMaxBatch {
		s.badRequest(w, r, fmt.Sprintf("too many evaluations (max %d)", authzenMaxBatch))
		return
	}
	sem := semExecuteAll
	if in.Options != nil && in.Options.EvaluationsSemantic != "" {
		switch in.Options.EvaluationsSemantic {
		case semExecuteAll, semDenyFirst, semPermitFirst:
			sem = in.Options.EvaluationsSemantic
		default:
			s.badRequest(w, r, "options.evaluations_semantic must be execute_all, deny_on_first_deny or permit_on_first_permit")
			return
		}
	}
	cache := authzenSubjectCache{}
	out := make([]azDecision, 0, len(in.Evaluations))
	for _, item := range in.Evaluations {
		subj := firstSubject(item.Subject, in.Subject)
		act := firstAction(item.Action, in.Action)
		res := firstResource(item.Resource, in.Resource)
		c := item.Context
		if c == nil {
			c = in.Context
		}
		var dec azDecision
		if subj == nil || act == nil || res == nil {
			dec = azDecision{Decision: false, Context: azContext{"reason": "each evaluation needs subject, action and resource (from the item or a top-level default)"}}
		} else {
			dec = s.authzenDecide(r.Context(), tenant, *subj, *act, *res, c, auth.AAL1, cache)
		}
		out = append(out, dec)
		if sem == semDenyFirst && !dec.Decision {
			break
		}
		if sem == semPermitFirst && dec.Decision {
			break
		}
	}
	writeJSON(w, http.StatusOK, azEvalsResponse{Evaluations: out})
}

// --- search: subject ("who can do A on R?") --------------------------------------

// handleAuthzenSearchSubject enumerates the candidate principal population of the
// tenant and returns those that may perform `action` on `resource` — the reverse
// query "who can access R". subject.type (user|token) optionally narrows the
// population; subject.id is ignored (spec). Assurance defaults to AAL3 (the user's
// maximum standing entitlement — the safe direction for an access review, which must
// not under-report; override via context.aal).
//
// Pagination is candidate-paged: page.limit bounds the candidates SCANNED this page
// (default 100, max 1000), so a page returns the ACCESSIBLE subset (≤ limit, possibly
// fewer or zero) and page.next_token continues — a consumer pages until next_token is
// empty. This is stated in the response context.
func (s *Server) handleAuthzenSearchSubject(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindSearch) {
		return
	}
	_, tenant, ok := s.authzTenant(w, r, auth.PermAuthzRead)
	if !ok {
		return
	}
	var in azSearchRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Resource == nil || strings.TrimSpace(in.Resource.Type) == "" {
		s.badRequest(w, r, "resource.type is required")
		return
	}
	if in.Action == nil {
		s.badRequest(w, r, "action is required")
		return
	}
	perm, okp := authzenPermission(*in.Action)
	if !okp {
		s.badRequest(w, r, "action.name is required (an Olivares permission, e.g. \"resource:read\")")
		return
	}
	var kindFilter auth.PrincipalKind
	hasFilter := false
	if in.Subject != nil && strings.TrimSpace(in.Subject.Type) != "" {
		k, okk := authzenKind(in.Subject.Type)
		if !okk {
			s.badRequest(w, r, "unsupported subject.type (use user or token)")
			return
		}
		kindFilter, hasFilter = k, true
	}
	aal := authzenAAL(in.Context, auth.AAL3)
	limit := authzenLimit(in.Page)
	offset := authzenDecodeOffset(in.Page)

	pop, err := s.authzenPrincipals.TenantPrincipals(r.Context(), tenant, aal)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	cands := pop
	if hasFilter {
		cands = cands[:0:0]
		for _, p := range pop {
			if p.Kind == kindFilter {
				cands = append(cands, p)
			}
		}
	}

	res := authzenResource(*in.Resource)
	results := []azEntityResult{}
	end := offset + limit
	if end > len(cands) {
		end = len(cands)
	}
	for i := offset; i < end; i++ {
		p := cands[i]
		if s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: res}).Allow {
			t, id := authzenSubjectRef(p)
			results = append(results, azEntityResult{Type: t, ID: id})
		}
	}
	next := ""
	if end < len(cands) {
		next = authzenEncodeToken(strconv.Itoa(end))
	}
	writeJSON(w, http.StatusOK, azSearchResponse{
		Results: results,
		Page:    azPageResponse{NextToken: next, Count: len(results)},
		Context: azContext{
			"population":      authzenPopulation(pop),
			"assurance":       aal,
			"pagination_note": "limit bounds candidates scanned per page; a page returns the accessible subset (may be fewer/zero) — page until page.next_token is empty",
		},
	})
}

// --- search: resource ("what can S do A on?") ------------------------------------

// handleAuthzenSearchResource enumerates the tenant's resources of resource.type and
// returns those the SUBJECT may perform `action` on — the reverse query "what can X
// access". subject (type+id) and action are required; resource.type is the kind to
// enumerate (resource|agent|session) and resource.id is ignored (spec). It may be
// bounded by resource.properties: `subtree` (a root resource id → a single
// materialized-path subtree scan) and `workspace` (a workspace id filter).
//
// Pagination uses the store's keyset cursor: one store page of resource.properties /
// page.limit candidates is scanned per request, returning the accessible subset;
// page.next_token carries the store cursor and is empty when the candidate set is
// exhausted.
func (s *Server) handleAuthzenSearchResource(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindSearch) {
		return
	}
	_, tenant, ok := s.authzTenant(w, r, auth.PermAuthzRead)
	if !ok {
		return
	}
	var in azSearchRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Subject == nil || strings.TrimSpace(in.Subject.Type) == "" || strings.TrimSpace(in.Subject.ID) == "" {
		s.badRequest(w, r, "subject (type and id) is required")
		return
	}
	if in.Action == nil {
		s.badRequest(w, r, "action is required")
		return
	}
	perm, okp := authzenPermission(*in.Action)
	if !okp {
		s.badRequest(w, r, "action.name is required (an Olivares permission, e.g. \"resource:read\")")
		return
	}
	if in.Resource == nil || strings.TrimSpace(in.Resource.Type) == "" {
		s.badRequest(w, r, "resource.type is required (the kind to enumerate: resource, agent or session)")
		return
	}
	kind := strings.TrimSpace(in.Resource.Type)
	aal := authzenAAL(in.Context, auth.AAL3)

	p, resolved, err := s.resolveAuthzenSubject(r.Context(), *in.Subject, aal)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !resolved {
		writeJSON(w, http.StatusOK, azSearchResponse{
			Results: []azEntityResult{}, Page: azPageResponse{NextToken: ""},
			Context: azContext{"reason": "subject not found or unsupported subject.type — no access"},
		})
		return
	}

	limit := authzenLimit(in.Page)
	cursor := authzenDecodeToken(in.Page)
	props := map[string]any{}
	if in.Resource.Properties != nil {
		props = in.Resource.Properties
	}

	// Phase 1 — read one candidate page (IDs only) in a short View; do NOT call
	// Authorize inside it (Authorize opens its own scope-resolution read; nesting
	// store transactions risks wedging the single-writer store).
	ids, nextCursor, err := s.authzenResourcePage(r.Context(), tenant, kind, props, limit, cursor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Phase 2 — authorize each candidate through the real Authorizer.
	results := []azEntityResult{}
	for _, id := range ids {
		res := auth.ResourceAttrs{Kind: kind, ID: id.String()}
		if s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: res}).Allow {
			results = append(results, azEntityResult{Type: kind, ID: id.String()})
		}
	}
	writeJSON(w, http.StatusOK, azSearchResponse{
		Results: results,
		Page:    azPageResponse{NextToken: authzenEncodeToken(nextCursor), Count: len(results)},
		Context: azContext{
			"assurance":       aal,
			"pagination_note": "limit bounds candidates scanned per page; a page returns the accessible subset (may be fewer/zero) — page until page.next_token is empty",
		},
	})
}

// --- search: action ("what can S do on R?") --------------------------------------

// handleAuthzenSearchAction returns the actions the SUBJECT may perform on a specific
// resource: the read/write/admin verb tiers for the resource kind, filtered to those
// the Authorizer allows. The action set is tiny, so this is a single page (next_token
// is always empty). subject (type+id) and resource (type, id recommended) required.
func (s *Server) handleAuthzenSearchAction(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindSearch) {
		return
	}
	_, tenant, ok := s.authzTenant(w, r, auth.PermAuthzRead)
	if !ok {
		return
	}
	var in azSearchRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Subject == nil || strings.TrimSpace(in.Subject.Type) == "" || strings.TrimSpace(in.Subject.ID) == "" {
		s.badRequest(w, r, "subject (type and id) is required")
		return
	}
	if in.Resource == nil || strings.TrimSpace(in.Resource.Type) == "" {
		s.badRequest(w, r, "resource.type is required")
		return
	}
	aal := authzenAAL(in.Context, auth.AAL3)
	p, resolved, err := s.resolveAuthzenSubject(r.Context(), *in.Subject, aal)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	results := []azEntityResult{}
	rctx := azContext{"assurance": aal}
	if resolved {
		res := authzenResource(*in.Resource)
		for _, verb := range []string{auth.VerbRead, auth.VerbWrite, auth.VerbAdmin} {
			perm := auth.Permission(res.Kind + ":" + verb)
			if s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: res}).Allow {
				results = append(results, azEntityResult{Name: string(perm)})
			}
		}
	} else {
		rctx["reason"] = "subject not found or unsupported subject.type — no access"
	}
	writeJSON(w, http.StatusOK, azSearchResponse{
		Results: results,
		Page:    azPageResponse{NextToken: "", Count: len(results)},
		Context: rctx,
	})
}

// --- shared decision + mapping helpers -------------------------------------------

// authzenSubjectCache memoizes subject resolution within one request (a batch may
// repeat a subject). Keyed by type+id+assurance (assurance affects a user's AAL).
type authzenSubjectCache map[string]authzenResolved

type authzenResolved struct {
	p  auth.Principal
	ok bool
}

// authzenDecide resolves the subject (cached) and returns the verbatim Authorizer
// decision as an AuthZEN result. A missing/invalid action, an unresolvable subject,
// or a resolution error all default CLOSED with an explanatory context.reason — the
// engine's own fail-closed posture, surfaced honestly rather than as a 5xx.
func (s *Server) authzenDecide(ctx context.Context, tenant model.TenantID, subj azSubject, act azAction, res azResource, c azContext, defaultAAL int, cache authzenSubjectCache) azDecision {
	perm, ok := authzenPermission(act)
	if !ok {
		return azDecision{Decision: false, Context: azContext{"reason": "missing or invalid action.name (expected an Olivares permission, e.g. \"agent:read\")"}}
	}
	aal := authzenAAL(c, defaultAAL)
	p, resolved := s.resolveSubjectCached(ctx, cache, subj, aal)
	if !resolved {
		// Deny-closed for an absent/unsupported subject AND for a (logged) store error:
		// the wording does not claim "not found" so it stays honest in the error case.
		return azDecision{Decision: false, Context: azContext{"reason": "subject could not be resolved (deny-closed)", "aal": aal}}
	}
	d := s.authz.Authorize(ctx, auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: authzenResource(res)})
	// `reason` is the Authorizer's NON-SENSITIVE reason (authorizer.go: it never leaks
	// which other tenant/resource exists); `aal` surfaces the assurance evaluated at so
	// a caller sees that evaluation is conservative (AAL1) vs a review's maximal (AAL3).
	return azDecision{Decision: d.Allow, Context: azContext{"reason": d.Reason, "aal": aal}}
}

// resolveSubjectCached resolves a subject once per (type,id,assurance) and caches it.
// A store error is treated as unresolved (deny-closed) and logged — it never caches.
func (s *Server) resolveSubjectCached(ctx context.Context, cache authzenSubjectCache, subj azSubject, aal int) (auth.Principal, bool) {
	key := subj.Type + "\x00" + subj.ID + "\x00" + strconv.Itoa(aal)
	if e, hit := cache[key]; hit {
		return e.p, e.ok
	}
	p, ok, err := s.resolveAuthzenSubject(ctx, subj, aal)
	if err != nil {
		if s.log != nil {
			s.log.Warn("authzen: subject resolution error (deny-closed)", "err", err)
		}
		return auth.Principal{}, false
	}
	cache[key] = authzenResolved{p: p, ok: ok}
	return p, ok
}

// resolveAuthzenSubject maps an AuthZEN subject to a real, store-resolved principal.
// type user → PrincipalForUser (by id or email); type mcp_client → PrincipalForExternalID
// first, then PrincipalForUser as fallback (COAZ — the subject.id is the IdP's
// external_id or email); type token/api_token/service_account → PrincipalForToken.
// An unknown type or empty id resolves to nothing (deny-closed).
func (s *Server) resolveAuthzenSubject(ctx context.Context, subj azSubject, aal int) (auth.Principal, bool, error) {
	kind, ok := authzenKind(subj.Type)
	if !ok || strings.TrimSpace(subj.ID) == "" {
		return auth.Principal{}, false, nil
	}
	switch kind {
	case auth.KindUser:
		subjID := strings.TrimSpace(subj.ID)
		if strings.ToLower(strings.TrimSpace(subj.Type)) == auth.COAZSubjectMCPClient {
			p, found, err := s.authr.PrincipalForExternalID(ctx, subjID, aal)
			if err != nil {
				return auth.Principal{}, false, err
			}
			if found {
				return p, true, nil
			}
		}
		return s.authr.PrincipalForUser(ctx, subjID, aal)
	default: // KindToken
		id, err := model.ParseID(strings.TrimSpace(subj.ID))
		if err != nil || id.IsZero() {
			return auth.Principal{}, false, nil
		}
		return s.authr.PrincipalForToken(ctx, id)
	}
}

// authzenResourcePage reads one keyset page of candidate resource IDs of `kind`,
// optionally bounded by props["subtree"] (a root resource id → a single
// materialized-path subtree scan, resource kind only) and props["workspace"] (a
// workspace_id filter). It returns the IDs and the opaque store cursor for the next
// page ("" when exhausted). It only reads inside the View — Authorize runs afterwards.
func (s *Server) authzenResourcePage(ctx context.Context, tenant model.TenantID, kind string, props map[string]any, limit int, cursor string) ([]model.ID, string, error) {
	q := model.Query{Limit: limit, Cursor: cursor}
	if ws := propString(props, "workspace", "workspace_id"); ws != "" {
		if id, err := model.ParseID(ws); err == nil && !id.IsZero() {
			q.Filters = append(q.Filters, model.Filter{Column: "workspace_id", Op: model.OpEq, Value: id.String()})
		}
	}
	var ids []model.ID
	var page model.Page
	err := s.st.View(ctx, tenant, func(sc store.Scope) error {
		var (
			rows []model.Resource
			pg   model.Page
			e    error
		)
		switch kind {
		case "resource":
			if root := propString(props, "subtree", "parent"); root != "" {
				rid, perr := model.ParseID(root)
				if perr != nil || rid.IsZero() {
					return errBadRequest
				}
				rows, pg, e = sc.Resources().Subtree(ctx, rid, q)
			} else {
				rows, pg, e = sc.Resources().List(ctx, q)
			}
			if e != nil {
				return e
			}
			for _, x := range rows {
				ids = append(ids, x.ID)
			}
		case "agent":
			ag, p2, e2 := sc.Agents().List(ctx, q)
			if e2 != nil {
				return e2
			}
			pg = p2
			for _, x := range ag {
				ids = append(ids, x.ID)
			}
		case "session":
			ss, p2, e2 := sc.Sessions().List(ctx, q)
			if e2 != nil {
				return e2
			}
			pg = p2
			for _, x := range ss {
				ids = append(ids, x.ID)
			}
		default:
			return errBadRequest
		}
		page = pg
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if page.HasMore {
		next = page.Cursor
	}
	return ids, next, nil
}

// authzenKind maps an AuthZEN subject type to a principal kind. Recognized types:
// user; token / api_token / service_account (all token principals);
// mcp_client (COAZ profile — an MCP client identity resolved via IdP
// subject link; the subject.id is the user's external_id or email).
func authzenKind(subjectType string) (auth.PrincipalKind, bool) {
	switch strings.ToLower(strings.TrimSpace(subjectType)) {
	case "user", auth.COAZSubjectMCPClient:
		return auth.KindUser, true
	case "token", "api_token", "service_account":
		return auth.KindToken, true
	default:
		return "", false
	}
}

// authzenPermission maps an AuthZEN action to an Olivares permission (the action name
// IS the permission string, e.g. "agent:read"). Empty is invalid.
func authzenPermission(act azAction) (auth.Permission, bool) {
	name := strings.TrimSpace(act.Name)
	if name == "" {
		return "", false
	}
	return auth.Permission(name), true
}

// authzenResource maps an AuthZEN resource to ResourceAttrs — deliberately ONLY its
// kind and id. The enforced request path (authzTenant/authzTenantEntity) builds a
// request from ResourceFor(perm) {Kind} plus, for an entity, {ID}; it NEVER carries a
// caller-supplied sensitivity, workspace or extra attribute. So the PDP adapter must
// not either: honoring caller `resource.properties` would let a PEP inject an
// attribute the enforced path never sees, yielding a decision that diverges from
// enforcement (and, for an agent/session that has no stored sensitivity, a forgeable
// `resource.sensitivity` a forbid keys on). For an entity (id set) the scoped engine
// resolves the TRUE scope (workspace/folder/group, and the resource's stored
// sensitivity) from the row — uncheatable. resource.properties used for bounding a
// resource SEARCH (subtree/workspace) are read separately in authzenResourcePage; they
// scope the candidate query, not the decision.
func authzenResource(res azResource) auth.ResourceAttrs {
	return auth.ResourceAttrs{Kind: strings.TrimSpace(res.Type), ID: strings.TrimSpace(res.ID)}
}

// authzenAAL extracts context.aal (JSON number/string) or returns def.
func authzenAAL(c azContext, def int) int {
	if c == nil {
		return def
	}
	v, ok := c["aal"]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return def
}

// authzenSubjectRef derives the AuthZEN (type,id) of a principal: a user is
// (user, user-id); a token is (token, token-id).
func authzenSubjectRef(p auth.Principal) (string, string) {
	if p.Kind == auth.KindToken {
		return "token", p.CredID.String()
	}
	return "user", p.UserID.String()
}

// authzenPopulation summarizes the enumerated candidate population for the response
// context, so a "who can access" answer never implicitly overclaims completeness.
func authzenPopulation(pop []auth.Principal) map[string]any {
	var users, tokens, superadmins int
	for _, p := range pop {
		if p.Superadmin {
			superadmins++
		}
		if p.Kind == auth.KindToken {
			tokens++
		} else {
			users++
		}
	}
	return map[string]any{
		"users": users, "tokens": tokens, "superadmins": superadmins, "total": len(pop),
		"scope": "tenant members + superadmins + active bound tokens; a free-form Cedar grant naming a non-member user directly is outside this population",
	}
}

// authzenLimit clamps page.limit to [1, authzenMaxLimit], defaulting when unset.
func authzenLimit(page *azPageRequest) int {
	if page == nil || page.Limit <= 0 {
		return authzenDefaultLimit
	}
	if page.Limit > authzenMaxLimit {
		return authzenMaxLimit
	}
	return page.Limit
}

// authzenDecodeOffset decodes an in-memory offset page token (subjects/actions).
func authzenDecodeOffset(page *azPageRequest) int {
	s := authzenDecodeToken(page)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return 0
}

// authzenDecodeToken base64url-decodes a page token to its raw resume marker ("" when
// absent or malformed — a fresh start).
func authzenDecodeToken(page *azPageRequest) string {
	if page == nil || page.Token == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(page.Token)
	if err != nil {
		return ""
	}
	return string(b)
}

// authzenEncodeToken base64url-encodes a resume marker into a page token ("" stays "").
func authzenEncodeToken(marker string) string {
	if marker == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(marker))
}

// firstSubject/firstAction/firstResource implement AuthZEN's object-level default
// override: an item's value wins, else the top-level default.
func firstSubject(item, def *azSubject) *azSubject {
	if item != nil {
		return item
	}
	return def
}

func firstAction(item, def *azAction) *azAction {
	if item != nil {
		return item
	}
	return def
}

func firstResource(item, def *azResource) *azResource {
	if item != nil {
		return item
	}
	return def
}

// propString returns the first present string value among keys in props.
func propString(props map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := props[k]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	return ""
}
