// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// Codex 0.153.4 server→client REQUEST codecs.
//
// The point of this file is that there is no such thing as "an approval". The
// installed schemas define SIX different answer contracts, and three of them
// cannot be expressed in each other's vocabulary:
//
//   - the v2 command/file surfaces take a `decision` string (plus, for commands,
//     two amendment OBJECTS) and their cancellation is `decision:"cancel"`;
//   - the permissions surface has NO decision field at all. Its answer is a
//     `permissions` profile plus a scope, and there is NO `cancel` — the safe
//     no-grant reply is `{"permissions":{},"scope":"turn"}`, and cancelling means
//     interrupting the TURN, not sending a cancel that does not exist;
//   - the legacy exec/patch surfaces use `ReviewDecision`, where cancellation is
//     `abort` and an ordinary denial is the structured `{"denied":{"rejection"}}`;
//   - MCP elicitation uses `action`, not `decision`, and has no generic field.
//
// A generic "cancel everything" reply would therefore be wrong on four of the six
// surfaces, which is exactly the correction the adjudication made.
//
// EVERY reply is bound to THIS owned run, profile, generation, conversation and
// ACTIVE TURN. A request naming another thread, or a turn that is no longer the
// active one, is answered with its own method's refusal and never with a grant:
// a stale reply must not be able to authorize anything in a new turn.
//
// And nothing here BROADENS. A grant is intersected with what the provider asked
// for, a session-scoped grant needs the authority to have said "session", and the
// deny-closed default authority grants nothing at all.

// JSON-RPC error code for a method this client does not implement.
const codexErrMethodNotSupported = -32601

// Codex server→client request methods.
const (
	codexReqCommandApproval     = "item/commandExecution/requestApproval"
	codexReqFileChangeApproval  = "item/fileChange/requestApproval"
	codexReqPermissionsApproval = "item/permissions/requestApproval"
	codexReqLegacyExecApproval  = "execCommandApproval"
	codexReqLegacyPatchApproval = "applyPatchApproval"
	codexReqMcpElicitation      = "mcpServer/elicitation/request"
	codexReqUserInput           = "item/tool/requestUserInput"
)

// Approval kinds carried to the governed authority.
const (
	codexKindCommand     = "command_execution"
	codexKindFileChange  = "file_change"
	codexKindPermissions = "permissions"
	codexKindLegacyExec  = "legacy_exec_command"
	codexKindLegacyPatch = "legacy_apply_patch"
	codexKindElicitation = "mcp_elicitation"
)

// ---------------------------------------------------------------------------
// AskForApproval: a string OR the typed granular object.
// ---------------------------------------------------------------------------

// CodexGranularApproval is the typed `granular` object. The first three booleans
// are required by the schema; the last two default to false and are omitted when
// false so the wire matches the default rather than restating it.
type CodexGranularApproval struct {
	MCPElicitations    bool `json:"mcp_elicitations"`
	Rules              bool `json:"rules"`
	SandboxApproval    bool `json:"sandbox_approval"`
	RequestPermissions bool `json:"request_permissions,omitempty"`
	SkillApproval      bool `json:"skill_approval,omitempty"`
}

// CodexApprovalPolicy is `AskForApproval`: one of three strings, OR the typed
// granular object. It is RETAINED as it was given — coercing the object into a
// string would silently discard four of its five switches.
type CodexApprovalPolicy struct {
	Mode     string
	Granular *CodexGranularApproval
}

func (p CodexApprovalPolicy) isZero() bool { return p.Mode == "" && p.Granular == nil }

func (p CodexApprovalPolicy) validate() error {
	if p.Granular != nil {
		if p.Mode != "" {
			return errors.New("sessions: a codex approval policy is either a mode or a granular object, not both")
		}
		return nil
	}
	switch p.Mode {
	case CodexApprovalUntrusted, CodexApprovalOnRequest, CodexApprovalNever:
		return nil
	default:
		// `on-failure` is deliberately not accepted: it is obsolete and absent from
		// this CLI's schema, so honoring it would be honoring an invention.
		return errors.New("sessions: codex approval policy must be untrusted, on-request, never, or a granular object")
	}
}

// MarshalJSON emits the exact `AskForApproval` union.
func (p CodexApprovalPolicy) MarshalJSON() ([]byte, error) {
	if p.Granular != nil {
		return json.Marshal(struct {
			Granular *CodexGranularApproval `json:"granular"`
		}{Granular: p.Granular})
	}
	mode := p.Mode
	if mode == "" {
		mode = CodexApprovalUntrusted
	}
	return json.Marshal(mode)
}

// UnmarshalJSON accepts both arms of the union.
func (p *CodexApprovalPolicy) UnmarshalJSON(data []byte) error {
	var mode string
	if err := json.Unmarshal(data, &mode); err == nil {
		*p = CodexApprovalPolicy{Mode: mode}
		return p.validate()
	}
	var obj struct {
		Granular *CodexGranularApproval `json:"granular"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if obj.Granular == nil {
		return errors.New("sessions: a codex approval policy object must carry granular")
	}
	*p = CodexApprovalPolicy{Granular: obj.Granular}
	return nil
}

// ---------------------------------------------------------------------------
// Permission profiles (request and grant share the schema's types).
// ---------------------------------------------------------------------------

type codexFSEntry struct {
	Access string          `json:"access"`
	Path   json.RawMessage `json:"path"`
}

type codexFileSystemPermissions struct {
	Entries          []codexFSEntry `json:"entries,omitempty"`
	Read             []string       `json:"read,omitempty"`
	Write            []string       `json:"write,omitempty"`
	GlobScanMaxDepth *int           `json:"globScanMaxDepth,omitempty"`
}

type codexNetworkPermissions struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type codexPermissionProfile struct {
	FileSystem *codexFileSystemPermissions `json:"fileSystem,omitempty"`
	Network    *codexNetworkPermissions    `json:"network,omitempty"`
}

// codexPermissionTokens is the canonical, references-only list of what the
// provider asked for. It is what the authority sees and what a grant is
// intersected with, so an authority can neither invent an entry nor widen one.
func codexPermissionTokens(p codexPermissionProfile) []string {
	var out []string
	if fs := p.FileSystem; fs != nil {
		for _, e := range fs.Entries {
			out = append(out, "fs:"+e.Access+":"+string(e.Path))
		}
		for _, r := range fs.Read {
			out = append(out, "fs:read:"+r)
		}
		for _, w := range fs.Write {
			out = append(out, "fs:write:"+w)
		}
	}
	if n := p.Network; n != nil && n.Enabled != nil && *n.Enabled {
		out = append(out, "network:enabled")
	}
	return out
}

// codexGrantSubset rebuilds a permission profile containing ONLY the requested
// items whose token the authority granted. An unknown token grants nothing.
func codexGrantSubset(req codexPermissionProfile, granted []string) codexPermissionProfile {
	allow := make(map[string]bool, len(granted))
	for _, g := range granted {
		allow[g] = true
	}
	var out codexPermissionProfile
	if fs := req.FileSystem; fs != nil {
		kept := &codexFileSystemPermissions{GlobScanMaxDepth: fs.GlobScanMaxDepth}
		any := false
		for _, e := range fs.Entries {
			if allow["fs:"+e.Access+":"+string(e.Path)] {
				kept.Entries = append(kept.Entries, e)
				any = true
			}
		}
		for _, r := range fs.Read {
			if allow["fs:read:"+r] {
				kept.Read = append(kept.Read, r)
				any = true
			}
		}
		for _, w := range fs.Write {
			if allow["fs:write:"+w] {
				kept.Write = append(kept.Write, w)
				any = true
			}
		}
		if any {
			out.FileSystem = kept
		}
	}
	if n := req.Network; n != nil && n.Enabled != nil && *n.Enabled && allow["network:enabled"] {
		enabled := true
		out.Network = &codexNetworkPermissions{Enabled: &enabled}
	}
	return out
}

// ---------------------------------------------------------------------------
// The bounded registry of supported server-request methods.
// ---------------------------------------------------------------------------

// codexRequestScope is the ownership envelope a request must satisfy.
type codexRequestScope struct {
	threadID string
	turnID   string
	hasTurn  bool
}

// codexApprovalCodec is one method's complete answer contract.
type codexApprovalCodec struct {
	kind string
	// scope extracts the conversation and (when the method carries one) the turn.
	scope func(json.RawMessage) codexRequestScope
	// tokens lists what the provider asked for (permissions only; nil elsewhere).
	tokens func(json.RawMessage) []string
	// grant encodes an AUTHORIZED answer, intersected with the request.
	grant func(params json.RawMessage, dec ProviderApprovalDecision) any
	// refuse encodes the ordinary refusal: the agent continues the turn.
	refuse func() any
	// cancel encodes cancellation, in THIS method's vocabulary.
	cancel func() any
	// timeout encodes the bounded-deadline answer.
	timeout func() any
}

func codexScopeThreadTurn(params json.RawMessage) codexRequestScope {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	_ = json.Unmarshal(params, &p)
	return codexRequestScope{threadID: p.ThreadID, turnID: p.TurnID, hasTurn: p.TurnID != ""}
}

// codexScopeLegacy reads the legacy surfaces' `conversationId`, which is the
// thread id under its older name. They carry no turn.
func codexScopeLegacy(params json.RawMessage) codexRequestScope {
	var p struct {
		ConversationID string `json:"conversationId"`
	}
	_ = json.Unmarshal(params, &p)
	return codexRequestScope{threadID: p.ConversationID}
}

func codexScopeElicitation(params json.RawMessage) codexRequestScope {
	var p struct {
		ThreadID string  `json:"threadId"`
		TurnID   *string `json:"turnId"`
	}
	_ = json.Unmarshal(params, &p)
	scope := codexRequestScope{threadID: p.ThreadID}
	if p.TurnID != nil && *p.TurnID != "" {
		scope.turnID, scope.hasTurn = *p.TurnID, true
	}
	return scope
}

type codexDecisionReply struct {
	Decision any `json:"decision"`
}

type codexDeniedDecision struct {
	Denied codexDeniedBody `json:"denied"`
}

type codexDeniedBody struct {
	Rejection string `json:"rejection"`
}

type codexPermissionsReply struct {
	Permissions codexPermissionProfile `json:"permissions"`
	Scope       string                 `json:"scope"`
}

type codexElicitationReply struct {
	Action string `json:"action"`
}

// codexNoGrant is the permissions surface's SAFE reply. It is not a cancel: the
// schema has no cancel here, and a turn is stopped by interrupting it.
func codexNoGrant() any {
	return codexPermissionsReply{Permissions: codexPermissionProfile{}, Scope: "turn"}
}

const codexRefusalReason = "denied by the Olivares session policy for this run"

// codexApprovalCodecs is the bounded registry. A method absent from it receives a
// protocol ERROR: never a hang, and never an approval.
var codexApprovalCodecs = map[string]codexApprovalCodec{
	codexReqCommandApproval: {
		kind:  codexKindCommand,
		scope: codexScopeThreadTurn,
		grant: func(_ json.RawMessage, dec ProviderApprovalDecision) any {
			// `acceptForSession` is a PERSISTENT grant and needs the authority to have
			// asked for session scope; the execpolicy/network amendment objects are
			// separate authorizations this driver never synthesizes.
			if dec.SessionScope {
				return codexDecisionReply{Decision: "acceptForSession"}
			}
			return codexDecisionReply{Decision: "accept"}
		},
		refuse:  func() any { return codexDecisionReply{Decision: "decline"} },
		cancel:  func() any { return codexDecisionReply{Decision: "cancel"} },
		timeout: func() any { return codexDecisionReply{Decision: "decline"} },
	},
	codexReqFileChangeApproval: {
		kind:  codexKindFileChange,
		scope: codexScopeThreadTurn,
		grant: func(_ json.RawMessage, dec ProviderApprovalDecision) any {
			if dec.SessionScope {
				return codexDecisionReply{Decision: "acceptForSession"}
			}
			return codexDecisionReply{Decision: "accept"}
		},
		refuse:  func() any { return codexDecisionReply{Decision: "decline"} },
		cancel:  func() any { return codexDecisionReply{Decision: "cancel"} },
		timeout: func() any { return codexDecisionReply{Decision: "decline"} },
	},
	codexReqPermissionsApproval: {
		kind:  codexKindPermissions,
		scope: codexScopeThreadTurn,
		tokens: func(params json.RawMessage) []string {
			var p struct {
				Permissions codexPermissionProfile `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			return codexPermissionTokens(p.Permissions)
		},
		grant: func(params json.RawMessage, dec ProviderApprovalDecision) any {
			var p struct {
				Permissions codexPermissionProfile `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			scope := "turn"
			if dec.SessionScope {
				scope = "session"
			}
			return codexPermissionsReply{
				Permissions: codexGrantSubset(p.Permissions, dec.Granted), Scope: scope,
			}
		},
		refuse:  codexNoGrant,
		cancel:  codexNoGrant,
		timeout: codexNoGrant,
	},
	codexReqLegacyExecApproval: {
		kind:  codexKindLegacyExec,
		scope: codexScopeLegacy,
		grant: func(_ json.RawMessage, dec ProviderApprovalDecision) any {
			if dec.SessionScope {
				return codexDecisionReply{Decision: "approved_for_session"}
			}
			return codexDecisionReply{Decision: "approved"}
		},
		refuse: func() any {
			return codexDecisionReply{Decision: codexDeniedDecision{Denied: codexDeniedBody{Rejection: codexRefusalReason}}}
		},
		cancel:  func() any { return codexDecisionReply{Decision: "abort"} },
		timeout: func() any { return codexDecisionReply{Decision: "timed_out"} },
	},
	codexReqLegacyPatchApproval: {
		kind:  codexKindLegacyPatch,
		scope: codexScopeLegacy,
		grant: func(_ json.RawMessage, dec ProviderApprovalDecision) any {
			if dec.SessionScope {
				return codexDecisionReply{Decision: "approved_for_session"}
			}
			return codexDecisionReply{Decision: "approved"}
		},
		refuse: func() any {
			return codexDecisionReply{Decision: codexDeniedDecision{Denied: codexDeniedBody{Rejection: codexRefusalReason}}}
		},
		cancel:  func() any { return codexDecisionReply{Decision: "abort"} },
		timeout: func() any { return codexDecisionReply{Decision: "timed_out"} },
	},
	codexReqMcpElicitation: {
		kind:    codexKindElicitation,
		scope:   codexScopeElicitation,
		grant:   func(json.RawMessage, ProviderApprovalDecision) any { return codexElicitationReply{Action: "accept"} },
		refuse:  func() any { return codexElicitationReply{Action: "decline"} },
		cancel:  func() any { return codexElicitationReply{Action: "cancel"} },
		timeout: func() any { return codexElicitationReply{Action: "decline"} },
	},
}

// codexUnsupportedRequestReason names why a request gets a protocol error rather
// than an answer. `item/tool/requestUserInput` is listed BY NAME because its
// schema is marked EXPERIMENTAL: a stable-only driver that answered it would be
// advertising a surface it did not negotiate, and synthesizing empty answers to
// look supported is worse than saying no.
func codexUnsupportedRequestReason(method string) string {
	if method == codexReqUserInput {
		return "item/tool/requestUserInput is experimental and is not part of this client's stable surface"
	}
	return "this client does not implement " + method
}

// ---------------------------------------------------------------------------
// Dispatch.
// ---------------------------------------------------------------------------

// codexServerRequest is one in-flight server→client request. once guarantees that
// a request is answered EXACTLY ONCE, whichever of the three racing paths gets
// there first: the authority, the deadline, or a cancellation from interrupt/stop.
type codexServerRequest struct {
	id     json.RawMessage
	method string
	codec  codexApprovalCodec
	once   sync.Once
}

func (s *codexSession) onServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	codec, ok := codexApprovalCodecs[method]
	if !ok {
		// Answered, not ignored: an unanswered request stalls the provider's turn
		// forever, and answering it with an approval would be worse than stalling.
		go func() {
			_ = s.conn.respondError(context.Background(), id, codexErrMethodNotSupported, codexUnsupportedRequestReason(method))
		}()
		return
	}
	req := &codexServerRequest{id: id, method: method, codec: codec}
	key := string(id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending[key] = req
	// The turn is read HERE, on the pump, at the instant of arrival — which is what
	// makes "the active turn when this request arrived" a fact rather than a guess
	// made later by a goroutine that may have been descheduled.
	thread, turn := s.threadID, s.turnID
	s.mu.Unlock()
	go s.resolveServerRequest(req, key, params, thread, turn)
}

// codexBoundTurn decides which turn a request belongs to, for EVERY supported
// surface, and refuses when it cannot name one.
//
// ⛔ THE ABSENCE OF A WIRE FIELD IS NOT THE ABSENCE OF THE REQUIREMENT. The
// installed 0.153.4 schema gives `execCommandApproval` and `applyPatchApproval` no
// `turnId` at all, and makes MCP elicitation's optional. The earlier code read
// that as "no turn to check" and skipped the scope entirely — so a legacy approval
// could be authorised during turn-1 and granted during turn-2, which the review
// reproduced twice. What the ratified contract requires is an ACTIVE TURN, not a
// field: a request that arrives with no active turn has nothing to belong to and
// is refused, and one that arrives during a turn is bound to it whether or not the
// wire says so.
func codexBoundTurn(scope codexRequestScope, activeTurn string) (string, bool) {
	if activeTurn == "" {
		// Nothing is in flight. A grant here would authorise a turn that does not
		// exist, so the only honest answer is the method's cancellation.
		return "", false
	}
	if scope.hasTurn {
		// An explicit turn must MATCH; it never overrides what is actually active.
		if scope.turnID != activeTurn {
			return "", false
		}
		return scope.turnID, true
	}
	return activeTurn, true
}

// resolveServerRequest binds the request to this owned conversation and turn,
// consults the governed authority under a bounded deadline, and answers with the
// METHOD's own codec.
//
// The order of the checks is the contract: conversation, then a NON-EMPTY active
// turn, then the authority, then — after the authority has spent whatever time it
// needs — the turn AND the durable launch authority again. Everything before the
// grant is deny-closed, and every refusal is spoken in the method's own words.
func (s *codexSession) resolveServerRequest(req *codexServerRequest, key string, params json.RawMessage, thread, turn string) {
	defer s.forgetServerRequest(key)
	scope := req.codec.scope(params)
	if thread == "" || scope.threadID != thread {
		// A request naming another conversation is not ours to answer with a grant.
		s.warn("sessions: a provider approval named a foreign conversation and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, req.codec.cancel())
		return
	}
	boundTurn, ok := codexBoundTurn(scope, turn)
	if !ok {
		// Either the request named a turn that is not the active one, or none is
		// active at all. Both are requests this launch cannot authorise.
		s.warn("sessions: a provider approval could not be bound to the active turn and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, req.codec.cancel())
		return
	}
	deadline := s.cfg.ApprovalDeadline
	if deadline <= 0 {
		deadline = defaultDriverApprovalDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var tokens []string
	if req.codec.tokens != nil {
		tokens = req.codec.tokens(params)
	}
	dec := ProviderApprovalDecision{}
	if s.cfg.Approve != nil {
		got, err := s.cfg.Approve(ctx, ProviderApprovalRequest{
			Driver: providerDriverCodex, RunRef: s.cfg.RunRef, ProfileRef: s.cfg.ProfileRef,
			// The turn the authority is asked about is the one this request was BOUND
			// to, never the empty string the wire happened to omit. An authority that
			// cannot see the turn cannot scope its decision to it.
			ConversationID: thread, TurnID: boundTurn,
			Method: req.method, Kind: req.codec.kind, Requested: tokens,
		})
		if err != nil {
			// An authority that could not decide never means "allow".
			s.warn("sessions: the provider approval authority failed; refusing deny-closed",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.answer(req, req.codec.refuse())
			return
		}
		dec = got
	}
	if ctx.Err() != nil {
		// The bounded deadline expired. Each surface has its OWN way of saying so:
		// the legacy contract has `timed_out`, the v2 ones only `decline`, and the
		// permissions surface says it with the safe empty grant.
		s.answer(req, req.codec.timeout())
		return
	}
	if !dec.Allow {
		s.answer(req, req.codec.refuse())
		return
	}
	// The turn is re-checked HERE, not only on arrival, and for EVERY surface. An
	// authority takes time — that is the point of giving it a deadline — and the
	// turn it was asked about can end while it thinks. A grant written after that
	// would be an authorization decided for a turn that no longer exists.
	if boundTurn != s.ActiveTurn() {
		s.warn("sessions: a provider approval was authorized after its turn ended; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, req.codec.cancel())
		return
	}
	// And the DURABLE authority, last of all. A turn that is still active proves
	// nothing about who owns the session: the review moved the Claim from fence 1
	// to 2 while the gate decided and the child received "accept". This asks the
	// store — with the claim row in the transaction's write set — and a launch that
	// has been superseded cancels instead of granting.
	if s.cfg.AuthorityCheck != nil {
		if err := s.cfg.AuthorityCheck(ctx); err != nil {
			s.warn("sessions: a provider approval lost its launch authority before the grant; refusing it",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.answer(req, req.codec.cancel())
			return
		}
	}
	s.answer(req, req.codec.grant(params, dec))
}

// answer writes the reply exactly once.
func (s *codexSession) answer(req *codexServerRequest, reply any) {
	req.once.Do(func() {
		if err := s.conn.respond(context.Background(), req.id, reply); err != nil &&
			!errors.Is(err, errRPCClosed) {
			s.warn("sessions: could not answer a provider approval request",
				"run_ref", s.cfg.RunRef, "method", req.method)
		}
	})
}

func (s *codexSession) forgetServerRequest(key string) {
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}

// cancelPendingApprovals answers every in-flight request with ITS OWN
// cancellation before the turn is interrupted or the process is torn down. A
// permissions request has no cancel to send, so it receives the safe no-grant and
// the turn interruption is what actually ends it.
func (s *codexSession) cancelPendingApprovals(context.Context) {
	s.mu.Lock()
	pending := make([]*codexServerRequest, 0, len(s.pending))
	for _, req := range s.pending {
		pending = append(pending, req)
	}
	s.mu.Unlock()
	for _, req := range pending {
		s.answer(req, req.codec.cancel())
	}
}

// codexSupportedApprovalMethods lists the bounded registry, for the report and
// for a test that pins it. It is sorted for determinism.
func codexSupportedApprovalMethods() []string {
	out := make([]string, 0, len(codexApprovalCodecs))
	for method := range codexApprovalCodecs {
		out = append(out, method)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && strings.Compare(out[j-1], out[j]) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
