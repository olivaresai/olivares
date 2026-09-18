// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The official Codex app-server driver (CLI 0.153.4).
//
// AUTHORITY. Every wire shape here comes from the generated schemas of THAT exact
// CLI, retained under an internal design note (not shipped)
// and hashed in the contract check. Nothing is guessed and nothing is copied from
// another version: `initialize`/`initialized`, `account/read`, `thread/start`,
// `thread/resume`, `thread/unsubscribe`, `turn/start`, `turn/steer`,
// `turn/interrupt`, and the six server→client approval surfaces.
//
// WIRE. NDJSON over the owned child's stdio, JSON-RPC 2.0 SEMANTICS but WITHOUT
// the `"jsonrpc":"2.0"` member: JSONRPCRequest.json requires exactly `id` and
// `method`, and the recorded standalone capture of this CLI shows frames with no
// such member. ACP (Grok, next) does carry it — which is why the envelope is a
// field of the shared pump rather than an assumption inside it.
//
// OWNERSHIP. The spawn is always `codex app-server --listen stdio://` as OUR
// child, through the module's Runner with its own process group. There is no
// daemon, no `--remote`, no proxy and no adoption of a running app-server: a
// process we did not create is a process we cannot claim, stop or fence.
//
// EXPERIMENTAL SURFACES ARE NOT CLAIMED. `initialize` declares
// `experimentalApi: false`, and the experimental `item/tool/requestUserInput`
// request is answered with a protocol error rather than synthesized empty
// answers — advertising support in order to fabricate a reply would be a claim
// the product cannot keep.

// Codex environment and argv.
const (
	// envCodexHome is the configuration home variable of the official Codex CLI.
	// It is the Codex counterpart of CLAUDE_CONFIG_DIR and is owned by the profile.
	envCodexHome = "CODEX_HOME"
)

// Codex methods, exactly as the installed schemas name them.
const (
	codexMethodInitialize        = "initialize"
	codexMethodInitialized       = "initialized"
	codexMethodAccountRead       = "account/read"
	codexMethodThreadStart       = "thread/start"
	codexMethodThreadResume      = "thread/resume"
	codexMethodThreadUnsubscribe = "thread/unsubscribe"
	codexMethodTurnStart         = "turn/start"
	codexMethodTurnSteer         = "turn/steer"
	codexMethodTurnInterrupt     = "turn/interrupt"

	codexNotifyTurnCompleted = "turn/completed"
	// The provider's OWN statement that it lost, and then regained, the ability to
	// authenticate with the model provider. It is what makes readiness revocable
	// instead of a one-shot answer taken at launch.
	codexNotifyAuthRecoveryStarted   = "modelProvider/authRecoveryStarted"
	codexNotifyAuthRecoveryCompleted = "modelProvider/authRecoveryCompleted"
)

// CodexPolicy is the pair of INDEPENDENT controls a Codex thread runs under. They
// are separate on purpose (adjudication, §5.5): `never` on the approval policy
// means "send no approval requests", NOT permission to widen the sandbox.
//
// The zero value is the deny-closed floor this slice launches with — the tightest
// pair the schema offers. Widening either one is an explicit authorization
// decision that belongs to the profile/template plane, not to a default here, and
// it is deliberately NOT derived from Claude's permission_mode: that enum is
// Claude's and means nothing to Codex.
type CodexPolicy struct {
	// Approval is the AskForApproval value. Empty ⇒ "untrusted".
	Approval CodexApprovalPolicy
	// Sandbox is the SandboxMode value. Empty ⇒ "read-only".
	Sandbox string
}

// Codex SandboxMode values (SandboxMode in ThreadStartParams.json).
const (
	CodexSandboxReadOnly       = "read-only"
	CodexSandboxWorkspaceWrite = "workspace-write"
	CodexSandboxDangerFull     = "danger-full-access"
)

// Codex AskForApproval string values. The obsolete `on-failure` is deliberately
// absent: it is not in this CLI's schema and adding it would be an invention.
const (
	CodexApprovalUntrusted = "untrusted"
	CodexApprovalOnRequest = "on-request"
	CodexApprovalNever     = "never"
)

func (p CodexPolicy) approval() CodexApprovalPolicy {
	if p.Approval.isZero() {
		return CodexApprovalPolicy{Mode: CodexApprovalUntrusted}
	}
	return p.Approval
}

func (p CodexPolicy) sandbox() string {
	if p.Sandbox == "" {
		return CodexSandboxReadOnly
	}
	return p.Sandbox
}

// ValidCodexSandbox reports whether s is one of the three schema values.
func ValidCodexSandbox(s string) bool {
	return s == CodexSandboxReadOnly || s == CodexSandboxWorkspaceWrite || s == CodexSandboxDangerFull
}

// codexDriver is the stateless driver; all per-launch state lives in a session.
type codexDriver struct {
	policy CodexPolicy
}

// NewCodexDriver returns the official Codex app-server driver at the deny-closed
// policy floor (approval `untrusted`, sandbox `read-only`).
func NewCodexDriver() ProviderDriver { return codexDriver{} }

// NewCodexDriverWithPolicy returns the driver under an EXPLICITLY authorized pair
// of controls. It exists so widening is a decision somebody made and can be read
// back, never a default that drifted.
func NewCodexDriverWithPolicy(policy CodexPolicy) (ProviderDriver, error) {
	if policy.Sandbox != "" && !ValidCodexSandbox(policy.Sandbox) {
		return nil, errors.New("sessions: codex sandbox must be read-only, workspace-write or danger-full-access")
	}
	if err := policy.approval().validate(); err != nil {
		return nil, err
	}
	return codexDriver{policy: policy}, nil
}

func (codexDriver) Key() string            { return providerDriverCodex }
func (codexDriver) ConfigHomeEnv() string  { return envCodexHome }
func (codexDriver) DefaultProgram() string { return "codex" }

// TransportProfile describes the channel this driver's owned child actually
// speaks: the Codex app-server protocol over stdio, bridged in both directions,
// with one operator turn arriving as TEXT (the driver encodes it as that
// provider's own turn method). It is deliberately NOT the Claude stream-json
// contract and deliberately NOT remote-control — neither is a form this driver
// can produce, and this read must not let a console offer them.
func (codexDriver) TransportProfile() DriverTransportProfile {
	return DriverTransportProfile{
		Protocol: protocolCodexAppServer, IO: ioBidirectional, Input: inputText,
	}
}

// LaunchArgs is the ONLY operate form: an owned stdio app-server. The daemon,
// proxy and remote forms are not options this driver can produce.
func (codexDriver) LaunchArgs(DriverLaunch) []string {
	return []string{"app-server", "--listen", "stdio://"}
}

func (d codexDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &codexSession{cfg: cfg, policy: d.policy, pending: map[string]*codexServerRequest{}}
	s.conn = newRPCConn(cfg.Send, false)
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

// codexSession is one owned conversation over one owned app-server child.
type codexSession struct {
	cfg    DriverSessionConfig
	policy CodexPolicy
	conn   *rpcConn

	mu        sync.Mutex
	threadID  string
	turnID    string
	authState string
	pending   map[string]*codexServerRequest
	closed    bool
	// finished holds turn ids whose completion arrived BEFORE we had recorded them
	// as active. A fast provider can answer turn/start and complete the turn in the
	// same breath, and the two are processed by different goroutines: the response
	// unblocks the caller, the notification runs on the pump. Without this the
	// completion would clear nothing and the caller would then mark a finished turn
	// active — leaving the session convinced a turn is in flight for ever, refusing
	// every later input as "steer" on a turn that no longer exists. Bounded: a turn
	// id is only interesting until its own start returns.
	finished []string
}

// codexFinishedTurnMemory bounds the ids above. It only ever needs to cover turns
// whose start response has not been read yet, so a handful is generous.
const codexFinishedTurnMemory = 16

// --- wire types (exactly the installed schemas) ------------------------------

type codexClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type codexInitializeCapabilities struct {
	// ExperimentalApi stays false: a stable-only driver does not receive
	// experimental methods and fields, and therefore never advertises them.
	ExperimentalApi bool `json:"experimentalApi"`
}

type codexInitializeParams struct {
	ClientInfo   codexClientInfo             `json:"clientInfo"`
	Capabilities codexInitializeCapabilities `json:"capabilities"`
}

type codexAccountReadParams struct {
	// RefreshToken false is the supported LOCAL status read: it must not trigger a
	// refresh flow as a side effect of asking whether we are authenticated.
	RefreshToken bool `json:"refreshToken"`
}

type codexAccountReadResponse struct {
	Account            json.RawMessage `json:"account"`
	RequiresOpenaiAuth bool            `json:"requiresOpenaiAuth"`
}

type codexThreadStartParams struct {
	Cwd            *string             `json:"cwd,omitempty"`
	ApprovalPolicy CodexApprovalPolicy `json:"approvalPolicy"`
	Sandbox        string              `json:"sandbox"`
	Model          *string             `json:"model,omitempty"`
}

type codexThreadResumeParams struct {
	ThreadID       string              `json:"threadId"`
	Cwd            *string             `json:"cwd,omitempty"`
	ApprovalPolicy CodexApprovalPolicy `json:"approvalPolicy"`
	Sandbox        string              `json:"sandbox"`
	Model          *string             `json:"model,omitempty"`
}

// codexThread is the subset of Thread this driver reads. `parentThreadId` and
// `agentRole` are read for ONE reason: a subagent thread must never be bound as
// this launch's conversation.
type codexThread struct {
	ID             string  `json:"id"`
	ParentThreadID *string `json:"parentThreadId"`
	AgentRole      *string `json:"agentRole"`
}

type codexThreadResponse struct {
	Thread codexThread `json:"thread"`
}

type codexUserInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexTurnStartParams struct {
	ThreadID string               `json:"threadId"`
	Input    []codexUserInputText `json:"input"`
	Effort   *string              `json:"effort,omitempty"`
}

type codexTurnSteerParams struct {
	ThreadID       string               `json:"threadId"`
	ExpectedTurnID string               `json:"expectedTurnId"`
	Input          []codexUserInputText `json:"input"`
}

type codexTurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type codexThreadUnsubscribeParams struct {
	ThreadID string `json:"threadId"`
}

type codexTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type codexTurnStartResponse struct {
	Turn codexTurn `json:"turn"`
}

type codexTurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

type codexTurnCompletedNotification struct {
	ThreadID string    `json:"threadId"`
	Turn     codexTurn `json:"turn"`
}

// --- session ------------------------------------------------------------------

func (s *codexSession) Deliver(frame OutputFrame) {
	if frame.Stream != streamStdout {
		return
	}
	s.conn.deliver(frame.Data)
}

// Handshake runs the owned sequence and returns the ONE nomination this launch is
// entitled to: the correlated root response to its own thread/start or the EXACT
// thread/resume it asked for.
func (s *codexSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	if _, err := s.conn.call(ctx, codexMethodInitialize, codexInitializeParams{
		ClientInfo:   codexClientInfo{Name: s.cfg.ClientName, Version: s.cfg.ClientVersion},
		Capabilities: codexInitializeCapabilities{ExperimentalApi: false},
	}, s.cfg.CallTimeout); err != nil {
		return DriverHandshake{}, codexHandshakeErr("initialize", err)
	}
	// The documented handshake completes with this notification; nothing is sent
	// before it, so no method can be attempted on a half-initialized peer.
	if err := s.conn.notify(ctx, codexMethodInitialized, nil); err != nil {
		return DriverHandshake{}, codexHandshakeErr("initialized", err)
	}

	state := AuthStateUnknown
	if raw, err := s.conn.call(ctx, codexMethodAccountRead, codexAccountReadParams{RefreshToken: false}, s.cfg.CallTimeout); err != nil {
		// A readiness we could not read stays UNKNOWN. It is not "ready", and it is
		// not a launch failure either: the process and the conversation are separate
		// planes from authentication.
		s.warn("sessions: could not read the provider account status; authentication readiness stays unknown")
	} else {
		state = codexAuthStateFrom(raw)
	}
	s.setAuthState(state)

	var raw json.RawMessage
	var err error
	cwd := codexOptionalString(s.cfg.WorkDir)
	model := codexOptionalString(s.cfg.Model)
	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" {
		raw, err = s.conn.call(ctx, codexMethodThreadResume, codexThreadResumeParams{
			ThreadID: resume, Cwd: cwd, ApprovalPolicy: s.policy.approval(), Sandbox: s.policy.sandbox(), Model: model,
		}, s.cfg.CallTimeout)
		if err != nil {
			// REFUSE. A resume that failed is not an invitation to start a different
			// conversation: that would silently hand the operator a new thread under
			// the name of the one they asked to continue.
			return DriverHandshake{}, &runErr{
				http.StatusConflict,
				"the provider could not resume the stored conversation for this session; refusing to start a different one",
			}
		}
	} else {
		raw, err = s.conn.call(ctx, codexMethodThreadStart, codexThreadStartParams{
			Cwd: cwd, ApprovalPolicy: s.policy.approval(), Sandbox: s.policy.sandbox(), Model: model,
		}, s.cfg.CallTimeout)
		if err != nil {
			return DriverHandshake{}, codexHandshakeErr("thread/start", err)
		}
	}
	var resp codexThreadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response could not be read"}
	}
	id := strings.TrimSpace(resp.Thread.ID)
	if id == "" {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response carried no thread id"}
	}
	if codexIsSubagentThread(resp.Thread) {
		// A subagent/forked thread is somebody else's conversation. Binding it would
		// give this run authority over a process tree it does not own.
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider nominated a subagent conversation; refusing to bind it to this session"}
	}
	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" && id != resume {
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider resumed a different conversation than the one stored for this session; refusing it",
		}
	}
	s.mu.Lock()
	s.threadID = id
	s.mu.Unlock()
	return DriverHandshake{ConversationID: id, AuthState: state}, nil
}

// Input starts a turn when the conversation is idle, or STEERS the active one.
// It never opens a second concurrent turn on the same conversation.
//
// ⛔ THE TURN IS PUBLISHED FROM THE PUMP, NOT FROM HERE. The response to
// turn/start authorises the very requests the provider may send on the next line
// of the same stream, so the turn it establishes has to be recorded before that
// line is judged. `callInOrder` runs the hook on the pump goroutine, in stream
// order; this goroutine then only reports the outcome. Recording it here instead
// was the measured false-refusal: ten of ten valid same-turn approvals cancelled.
func (s *codexSession) Input(ctx context.Context, text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, badRequest("input text is required for a provider-driven session")
	}
	s.mu.Lock()
	thread, turn := s.threadID, s.turnID
	s.mu.Unlock()
	if thread == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	input := []codexUserInputText{{Type: "text", Text: text}}
	if turn == "" {
		raw, err := s.conn.callInOrder(ctx, codexMethodTurnStart, codexTurnStartParams{
			ThreadID: thread, Input: input, Effort: codexOptionalString(s.cfg.Effort),
		}, s.cfg.CallTimeout, func(result json.RawMessage) {
			var started codexTurnStartResponse
			if json.Unmarshal(result, &started) == nil && started.Turn.ID != "" {
				s.setActiveTurn(started.Turn.ID, started.Turn.Status)
			}
		})
		// From the moment the request was written the frame has crossed: whatever
		// happens next, the caller cannot be told nothing was attempted.
		if err != nil {
			return true, codexTurnErr("start", err)
		}
		var resp codexTurnStartResponse
		if err := json.Unmarshal(raw, &resp); err != nil || resp.Turn.ID == "" {
			return true, &runErr{http.StatusBadGateway, "the provider's turn response carried no turn id"}
		}
		return true, nil
	}
	_, err := s.conn.callInOrder(ctx, codexMethodTurnSteer, codexTurnSteerParams{
		ThreadID: thread, ExpectedTurnID: turn, Input: input,
	}, s.cfg.CallTimeout, func(result json.RawMessage) {
		var steered codexTurnSteerResponse
		if json.Unmarshal(result, &steered) == nil && steered.TurnID != "" {
			s.setActiveTurn(steered.TurnID, "")
		}
	})
	if err != nil {
		// The provider refused to steer (the turn moved on, or it is not steerable).
		// That is a conflict on THIS input, not a reason to open a second turn.
		return true, codexTurnErr("steer", err)
	}
	return true, nil
}

// Interrupt cancels the ACTIVE turn and leaves the owned process alive. Pending
// approvals are cancelled with THEIR OWN method's codec first, so the child is
// never left waiting on a request whose turn no longer exists.
//
// ⛔ THE TWO GUARDS BELOW ARE THE ONLY PRE-EFFECT ANSWERS, and that is why they
// return false. From cancelPendingApprovals onwards bytes may already be on the
// child's stdin — each cancelled approval is a response frame written before the
// interrupt request is — so no later failure may be reported as "nothing was
// attempted". Over-reporting the attempt is the safe direction: it records an
// ambiguity for something that may have been clean, where the opposite would
// record a clean refusal for something that may have landed.
func (s *codexSession) Interrupt(ctx context.Context) (bool, error) {
	s.mu.Lock()
	thread, turn := s.threadID, s.turnID
	s.mu.Unlock()
	if thread == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if turn == "" {
		return false, conflictErr("there is no active provider turn to interrupt")
	}
	s.cancelPendingApprovals(ctx)
	if _, err := s.conn.call(ctx, codexMethodTurnInterrupt, codexTurnInterruptParams{
		ThreadID: thread, TurnID: turn,
	}, s.cfg.CallTimeout); err != nil {
		return true, codexTurnErr("interrupt", err)
	}
	s.clearTurn(turn)
	return true, nil
}

// Shutdown is the BOUNDED graceful half of a terminal stop: cancel an in-flight
// turn and unsubscribe from the thread. It does not, and cannot, end the process
// — the process group teardown does that, and it is the caller's next act.
func (s *codexSession) Shutdown(ctx context.Context) {
	s.mu.Lock()
	thread, turn := s.threadID, s.turnID
	s.mu.Unlock()
	if thread == "" {
		return
	}
	s.cancelPendingApprovals(ctx)
	if turn != "" {
		if _, err := s.conn.call(ctx, codexMethodTurnInterrupt, codexTurnInterruptParams{
			ThreadID: thread, TurnID: turn,
		}, s.cfg.CallTimeout); err == nil {
			s.clearTurn(turn)
		}
	}
	// thread/unsubscribe releases the subscription; the standalone probe showed it
	// does NOT kill the OS process, which is exactly why the teardown still runs.
	_, _ = s.conn.call(ctx, codexMethodThreadUnsubscribe, codexThreadUnsubscribeParams{ThreadID: thread}, s.cfg.CallTimeout)
}

func (s *codexSession) AuthState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authState == "" {
		return AuthStateUnknown
	}
	return s.authState
}

func (s *codexSession) ActiveTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnID
}

func (s *codexSession) ConversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID
}

func (s *codexSession) Close(err error) {
	s.mu.Lock()
	s.closed = true
	s.pending = map[string]*codexServerRequest{}
	s.mu.Unlock()
	s.conn.close(err)
}

// onNotification handles the notifications that move the TURN and AUTHENTICATION
// planes. Nothing here may nominate a conversation: a notification is addressed to
// nobody in particular, and a subagent or another client produces the same shape.
func (s *codexSession) onNotification(method string, params json.RawMessage) {
	switch method {
	case codexNotifyTurnCompleted:
		var n codexTurnCompletedNotification
		if err := json.Unmarshal(params, &n); err != nil {
			return
		}
		if s.foreignConversation(n.ThreadID) {
			return // a turn of another conversation completes nothing of ours
		}
		// A completed turn ends the TURN, not the run and not the process: the same
		// owned child accepts another input immediately.
		s.clearTurn(n.Turn.ID)
	case codexNotifyAuthRecoveryStarted:
		// The provider says it is recovering its model-provider authentication, so
		// whatever `account/read` answered at launch is no longer true. Readiness is
		// an OBSERVATION and it is revocable: a launch-time "ready" that could never
		// go back would be the "a home path is proof of an account" mistake with a
		// timestamp on it.
		var n codexAuthRecoveryNotification
		if err := json.Unmarshal(params, &n); err != nil || s.foreignConversation(n.ThreadID) {
			return
		}
		s.setAuthState(AuthStateRequired)
	case codexNotifyAuthRecoveryCompleted:
		var n codexAuthRecoveryNotification
		if err := json.Unmarshal(params, &n); err != nil || s.foreignConversation(n.ThreadID) {
			return
		}
		// Recovery COMPLETED is not the same statement as "authenticated": it says the
		// attempt finished. Ask again rather than assume, off the pump goroutine so a
		// notification never blocks the stream it arrived on.
		go s.refreshAuthState()
	}
}

// codexAuthRecoveryNotification is the shared shape of the two recovery
// notifications. Only the conversation is read: the message is provider text and
// never reaches a row, a log or an API answer.
type codexAuthRecoveryNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

func (s *codexSession) foreignConversation(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return threadID != s.threadID
}

// refreshAuthState re-reads the LOCAL account status and republishes readiness.
func (s *codexSession) refreshAuthState() {
	ctx, cancel := context.WithTimeout(context.Background(), s.callTimeout())
	defer cancel()
	raw, err := s.conn.call(ctx, codexMethodAccountRead, codexAccountReadParams{RefreshToken: false}, s.callTimeout())
	if err != nil {
		// We could not ask. That is not "ready" and it is not "required" either.
		s.setAuthState(AuthStateUnknown)
		return
	}
	s.setAuthState(codexAuthStateFrom(raw))
}

func (s *codexSession) callTimeout() time.Duration {
	if s.cfg.CallTimeout > 0 {
		return s.cfg.CallTimeout
	}
	return defaultDriverCallTimeout
}

// setAuthState records readiness and publishes the CHANGE to the runtime, which
// persists it on the run row. Only a change is published: a re-read that confirms
// what the row already says is not news.
func (s *codexSession) setAuthState(state string) {
	s.mu.Lock()
	changed := s.authState != state
	s.authState = state
	s.mu.Unlock()
	if changed && s.cfg.OnAuthState != nil {
		s.cfg.OnAuthState(state)
	}
}

func (s *codexSession) setActiveTurn(id, status string) {
	if status == "completed" || status == "interrupted" || status == "failed" {
		return // already finished when we read the response
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, done := range s.finished {
		if done == id {
			return // it already ended; a response that names it does not resurrect it
		}
	}
	s.turnID = id
}

// clearTurn ends a turn and REMEMBERS that it ended.
//
// Remembering is the load-bearing half. A turn id is unique and a finished turn
// can never become the active one again, but the response that names it is read
// on the CALLER's goroutine while the notification that ends it arrives on the
// pump — so a start (or a steer) can record an id the pump has already retired,
// in either order. Clearing alone fixed one ordering and left the other: a
// completion between a steer's response and the caller writing it back
// re-activated a turn that was over, and every later input became a steer on a
// turn the provider no longer had.
func (s *codexSession) clearTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || s.turnID == id {
		s.turnID = ""
	}
	if id == "" {
		return
	}
	for _, done := range s.finished {
		if done == id {
			return
		}
	}
	s.finished = append(s.finished, id)
	if len(s.finished) > codexFinishedTurnMemory {
		s.finished = s.finished[len(s.finished)-codexFinishedTurnMemory:]
	}
}

func (s *codexSession) warn(msg string, args ...any) {
	if s.cfg.Warn != nil {
		s.cfg.Warn(msg, args...)
	}
}

// codexAuthStateFrom maps the account/read answer onto the readiness plane.
//
//   - an account object ⇒ ready (saved ChatGPT OAuth, or an API-key account);
//   - no account and requiresOpenaiAuth ⇒ required (the empty-home probe's answer);
//   - no account and NOT requiresOpenaiAuth ⇒ ready, which is the legitimate
//     answer of an explicitly authorized custom provider. It is only reachable
//     under a profile whose authentication source was authorized, so "the server
//     says no OpenAI auth is needed" is a statement about a configuration
//     somebody chose, not an ambient default.
func codexAuthStateFrom(raw json.RawMessage) string {
	var resp codexAccountReadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return AuthStateUnknown
	}
	if len(resp.Account) > 0 && string(resp.Account) != "null" {
		return AuthStateReady
	}
	if resp.RequiresOpenaiAuth {
		return AuthStateRequired
	}
	return AuthStateReady
}

// codexIsSubagentThread reports a thread that belongs to a spawned subagent.
func codexIsSubagentThread(t codexThread) bool {
	return (t.ParentThreadID != nil && strings.TrimSpace(*t.ParentThreadID) != "") ||
		(t.AgentRole != nil && strings.TrimSpace(*t.AgentRole) != "")
}

func codexOptionalString(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// codexHandshakeErr keeps provider text out of the API answer: an app-server
// error message can echo a path or an argument, and this one crosses to a client.
func codexHandshakeErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusBadGateway, "the owned provider process ended during " + step}
	}
	return &runErr{http.StatusBadGateway, "the provider refused " + step + " for this launch"}
}

func codexTurnErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusConflict, "the owned provider process is no longer accepting input"}
	}
	return conflictErr("the provider refused to " + step + " a turn on this conversation")
}
