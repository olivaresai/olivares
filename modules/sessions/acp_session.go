// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Agent Client Protocol (ACP v1) that OpenCode and Grok Build both speak over
// stdio: the methods, the session state and the turn, approval and shutdown
// mechanics are the protocol's, so they live here once. Each driver embeds
// acpSession and keeps only what is its own: launch, handshake, authentication,
// settings and how it maps a permission request onto the governed authority.

// ACP methods, exactly as the pinned binaries name them.
const (
	acpMethodInitialize    = "initialize"
	acpMethodSessionNew    = "session/new"
	acpMethodSessionResume = "session/resume"
	acpMethodSessionLoad   = "session/load"
	acpMethodSessionPrompt = "session/prompt"
	acpMethodSessionCancel = "session/cancel"
	acpMethodSessionClose  = "session/close"

	acpNotifySessionUpdate = "session/update"
)

// acpProtocolVersion is the ONE ACP major version this client implements. The
// agent answers `initialize` with the version it will speak; anything else is a
// peer these drivers cannot correctly talk to, and pretending otherwise would put
// an unknown dialect in front of an approval gate.
const acpProtocolVersion = 1

// acpErrAuthRequired is the JSON-RPC error code an unauthenticated agent answers
// with (RequestError.authRequired in the ACP SDK; observed: -32000
// "Authentication required"). It is read as the PROVIDER's own statement about
// readiness, which is the only thing allowed to move that plane. Match the CODE,
// not localized message text.
const acpErrAuthRequired = -32000

// acpErrMethodNotSupported is the JSON-RPC code for a method this client does
// not implement. An agent request that gets one is answered, not ignored: an
// unanswered request stalls the agent's turn for ever.
const acpErrMethodNotSupported = -32601

// The ACP permission option kinds.
const (
	acpPermissionAllowOnce    = "allow_once"
	acpPermissionAllowAlways  = "allow_always"
	acpPermissionRejectOnce   = "reject_once"
	acpPermissionRejectAlways = "reject_always"
)

// The two `RequestPermissionOutcome` arms.
const (
	acpOutcomeSelected  = "selected"
	acpOutcomeCancelled = "cancelled"
)

// acpReqRequestPermission is the only agent→client request with an answer here.
const acpReqRequestPermission = "session/request_permission"

// acpKindToolCallPermission is the codec family carried to the governed
// authority. ACP has ONE permission surface, so there is one kind — inventing a
// finer taxonomy from the tool call's own text would be a driver deciding what
// another product's tools mean.
const acpKindToolCallPermission = "tool_call_permission"

// acpSession is one owned ACP conversation over one owned agent child: the state
// every ACP driver keeps, embedded by openCodeSession and grokSession.
type acpSession struct {
	cfg  DriverSessionConfig
	conn *rpcConn
	// driver is the key an approval request names.
	driver string

	mu        sync.Mutex
	sessionID string
	// promptID is the correlation key of the in-flight session/prompt, and it IS
	// the turn: ACP has no turn id on the wire, and the request that opened the turn
	// is the only thing that can be shown to have ended it.
	promptID string
	// cancelledTurn is the promptID a session/cancel has been sent for, and it is
	// non-empty only while THAT turn is still winding down. It is the state between
	// the cancel and the correlated result: the turn is still occupied — correctly,
	// because only its own result may end it — and is no longer a turn anything may
	// be authorized for.
	//
	// It NAMES the turn instead of being a session-wide flag on purpose. A
	// cancellation is a fact about one generation, and a mark that outlived its turn
	// would refuse approvals for a successor the operator never stopped.
	cancelledTurn string
	authState     string
	// replayFor is the conversation a resume/load is REPLAYING, non-empty only
	// while that request is in flight. The session/update frames that arrive during
	// it are HISTORY, not new live output, and telling them apart is the difference
	// between showing an operator a conversation and showing them a conversation
	// happening again.
	//
	// It is a separate field from sessionID on purpose: the id is not BOUND until
	// the correlated response confirms it, and the replay arrives BEFORE that
	// response. Classifying against sessionID would file a session's own history as
	// somebody else's, which is what it did until this was measured.
	replayFor string
	updates   acpUpdateCounts
	pending   map[string]*acpServerRequest
	closed    bool
}

// newACPSession is the shared state for a driver session; the driver wires the
// connection's request and notification handlers.
func newACPSession(cfg DriverSessionConfig, driver string) acpSession {
	return acpSession{cfg: cfg, driver: driver, conn: newRPCConn(cfg.Send, true), pending: map[string]*acpServerRequest{}}
}

// acpUpdateCounts separates the three things a session/update can be. It is
// package-internal evidence, not a product surface: the distinction is asserted
// by the tests that exist to prove a notification cannot nominate or finish
// anything.
type acpUpdateCounts struct {
	live    int
	history int
	foreign int
}

type acpClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// acpFSCapabilities is advertised FALSE on both members, and each driver's client
// capabilities advertise no terminal, because this client implements neither handler. An
// agent that is told the client can read files will ask it to, and a client that
// then answers a protocol error has advertised a capability it does not have.
type acpFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// acpSessionCapabilities is an OBJECT whose MEMBERS are the advertisement: the
// observed initialize answered `{"list":{},"resume":{},"close":{}}`, so presence
// is the fact and the value carries the capability's own options. A pointer is
// how "absent" stays distinguishable from "present and empty".
type acpSessionCapabilities struct {
	List   json.RawMessage `json:"list"`
	Resume json.RawMessage `json:"resume"`
	Close  json.RawMessage `json:"close"`
}

type acpResumeSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type acpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type acpPromptResponse struct {
	StopReason string `json:"stopReason"`
}

type acpSessionParams struct {
	SessionID string `json:"sessionId"`
}

// acpSessionNotification is the envelope of `session/update`. Only the
// conversation is read for authority; the update itself is provider content and
// reaches no row, log or API answer from here — the runtime's own bridge already
// captured the line as output.
type acpSessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// acpServerRequest is one in-flight agent→client request. once guarantees that
// it is answered EXACTLY ONCE, whichever of the three racing paths gets there
// first: the authority, the deadline, or a cancellation from interrupt/stop.
type acpServerRequest struct {
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

type acpPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type acpPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type acpRequestPermissionResponse struct {
	Outcome acpPermissionOutcome `json:"outcome"`
}

func (s *acpSession) Deliver(frame OutputFrame) {
	if frame.Stream != streamStdout {
		return
	}
	s.conn.deliver(frame.Data)
}

func (s *acpSession) endReplay() {
	s.mu.Lock()
	s.replayFor = ""
	s.mu.Unlock()
}

// Interrupt cancels the ACTIVE turn and leaves the owned process alive.
//
// ⛔ IT DOES NOT END THE TURN. `session/cancel` is a NOTIFICATION: it has no id,
// no answer, and no acknowledgement. What ends the turn is the original prompt's
// own correlated response coming back with stopReason `cancelled` — the same
// response that would have ended it normally. Clearing the turn here would let
// the next input open a second concurrent turn while the agent is still winding
// the first one down, which is precisely the state this seam refuses.
//
// Pending approvals ARE resolved first, with the protocol's own cancellation, so
// the agent is never left waiting on a request whose turn is being torn down.
func (s *acpSession) Interrupt(ctx context.Context) (bool, error) {
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if prompt == "" {
		return false, conflictErr("there is no active provider turn to interrupt")
	}
	s.cancelPendingApprovals(ctx)
	if err := s.conn.notify(ctx, acpMethodSessionCancel, acpSessionParams{SessionID: session}); err != nil {
		return true, acpTurnErr("interrupt", err)
	}
	return true, nil
}

func (s *acpSession) AuthState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authState == "" {
		return AuthStateUnknown
	}
	return s.authState
}

// ActiveTurn is the in-flight prompt's correlation key. ACP puts no turn id on
// the wire, so the request that opened the turn IS its identity — and it is an
// identity nothing outside this session can forge.
func (s *acpSession) ActiveTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptID
}

func (s *acpSession) ConversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Close fails every in-flight waiter once the owned child is gone. The dispatched
// prompt has no waiter, so its terminal callback is what ends the turn — closing
// the pump is what fires it.
func (s *acpSession) Close(err error) {
	s.mu.Lock()
	s.closed = true
	s.pending = map[string]*acpServerRequest{}
	s.mu.Unlock()
	s.conn.close(err)
}

func (s *acpSession) beginTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || id == "" {
		return
	}
	s.promptID = id
}

// clearTurn ends the turn with THIS id and never any other. The guard is what
// keeps a late callback from ending a successor: ids are unique per request, so a
// callback for a turn that already ended finds another one active and leaves it
// alone.
func (s *acpSession) clearTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return
	}
	if s.promptID == id {
		s.promptID = ""
	}
	// A cancellation is a fact about ONE turn and it is released with it, whatever
	// order the two arrive in. Ids are unique per request, so a stale mark could not
	// match a successor anyway; clearing it keeps the field meaning what it says.
	if s.cancelledTurn == id {
		s.cancelledTurn = ""
	}
}

// markTurnCancelled records that this conversation's ACTIVE turn is being
// cancelled, and returns the conversation and that turn.
//
// Reading the turn and naming it cancelled in ONE critical section is what keeps
// the mark on the turn it was taken for. A conversation with nothing in flight is
// not marked at all, and a turn opened after the correlated result of a cancelled
// one carries nothing from it.
func (s *acpSession) markTurnCancelled() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" && s.promptID != "" {
		s.cancelledTurn = s.promptID
	}
	return s.sessionID, s.promptID
}

// turnCancelled reports whether session/cancel has been sent for THIS turn and
// its own correlated result has not come back yet.
func (s *acpSession) turnCancelled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id != "" && s.cancelledTurn == id
}

// completeTurn is the prompt's terminal callback. It runs on the pump goroutine
// the moment the correlated response is decoded, which is the only frame entitled
// to end this turn.
func (s *acpSession) completeTurn(id string, result json.RawMessage, err error) {
	s.clearTurn(id)
	if err != nil {
		var re *rpcError
		if errors.As(err, &re) && re.Code == acpErrAuthRequired {
			// The provider's OWN statement that this profile is not authenticated. A
			// launch-time readiness that could never be revoked would be the "a home path
			// is proof of an account" mistake with a timestamp on it.
			s.setAuthState(AuthStateRequired)
		}
		return
	}
	var resp acpPromptResponse
	if json.Unmarshal(result, &resp) != nil {
		return
	}
	// The stop reason is the provider's, and it is not reinterpreted here: end_turn,
	// max_tokens, max_turn_requests, refusal and cancelled all END THE TURN and none
	// of them ends the run, the conversation or the process.
	s.warnStopReason(resp.StopReason)
}

func (s *acpSession) warnStopReason(reason string) {
	if reason == "" {
		s.warn("sessions: the provider's turn response carried no stop reason", "run_ref", s.cfg.RunRef)
	}
}

// onNotification handles the agent's push frames. NOTHING here may nominate a
// conversation or end a turn: a notification is addressed to nobody in
// particular, and a subagent, a fork or a replayed history produces the same
// shape as live output.
func (s *acpSession) onNotification(method string, params json.RawMessage) {
	if method != acpNotifySessionUpdate {
		// Extension notifications (Grok's x.ai/* family, for one) are not part of
		// this client's surface. Ignoring a NOTIFICATION is correct: it expects no
		// answer.
		return
	}
	var n acpSessionNotification
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.replayFor != "" && n.SessionID == s.replayFor:
		// The conversation this launch ASKED to continue, replaying itself before its
		// own correlated response. It is history, and it is checked against the
		// requested id rather than the bound one because the binding is exactly what
		// has not happened yet.
		s.updates.history++
	case s.sessionID == "" || n.SessionID != s.sessionID:
		// Another conversation's update. It binds nothing, ends nothing and is not
		// this session's output.
		s.updates.foreign++
	default:
		s.updates.live++
	}
}

// updateCounts is the package-internal split of what session/update frames were.
func (s *acpSession) updateCounts() acpUpdateCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

// workDir is the cwd this conversation is opened against. ACP requires one on
// session/new, and an unworkspaced launch runs the child in the engine's own
// working directory: naming it describes what the child already has rather than
// choosing one for it.
func (s *acpSession) workDir() string {
	if dir := strings.TrimSpace(s.cfg.WorkDir); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}

func (s *acpSession) callTimeout() time.Duration {
	if s.cfg.CallTimeout > 0 {
		return s.cfg.CallTimeout
	}
	return defaultDriverCallTimeout
}

// setAuthState records readiness and publishes the CHANGE to the runtime. Only a
// change is published: a re-read that confirms what the row already says is not
// news.
func (s *acpSession) setAuthState(state string) {
	s.mu.Lock()
	changed := s.authState != state
	s.authState = state
	s.mu.Unlock()
	if changed && s.cfg.OnAuthState != nil {
		s.cfg.OnAuthState(state)
	}
}

func (s *acpSession) warn(msg string, args ...any) {
	if s.cfg.Warn != nil {
		s.cfg.Warn(msg, args...)
	}
}

// answer writes the reply exactly once.
func (s *acpSession) answer(req *acpServerRequest, reply any) {
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

// refuse answers with the agent's own one-shot rejection when it offered one, and
// with the protocol's cancellation when it did not.
func (s *acpSession) refuse(req *acpServerRequest, options []acpPermissionOption) {
	if optionID, ok := acpSelectRefusal(options); ok {
		s.answer(req, acpSelectedOutcome(optionID))
		return
	}
	s.answer(req, acpCancelledOutcome())
}

func (s *acpSession) forgetServerRequest(key string) {
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}

// cancelPendingApprovals answers every in-flight request with the protocol's own
// cancellation before the turn is interrupted or the process is torn down, so the
// agent is never left waiting on a request whose turn no longer exists.
func (s *acpSession) cancelPendingApprovals(context.Context) {
	s.mu.Lock()
	pending := make([]*acpServerRequest, 0, len(s.pending))
	for _, req := range s.pending {
		pending = append(pending, req)
	}
	s.mu.Unlock()
	for _, req := range pending {
		s.answer(req, acpCancelledOutcome())
	}
}

// acpValidPermissionRequest reports whether the request can be answered by
// SELECTION at all, and names why not.
//
// A duplicate option id fails the WHOLE request rather than picking the first
// match: two options sharing an id make "the option the authority named"
// ambiguous, and an ambiguous grant is a grant nobody authorized. An option whose
// kind the driver does not know (known) is not a failure — it is simply not
// selectable, which is the deny-closed reading.
func acpValidPermissionRequest(sessionID string, options []acpPermissionOption, known func(kind string) bool) (string, bool) {
	if sessionID == "" {
		return "the request named no conversation", false
	}
	if len(options) == 0 {
		return "the request offered no options to select", false
	}
	seen := make(map[string]bool, len(options))
	selectable := false
	for _, opt := range options {
		if opt.OptionID == "" {
			return "the request offered an option with no id", false
		}
		if seen[opt.OptionID] {
			return "the request offered two options with the same id", false
		}
		seen[opt.OptionID] = true
		selectable = selectable || known(opt.Kind)
	}
	if !selectable {
		return "the request offered no option of a kind this client understands", false
	}
	return "", true
}

// acpOfferedOptionIDs is what the AUTHORITY is shown: exactly the ids the agent
// offered, in wire order. A grant is intersected with this, so an authority can
// neither invent an option nor widen the one it was shown.
func acpOfferedOptionIDs(options []acpPermissionOption) []string {
	out := make([]string, 0, len(options))
	for _, opt := range options {
		out = append(out, opt.OptionID)
	}
	return out
}

// acpSelectRefusal picks the option that REFUSES this tool call and nothing
// more.
//
// Only `reject_once` qualifies. `reject_always` is a persistent decision — a
// standing "never" recorded against this project — and it is not what a
// turn-scoped refusal means, however convenient it would be when it is the only
// rejection on offer. When no one-shot rejection is offered, the answer is the
// protocol's cancellation, which grants nothing and records nothing.
func acpSelectRefusal(options []acpPermissionOption) (string, bool) {
	for _, opt := range options {
		if opt.Kind == acpPermissionRejectOnce {
			return opt.OptionID, true
		}
	}
	return "", false
}

func acpTurnErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusConflict, "the owned provider process is no longer accepting input"}
	}
	return conflictErr("the provider refused to " + step + " a turn on this conversation")
}

// acpHandshakeErr keeps provider text out of the API answer: an agent's error
// message can echo a path or an argument, and this one crosses to a client.
func acpHandshakeErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusBadGateway, "the owned provider process ended during " + step}
	}
	return &runErr{http.StatusBadGateway, "the provider refused " + step + " for this launch"}
}

// acpProtocolVersionMatches reports whether the agent answered the ONE major
// version this client implements. Anything that is not that exact number —
// another version, a string, null, a missing field — is a mismatch.
func acpProtocolVersionMatches(raw json.RawMessage) bool {
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return false
	}
	return version == acpProtocolVersion
}

// acpCancelledOutcome is the protocol's own no-grant answer. It is a VALID
// response, not a hang and not an error: the agent learns the client will not
// decide, and grants nothing.
func acpCancelledOutcome() any {
	return acpRequestPermissionResponse{Outcome: acpPermissionOutcome{Outcome: acpOutcomeCancelled}}
}

func acpSelectedOutcome(optionID string) any {
	return acpRequestPermissionResponse{
		Outcome: acpPermissionOutcome{Outcome: acpOutcomeSelected, OptionID: optionID},
	}
}
