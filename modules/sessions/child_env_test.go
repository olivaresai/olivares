// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The child's environment comes from five sources: the engine's own environment by
// name (env_allow over the safe base), the launch's own values, a registered
// driver's non-secret variables, the launch gate's grant and the vault secrets the
// launch names. This table pins which source may give the child which variable,
// through the entry point each source is checked at, one column per source:
//
//	base   the engine's variable reaches the child with no env_allow
//	host   the engine's variable reaches the child when env_allow names it
//	allow  env_allow accepted when the launch is created
//	aP     env_allow on a profiled launch
//	aOC    env_allow on an OpenCode profiled launch
//	aG     env_allow beside git_read
//	secret a vault secret may set it (secret_env)
//	sG     secret_env beside git_read
//	gP     the gate's grant on a profiled Claude launch
//	gOC    the gate's grant on a profiled OpenCode launch
//	drv    a registered driver's LaunchEnv (Codex)
//	drvOC  OpenCode's LaunchEnv
//	cred   a governed credential adapter's variables (Codex, managed injection)
//	credOC the same on OpenCode
//	credGm the same on Gemini CLI
//	credGk the same on Grok Build
//	rec    the credential of an anthropic record from Providers
//
// Verdicts: '.' given or accepted; 'x' refused or withheld; 'e' refused because the
// engine reads it as a secret; 'p' refused because the provider profile owns it;
// 'o' refused because OpenCode's profiled launch owns the XDG mapping; 'g' refused
// because git_read sets git's configuration; 'n' refused because a credential
// adapter does not own it.
var childEnvRuleRows = []struct{ want, name string }{
	// base host allow aP aOC aG secret sG gP gOC drv drvOC cred credOC credGm credGk rec
	{". . . . . . x . . . . . n n n n x", "PATH"},
	{". . . p . . x . p p x x p p p p x", "HOME"},
	{". . . . . . x . . . . . n n n n x", "TMPDIR"},
	{". . . . . . x . . . . . n n n n x", "LANG"},
	{". . . . . . x . . . . . n n n n x", "LC_ALL"},
	{". . . . . . x . . . . . n n n n x", "LC_CTYPE"},
	{". . . . . . x . . . . . n n n n x", "TERM"},
	{". . . . . . x . . . . . n n n n x", "TZ"},
	{". . . . . . x . . . . . n n n n x", "USER"},
	{". . . . . . x . . . . . n n n n x", "SHELL"},
	{"x x x . . . x . . . . . n n n n x", "OLIVARES_ENGINE_KEY"},
	{"x x x . . . x . x x . . n n n n x", "OLIVARES_WORK_TOKEN"},
	{"x x x . . . x . x x . . n n n n x", "OLIVARES_COMMUNICATION_TOKEN"},
	{"x x x . . . x . . . . . n n n n x", "OLIVARES_HOOK_PEP_URL"},
	{"x x x . . . x . x x . . n n n n .", "ANTHROPIC_API_KEY"},
	{"x x x . . . x . x x . . n n n n x", "CLAUDE_CODE_USE_BEDROCK"},
	{"x x x p . . x . p p x x p p p p x", "CLAUDE_CONFIG_DIR"},
	{"x . . . . . x . . . . . n n n n x", "CLAUDE_PLUGIN_ROOT"},
	{"x x x p . . x . x x x x p p p p x", "CODEX_HOME"},
	{"x x x . . . x . x x . . . . n n x", "OPENAI_API_KEY"},
	{"x x x . . . x . x x . . . n n n x", "OPENAI_BASE_URL"},
	{"x x x . . . x . x x . . n n n n x", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"},
	{"x x x p . . x . x x x x p p p p x", "GROK_HOME"},
	{"x x x . . . x . x x . . n . n . x", "XAI_API_KEY"},
	{"x x x . . . x . x x . . n n n . x", "XAI_BASE_URL"},
	{"x x x . . . x . x x . . n n n n x", "GROK_API_KEY"},
	{"x x x p . . x . x x x x p p p p x", "OPENCODE_CONFIG"},
	{"x x x p . . x . x x x . p p p p x", "OPENCODE_CONFIG_CONTENT"},
	{"x x x p . . x . x x x x p p p p x", "OPENCODE_CONFIG_DIR"},
	{"x x x p . . x . x x x x p p p p x", "OPENCODE_TUI_CONFIG"},
	{"x x x . . . x . x x . . n n n n x", "OPENCODE_DISABLE_AUTOUPDATE"},
	{"x x x p . . x . x x x x p p p p x", "GEMINI_CLI_HOME"},
	{"x x x . . . x . x x . . n n n n x", "GEMINI_CLI_SYSTEM_SETTINGS_PATH"},
	{"x . . . . . . . . . . . . . . . x", "GEMINI_API_KEY"},
	{"x . . . o . x . . o . x n n n n x", "XDG_CONFIG_HOME"},
	{"x . . . o . x . . o . x n n n n x", "XDG_RUNTIME_DIR"},
	{"x . . . . . x . . . . . n n n n x", "XDG_SESSION_TYPE"},
	{"x . . . . . x . . . . . n n n n x", "LD_PRELOAD"},
	{"x . . . . . x . . . . . n n n n x", "DYLD_INSERT_LIBRARIES"},
	{"x x x . . . x . x x . . n n n n x", "DISABLE_AUTOUPDATER"},
	{"x x e . . . . . . . . . n n n n x", "AWS_SECRET_ACCESS_KEY"},
	{"x x e . . . . . . . . . n n n n x", "DATABASE_URL"},
	{"x x e . . . . . . . . . n n n n x", "VAULT_TOKEN"},
	{"x . . . . g . g . . . . n n n n x", "GIT_CONFIG_COUNT"},
	{"x . . . . g . g . . . . n n n n x", "GIT_CONFIG_KEY_0"},
	{"x . . . . . . . . . . . . . . . x", "MY_PROJECT_FLAG"},
	{"x . . . . . . . . . . . . . . . x", "OLLAMA_HOST"},
	{"x x x . . . x . x x . . x x x x x", "1BAD"},
}

func TestChildEnvNameRules(t *testing.T) {
	for _, row := range childEnvRuleRows {
		t.Run(row.name, func(t *testing.T) {
			got := strings.Join([]string{
				childEnvFromHost(t, row.name, nil),
				childEnvFromHost(t, row.name, []string{row.name}),
				childEnvFromAllow(row.name),
				childEnvFromProfiledAllow(t, row.name),
				childEnvFromOpenCodeAllow(row.name),
				childEnvBesideGitRead(t, CreateRunParams{EnvAllow: []string{row.name}}),
				childEnvFromSecret(row.name),
				childEnvBesideGitRead(t, CreateRunParams{SecretEnv: []SecretEnvRef{{Env: row.name, Secret: "env/probe"}}}),
				childEnvFromGate(t, row.name, providerDriverClaude),
				childEnvFromGate(t, row.name, providerDriverOpenCode),
				childEnvFromDriver(t, NewCodexDriver(), row.name),
				childEnvFromDriver(t, NewOpenCodeDriver(), row.name),
				childEnvFromAdapter(t, providerDriverCodex, row.name),
				childEnvFromAdapter(t, providerDriverOpenCode, row.name),
				childEnvFromAdapter(t, providerDriverGemini, row.name),
				childEnvFromAdapter(t, providerDriverGrok, row.name),
				childEnvFromRecord(row.name),
			}, " ")
			if got != row.want {
				t.Errorf("%s:\n got %s\nwant %s\n     base host allow aP aOC aG secret sG gP gOC drv drvOC cred credOC credGm credGk rec", row.name, got, row.want)
			}
		})
	}
}

// childEnvRuleProbe is the value every source offers, so the child's environment
// shows which offer reached it.
const childEnvRuleProbe = "probe-7f3a"

// childEnvFromHost puts the probe in the engine's environment only while the
// child's environment is built: the other columns need a usable TMPDIR and HOME.
func childEnvFromHost(t *testing.T, name string, allow []string) string {
	old, had := os.LookupEnv(name)
	if err := os.Setenv(name, childEnvRuleProbe); err != nil {
		t.Fatal(err)
	}
	env := sanitizedEnv(allow, nil)
	if had {
		_ = os.Setenv(name, old)
	} else {
		_ = os.Unsetenv(name)
	}
	if slices.Contains(env, name+"="+childEnvRuleProbe) {
		return "."
	}
	return "x"
}

func childEnvFromAllow(name string) string {
	err := validateEnvAllow([]string{name})
	switch {
	case err == nil:
		return "."
	case strings.Contains(err.Error(), "the engine reads it as a secret"):
		return "e"
	}
	return "x"
}

func childEnvFromProfiledAllow(t *testing.T, name string) string {
	p := CreateRunParams{ProviderProfileRef: "ppf_probe", EnvAllow: []string{name}}
	if err := New().resolveLaunchProfileInto(t.Context(), "fixture", &p); err != nil &&
		strings.Contains(err.Error(), "for a profiled launch: the profile owns it") {
		return "p"
	}
	return "."
}

func childEnvFromOpenCodeAllow(name string) string {
	if validateOpenCodeEnvAllow(providerDriverOpenCode, []string{name}) != nil {
		return "o"
	}
	return "."
}

func childEnvBesideGitRead(t *testing.T, p CreateRunParams) string {
	m := New()
	m.GitRead, m.GitReadDataDir = newFakeGitRead("https://github.com/acme/widgets.git"), t.TempDir()
	p.GitRead, p.MayUseSecretEnv = gitReadBinding, true
	if err := m.refuseGitReadFor(p); err != nil {
		if !strings.Contains(err.Error(), "cannot be combined: the read credential sets git's configuration") {
			t.Fatalf("git_read refused %+v for another reason: %v", p, err)
		}
		return "g"
	}
	return "."
}

func childEnvFromSecret(name string) string {
	if validateSecretEnv([]SecretEnvRef{{Env: name, Secret: "env/probe"}}, nil) != nil {
		return "x"
	}
	return "."
}

func childEnvFromGate(t *testing.T, name, driver string) string {
	gate := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		return LaunchDecision{Allowed: true, InjectEnv: []EnvVar{{Name: name, Value: childEnvRuleProbe}}}, nil
	})
	pr, err := New(WithLaunchGate(gate)).preflight(t.Context(), "fixture", LaunchIntent{ProviderProfileRef: "ppf_probe"}, StopDims{}, driver)
	switch {
	case err == nil:
		if len(pr.injectEnv) != 1 || pr.injectEnv[0].Name != name {
			t.Fatalf("the gate's grant became %+v", pr.injectEnv)
		}
		return "."
	case strings.Contains(err.Error(), "which the provider profile owns"):
		return "p"
	case strings.Contains(err.Error(), "reserved for the OpenCode profiled launch mapping"):
		return "o"
	}
	return "x"
}

// launchEnvProbeDriver is a registered driver whose LaunchEnv also offers one
// variable, after the driver's own.
type launchEnvProbeDriver struct {
	ProviderDriver
	offer EnvVar
}

func (d launchEnvProbeDriver) LaunchEnv(l DriverLaunch) []EnvVar {
	var env []EnvVar
	if own, ok := d.ProviderDriver.(ProviderDriverLaunchEnv); ok {
		env = own.LaunchEnv(l)
	}
	return append(env, d.offer)
}

func childEnvFromDriver(t *testing.T, driver ProviderDriver, name string) string {
	offer := EnvVar{Name: name, Value: childEnvRuleProbe}
	m := New(WithProviderDriver(launchEnvProbeDriver{ProviderDriver: driver, offer: offer}))
	p := CreateRunParams{WorkspaceDir: t.TempDir(), ProviderHome: &ProviderHomeSnapshot{
		Driver: driver.Key(), ConfigHome: "/homes/probe/config", UserHome: "/homes/probe/user",
	}}
	spec := m.childSpec(p, childDecision{})
	if slices.Contains(spec.Env, offer) {
		return "."
	}
	return "x"
}

// childEnvAdapter is a governed managed-injection adapter that offers one variable.
type childEnvAdapter struct{ offer EnvVar }

func (a childEnvAdapter) Mint(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
	return ProviderCredential{ID: "pc-probe", Scheme: "probe", NotAfter: farFuture, Env: []EnvVar{a.offer}}, nil
}

func childEnvFromAdapter(t *testing.T, driver, name string) string {
	offer := EnvVar{Name: name, Value: childEnvRuleProbe}
	m := New(WithProviderCredentialSource(driver, childEnvAdapter{offer}))
	p := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: driver, AuthSource: AuthSourceManagedInjection}}
	if driver == providerDriverGemini {
		p.ProviderHome = geminiTestHome(t, nil)
		p.ProviderHome.AuthSource = AuthSourceManagedInjection
	}
	_, env, err := m.mintLaunchAuthority(t.Context(), "fixture", "run", p)
	switch {
	case err == nil:
		if !slices.Contains(env, offer) {
			t.Fatalf("the %s adapter's %s did not reach the launch: %+v", driver, name, env)
		}
		return "."
	case strings.Contains(err.Error(), "which the provider profile owns"):
		return "p"
	case strings.Contains(err.Error(), "which it does not own"):
		return "n"
	case strings.Contains(err.Error(), "invalid or duplicate explicit environment value"):
		return "x"
	}
	t.Fatalf("the %s adapter offering %s was refused for another reason: %v", driver, name, err)
	return ""
}

func childEnvFromRecord(name string) string {
	if validateRecordCredentialEnv(ProviderKindAnthropic, []EnvVar{{Name: name, Value: childEnvRuleProbe}}) != nil {
		return "x"
	}
	return "."
}

// The bounds each source is held to, at and one past the limit.
func TestChildEnvLimits(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("PROBE_%d", i)
		}
		return out
	}
	vars := func(n int, value string) []EnvVar {
		out := make([]EnvVar, n)
		for i, name := range names(n) {
			out[i] = EnvVar{Name: name, Value: value}
		}
		return out
	}
	secrets := func(n int) []SecretEnvRef {
		out := make([]SecretEnvRef, n)
		for i, name := range names(n) {
			out[i] = SecretEnvRef{Env: name, Secret: "env/probe"}
		}
		return out
	}
	gate := func(env []EnvVar) error {
		g := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
			return LaunchDecision{Allowed: true, InjectEnv: env}, nil
		})
		_, err := New(WithLaunchGate(g)).preflight(t.Context(), "fixture", LaunchIntent{}, StopDims{}, "")
		return err
	}
	big := strings.Repeat("v", 64*1024)
	for _, tc := range []struct {
		name  string
		err   error
		valid bool
	}{
		{"env_allow 64 names", validateEnvAllow(names(64)), true},
		{"env_allow 65 names", validateEnvAllow(names(65)), false},
		{"secret_env 32 secrets", validateSecretEnv(secrets(32), nil), true},
		{"secret_env 33 secrets", validateSecretEnv(secrets(33), nil), false},
		{"gate 64 values", gate(vars(64, "v")), true},
		{"gate 65 values", gate(vars(65, "v")), false},
		{"gate value of 64 KiB", gate(vars(1, big)), true},
		{"gate value past 64 KiB", gate(vars(1, big+"v")), false},
		{"gate value with NUL", gate(vars(1, "a\x00b")), false},
		{"gate name twice", gate(append(vars(1, "a"), vars(1, "b")...)), false},
		{"runner 128 values", validateExplicitEnv(vars(128, "v")), true},
		{"runner 129 values", validateExplicitEnv(vars(129, "v")), false},
		{"runner value past 64 KiB", validateExplicitEnv(vars(1, big+"v")), false},
		{"runner value with NUL", validateExplicitEnv(vars(1, "a\x00b")), false},
		{"runner name twice", validateExplicitEnv(append(vars(1, "a"), vars(1, "b")...)), false},
	} {
		if (tc.err == nil) != tc.valid {
			t.Errorf("%s: err=%v, want valid=%v", tc.name, tc.err, tc.valid)
		}
	}
}

// The families and homes the runtime refuses are exactly what the tools declare
// today; a new tool adds its own in core/driverfacts, and this list with it.
func TestChildEnvProviderFamilies(t *testing.T) {
	prefixes, homes := slices.Sorted(slices.Values(providerEnvPrefixes)), slices.Sorted(slices.Values(providerConfigHomeEnvs))
	if want := []string{"ANTHROPIC_", "CLAUDE_CODE_", "CODEX_", "GEMINI_CLI_", "GROK_", "OPENAI_", "OPENCODE_", "XAI_"}; !slices.Equal(prefixes, want) {
		t.Errorf("provider families = %v, want %v", prefixes, want)
	}
	if want := []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "GROK_HOME", "OPENCODE_CONFIG_DIR"}; !slices.Equal(homes, want) {
		t.Errorf("configuration homes = %v, want %v", homes, want)
	}
}
