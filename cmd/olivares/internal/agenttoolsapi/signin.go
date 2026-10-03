// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Signing a tool in runs the tool's OWN login on this node, as the engine's OS
// user, with the tool's standard home: Claude Code `auth login --claudeai`
// (subscription; the browser shows a code the operator pastes back), and Codex
// and Grok Build `login --device-auth` (a link and a one-time code). The login is
// stored where the tool itself keeps it (~/.claude, ~/.codex, ~/.grok), which is
// the home a profile
// with auth_source provider_account_home and no named home runs under. Nothing
// here reads, copies or returns a credential: the page relays the link and the
// code, and the status read asks the tool itself.
//
// Docs: code.claude.com/docs/en/authentication (paste-code login for remote
// machines, credentials in .credentials.json under the config directory) and
// learn.chatgpt.com/docs/auth (codex login --device-auth, CODEX_HOME). Grok Build
// documents its device login in its own binary (1.0.46: "grok login --device-auth
// (or --device-code): no browser needed on the target machine"; the session token
// in ~/.grok/auth.json, refreshed by signing in again after 7 days).

const (
	signInTimeout   = 15 * time.Minute
	statusTimeout   = 15 * time.Second
	maxSignInOutput = 64 << 10
	maxPastedCode   = 2048
)

// SignIn is one login in progress. It never carries the pasted code.
type SignIn struct {
	ID         model.ID `json:"id"`
	Driver     string   `json:"driver"`
	State      string   `json:"state"`
	URL        string   `json:"url,omitempty"`
	UserCode   string   `json:"user_code,omitempty"`
	Message    string   `json:"message,omitempty"`
	AccountRef string   `json:"account_ref,omitempty"`

	stdin     io.WriteCloser
	cancel    context.CancelFunc
	expires   time.Time
	tenant    model.TenantID
	configDir string
}

// Sign-in states. needs_code: Claude waits for the code its page shows;
// waiting: Codex waits for the operator to enter its code on the link.
const (
	signInStarting = "starting"
	signInNeedCode = "needs_code"
	signInWaiting  = "waiting"
	signInChecking = "checking"
	signInDone     = "signed_in"
	signInFailed   = "failed"
)

// SignInStatus is what the tool itself says about its login on this node.
type SignInStatus struct {
	Driver     string `json:"driver"`
	Installed  bool   `json:"installed"`
	SignedIn   bool   `json:"signed_in"`
	Method     string `json:"method,omitempty"`
	Account    string `json:"account,omitempty"`
	AccountRef string `json:"account_ref,omitempty"`
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(\x07|\x1b\\)`)
	firstURL   = regexp.MustCompile(`https://[^\s\x1b]+`)
	deviceCode = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,5}\b`)
	// Grok prints "Code: <code>"; its server's codes are [A-Z0-9-] (the binary
	// refuses any other), with or without a dash.
	labelledCode = regexp.MustCompile(`(?i)\bcode:\s*([A-Z0-9][A-Z0-9-]{3,15})\b`)
)

// deviceSignIn reports whether a tool's login is a device login (a link and a
// one-time code the operator enters there), as opposed to Claude's pasted code.
func deviceSignIn(driver string) bool { return driver == "codex" || driver == "grok" }

// userCodeIn finds a device login's one-time code in one line of its output.
func userCodeIn(driver, line string) string {
	if driver == "grok" {
		if m := labelledCode.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return deviceCode.FindString(line)
}

// SetProgramResolver tells the module which executable a driver runs on this
// node (the same answer a session launch gets). Nil means "not installed".
func (m *Module) SetProgramResolver(resolve func(driver string) string) {
	m.mu.Lock()
	m.resolveProgram = resolve
	m.mu.Unlock()
}

func (m *Module) program(driver string) string {
	m.mu.Lock()
	resolve := m.resolveProgram
	m.mu.Unlock()
	if resolve == nil {
		return ""
	}
	return resolve(driver)
}

// LoginHomeFunc says where a tenant's own login of a tool lives on this node: the
// HOME the product created for it and the tool's configuration directory inside
// (sessions.ToolLoginHome, under the data directory). It refuses a tenant this
// node does not serve.
type LoginHomeFunc func(ctx context.Context, tenant model.TenantID, driver, accountRef string) (home, configDir string, err error)

func loginHomeError(w http.ResponseWriter, err error) {
	var refusal interface{ HTTPStatusCode() int }
	if errors.As(err, &refusal) && refusal.HTTPStatusCode() >= 400 && refusal.HTTPStatusCode() < 500 {
		code := "account_login_refused"
		if refusal.HTTPStatusCode() == 404 {
			code = "not_found"
		}
		fail(w, refusal.HTTPStatusCode(), code, err.Error())
		return
	}
	fail(w, 503, "unavailable", "This node cannot use the tool's login home.")
}

// SetLoginHome wires where the tools' own logins live (the composition root).
func (m *Module) SetLoginHome(f LoginHomeFunc) {
	m.mu.Lock()
	m.loginHome = f
	m.mu.Unlock()
}

// toolEnv is the child's whole environment: the tenant's login home the product
// created, PATH, and the tool's configuration directory inside that home, and
// nothing else. FH 036: it was the ENGINE USER's home (~/.claude), so a fresh
// install read, and would have used, a vendor login nobody gave the product. The
// engine's own HOME, CLAUDE_CONFIG_DIR, CODEX_HOME or API keys never reach it.
func (m *Module) toolEnv(ctx context.Context, tenant model.TenantID, driver, accountRef string) (env []string, configDir string, err error) {
	m.mu.Lock()
	loginHome := m.loginHome
	m.mu.Unlock()
	if loginHome == nil {
		return nil, "", errors.New("this node has no place for the tools' own logins")
	}
	home, configDir, err := loginHome(ctx, tenant, driver, accountRef)
	if err != nil {
		return nil, "", err
	}
	for _, dir := range []string{home, configDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
	}
	env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1", "BROWSER=true"}
	switch driver {
	case "claude":
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	case "codex":
		env = append(env, "CODEX_HOME="+configDir)
	case "grok":
		env = append(env, "GROK_HOME="+configDir)
	default:
		return nil, "", errors.New("sign-in is available for Claude Code, Codex and Grok Build")
	}
	return env, configDir, nil
}

// envValue is one variable of a child environment built here.
func envValue(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// tenantOf reads the organization a sign-in or its status is for. These are system
// routes, which ignore the tenant selection, so the tenant is named explicitly.
func tenantOf(raw string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(strings.TrimSpace(raw))
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

func signInArgs(driver string) []string {
	if deviceSignIn(driver) {
		return []string{"login", "--device-auth"}
	}
	return []string{"auth", "login", "--claudeai"}
}

// readStatus asks the installed tool whether it is signed in.
func (m *Module) readStatus(ctx context.Context, tenant model.TenantID, driver, accountRef string) (SignInStatus, error) {
	out := SignInStatus{Driver: driver, AccountRef: accountRef}
	program := m.program(driver)
	if program == "" && accountRef == "" {
		return out, nil
	}
	env, configDir, err := m.toolEnv(ctx, tenant, driver, accountRef)
	if err != nil {
		return out, err
	}
	if program == "" {
		return out, nil
	}
	out.Installed = true
	if driver == "grok" {
		// Grok Build has no status command; its login is the session token file
		// `grok login` writes. Only its presence is checked: it is never opened.
		_, serr := os.Lstat(filepath.Join(configDir, "auth.json"))
		out.SignedIn = serr == nil
		if out.SignedIn {
			out.Method = "xAI account"
		}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	args := []string{"auth", "status", "--json"}
	if driver == "codex" {
		args = []string{"login", "status"}
	}
	cmd := toolCommand(ctx, program, args...)
	cmd.Env = env
	cmd.Dir = envValue(env, "HOME") // the organization's login home, as for the login
	raw, runErr := cmd.Output()
	if len(raw) > maxSignInOutput {
		raw = raw[:maxSignInOutput]
	}
	if driver == "codex" {
		text := strings.TrimSpace(ansiEscape.ReplaceAllString(string(raw), ""))
		out.SignedIn = runErr == nil && strings.HasPrefix(strings.ToLower(text), "logged in")
		if out.SignedIn {
			out.Method = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "Logged in using"), "Logged in"))
		}
		return out, nil
	}
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
	}
	if json.Unmarshal(raw, &st) == nil {
		out.SignedIn = st.LoggedIn
		if st.LoggedIn {
			out.Method, out.Account = st.AuthMethod, st.Email
		}
	}
	return out, nil
}

// LoginStatus reports whether a driver's tool is installed on this node and, for
// a tool with its own login, whether the tenant's own login is signed in: the
// answer GET sign-in gives, for the sessions module's rule that picks a new
// session's profile.
// OpenCode has no login of its own, so it is only ever installed or not.
func (m *Module) LoginStatus(ctx context.Context, tenant model.TenantID, driver string) (installed, signedIn bool, err error) {
	if !validSignInDriver(driver) {
		return m.program(driver) != "", false, nil
	}
	st, err := m.readStatus(ctx, tenant, driver, "")
	return st.Installed, st.SignedIn, err
}

func validSignInDriver(driver string) bool {
	return driver == "claude" || driver == "codex" || driver == "grok"
}

// toolCommand builds every child this module starts (the tool's login and its
// status read). It is the module's only spawn site, so the composition can
// confine it like a session child: read-write the tool's own home, read-only
// its program.
var toolCommand = exec.CommandContext

// handleSignInStatus reports what an installed Claude Code, Codex or Grok Build says
// about its own login on this node: installed, signed in, and with which account.
func (m *Module) handleSignInStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		driver := r.URL.Query().Get("driver")
		if !validSignInDriver(driver) {
			fail(w, 400, "bad_request", "Choose claude, codex or grok.")
			return
		}
		tenant, ok := tenantOf(r.URL.Query().Get("tenant_id"))
		if !ok {
			fail(w, 400, "bad_request", "Name the organization the login is for (tenant_id).")
			return
		}
		st, err := m.readStatus(r.Context(), tenant, driver, r.URL.Query().Get("account_ref"))
		if err != nil {
			loginHomeError(w, err)
			return
		}
		write(w, 200, st)
	})(w, r, mc)
}

// handleSignInStart starts the tool's own login on this node (claude auth login,
// codex or grok login --device-auth) and returns its sign-in page link, plus the
// device code for Codex and Grok Build. One login per tool runs at a time; it expires after 15 minutes.
func (m *Module) handleSignInStart(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		var in struct {
			Driver     string `json:"driver"`
			TenantID   string `json:"tenant_id"`
			AccountRef string `json:"account_ref,omitempty"`
		}
		// The module's one strict decoder: unknown fields refused, one JSON value, 4 KiB.
		if !decode(w, r, &in) {
			return
		}
		if !validSignInDriver(in.Driver) {
			fail(w, 400, "bad_request", "Choose claude, codex or grok.")
			return
		}
		tenant, ok := tenantOf(in.TenantID)
		if !ok {
			fail(w, 400, "bad_request", "Name the organization the login is for (tenant_id).")
			return
		}
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot sign a tool in.")
			return
		}
		program := m.program(in.Driver)
		if program == "" {
			fail(w, 409, "tool_not_installed", "Install the tool first, then sign in.")
			return
		}
		env, configDir, err := m.toolEnv(r.Context(), tenant, in.Driver, in.AccountRef)
		if err != nil {
			loginHomeError(w, err)
			return
		}
		s, err := m.startSignIn(tenant, in.Driver, in.AccountRef, configDir, program, env)
		if err != nil {
			fail(w, 503, "unavailable", "The tool's sign-in could not be started on this node.")
			return
		}
		meta := map[string]any{"driver": in.Driver, "tenant_id": tenant.String()}
		if in.AccountRef != "" {
			meta["account_ref"] = in.AccountRef
		}
		if err := audit(r.Context(), mc, "signin.start", s.ID, meta); err != nil {
			m.cancelSignIn(s.ID)
			unavailable(w)
			return
		}
		write(w, 202, m.waitForLink(r.Context(), s.ID))
	})(w, r, mc)
}

// handleSignInGet reads one login in progress: its state, link and device code.
func (m *Module) handleSignInGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		s, ok := m.signInView(model.ID(chi.URLParam(r, "id")))
		if !ok {
			fail(w, 404, "not_found", "This sign-in is no longer running. Start it again.")
			return
		}
		write(w, 200, s)
	})(w, r, mc)
}

// handleSignInCode hands the code shown on Claude's sign-in page to the waiting
// login and answers once the tool says whether it is signed in. The code is
// never logged or stored.
func (m *Module) handleSignInCode(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		var in struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &in) {
			return
		}
		code := strings.TrimSpace(in.Code)
		if code == "" || len(code) > maxPastedCode || strings.ContainsAny(code, "\r\n") {
			fail(w, 400, "bad_request", "Paste the code the sign-in page shows.")
			return
		}
		id := model.ID(chi.URLParam(r, "id"))
		m.mu.Lock()
		s, ok := m.signIns[id]
		var stdin io.WriteCloser
		if ok && s.State == signInNeedCode {
			stdin = s.stdin
			s.State, s.Message = signInChecking, ""
		}
		m.mu.Unlock()
		if !ok {
			fail(w, 404, "not_found", "This sign-in is no longer running. Start it again.")
			return
		}
		if stdin == nil {
			fail(w, 409, "not_waiting", "This sign-in is not waiting for a code.")
			return
		}
		if _, err := io.WriteString(stdin, code+"\n"); err != nil {
			m.finishSignIn(id, signInFailed, "The tool stopped before it read the code. Start the sign-in again.")
		}
		write(w, 202, m.waitForOutcome(r.Context(), id, 20*time.Second))
	})(w, r, mc)
}

// handleSignInCancel stops a login in progress: the tool's login process ends on
// this node and the pending login is forgotten.
func (m *Module) handleSignInCancel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		m.cancelSignIn(model.ID(chi.URLParam(r, "id")))
		write(w, 200, map[string]any{"ok": true})
	})(w, r, mc)
}

// startSignIn starts the tool's login. A new start replaces a pending login in
// the same tenant, driver and configuration home; other accounts keep theirs.
func (m *Module) startSignIn(tenant model.TenantID, driver, accountRef, configDir, program string, env []string) (*SignIn, error) {
	ctx, cancel := context.WithTimeout(m.ctx, signInTimeout)
	cmd := toolCommand(ctx, program, signInArgs(driver)...)
	cmd.Env = env
	// Cancel and expiry kill the login; its output pipe must not keep Wait waiting.
	cmd.WaitDelay = 5 * time.Second
	// The login runs IN the organization's login home, never in the engine user's
	// (SR2 on 65d841a1): the child's working directory is the HOME it was given.
	cmd.Dir = envValue(env, "HOME")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s := &SignIn{ID: model.NewID(), Driver: driver, AccountRef: accountRef, tenant: tenant, configDir: configDir, State: signInStarting, stdin: stdin, cancel: cancel, expires: time.Now().Add(signInTimeout)}
	m.mu.Lock()
	if m.signIns == nil {
		m.signIns = map[model.ID]*SignIn{}
	}
	for id, old := range m.signIns {
		if old.tenant == tenant && old.Driver == driver && old.configDir == configDir {
			old.cancel()
			delete(m.signIns, id)
		}
	}
	m.signIns[s.ID] = s
	m.mu.Unlock()

	m.wg.Add(2)
	go func() { // read the tool's output: the link, and Codex's one-time code
		defer m.wg.Done()
		scanner := bufio.NewScanner(io.LimitReader(pr, maxSignInOutput))
		for scanner.Scan() {
			line := ansiEscape.ReplaceAllString(scanner.Text(), "")
			m.mu.Lock()
			if cur, ok := m.signIns[s.ID]; ok {
				if cur.URL == "" {
					if u := firstURL.FindString(line); u != "" {
						cur.URL = strings.TrimRight(u, ".,)")
					}
				}
				if deviceSignIn(driver) && cur.UserCode == "" && cur.URL != "" {
					cur.UserCode = userCodeIn(driver, line)
				}
				if driver == "claude" && cur.State == signInChecking && strings.Contains(strings.ToLower(line), "invalid") {
					cur.State, cur.Message = signInNeedCode, "That code was not accepted. Paste the newest code from the sign-in page."
				}
				if cur.State == signInStarting && cur.URL != "" && (driver == "claude" || cur.UserCode != "") {
					cur.State = map[string]string{"claude": signInNeedCode, "codex": signInWaiting, "grok": signInWaiting}[driver]
				}
			}
			m.mu.Unlock()
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	go func() { // the login ends: confirm with the tool itself
		defer m.wg.Done()
		err := cmd.Wait()
		_ = pw.Close()
		if err == nil {
			if st, serr := m.readStatus(m.ctx, tenant, driver, accountRef); serr == nil && st.SignedIn {
				m.finishSignIn(s.ID, signInDone, "")
				return
			}
		}
		msg := "The sign-in did not complete. Start it again."
		if ctx.Err() == context.DeadlineExceeded {
			msg = "The sign-in expired after 15 minutes. Start it again."
		}
		m.finishSignIn(s.ID, signInFailed, msg)
	}()
	return s, nil
}

func (m *Module) finishSignIn(id model.ID, state, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.signIns[id]; ok && s.State != signInDone && s.State != signInFailed {
		s.State, s.Message = state, msg
		_ = s.stdin.Close()
		s.cancel()
		if state == signInDone {
			// A new login: the tool's verified release is checked in full again.
			m.engine.ForgetVerified(s.Driver)
		}
	}
}

func (m *Module) cancelSignIn(id model.ID) {
	m.mu.Lock()
	s, ok := m.signIns[id]
	if ok {
		delete(m.signIns, id)
	}
	m.mu.Unlock()
	if ok {
		s.cancel()
	}
}

func (m *Module) signInView(id model.ID) (SignIn, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.signIns[id]
	if !ok {
		return SignIn{}, false
	}
	return SignIn{ID: s.ID, Driver: s.Driver, AccountRef: s.AccountRef, State: s.State, URL: s.URL, UserCode: s.UserCode, Message: s.Message}, true
}

// waitForLink returns once the tool printed its link (or ended), within 10 s.
func (m *Module) waitForLink(ctx context.Context, id model.ID) SignIn {
	return m.waitUntil(ctx, id, 10*time.Second, func(s SignIn) bool { return s.State != signInStarting })
}

// waitForOutcome returns once a pasted code was accepted or refused.
func (m *Module) waitForOutcome(ctx context.Context, id model.ID, limit time.Duration) SignIn {
	return m.waitUntil(ctx, id, limit, func(s SignIn) bool { return s.State != signInChecking })
}

func (m *Module) waitUntil(ctx context.Context, id model.ID, limit time.Duration, done func(SignIn) bool) SignIn {
	deadline := time.Now().Add(limit)
	for {
		s, ok := m.signInView(id)
		if !ok || done(s) || time.Now().After(deadline) {
			return s
		}
		select {
		case <-ctx.Done():
			return s
		case <-time.After(100 * time.Millisecond):
		}
	}
}
