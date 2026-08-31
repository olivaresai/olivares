// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// delegate.go is the governed A2A v1.0.1 delegation client (AIP-05 actuate). It
// is the full Policy-Enforcement-Point in front of Task delegation, built on the
// emission primitive (emit_task.go): every outbound delegation passes, in order,
//
//	verify signed AgentCard (deny-closed: trustVerified only)
//	  → allowlist (deny-by-default (agent, skill, scope))
//	  → PlanHash (anti-TOCTOU binding)
//	  → ApprovalGate (HITL seam, deny-closed)
//	  → emit one SendMessage
//	  → map the 9-state Task FSM (interrupts surfaced, never success)
//
// and only then does a Task leave for the remote agent. It also speaks the rest of
// the v1.0.1 method surface for reconciliation and lifecycle: GetTask, CancelTask,
// ListTasks (cursor pagination), GetExtendedAgentCard. SendStreamingMessage /
// SubscribeToTask (SSE) live in stream.go; the push-notification receiver in
// pushrecv.go. gRPC / HTTP+JSON/REST bindings are reserved behind the transport seam
// (transport.go); there is no WebSocket binding in v1.0.
//
// Boundary (LICENSING.md): Apache-2.0, /sdk + stdlib + go-jose only. The ApprovalGate
// and the decision Auditor are SEAMS the composition root (cmd, AGPL) wires to the
// real bridge + ledger/OTel span — a connector may not import /core. Minimal data
// (docs/SECURITY-HARDENING.md): the PEP reasons over references + scopes; credentials stay out-of-band
// in HTTP headers; the audited decision carries no message text and no token.

// DecisionAuditor records each delegation decision (allow or deny) for the ledger +
// an OTel span. It is a seam: the connector emits a minimal-data, non-sensitive
// record; the composition root writes it to the hash-chained audit ledger (docs/SECURITY-HARDENING.md
// §1) and decorates the W3C-trace span. The default is a no-op.
type DecisionAuditor interface {
	Record(ctx context.Context, dec DelegationDecision)
}

// DelegationDecision is the minimal-data audit record of one delegation attempt. It
// carries references, the bound plan, the gate verdict and the resulting Task state —
// NEVER the message text, the credentials, or a payload (docs/SECURITY-HARDENING.md).
type DelegationDecision struct {
	Tenant      string
	AgentName   string
	Skill       string
	Scope       string
	PlanHash    string
	Allowed     bool
	Reason      string // short, non-sensitive (e.g. "allowlist deny", "gate not approved", "delegated")
	ApprovalRef string
	State       TaskState
	RequestedBy string
	TraceParent string
	// Objective is the delegation's purpose label (DelegateSpec.Objective) — the "with
	// what objective" dimension of the communication graph. Minimal data: a goal ref,
	// never the task text.
	Objective string
	// ChainDepth / ChainRoot describe the multi-agent lineage this delegation sits in:
	// the number of agents already in the chain and the correlation root. They let the
	// audit/observability layer reconstruct the multi-agent task tree (who delegated to
	// whom) without persisting any payload.
	ChainDepth int
	ChainRoot  string
	// Principal is the chain's ORIGINAL on-behalf-of principal (the RFC 8693 `sub`
	// position): who the multi-agent task is ultimately FOR, as distinct from
	// RequestedBy (the actor asking at THIS hop). "" when the lineage predates this
	// plane and no principal was propagated — reported unknown, never fabricated.
	Principal string
	At        time.Time
}

// nopAuditor is the default DecisionAuditor: it records nothing (the cmd wires the
// real ledger/OTel auditor). It is never a security gap — the gate, not the auditor,
// authorizes; the auditor only observes.
type nopAuditor struct{}

func (nopAuditor) Record(context.Context, DelegationDecision) {}

// DelegatorConfig configures a governed Delegator. Emit is the underlying emission
// transport config (trust anchor + out-of-band headers + TLS posture, emit_task.go).
// Allowlist and Gate are the PEP; a nil Allowlist denies every delegation and a nil
// Gate denies every delegation (both deny-closed). Auditor is the ledger/OTel seam
// (nil ⇒ no-op). Clock is injectable for tests.
type DelegatorConfig struct {
	Emit      EmitConfig
	Allowlist *Allowlist
	Gate      DelegationGate
	Auditor   DecisionAuditor
	// ChainPolicy bounds multi-agent delegation lineages (max depth + no cycles,
	// chain.go). The zero value resolves to a safe default depth (never unbounded).
	ChainPolicy ChainPolicy
	Clock       func() time.Time
}

// Delegator is the governed A2A v1.0.1 delegation client. Construct it with
// NewDelegator; it is safe for concurrent use.
type Delegator struct {
	client      *Client
	allowlist   *Allowlist
	gate        DelegationGate
	auditor     DecisionAuditor
	chainPolicy ChainPolicy
	now         func() time.Time
}

// NewDelegator builds a governed Delegator, defaulting every governance seam to its
// deny-closed / no-op safe value: a nil Allowlist becomes an empty (deny-all)
// allowlist, a nil Gate becomes the deny-closed denyDelegationGate, a nil Auditor
// becomes the no-op auditor. So a Delegator constructed with zero governance config
// can NEVER delegate — the safe default.
func NewDelegator(cfg DelegatorConfig) *Delegator {
	d := &Delegator{
		client:      NewClient(cfg.Emit),
		allowlist:   cfg.Allowlist,
		gate:        cfg.Gate,
		auditor:     cfg.Auditor,
		chainPolicy: cfg.ChainPolicy,
		now:         cfg.Clock,
	}
	if d.allowlist == nil {
		d.allowlist = NewAllowlist(nil) // deny-all
	}
	if d.gate == nil {
		d.gate = denyDelegationGate{} // deny-closed
	}
	if d.auditor == nil {
		d.auditor = nopAuditor{}
	}
	if d.now == nil {
		d.now = time.Now
	}
	return d
}

// DelegateSpec describes one prospective delegation. It carries the remote agent
// reference, the skill + least-privilege scope being exercised, and the (minimal-data)
// task instruction. Tenant + RequestedBy are audit/governance attribution;
// TraceParent is the inbound W3C traceparent to propagate to the remote agent
// (correlation). It carries NO credentials (those are the operator's out-of-band
// EmitConfig.Headers).
type DelegateSpec struct {
	AgentName   string
	AgentURL    string
	Skill       string
	Scope       string
	Text        string
	ContextID   string
	Tenant      string
	RequestedBy string
	TraceParent string
	// Objective is a short, non-sensitive PURPOSE label for the delegation (e.g.
	// "nightly-report-build") — the "with what objective" of the communication graph. It
	// is minimal data: a goal reference, NEVER the task text/prompt. It is carried into
	// the audit decision + observability edge, never into the A2A payload.
	Objective string
	// ParamsHash is the caller-computed, minimal-data digest of the complete
	// governed operation being delegated. When set, it replaces the connector's
	// legacy shape-only digest in PlanHash so a control plane can bind WorkItem,
	// owner epoch, lease fence, brief and criteria revisions without handing any
	// of those values (or their content) to the connector. Empty preserves the
	// existing standalone behavior.
	ParamsHash string
	// Chain is the inbound multi-agent delegation lineage this delegation extends (the
	// agents already in the task's lineage). When nil, the lineage is taken from the
	// context (withChain) — and an absent lineage is a fresh root (depth 0). The PEP
	// enforces the ChainPolicy (max depth + no cycles) against it.
	Chain *DelegationChain
}

// DelegationPlanHash returns the exact anti-TOCTOU plan a Delegator will require
// for spec. Control-plane adapters use it while persisting a plan and later pass
// the same spec to Test and Delegate. The raw task text is never part of the
// digest; callers that need stronger binding provide ParamsHash.
func DelegationPlanHash(spec DelegateSpec) string {
	paramsHash := strings.TrimSpace(spec.ParamsHash)
	if paramsHash == "" {
		paramsHash = hashParams(spec.Skill, spec.ContextID, len(spec.Text))
	}
	return PlanHash(spec.AgentName, spec.Skill, spec.Scope, paramsHash)
}

// DelegationTestResult is the non-actuating verification result for a planned
// delegation. A successful result proves the signed card declares the skill,
// the local allowlist permits the tuple and the delegation chain is legal. It
// does not request approval or transmit a Message.
type DelegationTestResult struct {
	PlanHash  string
	AgentName string
	Skill     string
	Scope     string
	Trust     string
}

// Test verifies a prospective delegation without requesting approval or sending
// anything to the remote endpoint. It is the safe preflight used by K5's
// RemoteWorkExecutor Test phase.
func (d *Delegator) Test(ctx context.Context, spec DelegateSpec) (DelegationTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d.client.timeout)
	defer cancel()
	ctx = withTraceParent(ctx, spec.TraceParent)
	inbound := d.inboundChain(ctx, spec)
	plan := DelegationPlanHash(spec)
	card, err := d.client.verifiedCard(ctx, SendSpec{AgentName: spec.AgentName, AgentURL: spec.AgentURL})
	if err != nil {
		return DelegationTestResult{}, err
	}
	if err := requireDeclaredSkill(card, spec.AgentName, spec.Skill); err != nil {
		return DelegationTestResult{}, err
	}
	if !d.allowlist.Allowed(spec.AgentName, spec.Skill, spec.Scope) {
		return DelegationTestResult{}, &DenyError{
			Reason: "agent/skill/scope not on the delegation allowlist", PlanHash: plan,
		}
	}
	if _, err := enforceChain(d.chainPolicy, inbound, spec.AgentName); err != nil {
		return DelegationTestResult{}, err
	}
	return DelegationTestResult{
		PlanHash: plan, AgentName: strings.TrimSpace(spec.AgentName),
		Skill: strings.TrimSpace(spec.Skill), Scope: strings.TrimSpace(spec.Scope),
		Trust: string(trustVerified),
	}, nil
}

// DenyError is the typed error a delegation returns when the PEP refuses it (an
// unlisted (agent, skill, scope), or a gate that did not return Allowed()). It lets a
// caller / test distinguish a POLICY denial from a transport failure, and it always
// carries the bound plan for the audit trail. A DenyError is never a transport error.
type DenyError struct {
	Reason   string
	PlanHash string
}

func (e *DenyError) Error() string {
	if e.PlanHash == "" {
		return "a2a: delegation denied: " + e.Reason
	}
	return "a2a: delegation denied (" + e.Reason + ") plan=" + e.PlanHash
}

// Delegate is the governed delegation: it verifies the remote AgentCard, enforces the
// deny-by-default allowlist, binds a PlanHash, requires an ApprovalGate authorization,
// and only then emits one SendMessage — returning the Task lifecycle outcome with the
// FSM applied (interrupts surfaced as actionable, never as success;
// TASK_STATE_UNSPECIFIED never treated as success). EVERY exit path (allow or deny) is
// recorded to the DecisionAuditor with minimal data. A DenyError marks a policy
// refusal; any other error is a verification/transport/RPC failure.
func (d *Delegator) Delegate(ctx context.Context, spec DelegateSpec) (TaskResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d.client.timeout)
	defer cancel()
	ctx = withTraceParent(ctx, spec.TraceParent)
	inbound := d.inboundChain(ctx, spec)

	plan := DelegationPlanHash(spec)

	// 1) Verify the signed AgentCard BEFORE anything else (deny-closed: only a card
	//    whose identity is established against the operator trust anchor — an unsigned,
	//    self-asserted, or unverifiable card is refused; emitting is an action).
	card, err := d.client.verifiedCard(ctx, SendSpec{AgentName: spec.AgentName, AgentURL: spec.AgentURL})
	if err != nil {
		d.record(ctx, spec, inbound, plan, false, "agent card not verified", "", TaskStateUnspecified)
		return TaskResult{}, err
	}

	// 2) Capability binding: the requested skill MUST be declared in the SIGNED
	//    card — you may not delegate a capability the agent does not cryptographically
	//    claim. Deny-closed, before the operator allowlist is consulted.
	if err := requireDeclaredSkill(card, spec.AgentName, spec.Skill); err != nil {
		d.record(ctx, spec, inbound, plan, false, "capability deny (skill not in signed card)", "", TaskStateUnspecified)
		return TaskResult{}, err
	}

	// 3) Allowlist: deny-by-default least-privilege over (agent, skill, scope).
	if !d.allowlist.Allowed(spec.AgentName, spec.Skill, spec.Scope) {
		d.record(ctx, spec, inbound, plan, false, "allowlist deny (agent/skill/scope not permitted)", "", TaskStateUnspecified)
		return TaskResult{}, &DenyError{Reason: "agent/skill/scope not on the delegation allowlist", PlanHash: plan}
	}

	// 4) Multi-agent chain governance: enforce max depth + no cycles against the
	//    inbound lineage, deny-closed, and bind the outbound lineage for propagation.
	outbound, err := enforceChain(d.chainPolicy, inbound, spec.AgentName)
	if err != nil {
		d.record(ctx, spec, inbound, plan, false, "chain deny ("+chainReason(err)+")", "", TaskStateUnspecified)
		return TaskResult{}, err
	}
	ctx = withChain(ctx, outbound)

	// 5) ApprovalGate (HITL), bound to the PlanHash. Fail closed on any error.
	dec, err := d.gate.Authorize(ctx, DelegationRequest{
		Tenant: spec.Tenant, AgentName: spec.AgentName, Skill: spec.Skill,
		Scope: spec.Scope, PlanHash: plan, RequestedBy: spec.RequestedBy,
	})
	if err != nil {
		d.record(ctx, spec, inbound, plan, false, "gate error (fail-closed)", "", TaskStateUnspecified)
		return TaskResult{}, fmt.Errorf("a2a: delegation gate error (deny): %w", err)
	}
	// Anti-TOCTOU: the approval MUST be bound to the exact plan we are about to emit.
	if !dec.Allowed() || (dec.PlanHash != "" && dec.PlanHash != plan) {
		d.record(ctx, spec, inbound, plan, false, "gate not approved ("+string(dec.Status)+")", dec.ApprovalRef, TaskStateUnspecified)
		return TaskResult{}, &DenyError{Reason: "delegation not approved by governance (" + string(dec.Status) + ")", PlanHash: plan}
	}

	// 6) Emit exactly one SendMessage to the verified endpoint (credentials out-of-band).
	res, err := d.client.emit(ctx, card, SendSpec{
		AgentName: spec.AgentName, AgentURL: spec.AgentURL,
		Text: spec.Text, Skill: spec.Skill, ContextID: spec.ContextID,
	})
	if err != nil {
		d.record(ctx, spec, inbound, plan, true, "delegation emitted but transport failed", dec.ApprovalRef, TaskStateUnspecified)
		return TaskResult{}, err
	}
	d.record(ctx, spec, inbound, plan, true, "delegated", dec.ApprovalRef, res.State)
	return res, nil
}

// inboundChain resolves the inbound multi-agent lineage for a delegation: the explicit
// spec.Chain when supplied, otherwise the chain propagated on the context (withChain),
// otherwise the zero chain (a fresh root, depth 0).
//
// PRINCIPAL SEEDING (RFC 8693 semantics): on a FRESH ROOT (depth 0) this plane IS the
// origin, so the chain's Principal — the original on-behalf-of subject every later hop
// will see in the `sub` position — is seeded from spec.RequestedBy. A lineage that
// arrived with hops but no principal is left empty: the original principal is unknown
// to us, and labeling the CURRENT requester as the original would misattribute the
// whole chain (honest unknown over fabricated provenance).
func (d *Delegator) inboundChain(ctx context.Context, spec DelegateSpec) DelegationChain {
	chain := chainFrom(ctx)
	if spec.Chain != nil {
		chain = *spec.Chain
	}
	if chain.Depth() == 0 && chain.Principal == "" {
		chain.Principal = strings.TrimSpace(spec.RequestedBy)
	}
	return chain
}

// chainReason extracts the short reason from a ChainError for the audit record (a non-
// ChainError yields a generic label — never the raw error, keeping the record minimal).
func chainReason(err error) string {
	var ce *ChainError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return "chain policy violation"
}

// record emits a minimal-data audit decision (best-effort; never blocks the result). It
// carries the delegation's objective + multi-agent lineage (depth/root) so the audit and
// observability layers can reconstruct WHO delegated WHAT to WHOM with WHAT objective —
// never a payload, token, or message text (docs/SECURITY-HARDENING.md).
func (d *Delegator) record(ctx context.Context, spec DelegateSpec, inbound DelegationChain, plan string, allowed bool, reason, approvalRef string, state TaskState) {
	d.auditor.Record(ctx, DelegationDecision{
		Tenant: spec.Tenant, AgentName: spec.AgentName, Skill: spec.Skill, Scope: spec.Scope,
		PlanHash: plan, Allowed: allowed, Reason: reason, ApprovalRef: approvalRef,
		State: state, RequestedBy: spec.RequestedBy, TraceParent: spec.TraceParent,
		Objective: spec.Objective, ChainDepth: inbound.Depth(), ChainRoot: inbound.Root,
		Principal: inbound.Principal,
		At:        d.now().UTC(),
	})
}

// --- v1.0.1 reconciliation / lifecycle surface ----------------------------------

// TaskRef references a Task on a remote agent for a query/lifecycle call. AgentName/
// AgentURL identify the agent (its card is verified before any call); TaskID is the
// Task id; HistoryLength optionally bounds the returned message history (GetTask).
type TaskRef struct {
	AgentName     string
	AgentURL      string
	TaskID        string
	HistoryLength int
}

// ListSpec is a ListTasks request: the agent to query plus pageToken pagination
// (PageToken from a prior page's NextPageToken; PageSize bounds one page — the
// server defaults to at most 50 and caps at 100, a2a.proto ListTasksRequest).
type ListSpec struct {
	AgentName string
	AgentURL  string
	PageToken string
	PageSize  int
}

// TaskPage is one page of ListTasks results plus the token to fetch the next page.
// NextPageToken is "" when the listing is exhausted (§3.1.4: the field MUST always
// be present and MUST be the empty string on the final page); TotalSize is the
// server-reported total before pagination.
type TaskPage struct {
	Tasks         []TaskResult
	NextPageToken string
	TotalSize     int
}

// GetTask reconciles a Task's current lifecycle state (the v1.0 GetTask method). The
// remote AgentCard is verified first; the result is mapped through the FSM (interrupts
// surfaced, TASK_STATE_UNSPECIFIED never success). It is a READ — it carries no
// approval gate (it observes an existing Task, it does not start a new delegation).
func (d *Delegator) GetTask(ctx context.Context, ref TaskRef) (TaskResult, error) {
	params := map[string]any{"id": ref.TaskID}
	if ref.HistoryLength > 0 {
		params["historyLength"] = ref.HistoryLength
	}
	raw, err := d.callTask(ctx, ref.AgentName, ref.AgentURL, methodGetTask, params)
	if err != nil {
		return TaskResult{}, err
	}
	return resultToTask(raw)
}

// Reconcile folds a freshly fetched state (GetTask) into a prior TaskResult using the
// FSM transition table: it returns the resolved state and whether the observed
// transition was legal. An ILLEGAL transition (e.g. a remote re-opening a terminal
// Task) keeps the prior state and reports legal=false — the connector never silently
// adopts a re-opened terminal Task (docs/SECURITY-HARDENING.md anti-evasion).
func (d *Delegator) Reconcile(ctx context.Context, prior TaskResult, ref TaskRef) (TaskResult, bool, error) {
	fresh, err := d.GetTask(ctx, ref)
	if err != nil {
		return prior, false, err
	}
	resolved, legal := reconcile(prior.State, fresh.State)
	out := fresh
	out.State = resolved
	out.Interrupt = taskStateInterrupt(resolved)
	out.Terminal = taskStateTerminal(resolved)
	return out, legal, nil
}

// CancelTask requests cancellation of a Task (the v1.0 CancelTask method). Canceling
// is de-escalation (it stops, never starts, work), so it requires a verified card +
// allowlist match for the agent but NOT a fresh ApprovalGate authorization.
func (d *Delegator) CancelTask(ctx context.Context, ref TaskRef) (TaskResult, error) {
	raw, err := d.callTask(ctx, ref.AgentName, ref.AgentURL, methodCancelTask, map[string]any{"id": ref.TaskID})
	if err != nil {
		return TaskResult{}, err
	}
	return resultToTask(raw)
}

// ListTasks lists a remote agent's Tasks with pageToken pagination (the v1.0
// ListTasks method, new in v1.0; wire fields pageToken/nextPageToken per a2a.proto —
// NOT the cursor/nextCursor names the non-normative whats-new page shows). The card
// is verified first; each item is mapped through the FSM.
func (d *Delegator) ListTasks(ctx context.Context, spec ListSpec) (TaskPage, error) {
	params := map[string]any{}
	if spec.PageToken != "" {
		params["pageToken"] = spec.PageToken
	}
	if spec.PageSize > 0 {
		params["pageSize"] = spec.PageSize
	}
	raw, err := d.callTask(ctx, spec.AgentName, spec.AgentURL, methodListTasks, params)
	if err != nil {
		return TaskPage{}, err
	}
	var lr struct {
		Tasks         []json.RawMessage `json:"tasks"`
		NextPageToken string            `json:"nextPageToken"`
		TotalSize     int               `json:"totalSize"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return TaskPage{}, fmt.Errorf("a2a: decode ListTasks result: %w", err)
	}
	page := TaskPage{NextPageToken: lr.NextPageToken, TotalSize: lr.TotalSize}
	for _, item := range lr.Tasks {
		tr, terr := resultToTask(item)
		if terr != nil {
			continue // skip an undecodable item rather than failing the whole page
		}
		page.Tasks = append(page.Tasks, tr)
	}
	return page, nil
}

// ExtendedCard is the authenticated extended AgentCard (the v1.0 GetExtendedAgentCard
// method): the richer card a verified caller may see, returned as the typed view plus
// the HONEST trust outcome of ITS OWN signatures. The extended card arrives over a
// channel whose endpoint the verified PUBLIC card named (transport-attributed), but
// it is a distinct document — its declarations are only cryptographically attributed
// when Trust is "verified"; a caller replacing its cached public card with the
// extended one (spec SHOULD) must decide on that label, never silently.
type ExtendedCard struct {
	Card  AgentCard
	Trust string // verifyCard outcome for the extended card itself (verified/unsigned/...)
}

// GetExtendedAgentCard fetches the authenticated extended Agent Card (the v1.0
// GetExtendedAgentCard method) from a verified agent. Deny-closed gates: the base
// card is verified first (identity established) AND its signed capabilities must
// declare extendedAgentCard (§3.3.4 — the server would refuse with
// UnsupportedOperationError anyway; the governed client does not probe beyond the
// declared surface). The request carries no params member (GetExtendedAgentCardRequest
// has only the optional tenant, echoed when the selected interface declares one).
func (d *Delegator) GetExtendedAgentCard(ctx context.Context, agentName, agentURL string) (ExtendedCard, error) {
	raw, err := d.callTaskGated(ctx, agentName, agentURL, methodGetExtendedAgentCard, nil, func(card AgentCard) error {
		return requireExtendedCard(card, agentName)
	})
	if err != nil {
		return ExtendedCard{}, err
	}
	rc, err := parseCard(raw)
	if err != nil {
		return ExtendedCard{}, fmt.Errorf("a2a: decode GetExtendedAgentCard result: %w", err)
	}
	anchor, err := parseJWKS(d.client.anchorRaw)
	if err != nil {
		return ExtendedCard{}, err
	}
	lvl, _ := verifyCard(ctx, rc, anchor, nil)
	return ExtendedCard{Card: rc.card, Trust: string(lvl)}, nil
}

// callTask is the shared verify-then-call path for the reconciliation/lifecycle
// methods: it verifies the remote AgentCard (deny-closed), resolves and TLS-checks the
// JSON-RPC endpoint (supportedInterfaces, tenant echoed), and issues one JSON-RPC
// call, returning the raw result. It never emits a message (no Task is started), so it
// carries no ApprovalGate — only the mandatory identity verification a verified
// channel requires.
func (d *Delegator) callTask(ctx context.Context, agentName, agentURL, method string, params map[string]any) (json.RawMessage, error) {
	return d.callTaskGated(ctx, agentName, agentURL, method, params, nil)
}

// callTaskGated is callTask with an optional capability gate evaluated against the
// VERIFIED card before anything is sent (deny-closed): the v1.0 capability-gated
// methods (push config, extended card) must not be invoked outside the agent's
// signed capability surface. A nil params map yields a request with NO params member
// (e.g. GetExtendedAgentCard); when the selected interface declares a tenant, it is
// echoed into the request (a2a.proto: requests to that interface must carry it).
func (d *Delegator) callTaskGated(ctx context.Context, agentName, agentURL, method string, params map[string]any, gate func(AgentCard) error) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, d.client.timeout)
	defer cancel()
	card, err := d.client.verifiedCard(ctx, SendSpec{AgentName: agentName, AgentURL: agentURL})
	if err != nil {
		return nil, err
	}
	if gate != nil {
		if err := gate(card); err != nil {
			return nil, err
		}
	}
	endpoint, tenant, err := resolveJSONRPC(card, agentURL)
	if err != nil {
		return nil, err
	}
	if err := d.client.requireSecure(endpoint); err != nil {
		return nil, err
	}
	if tenant != "" {
		if params == nil {
			params = map[string]any{}
		}
		params["tenant"] = tenant
	}
	env := rpcEnvelope{JSONRPC: "2.0", ID: newID(), Method: method}
	if params != nil {
		env.Params = params
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return d.client.doRPC(ctx, endpoint, method, raw)
}

// rpcEnvelope is the generic A2A JSON-RPC 2.0 request envelope for the v1.0.1 methods
// beyond SendMessage (whose typed envelope lives in emit_task.go).
type rpcEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// --- W3C Trace Context propagation ----------------------------------------------

type traceParentKey struct{}

// withTraceParent stashes an inbound W3C traceparent on the context so doRPC
// propagates it to the remote agent (cross-agent trace correlation). An empty value
// is a no-op.
func withTraceParent(ctx context.Context, traceparent string) context.Context {
	if strings.TrimSpace(traceparent) == "" {
		return ctx
	}
	return context.WithValue(ctx, traceParentKey{}, traceparent)
}

// traceParentFrom returns the propagated W3C traceparent, or "" if none.
func traceParentFrom(ctx context.Context) string {
	if v, ok := ctx.Value(traceParentKey{}).(string); ok {
		return v
	}
	return ""
}
