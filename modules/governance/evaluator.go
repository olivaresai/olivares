// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errCorruptPolicy is returned by Evaluate when an enabled abac policy's stored
// spec cannot be parsed. Because the spec is typed and re-marshaled on write
// (policy.go), this is only reachable by external tampering — and a security
// product must fail CLOSED (deny) rather than silently skip a deny-rule an
// attacker corrupted.
var errCorruptPolicy = errors.New("governance: enabled abac policy has an unparseable spec")

// evaluator is the module's native ABAC engine. It implements the core
// auth.PolicyEvaluator seam (docs/contracts): it runs AFTER RBAC and may
// only FURTHER-RESTRICT — every rule is a DENY rule, so the worst it can do is turn
// an RBAC allow into a deny; it can never widen a grant (Allow=true is the identity
// of the AND the Authorizer applies). It is a process-wide singleton shared across
// tenants, so the per-tenant cache keyed on the EXACT req.Tenant is the whole
// isolation boundary; it is safe for concurrent use.
type evaluator struct {
	data  api.ModuleData
	mu    sync.RWMutex
	cache map[model.TenantID]*compiledSet
}

// compiledSet is the immutable, cached deny-rule set for one tenant. Once built it
// is never mutated, so concurrent Evaluate readers need no per-rule locking. err is
// set when an enabled policy's spec was corrupt — the whole set then denies.
type compiledSet struct {
	rules []abacRule
	err   error
}

var _ auth.PolicyEvaluator = (*evaluator)(nil)

// allowDecision is the no-restriction decision: the RBAC decision stands.
func allowDecision(reason string) auth.Decision { return auth.Decision{Allow: true, Reason: reason} }

// Evaluate decides whether the request survives the tenant's abac deny-rules. It
// short-circuits the system/zero tenant (superadmin and system-tenant operations
// are never further-restricted, and a zero-tenant store read would fail closed),
// fails OPEN on a transient policy-load error (not enforcing a restriction cannot
// escalate, whereas denying every request would be an outage), and fails CLOSED on
// a corrupt enabled policy (tamper defense).
func (e *evaluator) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	if req.Tenant.IsZero() || req.Tenant.IsSystem() {
		return allowDecision("no policy restriction"), nil
	}
	if e.data == nil {
		return allowDecision("no policy engine wired"), nil
	}
	set, loadErr := e.compiledFor(ctx, req.Tenant)
	if loadErr != nil {
		// Store unavailable: ABAC only restricts, so leaving the restriction
		// temporarily unenforced cannot grant anything the RBAC layer did not.
		return allowDecision("policy load unavailable"), nil
	}
	if set.err != nil {
		return auth.Decision{}, set.err // -> Authorizer denies (fail closed)
	}
	for i := range set.rules {
		if set.rules[i].matches(req) {
			// An authored ABAC rule matched: business policy (shadowable). A store
			// tampering/corruption deny is the set.err path above (ClassInvariant).
			return auth.Decision{Allow: false, Reason: "abac: denied by policy", Class: auth.ClassPolicy}, nil
		}
	}
	return allowDecision("no matching deny rule"), nil
}

// compiledFor returns the cached deny-rule set for a tenant, loading it on a miss.
// The store read happens OUTSIDE the lock; the result is published under the write
// lock with a re-check so a concurrent loader does not clobber. A store error is
// returned (not cached) so the next request retries when the store recovers.
func (e *evaluator) compiledFor(ctx context.Context, tenant model.TenantID) (*compiledSet, error) {
	e.mu.RLock()
	set, ok := e.cache[tenant]
	e.mu.RUnlock()
	if ok {
		return set, nil
	}
	loaded, err := e.load(ctx, tenant)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cache == nil {
		e.cache = map[model.TenantID]*compiledSet{}
	}
	if existing, ok := e.cache[tenant]; ok {
		return existing, nil
	}
	e.cache[tenant] = loaded
	return loaded, nil
}

// load reads the tenant's enabled abac policies and compiles their deny-rules.
// A policy whose stored spec cannot be parsed sets the set's err (deny-all for the
// tenant until a write invalidates the cache). A store error is returned so the
// caller fails open rather than caching a wrong empty set.
func (e *evaluator) load(ctx context.Context, tenant model.TenantID) (*compiledSet, error) {
	cs := &compiledSet{}
	err := e.data.View(ctx, tenant, func(sc store.Scope) error {
		q := model.Query{Filters: []model.Filter{eq("kind", policyKindABAC), eq("enabled", true)}, Limit: listCap}
		for {
			pols, page, err := sc.Policies().List(ctx, q)
			if err != nil {
				return err
			}
			for _, p := range pols {
				spec, perr := parseABACSpec(p.Spec)
				if perr != nil {
					cs.err = errCorruptPolicy
					cs.rules = nil
					return nil // stop compiling: the set already denies
				}
				cs.rules = append(cs.rules, spec.Rules...)
			}
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			q.Cursor = page.Cursor
		}
	})
	if err != nil {
		return nil, err
	}
	return cs, nil
}

// invalidate drops a tenant's cached set so the next Evaluate reloads it. It MUST
// be called only AFTER a policy-write transaction commits, never inside it, or a
// concurrent reload could repopulate the cache with the pre-commit state and leave
// the new rule permanently unenforced (the stale-allow window).
func (e *evaluator) invalidate(tenant model.TenantID) {
	e.mu.Lock()
	delete(e.cache, tenant)
	e.mu.Unlock()
}

// parseABACSpec re-parses a stored Policy.Spec map into the typed abac rule set.
// Because the spec was canonicalised through the typed struct on write, this
// round-trips cleanly; a parse failure means the row was tampered with.
func parseABACSpec(spec map[string]any) (abacSpec, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return abacSpec{}, err
	}
	var out abacSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return abacSpec{}, err
	}
	return out, nil
}

// matches reports whether a deny-rule applies to a request. Every non-empty field
// is ANDed; a rule with NO selector is rejected at write time, so this is never a
// match-everything (deny-all) rule. It keys ONLY on attributes that actually reach
// the evaluator today — the principal kind and the permission (and the verb /
// resource derived from it); resource-attribute rules (sensitivity) need a core
// resource-attrs seam and are intentionally NOT part of the v1 grammar (policy.go).
func (r abacRule) matches(req auth.Request) bool {
	if r.Permission != "" && r.Permission != string(req.Permission) {
		return false
	}
	if r.Verb != "" && r.Verb != req.Permission.Verb() {
		return false
	}
	if r.Resource != "" && r.Resource != permResource(req.Permission) {
		return false
	}
	if r.PrincipalKind != "" && r.PrincipalKind != string(req.Principal.Kind) {
		return false
	}
	// a min_aal rule matches (denies) ONLY a principal whose verified
	// session assurance is below the bar. Principal.AAL is 0 for tokens and is
	// already freshness-degraded for sessions (auth.effectiveAAL), so an
	// expired step-up is denied like a never-elevated one.
	if r.MinAAL > 0 && req.Principal.AAL >= r.MinAAL {
		return false
	}
	return true
}

// permResource returns the resource segment of a permission: the segment just
// before the trailing verb ("agent:write" -> "agent", "governance:policy:write" ->
// "policy"). It is "" for a malformed permission with no verb separator.
func permResource(p auth.Permission) string {
	s := string(p)
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return ""
	}
	prefix := s[:i]
	if j := strings.LastIndexByte(prefix, ':'); j >= 0 {
		return prefix[j+1:]
	}
	return prefix
}
