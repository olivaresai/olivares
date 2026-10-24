// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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

// acpErrInvalidParams is the JSON-RPC code for a request this client understood and
// refused: a file write the session may not make, now or at all.
const acpErrInvalidParams = -32602

// acpErrInternal is the JSON-RPC code for a request this client accepted and could
// not carry out.
const acpErrInternal = -32603

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

// The agent→client requests with an answer here: the permission request, and the
// one file request OpenCode makes after an approved edit.
const (
	acpReqRequestPermission = "session/request_permission"
	acpReqWriteTextFile     = "fs/write_text_file"
)

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
	// replayHeld is true from the request that asks the tool to replay until that
	// request's correlated response has been read on the pump. replayFor ends with
	// the call whatever its outcome, a timeout included, so a tool that answers late
	// still has its replay in the pipe when it is gone; the bridge keeps that replay
	// out of the stream until the answer is read or the child is gone (replaying).
	replayHeld bool

	commands acpCommandCatalog
	updates  acpUpdateCounts
	// toolFacts are the native facts of this conversation's live tool calls, by
	// toolCallId (observeToolCall), for a permission request that names none.
	toolFacts map[string]acpToolCall
	pending   map[string]*acpServerRequest
	closed    bool
	// usage is what this launch's turns used, summed from each prompt result.
	// usageCost is what this launch cost: the agent's lifetime total (usage_update, US
	// dollars) over costBase, never lower, present once known. costBase is the total
	// when this launch began counting: 0 for a new conversation, the first total seen
	// for a resumed one, which already holds what earlier launches spent.
	usage       modelUsage
	usageCost   *int64
	costBase    *int64
	costFresh   bool // a cost arrived since the last report
	costPending bool // a cost reported during the turn in flight, credited when it ends
}

// newACPSession is the shared state for a driver session; the driver wires the
// connection's request and notification handlers.
func newACPSession(cfg DriverSessionConfig, driver string) acpSession {
	// mcpServers is required by ACP, so the list is an empty array, never null.
	if cfg.sessionMCP == nil {
		cfg.sessionMCP = []json.RawMessage{}
	}
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
// capabilities advertise no terminal, because this client implements no read handler
// and no terminal. An agent that is told the client can read files will ask it to, and a
// client that then answers a protocol error has advertised a capability it does not
// have. fs/write_text_file is served all the same (acp_fs.go): OpenCode asks for it
// after an approved edit whatever was advertised, and advertising it would also send
// every other agent's writes through the engine.
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
	StopReason string    `json:"stopReason"`
	Usage      *acpUsage `json:"usage"`
}

// acpUsage is one turn's token use as an agent reports it on its prompt result
// (OpenCode 1.19.3: inputTokens, outputTokens, totalTokens, thoughtTokens). The
// thought tokens are billed as output.
type acpUsage struct {
	TotalTokens       int64 `json:"totalTokens"`
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	ThoughtTokens     int64 `json:"thoughtTokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CachedWriteTokens int64 `json:"cachedWriteTokens"`
}

// acpUsageUpdate is the session's cumulative cost an agent reports on a
// usage_update (OpenCode 1.19.3: {"amount":0,"currency":"USD"} on a free model).
type acpUsageUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Cost          *struct {
		Amount   *float64 `json:"amount"`
		Currency string   `json:"currency"`
	} `json:"cost"`
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

// beginReplay is called, with s.mu held, before the request that asks the tool to
// replay conversation id: its notifications arrive while the request is in flight.
func (s *acpSession) beginReplay(id string) {
	s.replayFor = id
	s.replayHeld = true
}

// endReplay stops filing the conversation's updates as history. The request's
// caller runs it whatever the outcome; the bridge's hold ends only with the answer.
func (s *acpSession) endReplay() {
	s.mu.Lock()
	s.replayFor = ""
	s.mu.Unlock()
}

// answerReplay is endReplay for the pump, on the correlated response: everything
// before it was replay and everything after it is live.
func (s *acpSession) answerReplay() {
	s.mu.Lock()
	s.replayFor = ""
	s.replayHeld = false
	s.mu.Unlock()
}

// replaying reports whether the stdout the bridge reads now may be the stored
// conversation being replayed: a request asked for it and its answer has not been
// read. The bridge calls it on the goroutine that delivers the answer, which ends
// the hold, so a frame is never judged against a hold that ended before it. An
// error response or a timeout leaves it set: that launch is then torn down.
func (s *acpSession) replaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replayHeld
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
	return true, s.sendCancel(ctx, session)
}

// interruptTurn cancels the turn with this id only. The check and the mark share
// the lock that ends turns, so a successor started meanwhile is never cancelled for
// its predecessor. A turn that already ended is left alone.
func (s *acpSession) interruptTurn(ctx context.Context, turn string) error {
	s.mu.Lock()
	session := s.sessionID
	current := turn != "" && session != "" && s.promptID == turn
	if current {
		s.cancelledTurn = turn
	}
	s.mu.Unlock()
	if !current {
		return nil
	}
	return s.sendCancel(ctx, session)
}

func (s *acpSession) sendCancel(ctx context.Context, session string) error {
	s.cancelPendingApprovals(ctx)
	if err := s.conn.notify(ctx, acpMethodSessionCancel, acpSessionParams{SessionID: session}); err != nil {
		return acpTurnErr("interrupt", err)
	}
	return nil
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
	s.toolFacts = nil // a call's facts belong to the turn that made it
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
		s.creditTurn(nil)
		return
	}
	var resp acpPromptResponse
	if json.Unmarshal(result, &resp) != nil {
		s.creditTurn(nil)
		return
	}
	// The stop reason is the provider's, and it is not reinterpreted here: end_turn,
	// max_tokens, max_turn_requests, refusal and cancelled all END THE TURN and none
	// of them ends the run, the conversation or the process.
	s.warnStopReason(resp.StopReason)
	s.creditTurn(resp.Usage)
}

// creditTurn reports what the turn that ended used: the tokens on its result and
// the cost the agent reported during it. A failed or unreadable result still
// credits that cost; a turn that reported neither reports nothing.
func (s *acpSession) creditTurn(u *acpUsage) {
	s.mu.Lock()
	if u != nil {
		t := u.tokens()
		s.usage.InputTokens += t.InputTokens
		s.usage.OutputTokens += t.OutputTokens
		s.usage.CacheReadTokens += t.CacheReadTokens
		s.usage.CacheCreationTokens += t.CacheCreationTokens
	}
	report := u != nil || s.costPending
	s.costPending = false
	var r resultUsage
	if report {
		r = s.usageReportLocked()
	}
	s.mu.Unlock()
	if report {
		s.reportUsage(r)
	}
}

// tokens is one turn's usage as the runtime counts it: input without the cached
// part, which the cache columns carry. Whether the agent's input includes the
// cached tokens is read from its own total, not assumed.
func (u acpUsage) tokens() modelUsage {
	clamp := func(n int64) int64 { return min(max(0, n), acpMaxTurnTokens) }
	input, out := clamp(u.InputTokens), clamp(u.OutputTokens)+clamp(u.ThoughtTokens)
	read, write := clamp(u.CachedReadTokens), clamp(u.CachedWriteTokens)
	if cached := read + write; cached > 0 && u.TotalTokens == input+out && input >= cached {
		input -= cached
	}
	return modelUsage{InputTokens: input, OutputTokens: out, CacheReadTokens: read, CacheCreationTokens: write}
}

// observeUsage keeps the cost a live usage_update reports. The agent's figure is the
// conversation's lifetime total (OpenCode 1.19.3: usage.ts sums every assistant
// message, and resume replays no usage_update), so this launch's cost is the rise over
// its baseline, and a total that goes down credits nothing. Only US dollars are a cost
// this product records; any other report leaves it unknown.
func (s *acpSession) observeUsage(update json.RawMessage) (resultUsage, bool) {
	var u acpUsageUpdate
	if json.Unmarshal(update, &u) != nil || u.SessionUpdate != "usage_update" || u.Cost == nil ||
		u.Cost.Amount == nil || !(*u.Cost.Amount >= 0 && *u.Cost.Amount <= acpMaxReportedCostUSD) || u.Cost.Currency != "USD" {
		return resultUsage{}, false
	}
	total := int64(math.Round(*u.Cost.Amount * microUSDPerUSD))
	if s.costBase == nil {
		base := int64(0)
		if strings.TrimSpace(s.cfg.ResumeConversationID) != "" {
			// The first total of a resumed conversation holds earlier launches' cost and
			// this turn's: it is the baseline, and this first turn's own cost is unknown.
			s.costBase = &total
			return resultUsage{}, false
		}
		s.costBase = &base
	}
	cost := max(total-*s.costBase, 0)
	if s.usageCost != nil {
		cost = max(cost, *s.usageCost)
	}
	s.usageCost, s.costFresh = &cost, true
	// During a turn its end credits the cost with the turn's tokens, as one credit;
	// outside one it is credited now.
	if s.promptID != "" {
		s.costPending = true
		return resultUsage{}, false
	}
	return s.usageReportLocked(), true
}

// acpMaxReportedCostUSD bounds a session cost an agent reports, and acpMaxTurnTokens
// one turn's token count of each kind; a larger figure is no report or is clipped,
// never an overflow.
const (
	acpMaxReportedCostUSD = 1_000_000
	acpMaxTurnTokens      = 1 << 40
)

// usageReportLocked is this launch's usage as the runtime credits it, from 0 at the
// launch: under the session's model when one was asked for, with the cost when it is
// known. The cost is the tool's own statement only when one arrived since the last
// report.
func (s *acpSession) usageReportLocked() resultUsage {
	u := s.usage
	r := resultUsage{Reported: true, CostReported: s.costFresh}
	s.costFresh = false
	if s.usageCost != nil {
		u.CostMicroUSD = *s.usageCost
	}
	if ref := modelRefValue(s.cfg.Model); ref != "" {
		r.Models = map[string]modelUsage{ref: u}
	} else {
		r.Fallback = u
	}
	return r
}

// reportUsage credits a report: every figure counts from this launch's start.
func (s *acpSession) reportUsage(r resultUsage) {
	if s.cfg.OnUsage != nil {
		s.cfg.OnUsage(r, true)
	}
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
	if r, ok := s.classifyUpdate(n); ok {
		s.reportUsage(r)
	}
}

// classifyUpdate counts one session/update and returns the usage a live one reports.
func (s *acpSession) classifyUpdate(n acpSessionNotification) (resultUsage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeCommands(n)
	switch {
	case s.replayFor != "" && n.SessionID == s.replayFor:
		// The conversation this launch ASKED to continue, replaying itself before its
		// own correlated response. It is history, and it is checked against the
		// requested id rather than the bound one because the binding is exactly what
		// has not happened yet.
		s.updates.history++
		return s.observeUsage(n.Update)
	case s.sessionID == "" || n.SessionID != s.sessionID:
		// Another conversation's update. It binds nothing, ends nothing and is not
		// this session's output.
		s.updates.foreign++
	default:
		s.updates.live++
		s.observeToolCall(n.Update)
		return s.observeUsage(n.Update)
	}
	return resultUsage{}, false
}

// maxACPToolFacts bounds toolFacts; a full map starts over, losing only the facts
// of calls whose permission was already asked.
const maxACPToolFacts = 256

// observeToolCall keeps the native facts of a live tool_call or tool_call_update by
// toolCallId. OpenCode's read asks for permission with locations [] and rawInput {}:
// its path is only in native patterns ACP does not carry. The tool_call_update it
// sends just before, for the same call, names the file (measured over ACP on 1.19.3
// and 1.19.4; #2979). Notifications are handled in order on the pump, before the
// request that follows them. Called with s.mu held.
func (s *acpSession) observeToolCall(raw json.RawMessage) {
	var update struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &update) != nil || (update.Kind != "tool_call" && update.Kind != "tool_call_update") {
		return
	}
	var call acpToolCall
	if json.Unmarshal(raw, &call) != nil || call.ToolCallID == "" {
		return
	}
	if !call.facts.Complete {
		// A later update the facts reader cannot vouch for withdraws the earlier facts.
		delete(s.toolFacts, call.ToolCallID)
		return
	}
	if s.toolFacts == nil || len(s.toolFacts) >= maxACPToolFacts {
		s.toolFacts = map[string]acpToolCall{}
	}
	s.toolFacts[call.ToolCallID] = call
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
