// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/core/api"
	coreaudit "github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// The stubs speak the same login protocol the official CLIs speak on a machine
// without a browser: Claude prints a link and reads the code its page shows;
// Codex prints a link and a one-time code and finishes on its own. Each writes
// its login where the real tool does, and its status command reads it back.
const stubClaude = `#!/bin/sh
case "$1 $2" in
"auth login")
  echo "Opening browser to sign in..."
  echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=stub"
  printf "Paste code here if prompted > "
  read code
  if [ "$code" = "good-code" ]; then mkdir -p "$CLAUDE_CONFIG_DIR"; echo '{}' > "$CLAUDE_CONFIG_DIR/.credentials.json"; echo "Login successful"; exit 0; fi
  echo "Invalid code"; exit 1;;
"auth status")
  if [ -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","email":"fran@example.test"}'; exit 0; fi
  echo '{"loggedIn":false,"authMethod":"none"}'; exit 1;;
esac
exit 2
`

const stubCodex = `#!/bin/sh
case "$1 $2" in
"login --device-auth")
  printf '1. Open this link in your browser\n   \033[94mhttps://auth.openai.com/codex/device\033[0m\n2. Enter this one-time code\n   \033[94mABCD-12345\033[0m\n'
  if [ -f "$HOME/.slow-login" ]; then echo $$ > "$HOME/.login.pid"; exec sleep 60; fi
  sleep 1; mkdir -p "$CODEX_HOME"; echo '{}' > "$CODEX_HOME/auth.json"; exit 0;;
"login status")
  if [ -f "$CODEX_HOME/auth.json" ]; then echo "Logged in using ChatGPT"; exit 0; fi
  echo "Not logged in"; exit 1;;
esac
exit 2
`

// stubGrok prints what Grok Build's device login prints (strings of the 1.0.46 binary:
// "Open this URL in your browser to approve:", "Code: ", "Waiting for approval"),
// with a code that has no dash, and stores its login where Grok keeps it.
const stubGrok = `#!/bin/sh
case "$1 $2" in
"login --device-auth")
  printf 'Sign in to Grok\nOpen this URL in your browser to approve:\n  https://accounts.x.ai/device\nCode: K7M2QX9P\nWaiting for approval...\n'
  sleep 1; mkdir -p "$GROK_HOME"; echo '{}' > "$GROK_HOME/auth.json"; exit 0;;
esac
exit 2
`

// signedInDecoy makes the ENGINE user's own home look signed in to every tool, with
// the tools' variables pointing at it (FH 036). Nothing the product does may read it:
// a product login lives in the tenant's own login home.
func signedInDecoy(t *testing.T) string {
	t.Helper()
	engineHome := t.TempDir()
	for _, f := range []string{".claude/.credentials.json", ".codex/auth.json", ".grok/auth.json"} {
		p := filepath.Join(engineHome, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", engineHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(engineHome, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(engineHome, ".codex"))
	t.Setenv("GROK_HOME", filepath.Join(engineHome, ".grok"))
	return engineHome
}

// testLoginHome is the composition's layout for the tests: <root>/<tenant>/<driver>.
func testLoginHome(root string) LoginHomeFunc {
	return func(_ context.Context, tenant model.TenantID, driver, accountRef string) (string, string, error) {
		rel, ok := map[string]string{"claude": ".claude", "codex": ".codex", "grok": ".grok"}[driver]
		if !ok || tenant.IsZero() {
			return "", "", errors.New("no login home")
		}
		home := filepath.Join(root, tenant.String(), driver)
		if accountRef != "" {
			home = filepath.Join(home, accountRef)
		}
		return home, filepath.Join(home, rel), nil
	}
}

// newSignInServer serves the module with stub tools. home is the harness tenant's
// login directory (<login root>/<tenant>); each tool's login is under <home>/<driver>.
// Calls to the sign-in status and start name the harness tenant unless they name one.
func newSignInServer(t *testing.T, configure ...func(m *Module, bin string)) (call func(method, path string, body any) (int, map[string]any), home string) {
	t.Helper()
	ctx := context.Background()
	signedInDecoy(t)
	loginRoot := t.TempDir()
	bin := toolinstalltest.ExecCapableDir(t)
	for name, script := range map[string]string{"claude": stubClaude, "codex": stubCodex, "grok": stubGrok} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(ctx, toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.SetProgramResolver(func(driver string) string { return filepath.Join(bin, driver) })
	m.SetLoginHome(testLoginHome(loginRoot))
	for _, c := range configure {
		c(m, bin)
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(s store.SystemScope) error { _, e := s.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := coreaudit.NewSigner(priv)
	a := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{Store: st, Authenticator: a, Authorizer: auth.NewAuthorizer(nil), PrincipalEvidenceProducer: a, Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Modules: []api.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BootstrapSuperadmin(ctx, "root@olivares.invalid", "fixture-password-123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login(ctx, "root@olivares.invalid", "fixture-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	call = func(method, path string, body any) (int, map[string]any) {
		if tenant != "" && method == "GET" && strings.HasPrefix(path, "/v1/m/agenttools/sign-in?") && !strings.Contains(path, "tenant_id=") {
			path += "&tenant_id=" + tenant.String()
		}
		if in, ok := body.(map[string]string); ok && tenant != "" && method == "POST" && path == "/v1/m/agenttools/sign-in" {
			if _, named := in["tenant_id"]; !named {
				with := map[string]string{"tenant_id": tenant.String()}
				for k, v := range in {
					with[k] = v
				}
				body = with
			}
		}
		b, raw := body.([]byte) // a raw body is sent as is
		if !raw {
			b, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		if tenant != "" {
			r.Header.Set("X-Olivares-Tenant", tenant.String())
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, org := call("POST", "/v1/system/orgs", map[string]string{"name": "Tools", "slug": "tools"})
	if code != 201 {
		t.Fatalf("org %d %v", code, org)
	}
	tenant = model.TenantID(org["tenant_id"].(string))
	return call, filepath.Join(loginRoot, tenant.String())
}

func pollSignIn(t *testing.T, call func(string, string, any) (int, map[string]any), id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, s := call("GET", "/v1/m/agenttools/sign-in/"+id, nil)
		if code == 200 && s["state"] == want {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("sign-in %s never reached %q: %d %v", id, want, code, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClaudeSignInRelaysTheLinkAndThePastedCode(t *testing.T) {
	call, home := newSignInServer(t)
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["installed"] != true || st["signed_in"] != false {
		t.Fatalf("status before = %d %v, want installed and not signed in", code, st)
	}
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude"})
	if code != 202 || s["state"] != "needs_code" || !strings.HasPrefix(s["url"].(string), "https://claude.com/") {
		t.Fatalf("start = %d %v, want needs_code with the tool's link", code, s)
	}
	id := s["id"].(string)
	if code, s := call("POST", "/v1/m/agenttools/sign-in/"+id+"/code", map[string]string{"code": "good-code"}); code != 202 || s["state"] != "signed_in" {
		t.Fatalf("code = %d %v, want signed_in", code, s)
	}
	if _, err := os.Stat(filepath.Join(home, "claude", ".claude", ".credentials.json")); err != nil {
		t.Fatalf("the login was not stored where Claude Code keeps it: %v", err)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["signed_in"] != true || st["account"] != "fran@example.test" {
		t.Fatalf("status after = %d %v, want signed in", code, st)
	}
}

func TestClaudeSignInRefusesAWrongCodeWithoutStoringALogin(t *testing.T) {
	call, home := newSignInServer(t)
	_, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude"})
	id := s["id"].(string)
	call("POST", "/v1/m/agenttools/sign-in/"+id+"/code", map[string]string{"code": "wrong"})
	pollSignIn(t, call, id, "failed")
	if _, err := os.Stat(filepath.Join(home, "claude", ".claude", ".credentials.json")); err == nil {
		t.Fatal("a refused code left a login behind")
	}
}

func TestCodexSignInShowsTheDeviceCodeAndFinishesOnItsOwn(t *testing.T) {
	call, home := newSignInServer(t)
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
	if code != 202 || s["state"] != "waiting" || s["url"] != "https://auth.openai.com/codex/device" || s["user_code"] != "ABCD-12345" {
		t.Fatalf("start = %d %v, want the device link and code", code, s)
	}
	pollSignIn(t, call, s["id"].(string), "signed_in")
	if _, err := os.Stat(filepath.Join(home, "codex", ".codex", "auth.json")); err != nil {
		t.Fatalf("the login was not stored where Codex keeps it: %v", err)
	}
}

// HU-R15 (refresh 06): Grok Build installed but offered no sign-in. Its CLI has a
// device login for machines without a browser (grok login --device-auth): the page
// relays its link and code like Codex's, and the login lands in GROK_HOME.
func TestGrokSignInShowsTheDeviceCodeAndFinishesOnItsOwn(t *testing.T) {
	call, home := newSignInServer(t)
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=grok", nil); code != 200 || st["installed"] != true || st["signed_in"] != false {
		t.Fatalf("status before = %d %v", code, st)
	}
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "grok"})
	if code != 202 || s["state"] != "waiting" || s["url"] != "https://accounts.x.ai/device" || s["user_code"] != "K7M2QX9P" {
		t.Fatalf("start = %d %v, want the device link and code", code, s)
	}
	pollSignIn(t, call, s["id"].(string), "signed_in")
	if _, err := os.Stat(filepath.Join(home, "grok", ".grok", "auth.json")); err != nil {
		t.Fatalf("the login was not stored where Grok keeps it: %v", err)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=grok", nil); code != 200 || st["signed_in"] != true {
		t.Fatalf("status after = %d %v", code, st)
	}
}

// HU on R1 refresh 01: a started device login must be cancellable. Cancel ends
// the tool's login process on the server and forgets the pending login.
func TestSignInCancelEndsTheLoginProcess(t *testing.T) {
	call, home := newSignInServer(t)
	if err := os.MkdirAll(filepath.Join(home, "codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "codex", ".slow-login"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
	if code != 202 || s["state"] != "waiting" {
		t.Fatalf("start = %d %v", code, s)
	}
	id := s["id"].(string)
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(home, "codex", ".login.pid")); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the stub login never started")
	}
	if code, _ := call("DELETE", "/v1/m/agenttools/sign-in/"+id, nil); code != 200 {
		t.Fatalf("cancel = %d", code)
	}
	if code, _ := call("GET", "/v1/m/agenttools/sign-in/"+id, nil); code != 404 {
		t.Fatalf("a cancelled login is still pending: %d", code)
	}
	deadline = time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the login process %d is still running after Cancel", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Sign-in bodies go through the module's one strict decoder: an unknown field
// or a second JSON value is refused before any login starts.
func TestSignInBodiesAreStrictJSON(t *testing.T) {
	call, _ := newSignInServer(t)
	if code, _ := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex", "extra": "x"}); code != 400 {
		t.Fatalf("unknown field = %d, want 400", code)
	}
	if code, _ := call("POST", "/v1/m/agenttools/sign-in", []byte(`{"driver":"codex"}{"driver":"claude"}`)); code != 400 {
		t.Fatalf("two JSON values = %d, want 400", code)
	}
}

func TestSignInRefusesAToolThatIsNotInstalled(t *testing.T) {
	call, _ := newSignInServer(t)
	// Grok Build signs in since HU-R15; OpenCode has no login of its own to relay.
	if code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode"}); code != 400 {
		t.Fatalf("opencode = %d %v, want 400 (claude, codex or grok)", code, s)
	}
}

// LoginStatus is what the sessions module's resolve rule reads: the tool's own
// answer, not a guess from files, and "installed" only for a tool with no login.
func TestLoginStatusIsWhatTheToolSays(t *testing.T) {
	signedInDecoy(t)
	loginRoot := t.TempDir()
	tenant := model.TenantID(model.NewID())
	home := filepath.Join(loginRoot, tenant.String())
	bin := toolinstalltest.ExecCapableDir(t)
	for name, script := range map[string]string{"claude": stubClaude, "opencode": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.SetProgramResolver(func(driver string) string {
		if driver == "claude" || driver == "opencode" {
			return filepath.Join(bin, driver)
		}
		return ""
	})
	m.SetLoginHome(testLoginHome(loginRoot))
	check := func(driver string, wantInstalled, wantSignedIn bool) {
		t.Helper()
		installed, signedIn, err := m.LoginStatus(context.Background(), tenant, driver)
		if err != nil || installed != wantInstalled || signedIn != wantSignedIn {
			t.Fatalf("%s = (%v, %v, %v), want (%v, %v)", driver, installed, signedIn, err, wantInstalled, wantSignedIn)
		}
	}
	check("claude", true, false)
	check("grok", false, false)
	check("opencode", true, false)
	if err := os.WriteFile(filepath.Join(home, "claude", ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("claude", true, true)
}

// Root on FH 036 (EU, refresh 08): a fresh install showed Claude Code signed in,
// because the status read the engine user's own ~/.claude. The engine here has a
// signed-in HOME, CLAUDE_CONFIG_DIR and CODEX_HOME; the product's answer is "not
// signed in" for every tool, and nothing is written into the engine user's home.
func TestAFreshInstallIsNotSignedInWhateverTheEngineUserHas(t *testing.T) {
	call, _ := newSignInServer(t)
	engineHome := os.Getenv("HOME")
	before, _ := os.ReadDir(filepath.Join(engineHome, ".claude"))
	for _, driver := range []string{"claude", "codex", "grok"} {
		if code, st := call("GET", "/v1/m/agenttools/sign-in?driver="+driver, nil); code != 200 || st["installed"] != true || st["signed_in"] != false {
			t.Fatalf("%s on a fresh install = %d %v, want installed and NOT signed in", driver, code, st)
		}
	}
	if after, _ := os.ReadDir(filepath.Join(engineHome, ".claude")); len(after) != len(before) {
		t.Fatal("the product wrote into the engine user's own ~/.claude")
	}
	if code, _ := call("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id=", nil); code != 400 {
		t.Fatalf("a status with no organization = %d, want 400", code)
	}
}

// Root on FH 036: the own login is per tenant. One organization's sign-in lives in
// its own home and does not sign the tool in for another organization.
func TestEachOrganizationHasItsOwnLogin(t *testing.T) {
	call, home := newSignInServer(t)
	code, other := call("POST", "/v1/system/orgs", map[string]string{"name": "Other", "slug": "other"})
	if code != 201 {
		t.Fatalf("org %d %v", code, other)
	}
	_, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude"})
	if code, s := call("POST", "/v1/m/agenttools/sign-in/"+s["id"].(string)+"/code", map[string]string{"code": "good-code"}); code != 202 || s["state"] != "signed_in" {
		t.Fatalf("sign-in for the first organization = %d %v", code, s)
	}
	if _, err := os.Stat(filepath.Join(home, "claude", ".claude", ".credentials.json")); err != nil {
		t.Fatalf("the login is not in the organization's own home: %v", err)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["signed_in"] != true {
		t.Fatalf("first organization = %d %v, want signed in", code, st)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id="+other["tenant_id"].(string), nil); code != 200 || st["signed_in"] != false {
		t.Fatalf("other organization = %d %v, want NOT signed in", code, st)
	}
}

// SR2 on 65d841a1: the tool's own processes run IN the organization's login home,
// never in the engine user's (the status read here; the login is SR2's oracle).
func TestTheStatusReadRunsInTheLoginHome(t *testing.T) {
	signedInDecoy(t)
	loginRoot := t.TempDir()
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.SetProgramResolver(func(string) string { return "/bin/true" })
	m.SetLoginHome(testLoginHome(loginRoot))
	var seen *exec.Cmd
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		seen = exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		return seen
	}
	t.Cleanup(func() { toolCommand = previous })
	tenant := model.TenantID(model.NewID())
	if _, err := m.readStatus(context.Background(), tenant, "claude", ""); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(loginRoot, tenant.String(), "claude")
	if seen == nil || filepath.Clean(seen.Dir) != want || seen.Dir == os.Getenv("HOME") {
		t.Fatalf("status read ran in %v, want the login home %q", seen, want)
	}
}

func TestAccountSignInStatusAndCompletionStayWithTheSelectedHome(t *testing.T) {
	call, home := newSignInServer(t)
	start := func(ref string) string {
		t.Helper()
		code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude", "account_ref": ref})
		if code != 202 || flow["state"] != "needs_code" || flow["account_ref"] != ref {
			t.Fatalf("start = %d %v", code, flow)
		}
		return flow["id"].(string)
	}
	first, second := start("ppf_first"), start("ppf_second")
	if code, flow := call("POST", "/v1/m/agenttools/sign-in/"+first+"/code", map[string]string{"code": "good-code"}); code != 202 || flow["state"] != "signed_in" {
		t.Fatalf("first completion = %d %v", code, flow)
	}
	for _, tc := range []struct {
		ref    string
		signed bool
	}{{"ppf_first", true}, {"ppf_second", false}, {"", false}} {
		code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude&account_ref="+tc.ref, nil)
		if code != 200 || st["signed_in"] != tc.signed {
			t.Fatalf("status %s = %d %v", tc.ref, code, st)
		}
	}
	if code, flow := call("GET", "/v1/m/agenttools/sign-in/"+second, nil); code != 200 || flow["state"] != "needs_code" {
		t.Fatalf("second login did not survive first = %d %v", code, flow)
	}
	if _, err := os.Stat(filepath.Join(home, "claude", "ppf_first", ".claude", ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+second, nil)
}
