// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
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
// OpenCode v1.18.30 uses `auth login --provider openai --method
// "ChatGPT Pro/Plus (headless)"`; the tool polls its device authorization and
// stores it under HOME/.local/share/opencode/auth.json. Only that native
// headless method is relayed; API-key entry and loopback browser methods are not.
// Docs: opencode.ai/docs/cli/ (auth login flags, auth list, auth.json).

const (
	signInTimeout   = 15 * time.Minute
	statusTimeout   = 15 * time.Second
	maxSignInOutput = 64 << 10
	maxPastedCode   = 2048
	maxReason       = 240
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

	stdin       io.WriteCloser
	cancel      context.CancelFunc
	expires     time.Time
	tenant      model.TenantID
	configDir   string
	geminiStage int    // expected native ACP response id; only the output reader advances it
	lastLine    string // the tool's last non-empty output line, raw: never shown as is
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
	Driver     string  `json:"driver"`
	Installed  bool    `json:"installed"`
	SignedIn   bool    `json:"signed_in"`
	Method     string  `json:"method,omitempty"`
	Account    string  `json:"account,omitempty"`
	Plan       string  `json:"plan,omitempty"`
	AccountRef string  `json:"account_ref,omitempty"`
	Pending    *SignIn `json:"pending,omitempty"`

	// Methods are the official login methods the tool offers; absent for a tool with one.
	Methods []driverfacts.LoginMethodInfo `json:"methods,omitempty"`

	// unrecognized: the tool answered in a form this reader does not know, which is
	// not the same as "not signed in" (the provider snapshot reports it as an error).
	unrecognized bool
}

var (
	errSignInStatus = errors.New("the tool's sign-in status could not be read")
	ansiEscape      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(\x07|\x1b\\)`)
	firstURL        = regexp.MustCompile(`https://[^\s\x1b]+`)
	// Native errors can name a request endpoint; it is not a sign-in prompt.
	// OpenCode prefixes its output with terminal UI markers.
	signInError = regexp.MustCompile(`(?i)^\s*[│●■✖]?\s*(?:error|fatal|failure|failed)\b`)
	deviceCode  = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,5}\b`)
	// Grok prints "Code: <code>"; its server's codes are [A-Z0-9-] (the binary
	// refuses any other), with or without a dash.
	labelledCode = regexp.MustCompile(`(?i)\bcode:\s*([A-Z0-9][A-Z0-9-]{3,15})\b`)
	// signInPrompt opens a line on which the tool waits for the person (Grok
	// 1.0.46 ends it with U+2026, Claude Code 2.1): it says nothing about a
	// failure, only what follows it on that line can.
	signInPrompt = regexp.MustCompile(`^\s*(?:Waiting for approval[.…]*|Paste code here if prompted >)`)
	// A sign-in reason keeps a URL's scheme and host only, and no query anywhere:
	// a path, query or userinfo can carry a code or token.
	urlTail  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)(?:[^\s/?#@]*@)?([^\s/?#@"'()<>,;\\]*)[^\s"'()<>]*`)
	urlQuery = regexp.MustCompile(`\?[^\s"'()<>]*=[^\s"'()<>]*`)
	// Elsewhere in the reason, no user:password@ without a scheme, no value of a
	// name=value or JSON "name": value (a quoted value whole) unless the name only
	// says what went wrong, and no value a label announces (a code, token or
	// password, or a credential after an auth scheme) unless it reads as a word:
	// letters, one leading capital at most, up to 14 ("authentication") and a
	// vowel (device-code alphabets have none). An Authorization header's
	// credential goes whatever it reads as.
	// ponytail: a lowercase run of up to 14 letters with a vowel after a label
	// reads as a word; mask it if a tool's code, token or state takes that shape.
	userInfo    = regexp.MustCompile(`\b[\w.%+-]+:[^\s/@]+@`)
	assignment  = regexp.MustCompile(`(?:([\w.-]+)=|"([\w.-]+)"\s*:\s*)("[^"]*"|'[^']*'|[^\s"'()<>;&,{}\[\]]+)`)
	authHeader  = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization\s*:\s*[a-z][\w-]*\s+)[\w.~+/=-]{8,}`)
	secretLabel = regexp.MustCompile(`\b((?i:(?:(?:user|device)[ _-]?)?code|otp|pin|token|password|passwd|secret|state|nonce|basic|bearer|digest|negotiate|ntlm|apikey)["']?(?:\s+is\s+|\s*[:=-]\s*|\s+)["']?)("[^"]*"|'[^']*'|[A-Z0-9]{4}[ -][A-Z0-9]{4,5}\b|[\w.~+/=-]{4,})`)
	plainWord   = regexp.MustCompile(`^[A-Za-z][a-z]{0,13}$`)
	// opaqueToken is a long run with no spaces, and mixedToken a shorter one with
	// digits and letters: a token or code no recognized shape caught. Words,
	// numbers, hosts and timestamps have neither; a model or target name such as
	// gpt-4o-mini is masked too (over-redaction is allowed, a leak is not).
	opaqueToken = regexp.MustCompile(`[A-Za-z0-9_\-+/=.~#]{32,}`)
	mixedToken  = regexp.MustCompile(`[A-Za-z0-9_\-+/~%]{8,}`)
	// reasonFields are the name=value fields that say what went wrong.
	reasonFields = map[string]bool{"status": true, "error": true, "error_description": true, "message": true}
)

// signInReason makes the tool's own last line safe to show and log: no entity,
// control or format character, full-width forms read as ASCII, a URL cut to its
// scheme and host, no query, userinfo, assigned or labelled secret, no recognized
// secret, no one-time code (the one it printed, or one shaped like Codex's), no
// opaque or mixed token, and at most maxReason bytes.
func signInReason(line, userCode string) string {
	line = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), unicode.IsSpace(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ! to ~
			return r - 0xFF01 + '!'
		}
		return r
	}, html.UnescapeString(line))
	line = urlQuery.ReplaceAllString(urlTail.ReplaceAllString(line, "$1$2"), "")
	line = opaqueToken.ReplaceAllString(redact.Clean(line), "[REDACTED]")
	line = assignment.ReplaceAllStringFunc(userInfo.ReplaceAllString(line, "[REDACTED]@"), func(m string) string {
		sub := assignment.FindStringSubmatch(m)
		if reasonFields[sub[1]+sub[2]] {
			return m
		}
		return strings.TrimSuffix(m, sub[3]) + "[REDACTED]"
	})
	if userCode != "" {
		line = strings.ReplaceAll(line, userCode, "[code]")
	}
	line = secretLabel.ReplaceAllStringFunc(authHeader.ReplaceAllString(line, "${1}[REDACTED]"), func(m string) string {
		sub := secretLabel.FindStringSubmatch(m)
		if plainWord.MatchString(sub[2]) && strings.ContainsAny(strings.ToLower(sub[2]), "aeiouy") {
			return m // "code request failed", "Basic authentication", "token rejected"
		}
		return sub[1] + "[REDACTED]"
	})
	line = mixedToken.ReplaceAllStringFunc(deviceCode.ReplaceAllString(line, "[code]"), func(run string) string {
		letter := strings.IndexFunc(run, unicode.IsLetter)
		if letter < 0 || strings.IndexFunc(run[letter+1:], unicode.IsLetter) < 0 || strings.IndexFunc(run, unicode.IsDigit) < 0 {
			return run // a word, a number, or a timestamp such as 2026-10-06T12
		}
		return "[REDACTED]"
	})
	line = strings.TrimRight(strings.Join(strings.Fields(line), " "), ".")
	if len(line) > maxReason {
		line = strings.ToValidUTF8(line[:maxReason], "") + "…"
	}
	return line
}

// signInFailure is what a login that ended without signing in tells the person:
// the tool's own reason and exit status when it exited with an error after
// printing one, else the plain sentence; always one next step. Each failure is
// logged once, with the same safe reason.
func signInFailure(driver string, err error, lastLine, userCode string) string {
	reason := signInReason(lastLine, userCode)
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	slog.Warn("tool sign-in failed", "driver", driver, "status", status, "reason", reason)
	var exit *exec.ExitError
	if reason == "" || !errors.As(err, &exit) || !exit.Exited() {
		return "The sign-in did not complete. Start it again."
	}
	name := driver
	if facts, ok := driverfacts.Lookup(driver); ok && facts.Name != "" {
		name = facts.Name
	}
	return name + " stopped (" + status + "): " + reason + ". Fix that, then start the sign-in again."
}

// deviceSignIn reports whether a tool's login is a device login (a link and a
// one-time code the operator enters there), as opposed to Claude's pasted code.
func deviceSignIn(driver string) bool {
	facts, _ := driverfacts.Lookup(driver)
	return facts.SignIn == driverfacts.SignInDevice
}

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
	m.statusReads = nil
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
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrWaitDelay) {
		fail(w, 503, "unavailable", "The tool's sign-in status check timed out. Try again.")
		return
	}
	if errors.Is(err, errSignInStatus) {
		fail(w, 503, "unavailable", "The tool's sign-in status could not be read. Try again.")
		return
	}
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
	m.statusReads = nil
	m.mu.Unlock()
}

// OnSignedIn wires what a confirmed sign-in tells the rest of the engine (the
// composition root): the models module refreshes that tenant's model lists.
func (m *Module) OnSignedIn(f func(model.TenantID)) {
	m.mu.Lock()
	m.signedIn = f
	m.mu.Unlock()
}

// toolEnv is the child's whole environment: the tenant's login home the product
// created, PATH, the tool's configuration directory inside that home and the
// host's way out (hostNetworkEnv), and nothing else. FH 036: it was the ENGINE
// USER's home (~/.claude), so a fresh install read, and would have used, a vendor
// login nobody gave the product. The engine's own HOME, CLAUDE_CONFIG_DIR,
// CODEX_HOME or API keys never reach it.
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
	facts, ok := driverfacts.Lookup(driver)
	if !ok || facts.SignIn == "" {
		return nil, "", errors.New("sign-in is available for " + driverfacts.JoinList(driverfacts.SignInNames(), "and"))
	}
	configValue := configDir
	if driver == "gemini-cli" {
		if filepath.Base(configDir) != ".gemini" {
			return nil, "", errors.New("Gemini CLI needs a configuration home named .gemini")
		}
		configValue = filepath.Dir(configDir)
		env = append(env, "NO_BROWSER=true", "GEMINI_CLI_NO_RELAUNCH=true")
	}
	env = append(env, facts.ConfigHomeEnv+"="+configValue)
	env = append(env, hostNetworkEnv()...)
	if driver == "opencode" {
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
			"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
			"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
			"XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "OPENCODE_DISABLE_AUTOUPDATE=1")
	}
	return env, configDir, nil
}

// hostNetworkEnv is the engine's proxy and CA variables (driverfacts.NetworkEnv),
// which a tool needs to reach its vendor from a host that goes out only through
// a proxy (#547). A variable the engine reads as a secret stays behind, in
// either case: https_proxy usually repeats HTTPS_PROXY.
func hostNetworkEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if proxy, trust := driverfacts.NetworkEnv(name); (proxy || trust) && !secret.EnvReferenced(name) &&
			!secret.EnvReferenced(strings.ToUpper(name)) && !secret.EnvReferenced(strings.ToLower(name)) {
			env = append(env, kv)
		}
	}
	return env
}

// hasUserInfo reports a URL that names a user (and maybe a password).
func hasUserInfo(raw string) bool {
	u, err := url.Parse(raw)
	return err != nil || u.User != nil
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

// signInMethodRefusal is the sentence for a method id the tool does not offer: the
// valid ids when it offers several, or that it has one.
func signInMethodRefusal(facts driverfacts.Facts, method string) string {
	if len(method) > 64 {
		method = method[:64]
	}
	ids := facts.LoginMethodIDs()
	if len(ids) == 0 {
		return facts.Name + " has one sign-in method; leave method out."
	}
	return fmt.Sprintf("%s has no sign-in method %q. Use one of: %s.", facts.Name, method, strings.Join(ids, ", "))
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
	if driver == "gemini-cli" {
		return geminiSignInStatus(out, configDir)
	}
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
	return m.readCachedStatus(ctx, tenant, out, program, configDir, env)
}

func (m *Module) readNativeStatus(ctx context.Context, out SignInStatus, program, configDir string, env []string) (SignInStatus, error) {
	driver := out.Driver
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	facts, ok := driverfacts.Lookup(driver)
	if !ok || len(facts.StatusArgs) == 0 {
		return out, errSignInStatus
	}
	args := facts.StatusArgs
	if driver == "opencode" {
		// A status read must not refresh the native model catalog.
		env = append(env, "OPENCODE_DISABLE_MODELS_FETCH=1")
	}
	cmd, release, err := m.command(ctx, driver, program, configDir, env, args...)
	if err != nil {
		return out, err
	}
	defer release()
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Dir = envValue(env, "HOME") // the organization's login home, as for the login
	var raw []byte
	var runErr error
	if driver == "codex" {
		// `codex login status` prints its answer on stderr (codex-cli 0.160), so
		// reading stdout alone reported every signed-in Codex as signed out.
		raw, runErr = cmd.CombinedOutput()
	} else {
		raw, runErr = cmd.Output()
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return out, runErr
	}
	if len(raw) > maxSignInOutput {
		raw = raw[:maxSignInOutput]
	}
	if driver == "codex" {
		text := strings.TrimSpace(ansiEscape.ReplaceAllString(string(raw), ""))
		out.SignedIn = runErr == nil && strings.HasPrefix(strings.ToLower(text), "logged in")
		out.unrecognized = !out.SignedIn && !strings.HasPrefix(strings.ToLower(text), "not logged in")
		if out.SignedIn {
			out.Method = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "Logged in using"), "Logged in"))
		}
		return out, nil
	}
	if driver == "opencode" {
		if runErr != nil {
			return out, errSignInStatus
		}
		// auth list prints provider + credential type, never the credential. A
		// file's mere presence (including an empty auth.json) is not a login.
		for _, line := range strings.Split(ansiEscape.ReplaceAllString(string(raw), ""), "\n") {
			if strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "│●")) == "OpenAI oauth" {
				out.SignedIn, out.Method = true, "ChatGPT"
				break
			}
		}
		return out, nil
	}
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
		Plan       string `json:"subscriptionType"`
	}
	if json.Unmarshal(raw, &st) != nil {
		out.unrecognized = true
		return out, nil
	}
	out.SignedIn = st.LoggedIn
	if st.LoggedIn {
		out.Method, out.Account, out.Plan = st.AuthMethod, st.Email, st.Plan
	}
	return out, nil
}

// LoginStatus reports whether a driver's tool is installed on this node and, for
// a tool with its own login, whether the tenant's own login is signed in: the
// answer GET sign-in gives, for the sessions module's rule that picks a new
// session's profile.
// OpenCode reports its ChatGPT OAuth credential through its native auth list.
func (m *Module) LoginStatus(ctx context.Context, tenant model.TenantID, driver string) (installed, signedIn bool, err error) {
	return m.LoginStatusForProfile(ctx, tenant, driver, "")
}

// LoginStatusForProfile reads the same native status in the selected profile's
// authorized home. Another profile's login cannot qualify this one.
func (m *Module) LoginStatusForProfile(ctx context.Context, tenant model.TenantID, driver, profileRef string) (installed, signedIn bool, err error) {
	if !validSignInDriver(driver) {
		return m.program(driver) != "", false, nil
	}
	st, err := m.readStatus(ctx, tenant, driver, profileRef)
	return st.Installed, st.SignedIn, err
}

// chooseSignInTool is the refusal for a driver that cannot sign in: it names the
// ones that can, from the driver facts.
func chooseSignInTool() string {
	return "Choose " + driverfacts.JoinList(driverfacts.SignInKeys(), "or") + "."
}

func validSignInDriver(driver string) bool {
	facts, _ := driverfacts.Lookup(driver)
	return facts.SignIn != ""
}

// ChildCommand builds a child with the paths it may write (rw) and read (ro);
// the composition confines it there like a session child, which also clears a
// root engine's capabilities. The program itself is readable.
type ChildCommand func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error)

// SetChildCommand wires how every child this module starts is built: the tool's
// login, its status read, the provider probes and Ollama. Without it they run
// unconfined, as the engine user.
func (m *Module) SetChildCommand(f ChildCommand) {
	m.mu.Lock()
	m.childCommand = f
	m.mu.Unlock()
}

// toolCommand builds an unconfined child when the composition wired no
// ChildCommand (tests substitute it).
var toolCommand = exec.CommandContext

// build is the composition's ChildCommand, or an unconfined one when none is wired.
func (m *Module) build() ChildCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.childCommand != nil {
		return m.childCommand
	}
	return func(ctx context.Context, _, _ []string, program string, args ...string) (*exec.Cmd, error) {
		return toolCommand(ctx, program, args...), nil
	}
}

// command builds a child of a driver's tool with env, as for a session child: it
// may write the tool's configuration home, its login home unless that is the
// engine user's own home (granting it would let the tool write every project and
// key in it), and a private temporary directory (TMPDIR) that release removes once
// the child has exited.
func (m *Module) command(ctx context.Context, driver, program, configDir string, env []string, args ...string) (*exec.Cmd, func(), error) {
	tmp, err := os.MkdirTemp("", "olivares-tool-")
	if err != nil {
		return nil, nil, fmt.Errorf("the tool's temporary directory: %w", err)
	}
	release := func() { _ = os.RemoveAll(tmp) }
	rw := []string{configDir, tmp}
	home := envValue(env, "HOME")
	if home != "" && !isEngineUserHome(home) {
		rw = append(rw, home)
	}
	if driver == "opencode" {
		// OpenCode keeps its login in its data home and writes its state and cache:
		// below the login home for an organization, in the engine user's home for its own.
		for name, dir := range map[string]string{"XDG_DATA_HOME": ".local/share", "XDG_STATE_HOME": ".local/state", "XDG_CACHE_HOME": ".cache"} {
			if base := envValue(env, name); base != "" {
				rw = append(rw, filepath.Join(base, "opencode"))
			} else if home != "" {
				rw = append(rw, filepath.Join(home, dir, "opencode"))
			}
		}
	}
	cmd, err := m.build()(ctx, rw, nil, program, args...)
	if err != nil {
		release()
		return nil, nil, err
	}
	cmd.Env = append(append([]string{}, env...), "TMPDIR="+tmp)
	return cmd, release, nil
}

// isEngineUserHome reports the engine user's own home directory.
func isEngineUserHome(path string) bool {
	home, err := os.UserHomeDir()
	return err == nil && filepath.Clean(path) == filepath.Clean(home)
}

// handleSignInStatus reports the tool's native login status and the active flow
// for this selection, so a reloaded page can continue polling the same flow.
func (m *Module) handleSignInStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		driver := r.URL.Query().Get("driver")
		if !validSignInDriver(driver) {
			fail(w, 400, "bad_request", chooseSignInTool())
			return
		}
		tenant, ok := tenantOf(r.URL.Query().Get("tenant_id"))
		if !ok {
			fail(w, 400, "bad_request", "Name the organization the login is for (tenant_id).")
			return
		}
		accountRef := r.URL.Query().Get("account_ref")
		st, err := m.readStatus(r.Context(), tenant, driver, accountRef)
		if err != nil {
			loginHomeError(w, err)
			return
		}
		st.Pending = m.pendingSignIn(tenant, driver, accountRef)
		facts, _ := driverfacts.Lookup(driver)
		st.Methods = facts.LoginMethodList()
		write(w, 200, st)
	})(w, r, mc)
}

// handleSignInStart starts the tool's own login on this node (claude auth login,
// codex or grok login --device-auth) and returns its sign-in page link, plus the
// device code for Codex and Grok Build. A new start replaces a pending login of the same account (or of the default login); other accounts keep theirs. A login expires after 15 minutes. The optional method names one of the tool's official login methods; without it the default runs.
func (m *Module) handleSignInStart(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		var in struct {
			Driver     string `json:"driver"`
			TenantID   string `json:"tenant_id"`
			AccountRef string `json:"account_ref,omitempty"`
			// Method is one of the tool's login method ids; omitted, the default.
			Method string `json:"method,omitempty"`
		}
		// The module's one strict decoder: unknown fields refused, one JSON value, 4 KiB.
		if !decode(w, r, &in) {
			return
		}
		if !validSignInDriver(in.Driver) {
			fail(w, 400, "bad_request", chooseSignInTool())
			return
		}
		facts, _ := driverfacts.Lookup(in.Driver)
		args, known := facts.LoginArgsFor(in.Method)
		if !known {
			fail(w, 400, "bad_request", signInMethodRefusal(facts, in.Method))
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
		if in.Driver == "gemini-cli" {
			if err := geminiSignInStartRefusal(configDir, envValue(env, "HOME")); err != nil {
				if errors.Is(err, errGeminiSavedGoogleLogin) {
					fail(w, 409, "account_login_refused", err.Error())
				} else {
					loginHomeError(w, err)
				}
				return
			}
		}
		s, err := m.startSignIn(tenant, in.Driver, in.AccountRef, configDir, program, args, env)
		if err != nil {
			fail(w, 503, "unavailable", "The tool's sign-in could not be started on this node.")
			return
		}
		meta := map[string]any{"driver": in.Driver, "tenant_id": tenant.String()}
		if in.AccountRef != "" {
			meta["account_ref"] = in.AccountRef
		}
		if in.Method != "" {
			meta["method"] = in.Method
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
func (m *Module) startSignIn(tenant model.TenantID, driver, accountRef, configDir, program string, args, env []string) (*SignIn, error) {
	ctx, cancel := context.WithTimeout(m.ctx, signInTimeout)
	cmd, release, err := m.command(ctx, driver, program, configDir, env, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	// Cancel and expiry kill the login; its output pipe must not keep Wait waiting.
	cmd.WaitDelay = 5 * time.Second
	// The login runs IN the organization's login home, never in the engine user's
	// (SR2 on 65d841a1): the child's working directory is the HOME it was given.
	cmd.Dir = envValue(env, "HOME")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		release()
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
	if driver == "gemini-cli" {
		s.geminiStage = 1
		if _, err := io.WriteString(stdin, geminiSignInInitialize); err != nil {
			cancel()
			_ = pw.Close()
			_ = pr.Close()
			_ = cmd.Wait()
			release()
			m.cancelSignIn(s.ID)
			return nil, err
		}
	}

	m.wg.Add(2)
	read := make(chan struct{})
	go func() { // read the tool's output: the link, and Codex's one-time code
		defer m.wg.Done()
		defer close(read)
		scanner := bufio.NewScanner(io.LimitReader(pr, maxSignInOutput))
		if driver == "gemini-cli" {
			scanner.Split(geminiSignInSplit)
		}
		for scanner.Scan() {
			line := ansiEscape.ReplaceAllString(scanner.Text(), "")
			if driver == "gemini-cli" && m.geminiSignInResponse(s, line) {
				continue
			}
			m.mu.Lock()
			if cur, ok := m.signIns[s.ID]; ok {
				if (cur.URL == "" || driver == "gemini-cli") && !signInError.MatchString(line) {
					// A URL with userinfo is never the sign-in link: it is the
					// host proxy's, with its credential (#547).
					if u := firstURL.FindString(line); u != "" && !hasUserInfo(u) && (driver != "gemini-cli" || geminiAuthorizationURL(u)) {
						cur.URL = strings.TrimRight(u, ".,)")
					}
				}
				if deviceSignIn(driver) && cur.UserCode == "" && cur.URL != "" {
					cur.UserCode = userCodeIn(driver, line)
				}
				prompted := cur.State == signInStarting && cur.URL != "" && (pasteSignIn(driver) || cur.UserCode != "")
				if prompted {
					cur.State = signInNeedCode
					if deviceSignIn(driver) {
						cur.State = signInWaiting
					}
				}
				// The reason is the last line after the prompt that relays the link
				// and the code, without the prompt that waits for the person: a tool
				// that waits again has left any earlier error behind. A URL in an
				// error printed before the prompt does not end that error.
				if prompted {
					cur.lastLine = ""
				} else if driver != "gemini-cli" && strings.TrimSpace(line) != "" {
					cur.lastLine = strings.TrimSpace(signInPrompt.ReplaceAllString(line, ""))
				}
				if driver == "gemini-cli" && cur.State == signInChecking && strings.Contains(line, "Enter the authorization code:") {
					cur.State, cur.Message = signInNeedCode, "That code was not accepted. Paste the newest code from the sign-in page."
				}
				if driver == "claude" && cur.State == signInChecking && strings.Contains(strings.ToLower(line), "invalid") {
					cur.State, cur.Message = signInNeedCode, "That code was not accepted. Paste the newest code from the sign-in page."
				}
			}
			m.mu.Unlock()
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	go func() { // the login ends: confirm with the tool itself
		defer m.wg.Done()
		err := cmd.Wait()
		release()
		_ = pw.Close()
		<-read // the last line is read before the outcome is told
		// Even a failed or canceled native login may have changed its credentials.
		m.mu.Lock()
		for key := range m.statusReads {
			if key.tenant == tenant && key.driver == driver && key.accountRef == accountRef {
				delete(m.statusReads, key)
			}
		}
		m.mu.Unlock()
		m.forgetProviders(driver)
		if driver == "gemini-cli" {
			if view, ok := m.signInView(s.ID); ok && view.State == signInDone {
				m.mu.Lock()
				signedIn := m.signedIn
				m.mu.Unlock()
				if signedIn != nil {
					signedIn(tenant)
				}
				return
			}
		}
		if err == nil && driver != "gemini-cli" {
			if st, serr := m.readStatus(m.ctx, tenant, driver, accountRef); serr == nil && st.SignedIn {
				m.finishSignIn(s.ID, signInDone, "")
				m.mu.Lock()
				signedIn := m.signedIn
				m.mu.Unlock()
				if signedIn != nil {
					signedIn(tenant)
				}
				return
			}
		}
		switch ctx.Err() {
		case context.DeadlineExceeded:
			m.finishSignIn(s.ID, signInFailed, "The sign-in expired after 15 minutes. Start it again.")
			return
		case context.Canceled: // stopped here (cancel, a new start, shutdown): no tool reason
			m.finishSignIn(s.ID, signInFailed, "The sign-in did not complete. Start it again.")
			return
		}
		m.mu.Lock()
		lastLine, userCode := s.lastLine, s.UserCode
		m.mu.Unlock()
		m.finishSignIn(s.ID, signInFailed, signInFailure(driver, err, lastLine, userCode))
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
	return publicSignIn(s), true
}

// pendingSignIn returns a detached public view from the node's live registry.
// A completed, failed, cancelled or expired flow cannot be resumed.
func (m *Module) pendingSignIn(tenant model.TenantID, driver, accountRef string) *SignIn {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.signIns {
		if s.tenant != tenant || s.Driver != driver || s.AccountRef != accountRef || !s.expires.After(time.Now()) {
			continue
		}
		switch s.State {
		case signInStarting, signInNeedCode, signInWaiting, signInChecking:
			view := publicSignIn(s)
			return &view
		}
	}
	return nil
}

// Callers hold m.mu while taking this view; no process handle or home is shared.
func publicSignIn(s *SignIn) SignIn {
	return SignIn{ID: s.ID, Driver: s.Driver, AccountRef: s.AccountRef, State: s.State, URL: s.URL, UserCode: s.UserCode, Message: s.Message}
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
