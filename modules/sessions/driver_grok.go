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
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The official Grok agent driver (CLI 1.0.13), over ACP on the owned child's
// stdio.
//
// AUTHORITY. Three sources, and they are not interchangeable. (1) The pinned
// executable's OWN shipped documentation and `--help`, which is what this binary
// does. (2) The Agent Client Protocol, which is what the wire means. (3) The
// recorded `initialize` of an owned 1.0.13 child under an empty isolated home
// (an internal design note (not shipped)), which is what this build
// actually answered. Where a generic guide disagrees with the installed help, the
// installed help wins; where nothing was observed, this file says so instead of
// guessing.
//
// WIRE. NDJSON over stdio, JSON-RPC 2.0 WITH the `"jsonrpc":"2.0"` member — the
// one place ACP and the Codex app-server differ on the wire, which is why the
// envelope has always been a field of the shared pump rather than an assumption
// inside it.
//
// OWNERSHIP. The spawn is always `agent --no-leader … stdio` as OUR child. The
// leader forms (`--leader`, `agent leader`) share ONE backend between clients,
// which is adoption of a process this run did not create; `serve` and `headless`
// are network servers. None of them is an owned operate spawn and this driver
// cannot produce one.
//
// ⛔ THE ONE THING THAT IS NOT THE CODEX DRIVER WITH ANOTHER VOCABULARY. Codex's
// `turn/start` answers "the turn started"; ACP's `session/prompt` answers "the
// turn FINISHED", carrying the stopReason. Copying the Codex shape would either
// misreport every normal model turn as a 30-second gateway timeout, or hold the
// per-run operation lock — /input, /interrupt, /stop, /resume — for the whole
// turn. So the prompt is DISPATCHED (rpcConn.dispatch) and its completion lands
// later on the pump. Input answers "a frame was written"; the turn ends when its
// own correlated response says so, and nothing else may end it.

// providerDriverGrok is the driver key of the official Grok agent driver. The
// driver key SET stays open: a provider becomes operable by being REGISTERED with
// the runtime, never by being named in a list.
const providerDriverGrok = "grok"

// envGrokDisableAutoUpdate pins the child's version for the life of the session.
//
// It is the provider's OWN documented variable and this is the provider's own
// documented use of it: the shipped headless guide records that the agent SDKs
// inject `GROK_DISABLE_AUTOUPDATER=1` for the non-leader agents they spawn. The
// flag form (`--no-auto-update`) is documented for `grok -p`, is absent from
// `grok agent --help`, and is therefore not what this driver relies on.
const envGrokDisableAutoUpdate = "GROK_DISABLE_AUTOUPDATER"

// ACP methods, exactly as the pinned 1.0.13 binary names them.
const (
	grokMethodInitialize    = "initialize"
	grokMethodAuthenticate  = "authenticate"
	grokMethodSessionNew    = "session/new"
	grokMethodSessionResume = "session/resume"
	grokMethodSessionLoad   = "session/load"
	grokMethodSessionPrompt = "session/prompt"
	grokMethodSessionCancel = "session/cancel"
	grokMethodSessionClose  = "session/close"

	grokNotifySessionUpdate = "session/update"
)

// grokProtocolVersion is the ONE ACP major version this client implements. The
// agent answers `initialize` with the version it will speak; anything else is a
// peer this driver cannot correctly talk to, and pretending otherwise would put
// an unknown dialect in front of an approval gate.
const grokProtocolVersion = 1

// The advertised authentication method ids this driver can use NONINTERACTIVELY,
// and the one it never selects.
//
// Both usable ids exist in the pinned binary, which also records their
// precedence ("cached_token overrides xai.api_key for default_auth_method_id").
// They are matched to the AUTHORIZED source and never to each other:
//
//   - provider_account_home  ⇒ cached_token: the login already stored inside the
//     profile's own GROK_HOME (`auth.json`). Olivares never reads, copies or
//     parses that file; the owned child opens the home it was authorized for.
//   - managed_injection      ⇒ xai.api_key: the provider-compatible key that this
//     driver's GOVERNED adapter minted into the child's environment.
//
// `grok.com` is the interactive browser/OIDC sign-in. It is a real method and it
// is never selected here: a headless control plane cannot complete it, and
// selecting it to look ready would start a flow nobody is watching.
const (
	grokAuthMethodCachedToken = "cached_token"
	grokAuthMethodAPIKey      = "xai.api_key"
	grokAuthMethodBrowser     = "grok.com"
)

// grokErrAuthRequired is the JSON-RPC error code an unauthenticated Grok agent
// answers `session/new` with (observed: -32000 "Authentication required"). It is
// read as the PROVIDER's own statement about readiness, which is the only thing
// allowed to move that plane.
const grokErrAuthRequired = -32000

// grokErrMethodNotSupported is the JSON-RPC code for a method this client does
// not implement. An agent request that gets one is answered, not ignored: an
// unanswered request stalls the agent's turn for ever.
const grokErrMethodNotSupported = -32601

// --- driver -------------------------------------------------------------------

// grokDriver is the stateless driver; all per-launch state lives in a session.
type grokDriver struct{}

// NewGrokDriver returns the official Grok agent driver.
func NewGrokDriver() ProviderDriver { return grokDriver{} }

func (grokDriver) Key() string            { return providerDriverGrok }
func (grokDriver) ConfigHomeEnv() string  { return envGrokHome }
func (grokDriver) DefaultProgram() string { return "grok" }

// LaunchArgs is the ONLY operate form: an owned, non-leader stdio agent.
//
// The option ORDER is the installed CLI's, not a preference: `grok agent --help`
// and the shipped agent-mode guide both put the agent options after `agent` and
// before the mode name. Model and effort are the provider's OWN open strings —
// `--model` and `--reasoning-effort` are documented agent options of this binary
// — and no common enum is invented across providers.
// The form itself is declared ONCE, in cliruntime, beside the transport it
// requires (r3): a driver that kept its own copy would let the declaration and
// the launch drift apart without a test going red.
func (grokDriver) LaunchArgs(l DriverLaunch) []string {
	return cliruntime.GrokArgs(l.cliRuntimeRequest())
}

// TransportProfile describes the channel this driver's owned child actually
// speaks: ACP over the stdio of a non-leader agent, bridged in both directions,
// with one operator turn arriving as TEXT. Grok has no remote-control form, and
// naming its protocol here is what stops a console describing it as one.
func (grokDriver) TransportProfile() DriverTransportProfile {
	return DriverTransportProfile{
		Protocol: protocolGrokACP, IO: ioBidirectional, Input: inputText,
	}
}

// LaunchEnv pins the child's version. Nothing else: authentication is resolved by
// the runtime and a driver never sees a credential value.
func (grokDriver) LaunchEnv(DriverLaunch) []EnvVar {
	return []EnvVar{{Name: envGrokDisableAutoUpdate, Value: "1"}}
}

func (grokDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &grokSession{cfg: cfg, pending: map[string]*grokServerRequest{}}
	s.conn = newRPCConn(cfg.Send, true)
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

// --- session ------------------------------------------------------------------

// grokSession is one owned ACP conversation over one owned agent child.
type grokSession struct {
	cfg  DriverSessionConfig
	conn *rpcConn

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
	caps      grokAgentCapabilities
	// authMethods is what the agent ADVERTISED. It is kept so a refusal can say
	// what was offered instead of what was expected.
	authMethods []grokAuthMethod
	updates     grokUpdateCounts
	pending     map[string]*grokServerRequest
	closed      bool
}

// grokUpdateCounts separates the three things a session/update can be. It is
// package-internal evidence, not a product surface: the distinction is asserted
// by the tests that exist to prove a notification cannot nominate or finish
// anything.
type grokUpdateCounts struct {
	live    int
	history int
	foreign int
}

// --- wire types (ACP v1, as the pinned binary declares them) -------------------

type grokClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// grokFSCapabilities is advertised FALSE on both members, and grokClientCapabilities
// advertises no terminal, because this client implements neither handler. An
// agent that is told the client can read files will ask it to, and a client that
// then answers a protocol error has advertised a capability it does not have.
type grokFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type grokClientCapabilities struct {
	FS       grokFSCapabilities `json:"fs"`
	Terminal bool               `json:"terminal"`
}

type grokInitializeParams struct {
	ProtocolVersion    int                    `json:"protocolVersion"`
	ClientInfo         grokClientInfo         `json:"clientInfo"`
	ClientCapabilities grokClientCapabilities `json:"clientCapabilities"`
}

// grokSessionCapabilities is an OBJECT whose MEMBERS are the advertisement: the
// observed initialize answered `{"list":{},"resume":{},"close":{}}`, so presence
// is the fact and the value carries the capability's own options. A pointer is
// how "absent" stays distinguishable from "present and empty".
type grokSessionCapabilities struct {
	List   json.RawMessage `json:"list"`
	Resume json.RawMessage `json:"resume"`
	Close  json.RawMessage `json:"close"`
}

type grokAgentCapabilities struct {
	LoadSession         bool                    `json:"loadSession"`
	SessionCapabilities grokSessionCapabilities `json:"sessionCapabilities"`
}

type grokAuthMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type grokInitializeResponse struct {
	// ProtocolVersion is RAW so a peer that answers a string, a null or an object
	// is a mismatch this driver can name, rather than a zero it would silently
	// accept.
	ProtocolVersion   json.RawMessage       `json:"protocolVersion"`
	AgentCapabilities grokAgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []grokAuthMethod      `json:"authMethods"`
	AgentVersion      string                `json:"agentVersion"`
}

type grokAuthenticateParams struct {
	MethodID string `json:"methodId"`
	// Meta carries the official headless example's `headless` marker, which tells
	// the agent that nobody is in front of a browser. It is NOT an authorization of
	// anything: permission decisions are the approval gate's, and this driver never
	// asks for a wider mode.
	Meta grokHeadlessMeta `json:"_meta"`
}

type grokHeadlessMeta struct {
	Headless bool `json:"headless"`
}

type grokNewSessionParams struct {
	Cwd string `json:"cwd"`
	// MCPServers is REQUIRED by the protocol and is deliberately an empty, non-nil
	// slice: this launch connects the child to no MCP server of its own.
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type grokResumeSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

// grokSessionIDResponse reads the ONE field a nomination may carry. It is
// deliberately a pointer-free optional: `session/new` MUST answer one, while
// `session/load` and `session/resume` need not — their success is the correlated
// answer to a request that NAMED the id.
type grokSessionIDResponse struct {
	SessionID string `json:"sessionId"`
}

type grokContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type grokPromptParams struct {
	SessionID string             `json:"sessionId"`
	Prompt    []grokContentBlock `json:"prompt"`
}

type grokPromptResponse struct {
	StopReason string `json:"stopReason"`
}

type grokSessionParams struct {
	SessionID string `json:"sessionId"`
}

// grokSessionNotification is the envelope of `session/update`. Only the
// conversation is read for authority; the update itself is provider content and
// reaches no row, log or API answer from here — the runtime's own bridge already
// captured the line as output.
type grokSessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// --- lifecycle ----------------------------------------------------------------

func (s *grokSession) Deliver(frame OutputFrame) {
	if frame.Stream != streamStdout {
		return
	}
	s.conn.deliver(frame.Data)
}

// Handshake runs the owned ACP sequence and returns the ONE nomination this
// launch is entitled to: the correlated root response to its own session/new, or
// the EXACT stored id its own session/resume (or session/load) confirmed.
func (s *grokSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, grokMethodInitialize, grokInitializeParams{
		ProtocolVersion: grokProtocolVersion,
		ClientInfo:      grokClientInfo{Name: s.cfg.ClientName, Version: s.cfg.ClientVersion},
		ClientCapabilities: grokClientCapabilities{
			FS: grokFSCapabilities{ReadTextFile: false, WriteTextFile: false}, Terminal: false,
		},
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, grokHandshakeErr("initialize", err)
	}
	var init grokInitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's initialize response could not be read"}
	}
	if !grokProtocolVersionMatches(init.ProtocolVersion) {
		// A peer speaking another major version is not a peer this client can hold to
		// the contract it is about to enforce.
		return DriverHandshake{}, &runErr{
			http.StatusBadGateway,
			"the provider answered an agent protocol version this client does not implement",
		}
	}
	s.mu.Lock()
	s.caps, s.authMethods = init.AgentCapabilities, init.AuthMethods
	s.mu.Unlock()

	state := s.authenticate(ctx, init.AuthMethods)
	s.setAuthState(state)

	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" {
		return s.resumeConversation(ctx, resume, state)
	}
	return s.newConversation(ctx, state)
}

// authenticate selects the advertised method that MATCHES this launch's
// authorized source, and returns the readiness the provider itself established.
//
// ⛔ NOTHING HERE IS INFERRED FROM A PATH. A GROK_HOME that exists, contains an
// `auth.json`, or was used yesterday proves nothing about this launch; what the
// plane records is what the agent answered when asked. And there is no fallback
// between the two sources: an account-home profile never authenticates with an
// injected key, and a managed profile never opens the account home, because each
// of those is a launch the operator did not authorize.
func (s *grokSession) authenticate(ctx context.Context, advertised []grokAuthMethod) string {
	if len(advertised) == 0 {
		// The agent asked for nothing, so there is nothing this launch's authorized
		// source can be matched against. The binding contract calls a profile offering
		// no usable advertised method auth_required, and an EMPTY advertisement is
		// that case reached by the shortest road.
		//
		// ⛔ IT USED TO BE `unknown`, AND THAT WAS A HOLE RATHER THAN CAUTION.
		// `unknown` is neither ready nor required, and a turn is refused only on
		// `required` — so a typed operator prompt CROSSED to an owned child that had
		// authenticated nothing. Deny-closed here is both halves at once: no method to
		// name, so no authenticate is attempted, and no turn may start.
		s.warn("sessions: the provider advertised no authentication method; this profile is not ready",
			"run_ref", s.cfg.RunRef)
		return AuthStateRequired
	}
	methodID, ok := grokAuthMethodForSource(s.cfg.AuthSource, advertised)
	if !ok {
		// Either no source was authorized, or the only methods on offer are ones this
		// headless launch cannot complete. The empty-home probe advertised exactly one,
		// the interactive browser sign-in, and naming that case is worth a line: the
		// operator's next step is to authenticate the profile's own home, not to
		// re-check the control plane.
		s.warn("sessions: no advertised authentication method matches this profile's authorized source",
			"run_ref", s.cfg.RunRef, "auth_source", s.cfg.AuthSource,
			"interactive_only", grokOnlyInteractiveOffered(advertised))
		return AuthStateRequired
	}
	if _, err := s.conn.call(ctx, grokMethodAuthenticate, grokAuthenticateParams{
		MethodID: methodID, Meta: grokHeadlessMeta{Headless: true},
	}, s.cfg.CallTimeout); err != nil {
		// The provider refused its own advertised method. The process and the
		// conversation are separate planes: this is a readiness answer, not a launch
		// failure, and whether the conversation can be opened at all is the next call's
		// question.
		s.warn("sessions: the provider refused the advertised authentication method for this profile",
			"run_ref", s.cfg.RunRef, "method_id", methodID)
		return AuthStateRequired
	}
	return AuthStateReady
}

// grokAuthMethodForSource maps an AUTHORIZED source onto an ADVERTISED method id.
// It returns false when the source authorizes none, or when the method that
// source implies was not offered.
func grokAuthMethodForSource(source string, advertised []grokAuthMethod) (string, bool) {
	var want string
	switch source {
	case AuthSourceAccountHome:
		want = grokAuthMethodCachedToken
	case AuthSourceManagedInjection:
		want = grokAuthMethodAPIKey
	default:
		return "", false
	}
	for _, m := range advertised {
		if m.ID == want {
			return want, true
		}
	}
	return "", false
}

// grokOnlyInteractiveOffered reports the case the empty-home probe recorded: the
// agent offered the browser sign-in and nothing else. It is a non-secret
// observation for a log line, never a reason to start that flow.
func grokOnlyInteractiveOffered(advertised []grokAuthMethod) bool {
	if len(advertised) == 0 {
		return false
	}
	for _, m := range advertised {
		if m.ID != grokAuthMethodBrowser {
			return false
		}
	}
	return true
}

// newConversation opens a fresh conversation. Only this correlated root response
// may nominate it.
func (s *grokSession) newConversation(ctx context.Context, state string) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, grokMethodSessionNew, grokNewSessionParams{
		Cwd: s.workDir(), MCPServers: []json.RawMessage{},
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, grokConversationErr(err, state)
	}
	var resp grokSessionIDResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response could not be read"}
	}
	id := strings.TrimSpace(resp.SessionID)
	if id == "" {
		// A new conversation with no id is a conversation this run cannot address,
		// resume or bind. There is nothing to fall back to.
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response carried no session id"}
	}
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
	return DriverHandshake{ConversationID: id, AuthState: state}, nil
}

// resumeConversation continues the EXACT stored conversation, and refuses
// anything else.
//
// ⛔ THE SUCCESS CONDITION IS THE CORRELATED ANSWER, NOT A FIELD. ACP's
// session/resume answers an object that need not carry a session id at all, and
// session/load may answer null. Requiring an id the protocol does not promise
// would turn every successful resume into a failure; accepting an id the response
// happens to carry, when it differs from the one we asked to continue, would hand
// the operator somebody else's conversation under the name of theirs. So: the
// request NAMES the id, a successful response CONFIRMS it, and a response that
// contradicts it is refused.
//
// ⛔ AND THE CAPABILITY IS CHOSEN ONCE. Resume is preferred when advertised;
// load is used only when resume is not. A resume that was ATTEMPTED and failed is
// never retried as a load and never becomes a session/new — those are different
// conversations wearing the same name.
func (s *grokSession) resumeConversation(ctx context.Context, resume, state string) (DriverHandshake, error) {
	s.mu.Lock()
	caps := s.caps
	// Set BEFORE the request: replay arrives as notifications while it is in flight.
	s.replayFor = resume
	s.mu.Unlock()

	method := ""
	switch {
	case len(caps.SessionCapabilities.Resume) > 0 && string(caps.SessionCapabilities.Resume) != "null":
		method = grokMethodSessionResume
	case caps.LoadSession:
		method = grokMethodSessionLoad
	default:
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider advertises neither session resume nor session load; refusing to start a different conversation",
		}
	}
	raw, err := s.conn.callInOrder(ctx, method, grokResumeSessionParams{
		SessionID: resume, Cwd: s.workDir(), MCPServers: []json.RawMessage{},
	}, s.cfg.CallTimeout, func(json.RawMessage) {
		// On the pump, in stream order: everything before this response was replay,
		// everything after it is live. Clearing it on the caller's goroutine would let
		// a live update arrive first and be filed as history.
		s.endReplay()
	})
	if err != nil {
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider could not resume the stored conversation for this session; refusing to start a different one",
		}
	}
	// A null result is a legitimate success here and unmarshals into the zero value.
	var resp grokSessionIDResponse
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &resp); err != nil {
			return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response could not be read"}
		}
	}
	if id := strings.TrimSpace(resp.SessionID); id != "" && id != resume {
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider resumed a different conversation than the one stored for this session; refusing it",
		}
	}
	s.mu.Lock()
	s.sessionID = resume
	s.mu.Unlock()
	return DriverHandshake{ConversationID: resume, AuthState: state}, nil
}

func (s *grokSession) endReplay() {
	s.mu.Lock()
	s.replayFor = ""
	s.mu.Unlock()
}

// Input opens the conversation's ONE turn and returns as soon as the frame is on
// the wire.
//
// ⛔ ACP HAS NO STEER. Codex can add input to a running turn; ACP's only turn verb
// is another session/prompt, and a second prompt on a live conversation is a
// SECOND CONCURRENT TURN — the exact thing this seam exists to refuse. So the
// second one is refused BEFORE any byte is written, with the uncertainty boundary
// reported honestly as "nothing was attempted".
func (s *grokSession) Input(ctx context.Context, text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, badRequest("input text is required for a provider-driven session")
	}
	s.mu.Lock()
	session, prompt := s.sessionID, s.promptID
	s.mu.Unlock()
	if session == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if prompt != "" {
		return false, conflictErr("a provider turn is already in flight on this conversation; interrupt it before sending another")
	}
	// The DISPATCH is bounded; the turn is not. A stdin that will not take the line
	// is a failure of this call, and a model that thinks for an hour is not.
	dctx, cancel := context.WithTimeout(ctx, s.callTimeout())
	defer cancel()
	key, err := s.conn.dispatch(dctx, grokMethodSessionPrompt, grokPromptParams{
		SessionID: session, Prompt: []grokContentBlock{{Type: "text", Text: text}},
	}, func(id string) func(json.RawMessage, error) {
		// Recorded UNDER THE PUMP'S MUTEX, so an agent that answers — or asks for an
		// approval — on the very next line is judged against a turn that is already
		// recorded. The callback closes over its own id: it can never end a successor.
		s.beginTurn(id)
		return func(result json.RawMessage, err error) { s.completeTurn(id, result, err) }
	})
	if err != nil {
		// The registration was withdrawn by dispatch, and the turn it opened here has
		// to go with it — otherwise the conversation refuses every later input as "a
		// turn is already in flight" on a turn that never started.
		s.clearTurn(key)
		if key == "" {
			// No correlation id was ever allocated, which is dispatch's own way of
			// saying the channel was already closed: no line, nothing attempted. It is
			// read from the key rather than from the error because the error is whatever
			// ENDED the channel — a process exit, a teardown — and matching on its text
			// or its wrapping would make this depend on who closed it.
			return false, grokTurnErr("start", err)
		}
		// A write that FAILED may still have put bytes on the child's stdin.
		// Over-reporting the attempt is the safe direction: it records an ambiguity for
		// something that may have been clean, where the opposite would durably record a
		// clean refusal for something that may have landed.
		return true, grokTurnErr("start", err)
	}
	return true, nil
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
func (s *grokSession) Interrupt(ctx context.Context) (bool, error) {
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if prompt == "" {
		return false, conflictErr("there is no active provider turn to interrupt")
	}
	s.cancelPendingApprovals(ctx)
	if err := s.conn.notify(ctx, grokMethodSessionCancel, grokSessionParams{SessionID: session}); err != nil {
		return true, grokTurnErr("interrupt", err)
	}
	return true, nil
}

// Shutdown is the BOUNDED graceful half of a terminal stop: resolve pending
// approvals, cancel an in-flight turn, and close the conversation when the agent
// advertised that it can. It does not, and cannot, end the process — the process
// group teardown does that, and it is the caller's next act.
func (s *grokSession) Shutdown(ctx context.Context) {
	// The same notification, so the same state: between here and the process
	// teardown the conversation is still live, and an approval that arrives in that
	// window belongs to a turn that is being cancelled.
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return
	}
	s.mu.Lock()
	caps := s.caps
	s.mu.Unlock()
	s.cancelPendingApprovals(ctx)
	if prompt != "" {
		_ = s.conn.notify(ctx, grokMethodSessionCancel, grokSessionParams{SessionID: session})
	}
	if len(caps.SessionCapabilities.Close) > 0 && string(caps.SessionCapabilities.Close) != "null" {
		// Advertised, so it is asked for; bounded, so a peer that will not answer
		// cannot postpone the stop. session/close releases the conversation and is NOT
		// a stop: the teardown below it is what ends the process.
		_, _ = s.conn.call(ctx, grokMethodSessionClose, grokSessionParams{SessionID: session}, s.callTimeout())
	}
}

func (s *grokSession) AuthState() string {
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
func (s *grokSession) ActiveTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptID
}

func (s *grokSession) ConversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Close fails every in-flight waiter once the owned child is gone. The dispatched
// prompt has no waiter, so its terminal callback is what ends the turn — closing
// the pump is what fires it.
func (s *grokSession) Close(err error) {
	s.mu.Lock()
	s.closed = true
	s.pending = map[string]*grokServerRequest{}
	s.mu.Unlock()
	s.conn.close(err)
}

// --- turn bookkeeping ---------------------------------------------------------

func (s *grokSession) beginTurn(id string) {
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
func (s *grokSession) clearTurn(id string) {
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
func (s *grokSession) markTurnCancelled() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" && s.promptID != "" {
		s.cancelledTurn = s.promptID
	}
	return s.sessionID, s.promptID
}

// turnCancelled reports whether session/cancel has been sent for THIS turn and
// its own correlated result has not come back yet.
func (s *grokSession) turnCancelled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id != "" && s.cancelledTurn == id
}

// completeTurn is the prompt's terminal callback. It runs on the pump goroutine
// the moment the correlated response is decoded, which is the only frame entitled
// to end this turn.
func (s *grokSession) completeTurn(id string, result json.RawMessage, err error) {
	s.clearTurn(id)
	if err != nil {
		var re *rpcError
		if errors.As(err, &re) && re.Code == grokErrAuthRequired {
			// The provider's OWN statement that this profile is not authenticated. A
			// launch-time readiness that could never be revoked would be the "a home path
			// is proof of an account" mistake with a timestamp on it.
			s.setAuthState(AuthStateRequired)
		}
		return
	}
	var resp grokPromptResponse
	if json.Unmarshal(result, &resp) != nil {
		return
	}
	// The stop reason is the provider's, and it is not reinterpreted here: end_turn,
	// max_tokens, max_turn_requests, refusal and cancelled all END THE TURN and none
	// of them ends the run, the conversation or the process.
	s.warnStopReason(resp.StopReason)
}

func (s *grokSession) warnStopReason(reason string) {
	if reason == "" {
		s.warn("sessions: the provider's turn response carried no stop reason", "run_ref", s.cfg.RunRef)
	}
}

// --- notifications ------------------------------------------------------------

// onNotification handles the agent's push frames. NOTHING here may nominate a
// conversation or end a turn: a notification is addressed to nobody in
// particular, and a subagent, a fork or a replayed history produces the same
// shape as live output.
func (s *grokSession) onNotification(method string, params json.RawMessage) {
	if method != grokNotifySessionUpdate {
		// The x.ai/* extension notifications (including the `_x.ai/mcp/servers_updated`
		// the observed initialize was followed by) are not part of this client's
		// surface. Ignoring a NOTIFICATION is correct: it expects no answer.
		return
	}
	var n grokSessionNotification
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
func (s *grokSession) updateCounts() grokUpdateCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

// --- helpers ------------------------------------------------------------------

// workDir is the cwd this conversation is opened against. ACP requires one on
// session/new, and an unworkspaced launch runs the child in the engine's own
// working directory: naming it describes what the child already has rather than
// choosing one for it.
func (s *grokSession) workDir() string {
	if dir := strings.TrimSpace(s.cfg.WorkDir); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}

func (s *grokSession) callTimeout() time.Duration {
	if s.cfg.CallTimeout > 0 {
		return s.cfg.CallTimeout
	}
	return defaultDriverCallTimeout
}

// setAuthState records readiness and publishes the CHANGE to the runtime. Only a
// change is published: a re-read that confirms what the row already says is not
// news.
func (s *grokSession) setAuthState(state string) {
	s.mu.Lock()
	changed := s.authState != state
	s.authState = state
	s.mu.Unlock()
	if changed && s.cfg.OnAuthState != nil {
		s.cfg.OnAuthState(state)
	}
}

func (s *grokSession) warn(msg string, args ...any) {
	if s.cfg.Warn != nil {
		s.cfg.Warn(msg, args...)
	}
}

// grokProtocolVersionMatches reports whether the agent answered the ONE major
// version this client implements. Anything that is not that exact number —
// another version, a string, null, a missing field — is a mismatch.
func grokProtocolVersionMatches(raw json.RawMessage) bool {
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return false
	}
	return version == grokProtocolVersion
}

// grokHandshakeErr keeps provider text out of the API answer: an agent's error
// message can echo a path or an argument, and this one crosses to a client.
func grokHandshakeErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusBadGateway, "the owned provider process ended during " + step}
	}
	return &runErr{http.StatusBadGateway, "the provider refused " + step + " for this launch"}
}

// grokConversationErr distinguishes the two ways opening a conversation fails,
// because they are different facts about different planes: an agent that says
// "authentication required" has a working protocol and an unauthenticated
// profile, and reporting that as a protocol failure would send an operator to fix
// the wrong thing.
func grokConversationErr(err error, state string) error {
	var re *rpcError
	if errors.As(err, &re) && re.Code == grokErrAuthRequired {
		return &runErr{
			http.StatusUnprocessableEntity,
			"the provider refused to open a conversation because this profile is not authenticated (auth_required)",
		}
	}
	if state == AuthStateRequired {
		return &runErr{
			http.StatusUnprocessableEntity,
			"the provider refused to open a conversation and reports that this profile is not authenticated (auth_required)",
		}
	}
	return grokHandshakeErr(grokMethodSessionNew, err)
}

func grokTurnErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusConflict, "the owned provider process is no longer accepting input"}
	}
	return conflictErr("the provider refused to " + step + " a turn on this conversation")
}
