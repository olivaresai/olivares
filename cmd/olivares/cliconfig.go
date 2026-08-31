// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const cliConfigOverrideEnv = "OLIVARES_CLI_CONFIG"

// errNoUserConfigDir marks the one failure cliConfigPath can produce: the
// process has no resolvable per-user configuration directory (no
// $XDG_CONFIG_HOME, no $HOME). It is a sentinel because the two sides of the
// config treat it OPPOSITELY, and conflating them is what made this a bug:
// READING a config that does not exist is normal and must not stop a command
// that already carries --server/--token, while WRITING one with nowhere to put
// it must fail loudly rather than guess a location.
var errNoUserConfigDir = errors.New("no per-user configuration directory: neither $XDG_CONFIG_HOME nor $HOME is set")

// cliConfig is the on-disk client configuration. Its wire names deliberately
// follow kubeconfig's naming style while keeping the schema small and auditable.
type cliConfig struct {
	CurrentContext string       `yaml:"current-context"`
	Contexts       []cliContext `yaml:"contexts"`
}

type cliContext struct {
	Name      string   `yaml:"name"`
	Server    string   `yaml:"server"`
	Token     string   `yaml:"token,omitempty"`
	Tenant    string   `yaml:"tenant,omitempty"`
	CACert    string   `yaml:"ca-cert,omitempty"`
	PinSHA256 []string `yaml:"pin-sha256,omitempty"`
}

// cliResolutionOptions retains whether a value came from an explicit flag. An
// explicitly empty flag can therefore clear a lower-precedence environment or
// context value instead of being mistaken for an omitted flag.
type cliResolutionOptions struct {
	Server         string
	Token          string
	Tenant         string
	CACert         string
	PinSHA256      []string
	ServerExplicit bool
	TokenExplicit  bool
	TenantExplicit bool
	CACertExplicit bool
	PinsExplicit   bool
	// SkipCredentials keeps Token/Tenant EMPTY regardless of environment or
	// context. A command against an unauthenticated endpoint (status → public
	// GET /status) must set it: otherwise the active context's bearer token
	// rides along to whatever --server the operator pointed at — a credential
	// leak to an arbitrary, possibly untrusted host (adversarial review).
	SkipCredentials bool
}

type cliResolvedConfig struct {
	ContextName string
	ConfigPath  string
	Server      string
	Token       string
	Tenant      string
	CACert      string
	PinSHA256   []string
}

// cliConfigPath resolves the client config independently from the engine
// configuration. os.UserConfigDir honors XDG_CONFIG_HOME on Unix. Tests and
// hermetic automation may override the final path with OLIVARES_CLI_CONFIG.
func cliConfigPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(cliConfigOverrideEnv)); override != "" {
		return filepath.Clean(override), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w (%v); set %s to choose the file explicitly", errNoUserConfigDir, err, cliConfigOverrideEnv)
	}
	return filepath.Join(base, "olivares", "config.yaml"), nil
}

// loadCLIConfig reads the client config, and treats HAVING NOWHERE TO LOOK the
// same as LOOKING AND FINDING NOTHING: both mean "no stored context", and a
// command carrying --server/--token needs neither. It returns an EMPTY path in
// that case, which writeCLIConfig refuses by design — the degradation belongs
// to the read side only.
func loadCLIConfig() (cliConfig, string, error) {
	path, err := cliConfigPath()
	if errors.Is(err, errNoUserConfigDir) {
		return cliConfig{Contexts: []cliContext{}}, "", nil
	}
	if err != nil {
		return cliConfig{}, "", err
	}
	cfg, err := readCLIConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return cliConfig{Contexts: []cliContext{}}, path, nil
	}
	return cfg, path, err
}

// readCLIConfig fails closed when group or world permissions are present. The
// mode is checked on the opened descriptor so a path swap cannot bypass it.
func readCLIConfig(path string) (cliConfig, error) {
	f, err := os.Open(path) //nolint:gosec // path is the operator-selected client config
	if err != nil {
		return cliConfig{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return cliConfig{}, fmt.Errorf("stat client config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return cliConfig{}, fmt.Errorf("client config %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return cliConfig{}, fmt.Errorf("client config %s has permissions %04o; run `chmod 600 %s` before using it", path, info.Mode().Perm(), path)
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return cliConfig{}, fmt.Errorf("read client config %s: %w", path, err)
	}
	var cfg cliConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cliConfig{}, fmt.Errorf("parse client config %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return cliConfig{}, fmt.Errorf("parse client config %s: multiple YAML documents are not allowed", path)
		}
		return cliConfig{}, fmt.Errorf("parse client config %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = []cliContext{}
	}
	if err := validateCLIConfig(cfg); err != nil {
		return cliConfig{}, fmt.Errorf("invalid client config %s: %w", path, err)
	}
	return cfg, nil
}

// writeCLIConfig atomically replaces the client configuration. Newly created
// directories are 0700 and the final file is forced to 0600 even when an older
// file had been loosened.
func writeCLIConfig(path string, cfg cliConfig) error {
	// An empty path reaches here when loadCLIConfig degraded (no $HOME and no
	// $XDG_CONFIG_HOME) and the caller then tried to save. Measured without
	// this guard: filepath.Dir("") is ".", so MkdirAll and CreateTemp succeed
	// and a temp file CARRYING THE BEARER TOKEN is created in the operator's
	// working directory; os.Rename to "" then fails, the deferred cleanup
	// removes it, and `olivares auth login` reports
	// «replace client config : rename ./.config-1450271771 : no such file or
	// directory» — a message that names neither the cause nor the remedy. The
	// token does not persist, but it is written to a directory nobody chose,
	// and the operator is left with an unintelligible error. Refusing is the
	// only correct answer: the operator must say where the config goes.
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("refuse to write the client config: %w", errNoUserConfigDir)
	}
	if err := validateCLIConfig(cfg); err != nil {
		return fmt.Errorf("refuse to write invalid client config: %w", err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = []cliContext{}
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode client config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create client config directory %s: %w", dir, err)
	}
	// Re-tighten UNCONDITIONALLY: a pre-existing ~/.config/olivares created by
	// hand (or by an older build) with wider permissions must not stay wide —
	// the file is 0600 either way, this is defense in depth for the directory.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure client config directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary client config: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary client config: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write temporary client config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary client config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary client config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace client config %s: %w", path, err)
	}
	keep = true
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure client config %s: %w", path, err)
	}
	return nil
}

func validateCLIConfig(cfg cliConfig) error {
	seen := make(map[string]struct{}, len(cfg.Contexts))
	for i, ctx := range cfg.Contexts {
		if strings.TrimSpace(ctx.Name) == "" {
			return fmt.Errorf("contexts[%d].name is required", i)
		}
		if _, exists := seen[ctx.Name]; exists {
			return fmt.Errorf("duplicate context name %q", ctx.Name)
		}
		seen[ctx.Name] = struct{}{}
	}
	if cfg.CurrentContext != "" {
		if _, exists := seen[cfg.CurrentContext]; !exists {
			return fmt.Errorf("current-context %q does not exist", cfg.CurrentContext)
		}
	}
	return nil
}

func (cfg cliConfig) context(name string) (cliContext, bool) {
	for _, ctx := range cfg.Contexts {
		if ctx.Name == name {
			return ctx, true
		}
	}
	return cliContext{}, false
}

func (cfg *cliConfig) upsertContext(updated cliContext) {
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == updated.Name {
			cfg.Contexts[i] = updated
			return
		}
	}
	cfg.Contexts = append(cfg.Contexts, updated)
}

func (cfg *cliConfig) removeContext(name string) bool {
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == name {
			cfg.Contexts = append(cfg.Contexts[:i], cfg.Contexts[i+1:]...)
			if cfg.CurrentContext == name {
				cfg.CurrentContext = ""
			}
			return true
		}
	}
	return false
}

// resolveCLIConfig applies the documented precedence independently per value:
// explicit flag, non-empty environment value, then the active file context.
// TLS CA/pins have no environment form and resolve flag then active context.
func resolveCLIConfig(opts cliResolutionOptions) (cliResolvedConfig, error) {
	cfg, path, err := loadCLIConfig()
	if err != nil {
		return cliResolvedConfig{}, err
	}
	active, _ := cfg.context(cfg.CurrentContext)
	resolved := cliResolvedConfig{
		ContextName: cfg.CurrentContext,
		ConfigPath:  path,
		Server:      resolveCLIValue(opts.Server, opts.ServerExplicit, os.Getenv("OLIVARES_SERVER_URL"), active.Server),
	}
	if !opts.SkipCredentials {
		resolved.Token = resolveCLIValue(opts.Token, opts.TokenExplicit, os.Getenv("OLIVARES_TOKEN"), active.Token)
		resolved.Tenant = resolveCLIValue(opts.Tenant, opts.TenantExplicit, os.Getenv("OLIVARES_TENANT"), active.Tenant)
	}
	if opts.CACertExplicit {
		resolved.CACert = opts.CACert
	} else {
		resolved.CACert = active.CACert
	}
	if opts.PinsExplicit {
		resolved.PinSHA256 = append([]string(nil), opts.PinSHA256...)
	} else {
		resolved.PinSHA256 = append([]string(nil), active.PinSHA256...)
	}
	if resolved.Server != "" {
		resolved.Server, err = normalizeCLIServer(resolved.Server)
		if err != nil {
			return cliResolvedConfig{}, err
		}
	}
	if strings.ContainsAny(resolved.Token, "\r\n") {
		return cliResolvedConfig{}, errors.New("token must not contain newline characters")
	}
	if strings.ContainsAny(resolved.Tenant, "\r\n") {
		return cliResolvedConfig{}, errors.New("tenant must not contain newline characters")
	}
	// ONE normalized tenant for the local check, the X-Olivares-Tenant header, the
	// request body and the output. Trimming at each use site instead — which is what
	// `tokens issue` and `members grant` did — let `--tenant " t_acme"` pass a local
	// "is it set?" check and then travel with the space, to be refused by an engine
	// that compares exactly. Trimming HERE means the same string reaches all four.
	resolved.Tenant = strings.TrimSpace(resolved.Tenant)
	return resolved, nil
}

func resolveCLIValue(flagValue string, explicit bool, envValue, contextValue string) string {
	if explicit {
		return flagValue
	}
	if envValue != "" {
		return envValue
	}
	return contextValue
}

func normalizeCLIServer(server string) (string, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	u, err := url.Parse(server)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q: an absolute http(s) URL is required", server)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid server URL %q: scheme must be http or https", server)
	}
	if u.User != nil {
		return "", errors.New("invalid server URL: user information is not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid server URL %q: query strings and fragments are not allowed", server)
	}
	return server, nil
}
