// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
)

// IDN-09 Cedar PDP: an embedded, pure-Go external policy engine behind the
// core/auth.PolicyEvaluator seam. Cedar is deny-by-default and forbid-overrides-
// permit; to keep the seam's RESTRICT-ONLY contract (the evaluator may only narrow an
// RBAC grant, never widen it, and an empty policy set must leave the RBAC decision
// standing) the engine is run as a FORBID OVERLAY: an implicit base
// `permit(principal, action, resource);` is compiled ahead of the operator's policy,
// so a Cedar Deny can only result from a matched `forbid` rule (a restriction), and a
// policy with no matching forbid yields Allow (the RBAC decision stands). This makes
// a Cedar `permit` unable to widen anything — the Authorizer still ANDs with RBAC.

// cedarBasePermit is the implicit base policy that turns Cedar's deny-by-default into
// the restrict-only overlay this seam requires.
const cedarBasePermit = "permit(principal, action, resource);\n"

// Fixed Cedar entity types. The resource KIND is carried as an attribute rather than
// the entity type, so a dotted kind like "core.agent" (not a valid Cedar type name)
// never breaks compilation.
const (
	cedarTypePrincipal = "Principal"
	cedarTypeAction    = "Action"
	cedarTypeResource  = "Resource"
)

// CedarEvaluator is an auth.PolicyEvaluator backed by an embedded Cedar policy set.
type CedarEvaluator struct {
	policies *cedar.PolicySet
	now      func() time.Time
	log      *slog.Logger // optional; logs per-policy evaluation errors (nil-safe)
}

var _ auth.PolicyEvaluator = (*CedarEvaluator)(nil)

// NewCedarEvaluator compiles the operator's Cedar source (a set of `forbid` rules)
// under the implicit base permit. A syntactically invalid policy fails here (so a
// broken policy is caught at load, not silently ignored on the hot path). logger
// (nil-safe) surfaces per-request policy EVALUATION errors — e.g. a forbid that
// accesses an attribute Cedar cannot resolve — which Cedar otherwise skips silently.
func NewCedarEvaluator(policySrc string, logger *slog.Logger) (*CedarEvaluator, error) {
	src := cedarBasePermit + policySrc
	ps, err := cedar.NewPolicySetFromBytes("governance.cedar", []byte(src))
	if err != nil {
		return nil, fmt.Errorf("governance: compile cedar policy: %w", err)
	}
	return &CedarEvaluator{policies: ps, now: time.Now, log: logger}, nil
}

// Evaluate maps the authorization request into a Cedar request — the resource's
// attributes (kind, sensitivity, ownership and any extra) onto the resource entity,
// and the request context (tenant, principal kind, permission, time) into the Cedar
// context — and returns Allow unless a forbid rule matches. It never widens: a Cedar
// Allow means "no restriction", a Cedar Deny means "a forbid matched".
func (c *CedarEvaluator) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	resID := req.Resource.ID
	if resID == "" {
		resID = "*" // a collection-level action: no specific resource id
	}
	resUID := cedar.NewEntityUID(cedarTypeResource, cedar.String(resID))

	// Extra first, then the canonical attributes LAST so they always win and are
	// ALWAYS present (even empty). A present-but-empty attribute makes an
	// attribute-based forbid (e.g. `resource.sensitivity == "secret"`) evaluate to
	// FALSE; an ABSENT attribute would instead make Cedar ERROR and silently skip the
	// forbid (neutralizing the deny rule). So kind/sensitivity are unconditional.
	attrs := cedar.RecordMap{}
	for k, v := range req.Resource.Extra {
		attrs[cedar.String(k)] = cedar.String(v)
	}
	attrs["kind"] = cedar.String(req.Resource.Kind)
	attrs["sensitivity"] = cedar.String(req.Resource.Sensitivity)
	entities := cedar.EntityMap{
		resUID: cedar.Entity{UID: resUID, Attributes: cedar.NewRecord(attrs)},
	}

	creq := cedar.Request{
		Principal: cedar.NewEntityUID(cedarTypePrincipal, cedar.String(string(req.Principal.CredID))),
		Action:    cedar.NewEntityUID(cedarTypeAction, cedar.String(string(req.Permission))),
		Resource:  resUID,
		Context: cedar.NewRecord(cedar.RecordMap{
			"tenant":         cedar.String(string(req.Tenant)),
			"principal_kind": cedar.String(string(req.Principal.Kind)),
			"permission":     cedar.String(string(req.Permission)),
			"sensitivity":    cedar.String(req.Resource.Sensitivity),
			"time":           cedar.Long(c.clock().Unix()),
		}),
	}

	decision, diag := cedar.Authorize(c.policies, entities, creq)
	// A policy that ERRORS during evaluation is skipped by Cedar (not a Deny). For a
	// security control that is dangerous to do silently — a forbid that errors would
	// fail to restrict with no signal — so surface it.
	if len(diag.Errors) > 0 && c.log != nil {
		c.log.Warn("cedar policy evaluation error (guard attribute access with `has`)",
			"errors", len(diag.Errors), "first", diag.Errors[0].PolicyID, "message", diag.Errors[0].Message)
	}
	// F-06: fail CLOSED when a FORBID rule errored. Cedar dropped it silently, so
	// the restriction would otherwise evaporate (fail-open on a deny rule). A forbid we
	// could not evaluate must be assumed to apply. This is SELECTIVE: a permit that errors
	// could only have widened, so it stays dropped. Cedar evaluates the policy head
	// (principal/action/resource scope) before the `when` conditions and short-circuits a
	// non-matching head, so a broken forbid errors — and thus denies — ONLY for the
	// requests that both match its scope AND hit the unresolved attribute; requests outside
	// the forbid's scope are untouched, and a `has`-guarded forbid never errors. Common
	// forbids (kind/sensitivity, always present) never reach here.
	if hasErroredForbid(c.policies, diag) {
		return auth.Decision{Allow: false, Reason: "cedar: forbid rule evaluation error (fail-closed)"}, nil
	}
	if decision == cedar.Allow {
		return auth.Decision{Allow: true, Reason: "cedar: no forbid matched"}, nil
	}
	// A cleanly-evaluated Cedar forbid rule matched: business policy (shadowable).
	// The errored-forbid fail-closed above stays ClassInvariant.
	return auth.Decision{Allow: false, Reason: "cedar: denied by policy", Class: auth.ClassPolicy}, nil
}

// clock returns the evaluator's time source (injectable for tests via the now field).
func (c *CedarEvaluator) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
