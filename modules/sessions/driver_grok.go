// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/olivaresai/olivares/core/driverfacts"
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

// Grok Build's endpoint variables for a record-bound launch (LaunchEnv).
const (
	envGrokXAIAPIBaseURL = "GROK_XAI_API_BASE_URL"
	envGrokModelsBaseURL = "GROK_MODELS_BASE_URL"
)

// grokMethodAuthenticate is the one ACP method only Grok's handshake sends, as the
// pinned 1.0.13 binary names it; the shared methods are in acp_session.go.
const grokMethodAuthenticate = "authenticate"

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

// --- driver -------------------------------------------------------------------

// grokDriver is the stateless driver; all per-launch state lives in a session.
type grokDriver struct{}

// NewGrokDriver returns the official Grok agent driver.
func NewGrokDriver() ProviderDriver { return grokDriver{} }

func (grokDriver) Key() string { return providerDriverGrok }
func (grokDriver) ConfigHomeEnv() string {
	facts, _ := driverfacts.Lookup(providerDriverGrok)
	return facts.ConfigHomeEnv
}
func (grokDriver) DefaultProgram() string {
	facts, _ := driverfacts.Lookup(providerDriverGrok)
	return facts.Program
}

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

// LaunchTerms declares what a Grok launch hands its child. The model and the
// effort travel on the argv as `--model` and `--reasoning-effort`. The permission
// preset reaches the native sandbox and an advertised ACP mode. Current policy
// still answers every native permission request. The models are discovered from the
// credential a profile binds; the driver lists none.
func (grokDriver) LaunchTerms() DriverLaunchTerms {
	return DriverLaunchTerms{
		Model:          TermCarried,
		Effort:         TermCarried,
		PermissionMode: TermCarried,
		ModelDiscovery: ModelDiscoveryBoundCredentialProbe,
	}
}

// LaunchEnv pins the child's version and native sandbox. Authentication is resolved by
// the runtime and a driver never sees a credential value.
//
// A session on a key from Providers is held to that key's endpoint
// by Grok Build's documented variables (docs.x.ai/build/settings/reference): the API it
// authenticates against with the key, and the base its other models are listed and called
// from (session summaries, image description, web search, subagents). Both are the
// carrier's endpoint.
func (grokDriver) LaunchEnv(l DriverLaunch) []EnvVar {
	env := []EnvVar{{Name: envGrokDisableAutoUpdate, Value: "1"}}
	if l.BoundProvider.Kind != "" {
		env = append(env,
			EnvVar{Name: envGrokXAIAPIBaseURL, Value: l.BoundProvider.Endpoint},
			EnvVar{Name: envGrokModelsBaseURL, Value: l.BoundProvider.Endpoint})
	}
	if sandbox := grokSandboxFor(l.Preset); sandbox != "" {
		env = append(env, EnvVar{Name: "GROK_SANDBOX", Value: sandbox})
	}
	return env
}

// grokSandboxFor is the native sandbox a permission preset turns on ("" for none).
func grokSandboxFor(preset string) string {
	switch preset {
	case PresetReadOnly:
		return "read-only"
	case PresetAsk, PresetEditsOnly, PresetEditsAndCommands:
		return "workspace"
	}
	return ""
}

// GROK BUILD'S SANDBOX NEEDS BUBBLEWRAP ON LINUX. Without bwrap Grok exits during
// initialize ("bwrap exec failed ... Install bubblewrap"), and all a person saw was "the owned
// provider process ended during initialize". So a launch whose preset turns the sandbox on is
// refused first with the reason, and the resolve rule does not offer Grok for its default
// preset on such a server.
const grokSandboxMissing = "Grok Build runs its sandbox with bubblewrap (bwrap), which is not installed on this server; install the bubblewrap package, then start again"

// grokLookPath finds bwrap the way the child's PATH would; a variable only so tests can stand in.
var grokLookPath = exec.LookPath

// grokSandboxUnavailable reports whether a Grok launch under preset needs bubblewrap here and
// it cannot be found.
func grokSandboxUnavailable(preset string) bool {
	if runtime.GOOS != "linux" || grokSandboxFor(preset) == "" {
		return false
	}
	_, err := grokLookPath("bwrap")
	return err != nil
}

func (grokDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &grokSession{acpSession: newACPSession(cfg, providerDriverGrok)}
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

// --- session ------------------------------------------------------------------

// grokSession is one owned ACP conversation over one owned Grok Build child.
type grokSession struct {
	acpSession
	caps grokAgentCapabilities
	// authMethods is what the agent ADVERTISED. It is kept so a refusal can say
	// what was offered instead of what was expected.
	authMethods []grokAuthMethod
}

// --- wire types (ACP v1, as the pinned binary declares them) -------------------

type grokClientCapabilities struct {
	FS       acpFSCapabilities `json:"fs"`
	Terminal bool              `json:"terminal"`
}

type grokInitializeParams struct {
	ProtocolVersion    int                    `json:"protocolVersion"`
	ClientInfo         acpClientInfo          `json:"clientInfo"`
	ClientCapabilities grokClientCapabilities `json:"clientCapabilities"`
}

type grokAgentCapabilities struct {
	LoadSession         bool                   `json:"loadSession"`
	SessionCapabilities acpSessionCapabilities `json:"sessionCapabilities"`
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

// grokSessionIDResponse reads the ONE field a nomination may carry. It is
// deliberately a pointer-free optional: `session/new` MUST answer one, while
// `session/load` and `session/resume` need not — their success is the correlated
// answer to a request that NAMED the id.
type grokSessionIDResponse struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentID string `json:"currentModeId"`
		Available []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
}

type grokPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []acpContentBlock `json:"prompt"`
}

// --- lifecycle ----------------------------------------------------------------

// Handshake runs the owned ACP sequence and returns the ONE nomination this
// launch is entitled to: the correlated root response to its own session/new, or
// the EXACT stored id its own session/resume (or session/load) confirmed.
func (s *grokSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, acpMethodInitialize, grokInitializeParams{
		ProtocolVersion: acpProtocolVersion,
		ClientInfo:      acpClientInfo{Name: s.cfg.ClientName, Version: s.cfg.ClientVersion},
		ClientCapabilities: grokClientCapabilities{
			FS: acpFSCapabilities{ReadTextFile: false, WriteTextFile: false}, Terminal: false,
		},
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, acpHandshakeErr("initialize", err)
	}
	var init grokInitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's initialize response could not be read"}
	}
	if !acpProtocolVersionMatches(init.ProtocolVersion) {
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
	raw, err := s.conn.call(ctx, acpMethodSessionNew, grokNewSessionParams{
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
	if err := s.selectPresetMode(ctx, id, resp); err != nil {
		return DriverHandshake{}, err
	}
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
		method = acpMethodSessionResume
	case caps.LoadSession:
		method = acpMethodSessionLoad
	default:
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider advertises neither session resume nor session load; refusing to start a different conversation",
		}
	}
	raw, err := s.conn.callInOrder(ctx, method, acpResumeSessionParams{
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
	if err := s.selectPresetMode(ctx, resume, resp); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: resume, AuthState: state}, nil
}

func (s *grokSession) selectPresetMode(ctx context.Context, id string, response grokSessionIDResponse) error {
	want := ""
	switch s.cfg.Preset {
	case PresetReadOnly:
		want = "plan"
	case PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull:
		want = "default"
	}
	if want == "" || response.Modes == nil {
		return nil // No native mode equivalent: the live provider gate still applies.
	}
	for _, offered := range response.Modes.Available {
		if offered.ID != want {
			continue
		}
		_, err := s.conn.call(ctx, "session/set_mode", map[string]string{"sessionId": id, "modeId": want}, s.cfg.CallTimeout)
		if err != nil {
			return &runErr{http.StatusBadGateway, "Grok could not apply the chosen session permission preset"}
		}
		return nil
	}
	if response.Modes.CurrentID != "" && response.Modes.CurrentID != want {
		return &runErr{http.StatusBadGateway, "Grok cannot apply the chosen session permission preset; the session was not started"}
	}
	return nil // A legacy response may have no native mode; never invent one.
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
	key, err := s.conn.dispatch(dctx, acpMethodSessionPrompt, grokPromptParams{
		SessionID: session, Prompt: []acpContentBlock{{Type: "text", Text: text}},
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
			return false, acpTurnErr("start", err)
		}
		// A write that FAILED may still have put bytes on the child's stdin.
		// Over-reporting the attempt is the safe direction: it records an ambiguity for
		// something that may have been clean, where the opposite would durably record a
		// clean refusal for something that may have landed.
		return true, acpTurnErr("start", err)
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
		_ = s.conn.notify(ctx, acpMethodSessionCancel, acpSessionParams{SessionID: session})
	}
	if len(caps.SessionCapabilities.Close) > 0 && string(caps.SessionCapabilities.Close) != "null" {
		// Advertised, so it is asked for; bounded, so a peer that will not answer
		// cannot postpone the stop. session/close releases the conversation and is NOT
		// a stop: the teardown below it is what ends the process.
		_, _ = s.conn.call(ctx, acpMethodSessionClose, acpSessionParams{SessionID: session}, s.callTimeout())
	}
}

// --- turn bookkeeping ---------------------------------------------------------

// --- notifications ------------------------------------------------------------

// --- helpers ------------------------------------------------------------------

// grokConversationErr distinguishes the two ways opening a conversation fails,
// because they are different facts about different planes: an agent that says
// "authentication required" has a working protocol and an unauthenticated
// profile, and reporting that as a protocol failure would send an operator to fix
// the wrong thing.
func grokConversationErr(err error, state string) error {
	var re *rpcError
	if errors.As(err, &re) && re.Code == acpErrAuthRequired {
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
	return acpHandshakeErr(acpMethodSessionNew, err)
}
