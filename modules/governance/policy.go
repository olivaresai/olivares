// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// Policy kinds this module authors over the core Policy entity.
const (
	policyKindABAC     = "abac"
	policyKindApproval = "approval"
)

// Bounds on a policy spec — a typed, bounded, re-marshaled spec is the guarantee
// that no secret/credential can be round-tripped into a policy (docs/SECURITY-HARDENING.md),
// mirroring capabilities' secret_refs caps.
const (
	maxABACRules     = 64
	maxMatchLen      = 128
	maxApprovalCount = 64
	maxSeconds       = int64(365 * 24 * 3600) // a year, the cap on expiry/escalation windows
)

// abacRule is one DENY rule. v1 keys only on the attributes that actually reach
// the core PolicyEvaluator (principal kind + permission/verb/resource); a
// resource-attribute rule (sensitivity) would need a core resource-attrs seam and
// is intentionally NOT part of the grammar so the module never ships syntax that
// silently never fires.
type abacRule struct {
	// Deny must be true: v1 supports only deny rules (the seam can only restrict).
	Deny bool `json:"deny"`
	// Permission matches the full permission string, e.g. "agent:write" or
	// "governance:policy:admin" (exact match).
	Permission string `json:"permission,omitempty"`
	// Verb matches the trailing verb: read | write | admin.
	Verb string `json:"verb,omitempty"`
	// Resource matches the resource segment (the segment before the verb).
	Resource string `json:"resource,omitempty"`
	// PrincipalKind matches the caller kind: user | token.
	PrincipalKind string `json:"principal_kind,omitempty"`
	// MinAAL matches a principal whose session assurance is BELOW the
	// given level (1-3): the rule denies an under-assured caller, expressing
	// "this surface needs an AAL3 step-up" as tenant policy. A token principal
	// has assurance 0 (no human assurance) and is denied by any min_aal rule —
	// scope the rule with principal_kind "user" to gate only humans.
	MinAAL int `json:"min_aal,omitempty"`
}

// abacSpec is the spec of an "abac" policy.
type abacSpec struct {
	Rules []abacRule `json:"rules"`
}

// approvalMatch selects which requested actions a human-in-the-loop policy governs.
type approvalMatch struct {
	Action      string `json:"action,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
}

// approvalSpec is the spec of an "approval" policy: how many approvals an
// in-scope request needs, its timeout/escalation windows, and the
// action's explicit risk tier. An empty RiskTier defers to the built-in default
// classification (risktier.go); a set one is authoritative for the matched
// actions — the operator's audited way to grow or shrink the CRITICAL set
//. A "critical" tier floors the threshold at two distinct human
// approvers regardless of RequiredApprovals.
type approvalSpec struct {
	RequiredApprovals int           `json:"required_approvals,omitempty"`
	ExpiresInSeconds  int64         `json:"expires_in_seconds,omitempty"`
	EscalateInSeconds int64         `json:"escalate_in_seconds,omitempty"`
	RiskTier          string        `json:"risk_tier,omitempty"`
	Match             approvalMatch `json:"match,omitempty"`
}

// policyRequest is the create/update body. Spec is a raw message parsed against
// the typed spec for Kind (DisallowUnknownFields), so an unknown/free field is
// rejected and a value can never reach storage.
type policyRequest struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Spec    json.RawMessage `json:"spec"`
}

// policyDTO is the stored policy view.
type policyDTO struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Enabled bool           `json:"enabled"`
	Spec    map[string]any `json:"spec"`
}

func toPolicyDTO(p model.Policy) policyDTO {
	spec := p.Spec
	if spec == nil {
		spec = map[string]any{}
	}
	return policyDTO{ID: p.ID.String(), Name: p.Name, Kind: p.Kind, Enabled: p.Enabled, Spec: spec}
}

// validateAndCanonicalize parses and bounds a policy request's spec against its
// kind and returns the canonical spec map to persist (re-marshaled through the
// typed struct, so unknown/free fields and any smuggled value are dropped). It
// returns a client-facing message on invalid input.
func (in *policyRequest) validateAndCanonicalize() (map[string]any, string) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, "name is required"
	}
	in.Kind = strings.TrimSpace(strings.ToLower(in.Kind))
	switch in.Kind {
	case policyKindABAC:
		return canonicalizeABAC(in.Spec)
	case policyKindApproval:
		return canonicalizeApproval(in.Spec)
	default:
		return nil, "kind must be one of abac, approval"
	}
}

func canonicalizeABAC(raw json.RawMessage) (map[string]any, string) {
	spec, err := strictUnmarshalABAC(raw)
	if err != nil {
		return nil, "invalid abac spec: " + err.Error()
	}
	if len(spec.Rules) == 0 {
		return nil, "an abac policy needs at least one rule"
	}
	if len(spec.Rules) > maxABACRules {
		return nil, "too many rules"
	}
	for i := range spec.Rules {
		r := &spec.Rules[i]
		r.Permission = strings.TrimSpace(r.Permission)
		r.Verb = strings.TrimSpace(strings.ToLower(r.Verb))
		r.Resource = strings.TrimSpace(r.Resource)
		r.PrincipalKind = strings.TrimSpace(strings.ToLower(r.PrincipalKind))
		if !r.Deny {
			return nil, "every abac rule must be a deny rule (the engine seam can only restrict; an allow rule could never widen an RBAC grant)"
		}
		if r.Verb != "" && r.Verb != auth.VerbRead && r.Verb != auth.VerbWrite && r.Verb != auth.VerbAdmin {
			return nil, "rule verb must be one of read, write, admin"
		}
		if r.PrincipalKind != "" && r.PrincipalKind != string(auth.KindUser) && r.PrincipalKind != string(auth.KindToken) {
			return nil, "rule principal_kind must be one of user, token"
		}
		if len(r.Permission) > maxMatchLen || len(r.Resource) > maxMatchLen {
			return nil, "rule match value too long"
		}
		if containsInlineCredential(r.Permission) || containsInlineCredential(r.Resource) {
			return nil, "rule match value must not contain a credential"
		}
		if r.MinAAL < 0 || r.MinAAL > 3 {
			return nil, "rule min_aal must be between 1 and 3"
		}
		// min_aal counts as a selector: it narrows the rule to under-assured
		// principals (it can never become a deny-everything rule — an AAL3
		// session always passes it).
		if r.Permission == "" && r.Verb == "" && r.Resource == "" && r.PrincipalKind == "" && r.MinAAL == 0 {
			return nil, "a rule must select at least one of permission, verb, resource, principal_kind, min_aal (a rule with no selector would deny every request)"
		}
	}
	return toMap(spec)
}

func canonicalizeApproval(raw json.RawMessage) (map[string]any, string) {
	spec, err := strictUnmarshalApproval(raw)
	if err != nil {
		return nil, "invalid approval spec: " + err.Error()
	}
	if spec.RequiredApprovals < 0 || spec.RequiredApprovals > maxApprovalCount {
		return nil, "required_approvals out of range"
	}
	if spec.ExpiresInSeconds < 0 || spec.ExpiresInSeconds > maxSeconds || spec.EscalateInSeconds < 0 || spec.EscalateInSeconds > maxSeconds {
		return nil, "expiry/escalation window out of range"
	}
	spec.Match.Action = strings.TrimSpace(spec.Match.Action)
	spec.Match.SubjectKind = strings.TrimSpace(spec.Match.SubjectKind)
	if len(spec.Match.Action) > maxMatchLen || len(spec.Match.SubjectKind) > maxMatchLen {
		return nil, "match value too long"
	}
	spec.RiskTier = strings.TrimSpace(strings.ToLower(spec.RiskTier))
	if spec.RiskTier != "" && !validRiskTier(spec.RiskTier) {
		return nil, "risk_tier must be one of low, medium, high, critical"
	}
	// Authoring-time coherence: a policy cannot declare an action CRITICAL and
	// simultaneously set a sub-floor threshold. The create/decide floor would
	// override it anyway (deny-closed), but rejecting the contradiction here makes
	// the misconfiguration visible to its author instead of silently corrected.
	if spec.RiskTier == string(RiskTierCritical) &&
		spec.RequiredApprovals > 0 && spec.RequiredApprovals < criticalApprovalFloor {
		return nil, "a critical action requires at least 2 approvals (NIST AC-3(2) dual authorization); leave required_approvals unset or set it >= 2"
	}
	return toMap(spec)
}

// strictUnmarshalABAC / strictUnmarshalApproval decode a spec rejecting unknown
// fields. An absent spec ("" / null) is treated as an empty object so the
// kind-specific validation (which requires rules / bounds) produces the message.
func strictUnmarshalABAC(raw json.RawMessage) (abacSpec, error) {
	var s abacSpec
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	err := dec.Decode(&s)
	return s, err
}

func strictUnmarshalApproval(raw json.RawMessage) (approvalSpec, error) {
	var s approvalSpec
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	err := dec.Decode(&s)
	return s, err
}

// parseApprovalSpec re-parses a stored approval Policy.Spec map into the typed
// approval spec (the canonical round-trip written by canonicalizeApproval).
func parseApprovalSpec(spec map[string]any) (approvalSpec, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return approvalSpec{}, err
	}
	var out approvalSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return approvalSpec{}, err
	}
	return out, nil
}

// toMap renders a typed spec to the canonical map[string]any persisted in
// Policy.Spec (so only typed fields ever reach storage).
func toMap(v any) (map[string]any, string) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, "could not encode spec"
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "could not encode spec"
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, ""
}

// handleListPolicies lists governance policies, optionally filtered by kind/enabled.
func (m *Module) handleListPolicies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("kind"); v != "" {
		q.Filters = append(q.Filters, eq("kind", strings.ToLower(v)))
	}
	if v := r.URL.Query().Get("enabled"); v == "true" || v == "false" {
		q.Filters = append(q.Filters, eq("enabled", v == "true"))
	}
	out := listResponse[policyDTO]{Items: []policyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		// Only the two governance kinds; other modules may store their own Policy
		// rows (finops budgets, models routing), which this view must not surface.
		list, page, err := sc.Policies().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, p := range list {
			if p.Kind == policyKindABAC || p.Kind == policyKindApproval {
				out.Items = append(out.Items, toPolicyDTO(p))
			}
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetPolicy returns one governance policy.
func (m *Module) handleGetPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   policyDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindABAC && p.Kind != policyKindApproval {
			return nil // not a governance policy — treat as not found below
		}
		found, out = true, toPolicyDTO(p)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreatePolicy authors a governance policy. Admin-tier and self-audited; the
// ABAC cache is invalidated AFTER the write commits.
func (m *Module) handleCreatePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in policyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	canon, msg := in.validateAndCanonicalize()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out policyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		candidate := model.Policy{Name: in.Name, Kind: in.Kind, Spec: canon, Enabled: in.Enabled}
		advance, err := policyChangeAdvancesAuthorizationEpoch(nil, &candidate)
		if err != nil {
			return err
		}
		if advance {
			// The epoch CAS is the first effective write and shares this transaction
			// with the policy row and its audit event.
			if err := advancePolicyAuthorizationEpoch(r.Context(), sc); err != nil {
				return err
			}
		}
		p, err := sc.Policies().Create(r.Context(), candidate)
		if err != nil {
			return err
		}
		out = toPolicyDTO(p)
		return auditEvent(r.Context(), sc, mc, "governance.policy.create", model.Kind("core.policy"), p.ID, map[string]any{"kind": in.Kind, "enabled": in.Enabled})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.eval.invalidate(mc.Tenant) // post-commit: the new rule takes effect on the next evaluate
	m.emitPolicyChanged(r.Context(), mc.Tenant, model.ID(out.ID), in.Kind, event.PolicyOpCreated, in.Enabled)
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdatePolicy updates a governance policy in place (kind is immutable).
// Admin-tier, self-audited; invalidates the ABAC cache after commit.
func (m *Module) handleUpdatePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in policyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	canon, msg := in.validateAndCanonicalize()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out     policyDTO
		changed bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindABAC && p.Kind != policyKindApproval {
			return store.ErrNotFound // not a governance policy
		}
		if p.Kind != in.Kind {
			return validationError("policy kind is immutable")
		}
		candidate := p
		candidate.Name, candidate.Spec, candidate.Enabled = in.Name, canon, in.Enabled
		equal, err := policiesCanonicallyEqual(p, candidate)
		if err != nil {
			return policyAuthorizationEpochUnavailable("compare policy replay", err)
		}
		if equal {
			out = toPolicyDTO(p)
			return nil
		}
		advance, err := policyChangeAdvancesAuthorizationEpoch(&p, &candidate)
		if err != nil {
			return err
		}
		if advance {
			// Reads above establish impact; the epoch CAS remains the first write.
			if err := advancePolicyAuthorizationEpoch(r.Context(), sc); err != nil {
				return err
			}
		}
		p, err = sc.Policies().Update(r.Context(), candidate)
		if err != nil {
			return err
		}
		changed = true
		out = toPolicyDTO(p)
		return auditEvent(r.Context(), sc, mc, "governance.policy.update", model.Kind("core.policy"), p.ID, map[string]any{"kind": in.Kind, "enabled": in.Enabled})
	})
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if changed {
		m.eval.invalidate(mc.Tenant)
		m.emitPolicyChanged(r.Context(), mc.Tenant, id, in.Kind, event.PolicyOpUpdated, in.Enabled)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeletePolicy deletes a governance policy. Admin-tier, self-audited;
// invalidates the ABAC cache after commit.
func (m *Module) handleDeletePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	// The kind is read inside the transaction; captured so the post-commit
	// policy.changed event can carry it.
	var deletedKind string
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindABAC && p.Kind != policyKindApproval {
			return store.ErrNotFound
		}
		deletedKind = p.Kind
		advance, err := policyChangeAdvancesAuthorizationEpoch(&p, nil)
		if err != nil {
			return err
		}
		if advance {
			// The policy read determines impact; the epoch CAS is the first write.
			if err := advancePolicyAuthorizationEpoch(r.Context(), sc); err != nil {
				return err
			}
		}
		if err := sc.Policies().Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.policy.delete", model.Kind("core.policy"), id, map[string]any{"kind": p.Kind})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.eval.invalidate(mc.Tenant)
	m.emitPolicyChanged(r.Context(), mc.Tenant, id, deletedKind, event.PolicyOpDeleted, false)
	writeJSON(w, http.StatusNoContent, nil)
}
