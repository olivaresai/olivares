// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

var (
	ErrMCPGatewayInvalid     = errors.New("invalid MCP gateway configuration")
	ErrMCPGatewayFileOwned   = errors.New("MCP gateway configuration is owned by the operator file")
	ErrMCPGatewayUnavailable = errors.New("MCP gateway configuration unavailable")
)

const mcpGatewayRosterName = "mcp-gateway"
const mcpGatewayRosterKind = "olivares.mcp-gateway"
const mcpGatewayRosterKey = "gateway_json"

// MCPGatewayConfig uses the existing auth-partition configuration repository's
// business-tenant scope. It is NOT an observation source: the runtime reconciler
// reads only GlobalSourceScope, and generic console source CRUD authors only that
// global scope. Module repositories cannot reach the auth partition.
type MCPGatewayConfig struct {
	SessionTools bool               `json:"session_tools"`
	Servers      []MCPGatewayServer `json:"servers"`
}

type MCPGatewayTrust struct {
	Resource string          `json:"resource"`
	Issuer   string          `json:"issuer"`
	JWKSURL  string          `json:"jwks_url,omitempty"`
	JWKS     json.RawMessage `json:"jwks,omitempty"`
}

type MCPGatewayToolPolicy struct {
	Name          string `json:"name"`
	RequiredScope string `json:"required_scope"`
	Destructive   bool   `json:"destructive"`
}

type MCPGatewayTool struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	ReadOnly    bool   `json:"read_only,omitempty"`
}

type MCPGatewayProbe struct {
	State    string           `json:"state"`
	TestedAt string           `json:"tested_at,omitempty"`
	Tools    []MCPGatewayTool `json:"tools"`
	// Reason says why an "unreachable" test failed, so the console can name the next step:
	// Remote connection class, or process_start/process_exit for local servers.
	// Detail is the redacted last stderr line or OS error for a local failure.
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// Input contains references and public trust anchors only. Discovery is an
// observation; AllowedTools is a separate administrator-authored policy.
// EgressHosts confines a command server's network to these HTTPS hosts (host or
// host:port); empty keeps the network of the launch the command runs next to.
type MCPGatewayServerInput struct {
	Name          string                 `json:"name"`
	Transport     string                 `json:"transport"`
	URL           string                 `json:"url"`
	Command       string                 `json:"command,omitempty"`
	Args          []string               `json:"args,omitempty"`
	Env           map[string]string      `json:"env,omitempty"`
	EnvSecretRefs map[string]string      `json:"env_secret_refs,omitempty"`
	CredentialRef string                 `json:"credential_ref,omitempty"`
	EgressCIDRs   []string               `json:"egress_cidrs"`
	EgressHosts   []string               `json:"egress_hosts,omitempty"`
	Trust         MCPGatewayTrust        `json:"trust"`
	AllowedTools  []MCPGatewayToolPolicy `json:"allowed_tools"`
	Enabled       bool                   `json:"enabled"`
}

type MCPGatewayServer struct {
	MCPGatewayServerInput
	ID    string          `json:"id"`
	Probe MCPGatewayProbe `json:"probe"`
	// Response-only catalogue hints, never tool authority. A snapshot supplies a
	// non-nil pointer even for an empty proposal; stored configuration omits it.
	ProposedAllow *[]string `json:"proposed_allow,omitempty"`
}

type MCPGatewaySnapshot struct {
	MCPGatewayConfig
	Version         int64             `json:"version"`
	Source          string            `json:"source"`
	ReadOnly        bool              `json:"read_only"`
	SessionEndpoint string            `json:"session_endpoint"`
	Governance      map[string]string `json:"governance"`
}

type MCPGatewayStore struct{ st store.Store }

func NewMCPGatewayStore(st store.Store) *MCPGatewayStore { return &MCPGatewayStore{st: st} }

func mcpGatewayTenant(tenant model.TenantID) error {
	parsed, err := model.ParseTenantID(tenant.String())
	if err != nil || parsed.IsZero() || parsed == model.SystemTenantID || parsed != tenant {
		return ErrMCPGatewayInvalid
	}
	return nil
}

func (s *MCPGatewayStore) load(ctx context.Context, tenant model.TenantID) (model.SourceDef, MCPGatewayConfig, error) {
	if err := mcpGatewayTenant(tenant); err != nil {
		return model.SourceDef{}, MCPGatewayConfig{}, err
	}
	row, found, err := NewSourceStore(s.st).Get(ctx, tenant, mcpGatewayRosterName)
	if err != nil {
		return row, MCPGatewayConfig{}, err
	}
	cfg := MCPGatewayConfig{Servers: []MCPGatewayServer{}}
	if !found {
		cfg.SessionTools = true
		return row, cfg, nil
	}
	if row.Scope != tenant || row.Tenant != tenant.String() || row.Kind != mcpGatewayRosterKind || row.Enabled || row.Plugin != nil || len(row.Config) != 1 {
		return row, cfg, ErrMCPGatewayUnavailable
	}
	raw := row.Config[mcpGatewayRosterKey]
	if len(raw) == 0 || len(raw) > 256<<10 {
		return row, cfg, ErrMCPGatewayUnavailable
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return row, cfg, ErrMCPGatewayUnavailable
	}
	if dec.Decode(new(any)) != io.EOF {
		return row, cfg, ErrMCPGatewayUnavailable
	}
	if len(cfg.Servers) > 32 {
		return row, cfg, ErrMCPGatewayUnavailable
	}
	ids := map[string]bool{}
	for i, server := range cfg.Servers {
		cfg.Servers[i].ProposedAllow = nil
		id, err := model.ParseID(server.ID)
		if err != nil || id.IsZero() || id.String() != server.ID || ids[server.ID] || validateMCPGatewayServer(server.MCPGatewayServerInput) != nil {
			return row, cfg, ErrMCPGatewayUnavailable
		}
		if server.Enabled && (server.Probe.State != "ok" || !mcpGatewayTrustComplete(server.Trust)) {
			return row, cfg, ErrMCPGatewayUnavailable
		}
		if !validMCPGatewayProbe(server.Probe, true) {
			return row, cfg, ErrMCPGatewayUnavailable
		}
		ids[server.ID] = true
	}
	if cfg.Servers == nil {
		cfg.Servers = []MCPGatewayServer{}
	}
	return row, cfg, nil
}

func (s *MCPGatewayStore) Get(ctx context.Context, tenant model.TenantID) (MCPGatewaySnapshot, error) {
	row, cfg, err := s.load(ctx, tenant)
	return mcpGatewaySnapshot(cfg, row.Version), err
}

func mcpGatewaySnapshot(cfg MCPGatewayConfig, version int64) MCPGatewaySnapshot {
	// Keep the derived proposal out of the persisted roster. Only the current
	// successful catalogue contributes names; its declarations prove no safety.
	cfg.Servers = slices.Clone(cfg.Servers)
	for i, server := range cfg.Servers {
		proposal := []string{}
		if server.Probe.State == "ok" {
			for _, tool := range server.Probe.Tools {
				if tool.ReadOnly {
					proposal = append(proposal, tool.Name)
				}
			}
		}
		cfg.Servers[i].ProposedAllow = &proposal
	}
	return MCPGatewaySnapshot{MCPGatewayConfig: cfg, Version: version, Source: "store", SessionEndpoint: "/session/mcp"}
}

// mutate reads the CAS version and publishes config plus semantic audit atomically.
// Repository.Update checks the same version at the SQL write, so a competing
// writer after this read cannot overwrite the winner.
func (s *MCPGatewayStore) mutate(ctx context.Context, actor Principal, tenant model.TenantID, version int64, action, id string, change func(*MCPGatewayConfig) error) (MCPGatewaySnapshot, error) {
	row, cfg, err := s.load(ctx, tenant)
	if err != nil {
		return MCPGatewaySnapshot{}, err
	}
	if version < 0 || row.Version != version {
		return MCPGatewaySnapshot{}, store.ErrConflict
	}
	if err := change(&cfg); err != nil {
		return MCPGatewaySnapshot{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil || len(raw) > 256<<10 {
		return MCPGatewaySnapshot{}, ErrMCPGatewayInvalid
	}
	if row.ID.IsZero() {
		row = model.SourceDef{Name: mcpGatewayRosterName, Scope: tenant, Tenant: tenant.String(), Kind: mcpGatewayRosterKind}
	}
	row.Config = map[string]string{mcpGatewayRosterKey: string(raw)}
	err = s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		if row.ID.IsZero() {
			row, err = as.Sources().Create(ctx, row)
		} else {
			row, err = as.Sources().Update(ctx, row)
		}
		if err != nil {
			return err
		}
		event, err := as.Audit().Append(ctx, model.AuditDraft{Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: action,
			TargetKind: "core.source_def", TargetID: row.ID,
			Meta: map[string]any{"tenant_id": tenant.String(), "server_id": id, "version": row.Version}})
		if err == nil && event.Seq == 0 {
			return ErrMCPGatewayUnavailable
		}
		return err
	})
	if err != nil {
		return MCPGatewaySnapshot{}, err
	}
	return mcpGatewaySnapshot(cfg, row.Version), nil
}

func (s *MCPGatewayStore) PutServer(ctx context.Context, actor Principal, tenant model.TenantID, version int64, id string, in MCPGatewayServerInput) (MCPGatewaySnapshot, error) {
	in = in.WithDefaults()
	if err := validateMCPGatewayServer(in); err != nil {
		return MCPGatewaySnapshot{}, err
	}
	if in.EgressCIDRs == nil {
		in.EgressCIDRs = []string{}
	}
	create := id == ""
	if create {
		id = model.NewID().String()
		if in.Enabled {
			return MCPGatewaySnapshot{}, fmt.Errorf("%w: new servers must be disabled", ErrMCPGatewayInvalid)
		}
	}
	action := "mcp_gateway.server.update"
	if create {
		action = "mcp_gateway.server.create"
	}
	return s.mutate(ctx, actor, tenant, version, action, id, func(cfg *MCPGatewayConfig) error {
		if create {
			if len(cfg.Servers) >= 32 {
				return fmt.Errorf("%w: at most 32 servers", ErrMCPGatewayInvalid)
			}
			if in.AllowedTools == nil {
				in.AllowedTools = []MCPGatewayToolPolicy{}
			}
			cfg.Servers = append(cfg.Servers, MCPGatewayServer{MCPGatewayServerInput: in, ID: id, Probe: MCPGatewayProbe{State: "never_tested", Tools: []MCPGatewayTool{}}})
			return nil
		}
		for i := range cfg.Servers {
			old := cfg.Servers[i]
			if old.ID != id {
				continue
			}
			// Changing destination, credentials or address grants withdraws the
			// connection observation. A new destination cannot inherit approval.
			changed := old.Transport != in.Transport || old.URL != in.URL || old.Command != in.Command ||
				!slices.Equal(old.Args, in.Args) || !maps.Equal(old.Env, in.Env) ||
				!maps.Equal(old.EnvSecretRefs, in.EnvSecretRefs) || old.CredentialRef != in.CredentialRef || !slices.Equal(old.EgressCIDRs, in.EgressCIDRs) ||
				!slices.Equal(old.EgressHosts, in.EgressHosts)
			if changed {
				old.Probe = MCPGatewayProbe{State: "never_tested", Tools: []MCPGatewayTool{}}
			}
			if in.Enabled {
				if old.Probe.State != "ok" || !mcpGatewayTrustComplete(in.Trust) {
					return fmt.Errorf("%w: enable requires a current successful test and complete trust if supplied", ErrMCPGatewayInvalid)
				}
				// Server hints never authorize effects. Omitted policies make every
				// tested tool Ask; only an explicit administrator list may Allow.
				// An explicit empty list still grants none.
				if in.AllowedTools == nil {
					for _, tool := range old.Probe.Tools {
						in.AllowedTools = append(in.AllowedTools, MCPGatewayToolPolicy{Name: tool.Name, RequiredScope: "tools:call", Destructive: true})
					}
				}
				observed := map[string]bool{}
				for _, tool := range old.Probe.Tools {
					observed[tool.Name] = true
				}
				for _, tool := range in.AllowedTools {
					if !observed[tool.Name] {
						return fmt.Errorf("%w: allowed tool was not observed", ErrMCPGatewayInvalid)
					}
				}
			}
			if in.AllowedTools == nil {
				in.AllowedTools = []MCPGatewayToolPolicy{}
			}
			old.MCPGatewayServerInput = in
			cfg.Servers[i] = old
			return nil
		}
		return store.ErrNotFound
	})
}

func (s *MCPGatewayStore) DeleteServer(ctx context.Context, actor Principal, tenant model.TenantID, version int64, id string) (MCPGatewaySnapshot, error) {
	return s.mutate(ctx, actor, tenant, version, "mcp_gateway.server.delete", id, func(cfg *MCPGatewayConfig) error {
		for i, row := range cfg.Servers {
			if row.ID == id {
				cfg.Servers = append(cfg.Servers[:i], cfg.Servers[i+1:]...)
				return nil
			}
		}
		return store.ErrNotFound
	})
}

func (s *MCPGatewayStore) SetSessionTools(ctx context.Context, actor Principal, tenant model.TenantID, version int64, enabled bool) (MCPGatewaySnapshot, error) {
	return s.mutate(ctx, actor, tenant, version, "mcp_gateway.session_tools.update", "", func(cfg *MCPGatewayConfig) error { cfg.SessionTools = enabled; return nil })
}

func (s *MCPGatewayStore) SaveProbe(ctx context.Context, actor Principal, tenant model.TenantID, version int64, id string, probe MCPGatewayProbe) (MCPGatewaySnapshot, error) {
	if probe.Tools == nil {
		probe.Tools = []MCPGatewayTool{}
	}
	if !validMCPGatewayProbe(probe, false) {
		return MCPGatewaySnapshot{}, ErrMCPGatewayInvalid
	}
	return s.mutate(ctx, actor, tenant, version, "mcp_gateway.server.test", id, func(cfg *MCPGatewayConfig) error {
		for i := range cfg.Servers {
			if cfg.Servers[i].ID == id {
				cfg.Servers[i].Probe = probe
				if probe.State != "ok" {
					cfg.Servers[i].Enabled = false
				}
				return nil
			}
		}
		return store.ErrNotFound
	})
}

var mcpProbeReasons = map[string]bool{"dns": true, "tls": true, "timeout": true, "connection_refused": true, "http_status": true, "process_start": true, "process_exit": true}

var mcpProbeStates = map[string]bool{"ok": true, "refused": true, "unreachable": true, "invalid_response": true, "egress_denied": true, "redirect_refused": true, "credential_unavailable": true}
var mcpToolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var mcpScope = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,128}$`)
var mcpEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// The runner supplies a private child home and scratch directory. The roster
// must not redirect package caches into a project or another account's home.
func mcpRunnerEnvName(name string) bool {
	return name == "HOME" || name == "TMPDIR" || name == "TMP" || name == "TEMP"
}

// WithDefaults selects the transport from the provided command or URL. It does
// not enable a server or grant tools.
func (in MCPGatewayServerInput) WithDefaults() MCPGatewayServerInput {
	if in.Transport == "" {
		in.Transport = "streamable_http"
		if in.Command != "" {
			in.Transport = "stdio"
		}
	}
	return in
}

func mcpGatewayTrustComplete(trust MCPGatewayTrust) bool {
	if trust.Resource == "" && trust.Issuer == "" && trust.JWKSURL == "" && len(trust.JWKS) == 0 {
		return true // exact-session authentication does not need external OAuth trust
	}
	return trust.Resource != "" && trust.Issuer != "" && (trust.JWKSURL != "" || len(trust.JWKS) != 0)
}

func validateMCPGatewayServer(in MCPGatewayServerInput) error {
	bad := func(message string) error { return fmt.Errorf("%w: %s", ErrMCPGatewayInvalid, message) }
	if strings.TrimSpace(in.Name) != in.Name || len(in.Name) == 0 || len(in.Name) > 128 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return bad("provide a bounded server name")
	}
	switch in.Transport {
	case "streamable_http":
		if !mcpPublicURL(in.URL) || in.Command != "" || len(in.Args)+len(in.Env)+len(in.EnvSecretRefs)+len(in.EgressHosts) != 0 {
			return bad("provide an HTTPS URL without credentials, query or fragment")
		}
	case "stdio":
		if in.URL != "" || in.CredentialRef != "" || len(in.EgressCIDRs) != 0 || strings.TrimSpace(in.Command) != in.Command || in.Command == "" || len(in.Command) > 4096 || strings.ContainsAny(in.Command, "\r\n\x00") || secret.ContainsInlineCredential(in.Command) {
			return bad("provide a command without inline credentials; stdio secrets belong in env_secret_refs")
		}
		if len(in.Args) > 128 || len(in.Env)+len(in.EnvSecretRefs) > 64 {
			return bad("at most 128 arguments and 64 environment entries")
		}
		for _, arg := range in.Args {
			if len(arg) > 4096 || strings.ContainsAny(arg, "\r\n\x00") || secret.ContainsInlineCredential(arg) || strings.HasPrefix(arg, "store:") {
				return bad("arguments must be bounded public values; use env_secret_refs for secrets")
			}
		}
		for name, value := range in.Env {
			if !mcpEnvName.MatchString(name) || mcpRunnerEnvName(name) || strings.HasPrefix(name, "OLIVARES_") || secret.IsCredentialBearingConfigKey(name) || len(value) > 8192 || strings.ContainsRune(value, '\x00') || secret.ContainsInlineCredential(value) {
				return bad("env must contain bounded public values; use env_secret_refs for secrets")
			}
		}
		for name, value := range in.EnvSecretRefs {
			ref, ok := secret.ParseReference(value)
			_, duplicate := in.Env[name]
			if !mcpEnvName.MatchString(name) || mcpRunnerEnvName(name) || strings.HasPrefix(name, "OLIVARES_") || duplicate || !ok || ref.Scheme != secret.SchemeStore || ValidateSecretName(ref.Locator) != "" || !strings.HasPrefix(ref.Locator, "mcp/") || value != "store:"+ref.Locator {
				return bad("env_secret_refs must map unique environment names to tenant store:mcp/ references")
			}
		}
	default:
		return bad("transport must be stdio or streamable_http")
	}
	if in.CredentialRef != "" {
		ref, ok := secret.ParseReference(in.CredentialRef)
		if !ok || ref.Scheme != secret.SchemeStore || ValidateSecretName(ref.Locator) != "" || !strings.HasPrefix(ref.Locator, "mcp/") || in.CredentialRef != "store:"+ref.Locator {
			return bad("credential_ref must be a tenant-scoped store reference")
		}
	}
	if len(in.EgressCIDRs) > 16 {
		return bad("at most 16 explicit address grants")
	}
	if len(in.EgressHosts) > 16 {
		return bad("at most 16 egress hosts")
	}
	hosts := map[string]bool{}
	for _, host := range in.EgressHosts {
		if !mcpEgressHost(host) || hosts[host] {
			return bad("egress hosts must be unique lowercase HTTPS hosts, host or host:port")
		}
		hosts[host] = true
	}
	for _, cidr := range in.EgressCIDRs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil || network.String() != cidr {
			return bad("address grants must be canonical CIDRs")
		}
	}
	if len(in.AllowedTools) > 128 {
		return bad("at most 128 tool policies")
	}
	names := map[string]bool{}
	for _, p := range in.AllowedTools {
		if !mcpToolName.MatchString(p.Name) || !mcpScope.MatchString(p.RequiredScope) || names[p.Name] {
			return bad("each tool needs a unique name and explicit required scope")
		}
		names[p.Name] = true
	}
	for _, value := range []string{in.Trust.Resource, in.Trust.Issuer, in.Trust.JWKSURL} {
		if value != "" && !mcpPublicURL(value) {
			return bad("trust URLs must be HTTPS without credentials, query or fragment")
		}
	}
	if in.Trust.JWKSURL != "" && len(in.Trust.JWKS) > 0 {
		return bad("provide exactly one public JWKS or JWKS URL")
	}
	if len(in.Trust.JWKS) > 0 {
		var object map[string]any
		if len(in.Trust.JWKS) > 32<<10 || json.Unmarshal(in.Trust.JWKS, &object) != nil || !mcpPublicJWKS(object) {
			return bad("JWKS must contain only bounded public keys")
		}
	}
	return nil
}

func mcpPublicURL(raw string) bool {
	u, err := url.Parse(raw)
	_, destinationErr := egress.ParseDestination(raw)
	return destinationErr == nil && strings.TrimSpace(raw) == raw && err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !secret.ContainsInlineCredential(raw) && len(raw) <= 2048
}

// mcpEgressHost accepts the authority of an HTTPS URL in canonical form only,
// so the stored grant is exactly what the egress proxy admits. The proxy admits
// public addresses only; a loopback, private or local name is refused here.
func mcpEgressHost(host string) bool {
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "*%\\") || strings.HasSuffix(host, ":") {
		return false
	}
	destination, err := egress.ParseDestination(u.String())
	if err != nil || destination.Host != u.Hostname() || destination.Host == "localhost" || strings.HasSuffix(destination.Host, ".localhost") ||
		(destination.IP != nil && egress.ReservedAddress(destination.IP)) {
		return false
	}
	return u.Port() == "" || u.Port() == strconv.Itoa(destination.Port) && u.Port() != "443"
}

func mcpPublicJWKS(object map[string]any) bool {
	if len(object) != 1 {
		return false
	}
	keys, ok := object["keys"].([]any)
	if !ok || len(keys) == 0 || len(keys) > 16 {
		return false
	}
	allowed := map[string]bool{"kty": true, "crv": true, "x": true, "y": true, "n": true, "e": true, "kid": true, "use": true, "alg": true, "key_ops": true, "x5c": true, "x5t": true, "x5t#S256": true}
	for _, item := range keys {
		key, ok := item.(map[string]any)
		if !ok {
			return false
		}
		for name := range key {
			if !allowed[name] {
				return false
			}
		}
		switch key["kty"] {
		case "OKP", "EC", "RSA":
		default:
			return false
		}
	}
	return true
}

func validMCPGatewayProbe(probe MCPGatewayProbe, allowNever bool) bool {
	if !mcpProbeStates[probe.State] && !(allowNever && probe.State == "never_tested") {
		return false
	}
	if len(probe.Tools) > 128 || (probe.State != "ok" && len(probe.Tools) != 0) {
		return false
	}
	if probe.Reason != "" && (probe.State != "unreachable" || !mcpProbeReasons[probe.Reason]) {
		return false
	}
	if len(probe.Detail) > 512 || !utf8.ValidString(probe.Detail) || strings.ContainsFunc(probe.Detail, unicode.IsControl) ||
		(probe.Detail != "" && (probe.State != "unreachable" || probe.Reason == "")) {
		return false
	}
	if (probe.Reason == "http_status") != (probe.HTTPStatus != 0) || (probe.HTTPStatus != 0 && (probe.HTTPStatus < 400 || probe.HTTPStatus > 599)) {
		return false
	}
	names := map[string]bool{}
	for _, tool := range probe.Tools {
		if !mcpToolName.MatchString(tool.Name) || names[tool.Name] || len(tool.Fingerprint) != 64 || strings.Trim(tool.Fingerprint, "0123456789abcdef") != "" {
			return false
		}
		names[tool.Name] = true
	}
	return true
}
