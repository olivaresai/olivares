// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestBackgroundProvidersReturnBeforeNativeProbesFinish(t *testing.T) {
	call, m, _ := newProvidersServer(t)
	release := make(chan struct{})
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })
	done := make(chan map[string]any, 1)
	go func() {
		_, body := call("GET", "/v1/m/agenttools/providers?background=true", nil)
		done <- body
	}()
	select {
	case body := <-done:
		if body["refreshing"] != true {
			t.Errorf("unfinished probes were not reported: %v", body)
		}
		if len(body["providers"].([]any)) != 0 {
			t.Error("a cold probe fabricated a provider snapshot")
		}
		_, again := call("GET", "/v1/m/agenttools/providers?background=true", nil)
		if again["refreshing"] != true {
			t.Error("a concurrent background read lost the pending probe")
		}
		close(release)
	case <-time.After(300 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("background providers read waited for the native probes")
	}
	// The synchronous contract still waits for and returns the tool's answer.
	if p := providers(t, call, "")["claude"]; p["state"] != "ready" {
		t.Fatalf("completed snapshot = %v", p)
	}
	m.wg.Wait()
	_, body := call("GET", "/v1/m/agenttools/providers?background=true", nil)
	if body["refreshing"] != false {
		t.Fatalf("completed probe stayed refreshing: %v", body)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(m.program("claude")), "claude.runs"))
	if err != nil || strings.Count(string(b), "--version") != 1 {
		t.Fatalf("background and synchronous reads did not share one probe: %s %v", b, err)
	}
}

func TestBackgroundProvidersKeepTheLastAnswerAndReportRefreshFailure(t *testing.T) {
	call, m, _ := newProvidersServer(t)
	providers(t, call, "")
	if err := os.WriteFile(os.Getenv("CLAUDE_CONFIG_DIR")+"/.stub-garbage", nil, 0600); err != nil {
		t.Fatal(err)
	}
	ageProviders(m)
	release := make(chan struct{})
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })
	_, body := call("GET", "/v1/m/agenttools/providers?background=true", nil)
	close(release)
	if body["refreshing"] != true {
		t.Fatalf("refresh not reported: %v", body)
	}
	found := false
	for _, raw := range body["providers"].([]any) {
		p := raw.(map[string]any)
		if p["driver"] == "claude" {
			found = true
			if p["state"] != "ready" || p["stale"] != true || p["email"] != "f***@example.test" {
				t.Fatalf("last checked answer lost: %v", p)
			}
		}
	}
	if !found {
		t.Fatal("background refresh omitted the last checked answer")
	}
	if p := providers(t, call, "")["claude"]; p["state"] != "ready" || p["stale"] != true || !strings.Contains(p["error"].(string), "auth status") {
		t.Fatalf("failed refresh hidden: %v", p)
	}
}

func TestBackgroundProvidersStillAuthorizeTheTenantAndValidateTheOption(t *testing.T) {
	call, m, home := newProvidersServer(t)
	providers(t, call, "") // Warm default snapshots cannot authorize a tenant.
	m.SetLoginHome(func(context.Context, model.TenantID, string, string) (string, string, error) {
		return "", "", errors.New("organization is no longer served")
	})
	if code, _ := call("GET", "/v1/m/agenttools/providers?background=true&tenant_id="+url.QueryEscape(filepath.Base(home)), nil); code != 503 {
		t.Fatalf("revoked tenant = %d, want 503", code)
	}
	if code, _ := call("GET", "/v1/m/agenttools/providers?background=invalid", nil); code != 400 {
		t.Fatalf("invalid option = %d, want 400", code)
	}
}

// The stubs answer the way the real tools do (measured on Claude Code 2.1.289 and
// Codex 0.160.0) and exit 9 when a probe is not read-only: a secret in the
// environment, hooks or MCP left on. A stub answers a request only when the request
// asks for it, so a wrong subtype or method is a missing reply, not a pass.
const providerStubGuard = `[ -z "$ANTHROPIC_API_KEY$OPENAI_API_KEY$GH_TOKEN" ] || exit 9
echo "$*" >> "$(dirname "$0")/$(basename "$0").runs"
`

const providerClaude = `#!/bin/sh
` + providerStubGuard + `
case "$1" in
--version) echo "2.1.289 (Claude Code)"; exit 0;;
-p)
  case "$*" in *disableAllHooks*) ;; *) exit 9;; esac
  case "$*" in *--strict-mcp-config*) ;; *) exit 9;; esac
  echo '{"type":"system","subtype":"init"}'
  while read line; do
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    case "$line" in
    *'"subtype":"get_usage"'*)
      if [ -f "$CLAUDE_CONFIG_DIR/.stub-api-key" ]; then
        printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"subscription_type":null,"rate_limits_available":false,"rate_limits":null}}}\n' "$id"
      else
        printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"subscription_type":"max","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":40,"resets_at":"x"},"iguana_necktie":{"utilization":0},"limits":[{"kind":"session","group":"session","percent":40,"severity":"normal","resets_at":"2026-10-06T01:00:00.000000+00:00","scope":null,"is_active":false},{"kind":"weekly_all","group":"weekly","percent":78,"severity":"warning","resets_at":"2026-10-09T01:00:00+00:00","scope":null,"is_active":true},{"kind":"weekly_scoped","group":"weekly","percent":0,"severity":"normal","resets_at":"2026-10-09T01:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]}}}}\n' "$id"
      fi;;
    *'"subtype":"initialize"'*)
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"commands":[],"models":[{"value":"default","displayName":"Default (recommended)"},{"value":"opus","displayName":"Opus 5.5"}]}}}\n' "$id";;
    esac
  done
  exit 0;;
esac
case "$1 $2" in
"auth status")
  if [ -f "$CLAUDE_CONFIG_DIR/.stub-garbage" ]; then echo "boom" >&2; echo "segmentation fault"; exit 2; fi
  if [ -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","email":"fran@example.test","orgName":"fran@example.test'"'"'s Organization","subscriptionType":"max"}'; exit 0; fi
  echo '{"loggedIn":false,"authMethod":"none"}'; exit 1;;
"auth login")
  echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=stub"
  read code
  if [ "$code" = "good-code" ]; then mkdir -p "$CLAUDE_CONFIG_DIR"; echo '{}' > "$CLAUDE_CONFIG_DIR/.credentials.json"; exit 0; fi
  exit 1;;
esac
exit 2
`

const providerCodex = `#!/bin/sh
` + providerStubGuard + `
case "$1" in
--version) echo "codex-cli 0.160.0"; exit 0;;
app-server)
  case "$*" in *"--disable hooks"*) ;; *) exit 9;; esac
  case "$*" in *"mcp_servers={}"*) ;; *) exit 9;; esac
  [ -f "$CODEX_HOME/.stub-silent" ] && exit 3
  ready=
  while read line; do
    case "$line" in
    *'"method":"initialize"'*)
      echo '{"method":"remoteControl/status/changed","params":{"status":"disabled"}}'
      echo '{"id":1,"result":{"userAgent":"stub","codexHome":"x","platformFamily":"unix","platformOs":"linux"}}';;
    *'"method":"initialized"'*) ready=1;;
    *'"method":"account/read"'*)
      [ -n "$ready" ] && echo '{"id":2,"result":{"account":{"type":"chatgpt","email":"fran@example.test","planType":"promax"},"requiresOpenaiAuth":true}}';;
    *'"method":"account/rateLimits/read"'*)
      [ -n "$ready" ] && echo '{"id":3,"result":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1791756824},"secondary":{"usedPercent":12.5,"windowDurationMins":300,"resetsAt":1791700000}}}}';;
    *'"method":"model/list"'*)
      if [ -f "$CODEX_HOME/.stub-models-error" ]; then echo '{"id":4,"error":{"code":-32000,"message":"model list unavailable"}}';
      else [ -n "$ready" ] && echo '{"id":4,"result":{"data":[{"model":"gpt-6","displayName":"GPT-6","hidden":false},{"model":"internal-only","displayName":"x","hidden":true}],"nextCursor":null}}'; fi;;
    esac
  done
  exit 0;;
esac
case "$1 $2" in
"login status")
  # The real codex-cli 0.160 prints this on stderr, not stdout.
  if [ -f "$CODEX_HOME/auth.json" ]; then echo "Logged in using ChatGPT" >&2; exit 0; fi
  echo "Not logged in" >&2; exit 1;;
esac
exit 2
`

const providerGrok = `#!/bin/sh
` + providerStubGuard + `
case "$1" in
--version) echo "grok 1.0.13 (5e9a58528b76)"; exit 0;;
models) printf 'You are not authenticated.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n  - grok-4.7-fast\n'; exit 0;;
esac
exit 2
`

const providerOpenCode = `#!/bin/sh
` + providerStubGuard + `
case "$1 $2" in
"--version ") echo "1.18.30"; exit 0;;
"auth list")
  printf '┌  Credentials\n│\n'
  if [ -f "$XDG_DATA_HOME/opencode/auth.json" ]; then printf '●  OpenAI oauth\n│\n└  1 credentials\n'; else printf '└  0 credentials\n'; fi
  exit 0;;
"models ") printf 'openai/gpt-5\nanthropic/claude-sonnet\n'; exit 0;;
esac
exit 2
`

// newProvidersServer serves the module with the provider stubs. The engine user's own
// logins (the decoy home of newSignInServer) are signed in for every tool.
func newProvidersServer(t *testing.T) (call func(method, path string, body any) (int, map[string]any), m *Module, tenantHome string) {
	t.Helper()
	call, home := newSignInServer(t, func(mod *Module, bin string) {
		m = mod
		for name, script := range map[string]string{"claude": providerClaude, "codex": providerCodex, "grok": providerGrok, "opencode": providerOpenCode} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	})
	return call, m, home
}

// providers reads GET providers and returns the snapshots by instance.
func providers(t *testing.T, call func(string, string, any) (int, map[string]any), query string) map[string]map[string]any {
	t.Helper()
	code, body := call("GET", "/v1/m/agenttools/providers"+query, nil)
	if code != 200 {
		t.Fatalf("providers = %d %v", code, body)
	}
	out := map[string]map[string]any{}
	for _, raw := range body["providers"].([]any) {
		p := raw.(map[string]any)
		out[p["instance"].(string)] = p
	}
	return out
}

// ageProviders makes the cached snapshots old enough to be probed again.
func ageProviders(m *Module) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.providerEntries {
		e.attempted = time.Now().Add(-2 * providerRefresh)
	}
}

func limitsByLabel(p map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, raw := range p["limits"].([]any) {
		l := raw.(map[string]any)
		out[l["label"].(string)] = l
	}
	return out
}

func modelIDs(p map[string]any) []string {
	var ids []string
	for _, raw := range p["models"].([]any) {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	return ids
}

func TestProvidersShowTheEngineUsersOwnLogins(t *testing.T) {
	// A secret in the engine's environment must not reach any probe (the stubs exit 9).
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-reach-the-tool")
	t.Setenv("OPENAI_API_KEY", "sk-must-not-reach-the-tool")
	call, _, _ := newProvidersServer(t)
	_, raw := call("GET", "/v1/m/agenttools/providers", nil)
	if b, _ := json.Marshal(raw); strings.Contains(string(b), "fran@example.test") {
		t.Fatalf("the response carries an unmasked email: %s", b)
	}
	all := providers(t, call, "")

	claude := all["claude"]
	if claude["state"] != "ready" || claude["default"] != true || claude["version"] != "2.1.289 (Claude Code)" ||
		claude["email"] != "f***@example.test" || claude["plan"] != "max" || claude["auth_method"] != "claude.ai" {
		t.Fatalf("claude = %v", claude)
	}
	limits := limitsByLabel(claude)
	if len(limits) != 3 || limits["5-hour"]["percent"] != 40.0 || limits["Weekly"]["percent"] != 78.0 || limits["Weekly"]["severity"] != "warning" ||
		limits["Weekly (Fable)"]["percent"] != 0.0 || limits["5-hour"]["resets_at"] != "2026-10-06T01:00:00Z" {
		t.Fatalf("claude limits = %v", claude["limits"])
	}
	if ids := modelIDs(claude); len(ids) != 2 || ids[0] != "default" || ids[1] != "opus" {
		t.Fatalf("claude models = %v", ids)
	}
	if src := claude["source"].(string); !strings.Contains(src, "claude auth status --json") || !strings.Contains(src, "get_usage") {
		t.Fatalf("claude source = %q", src)
	}

	codex := all["codex"]
	if codex["state"] != "ready" || codex["email"] != "f***@example.test" || codex["plan"] != "promax" || codex["auth_method"] != "chatgpt" || codex["version"] != "codex-cli 0.160.0" {
		t.Fatalf("codex = %v", codex)
	}
	limits = limitsByLabel(codex)
	if len(limits) != 2 || limits["Weekly"]["percent"] != 100.0 || limits["5-hour"]["percent"] != 12.5 || limits["Weekly"]["resets_at"] != "2026-10-11T22:13:44Z" {
		t.Fatalf("codex limits = %v", codex["limits"])
	}
	if ids := modelIDs(codex); len(ids) != 1 || ids[0] != "gpt-6" {
		t.Fatalf("codex models (the hidden one must be left out) = %v", ids)
	}

	grok := all["grok"]
	if grok["state"] != "unknown" || grok["email"] != nil || len(grok["notes"].([]any)) != 1 {
		t.Fatalf("grok must say it reports no sign-in state: %v", grok)
	}
	if ids := modelIDs(grok); len(ids) != 2 || ids[0] != "grok-4.7" || ids[1] != "grok-4.7-fast" {
		t.Fatalf("grok models = %v", ids)
	}
	if oc := all["opencode"]; oc["state"] != "ready" || len(modelIDs(oc)) != 2 {
		t.Fatalf("opencode = %v", oc)
	}
}

func TestProvidersNameTheToolsOwnLoginCommandWhenNotSignedIn(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	for _, f := range []string{os.Getenv("CLAUDE_CONFIG_DIR") + "/.credentials.json", os.Getenv("CODEX_HOME") + "/auth.json"} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	all := providers(t, call, "")
	if p := all["claude"]; p["state"] != "not_signed_in" || p["next_command"] != "claude auth login --claudeai" || len(p["limits"].([]any)) != 0 || p["email"] != nil {
		t.Fatalf("claude = %v", p)
	}
	if p := all["codex"]; p["state"] != "not_signed_in" || p["next_command"] != "codex login --device-auth" {
		t.Fatalf("codex = %v", p)
	}
}

func TestProvidersSayNotInstalledAndNameTheInstallCommand(t *testing.T) {
	call, m, _ := newProvidersServer(t)
	m.SetProgramResolver(func(string) string { return "" })
	if p := providers(t, call, "")["claude"]; p["state"] != "not_installed" || p["installed"] != false || p["next_command"] != "olivares tool install claude" {
		t.Fatalf("claude = %v", p)
	}
}

// A tool whose answer cannot be read is an error naming the command, never "not signed in".
func TestProvidersReportAnUnreadableAnswerAsAnErrorNotAsSignedOut(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	if err := os.WriteFile(os.Getenv("CLAUDE_CONFIG_DIR")+"/.stub-garbage", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := providers(t, call, "")["claude"]
	msg, _ := p["error"].(string)
	if p["state"] != "error" || !strings.HasPrefix(msg, "claude auth status --json: ") {
		t.Fatalf("claude = %v", p)
	}
}

func TestProvidersAreRefreshedAtMostOncePerMinuteAndKeepTheLastGoodOneWhenARefreshFails(t *testing.T) {
	call, m, _ := newProvidersServer(t)
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(m.program("claude")), "claude.runs"))
		return strings.Count(string(b), "--version")
	}
	providers(t, call, "")
	providers(t, call, "")
	if n := runs(); n != 1 {
		t.Fatalf("two reads inside a minute ran the tool %d times, want 1", n)
	}

	if err := os.WriteFile(os.Getenv("CLAUDE_CONFIG_DIR")+"/.stub-garbage", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p := providers(t, call, "")["claude"]; p["stale"] != nil || runs() != 1 {
		t.Fatalf("a read inside the minute must not probe again: %v", p)
	}
	ageProviders(m)
	p := providers(t, call, "")["claude"]
	msg, _ := p["error"].(string)
	if runs() != 2 || p["stale"] != true || !strings.HasPrefix(msg, "claude auth status --json: ") || p["state"] != "ready" || p["email"] != "f***@example.test" || len(p["limits"].([]any)) != 3 {
		t.Fatalf("a failed refresh must keep the last good snapshot, marked stale: %v", p)
	}

	if err := os.Remove(os.Getenv("CLAUDE_CONFIG_DIR") + "/.stub-garbage"); err != nil {
		t.Fatal(err)
	}
	ageProviders(m)
	if p := providers(t, call, "")["claude"]; p["stale"] != nil || p["error"] != nil || p["state"] != "ready" {
		t.Fatalf("a good refresh must clear the stale mark: %v", p)
	}
}

func TestProvidersSayWhenTheToolReportsNoPlanLimits(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	if err := os.WriteFile(os.Getenv("CLAUDE_CONFIG_DIR")+"/.stub-api-key", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := providers(t, call, "")["claude"]
	notes, _ := p["notes"].([]any)
	if p["state"] != "ready" || len(p["limits"].([]any)) != 0 || len(notes) != 1 || !strings.Contains(notes[0].(string), "no plan limits") || p["plan"] != "max" {
		t.Fatalf("claude = %v", p)
	}
}

func TestProvidersKeepWhatTheToolReportedWhenOneCallFails(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	if err := os.WriteFile(os.Getenv("CODEX_HOME")+"/.stub-models-error", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := providers(t, call, "")["codex"]
	notes, _ := p["notes"].([]any)
	if p["state"] != "ready" || p["plan"] != "promax" || len(p["limits"].([]any)) != 2 || len(modelIDs(p)) != 0 ||
		len(notes) != 1 || !strings.Contains(notes[0].(string), "models:") || !strings.Contains(notes[0].(string), "model list unavailable") {
		t.Fatalf("codex = %v", p)
	}
}

// A tool that signs in but never answers its protocol still shows what its own status said.
func TestProvidersNoteAToolThatDoesNotAnswerItsProtocol(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	if err := os.WriteFile(os.Getenv("CODEX_HOME")+"/.stub-silent", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := providers(t, call, "")["codex"]
	notes, _ := p["notes"].([]any)
	if p["state"] != "ready" || p["email"] != nil || len(p["limits"].([]any)) != 0 || len(notes) != 1 || !strings.HasPrefix(notes[0].(string), "app-server: codex app-server") {
		t.Fatalf("codex = %v", p)
	}
}

// A tenant this node does not serve is an error, as for a sign-in status read, not a
// silently shorter list.
func TestProvidersRefuseATenantTheNodeDoesNotServe(t *testing.T) {
	call, m, home := newProvidersServer(t)
	m.SetLoginHome(func(context.Context, model.TenantID, string, string) (string, string, error) {
		return "", "", errors.New("that organization is not served by this node")
	})
	if code, _ := call("GET", "/v1/m/agenttools/providers?tenant_id="+url.QueryEscape(filepath.Base(home)), nil); code != 503 {
		t.Fatalf("status = %d, want 503", code)
	}
	if got := providers(t, call, ""); got["claude"] == nil {
		t.Fatalf("without tenant_id the engine user's own logins are still listed: %v", got)
	}
}

func TestProvidersListALoginMadeThroughOlivaresAsAnExtraInstance(t *testing.T) {
	call, m, home := newProvidersServer(t)
	tenant := "?tenant_id=" + url.QueryEscape(filepath.Base(home))
	for instance := range providers(t, call, tenant) {
		if strings.HasSuffix(instance, "/olivares") {
			t.Fatalf("%s: a tool nobody signed in through Olivares must not be listed as an extra instance", instance)
		}
	}
	// Grok Build reports no state: its session file, which is never opened, is the sign.
	if err := os.MkdirAll(filepath.Join(home, "grok", ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "grok", ".grok", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageProviders(m)
	if g := providers(t, call, tenant)["grok/olivares"]; g == nil || g["state"] != "unknown" || g["default"] != false {
		t.Fatalf("grok/olivares = %v", g)
	}

	// Signing in through Olivares makes the instance appear at once, not a minute later.
	_, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude"})
	call("POST", "/v1/m/agenttools/sign-in/"+s["id"].(string)+"/code", map[string]string{"code": "good-code"})
	pollSignIn(t, call, s["id"].(string), "signed_in")
	all := providers(t, call, tenant)
	extra := all["claude/olivares"]
	if extra == nil || extra["default"] != false || extra["state"] != "ready" || extra["email"] != "f***@example.test" ||
		extra["config_dir"] != filepath.Join(home, "claude", ".claude") {
		t.Fatalf("claude/olivares = %v", extra)
	}
	if all["claude"]["config_dir"] == extra["config_dir"] || all["claude"]["default"] != true {
		t.Fatalf("the engine user's own login must stay its own instance: %v", all["claude"])
	}
	if _, ok := providers(t, call, "")["claude/olivares"]; ok {
		t.Fatal("without tenant_id only the engine user's own logins are listed")
	}
}

func TestProvidersRefuseABadTenant(t *testing.T) {
	call, _, _ := newProvidersServer(t)
	if code, _ := call("GET", "/v1/m/agenttools/providers?tenant_id=nope", nil); code != 400 {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestMaskEmailKeepsTheFirstLetterAndTheDomain(t *testing.T) {
	const unicodeEmail = "Émile@x.org"      // language-data: multibyte email masking input
	const maskedUnicodeEmail = "É***@x.org" // language-data: multibyte email masking expectation
	for in, want := range map[string]string{"fran@example.test": "f***@example.test", unicodeEmail: maskedUnicodeEmail, "nodomain": "***", "@x.org": "***", "": "***"} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexWindowLabels(t *testing.T) {
	for minutes, want := range map[int]string{10080: "Weekly", 300: "5-hour", 1440: "1-day", 20160: "14-day", 45: "45-minute"} {
		if got := (codexWindow{Minutes: minutes}).limit().Label; got != want {
			t.Errorf("%d minutes = %q, want %q", minutes, got, want)
		}
	}
}
