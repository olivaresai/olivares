// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// #2888 part 2: an OpenCode session reaches only its provider's hosts. Measured on
// 1.19.4 behind a refusing proxy (the switches of #3053 on): a record-bound launch
// reaches only its record's endpoint; the own login on OpenCode Zen or Go reaches
// opencode.ai, on ChatGPT chatgpt.com and auth.openai.com (an expired token is
// refreshed there), and every own login reads OpenCode's catalog at models.opencode.ai.
// A launch whose model names no known provider keeps the host network, as before.
func TestOpenCodeSessionsAreHeldToTheirProviderHosts(t *testing.T) {
	m := New(WithConfinement([]string{"/srv/olivares"}, false), WithProviderDriver(NewOpenCodeDriver()))
	home := &ProviderHomeSnapshot{Driver: providerDriverOpenCode, AuthSource: AuthSourceAccountHome, ConfigHome: "/homes/p/config", UserHome: "/homes/p/user"}
	for _, tc := range []struct {
		name            string
		model           string
		cred            Credential
		providers, more []string
	}{
		{"record-bound", "claude-sonnet-5-5", Credential{bound: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}}, []string{"https://api.anthropic.com"}, nil},
		{"own login, Zen", "opencode/glm-5.3-flash-free", Credential{}, []string{"https://opencode.ai"}, []string{"https://models.opencode.ai"}},
		{"own login, Go", "opencode-go/glm-5.3-flash", Credential{}, []string{"https://opencode.ai"}, []string{"https://models.opencode.ai"}},
		{"own login, ChatGPT", "openai/gpt-5.4-mini", Credential{}, []string{"https://chatgpt.com"}, []string{"https://auth.openai.com", "https://models.opencode.ai"}},
		{"own login, unknown provider", "elsewhere/model", Credential{}, nil, nil},
		{"own login, no model", "", Credential{}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CreateRunParams{WorkspaceDir: "/srv/work", Model: tc.model, ProviderHome: home}
			spec := m.childSpec(p, childDecision{cred: tc.cred})
			pinned := slices.Contains(spec.Args, "--port")
			if tc.providers == nil {
				if spec.NetworkPolicy != nil || pinned {
					t.Fatalf("a launch with no known provider got a boundary: %+v %q", spec.NetworkPolicy, spec.Args)
				}
				return
			}
			if spec.NetworkPolicy == nil {
				t.Fatal("the session keeps the host network")
			}
			if !slices.Equal(spec.NetworkPolicy.Providers, tc.providers) || !slices.Equal(spec.NetworkPolicy.PublicProviders, tc.more) {
				t.Fatalf("policy = providers %v public %v, want %v %v", spec.NetworkPolicy.Providers, spec.NetworkPolicy.PublicProviders, tc.providers, tc.more)
			}
			// The own login's hosts are public: a resolver that answers one of them with a
			// loopback or private address does not open that address (#3082 review).
			if own := tc.cred.bound.Kind == ""; spec.NetworkPolicy.PublicOnly != own {
				t.Fatalf("PublicOnly = %v, want %v", spec.NetworkPolicy.PublicOnly, own)
			}
			// #3082: OpenCode's ACP mode calls its own server on loopback, and the proxy
			// cannot reach it. Its port is pinned in the session's own network and is the
			// only one NO_PROXY names (Bun matches host:port exactly, measured on 1.19.4).
			i := slices.Index(spec.Args, "--port")
			if i < 0 || i+1 >= len(spec.Args) || spec.Args[i+1] != "4096" || !slices.Equal(spec.NetworkPolicy.LoopbackPorts, []int{4096}) {
				t.Fatalf("args %q, loopback ports %v: want --port 4096 exempted alone", spec.Args, spec.NetworkPolicy.LoopbackPorts)
			}
		})
	}
}

// Without the filesystem confinement helper, a session that must be held to its
// provider's hosts does not start, and the refusal names the helper and the fix.
func TestOpenCodeOwnLoginWithoutTheConfinementHelperDoesNotStart(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome, WithConfinement(nil, false))
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-unconfined"})
	_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative,
		ProviderProfileRef: prof.Ref, Model: "opencode/glm-5.3-flash-free", Actor: "user:u1", ActorKind: model.ActorUser})
	if err == nil {
		t.Fatal("an own-login OpenCode session started with no network boundary")
	}
	for _, want := range []string{"filesystem confinement helper", "enable session confinement"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
}

// #3094: an own-login launch that names no model is held to the provider of the
// model the account's OpenCode configuration sets, the one OpenCode would use.
func TestOpenCodeOwnLoginWithNoModelIsHeldToTheConfiguredModel(t *testing.T) {
	m := New(WithConfinement([]string{"/srv/olivares"}, false), WithProviderDriver(NewOpenCodeDriver()))
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "opencode.json"), []byte(`{"model":"opencode-go/glm-5.3-flash"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home := &ProviderHomeSnapshot{Driver: providerDriverOpenCode, AuthSource: AuthSourceAccountHome, ConfigHome: config, UserHome: t.TempDir()}
	spec := m.childSpec(CreateRunParams{WorkspaceDir: "/srv/work", ProviderHome: home}, childDecision{})
	if spec.NetworkPolicy == nil || !slices.Equal(spec.NetworkPolicy.Providers, []string{"https://opencode.ai"}) {
		t.Fatalf("policy = %+v, want the configured model's provider", spec.NetworkPolicy)
	}
}

// #3094: an own-login launch whose hosts cannot be known does not start, and the
// refusal says what to choose (the bible's privacy rule: a session reaches only its
// provider, or it does not start).
func TestOpenCodeOwnLoginWithUnknownHostsDoesNotStart(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"anthropic/claude-sonnet-4", `"anthropic"`}, // offered by OpenCode, no own-login hosts known
		{"big-pickle", "names no provider"},
		{"", "--model"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
			if err := os.Remove(filepath.Join(prof.ConfigHome, "opencode.json")); err != nil {
				t.Fatal(err)
			}
			setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-unknown"})
			_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative,
				ProviderProfileRef: prof.Ref, Model: tc.model, Actor: "user:u1", ActorKind: model.ActorUser})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "OpenCode") {
				t.Fatalf("launch error = %v, want a named refusal with %s", err, tc.want)
			}
		})
	}
}

// openCodeConfiguredModel reads what OpenCode reads from OPENCODE_CONFIG_DIR
// (review of #3094): opencode.json, then opencode.jsonc over it; never config.json,
// a symlink, or anything that is not a small regular file.
func TestOpenCodeConfiguredModelReadsWhatOpenCodeReads(t *testing.T) {
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	write(dir, "opencode.json", `{"model":"opencode/a"}`)
	write(dir, "opencode.jsonc", `{"model":"openai/b"}`)
	if got := openCodeConfiguredModel(dir); got != "openai/b" {
		t.Fatalf("json and jsonc: %q, want the jsonc model", got)
	}
	dir = t.TempDir()
	write(dir, "opencode.json", `{"model":"opencode/a"}`)
	write(dir, "opencode.jsonc", `{"theme":"x"}`)
	if got := openCodeConfiguredModel(dir); got != "opencode/a" {
		t.Fatalf("jsonc without a model: %q, want the json model", got)
	}
	dir = t.TempDir()
	write(dir, "config.json", `{"model":"opencode/a"}`)
	if got := openCodeConfiguredModel(dir); got != "" {
		t.Fatalf("config.json: %q, OpenCode does not read it here", got)
	}
	dir = t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	write(filepath.Dir(target), "elsewhere.json", `{"model":"opencode/a"}`)
	if err := os.Symlink(target, filepath.Join(dir, "opencode.json")); err != nil {
		t.Fatal(err)
	}
	if got := openCodeConfiguredModel(dir); got != "" {
		t.Fatalf("symlink: %q, want none", got)
	}
	dir = t.TempDir()
	write(dir, "opencode.json", `{"model":"opencode/`+strings.Repeat("x", 300)+`"}`)
	if got := openCodeConfiguredModel(dir); got != "" {
		t.Fatalf("a %d-byte model was read, want none", len(got))
	}
}

// The configured model also narrows OpenCode's own providers, as a named one does.
func TestOpenCodeConfiguredModelNarrowsTheProviders(t *testing.T) {
	m := New(WithConfinement([]string{"/srv/olivares"}, false), WithProviderDriver(NewOpenCodeDriver()))
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "opencode.json"), []byte(`{"model":"opencode-go/glm-5.3-flash"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home := &ProviderHomeSnapshot{Driver: providerDriverOpenCode, AuthSource: AuthSourceAccountHome, ConfigHome: config, UserHome: t.TempDir()}
	spec := m.childSpec(CreateRunParams{WorkspaceDir: "/srv/work", ProviderHome: home}, childDecision{})
	for _, e := range spec.Env {
		if e.Name == envOpenCodeConfigContent && strings.Contains(e.Value, `"enabled_providers":["opencode-go"]`) {
			return
		}
	}
	t.Fatalf("inline configuration does not narrow to the configured model's provider: %v", spec.Env)
}

// A profile's registries are appended to a launch's policy (applyProfileRegistries):
// that must not write into the own-login host table, which every later own-login
// launch reads (review of #3095).
func TestOpenCodeOwnLoginPolicyDoesNotShareTheHostTable(t *testing.T) {
	before := slices.Clone(openCodeOwnLoginHosts[openCodeHostedProviderID])
	p := sessionNetworkPolicy(providerDriverOpenCode, BoundProvider{}, "opencode/big-pickle", nil, nil)
	p.Providers = append(p.Providers, "https://npm.tenant-a.example/")
	p.PublicProviders = append(p.PublicProviders, "https://hosts.tenant-a.example/")
	if got := openCodeOwnLoginHosts[openCodeHostedProviderID]; !slices.Equal(got, before) {
		t.Fatalf("host table = %v after one launch's appends, want %v", got, before)
	}
	next := sessionNetworkPolicy(providerDriverOpenCode, BoundProvider{}, "opencode/big-pickle", nil, nil)
	if !slices.Equal(next.Providers, before[:1]) || !slices.Equal(next.PublicProviders, before[1:]) {
		t.Fatalf("next launch's policy = %v %v, want %v", next.Providers, next.PublicProviders, before)
	}
}
