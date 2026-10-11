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
	// What makes the child run other code, trust another CA or reach another host:
	// the operator may forward the engine's own value, a secret or an adapter never sets it.
	{"x . . . . . x . . . . . n n n n x", "NODE_OPTIONS"},
	{"x . . . . . x . . . . . n n n n x", "PYTHONPATH"},
	{"x . . . . . x . . . . . n n n n x", "JAVA_TOOL_OPTIONS"},
	{"x . . . . . x . . . . . n n n n x", "BASH_ENV"},
	{"x . . . . . x . . . . . n n n n x", "PERL5LIB"},
	{"x . . . . . x . . . . . n n n n x", "RUBYOPT"},
	{"x . . . . . x . . . . . n n n n x", "GLIBC_TUNABLES"},
	{"x . . . . . x . . . . . n n n n x", "GIT_SSH_COMMAND"},
	{"x . . . . . x . . . . . n n n n x", "GIT_DIR"},
	{"x . . . . . x . . . . . n n n n x", "GIT_SSL_NO_VERIFY"},
	{"x . . . . . x . . . . . n n n n x", "EDITOR"},
	{"x . . . . . x . . . . . n n n n x", "SSL_CERT_FILE"},
	{"x . . . . . x . . . . . n n n n x", "SSL_CERT_DIR"},
	{"x . . . . . x . . . . . n n n n x", "SSLKEYLOGFILE"},
	{"x . . . . . x . . . . . n n n n x", "HTTPS_PROXY"},
	{"x . . . . . x . . . . . n n n n x", "https_proxy"},
	{"x . . . . . x . . . . . n n n n x", "no_proxy"},
	{"x . . . . . x . . . . . n n n n x", "npm_config_registry"},
	{"x . . . . . x . . . . . n n n n x", "PIP_INDEX_URL"},
	{"x . . . . . x . . . . . n n n n x", "GOPROXY"},
	{"x . . . . . x . . . . . n n n n x", "CARGO_BUILD_RUSTC_WRAPPER"},
	{"x . . . . . . . . . . . . . . . x", "GITHUB_TOKEN"},
	{"x . . . . . . . . . . . . . . . x", "NPM_TOKEN"},
	{"x . . . . . . . . . . . . . . . x", "CARGO_REGISTRY_TOKEN"},
	{"x . . . . . . . . . . . . . . . x", "MY_PROJECT_FLAG"},
	{"x . . . . . . . . . . . . . . . x", "OLLAMA_HOST"},
	{"x x x . . . x . x x . . x x x x x", "1BAD"},
}

// TestChildExecEnvIsNoSecretsOrAdapters pins, name by name, what a vault secret
// and a credential adapter may not set because it makes the child run other code,
// trust another CA or reach another host (#1375). The table above carries one row
// per family; this list fails when any one name leaves the rule.
func TestChildExecEnvIsNoSecretsOrAdapters(t *testing.T) {
	for _, name := range []string{
		"NODE_OPTIONS", "NODE_PATH", "NODE_REPL_EXTERNAL_MODULE", "NODE_TLS_REJECT_UNAUTHORIZED",
		"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONUSERBASE", "PYTHONBREAKPOINT",
		"PERL5OPT", "PERL5LIB", "PERLLIB", "RUBYOPT", "RUBYLIB", "RUBYGEMS_GEMDEPS",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "CLASSPATH",
		"BASH_ENV", "ENV", "PROMPT_COMMAND", "SHELLOPTS", "BASHOPTS", "PS4",
		"GLIBC_TUNABLES", "GCONV_PATH", "LOCPATH", "NLSPATH", "HOSTALIASES", "RES_OPTIONS", "LOCALDOMAIN",
		"GIT_SSH_COMMAND", "GIT_SSH", "GIT_SSH_VARIANT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
		"GIT_EXEC_PATH", "GIT_PROXY_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_EDITOR",
		"GIT_SEQUENCE_EDITOR", "GIT_TEMPLATE_DIR", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR",
		"GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_SSL_CAINFO", "GIT_SSL_CAPATH", "GIT_SSL_NO_VERIFY",
		"EDITOR", "VISUAL", "PAGER", "LESSOPEN", "LESSCLOSE", "BROWSER",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
		"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy",
		// Package managers and toolchains (#1442): npm and pnpm read npm_config_ in
		// either case, pip and Cargo their own prefix, Go and rustup their names.
		"npm_config_script_shell", "npm_config_node_options", "npm_config_cafile", "npm_config_ca",
		"npm_config_registry", "npm_config_proxy", "npm_config_https_proxy", "npm_config_noproxy",
		"npm_config_strict_ssl", "npm_config_userconfig", "npm_config_globalconfig", "npm_config_git",
		"npm_config_shell", "npm_config_offline", "NPM_CONFIG_REGISTRY", "NPM_CONFIG_SCRIPT_SHELL", "Npm_Config_CaFile",
		"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "PIP_FIND_LINKS", "PIP_CERT", "PIP_TRUSTED_HOST", "PIP_PROXY",
		"PIP_CONFIG_FILE", "PIP_index_url",
		"GOFLAGS", "GOPROXY", "GONOPROXY", "GOPRIVATE", "GOSUMDB", "GONOSUMDB", "GONOSUMCHECK", "GOINSECURE",
		"GOVCS", "GOAUTH", "GOTOOLCHAIN", "GOROOT", "GOENV", "GOWORK", "GOCACHEPROG", "GCCGO",
		"CC", "CXX", "CGO_CFLAGS", "CGO_LDFLAGS",
		"RUSTC", "RUSTC_WRAPPER", "RUSTC_WORKSPACE_WRAPPER", "RUSTDOC", "RUSTFLAGS", "RUSTDOCFLAGS",
		"RUSTUP_HOME", "RUSTUP_TOOLCHAIN", "RUSTUP_DIST_SERVER", "RUSTUP_UPDATE_ROOT",
		"CARGO_HOME", "CARGO_BUILD_RUSTC", "CARGO_BUILD_RUSTC_WRAPPER", "CARGO_BUILD_RUSTFLAGS",
		"CARGO_ENCODED_RUSTFLAGS", "CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER",
		"CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_RUNNER", "CARGO_REGISTRY_CREDENTIAL_PROVIDER",
		"CARGO_REGISTRIES_MY_INDEX", "CARGO_REGISTRIES_MY_CREDENTIAL_PROVIDER", "CARGO_HTTP_CAINFO", "CARGO_HTTP_PROXY",
		"CARGO_NET_GIT_TOKEN", "pnpm_config_registry", "PNPM_CONFIG_SCRIPT_SHELL", "PNPM_HOME",
		"COREPACK_NPM_REGISTRY", "COREPACK_INTEGRITY_KEYS", "COREPACK_HOME",
		"FC", "AR", "LD", "CFLAGS", "CXXFLAGS", "CPPFLAGS", "LDFLAGS", "TARGET_CC", "HOST_CC", "TARGET_CXX",
		"HOST_CXX", "TARGET_AR", "HOST_AR", "TARGET_CFLAGS", "HOST_CFLAGS", "TARGET_CXXFLAGS", "HOST_CXXFLAGS",
		"CC_x86_64_unknown_linux_gnu", "CXX_x86_64_unknown_linux_gnu", "AR_x86_64_unknown_linux_gnu",
		"CFLAGS_x86_64_unknown_linux_gnu", "CXXFLAGS_x86_64_unknown_linux_gnu", "ARFLAGS", "RANLIB", "HOST_RANLIB",
		"PKG_CONFIG", "PKG_CONFIG_PATH", "PKG_CONFIG_LIBDIR", "PKG_CONFIG_SYSROOT_DIR", "TARGET_PKG_CONFIG",
		"HOST_PKG_CONFIG_PATH", "PKG_CONFIG_x86_64_unknown_linux_gnu", "PKG_CONFIG_PATH_aarch64_unknown_linux_gnu",
		"GOPACKAGESDRIVER", "GOPATH", "GOMODCACHE", "GOCACHE", "GOBIN", "RUSTFMT",
		"AWS_CA_BUNDLE", "DENO_CERT", "DENO_TLS_CA_STORE", "PYTHONHTTPSVERIFY",
		"OPENSSL_CONF", "OPENSSL_MODULES", "OPENSSL_ENGINES",
	} {
		if got := childEnvFromSecret(name) + childEnvFromAdapter(t, providerDriverCodex, name); got != "xn" {
			t.Errorf("%s: secret and Codex adapter gave %q, want refused by both (xn)", name, got)
		}
	}
}

// TestRegistryCredentialsStayASecret pins what the rule above keeps (#1442): the
// registry credentials a package manager reads, by its own key or by the variable
// a project's .npmrc or CI names, are still a secret and an adapter's to set.
func TestRegistryCredentialsStayASecret(t *testing.T) {
	for _, name := range []string{
		"NPM_TOKEN", "NODE_AUTH_TOKEN", "npm_config__auth", "npm_config__authToken", "NPM_CONFIG__AUTHTOKEN",
		"npm_config_username", "npm_config__password", "npm_config_email", "npm_config_otp",
		"npm_config_certfile", "npm_config_keyfile", "npm_config_cert", "npm_config_key",
		"pnpm_config__authToken", "PIP_CLIENT_CERT", "CARGO_REGISTRY_TOKEN", "CARGO_REGISTRIES_MY_REGISTRY_TOKEN",
		"COREPACK_NPM_TOKEN", "COREPACK_NPM_USERNAME", "COREPACK_NPM_PASSWORD",
		"PIP_client_cert", "GITHUB_TOKEN", "GOOGLE_API_KEY", "CCACHE_DIR", "GOPHER_TOKEN", "RUST_LOG",
		// Names outside each tool's own prefix, as the tool reads it.
		"NPM_CONFIGURE", "PIPX_HOME", "pip_index_url", "cargo_home", "CARGOX_TOKEN", "corepack_home",
		"HOSTNAME_ALIAS", "TARGET_ENV", "LDAP_PASSWORD", "ARTIFACTORY_TOKEN",
		"CC_API_KEY", "AR_TOKEN", "CXX_SECRET", "CFLAGS_TOKEN", "PKG_CONFIG_TOKEN", "TARGET_CCACHE", "HOST_TOKEN",
	} {
		if got := childEnvFromSecret(name) + childEnvFromAdapter(t, providerDriverCodex, name); got != ".." {
			t.Errorf("%s: secret and Codex adapter gave %q, want accepted by both (..)", name, got)
		}
	}
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
