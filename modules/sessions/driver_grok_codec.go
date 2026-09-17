// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
)

// ACP `session/request_permission` — the ONE server→client request this driver
// implements, and its complete answer contract.
//
// The shape is the protocol's, not a guess: the pinned 1.0.13 binary declares
// `RequestPermissionRequest`, `PermissionOption` (`optionId`, `name`, `kind`),
// `RequestPermissionResponse` with a single `outcome`, the internally-tagged
// `RequestPermissionOutcome` with its `selected` and `cancelled` arms, and the
// four option kinds `allow_once`, `allow_always`, `reject_once`,
// `reject_always`. What has NOT been observed is which options an AUTHENTICATED
// Grok actually offers for a given tool call — so nothing here assumes a
// catalogue. Every decision is taken over the options the agent SENT.
//
// ⛔ THE DIFFERENCE FROM THE CODEX CODECS, AND IT IS THE WHOLE FILE. Codex has
// six method-specific answer vocabularies, and the driver knows all six because
// they are fixed by its schema. ACP has ONE vocabulary and the agent supplies its
// terms per request. That inverts the risk: there is nothing to mistranslate, and
// everything to over-grant. So the rule is intersection, in both directions —
//
//   - an option this client selects must be one the agent OFFERED, by exact id;
//   - it must also be one the AUTHORITY named, by exact id;
//   - a PERSISTENT option (`allow_always`) additionally needs the authority to
//     have said session scope. A one-shot grant never becomes a standing one
//     because the agent happened to offer only the standing form.
//
// And nothing is inferred from the payload's prose. A title, a command line or a
// tool name is provider TEXT: it is never parsed into an authorization, and it
// never reaches a row or an API answer from here.

// The ACP permission option kinds.
const (
	grokPermissionAllowOnce    = "allow_once"
	grokPermissionAllowAlways  = "allow_always"
	grokPermissionRejectOnce   = "reject_once"
	grokPermissionRejectAlways = "reject_always"
)

// The two `RequestPermissionOutcome` arms.
const (
	grokOutcomeSelected  = "selected"
	grokOutcomeCancelled = "cancelled"
)

// grokReqRequestPermission is the only agent→client request with an answer here.
const grokReqRequestPermission = "session/request_permission"

// grokKindToolCallPermission is the codec family carried to the governed
// authority. ACP has ONE permission surface, so there is one kind — inventing a
// finer taxonomy from the tool call's own text would be this driver deciding what
// another product's tools mean.
const grokKindToolCallPermission = "tool_call_permission"

// --- wire types ---------------------------------------------------------------

type grokPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// grokToolCall is the subset of the tool call this driver reads. The id and kind
// are references; the title and the raw input are provider TEXT and are read by
// nobody here.
type grokToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Kind       string `json:"kind"`
}

type grokRequestPermissionParams struct {
	SessionID string                 `json:"sessionId"`
	ToolCall  grokToolCall           `json:"toolCall"`
	Options   []grokPermissionOption `json:"options"`
}

type grokPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type grokRequestPermissionResponse struct {
	Outcome grokPermissionOutcome `json:"outcome"`
}

// grokCancelledOutcome is the protocol's own no-grant answer. It is a VALID
// response, not a hang and not an error: the agent learns the client will not
// decide, and grants nothing.
func grokCancelledOutcome() any {
	return grokRequestPermissionResponse{Outcome: grokPermissionOutcome{Outcome: grokOutcomeCancelled}}
}

func grokSelectedOutcome(optionID string) any {
	return grokRequestPermissionResponse{
		Outcome: grokPermissionOutcome{Outcome: grokOutcomeSelected, OptionID: optionID},
	}
}

// --- validation ---------------------------------------------------------------

// grokValidPermissionRequest reports whether the request can be answered by
// SELECTION at all, and names why not.
//
// A duplicate option id fails the WHOLE request rather than picking the first
// match: two options sharing an id make "the option the authority named"
// ambiguous, and an ambiguous grant is a grant nobody authorized. An option whose
// kind this client does not know is not a failure — it is simply not selectable,
// which is the deny-closed reading.
func grokValidPermissionRequest(p grokRequestPermissionParams) (string, bool) {
	if p.SessionID == "" {
		return "the request named no conversation", false
	}
	if len(p.Options) == 0 {
		return "the request offered no options to select", false
	}
	seen := make(map[string]bool, len(p.Options))
	known := false
	for _, opt := range p.Options {
		if opt.OptionID == "" {
			return "the request offered an option with no id", false
		}
		if seen[opt.OptionID] {
			return "the request offered two options with the same id", false
		}
		seen[opt.OptionID] = true
		known = known || grokKnownPermissionKind(opt.Kind)
	}
	if !known {
		return "the request offered no option of a kind this client understands", false
	}
	return "", true
}

// grokKnownPermissionKind reports an option kind this client understands. An
// unknown kind is not an error in itself — the protocol may grow kinds — it is
// simply not selectable, which is the deny-closed reading. A request in which
// NONE of the options is understood cannot be answered by selection at all, and
// says so rather than picking one by position.
func grokKnownPermissionKind(kind string) bool {
	switch kind {
	case grokPermissionAllowOnce, grokPermissionAllowAlways,
		grokPermissionRejectOnce, grokPermissionRejectAlways:
		return true
	default:
		return false
	}
}

// grokOfferedOptionIDs is what the AUTHORITY is shown: exactly the ids the agent
// offered, in wire order. A grant is intersected with this, so an authority can
// neither invent an option nor widen the one it was shown.
func grokOfferedOptionIDs(options []grokPermissionOption) []string {
	out := make([]string, 0, len(options))
	for _, opt := range options {
		out = append(out, opt.OptionID)
	}
	return out
}

// grokSelectGrant picks the option to SELECT for an authorized decision, or
// reports that none may be.
//
// The candidate must satisfy three things at once, and dropping any of them is a
// widening: the agent offered it, the authority named it, and its kind is one the
// decision actually authorizes. `allow_always` is a PERSISTENT grant that outlives
// this tool call, so it needs the authority to have asked for session scope; a
// turn-scoped decision cannot be upgraded by the agent's choice of options.
func grokSelectGrant(options []grokPermissionOption, dec ProviderApprovalDecision) (string, bool) {
	granted := make(map[string]bool, len(dec.Granted))
	for _, id := range dec.Granted {
		granted[id] = true
	}
	for _, opt := range options {
		if !granted[opt.OptionID] {
			continue
		}
		switch opt.Kind {
		case grokPermissionAllowOnce:
			return opt.OptionID, true
		case grokPermissionAllowAlways:
			if dec.SessionScope {
				return opt.OptionID, true
			}
		}
	}
	return "", false
}

// grokSelectRefusal picks the option that REFUSES this tool call and nothing
// more.
//
// Only `reject_once` qualifies. `reject_always` is a persistent decision — a
// standing "never" recorded against this project — and it is not what a
// turn-scoped refusal means, however convenient it would be when it is the only
// rejection on offer. When no one-shot rejection is offered, the answer is the
// protocol's cancellation, which grants nothing and records nothing.
func grokSelectRefusal(options []grokPermissionOption) (string, bool) {
	for _, opt := range options {
		if opt.Kind == grokPermissionRejectOnce {
			return opt.OptionID, true
		}
	}
	return "", false
}

// --- dispatch -----------------------------------------------------------------

// grokServerRequest is one in-flight agent→client request. once guarantees that
// it is answered EXACTLY ONCE, whichever of the three racing paths gets there
// first: the authority, the deadline, or a cancellation from interrupt/stop.
type grokServerRequest struct {
	id     json.RawMessage
	method string
	once   sync.Once
	// answered records that the ONE reply has been CLAIMED. It is stored INSIDE the
	// once and BEFORE the write, so it is true from the instant a path commits to
	// answering rather than from the instant the bytes leave — and a resolver that
	// reads it true is reading a request that already has its only answer.
	//
	// It is request-local, not session or turn state, because it has to survive both:
	// the turn it was answered for can end, and the session can go on, while this
	// request stays answered for as long as its resolver may still be running.
	answered atomic.Bool
}

func (s *grokSession) onServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method != grokReqRequestPermission {
		// Answered, not ignored: an unanswered request stalls the agent's turn for
		// ever, and answering it with a grant would be worse than stalling. This is
		// also where the capabilities this client did NOT advertise come home — an
		// `fs/read_text_file` or a `terminal/create` gets a protocol error rather than
		// a synthesized answer that would make the client look capable.
		go func() {
			_ = s.conn.respondError(context.Background(), id, grokErrMethodNotSupported,
				"this client does not implement "+method)
		}()
		return
	}
	req := &grokServerRequest{id: id, method: method}
	key := string(id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending[key] = req
	// The conversation and the turn are read HERE, on the pump, at the instant of
	// arrival — which is what makes "the turn that was active when this request
	// arrived" a fact rather than a guess made later by a goroutine that may have
	// been descheduled.
	session, turn := s.sessionID, s.promptID
	// And WHETHER THAT TURN WAS ALREADY CANCELLING, read in the SAME critical
	// section that registers the request. Registration and cancellation take this
	// one mutex, so the two possible orders both end covered and neither leaves a
	// gap: if the request registers first, `cancelPendingApprovals` finds it in
	// `pending` and answers it; if the mark lands first, the request leaves arrival
	// carrying it. There is no interleaving in which a request arrives during a
	// cancellation and neither side sees it.
	//
	// It is captured rather than re-read later because the fact has to OUTLIVE the
	// state it was read from. `clearTurn` releases `cancelledTurn` when the turn's
	// own correlated result comes back, so a goroutine that asked afterwards would
	// be told "not cancelling" about a request that arrived while it was — the
	// window widening with exactly the descheduling this capture exists to survive.
	cancellingAtArrival := turn != "" && s.cancelledTurn == turn
	s.mu.Unlock()
	go s.resolveServerRequest(req, key, params, session, turn, cancellingAtArrival)
}

// resolveServerRequest binds the request to this owned conversation and turn,
// consults the governed authority under a bounded deadline, and answers with a
// selection the agent itself offered.
//
// The order of the checks is the contract: decode, then the conversation, then a
// NON-EMPTY active turn, then a turn that was ALREADY CANCELLING WHEN THIS
// REQUEST ARRIVED, then a request THE CANCELLATION HAS ALREADY ANSWERED — all of
// them before the deadline is opened or the authority is consulted — then the
// authority, then, after the authority has spent whatever time it needs,
// the turn AND the durable launch authority again. Everything before the
// selection is deny-closed.
func (s *grokSession) resolveServerRequest(req *grokServerRequest, key string, raw json.RawMessage, session, turn string, cancellingAtArrival bool) {
	defer s.forgetServerRequest(key)

	var params grokRequestPermissionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.warn("sessions: a provider permission request could not be read and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, grokCancelledOutcome())
		return
	}
	if why, ok := grokValidPermissionRequest(params); !ok {
		s.warn("sessions: a provider permission request was malformed and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method, "why", why)
		s.answer(req, grokCancelledOutcome())
		return
	}
	if session == "" || params.SessionID != session {
		// A request naming another conversation is not ours to answer with a grant.
		s.warn("sessions: a provider permission request named a foreign conversation and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, grokCancelledOutcome())
		return
	}
	if turn == "" {
		// ⛔ THE ABSENCE OF A WIRE FIELD IS NOT THE ABSENCE OF THE REQUIREMENT. ACP's
		// permission request carries no turn id at all, and the ratified contract
		// requires an ACTIVE TURN, not a field: a request that arrives with no turn in
		// flight has nothing to belong to, and a grant would authorize a turn that does
		// not exist.
		s.warn("sessions: a provider permission request arrived with no active turn and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, grokCancelledOutcome())
		return
	}
	// ⛔ AND A TURN THE OPERATOR HAS ALREADY STOPPED IS NOT ONE AN AUTHORITY IS
	// ASKED ABOUT. This is the ADMISSION check, and its whole point is WHERE it
	// sits: before the deadline is opened and before `cfg.Approve` is reached.
	// Refusing after consulting would still write a refusal on the wire, and would
	// still be wrong — the authority is a governed side effect, and it can put an
	// approval in front of an operator, wake a policy engine or record a decision
	// for work that was stopped before the request ever turned up. Not granting is
	// not the whole requirement; not ASKING is.
	//
	// The fact was taken at arrival under the same mutex that registers the request,
	// so it names the cancellation of THE TURN THIS REQUEST BELONGS TO and cannot be
	// erased by that turn's own correlated result landing in the meantime.
	//
	// The refusal is the agent's own one-shot rejection when it offered one: the
	// agent gets a decided answer instead of a stall, and it is never
	// `reject_always`, because a cancelled turn is not where a standing decision
	// gets recorded.
	if cancellingAtArrival {
		s.warn("sessions: a provider permission arrived on a turn that was already being cancelled; refusing it without consulting the authority",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	// ⛔ AND NEITHER IS A REQUEST THE CANCELLATION HAS ALREADY ANSWERED. This one
	// arrived on a LIVE turn, so it carries no arrival fact and admitting it was
	// correct — but its resolver may only get here after `cancelPendingApprovals`
	// answered it from `pending`, and the once-only reply that stops it from
	// granting does not stop it from ASKING. Consulting now spends the governed side
	// effect the check above exists to withhold — an approval in front of an
	// operator, a policy engine woken, a decision recorded — on a request that is
	// already answered and a turn that is already stopped.
	//
	// THE ADMISSION LINEARIZATION POINT IS THIS LOAD, and that is the whole
	// guarantee: `answer` stores the claim inside the once, before the reply is
	// written, so the two orders are total and both are defined. A claim that
	// precedes this load refuses without asking. A load that precedes the claim
	// ADMITS the request, and an admitted request keeps exactly the behaviour it
	// had: it may finish with the authority, and its late decision still cannot
	// grant, because the turn is re-checked below and the reply is written once.
	// What no flag can promise is WHEN the callback itself starts — a request
	// admitted before the cancellation whose `cfg.Approve` runs long afterwards is
	// admitted, not a leak, and claiming otherwise would be claiming scheduler
	// control this code does not have.
	//
	// Nothing is written here: the only reply this request will ever have exists
	// already, and a second `refuse` would be a no-op dressed as an answer.
	if req.answered.Load() {
		s.warn("sessions: a provider permission was already answered by a cancellation; not consulting the authority for it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		return
	}
	deadline := s.cfg.ApprovalDeadline
	if deadline <= 0 {
		deadline = defaultDriverApprovalDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	offered := grokOfferedOptionIDs(params.Options)
	dec := ProviderApprovalDecision{}
	if s.cfg.Approve != nil {
		got, err := s.cfg.Approve(ctx, ProviderApprovalRequest{
			Driver: providerDriverGrok, RunRef: s.cfg.RunRef, ProfileRef: s.cfg.ProfileRef,
			ConversationID: session, TurnID: turn,
			Method: req.method, Kind: grokKindToolCallPermission,
			// Exactly what the agent offered: an authority cannot widen a request it
			// was shown, because a selection is checked back against this list.
			Requested: offered,
		})
		if err != nil {
			// An authority that could not decide never means "allow".
			s.warn("sessions: the provider approval authority failed; refusing deny-closed",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.refuse(req, params.Options)
			return
		}
		dec = got
	}
	if ctx.Err() != nil {
		// The bounded deadline expired while the authority thought. A refusal is the
		// honest outcome; the agent may continue its turn without the tool.
		s.refuse(req, params.Options)
		return
	}
	if !dec.Allow {
		s.refuse(req, params.Options)
		return
	}
	// ⛔ AND THE TURN MUST NOT HAVE BEEN CANCELLED WHILE THE AUTHORITY WAS THINKING.
	// `session/cancel` is a NOTIFICATION: it ends nothing, so between the interrupt
	// and the prompt's own correlated result the turn is still active and still
	// occupied — which is correct, and is exactly why the active-turn check below
	// cannot see this state.
	//
	// This is the OTHER side of the clock from the admission check above, and the
	// two are not interchangeable. That one refuses a request that ARRIVED after the
	// cancel, and refuses it without asking anyone. This one belongs to a request
	// that arrived while the turn was live and legitimately reached the authority:
	// the authority is allowed to take its time, so a decision can be handed back
	// after a cancel it knew nothing about, and a grant that lands late is still a
	// grant for a turn the operator stopped. Nothing here can be dropped because the
	// admission check exists, and nothing there is covered by this one.
	//
	// The refusal is the agent's OWN one-shot rejection when it offered one, and the
	// protocol's cancellation when it did not. It never becomes `reject_always`: a
	// cancelled turn is not the place a standing decision gets recorded.
	if s.turnCancelled(turn) {
		s.warn("sessions: a provider permission's turn was cancelled while the authority decided; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	// The turn is re-checked HERE, not only on arrival. An authority takes time —
	// that is the point of giving it a deadline — and the turn it was asked about can
	// end while it thinks. A selection written after that would authorize a tool call
	// for a turn that no longer exists.
	if turn != s.ActiveTurn() {
		s.warn("sessions: a provider permission was authorized after its turn ended; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, grokCancelledOutcome())
		return
	}
	// And the DURABLE authority, last of all. A turn that is still active proves
	// nothing about who owns the session: this asks the store, with the claim row in
	// the transaction's write set, and a launch that has been superseded cancels
	// instead of granting.
	if s.cfg.AuthorityCheck != nil {
		if err := s.cfg.AuthorityCheck(ctx); err != nil {
			s.warn("sessions: a provider permission lost its launch authority before the grant; refusing it",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.answer(req, grokCancelledOutcome())
			return
		}
	}
	optionID, ok := grokSelectGrant(params.Options, dec)
	if !ok {
		// The authority allowed, but nothing it named is an option this client may
		// select — an id the agent did not offer, or a persistent option under a
		// turn-scoped decision. Refusing is the only answer that does not widen.
		s.warn("sessions: an authorized provider permission named no selectable offered option; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	s.answer(req, grokSelectedOutcome(optionID))
}

// refuse answers with the agent's own one-shot rejection when it offered one, and
// with the protocol's cancellation when it did not.
func (s *grokSession) refuse(req *grokServerRequest, options []grokPermissionOption) {
	if optionID, ok := grokSelectRefusal(options); ok {
		s.answer(req, grokSelectedOutcome(optionID))
		return
	}
	s.answer(req, grokCancelledOutcome())
}

// answer writes the reply exactly once.
func (s *grokSession) answer(req *grokServerRequest, reply any) {
	req.once.Do(func() {
		// The claim is recorded BEFORE the write, and that order is the whole point:
		// this store is the ordering side of the admission check in
		// `resolveServerRequest`. It is NOT a claim that nothing is held while the
		// write runs — `once.Do` holds its own internal mutex across this entire
		// function, `respond` included, which is the pre-existing serialization of the
		// ONE reply and is untouched here. What the admission check needs is narrower
		// and is true: that read takes neither `s.mu` nor this once, so a resolver can
		// still reject while this write is blocked on the child's stdin.
		req.answered.Store(true)
		if err := s.conn.respond(context.Background(), req.id, reply); err != nil &&
			!errors.Is(err, errRPCClosed) {
			s.warn("sessions: could not answer a provider permission request",
				"run_ref", s.cfg.RunRef, "method", req.method)
		}
	})
}

func (s *grokSession) forgetServerRequest(key string) {
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}

// cancelPendingApprovals answers every in-flight request with the protocol's own
// cancellation before the turn is interrupted or the process is torn down, so the
// agent is never left waiting on a request whose turn no longer exists.
func (s *grokSession) cancelPendingApprovals(context.Context) {
	s.mu.Lock()
	pending := make([]*grokServerRequest, 0, len(s.pending))
	for _, req := range s.pending {
		pending = append(pending, req)
	}
	s.mu.Unlock()
	for _, req := range pending {
		s.answer(req, grokCancelledOutcome())
	}
}
