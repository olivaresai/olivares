// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The INTERNAL provider-driver seam.
//
// The historical operate path drives `claude` by PARSING its stream-json output:
// the process announces a session id on an init frame and the bridge captures it.
// That works for a provider whose transport is a one-way announcement, and it is
// preserved here byte for byte.
//
// An official app-server / ACP child is a different shape: it answers REQUESTS,
// and the only frame that may nominate a conversation is the CORRELATED ROOT
// RESPONSE to the request this launch itself sent. A notification cannot, because
// a notification is not addressed to anybody — a subagent, a fork or another
// client's thread produces the same shape.
//
// So a driver owns four things and nothing else:
//
//  1. the argv and the configuration-home variable of ITS official CLI;
//  2. the handshake that nominates the conversation for THIS launch;
//  3. the turn controls (start, steer, interrupt) over that conversation;
//  4. the method-specific codecs for the requests the child sends back.
//
// Everything durable stays where it already is: the run row, the claim, the
// fence, the scoped alias and the transactional capture belong to the runtime,
// not to a driver. A driver never writes a row and never sees a credential value.
//
// Grok implements this same interface next (CORRECTED-CONTRACT §11 item 7); no
// Claude permission/effort enum is inherited by anybody (§5.5).

// providerDriverCodex is the driver key of the official Codex app-server driver.
// The driver key SET stays open (normalizeDriverKey validates the shape, not the
// value): a provider becomes operable by being REGISTERED with the runtime, never
// by being named in a list here.
const providerDriverCodex = "codex"

// DriverLaunch is the non-secret launch context a driver turns into argv. It
// carries references and choices, never a credential: the authentication source
// is resolved separately (runtime_provider_auth.go) and only the child's
// environment ever carries material.
type DriverLaunch struct {
	// WorkDir is the resolved, governed workspace cwd ("" ⇒ the process cwd).
	WorkDir string
	// ConfigHome / UserHome are the profile's canonical homes. A driver uses them
	// to describe itself; the runtime, not the driver, sets them on the child.
	ConfigHome string
	UserHome   string
	// Model / Effort are OPEN strings passed through to the provider, which
	// validates them. No common enum is invented across providers.
	Model  string
	Effort string
}

// DriverHandshake is what an owned handshake PROVED about this launch. It is the
// only thing allowed to nominate the provider conversation.
type DriverHandshake struct {
	// ConversationID is the id carried by the correlated root response to this
	// launch's own start/resume request. Empty is a failed nomination.
	ConversationID string
	// AuthState is the authentication readiness the provider itself reported
	// (unknown | required | ready). It is NOT derived from a home path.
	AuthState string
}

// DriverSessionConfig is everything a driver session needs from the runtime. The
// runtime supplies the transport (Send), the clock and the launch terms; the
// driver supplies the protocol.
type DriverSessionConfig struct {
	// Send writes one NDJSON line to the owned child's stdin.
	Send func(context.Context, []byte) error
	// Warn reports a non-fatal protocol observation. Never given provider text
	// that could carry a credential echo.
	Warn func(msg string, args ...any)

	// ClientName / ClientVersion identify Olivares to the provider. The name is a
	// stable product id; the version is the build's, never invented.
	ClientName    string
	ClientVersion string

	// The launch terms. WorkDir is the governed workspace cwd.
	WorkDir string
	Model   string
	Effort  string

	// ResumeConversationID is the EXACT stored conversation this launch continues.
	// Non-empty makes the handshake a resume, and a failed resume is a REFUSAL:
	// a driver never falls back to starting a different conversation.
	ResumeConversationID string

	// AuthSource is the AUTHORIZED authentication source of this launch's profile
	// (runtime_provider_auth.go), copied from the persisted launch facts. It is a
	// NON-SECRET label and it is here for one reason: a protocol whose handshake
	// ADVERTISES authentication methods has to pick one that matches what the
	// operator actually authorized, and picking it from the advertisement alone
	// would let the provider choose the source. Empty authorizes none.
	//
	// No credential value ever reaches this struct. The material — when there is
	// any at all — travels in the child's environment, which the runtime builds and
	// the driver never sees.
	AuthSource string

	// CallTimeout bounds one client→server request. ApprovalDeadline bounds one
	// server→client approval before its method-specific timeout refusal is sent.
	CallTimeout      time.Duration
	ApprovalDeadline time.Duration

	// Approve is the governed approval authority for provider requests. It is
	// DENY-CLOSED by default: without a wired gate the driver refuses every
	// approval with the method's own refusal codec rather than granting one.
	Approve func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error)

	// OnAuthState is called when the provider's own readiness CHANGES after the
	// handshake, so a later authentication failure invalidates what the row says
	// instead of leaving a launch-time answer standing for ever.
	OnAuthState func(state string)

	// AuthorityCheck re-proves the launch's exact durable authority immediately
	// before an answer crosses to the child. It is the driver's only way to ask
	// "am I still the owner?", and the runtime answers with the store, not with
	// memory. Nil (a protocol-level unit test with no runtime behind it) skips it;
	// every launch the runtime builds supplies it.
	AuthorityCheck func(ctx context.Context) error

	// Attribution carried into an approval request (references only).
	RunRef     string
	ProfileRef string
}

// DriverSession is one owned protocol conversation over one owned child process.
//
// Deliver is called from the bridge goroutine for every stdout line and must not
// block on a durable write: it correlates responses and dispatches server
// requests, and nothing else. Everything durable is deferred by the runtime until
// the reservation commits.
type DriverSession interface {
	// Deliver hands one output frame to the protocol. It performs no durable write.
	Deliver(frame OutputFrame)
	// Handshake initializes the child, reads authentication readiness, and starts
	// or EXACTLY resumes the conversation this launch owns.
	Handshake(ctx context.Context) (DriverHandshake, error)
	// Input starts a turn, or steers the active one. It refuses when neither is
	// possible rather than silently opening a second turn.
	//
	// The bool is the UNCERTAINTY boundary: false means the input was refused
	// before any byte could reach the child, true means a frame was written and the
	// outcome of the effect is whatever the error says — including "unknown". A
	// work-fenced caller records that distinction durably, so collapsing the two
	// would turn an ambiguous write into a clean failure.
	Input(ctx context.Context, text string) (attempted bool, err error)
	// Interrupt cancels the ACTIVE TURN and leaves the process usable. It is not
	// a stop.
	//
	// The bool is the same UNCERTAINTY boundary as Input, and it is here because a
	// work-fenced interrupt has to record a refusal as a refusal. "There is no
	// active turn" and "this conversation is not bound yet" are decisions taken
	// with nothing on the wire; reporting them as an attempt would durably file a
	// perfectly clean refusal as an ambiguity, and an UNKNOWN that nobody can
	// resolve is the most expensive answer this contract has.
	Interrupt(ctx context.Context) (attempted bool, err error)
	// Shutdown is the BOUNDED graceful half of a terminal stop: cancel an
	// in-flight turn and release the conversation. It never ends the OS process —
	// the process-group teardown does that, and it is the caller's next act.
	Shutdown(ctx context.Context)
	// AuthState is the last readiness the provider reported.
	AuthState() string
	// ActiveTurn is the in-flight turn id ("" when idle).
	ActiveTurn() string
	// ConversationID is the nominated conversation ("" until the root response).
	ConversationID() string
	// Close fails every in-flight waiter after the child is gone.
	Close(err error)
}

// ProviderDriver is the extensible internal driver interface. Registering one is
// what makes its profiles LAUNCHABLE on this node: readiness is per driver, never
// a single shared flag (RATIFIED §3).
type ProviderDriver interface {
	// Key is the normalized driver key this driver operates.
	Key() string
	// ConfigHomeEnv is the environment variable that selects the provider's OWN
	// configuration home on the child (Codex `CODEX_HOME`).
	ConfigHomeEnv() string
	// DefaultProgram is the official executable this driver spawns.
	DefaultProgram() string
	// LaunchArgs builds the fully-formed argv of an OWNED operate spawn. It never
	// returns a daemon, remote, attach or adoption form.
	LaunchArgs(DriverLaunch) []string
	// OpenSession builds the protocol session for one owned child.
	OpenSession(DriverSessionConfig) DriverSession
}

// DriverTransportProfile is what a driver says about the CHANNEL its owned child
// speaks. It is a description of an existing contract, never a capability
// negotiation: the values are the driver's own protocol, whether Olivares
// bridges the I/O in both directions, and what an operator's input looks like on
// the way in (a raw NDJSON line for the Claude stream-json child, a turn of text
// for a driver that owns an RPC protocol).
type DriverTransportProfile struct {
	// Protocol names the wire contract of this driver's owned child.
	Protocol string
	// IO is whether Olivares bridges the child's I/O or only its lifecycle.
	IO string
	// Input is the shape of one operator input this driver accepts.
	Input string
}

// ProviderDriverTransport is the OPTIONAL half of a driver that can describe its
// own transport. It is optional for the same reason ProviderDriverLaunchEnv is:
// adding a required method would force every driver — including ones outside
// this repository — to change in order for a READ to work.
//
// ⛔ A DRIVER THAT DOES NOT IMPLEMENT IT IS `unknown`, NOT A GUESS. Nothing
// infers "this must speak the Codex app-server protocol" or "this must support
// remote-control" from a driver key, a type name or a resemblance; the readiness
// read publishes unknown and says so.
type ProviderDriverTransport interface {
	TransportProfile() DriverTransportProfile
}

// ProviderDriverLaunchEnv is the OPTIONAL half of a driver whose official CLI is
// governed by an environment variable rather than by argv — today, pinning the
// child's version by disabling its background updater.
//
// It is an optional interface, not a new required method, so a driver that does
// not implement it (Codex) builds exactly the environment it built before. The
// runtime, not the driver, decides what is acceptable: a name the provider
// PROFILE owns is dropped, because a driver that could re-point a home would
// undo the one thing the profile exists to fix.
type ProviderDriverLaunchEnv interface {
	// LaunchEnv is the explicit, NON-SECRET environment this driver's own child
	// needs. It is never a credential: authentication is resolved separately
	// (runtime_provider_auth.go) and a driver never sees a credential value.
	LaunchEnv(DriverLaunch) []EnvVar
}

// ---------------------------------------------------------------------------
// Registry.
// ---------------------------------------------------------------------------

// registerDriver adds a driver to the runtime's operable set.
func (rt *runtimeState) registerDriver(d ProviderDriver) error {
	if d == nil {
		return errors.New("sessions: nil provider driver")
	}
	key, err := normalizeDriverKey(d.Key())
	if err != nil {
		return err
	}
	if key == providerDriverClaude {
		// The Claude path is the historical frame-driven runtime, not a registered
		// driver. Letting a driver shadow it would silently change a shipped path.
		return errors.New("sessions: the claude operate path is not a registered driver")
	}
	if rt.drivers == nil {
		rt.drivers = map[string]ProviderDriver{}
	}
	rt.drivers[key] = d
	return nil
}

// driverFor returns the registered driver for a key.
func (m *Module) driverFor(key string) (ProviderDriver, bool) {
	d, ok := m.rt.drivers[key]
	return d, ok
}

// driverProgram is the executable a driver is spawned as: the operator's explicit
// override when configured, otherwise the driver's own official program name.
func (m *Module) driverProgram(d ProviderDriver) string {
	if p := m.rt.driverPrograms[d.Key()]; p != "" {
		return p
	}
	return d.DefaultProgram()
}

// launchDriverKey is the driver a set of launch params runs under. An unprofiled
// launch is the historical Claude path.
func launchDriverKey(p CreateRunParams) string {
	if p.ProviderHome != nil && p.ProviderHome.Driver != "" {
		return p.ProviderHome.Driver
	}
	return providerDriverClaude
}

// ---------------------------------------------------------------------------
// The owned bidirectional NDJSON JSON-RPC pump.
// ---------------------------------------------------------------------------

// errRPCClosed is returned to every waiter once the owned child is gone.
var errRPCClosed = errors.New("sessions: the provider protocol channel closed")

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return "provider protocol error " + strconv.Itoa(e.Code)
}

// rpcFrame is the union of the four NDJSON frame shapes. `id` is kept RAW because
// the wire type is string OR integer (RequestId.json) and correlation must not
// depend on which one the provider chose.
type rpcFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// rpcConn is the owned bidirectional NDJSON pump over ONE child's stdio. It is
// deliberately transport-only: it knows nothing about threads, turns or
// approvals, so a second driver (Grok ACP) reuses it unchanged apart from the
// `jsonrpc` envelope bit, which is the one place the two protocols differ on the
// wire (Codex app-server omits it; ACP requires it).
type rpcConn struct {
	send    func(context.Context, []byte) error
	jsonrpc bool

	// onRequest / onNotify are the driver's dispatch. Both are called from the
	// bridge goroutine and must not block.
	onRequest func(id json.RawMessage, method string, params json.RawMessage)
	onNotify  func(method string, params json.RawMessage)

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan rpcResult
	// hooks are the per-request in-order callbacks (see callInOrder). They run on
	// the pump goroutine, so the state a response establishes is published before
	// the next line of the same stream is judged against it.
	hooks map[string]func(json.RawMessage)
	// terminal are the per-request callbacks of a DISPATCHED request — one whose
	// correlated response nobody is blocked on (see dispatch).
	terminal map[string]func(json.RawMessage, error)
	closed   bool
	closeErr error
}

func newRPCConn(send func(context.Context, []byte) error, jsonrpc bool) *rpcConn {
	return &rpcConn{
		send: send, jsonrpc: jsonrpc,
		pending: map[string]chan rpcResult{}, hooks: map[string]func(json.RawMessage){},
		terminal: map[string]func(json.RawMessage, error){},
	}
}

// requestOut is one client→server request. `jsonrpc` is emitted only for the
// protocols that carry it; Codex app-server does NOT (JSONRPCRequest.json
// requires exactly `id` and `method`).
type requestOut struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notificationOut struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type responseOut struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorOut struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

func (c *rpcConn) envelope() string {
	if c.jsonrpc {
		return "2.0"
	}
	return ""
}

// call sends one request and waits for ITS correlated response. The returned
// result is the root response and nothing else: no notification can satisfy it.
func (c *rpcConn) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	return c.callInOrder(ctx, method, params, timeout, nil)
}

// callInOrder is call with a hook that runs ON THE PUMP GOROUTINE the moment the
// correlated response is decoded, BEFORE the next line of the stream is touched.
//
// ⛔ IT EXISTS BECAUSE ORDER IS THE ANSWER AND WAITING IS NOT. A provider is
// entitled to send a request immediately after the response that authorises it —
// the fixture peer does, and so does the real CLI. If the state that response
// establishes is published by the CALLER goroutine after `call` returns, the pump
// has already parsed the next line, and a perfectly valid same-turn request is
// judged against a turn nobody had recorded yet. The review measured that:
// ten of ten runs cancelled a valid request and the authority gate saw zero calls.
//
// The fix cannot be a sleep, a retry or a permissive fallback — those trade a
// false refusal for a false grant. It has to be ordering, and the only place with
// the order is the pump itself. The hook must therefore be cheap and must never
// block: it decodes and records, nothing else.
func (c *rpcConn) callInOrder(
	ctx context.Context,
	method string,
	params any,
	timeout time.Duration,
	inOrder func(json.RawMessage),
) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		return nil, err
	}
	c.nextID++
	id := c.nextID
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcResult, 1)
	c.pending[key] = ch
	if inOrder != nil {
		c.hooks[key] = inOrder
	}
	c.mu.Unlock()

	line, err := json.Marshal(requestOut{JSONRPC: c.envelope(), ID: id, Method: method, Params: params})
	if err != nil {
		c.forget(key)
		return nil, err
	}
	if err := c.send(ctx, line); err != nil {
		c.forget(key)
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultDriverCallTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.result, res.err
	case <-ctx.Done():
		c.forget(key)
		return nil, ctx.Err()
	case <-timer.C:
		c.forget(key)
		return nil, &runErr{http.StatusGatewayTimeout, "the provider did not answer " + method + " within the bounded deadline"}
	}
}

// dispatch sends one request and returns AS SOON AS THE BYTES ARE OUT, handing
// its correlated response to a terminal callback instead of blocking a caller on
// it. It returns the correlation key, which is this request's identity for as
// long as it is in flight.
//
// ⛔ IT EXISTS BECAUSE ONE PROTOCOL'S REQUEST IS ANOTHER'S RECEIPT. Codex's
// turn/start answers "the turn started" and `call` is exactly right for it. ACP's
// session/prompt answers "the turn FINISHED": its correlated response carries the
// stopReason and arrives when the model is done, which is a model turn, not a
// bounded protocol round trip. Waiting for it under the 30-second call timeout
// would misreport every normal turn as a gateway timeout; waiting for it without
// one would hold the per-run operation lock — and therefore /input, /interrupt,
// /stop and /resume — for the whole turn. Neither is a session anybody can drive.
//
// So the wait is REMOVED, not lengthened, and the two facts stay separate: the
// caller learns whether a frame was written, and the completion resolves later on
// the pump. The 30-second bound still governs the DISPATCH (a stdin that will not
// take the line), never the model's thinking time.
//
// `register` runs UNDER THE PUMP'S OWN MUTEX, and that is what makes this safe:
// deliver→resolve needs the same mutex, so nothing this callback records can be
// missed by the response it is recording it for — the same ordering argument as
// callInOrder, applied to the other end of the request. It returns the terminal
// callback, which closes over the id it was just given rather than sharing a
// variable with the caller. Both must be cheap and must not block.
func (c *rpcConn) dispatch(
	ctx context.Context,
	method string,
	params any,
	register func(id string) func(json.RawMessage, error),
) (string, error) {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		return "", err
	}
	c.nextID++
	id := c.nextID
	key := strconv.FormatInt(id, 10)
	if register != nil {
		if done := register(key); done != nil {
			c.terminal[key] = done
		}
	}
	line, err := json.Marshal(requestOut{JSONRPC: c.envelope(), ID: id, Method: method, Params: params})
	c.mu.Unlock()
	if err != nil {
		c.forget(key)
		return key, err
	}
	if err := c.send(ctx, line); err != nil {
		// Nothing crossed, so the registration is withdrawn here and the caller
		// undoes whatever it recorded. Leaving it would strand a turn that never
		// started on a response that can never arrive.
		c.forget(key)
		return key, err
	}
	return key, nil
}

// notify sends one notification (no id, no answer).
func (c *rpcConn) notify(ctx context.Context, method string, params any) error {
	line, err := json.Marshal(notificationOut{JSONRPC: c.envelope(), Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.send(ctx, line)
}

// respond answers a server→client request with a result.
func (c *rpcConn) respond(ctx context.Context, id json.RawMessage, result any) error {
	line, err := json.Marshal(responseOut{JSONRPC: c.envelope(), ID: id, Result: result})
	if err != nil {
		return err
	}
	return c.send(ctx, line)
}

// respondError answers a server→client request with a protocol error. It is what
// an UNSUPPORTED request receives: never a hang, and never an approval.
func (c *rpcConn) respondError(ctx context.Context, id json.RawMessage, code int, message string) error {
	line, err := json.Marshal(errorOut{JSONRPC: c.envelope(), ID: id, Error: rpcError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	return c.send(ctx, line)
}

func (c *rpcConn) forget(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	delete(c.hooks, key)
	delete(c.terminal, key)
	c.mu.Unlock()
}

// deliver routes one inbound line. An unparsable line is IGNORED rather than
// treated as protocol: an official CLI may print a plain log line on stdout, and
// killing the pump for it would turn a cosmetic difference into a dead session.
func (c *rpcConn) deliver(data []byte) {
	var f rpcFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	hasID := len(f.ID) > 0 && string(f.ID) != "null"
	switch {
	case hasID && f.Method == "":
		c.resolve(f)
	case hasID && f.Method != "":
		if c.onRequest != nil {
			c.onRequest(append(json.RawMessage(nil), f.ID...), f.Method, f.Params)
		}
	case !hasID && f.Method != "":
		if c.onNotify != nil {
			c.onNotify(f.Method, f.Params)
		}
	}
}

func (c *rpcConn) resolve(f rpcFrame) {
	key := string(f.ID)
	c.mu.Lock()
	ch, ok := c.pending[key]
	hook := c.hooks[key]
	done := c.terminal[key]
	if ok || done != nil {
		delete(c.pending, key)
		delete(c.hooks, key)
		delete(c.terminal, key)
	}
	c.mu.Unlock()
	if !ok && done == nil {
		return // not ours: a stale id, or an answer to another client's request
	}
	if done != nil {
		// A dispatched request: its terminal callback IS the waiter, and it runs here
		// on the pump so what the response ends is ended before the next line of the
		// same stream is judged against it.
		if f.Error != nil {
			done(nil, f.Error)
			return
		}
		done(f.Result, nil)
		return
	}
	if f.Error != nil {
		ch <- rpcResult{err: f.Error}
		return
	}
	// The hook runs HERE — still on the pump, still before deliver() returns and
	// the next line is read — and only then is the waiter released.
	if hook != nil {
		hook(f.Result)
	}
	ch <- rpcResult{result: f.Result}
}

// close fails every waiter exactly once. Called when the owned child's output
// channel closes, so a handshake or a turn cannot hang on a dead process.
func (c *rpcConn) close(err error) {
	if err == nil {
		err = errRPCClosed
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.closeErr = true, err
	pending := c.pending
	terminal := c.terminal
	c.pending = map[string]chan rpcResult{}
	c.hooks = map[string]func(json.RawMessage){}
	c.terminal = map[string]func(json.RawMessage, error){}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResult{err: err}
	}
	// A dispatched request has no waiter to release, so its callback is the ONLY
	// thing that can end the turn it opened. Skipping it here is how a dead child
	// leaves a session convinced a turn is still in flight for ever.
	for _, done := range terminal {
		done(nil, err)
	}
}

// defaultDriverCallTimeout bounds one client→server request. A handshake that
// cannot be answered within it is a failed launch, not a hung one.
const defaultDriverCallTimeout = 30 * time.Second

// defaultDriverApprovalDeadline bounds one server→client approval before the
// method's own timeout refusal is sent. A provider that gets no answer stalls its
// turn forever; a bounded refusal is the honest outcome.
const defaultDriverApprovalDeadline = 30 * time.Second

// ---------------------------------------------------------------------------
// Runtime glue.
// ---------------------------------------------------------------------------

// attachDriverSession attaches the owned protocol session when this launch runs
// under a REGISTERED driver, and does nothing at all on the historical Claude
// path — that nil session is what keeps the shipped path unchanged.
//
// It is called BEFORE the bridge starts pumping, so no frame can arrive with no
// session to receive it.
func (m *Module) attachDriverSession(lr *liveRun, p CreateRunParams, workDir, resumeID string) {
	d, ok := m.driverFor(launchDriverKey(p))
	if !ok {
		return
	}
	m.openDriverSession(lr, d, p, workDir, resumeID)
}

// openDriverSession builds the driver's protocol session for one owned child.
func (m *Module) openDriverSession(lr *liveRun, d ProviderDriver, p CreateRunParams, workDir, resumeID string) {
	tenant := lr.tenant
	profileRef, authSource := "", ""
	if lr.profile != nil {
		profileRef = lr.profile.ProfileID
		// The snapshot's own source, which the run row persisted BEFORE the spawn.
		// Re-reading the profile here would answer with a re-authorization the running
		// child never launched under.
		authSource = lr.profile.AuthSource
	}
	lr.driver = d
	lr.session = d.OpenSession(DriverSessionConfig{
		Send:                 func(ctx context.Context, line []byte) error { return lr.proc.Send(ctx, line) },
		Warn:                 m.warnf,
		ClientName:           driverClientName,
		ClientVersion:        m.productVersion(),
		WorkDir:              workDir,
		Model:                p.Model,
		Effort:               p.Effort,
		ResumeConversationID: resumeID,
		AuthSource:           authSource,
		CallTimeout:          m.rt.driverCallTimeout,
		ApprovalDeadline:     m.rt.driverApprovalDeadline,
		RunRef:               lr.runRef,
		ProfileRef:           profileRef,
		Approve: func(ctx context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return m.rt.approvalGate.Approve(ctx, tenant, req)
		},
		AuthorityCheck: func(ctx context.Context) error { return m.assertRunAuthority(ctx, lr) },
		OnAuthState: func(state string) {
			// Through the reservation window like every other row effect, and with a
			// context of its own: this can arrive from the pump at any moment,
			// including before the launch transition commits.
			m.deferDurable(lr, func() {
				m.recordProviderAuthState(context.Background(), lr, state)
			})
		},
	})
}

// driverClientName is the stable product id Olivares presents to an official CLI.
// It is a product identity, never a per-session value.
const driverClientName = "olivares"

// productVersion is the build's version as presented to a provider handshake. It
// is the composition root's value; the module invents none.
func (m *Module) productVersion() string {
	if v := m.rt.productVersion; v != "" {
		return v
	}
	return "0.0.0-dev"
}

// runDriverHandshake performs the owned handshake and, on success, nominates the
// conversation for THIS launch.
//
// The nomination is deliberately NOT written here. It goes through the same
// reservation window every other row effect goes through: a root response that
// arrives before the launch transition commits is queued and applied after it, so
// a rolled-back launch binds no alias, and the retry stays idempotent because the
// captured flag is set only after the store confirms (runtime_profile.go).
func (m *Module) runDriverHandshake(ctx context.Context, lr *liveRun) error {
	hs, err := lr.session.Handshake(ctx)
	if err != nil {
		return err
	}
	if hs.ConversationID == "" {
		return &runErr{http.StatusBadGateway, "the provider did not nominate a conversation for this launch"}
	}
	lr.mu.Lock()
	lr.conversationID = hs.ConversationID
	lr.authState = hs.AuthState
	lr.mu.Unlock()
	// ⛔ THE DEFERRED EFFECT MUST NOT CARRY THE HANDSHAKE'S CONTEXT. This ctx is
	// bounded by the handshake budget and is cancelled the moment finishDriverLaunch
	// returns — which is BEFORE the reservation window is flushed, because that is
	// the whole point of the window. Capturing it wrote a nomination the plane knew
	// and the database never received, silently: mutateRunBest swallows its error by
	// contract, so the readiness column simply stayed NULL while everything else
	// looked correct. Measured, not reasoned about.
	// The readiness itself is NOT written here: the session publishes every change
	// through OnAuthState, and the handshake's own reading is the first of them.
	// Writing it twice would be two row versions for one fact.
	dctx := context.WithoutCancel(ctx)
	m.deferDurable(lr, func() {
		m.captureSessionID(dctx, lr, hs.ConversationID, m.now())
	})
	return nil
}

// finishDriverLaunch runs the owned handshake of a driver launch under a bounded
// wall-clock budget. A silent app-server therefore bounds the launch instead of
// holding the create/resume request open forever.
//
// It is a no-op on the Claude path: there is no handshake to run, and inventing
// one would change a shipped launch.
func (m *Module) finishDriverLaunch(ctx context.Context, lr *liveRun) error {
	if lr.session == nil {
		return nil
	}
	hctx, cancel := context.WithTimeout(ctx, driverHandshakeBudget)
	defer cancel()
	return m.runDriverHandshake(hctx, lr)
}

// driverHandshakeBudget is the wall-clock ceiling of a whole owned handshake.
const driverHandshakeBudget = 90 * time.Second

// awaitFinalize waits, BOUNDED, for the bridge to settle a run's row after its
// process was told to end. It is the same wait stopRun already performs and for
// the same reason: returning before the row settles reports a half-torn-down run
// as if it were the run's state.
func (m *Module) awaitFinalize(lr *liveRun) {
	select {
	case <-lr.finalizedCh:
	case <-time.After(2 * m.rt.waitDelay):
	}
}

// settleDriverLaunch closes the reservation window and re-reads the row, so a
// driver launch ANSWERS with the conversation it just proved it owns.
//
// The window is flushed here rather than only by the caller's defer because a
// driver nomination is SYNCHRONOUS — the root response arrived during the
// handshake — and returning a launch that omits it would report less than the
// runtime knows. The flush is idempotent, so the caller's defer stays as the
// guarantee for every path that does not reach this point.
func (m *Module) settleDriverLaunch(ctx context.Context, lr *liveRun, rec model.Record) model.Record {
	if lr.session == nil {
		return rec
	}
	lr.cierraVentanaYVuelca()
	updated, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		return rec
	}
	return updated
}

// deferDurable applies fn under the reservation window's rules: queued while the
// window is open, otherwise applied directly under the same mutex the flush uses,
// so a late effect cannot overtake a queued one.
func (m *Module) deferDurable(lr *liveRun, fn func()) {
	if lr.difiereSiLaReservaSigueAbierta(fn) {
		return
	}
	lr.aplicaMu.Lock()
	defer lr.aplicaMu.Unlock()
	fn()
}

// retryDriverCapture re-attempts the transactional capture of an already
// nominated conversation. The bridge calls it on any later frame, which is what
// makes a store failure at commit time converge instead of stranding a run whose
// provider id the plane knows but never persisted.
func (m *Module) retryDriverCapture(ctx context.Context, lr *liveRun, at time.Time) bool {
	lr.mu.Lock()
	id, captured := lr.conversationID, lr.sessionIDCaptured
	lr.mu.Unlock()
	if id == "" || captured {
		return false
	}
	m.captureSessionID(ctx, lr, id, at)
	return true
}

// recordProviderAuthState persists the readiness the provider itself reported. It
// is a non-secret label on the run row, independent of process, conversation and
// turn state (CORRECTED-CONTRACT §7).
func (m *Module) recordProviderAuthState(ctx context.Context, lr *liveRun, state string) {
	if state == "" {
		return
	}
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		rec[colRunProviderAuthState] = state
	})
}

// driverInput routes one operator input to the owned conversation. A driver run
// never accepts a RAW protocol line: the child is an owned RPC peer, and letting
// a caller write arbitrary frames onto its stdin would hand it the whole method
// surface, approvals included.
//
// The returned bool is the uncertainty boundary — see DriverSession.Input. Every
// refusal ABOVE the driver returns false, because none of them has written
// anything; the driver decides the rest.
func (m *Module) driverInput(ctx context.Context, lr *liveRun, text string) (bool, error) {
	if lr.session == nil {
		return false, conflictErr("this session has no provider protocol driver")
	}
	if state := lr.session.AuthState(); state == AuthStateRequired {
		return false, &runErr{http.StatusConflict, "the provider reports that this profile is not authenticated (auth_required); a turn cannot be started"}
	}
	// The LAST thing before the bytes: the durable authority, re-proved with the
	// claim row in the transaction's write set. A takeover that commits between
	// here and the write loses the CAS, not the race.
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return false, err
	}
	return lr.session.Input(ctx, text)
}

// interruptDriverTurn cancels the ACTIVE provider turn and leaves the owned
// process alive and usable. It is not a stop and never becomes one.
//
// The returned bool is the uncertainty boundary — see DriverSession.Interrupt.
// Every refusal ABOVE the driver returns false, because none of them has written
// anything, and losing the durable authority here is exactly such a refusal: the
// store said no before a byte moved. The driver decides the rest.
func (m *Module) interruptDriverTurn(ctx context.Context, lr *liveRun) (bool, error) {
	if lr.session == nil {
		return false, conflictErr("this session has no provider protocol driver to interrupt")
	}
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return false, err
	}
	return lr.session.Interrupt(ctx)
}

// closeDriverSession releases every in-flight protocol waiter once the owned
// child is gone.
func (m *Module) closeDriverSession(lr *liveRun) {
	if lr.session != nil {
		lr.session.Close(fmt.Errorf("%w: the owned provider process exited", errRPCClosed))
	}
}
