// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
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

// --- wire types ---------------------------------------------------------------

// grokToolCall is the subset of the tool call this driver reads. The id and kind
// are references; the title and the raw input are provider TEXT and are read by
// nobody here.
type grokToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Kind       string `json:"kind"`
}

type grokRequestPermissionParams struct {
	SessionID string                `json:"sessionId"`
	ToolCall  grokToolCall          `json:"toolCall"`
	Options   []acpPermissionOption `json:"options"`
}

// --- validation ---------------------------------------------------------------

// grokKnownPermissionKind reports an option kind this client understands. An
// unknown kind is not an error in itself — the protocol may grow kinds — it is
// simply not selectable, which is the deny-closed reading. A request in which
// NONE of the options is understood cannot be answered by selection at all, and
// says so rather than picking one by position.
func grokKnownPermissionKind(kind string) bool {
	switch kind {
	case acpPermissionAllowOnce, acpPermissionAllowAlways,
		acpPermissionRejectOnce, acpPermissionRejectAlways:
		return true
	default:
		return false
	}
}

// grokSelectGrant picks the option to SELECT for an authorized decision, or
// reports that none may be.
//
// The candidate must satisfy three things at once, and dropping any of them is a
// widening: the agent offered it, the authority named it, and its kind is one the
// decision actually authorizes. `allow_always` is a PERSISTENT grant that outlives
// this tool call, so it needs the authority to have asked for session scope; a
// turn-scoped decision cannot be upgraded by the agent's choice of options.
func grokSelectGrant(options []acpPermissionOption, dec ProviderApprovalDecision) (string, bool) {
	granted := make(map[string]bool, len(dec.Granted))
	for _, id := range dec.Granted {
		granted[id] = true
	}
	for _, opt := range options {
		if !granted[opt.OptionID] {
			continue
		}
		switch opt.Kind {
		case acpPermissionAllowOnce:
			return opt.OptionID, true
		case acpPermissionAllowAlways:
			if dec.SessionScope {
				return opt.OptionID, true
			}
		}
	}
	return "", false
}

// --- dispatch -----------------------------------------------------------------

func (s *grokSession) onServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method != acpReqRequestPermission {
		// Answered, not ignored: an unanswered request stalls the agent's turn for
		// ever, and answering it with a grant would be worse than stalling. This is
		// also where the capabilities this client did NOT advertise come home — an
		// `fs/read_text_file` or a `terminal/create` gets a protocol error rather than
		// a synthesized answer that would make the client look capable.
		go func() {
			_ = s.conn.respondError(context.Background(), id, acpErrMethodNotSupported,
				"this client does not implement "+method)
		}()
		return
	}
	req := &acpServerRequest{id: id, method: method}
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
func (s *grokSession) resolveServerRequest(req *acpServerRequest, key string, raw json.RawMessage, session, turn string, cancellingAtArrival bool) {
	defer s.forgetServerRequest(key)

	var params grokRequestPermissionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.warn("sessions: a provider permission request could not be read and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if why, ok := acpValidPermissionRequest(params.SessionID, params.Options, grokKnownPermissionKind); !ok {
		s.warn("sessions: a provider permission request was malformed and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method, "why", why)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if session == "" || params.SessionID != session {
		// A request naming another conversation is not ours to answer with a grant.
		s.warn("sessions: a provider permission request named a foreign conversation and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
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
		s.answer(req, acpCancelledOutcome())
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

	offered := acpOfferedOptionIDs(params.Options)
	dec := ProviderApprovalDecision{}
	if s.cfg.Approve != nil {
		got, err := s.cfg.Approve(ctx, ProviderApprovalRequest{
			Driver: providerDriverGrok, RunRef: s.cfg.RunRef, ProfileRef: s.cfg.ProfileRef,
			ConversationID: session, TurnID: turn,
			Method: req.method, Kind: acpKindToolCallPermission,
			// Exactly what the agent offered: an authority cannot widen a request it
			// was shown, because a selection is checked back against this list.
			Requested: offered,
		})
		if err != nil {
			// An authority that could not decide never means "allow".
			s.warn("sessions: the provider approval authority failed; refusing deny-closed",
				"run_ref", s.cfg.RunRef, "method", req.method, "cause", errorCause(err, "unavailable"))
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
		s.answer(req, acpCancelledOutcome())
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
			s.answer(req, acpCancelledOutcome())
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
	s.answer(req, acpSelectedOutcome(optionID))
}
