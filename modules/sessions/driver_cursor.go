// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// The official Cursor CLI driver (`agent acp`, 2026.10.22-27dcf5f), over ACP on the owned
// child's stdio. The wire, the permission request and the turn are the shared ACP session's
// (acp_session.go, acp_conversation.go, acp_permission.go, acp_settings.go); this file keeps
// what is Cursor's own.
//
// AUTHENTICATION. initialize advertises one method, cursor_login, and the CLI answers
// session/new with -32000 until `authenticate` has used the login the profile's home holds
// (written by `agent login`, run through AI tools). Without a stored login `authenticate`
// can only open a browser, which this host never does (NO_OPEN_BROWSER): it answers
// -32602 with a one-time link in its data, which is "sign in first" and is never repeated
// to a client. No credential is passed on the protocol, on argv or in this driver's
// environment: the login stays in the home (AGENT_CLI_CREDENTIAL_STORE=file).
//
// MODE. Every launch sets the mode explicitly. read-only runs Cursor's `ask` mode
// ("read-only exploration without making changes"); every other preset runs `agent`,
// which asks for each tool, and the session's live policy answers each ask.
//
// ponytail: the signed-in half of this driver (session/new, set_mode, prompt, the shape of a
// permission request) follows the vendor's server code and the standard ACP shapes, and was
// run against a fake peer only: no Cursor account existed to run it. Upgrade trigger: the
// first run with a signed-in account; until then a permission request with nothing the
// policy can review is refused.
//
// NO PROVIDER RECORD. Cursor runs on the account's own plan and models; no provider kind
// binds it (driverfacts), so a launch bound to a record is refused rather than ignored.

const providerDriverCursor = "cursor"

// cursorAuthMethod is the one authentication method `agent acp` advertises.
const cursorAuthMethod = "cursor_login"

const (
	cursorMethodAuthenticate = "authenticate"
	cursorModeAgent          = "agent"
	cursorModeAsk            = "ask"
)

// cursorOwnLoginHosts are Cursor's production apiUrl, metricsUrl and websiteUrl, in
// that order, from the official package's src/utils/service-urls.ts (2026.10.22-27dcf5f).
// The first carries the account credential; the others are unkeyed public service calls.
var cursorOwnLoginHosts = [...]string{"https://api2.cursor.sh", "https://api3.cursor.sh", "https://cursor.com"}

type cursorDriver struct{}

// NewCursorDriver returns the official Cursor CLI driver.
func NewCursorDriver() ProviderDriver { return cursorDriver{} }

func (cursorDriver) Key() string { return providerDriverCursor }
func (cursorDriver) ConfigHomeEnv() string {
	facts, _ := driverfacts.Lookup(providerDriverCursor)
	return facts.ConfigHomeEnv
}
func (cursorDriver) DefaultProgram() string {
	facts, _ := driverfacts.Lookup(providerDriverCursor)
	return facts.Program
}

// LaunchArgs is the ONLY operate form. The model and the mode travel on the protocol
// after session/new, never on argv.
func (cursorDriver) LaunchArgs(DriverLaunch) []string { return []string{"acp"} }

func (cursorDriver) TransportProfile() DriverTransportProfile {
	return DriverTransportProfile{Protocol: protocolCursorACP, IO: ioBidirectional, Input: inputText}
}

// LaunchTerms: the model rides on session/set_model as an exact value the CLI offered, and
// the permission preset on session/set_mode. The CLI has no effort control. The models are
// the CLI's own offered choices for the account, so provider model discovery does not
// select them.
func (cursorDriver) LaunchTerms() DriverLaunchTerms {
	return DriverLaunchTerms{
		Model:          TermCarried,
		Effort:         TermNotCarried,
		PermissionMode: TermCarried,
		ModelDiscovery: ModelDiscoveryNone,
	}
}

// LaunchEnv keeps the login inside the profile's home and the browser off this host. Neither
// is a credential.
func (cursorDriver) LaunchEnv(DriverLaunch) []EnvVar {
	return []EnvVar{{Name: "AGENT_CLI_CREDENTIAL_STORE", Value: "file"}, {Name: "NO_OPEN_BROWSER", Value: "1"}}
}

func (cursorDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &cursorSession{acpSession: newACPSession(cfg, providerDriverCursor)}
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

type cursorSession struct {
	acpSession
	caps acpAgentCapabilities
}

func (s *cursorSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	if s.cfg.BoundProvider.Kind != "" {
		return DriverHandshake{}, &runErr{http.StatusConflict, "Cursor CLI runs on a Cursor account; a provider record cannot be bound to it"}
	}
	init, err := s.initialize(ctx)
	if err != nil {
		return DriverHandshake{}, err
	}
	if err := acpRequireSessionMCP("Cursor CLI", s.cfg.sessionMCP, init.AgentCapabilities.MCP.HTTP); err != nil {
		return DriverHandshake{}, err
	}
	s.mu.Lock()
	s.caps = init.AgentCapabilities
	s.mu.Unlock()
	// Readiness starts unknown and stays so after a successful authenticate: that is the
	// CLI's step, and only its own answer that no login exists moves readiness to required.
	s.setAuthState(AuthStateUnknown)
	if err := s.authenticate(ctx, init); err != nil {
		return DriverHandshake{}, err
	}
	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" {
		s.mu.Lock()
		loadable := s.caps.LoadSession
		s.mu.Unlock()
		return s.resumeConversation(ctx, resume, loadable, s.honorSettings)
	}
	return s.newConversation(ctx, s.honorSettings)
}

// authenticate uses the stored login through the method the CLI advertised. A tool that
// offers no cursor_login is refused before anything is sent.
func (s *cursorSession) authenticate(ctx context.Context, init acpInitializeResponse) error {
	offered := false
	for _, m := range init.AuthMethods {
		offered = offered || m.ID == cursorAuthMethod
	}
	if !offered {
		return &runErr{http.StatusUnprocessableEntity, "Cursor CLI did not offer its account login; the session did not start"}
	}
	_, err := s.conn.call(ctx, cursorMethodAuthenticate, map[string]string{"methodId": cursorAuthMethod}, s.cfg.CallTimeout)
	if err == nil {
		return nil
	}
	var re *rpcError
	if errors.As(err, &re) {
		// The code only: the error's data carries the one-time login link.
		s.warn("sessions: Cursor CLI could not authenticate", "run_ref", s.cfg.RunRef, "code", re.Code)
		if cursorNeedsLogin(re) {
			s.setAuthState(AuthStateRequired)
			return &runErr{
				http.StatusUnprocessableEntity,
				"the provider refused to open a conversation because this profile is not authenticated (auth_required)",
			}
		}
	}
	return acpHandshakeErr(cursorMethodAuthenticate, err)
}

// cursorNeedsLogin reports the CLI's answer that no login is stored: its own authentication
// code, or the invalid-params answer that carries the browser link it could not open (measured
// on 2026.10.22-27dcf5f). Any other invalid-params answer is a protocol error, not a login state.
func cursorNeedsLogin(re *rpcError) bool {
	if re.Code == acpErrAuthRequired {
		return true
	}
	return re.Code == acpErrInvalidParams && bytes.Contains(bytes.ToLower(append(append([]byte{}, re.Message...), re.Data...)), []byte("browser"))
}

// cursorLoginLink is the one-time browser link the CLI prints when it cannot open a browser:
// whoever holds it can approve that login with their own account, so it never leaves the child
// through the attach stream or the recorder.
var cursorLoginLink = regexp.MustCompile(`https?://[^\s"\\]*loginDeepControl[^\s"\\]*`)

// withholdCursorLoginLink replaces that link wherever a line of the child's output carries it.
func withholdCursorLoginLink(data []byte) []byte {
	return cursorLoginLink.ReplaceAll(data, []byte("[login link withheld]"))
}

// honorSettings applies the mode, then the model when the launch names one.
func (s *cursorSession) honorSettings(ctx context.Context, offered acpSessionResult) error {
	return s.applyModeAndModel(ctx, "Cursor CLI", cursorModeFor(s.cfg.Preset), offered)
}

// cursorModeFor is the mode a launch preset runs. Only read-only differs.
func cursorModeFor(preset string) string {
	if preset == PresetReadOnly {
		return cursorModeAsk
	}
	return cursorModeAgent
}

func (s *cursorSession) Shutdown(ctx context.Context) {
	// The CLI advertises no session/close, so a stop is a cancel and the child's end.
	s.shutdown(ctx, false)
}
