// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrNotWired is returned by Policy when the module has no data handle (used without
// the composition root). The decider treats any Policy error as DENY (deny-closed).
var ErrNotWired = errors.New("inferenceproxy: data handle not wired")

// Response-DLP modes: the prompt (request) DLP is always synchronous fail-closed;
// only the streaming RESPONSE has the buffer/pass-through tension.
//   - off    — do not scan the response.
//   - flag   — relay live, scan the accumulated response after close, emit a finding +
//     ledger note (CANNOT un-send; detective on the response).
//   - buffer — buffer the full response before relaying, so a denied class BLOCKS the
//     response (true fail-closed response DLP, at the cost of streaming latency). The
//     secure default; an operator can explicitly select flag or off.
const (
	ResponseDLPOff    = "off"
	ResponseDLPFlag   = "flag"
	ResponseDLPBuffer = "buffer"
)

// DLP rule actions and reserved classes (the vocabulary, reused for inference egress).
const (
	dlpAllow          = "allow"
	dlpDeny           = "deny"
	dlpClassAny       = "*"
	dlpClassUnscanned = "unscanned"
	dlpClassSecret    = "secret.credential"
)

// seededDLPRules is the tunable stock-deployment posture. These are effective default
// rules, not unconditional checks: a tenant-authored exact rule loaded later replaces
// the seeded action for that class. Keeping the defaults in the same class→action
// algebra means the public admin API remains the one override path.
func seededDLPRules() map[string]string {
	return map[string]string{
		dlpClassSecret:    dlpDeny,
		dlpClassUnscanned: dlpDeny,
		dlpClassAny:       dlpAllow,
	}
}

// dlpPolicy is one tenant's loaded inference-egress DLP rule set (class → action). It
// is the SAME deny-closed algebra as the knowledge plane's gate, applied to the
// prompt and the model response instead of retrieval/ingest.
type dlpPolicy struct {
	rules map[string]string
}

// enabled reports whether the effective rule set contains any DLP rule. A stock
// policy contains the seeded secret/unscanned denies, so it is enabled by default.
func (p dlpPolicy) enabled() bool { return len(p.rules) > 0 }

// decide returns the classes among the given ones whose egress the policy DENIES.
// Deny-closed: a class with neither an exact rule nor a "*" rule denies, and an
// unrecognized action denies. Clean content (no classes) is allowed.
func (p dlpPolicy) decide(classes []string) (denied []string) {
	if !p.enabled() {
		return nil
	}
	seen := map[string]bool{}
	for _, c := range classes {
		if seen[c] {
			continue
		}
		seen[c] = true
		action, ok := p.rules[c]
		if !ok {
			action = p.rules[dlpClassAny]
		}
		if action != dlpAllow {
			denied = append(denied, c)
		}
	}
	sort.Strings(denied)
	return denied
}

// unscannedDenied reports whether content WITHOUT a classification (the classifier did
// not run, or errored) may egress. Deny-closed: only an explicit
// {"class":"unscanned","action":"allow"} rule permits it — "*" deliberately does not
// cover unscanned (unprovable sensitivity needs its own opt-out).
func (p dlpPolicy) unscannedDenied() bool {
	if !p.enabled() {
		return false
	}
	return p.rules[dlpClassUnscanned] != dlpAllow
}

// RequestCeilings is the per-tenant, per-request consumption ceiling set (#19).
// Zero values mean "no ceiling". Enforce=false (the default) is OBSERVE mode.
type RequestCeilings struct {
	Enforce          bool
	MaxTokens        int64 // ceiling on max_tokens per request
	MaxToolUses      int64 // ceiling on a server tool's max_uses per request
	TaskBudgetTokens int64 // ceiling on (and enforce-mode injection of) output_config.task_budget.total
}

// Any reports whether the tenant configured at least one request ceiling.
func (c RequestCeilings) Any() bool {
	return c.MaxTokens > 0 || c.MaxToolUses > 0 || c.TaskBudgetTokens > 0
}

// ProxyPolicy is the resolved per-tenant governance config the composition-root decider
// reads once per request. Its zero value (the defaults applied when no config row
// exists) is the SAFE posture: every gate on, fail-CLOSED if the decision plane is
// down, response DLP in buffer mode, recording best-effort. A tenant tunes it via
// PUT /config; the DLP rule set rides along regardless of the config row.
type ProxyPolicy struct {
	// Configured is true when the tenant has a config row (it has tuned the proxy). When
	// false the defaults below apply — they are the safe posture, not "off".
	Configured bool

	// FailOpen: when the proxy's own decision plane (store/modules) is unreachable, allow
	// the request ungoverned (true) or deny with 503 (false). DEFAULT false (fail-closed).
	FailOpen bool
	// ResponseDLPMode is off | flag | buffer (default buffer).
	ResponseDLPMode string
	// RecordMandatory: the absence of ledger evidence is a DENY (no audit ⇒ no forward).
	// DEFAULT true.
	//
	// It used to default false, and the doctrine that was supposed to flip it never
	// landed. The default matters more than the flag: a tenant that has never opened the
	// config page is exactly the tenant nobody has reasoned about, and shipping it
	// "best-effort" meant the product's evidence guarantee was opt-in for everyone who had
	// not opted into anything. It is now symmetric with the gate flags — on unless
	// explicitly turned off — and turning it off is a recorded decision, not an omitted
	// JSON field.
	//
	// What it governs is the PRE-FORWARD anchor only. After the forward the call has
	// happened and no posture can undo it: that path is a loud gap by construction, and
	// the fail-mode matrix says so rather than implying otherwise.
	RecordMandatory bool
	// RecordMandatoryChosen reports whether an operator EXPLICITLY set the posture
	// above. False means the value is this build's default and nobody decided it.
	//
	// It exists because `Configured` answers a different question — "is there a config
	// row" — and a tenant who set the DLP mode and never mentioned evidence has that
	// true. Precedence rules that must not override a choice need to know there WAS
	// one, not that some unrelated field was written once.
	RecordMandatoryChosen bool

	// Per-gate toggles (default true). A gate that is off is simply not run. Gates with
	// native policy (for example residency) retain that policy; DLP has seeded stock rules.
	GateModelAccess   bool
	GateBudget        bool
	GateResidency     bool
	GateContextWindow bool
	GateDLPRequest    bool
	GateDLPResponse   bool

	Ceilings RequestCeilings

	dlp dlpPolicy
}

// DLPDecide returns the denied classes among those given (deny-closed; empty when DLP
// is allowed by the effective seeded-plus-tenant policy).
func (p ProxyPolicy) DLPDecide(classes []string) []string { return p.dlp.decide(classes) }

// DLPUnscannedDenied reports whether unclassifiable content is denied egress.
func (p ProxyPolicy) DLPUnscannedDenied() bool { return p.dlp.unscannedDenied() }

// DLPEnabled reports whether the effective seeded-plus-tenant rule set is active.
func (p ProxyPolicy) DLPEnabled() bool { return p.dlp.enabled() }

// PolicyWithDLPRules returns p with tenant rules overlaid on the seeded inference-egress
// DLP rule set (class → "allow"|"deny", with the reserved "*"/"unscanned" classes). It
// lets a caller compose the same effective policy as the store loader without a round-trip.
func PolicyWithDLPRules(p ProxyPolicy, rules map[string]string) ProxyPolicy {
	effective := seededDLPRules()
	if len(rules) > 0 {
		// Once a tenant authors policy, preserve the existing deny-closed algebra:
		// unmatched classified values deny unless the tenant explicitly writes "*".
		delete(effective, dlpClassAny)
	}
	for class, action := range rules {
		effective[class] = action
	}
	p.dlp = dlpPolicy{rules: effective}
	return p
}

// recordMandatoryChosen reports whether an operator EXPLICITLY set this row's evidence
// posture. It is not simply the column, because the column did not always exist.
//
// A row written before it was added scans as NULL, and reading NULL as "nobody chose" is
// right for every value except one. Before this change the wire field was a BARE BOOL
// (`RecordMandatory bool`, no pointer): encoding/json zeroed it to false whenever the
// request omitted it, and the handler stored that literal bool. So the handler could only
// ever write `true` when the request actually carried `record_mandatory: true`. A legacy
// `true` is therefore a PROVEN explicit choice — the only posture the old wire format
// could not produce by accident — and reading it as a default hands that operator's
// decision to the audit spool: `defaultMandatoryYieldsTo` would forward their call with an
// evidence gap on a declared `degrade`, which is precisely what they asked not to happen.
//
// A legacy `false` stays ambiguous — an omission and a deliberate opt-out are
// indistinguishable in that data, as the first contrast report already established — so it
// keeps reading as "nobody chose". Nothing is lost by that: with the value false the
// mandatory branch is never entered, so the provenance cannot change any outcome.
//
// This resolves a NULL from a proven property of the old format. It does not fabricate a
// decision, and it can only ever move a tenant towards denying rather than forwarding.
func recordMandatoryChosen(rec model.Record) bool {
	if b, ok := rec[colRecordMandatoryChosen].(bool); ok {
		return b // the column exists on this row and says what it says
	}
	return rec.Bool(colRecordMandatory)
}

// defaultProxyPolicy is the safe posture applied when a tenant has no config row.
func defaultProxyPolicy() ProxyPolicy {
	return ProxyPolicy{
		FailOpen:          false,
		ResponseDLPMode:   ResponseDLPBuffer,
		RecordMandatory:   true,
		GateModelAccess:   true,
		GateBudget:        true,
		GateResidency:     true,
		GateContextWindow: true,
		GateDLPRequest:    true,
		GateDLPResponse:   true,
		dlp:               dlpPolicy{rules: seededDLPRules()},
	}
}

// Policy resolves a tenant's proxy governance config + DLP rule set in ONE read
// transaction, using the module-level (tenant-parameterized) data handle so the
// in-band proxy can call it WITHOUT a request ModuleContext. A nil data handle (module
// used without the composition root) or any store error is returned to the caller — the
// decider treats a read error as DENY (deny-closed), the same posture as the model-
// access gate. A tenant with no config row gets the safe defaults (NOT an error).
func (m *Module) Policy(ctx context.Context, tenant model.TenantID) (ProxyPolicy, error) {
	data := m.moduleData()
	if data == nil {
		return ProxyPolicy{}, ErrNotWired
	}
	pol := defaultProxyPolicy()
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		// Config singleton (zero or one row per tenant).
		if rec, ok, err := singletonConfig(ctx, sc); err != nil {
			return err
		} else if ok {
			pol.Configured = true
			pol.FailOpen = rec.Bool(colFailOpen)
			pol.ResponseDLPMode = normalizeResponseMode(rec.String(colResponseDLPMode))
			pol.RecordMandatory = rec.Bool(colRecordMandatory)
			pol.RecordMandatoryChosen = recordMandatoryChosen(rec)
			pol.GateModelAccess = rec.Bool(colGateModelAccess)
			pol.GateBudget = rec.Bool(colGateBudget)
			pol.GateResidency = rec.Bool(colGateResidency)
			pol.GateContextWindow = rec.Bool(colGateContextWin)
			pol.GateDLPRequest = rec.Bool(colGateDLPRequest)
			pol.GateDLPResponse = rec.Bool(colGateDLPResponse)
			pol.Ceilings = RequestCeilings{
				Enforce:          rec.Bool(colCeilingsEnforce),
				MaxTokens:        rec.Int(colCeilingMaxTokens),
				MaxToolUses:      rec.Int(colCeilingMaxToolUses),
				TaskBudgetTokens: rec.Int(colCeilingTaskBudgetTokens),
			}
		}
		// DLP rule set (independent of the config row).
		dlp, err := loadDLPPolicy(ctx, sc)
		if err != nil {
			return err
		}
		pol.dlp = dlp
		return nil
	})
	if err != nil {
		return ProxyPolicy{}, err
	}
	return pol, nil
}

// singletonConfig reads the tenant's one config row (if any) inside an open scope.
func singletonConfig(ctx context.Context, sc store.Scope) (model.Record, bool, error) {
	repo, err := sc.Ext(configKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

// loadDLPPolicy reads the tenant's inference-egress DLP rules inside an open scope. An
// error from the store fails the caller's transaction (and with it Policy) — the gate
// never degrades to allow.
func loadDLPPolicy(ctx context.Context, sc store.Scope) (dlpPolicy, error) {
	recs, err := allRules(ctx, sc)
	if err != nil {
		return dlpPolicy{}, err
	}
	p := dlpPolicy{rules: seededDLPRules()}
	if len(recs) > 0 {
		delete(p.rules, dlpClassAny)
	}
	for _, rec := range recs {
		p.rules[rec.String(colClass)] = rec.String(colAction)
	}
	return p, nil
}

// normalizeResponseMode maps a stored/incoming response-DLP mode to a known value,
// defaulting to preventive buffering (never off or detective-only by accident).
func normalizeResponseMode(s string) string {
	switch s {
	case ResponseDLPOff, ResponseDLPFlag, ResponseDLPBuffer:
		return s
	default:
		return ResponseDLPBuffer
	}
}
