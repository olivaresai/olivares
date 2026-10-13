// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// PROGRAMS AND PACKAGE REGISTRIES A PROVIDER PROFILE GIVES ITS SESSIONS.
//
// A profile may declare the third-party programs its sessions get (installed
// through the tool installer as declared-<program>) and the npm registries they
// install packages from (a URL, an optional scope and the vault secret holding the
// registry's token). Both are launch terms re-read from the CURRENT profile on
// every launch and resume, like the tool policy.
//
// Programs: each declared program's newest verified install is resolved at launch
// (the same resolver a driver's own program uses) and its directory goes first on
// the child's PATH. A declared program that is not installed refuses the launch.
//
// Registries: the engine writes the npm configuration for this launch, mode 0600,
// under run/<run_ref> of its data directory, grants the child read access to it,
// points npm at it with npm_config_globalconfig, and removes it when the launch
// ends. The token is in that file only: never in argv, the environment, the run
// row or a log line, and the output the plane stores has it replaced by a mark. A
// confined session's network (child_launch.go sessionNetworkPolicy) is widened by
// exactly the declared registry hosts, so a session on another profile, which has
// no grant, is refused by the egress proxy. A launch's env_allow may not forward
// npm's own settings beside them: two sources of npm configuration would be
// resolved by npm's order, not by the profile.
//
// Declaring either needs tenant administration. The token is delegated to every
// session launched under the profile, as the profile's provider key is: the
// launcher needs no vault right, and the child can read the file.
//
// ponytail: npm only (pnpm and yarn 1 read the same .npmrc keys but not this
// file's variable), native sessions only, and a registry whose tarballs live on
// another host (a CDN) is refused by a confined session's proxy. An unconfined
// session (account login, unmeasured tool) keeps the host network: another profile
// can reach the host but holds no token. As with git_read, an engine killed
// before a launch ends leaves that launch's 0600 file under run/<run_ref>.
// Upgrade trigger: a pip/cargo registry need, container sessions with
// registries, a measured CDN host list, or a startup sweep of run/.

// colPPSessionPrograms and colPPSessionRegistries are the declarations, JSON arrays,
// NULL when the profile declares none.
const (
	colPPSessionPrograms   = "session_programs"
	colPPSessionRegistries = "session_registries"
)

const (
	// maxSessionPrograms and maxSessionRegistries bound one profile. The egress
	// policy holds at most 16 endpoints, the provider and hook relays included.
	maxSessionPrograms   = 16
	maxSessionRegistries = 8
	// registryKindNPM is the one registry kind sessions get.
	registryKindNPM = "npm"
	// envNPMGlobalConfig selects npm's global configuration file. The profile's
	// own ~/.npmrc and a project's .npmrc still apply above it.
	envNPMGlobalConfig = "npm_config_globalconfig"
	// declaredProgramDriver prefixes a declared program's install driver key
	// (modules/catalog ToolDeclaration.Driver).
	declaredProgramDriver = "declared-"
)

// SessionRegistry is one package registry a provider profile gives its sessions.
type SessionRegistry struct {
	// Kind is the package manager: "npm".
	Kind string `json:"kind"`
	// URL is the registry's base URL: https, or http on a loopback host.
	URL string `json:"url"`
	// Scope routes only that npm scope ("@acme") to the registry; empty makes it
	// the default registry.
	Scope string `json:"scope,omitempty"`
	// Secret names the tenant vault secret (env/…) holding the registry's token;
	// empty sends none.
	Secret string `json:"secret,omitempty"`
}

// validDeclaredProgram is the tool installer's program name: a lowercase
// identifier (modules/catalog validSlug).
func validDeclaredProgram(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '-' || s[0] == '_' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// normalizeSessionPrograms validates a declaration: trimmed, de-duplicated and
// sorted, stored as a JSON array, "" (NULL) for none.
func normalizeSessionPrograms(in []string) (string, error) {
	if len(in) > maxSessionPrograms {
		return "", badRequest(fmt.Sprintf("a profile declares at most %d programs", maxSessionPrograms))
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if !validDeclaredProgram(name) {
			return "", badRequest(fmt.Sprintf("session_programs: %q is not an installed program's name", raw))
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	slices.Sort(out)
	blob, _ := json.Marshal(out)
	return string(blob), nil
}

// validNPMScope is an npm scope: "@" and a lowercase name npm accepts.
func validNPMScope(s string) bool {
	name, ok := strings.CutPrefix(s, "@")
	if !ok || name == "" || len(name) > 214 || name[0] == '.' || name[0] == '_' {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune("-._~", c) {
			return false
		}
	}
	return true
}

// normalizeRegistryURL returns the registry's base URL with one trailing slash.
// A token never crosses the network in clear text: http only on a loopback host.
func normalizeRegistryURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(raw, " \t\r\n\x00*;#\"'$`\\{}") {
		return "", errors.New("is not a registry URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()) {
		return "", errors.New("is not a registry address")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("must use https (http only on a loopback host)")
		}
	default:
		return "", errors.New("must use https (http only on a loopback host)")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	return u.String(), nil
}

// normalizeSessionRegistries validates a declaration and returns it stored as a
// JSON array ("" for none), and whether any registry names a vault secret.
func normalizeSessionRegistries(in []SessionRegistry) (string, bool, error) {
	if len(in) > maxSessionRegistries {
		return "", false, badRequest(fmt.Sprintf("a profile declares at most %d registries", maxSessionRegistries))
	}
	out := make([]SessionRegistry, 0, len(in))
	scopes := map[string]bool{}
	secrets := false
	for _, r := range in {
		r = SessionRegistry{Kind: strings.TrimSpace(r.Kind), URL: r.URL, Scope: strings.TrimSpace(r.Scope), Secret: strings.TrimSpace(r.Secret)}
		if r.Kind != registryKindNPM {
			return "", false, badRequest(fmt.Sprintf("session_registries: kind %q is not supported (npm)", r.Kind))
		}
		u, err := normalizeRegistryURL(r.URL)
		if err != nil {
			return "", false, badRequest("session_registries: " + strings.TrimSpace(r.URL) + " " + err.Error())
		}
		r.URL = u
		if r.Scope != "" && !validNPMScope(r.Scope) {
			return "", false, badRequest(fmt.Sprintf("session_registries: %q is not an npm scope (@name)", r.Scope))
		}
		if scopes[r.Scope] {
			if r.Scope == "" {
				return "", false, badRequest("session_registries: only one registry may be the default (no scope)")
			}
			return "", false, badRequest("session_registries: scope " + r.Scope + " is declared twice")
		}
		scopes[r.Scope] = true
		if r.Secret != "" {
			if !strings.HasPrefix(r.Secret, secretEnvPrefix) || len(r.Secret) == len(secretEnvPrefix) || auth.ValidateSecretName(r.Secret) != "" {
				return "", false, badRequest(fmt.Sprintf("session_registries: %q is not a session secret (names begin with %s)", r.Secret, secretEnvPrefix))
			}
			secrets = true
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "", false, nil
	}
	blob, _ := json.Marshal(out)
	return string(blob), secrets, nil
}

// validateProfilePackagesInput validates a profile's declarations and returns
// their stored values. Declaring either needs the tenant administration
// secret_env needs (principalMayUseSecretEnv), not only profile write: they
// decide which code every session under the profile runs and installs, widen a
// confined session's network, and hand a vault secret's value to each of them.
func validateProfilePackagesInput(programs []string, registries []SessionRegistry, declared, mayUseSecretEnv bool) (string, string, error) {
	if declared && !mayUseSecretEnv {
		return "", "", forbiddenErr("declaring a profile's programs or package registries needs tenant administration")
	}
	storedPrograms, err := normalizeSessionPrograms(programs)
	if err != nil {
		return "", "", err
	}
	storedRegistries, _, err := normalizeSessionRegistries(registries)
	if err != nil {
		return "", "", err
	}
	return storedPrograms, storedRegistries, nil
}

// decodeProfilePackages reads both declarations off a profile row. A value that
// cannot be read is reported, never read as none: a session without its
// registry would install the same names from the public one.
func decodeProfilePackages(rec model.Record) (programs []string, registries []SessionRegistry, ok bool) {
	ok = true
	if raw := rec.String(colPPSessionPrograms); raw != "" {
		ok = json.Unmarshal([]byte(raw), &programs) == nil
	}
	if raw := rec.String(colPPSessionRegistries); raw != "" {
		ok = json.Unmarshal([]byte(raw), &registries) == nil && ok
	}
	return programs, registries, ok
}

// errUnreadablePackages refuses a launch whose profile's stored programs or
// registries cannot be read.
var errUnreadablePackages = &runErr{http.StatusConflict,
	"the provider profile's programs or package registries cannot be read; declare them again with agent profile update"}

// validateRegistryEnvAllow refuses a launch that forwards npm's own settings
// beside the profile's registries.
func validateRegistryEnvAllow(registries []SessionRegistry, envAllow []string) error {
	if len(registries) == 0 {
		return nil
	}
	for _, name := range envAllow {
		if strings.HasPrefix(strings.ToLower(name), "npm_config_") {
			return badRequest("the provider profile declares package registries and env_allow names " + name +
				": npm would read two configurations; remove it from env_allow")
		}
	}
	return nil
}

// refuseNonNativePackages refuses a container or sandbox launch on a profile
// with programs or registries: their files and paths are the host's.
func refuseNonNativePackages(p CreateRunParams) error {
	if len(p.profilePrograms)+len(p.profileRegistries) > 0 && p.Isolation != "" && p.Isolation != IsolationNative {
		return &runErr{http.StatusUnprocessableEntity,
			"the provider profile declares programs or package registries, which reach native sessions only; launch this session natively"}
	}
	return nil
}

// configureProfilePackages applies the profile's programs and registries to one
// launch. It runs with the other launch-time contributions (prepareSessionLaunch).
func (m *Module) configureProfilePackages(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) error {
	if len(p.profilePrograms) == 0 && len(p.profileRegistries) == 0 {
		return nil
	}
	if err := refuseNonNativePackages(*p); err != nil {
		return err
	}
	if err := m.applyProfilePrograms(p.profilePrograms, spec); err != nil {
		return err
	}
	return m.applyProfileRegistries(ctx, runCtx, tenant, runRef, p, spec)
}

// applyProfilePrograms puts each declared program's install directory first on
// the child's PATH, in the stored (sorted) order.
func (m *Module) applyProfilePrograms(programs []string, spec *LaunchSpec) error {
	if len(programs) == 0 {
		return nil
	}
	if m.rt.ProgramResolver == nil {
		return &runErr{http.StatusServiceUnavailable,
			"the provider profile declares programs, and this node resolves no installed tools (the launch is denied)"}
	}
	dirs := make([]string, 0, len(programs)+1)
	for _, name := range programs {
		exe := m.rt.ProgramResolver(declaredProgramDriver + name)
		info, err := os.Stat(exe)
		if exe == "" || !filepath.IsAbs(exe) || err != nil || !info.Mode().IsRegular() {
			return conflictErr(fmt.Sprintf(
				"the provider profile declares the program %s, which is not installed on this node; install it under Tools or remove it from the profile", name))
		}
		if dir := filepath.Dir(exe); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
			// Installs live under the engine's data directory, which a
			// confined child cannot otherwise reach.
			spec.AllowRead(dir)
		}
	}
	if host := os.Getenv("PATH"); host != "" {
		dirs = append(dirs, host)
	}
	setLaunchEnv(spec, "PATH", strings.Join(dirs, string(os.PathListSeparator)))
	return nil
}

// applyProfileRegistries writes this launch's npm configuration and grants its
// hosts to a confined session's network.
func (m *Module) applyProfileRegistries(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) error {
	if len(p.profileRegistries) == 0 {
		return nil
	}
	if !filepath.IsAbs(m.rt.hookDataDir) || runRef == "" || filepath.Base(runRef) != runRef || strings.ContainsAny(runRef, "\\\r\n\x00") {
		return &runErr{http.StatusServiceUnavailable,
			"the provider profile declares package registries, and this node has no data directory to write their configuration (the launch is denied)"}
	}
	var refs []SecretEnvRef
	for _, r := range p.profileRegistries {
		if r.Secret != "" {
			// Keyed by the secret's name: the output mark and every refusal name it.
			refs = append(refs, SecretEnvRef{Env: r.Secret, Secret: r.Secret})
		}
	}
	tokens, err := m.resolveSecretEnv(ctx, tenant, refs)
	if err != nil {
		return err
	}
	tokenOf := map[string]string{}
	for _, t := range tokens {
		if strings.ContainsAny(t.Value, "\r\n") || strings.Contains(t.Value, "${") {
			return conflictErr(t.Name + " holds a line break or ${, which npm's configuration would not keep as written; store the token alone on one line" + secretEnvWayOut)
		}
		tokenOf[t.Name] = t.Value
	}
	// Armed before the file exists: nothing can return between them unredacted.
	p.registryTokens = tokens
	var npmrc strings.Builder
	for _, r := range p.profileRegistries {
		if r.Scope == "" {
			npmrc.WriteString("registry=" + r.URL + "\n")
		} else {
			npmrc.WriteString(r.Scope + ":registry=" + r.URL + "\n")
		}
		if token := tokenOf[r.Secret]; token != "" {
			// npm keys a credential by the registry URL without its scheme.
			// Quoted: npm's ini parser reads a quoted value as JSON, so ; and # stay
			// part of the token instead of starting a comment.
			quoted, _ := json.Marshal(token)
			npmrc.WriteString(strings.TrimPrefix(strings.TrimPrefix(r.URL, "https:"), "http:") + ":_authToken=" + string(quoted) + "\n")
		}
	}
	path, err := writeRunFile(filepath.Join(m.rt.hookDataDir, "run", runRef), "npmrc-*", npmrc.String())
	if err != nil {
		m.warnf("sessions: the package registry configuration could not be written", "run_ref", runRef, "err", redactErr(err))
		return &runErr{http.StatusServiceUnavailable, "the package registry configuration could not be written on this node; the launch is denied"}
	}
	context.AfterFunc(runCtx, func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.warnf("sessions: a session's package registry configuration could not be removed; delete it by hand",
				"run_ref", runRef, "path", path, "err", redactErr(err))
		}
	})
	setLaunchEnv(spec, envNPMGlobalConfig, path)
	spec.AllowRead(path)
	if spec.NetworkPolicy != nil {
		for _, r := range p.profileRegistries {
			spec.NetworkPolicy.Providers = append(spec.NetworkPolicy.Providers, r.URL)
		}
	}
	parts := make([]string, 0, len(p.profileRegistries))
	for _, r := range p.profileRegistries {
		parts = append(parts, strings.TrimSpace(r.Scope+" "+r.URL))
	}
	p.registryDetail = "registries " + strings.Join(parts, ",")
	return nil
}

// setLaunchEnv sets name on the launch, replacing an earlier value: the runner
// keeps the first of two, and the profile's is the one that applies.
func setLaunchEnv(spec *LaunchSpec, name, value string) {
	spec.Env = slices.DeleteFunc(spec.Env, func(e EnvVar) bool { return e.Name == name })
	spec.Env = append(spec.Env, EnvVar{Name: name, Value: value})
}

// writeRunFile writes one launch file, mode 0600, under the run's directory.
func writeRunFile(dir, pattern, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, werr := file.WriteString(content)
	if err := errors.Join(werr, file.Close()); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
