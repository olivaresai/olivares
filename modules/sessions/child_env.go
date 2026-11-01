// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/secret"
)

// WHICH VARIABLE A SESSION'S CHILD MAY BE GIVEN, AND BY WHOM.
//
// The child's environment has five sources, and this file is the one place that
// says which of them may name which variable:
//
//   - the engine's own environment, inherited by name: the safe base and the names
//     a launch allowlists (validateEnvAllow, sanitizedEnv);
//   - the launch's own values: the driver's governed credential
//     (validateProviderCredentialEnv, validateRecordCredentialEnv), the profile's
//     homes and the runtime's grants (childSpec, child_launch.go);
//   - a registered driver's non-secret variables (driverMaySetEnv);
//   - the launch gate's grant (validateGateEnv);
//   - the vault secrets the launch names (reservedSecretEnvName).
//
// TestChildEnvNameRules holds every source to this file, one row per kind of name.

// The environment variables a profile owns on the child. All are explicit launch
// values, never inherited for a profiled launch and never accepted from a
// caller's env_allow, a gate's injection or a credential adapter.
const (
	envUserHome              = "HOME"
	envClaudeConfigDir       = driverfacts.ClaudeConfigHomeEnv
	envGrokHome              = driverfacts.GrokConfigHomeEnv
	envOpenCodeConfigDir     = driverfacts.OpenCodeConfigHomeEnv
	envOpenCodeConfig        = "OPENCODE_CONFIG"
	envOpenCodeConfigContent = "OPENCODE_CONFIG_CONTENT"
	envOpenCodeTUIConfig     = "OPENCODE_TUI_CONFIG"
	envXDGConfigHome         = "XDG_CONFIG_HOME"
	envXDGDataHome           = "XDG_DATA_HOME"
	envXDGStateHome          = "XDG_STATE_HOME"
	envXDGCacheHome          = "XDG_CACHE_HOME"
	envXDGRuntimeDir         = "XDG_RUNTIME_DIR"
)

// The runtime's own variables on the child. The launch gate grants the managed
// hook's PEP endpoint and bearer (with the hook's tenant and agent hints and the
// context policy, OLIVARES_HOOK_PEP_* and OLIVARES_CONTEXT_*); the launch sets the
// work and communication grants and Claude's updater pin, and nothing else may.
const (
	envHookPEPURL         = "OLIVARES_HOOK_PEP_URL"
	envHookPEPToken       = "OLIVARES_HOOK_PEP_TOKEN"
	envWorkPrefix         = "OLIVARES_WORK_"
	envWorkToken          = envWorkPrefix + "TOKEN"
	envWorkSessionID      = envWorkPrefix + "SESSION_ID"
	envWorkRunRef         = envWorkPrefix + "RUN_REF"
	envCommunicationToken = "OLIVARES_COMMUNICATION_TOKEN"
	envDisableAutoupdater = "DISABLE_AUTOUPDATER"
)

// hookControlEnv names each tool hook's PEP endpoint as its hook command reads it:
// Claude's (and hook-pep's), Codex's and Grok's. A confined session's network
// boundary relays the endpoint the launch gate grants under any of them, and no
// other value of the gate's (sessionNetworkPolicy).
var hookControlEnv = []string{envHookPEPURL, "OLIVARES_CODEX_HOOK_URL", "OLIVARES_GROK_HOOK_URL"}

// baseEnvAllow is the minimal, non-sensitive host environment a launched official
// CLI needs: PATH (to find node/claude/codex), HOME (its config + transcripts) and
// locale/term/tmp basics. Session launches set HOME from the resolved profile;
// the shared process runner also accepts non-session children without that value.
var baseEnvAllow = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TZ", "USER", "SHELL",
}

// sanitizedEnv builds the child environment as an ALLOWLIST: ONLY the minimal
// safe base (baseEnvAllow) plus the operator-named `allow` variables are inherited
// from the host; EVERYTHING else — every OLIVARES_* signing key / KMS token the
// control-plane process holds — is withheld (minimal data: a denylist
// would leak the whole secret set to an agent running under bypassPermissions).
// The explicit spec env (the governed ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL) is
// appended last. ANTHROPIC_*/CLAUDE_CODE_* host vars are dropped even if allowlisted
// (a static key/cloud-provider var would shadow the minted WIF token), and so is
// every variable the engine reads as a secret (engineSecretEnvName).
func sanitizedEnv(allow []string, extra []EnvVar) []string {
	allowed := make(map[string]bool, len(baseEnvAllow)+len(allow))
	for _, n := range baseEnvAllow {
		allowed[n] = true
	}
	for _, n := range allow {
		if n = strings.TrimSpace(n); validEnvName(n) && !forbiddenInheritedEnvName(n) && !engineSecretEnvName(n) {
			allowed[n] = true
		}
	}
	explicitNames := make(map[string]bool, len(extra))
	for _, e := range extra {
		if validEnvName(e.Name) {
			explicitNames[e.Name] = true
		}
	}
	out := make([]string, 0, len(allowed)+len(extra))
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if !allowed[name] || forbiddenInheritedEnvName(name) || explicitNames[name] {
			continue // withhold everything not explicitly allowed (incl. all OLIVARES_*)
		}
		out = append(out, kv)
	}
	seenExplicit := make(map[string]bool, len(extra))
	for _, e := range extra {
		if !validEnvName(e.Name) || seenExplicit[e.Name] {
			continue
		}
		seenExplicit[e.Name] = true
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// envValueFits bounds one explicit value: no NUL, at most 64 KiB.
func envValueFits(value string) bool {
	return !strings.ContainsRune(value, '\x00') && len(value) <= 64*1024
}

// validateExplicitEnv is the runner's last check of the launch's own values.
func validateExplicitEnv(env []EnvVar) error {
	if len(env) > 128 {
		return errors.New("sessions: too many explicit environment values")
	}
	seen := make(map[string]bool, len(env))
	for _, item := range env {
		if !validEnvName(item.Name) || seen[item.Name] || !envValueFits(item.Value) {
			return errors.New("sessions: invalid or duplicate explicit environment value")
		}
		seen[item.Name] = true
	}
	return nil
}

// forbiddenInheritedEnvName names what the child never INHERITS, whatever an
// operator allowlists.
//
// The set is the control plane's own secrets plus EVERY provider's credential and
// routing family and configuration home, not just the one this launch uses. An
// inherited OPENAI_API_KEY would silently authenticate a Codex child as whoever
// runs the engine, and a CODEX_HOME or GROK_HOME would point it at a home nobody
// selected — both are the same accident a launch refuses from a caller, arriving
// through the host environment instead of a request body. The explicit launch
// values are unaffected: they are appended after this filter, which is what lets a
// profile set the home it owns.
func forbiddenInheritedEnvName(name string) bool {
	return strings.HasPrefix(name, "OLIVARES_") ||
		providerEnvName(name) ||
		slices.Contains(providerConfigHomeEnvs, name) ||
		name == envDisableAutoupdater
}

// providerEnvPrefixes are the credential and routing families of every provider
// CLI a session can run, and providerConfigHomeEnvs the variable that selects each
// one's configuration home, both as each tool declares them (driverfacts). Neither
// the host environment (forbiddenInheritedEnvName) nor a launch gate
// (validateGateEnv) may set a family: only the driver's own governed launch values
// do. A new driver declares its family in its facts row, once.
var providerEnvPrefixes, providerConfigHomeEnvs = providerEnvFamilies()

func providerEnvFamilies() (prefixes, configHomes []string) {
	for _, facts := range driverfacts.All() {
		prefixes = append(prefixes, facts.EnvPrefixes...)
		if facts.ConfigHomeEnv != "" {
			configHomes = append(configHomes, facts.ConfigHomeEnv)
		}
	}
	return prefixes, configHomes
}

func providerEnvName(name string) bool {
	return slices.ContainsFunc(providerEnvPrefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// engineSecretEnvName names a host variable the engine itself reads as a credential
// outside the families above, which no run creator may forward: the AWS keys (ledger
// signer, key custody, the AWS secret reader and connectors), the vault token the
// vault secret reader falls back to, the PostgreSQL driver's passwords, the OTLP
// exporters' headers, DATABASE_URL (the name the documented `--dsn env:` reference
// gives the database) and any variable an `env:` reference has resolved in this
// process. A base name is inherited anyway, so it is never one. A session still
// gets its own value through secret_env: that comes from the tenant's vault, not
// from the engine's environment.
func engineSecretEnvName(name string) bool {
	switch name {
	case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"VAULT_TOKEN", "PGPASSWORD", "PGSSLPASSWORD", "DATABASE_URL",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_HEADERS":
		return true
	}
	return !slices.Contains(baseEnvAllow, name) && secret.EnvReferenced(name)
}

// providerHomeEnvName reports whether name selects a provider HOME on the child.
//
// The set is the whole family, not just the current driver's, and that is the
// point: a Claude launch that accepted CODEX_HOME from a caller would be handing
// the child a home nobody authorized for a provider nobody selected. Every tool's
// configuration home is here because its facts row declares it, so the refusal
// exists before a launch of that tool does.
func providerHomeEnvName(name string) bool {
	switch name {
	case envUserHome, envOpenCodeConfig, envOpenCodeConfigContent, envOpenCodeTUIConfig:
		return true
	}
	return slices.Contains(providerConfigHomeEnvs, name)
}

// openCodeReservedEnvName is the XDG family reserved for OpenCode profiled
// launches only. It is not a global home name: existing drivers may still set
// explicit XDG values of their own.
func openCodeReservedEnvName(name string) bool {
	switch name {
	case envXDGConfigHome, envXDGDataHome, envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir:
		return true
	default:
		return false
	}
}

func openCodeXDGMapping(userHome string) []EnvVar {
	return []EnvVar{
		{Name: envXDGConfigHome, Value: filepath.Join(userHome, ".config")},
		{Name: envXDGDataHome, Value: filepath.Join(userHome, ".local", "share")},
		{Name: envXDGStateHome, Value: filepath.Join(userHome, ".local", "state")},
		{Name: envXDGCacheHome, Value: filepath.Join(userHome, ".cache")},
	}
}

// reservedSecretEnvName is every variable the runtime, a provider or the host base
// owns. A secret may never replace one: it would re-point a home, impersonate a
// provider credential or a control-plane token, or change how the child loads code,
// whom it trusts or where it connects (childExecEnvName).
func reservedSecretEnvName(name string) bool {
	return slices.Contains(baseEnvAllow, name) ||
		forbiddenInheritedEnvName(name) ||
		providerHomeEnvName(name) ||
		openCodeReservedEnvName(name) ||
		childExecEnvName(name) ||
		strings.HasPrefix(name, "CLAUDE_") ||
		strings.HasPrefix(name, "XDG_") ||
		strings.HasPrefix(name, "LD_") ||
		strings.HasPrefix(name, "DYLD_")
}

// childExecEnv names what makes the child run other code, trust another CA or
// reach another host, beyond the proxy and CA-bundle families: interpreter and
// shell start-up hooks, glibc's loader and resolver outside LD_*, git's commands,
// repository and TLS overrides, the editors and pagers tools start, the TLS key
// log, OpenSSL's configuration and modules, other stacks' CA bundles, the build
// toolchains' compilers, wrappers, flags, caches and module routing, the JVM
// builds' launchers, options, homes and caches, PHP's ini files, RubyGems' homes
// and source, NuGet's own plugins, caches, msbuild and revocation mode (its prefix
// is shared with the API keys projects name, NUGET_API_KEY and NUGET_AUTH_TOKEN),
// Deno's cache and JSR registry, and Python's and Node's code caches. An operator may
// forward the engine's own value through env_allow; a vault secret or a
// credential adapter never sets one. Bash's exported functions (BASH_FUNC_name%%)
// are no valid name anywhere (validEnvName).
var childExecEnv = []string{
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
	"SSL_CERT_DIR", "SSLKEYLOGFILE", "OPENSSL_CONF", "OPENSSL_MODULES", "OPENSSL_ENGINES",
	"AWS_CA_BUNDLE", "DENO_CERT", "DENO_TLS_CA_STORE", "PYTHONHTTPSVERIFY",
	"FC", "LD", "GCCGO", "CPPFLAGS", "LDFLAGS",
	"GOFLAGS", "GOTOOLCHAIN", "GOROOT", "GOENV", "GOWORK", "GOCACHEPROG", "GOAUTH", "GOPACKAGESDRIVER",
	"GOPATH", "GOMODCACHE", "GOCACHE", "GOBIN",
	"GOPROXY", "GONOPROXY", "GOPRIVATE", "GOSUMDB", "GONOSUMDB", "GONOSUMCHECK", "GOINSECURE", "GOVCS",
	"RUSTC", "RUSTC_WRAPPER", "RUSTC_WORKSPACE_WRAPPER", "RUSTDOC", "RUSTFMT", "RUSTFLAGS", "RUSTDOCFLAGS",
	"PNPM_HOME",
	"PYTHONWARNINGS", "PYTHONPYCACHEPREFIX", "NODE_COMPILE_CACHE",
	"GIT_ALLOW_PROTOCOL", "GIT_PROXY_SSL_CAINFO", "DENO_DIR", "JSR_URL",
	"MAVEN_OPTS", "MAVEN_ARGS", "MAVEN_CONFIG", "MAVEN_HOME", "M2_HOME", "MAVEN_USER_HOME", "MAVEN_PROJECTBASEDIR",
	"MVNW_REPOURL", "GRADLE_OPTS", "GRADLE_USER_HOME", "GRADLE_RO_DEP_CACHE", "ANT_OPTS", "ANT_ARGS", "SBT_OPTS",
	"JAVA_HOME", "JDK_HOME", "JRE_HOME", "JAVACMD", "JAVA_OPTS", "JDK_JAVAC_OPTIONS",
	"NUGET_PLUGIN_PATHS", "NUGET_NETFX_PLUGIN_PATHS", "NUGET_NETCORE_PLUGIN_PATHS", "NUGET_PACKAGES",
	"NUGET_FALLBACK_PACKAGES", "NUGET_HTTP_CACHE_PATH", "NUGET_PLUGINS_CACHE_PATH", "NUGET_RESTORE_MSBUILD_PATH",
	"NUGET_EXTENSIONS_PATH", "NUGET_CERT_REVOCATION_MODE", "PHPRC", "PHP_INI_SCAN_DIR", "COMPOSER",
	"GEM_HOME", "GEM_PATH", "GEMRC", "RUBYGEMS_HOST",
}

// childExecEnvName adds the proxy and CA-bundle families in either case, whose
// one source is the engine's own environment (driverfacts.NetworkEnv), the
// build tools (buildToolEnvName), the package managers' settings
// (packageManagerEnvName) and git's configuration, which runs commands
// (core.fsmonitor, core.sshCommand, a credential.helper of !cmd): a
// repository's read credential is git_read's (validateGitReadEnv).
func childExecEnvName(name string) bool {
	proxy, trust := driverfacts.NetworkEnv(name)
	return proxy || trust || slices.Contains(childExecEnv, name) || buildToolEnvName(name) ||
		packageManagerEnvName(name) || name == "GIT_CONFIG" || strings.HasPrefix(name, "GIT_CONFIG_")
}

// buildToolEnv are the compilers, archivers, flags and pkg-config search paths
// make, cgo, node-gyp and the cc and pkg-config crates of a Cargo build read.
var buildToolEnv = []string{"CC", "CXX", "AR", "ARFLAGS", "RANLIB", "CFLAGS", "CXXFLAGS",
	"PKG_CONFIG", "PKG_CONFIG_PATH", "PKG_CONFIG_LIBDIR", "PKG_CONFIG_SYSROOT_DIR"}

// buildToolEnvName reports a build tool's name as the crates read it: bare, as
// TARGET_ or HOST_, or per target with the target as written, which is lowercase
// (CC_x86_64_unknown_linux_gnu). CC_API_KEY names no target and stays a secret's.
func buildToolEnvName(name string) bool {
	if scoped, ok := strings.CutPrefix(name, "TARGET_"); ok {
		name = scoped
	} else {
		name = strings.TrimPrefix(name, "HOST_")
	}
	return slices.ContainsFunc(buildToolEnv, func(tool string) bool {
		target, ok := strings.CutPrefix(name, tool)
		return ok && (target == "" || target[0] == '_' && target == strings.ToLower(target))
	})
}

// npmCredentialKeys are the npm settings that are a registry credential, as npm
// names them after it reads npm_config_<key>. The rest of npm's settings run code
// (script-shell, node-options, git, shell), change trust (ca, cafile, strict-ssl)
// or route traffic (registry, proxy, userconfig).
var npmCredentialKeys = []string{"_auth", "_authtoken", "username", "_password", "email", "otp",
	"cert", "key", "certfile", "keyfile"}

// loweredKeyCredentials are, per family whose tool reads its prefix in either
// case and lowers the key, the keys that are a registry credential: npm's and
// pnpm's, and yarn's (npmAuthToken, npmAuthIdent and the client certificate, as
// npm's; yarn 1 reads yarn_ too).
var loweredKeyCredentials = []struct {
	prefix string
	keys   []string
}{
	{"npm_config_", npmCredentialKeys}, {"pnpm_config_", npmCredentialKeys},
	{"yarn_", []string{"npm_auth_token", "npm_auth_ident", "https_cert_file_path", "https_key_file_path"}},
}

// packageManagerCredentialEnv are the registry credentials among the families of
// toolchainEnvPrefixes: Cargo's for crates.io, corepack's, bun's, and uv's for
// publishing and its GitHub token. COMPOSER_AUTH is none: Composer merges the
// whole object into its configuration (cafile, disable-tls, repositories) and
// only warns when it is no auth.json.
var packageManagerCredentialEnv = []string{"CARGO_REGISTRY_TOKEN",
	"COREPACK_NPM_TOKEN", "COREPACK_NPM_USERNAME", "COREPACK_NPM_PASSWORD", "BUN_CONFIG_TOKEN",
	"UV_PUBLISH_USERNAME", "UV_PUBLISH_PASSWORD", "UV_PUBLISH_TOKEN", "UV_GITHUB_TOKEN"}

// toolchainEnvPrefixes are families each tool reads as written: Cargo's
// settings, corepack's (which package manager it downloads, from where, checked
// against which keys), rustup's and cgo's flags, bun's options and settings,
// uv's, Composer's, PEAR's, the .NET runtime's (startup hooks, profilers,
// roots and knobs), Gradle's project properties and Bundler's settings.
var toolchainEnvPrefixes = []string{"CARGO_", "COREPACK_", "RUSTUP_", "CGO_", "BUN_", "UV_", "COMPOSER_",
	"PHP_PEAR_", "DOTNET_", "COMPlus_", "COMPLUS_", "CORECLR_", "COR_", "ORG_GRADLE_PROJECT_", "BUNDLE_"}

// gradleCredentialSuffixes are the project properties Gradle's credentials(type,
// identity) reads for a repository (<identity>Username, <identity>Password and the
// AWS and HTTP-header kinds'), plus in-memory signing's signingKey.
var gradleCredentialSuffixes = []string{"Username", "Password", "AccessKey", "SecretKey", "SessionToken",
	"AuthHeaderName", "AuthHeaderValue", "signingKey"}

// packageManagerEnvName names a setting a package manager or toolchain reads
// under its own prefix, other than its registry credential: npm, pnpm and yarn
// read theirs in either case and lower the key (loweredKeyCredentials), pip reads
// PIP_ and lowers the key, and the rest read theirs as written. A registry token
// a project's .npmrc or CI names (NPM_TOKEN, NODE_AUTH_TOKEN, NUGET_API_KEY) is
// no setting and stays a secret.
func packageManagerEnvName(name string) bool {
	lower := strings.ToLower(name)
	for _, family := range loweredKeyCredentials {
		if key, ok := strings.CutPrefix(lower, family.prefix); ok {
			return !slices.Contains(family.keys, key)
		}
	}
	if key, ok := strings.CutPrefix(name, "PIP_"); ok {
		return strings.ToLower(key) != "client_cert"
	}
	if registryCredentialEnvName(name) {
		return false
	}
	return slices.ContainsFunc(toolchainEnvPrefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// registryCredentialEnvName names a toolchain family's registry credential:
// the fixed names, and those a family names per registry. Cargo's is
// CARGO_REGISTRIES_<name>_TOKEN, uv's UV_INDEX_<name>_USERNAME and _PASSWORD,
// Gradle's a credential property, and Bundler's the host itself, its dots
// doubled into underscores (BUNDLE_GEMS__EXAMPLE__COM), where the dotted
// settings mirror., build., local., path.system and github.https route,
// compile, load or place gems instead. Bundler's client certificate is a
// credential, as npm's and pip's are.
func registryCredentialEnvName(name string) bool {
	if slices.Contains(packageManagerCredentialEnv, name) || name == "BUNDLE_SSL_CLIENT_CERT" ||
		strings.HasPrefix(name, "CARGO_REGISTRIES_") && strings.HasSuffix(name, "_TOKEN") {
		return true
	}
	if key, ok := strings.CutPrefix(name, "UV_INDEX_"); ok {
		return strings.HasSuffix(key, "_USERNAME") || strings.HasSuffix(key, "_PASSWORD")
	}
	if property, ok := strings.CutPrefix(name, "ORG_GRADLE_PROJECT_"); ok {
		return slices.ContainsFunc(gradleCredentialSuffixes, func(suffix string) bool {
			return strings.HasSuffix(property, suffix)
		})
	}
	if key, ok := strings.CutPrefix(name, "BUNDLE_"); ok {
		key = strings.ToUpper(key)
		setting, host, _ := strings.Cut(key, "__")
		return host != "" && !slices.Contains([]string{"MIRROR", "BUILD", "LOCAL"}, setting) &&
			key != "PATH__SYSTEM" && key != "GITHUB__HTTPS"
	}
	return false
}

// validateEnvAllow checks a launch's env_allow names and trims them in place. A
// queued launch's stored names are checked again when it is approved: a variable
// that became an engine secret meanwhile is refused there, not dropped silently.
func validateEnvAllow(names []string) error {
	if len(names) > 64 {
		return badRequest("env_allow has too many names")
	}
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if !validEnvName(name) || forbiddenInheritedEnvName(name) || seen[name] {
			return badRequest("invalid or reserved env_allow name")
		}
		if engineSecretEnvName(name) {
			return badRequest("env_allow may not name " + name + ": the engine reads it as a secret; give the session its own value through secret_env")
		}
		seen[name] = true
		names[i] = name
	}
	return nil
}

// validateProfiledEnvAllow refuses an env_allow name the profile owns: resolving
// it by order would be exactly the accident a profile exists to fix.
func validateProfiledEnvAllow(names []string) error {
	for _, name := range names {
		if providerHomeEnvName(name) {
			return badRequest("env_allow may not name " + name + " for a profiled launch: the profile owns it")
		}
	}
	return nil
}

func validateOpenCodeEnvAllow(driver string, names []string) error {
	if driver != providerDriverOpenCode {
		return nil
	}
	for _, name := range names {
		if openCodeReservedEnvName(name) {
			return badRequest("env_allow may not name " + name + " for an OpenCode profiled launch: the profile owns the XDG mapping")
		}
	}
	return nil
}

// validateGitReadEnv refuses a git_read launch that also forwards one of git's
// configuration variables: the read credential sets git's configuration. No
// secret sets one at all (childExecEnvName).
func validateGitReadEnv(envAllow []string) error {
	for _, name := range envAllow {
		if strings.HasPrefix(name, "GIT_CONFIG_") {
			return badRequest("git_read and env_allow " + name + " cannot be combined: the read credential sets git's configuration")
		}
	}
	return nil
}

// validateGateEnv checks the launch gate's grant. A gate provisions POLICY, never a
// provider identity or a runtime grant: the provider families are how the official
// CLIs are authenticated and routed, and only the driver's own governed launch
// values may name them — a gate that could set OPENAI_API_KEY would be a second,
// ungoverned issuer. Every launch has a resolved profile, authoritative over the
// gate for its homes, and an OpenCode launch for its XDG mapping: the
// conflict is refused rather than ordered.
func validateGateEnv(env []EnvVar, driver string) error {
	if err := gateEnvShape(env); err != nil {
		return denyClosedErr("launch gate returned an invalid environment", err)
	}
	for _, item := range env {
		if providerHomeEnvName(item.Name) {
			// A decision, not an outage: the launch is refused with the status the
			// other deny-closed verdicts carry, and denyClosedErr passes it through.
			return denyClosedErr("launch gate returned an environment that conflicts with the provider profile",
				forbiddenErr("launch denied: the launch gate injected "+item.Name+", which the provider profile owns"))
		}
	}
	if err := validateOpenCodeReservedInjection(driver, env); err != nil {
		return denyClosedErr("launch gate returned an environment that conflicts with the OpenCode profile mapping", err)
	}
	return nil
}

func gateEnvShape(env []EnvVar) error {
	if len(env) > 64 {
		return errors.New("too many injected environment values")
	}
	seen := make(map[string]bool, len(env))
	for _, item := range env {
		name := item.Name
		reserved := name == envDisableAutoupdater || name == envCommunicationToken ||
			strings.HasPrefix(name, envWorkPrefix) || providerEnvName(name)
		if !validEnvName(name) || reserved || seen[name] || !envValueFits(item.Value) {
			return errors.New("invalid, duplicate, or runtime-reserved injected environment value")
		}
		seen[name] = true
	}
	return nil
}

// validateOpenCodeReservedInjection refuses a launch gate's injection that names
// the XDG family an OpenCode launch owns. A credential adapter may name no XDG
// variable at all (validateProviderCredentialEnv).
func validateOpenCodeReservedInjection(driver string, env []EnvVar) error {
	if driver != providerDriverOpenCode {
		return nil
	}
	for _, item := range env {
		if openCodeReservedEnvName(item.Name) {
			return forbiddenErr("launch denied: " + item.Name + " is reserved for the OpenCode profiled launch mapping")
		}
	}
	return nil
}

// validateProviderCredentialEnv refuses an adapter that tries to name a variable
// it does not own. The adapter's licence is to supply CREDENTIAL variables; a
// home or a routing override from it would be the same accident §6 refuses from a
// caller and from a gate, arriving through a third door.
func validateProviderCredentialEnv(driver string, env []EnvVar) error {
	for _, item := range env {
		if providerHomeEnvName(item.Name) {
			return forbiddenErr("launch denied: the provider credential adapter named " + item.Name + ", which the provider profile owns")
		}
		// Nor may it name the control plane's own variables or Claude's (the historical
		// path mints Claude's credential itself, never an adapter). Beyond its OWN
		// driver's credential, it is held to what a vault secret may not set (a
		// provider family, the host base, the loader, the exec, trust and proxy
		// names and git's configuration of childExecEnvName) and the variables the
		// engine reads as secrets: each would hand the child an identity or code
		// nobody selected.
		if strings.HasPrefix(item.Name, "OLIVARES_") ||
			strings.HasPrefix(item.Name, "ANTHROPIC_") ||
			strings.HasPrefix(item.Name, "CLAUDE_") ||
			!adapterCredentialEnvName(driver, item.Name) && (reservedSecretEnvName(item.Name) ||
				engineSecretEnvName(item.Name)) {
			return forbiddenErr("launch denied: the provider credential adapter named " + item.Name + ", which it does not own")
		}
	}
	return validateExplicitEnv(env)
}

// adapterCredentialEnvName reports whether name is a variable a credential of a
// provider kind this driver binds sets (driverfacts, providerRecordEnv): its key,
// and its base URL only where the binding may configure the endpoint. That is the
// one credential an adapter is documented to supply; a driver that binds no kind,
// like Gemini CLI, owns no provider-family name at all.
func adapterCredentialEnvName(driver, name string) bool {
	facts, _ := driverfacts.Lookup(driver)
	for _, b := range facts.Bindings {
		baseURL := ""
		if b.Egress == driverfacts.EgressConfigured {
			baseURL = "https://example.invalid"
		}
		if slices.ContainsFunc(providerRecordEnv(b.Kind, baseURL, "x"), func(v EnvVar) bool { return v.Name == name }) {
			return true
		}
	}
	return false
}

// validateRecordCredentialEnv checks a record-backed injection against the CLOSED
// set its own KIND declares.
//
// It is a second validator and not a relaxation of validateProviderCredentialEnv.
// That one governs a THIRD-PARTY adapter wired through WithProviderCredentialSource:
// it bans ANTHROPIC_*, CLAUDE_* and OLIVARES_* and holds every other provider family
// to the credentials of the kinds its DRIVER binds, so one provider's adapter can
// never name another's variables. A first-party anthropic record has to set
// ANTHROPIC_API_KEY, so it cannot pass that rule — and weakening that rule to let it
// through would weaken it for every adapter it was written to constrain.
//
// Two closed sets, each proving its own property, cost twelve lines and prove more
// than one rule with an exception carved into it.
func validateRecordCredentialEnv(kind string, env []EnvVar, auth ...modelprovider.AuthScheme) error {
	allowed := providerRecordEnvNames(kind, auth...)
	for _, item := range env {
		if _, ok := allowed[item.Name]; !ok {
			return forbiddenErr("launch denied: a " + kind + " provider may not set " + item.Name)
		}
	}
	return validateExplicitEnv(env)
}

// driverMaySetEnv reports whether a registered driver's LaunchEnv may give the
// child name. A name the profile OWNS is dropped rather than ordered: the whole
// point of resolving homes server-side is that nothing downstream re-points them,
// and a driver is downstream of that decision like a caller or a gate. OpenCode's
// own non-secret native configuration is the exception: it is the driver's, not a
// caller or a gate overriding the resolved account homes.
func driverMaySetEnv(driver, name string) bool {
	if driver == providerDriverOpenCode {
		if name == envOpenCodeConfigContent {
			return true
		}
		if openCodeReservedEnvName(name) {
			return false
		}
	}
	return !providerHomeEnvName(name)
}
