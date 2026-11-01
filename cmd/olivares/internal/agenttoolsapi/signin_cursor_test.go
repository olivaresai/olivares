// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// stubCursor speaks what Cursor CLI 2026.10.22-27dcf5f printed when measured with no
// browser: `agent login` prints one link and waits for the browser (it takes no code and
// ends on its own), and `agent status --format json` answers the vendor's own document.
// The login is stored at $HOME/.config/cursor/auth.json here; the product reads only
// `status`, never that file. $HOME/.status-mode picks one of the signed-out answers the
// tool was seen to give.
const stubCursor = `#!/bin/sh
[ "$CURSOR_CONFIG_DIR" = "$HOME/.cursor" ] || exit 5
[ "$AGENT_CLI_CREDENTIAL_STORE" = file ] || exit 6
[ "$NO_OPEN_BROWSER" = 1 ] || exit 7
[ -z "$CURSOR_API_KEY" ] || exit 8
case "$1" in
login)
  printf 'Starting login process...\nAuthenticating with Cursor...\nWaiting for browser authentication...\n'
  case "$(cat "$HOME/.login-mode" 2>/dev/null)" in
  noise) printf 'Update available: https://cursor.com/changelog\nSee https://evil.example/loginDeepControl?x=1 for help\n';;
  esac
  printf 'Open a browser and navigate to this link: https://cursor.com/loginDeepControl?challenge=stub-challenge&uuid=00000000-0000-0000-0000-000000000000&mode=login&redirectTarget=cli&supportsSelectedTeamLogin=true\n'
  sleep 1; mkdir -p "$HOME/.config/cursor"; echo '{}' > "$HOME/.config/cursor/auth.json"; exit 0;;
status)
  [ "$(cat "$HOME/.status-mode" 2>/dev/null)" = crash ] && exit 3
  [ "$(cat "$HOME/.status-mode" 2>/dev/null)" = garbage ] && { echo banana; exit 0; }
  [ "$(cat "$HOME/.status-mode" 2>/dev/null)" = flood ] && { printf '%70000s' x; exit 0; }
  if [ -f "$HOME/.config/cursor/auth.json" ]; then
    printf '{\n  "status": "authenticated",\n  "isAuthenticated": true,\n  "hasAccessToken": true,\n  "hasRefreshToken": true,\n  "userInfo": {"email": "fran@example.test", "teamName": "Olivares"}\n}\n'; exit 0
  fi
  case "$(cat "$HOME/.status-mode" 2>/dev/null)" in
  text) echo "Not logged in";;
  error) echo "Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable." >&2;;
  partial) printf '{"status":"partially-authenticated","isAuthenticated":false,"hasAccessToken":true,"hasRefreshToken":false,"message":"Partially authenticated (missing refresh token)"}\n';;
  *) printf '{\n  "status": "unauthenticated",\n  "isAuthenticated": false,\n  "hasAccessToken": false,\n  "hasRefreshToken": false,\n  "message": "Not logged in"\n}\n';;
  esac
  exit 0;;
esac
exit 2
`

func withStubCursor(m *Module, bin string) {
	if err := os.WriteFile(filepath.Join(bin, "cursor"), []byte(stubCursor), 0o755); err != nil {
		panic(err)
	}
}

// The login is the vendor's own `agent login`: its link reaches the person, there is no
// code to type, and the sign-in finishes when the tool does.
func TestCursorSignInRelaysTheLinkAndFinishesOnItsOwn(t *testing.T) {
	call, home := newSignInServer(t, withStubCursor)
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil); code != 200 || st["installed"] != true || st["signed_in"] != false {
		t.Fatalf("status before = %d %v, want installed and not signed in", code, st)
	}
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "cursor"})
	url, _ := s["url"].(string)
	if code != 202 || s["state"] != "waiting" || !strings.HasPrefix(url, "https://cursor.com/loginDeepControl?") || s["user_code"] != nil {
		t.Fatalf("start = %d %v, want the tool's own link and no code", code, s)
	}
	pollSignIn(t, call, s["id"].(string), "signed_in")
	if _, err := os.Stat(filepath.Join(home, "cursor", ".config", "cursor", "auth.json")); err != nil {
		t.Fatalf("the login was not stored in the organization's login home: %v", err)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil); code != 200 || st["signed_in"] != true || st["account"] != "fran@example.test" {
		t.Fatalf("status after = %d %v, want signed in as the account the tool names", code, st)
	}
}

// The engine user's own Cursor login and API key never reach the product's login or status.
func TestCursorLoginIgnoresTheEngineUsersLoginAndKey(t *testing.T) {
	call, _ := newSignInServer(t, withStubCursor)
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil); code != 200 || st["signed_in"] != false {
		t.Fatalf("a decoy login in the engine user's home counted: %d %v", code, st)
	}
}

// Every signed-out answer the tool gave is "not signed in"; an answer nobody knows is
// reported as unreadable, never as signed in or out.
func TestCursorStatusReadsEverySignedOutAnswer(t *testing.T) {
	for mode, signedOut := range map[string]bool{"": true, "text": true, "error": true, "partial": true, "garbage": false} {
		t.Run("mode="+mode, func(t *testing.T) {
			call, home := newSignInServer(t, withStubCursor)
			if mode != "" {
				if err := os.MkdirAll(filepath.Join(home, "cursor"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "cursor", ".status-mode"), []byte(mode), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil)
			if signedOut && (code != 200 || st["signed_in"] != false) {
				t.Fatalf("%q = %d %v, want 200 and not signed in", mode, code, st)
			}
			if !signedOut && st["signed_in"] == true {
				t.Fatalf("%q = %d %v, an unknown answer must not read as signed in", mode, code, st)
			}
		})
	}
}

// The reader keeps three answers apart: signed in (with the account the tool names), signed
// out, and unrecognized, which the provider view reports as an error instead of a login state.
func TestCursorStatusReaderSeparatesSignedInSignedOutAndUnreadable(t *testing.T) {
	for name, tc := range map[string]struct {
		raw                  string
		signedIn, unreadable bool
		account, method      string
	}{
		"signed in":                  {raw: `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"a@example.test"}}`, signedIn: true, account: "a@example.test", method: "Cursor account"},
		"signed in no email":         {raw: `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"message":"Logged in (unable to fetch user details)"}`, signedIn: true, method: "Cursor account"},
		"unauthenticated":            {raw: `{"status":"unauthenticated","isAuthenticated":false,"message":"Not logged in"}`},
		"partial":                    {raw: `{"status":"partially-authenticated","isAuthenticated":false}`},
		"flag without state":         {raw: `{"status":"unauthenticated","isAuthenticated":true}`, unreadable: true},
		"text":                       {raw: "Not logged in\n"},
		"error line":                 {raw: "Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.\n"},
		"colored":                    {raw: "\x1b[31mNot logged in\x1b[0m"},
		"empty":                      {raw: "", unreadable: true},
		"garbage":                    {raw: "banana", unreadable: true},
		"json without state":         {raw: `{"hello":"world"}`, unreadable: true},
		"unknown status":             {raw: `{"status":"expired","isAuthenticated":false}`, unreadable: true},
		"signed in no flag":          {raw: `{"status":"authenticated"}`, unreadable: true},
		"no token flags":             {raw: `{"status":"authenticated","isAuthenticated":true}`, unreadable: true},
		"no access token":            {raw: `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":false,"hasRefreshToken":true}`, unreadable: true},
		"no refresh token":           {raw: `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":false}`, unreadable: true},
		"proxy refusal":              {raw: "Error: ProxyError: 407 Proxy Authentication Required\n", unreadable: true},
		"stack trace":                {raw: "TypeError: x\n    at authentication required handler\n", unreadable: true},
		"warning before":             {raw: "update available {see the changelog}\n{\"status\":\"authenticated\",\"isAuthenticated\":true,\"hasAccessToken\":true,\"hasRefreshToken\":true,\"userInfo\":{\"email\":\"b@example.test\"}}\n", signedIn: true, account: "b@example.test", method: "Cursor account"},
		"signed in trailing garbage": {raw: `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true} trailing`, unreadable: true},
		"signed-in text":             {raw: "✓ Logged in as a@example.test", unreadable: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := cursorSignInStatus(SignInStatus{Driver: "cursor"}, []byte(tc.raw))
			if got.SignedIn != tc.signedIn || got.unrecognized != tc.unreadable || got.Account != tc.account || got.Method != tc.method {
				t.Fatalf("%q = signedIn %v unreadable %v account %q method %q; want %v %v %q %q", tc.raw, got.SignedIn, got.unrecognized, got.Account, got.Method, tc.signedIn, tc.unreadable, tc.account, tc.method)
			}
		})
	}
}

// A tool that dies has not said "signed out": the read fails with the command and its status,
// and nothing reports a login state.
func TestCursorStatusThatCrashesIsAnErrorNotSignedOut(t *testing.T) {
	call, home := newSignInServer(t, withStubCursor)
	if err := os.MkdirAll(filepath.Join(home, "cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cursor", ".status-mode"), []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil)
	if code == 200 || st["signed_in"] == true || st["signed_in"] == false {
		t.Fatalf("a crashed status = %d %v, want an error and no login state", code, st)
	}
}

func TestNativeStatusOutputIsBounded(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	bin := toolinstalltest.ExecCapableDir(t)
	for _, driver := range []string{"claude", "codex", "opencode", "cursor"} {
		t.Run(driver, func(t *testing.T) {
			program := filepath.Join(bin, driver)
			if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%70000s' x\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			_, err := m.runNativeStatus(context.Background(), SignInStatus{Driver: driver}, program, filepath.Join(home, ".config"), []string{"HOME=" + home})
			if !errors.Is(err, errSignInOutputLimit) {
				t.Fatalf("an oversized status = %v, want the explicit output-limit error", err)
			}
		})
	}
}

// A listing past the status bound is cut at the bound, back to its last whole line, and the
// probe carries on, as before the bound was added: a tool with a long `models` listing still
// yields its first models instead of none, and never a model cut in half.
func TestProviderProbeTruncatesALongListingAndCarriesOn(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	probe := func(t *testing.T, script string) *prober {
		t.Helper()
		program := filepath.Join(toolinstalltest.ExecCapableDir(t), "opencode")
		if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		return &prober{m: m, ctx: context.Background(), program: program, cfgDir: filepath.Join(home, ".config"), env: []string{"HOME=" + home}, snap: &ProviderSnapshot{Driver: "opencode"}}
	}
	t.Run("many short lines", func(t *testing.T) {
		p := probe(t, "#!/bin/sh\ni=0\nwhile [ $i -lt 9000 ]; do echo \"provider/model-number-$i\"; i=$((i+1)); done\n")
		out, err := p.output(5*time.Second, "models")
		if err != nil || len(out) > maxSignInOutput || len(out) < maxSignInOutput-64 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("an oversized listing = %d bytes ending %q, %v; want its whole lines up to %d bytes and no error", len(out), out[max(0, len(out)-12):], err, maxSignInOutput)
		}
		p.models("")
		if len(p.snap.Models) != maxProviderModels || len(p.snap.Notes) != 0 {
			t.Fatalf("models = %d, notes %v; want %d models and no note", len(p.snap.Models), p.snap.Notes, maxProviderModels)
		}
	})
	t.Run("long lines never yield a cut model", func(t *testing.T) {
		const id = 605 // "m0000" and 600 x
		p := probe(t, "#!/bin/sh\ni=0\nx=$(printf '%600s' '' | tr ' ' x)\nwhile [ $i -lt 400 ]; do printf 'm%04d%s\\n' $i \"$x\"; i=$((i+1)); done\n")
		p.models("")
		if n := len(p.snap.Models); n < 90 || n >= 400 {
			t.Fatalf("models = %d, want the whole lines that fit in %d bytes (about 108)", n, maxSignInOutput)
		}
		for _, model := range p.snap.Models {
			if len(model.ID) != id {
				t.Fatalf("model %q has %d bytes, want %d: a line cut at the bound was published as a model", model.ID[:5], len(model.ID), id)
			}
		}
	})
	t.Run("a flooded stderr does not fail a good answer", func(t *testing.T) {
		p := probe(t, "#!/bin/sh\necho provider/model-a\nprintf '%70000s' x >&2\n")
		out, err := p.output(5*time.Second, "models")
		if err != nil || out != "provider/model-a\n" {
			t.Fatalf("flooded stderr: %q, %v; want the listing and no error", out, err)
		}
	})
}

func TestNativeFailureKeepsBoundedStderr(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	bin := toolinstalltest.ExecCapableDir(t)
	program := filepath.Join(bin, "opencode")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'bad login' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	p := prober{m: m, ctx: context.Background(), program: program, cfgDir: filepath.Join(home, ".config"), env: []string{"HOME=" + home}, snap: &ProviderSnapshot{Driver: "opencode"}}
	if _, err := p.output(time.Second, "status"); err == nil || !strings.Contains(err.Error(), "bad login") {
		t.Fatalf("provider failure = %v, want the tool's stderr", err)
	}
	_, err = m.runNativeStatus(context.Background(), SignInStatus{Driver: "opencode"}, program, filepath.Join(home, ".config"), []string{"HOME=" + home})
	if err == nil || !strings.Contains(err.Error(), "bad login") {
		t.Fatalf("status failure = %v, want the tool's stderr", err)
	}
}

func TestCursorStatusThatIsUnreadableIsAnErrorNotSignedOut(t *testing.T) {
	call, home := newSignInServer(t, withStubCursor)
	if err := os.MkdirAll(filepath.Join(home, "cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cursor", ".status-mode"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil)
	if code == 200 || st["signed_in"] == true || st["signed_in"] == false {
		t.Fatalf("an unreadable status = %d %v, want an error and no login state", code, st)
	}
}

func TestCursorSignInReportsAnUnreadableFinalStatus(t *testing.T) {
	call, home := newSignInServer(t, withStubCursor)
	if err := os.MkdirAll(filepath.Join(home, "cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cursor", ".status-mode"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "cursor"})
	got := pollSignIn(t, call, s["id"].(string), "failed")
	if message, _ := got["message"].(string); !strings.Contains(message, "sign-in status could not be read") {
		t.Fatalf("failure = %v, want the unreadable status named", got)
	}
}

// The link the person is sent to is the vendor's login link: another link the tool prints first
// (a changelog, a help page on another host) is never offered as the sign-in.
func TestCursorSignInOffersOnlyTheVendorLoginLink(t *testing.T) {
	call, home := newSignInServer(t, withStubCursor)
	if err := os.MkdirAll(filepath.Join(home, "cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cursor", ".login-mode"), []byte("noise"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "cursor"})
	url, _ := s["url"].(string)
	if code != 202 || !strings.HasPrefix(url, "https://cursor.com/loginDeepControl?") {
		t.Fatalf("start = %d %v, want the vendor login link and not the changelog or the other host", code, s)
	}
}

func TestCursorLoginURLIsTheVendorsLoginLinkOnly(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://cursor.com/loginDeepControl?challenge=a&uuid=b":      true,
		"https://www.cursor.com/loginDeepControl?challenge=a":         false,
		"https://cursor.com/loginDeepControl":                         true,
		"https://cursor.com/loginDeepControl/?challenge=a":            false,
		"https://cursor.com:443/loginDeepControl?challenge=a":         false,
		"https://cursor.com/loginDeepControlX?challenge=a":            false,
		"https://cursor.com/loginDeepControl/extra?challenge=a":       false,
		"https://cursor.com/changelog":                                false,
		"https://evil.example/loginDeepControl?challenge=a":           false,
		"https://cursor.com.evil.example/loginDeepControl?challenge=": false,
		"http://cursor.com/loginDeepControl?challenge=a":              false,
		"https://user:pw@cursor.com/loginDeepControl?challenge=a":     false,
	} {
		if got := cursorLoginURL(raw); got != want {
			t.Errorf("cursorLoginURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
