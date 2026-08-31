// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// IDN-09 external PDP wiring: the operator chooses ONE external policy engine —
// embedded Cedar (the pure-Go default, no sidecar) or OPA over HTTP (for Rego shops)
// — behind the same auth.PolicyEvaluator seam. It is composed with the module's
// native ABAC engine into a deny-only chain: a request must survive every evaluator,
// so adding a PDP can only further-restrict, never widen (the Authorizer still ANDs
// the whole chain with RBAC).

// PDPEngine selects the external policy engine.
type PDPEngine string

const (
	// PDPNone disables the external PDP (only the native ABAC engine runs).
	PDPNone PDPEngine = ""
	// PDPCedar is the embedded, pure-Go Cedar engine (default external PDP).
	PDPCedar PDPEngine = "cedar"
	// PDPOPA is the OPA-over-HTTP adapter.
	PDPOPA PDPEngine = "opa"
)

// PDPConfig is the operator-supplied external-PDP configuration (read from the
// environment by the composition root).
type PDPConfig struct {
	Engine PDPEngine
	// Logger (nil-safe) surfaces Cedar per-request policy evaluation errors.
	Logger *slog.Logger
	// Cedar:
	CedarPolicy string // the Cedar policy source (a set of forbid rules)
	// OPA:
	OPABaseURL      string
	OPADecisionPath string
	OPAToken        string
}

// NewExternalPDP builds the configured external PolicyEvaluator, or (nil, nil) when
// no engine is selected (the native ABAC engine stands alone). A misconfiguration
// (unknown engine, invalid Cedar policy, missing OPA url) is an error so a broken PDP
// config fails the boot rather than silently leaving requests un-governed.
func NewExternalPDP(cfg PDPConfig) (auth.PolicyEvaluator, error) {
	switch PDPEngine(strings.ToLower(string(cfg.Engine))) {
	case PDPNone, "none":
		return nil, nil
	case PDPCedar:
		return NewCedarEvaluator(cfg.CedarPolicy, cfg.Logger)
	case PDPOPA:
		return NewOPAEvaluator(cfg.OPABaseURL, cfg.OPADecisionPath, cfg.OPAToken, nil)
	default:
		return nil, fmt.Errorf("governance: unknown pdp engine %q (want cedar|opa|none)", cfg.Engine)
	}
}

// chainEvaluator ANDs several deny-only evaluators: a request survives only if every
// member allows. A member ERROR decides immediately (fail-closed). Otherwise the chain
// applies "invariant dominates" (see Evaluate): a ClassInvariant (or zero/unknown)
// deny decides immediately, while a ClassPolicy deny is remembered and decides only if
// no invariant appears. Because every member is restrict-only, the chain is too.
type chainEvaluator struct {
	members []auth.PolicyEvaluator
	onDeny  func(auth.Request, auth.Decision)
}

var _ auth.PolicyEvaluator = (*chainEvaluator)(nil)

// composeEvaluators builds the evaluator the Authorizer uses: the native ABAC engine
// plus any external PDP, with onDeny called when the chain restricts (the ledger
// audit hook). nil members are dropped. With a single member and no audit hook the
// member is returned directly.
func composeEvaluators(onDeny func(auth.Request, auth.Decision), members ...auth.PolicyEvaluator) auth.PolicyEvaluator {
	var ms []auth.PolicyEvaluator
	for _, m := range members {
		if m != nil {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return auth.DenyNothing{}
	}
	if len(ms) == 1 && onDeny == nil {
		return ms[0]
	}
	return &chainEvaluator{members: ms, onDeny: onDeny}
}

// Evaluate runs each member in order and returns the chain's restriction or error.
//
// E1b — "INVARIANT DOMINATES": the constrained-observe mode may shadow a
// ClassPolicy deny (allow-but-record) but NEVER a ClassInvariant one. If the chain
// stopped at the FIRST deny, an early business-policy deny (ABAC) would mask a later
// platform-invariant deny (an OPA transport error → fail-closed, a Cedar eval-fail),
// and shadowing that first policy deny in observe would silently drop the invariant.
// So the chain does not stop at the first policy deny: it short-circuits only on an
// invariant deny (nothing overrides it) or a member error (hardest fail-closed signal),
// and otherwise remembers the FIRST policy deny while scanning the rest for an invariant.
// Enforce and observe consume this SAME seam, so their evaluation never diverges; for
// enforce the AUTHORIZATION outcome is unchanged (any deny still denies). What can change
// vs the old first-deny short-circuit: the reported reason in a mixed chain (reporting
// the invariant is strictly more correct), and — because later members now run after a
// policy deny — extra evaluator work (an OPA POST, latency) and, on a later member error,
// the fail-closed audit path. That extra cost is intentional: the price of a single
// class-aware seam that chain ordering cannot fool.
// Shadowability is keyed on `!= ClassPolicy` here (i.e. zero/unknown dominates too),
// mirroring the fail-safe contract in core/auth (only an explicit ClassPolicy is soft).
// A member ERROR returns immediately WITHOUT calling onDeny: there is no decision to
// record, and the resulting fail-closed deny is audited by the Authorizer/governance
// layer (governance.go), not this per-restriction hook. onDeny fires exactly once, with
// the effective (returned) decision, on either the invariant-dominant or first-policy path.
func (c *chainEvaluator) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	var firstPolicyDeny *auth.Decision
	for _, m := range c.members {
		dec, err := m.Evaluate(ctx, req)
		if err != nil {
			return auth.Decision{}, err
		}
		if dec.Allow {
			continue
		}
		if dec.Class != auth.ClassPolicy {
			// A platform-invariant (or zero/unknown) deny dominates: return immediately.
			if c.onDeny != nil {
				c.onDeny(req, dec)
			}
			return dec, nil
		}
		if firstPolicyDeny == nil {
			d := dec
			firstPolicyDeny = &d
		}
	}
	if firstPolicyDeny != nil {
		if c.onDeny != nil {
			c.onDeny(req, *firstPolicyDeny)
		}
		return *firstPolicyDeny, nil
	}
	return auth.Decision{Allow: true, Reason: "no policy restriction"}, nil
}
