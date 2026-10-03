// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

// mcpManagement keeps source ownership immutable for this boot. Store-mode reads
// are live; API writes and local protocol requests share the same gate, so a
// completed disable cannot be followed by a new local dispatch on an old cache.
type mcpManagement struct {
	mu                   sync.RWMutex
	cacheMu              sync.Mutex
	store                *auth.MCPGatewayStore
	secrets              *auth.SecretStore
	cfg                  agentGatewayConfig
	source               string
	eng                  *engine
	cache                map[string]managedMCPServer
	sessionCache         map[string]*managedSessionServer
	sessionAuthenticator sessionMCPAuthenticator
}

type managedMCPServer struct {
	version int64
	server  *mcpc.ResourceServer
}

func newMCPManagement(st store.Store, secrets *auth.SecretStore, cfg agentGatewayConfig, filePresent bool) (*mcpManagement, error) {
	if err := validateAgentGatewaySource(cfg); err != nil {
		return nil, err
	}
	source := cfg.MCPSource
	if source == "" {
		source = "store"
		if filePresent {
			source = "file"
		}
	}
	return &mcpManagement{store: auth.NewMCPGatewayStore(st), secrets: secrets, cfg: cfg, source: source, cache: map[string]managedMCPServer{}}, nil
}

func (m *mcpManagement) Get(ctx context.Context, tenant model.TenantID) (auth.MCPGatewaySnapshot, error) {
	if m.source == "file" {
		out := auth.MCPGatewaySnapshot{Source: "file", ReadOnly: true, SessionEndpoint: "/session/mcp", MCPGatewayConfig: auth.MCPGatewayConfig{SessionTools: m.cfg.SessionTools, Servers: []auth.MCPGatewayServer{}}}
		if cfg := m.cfg.MCP; cfg != nil {
			configured, present, err := parseBusinessTenant("MCP tenant", cfg.Tenant)
			if err == nil && present && configured == tenant {
				row := auth.MCPGatewayServer{ID: "file", MCPGatewayServerInput: auth.MCPGatewayServerInput{Name: "Operator file", Transport: "streamable_http", URL: safeMCPConfigURL(cfg.UpstreamURL), Enabled: cfg.Resource != "", Trust: auth.MCPGatewayTrust{Resource: safeMCPConfigURL(cfg.Resource), Issuer: safeMCPConfigURL(cfg.Issuer), JWKSURL: safeMCPConfigURL(cfg.JWKSURL)}}, Probe: auth.MCPGatewayProbe{State: "file_owned", Tools: []auth.MCPGatewayTool{}}, ProposedAllow: &[]string{}}
				for _, policy := range cfg.Tools {
					row.AllowedTools = append(row.AllowedTools, auth.MCPGatewayToolPolicy{Name: policy.Name, RequiredScope: policy.RequiredScope, Destructive: policy.Destructive})
				}
				out.Servers = append(out.Servers, row)
			}
		}
		out.Governance = map[string]string{"configuration": "operator_file_read_only", "credential": "operator_file_not_returned", "session_listener": "operator_file_listener", "content_gate": "declared_inventory_and_consent", "deep_content_inspection": "not_configured", "egress": "operator_provisioned", "redirects": "legacy_forwarder"}
		return out, nil
	}
	out, err := m.store.Get(ctx, tenant)
	if err != nil {
		return out, err
	}
	out.Governance = managedMCPGovernance()
	return out, nil
}

func managedMCPGovernance() map[string]string {
	return map[string]string{"configuration": "tenant_store", "credential": "tenant_sealed_reference", "session_listener": "control_plane_http_listener", "content_gate": "declared_inventory_and_consent", "deep_content_inspection": "not_configured", "egress": "exact_https_destination_and_pinned_addresses", "redirects": "refused", "tool_policy": "explicit_scope_and_destructive_approval", "tasks": "not_provisioned", "subscriptions": "not_provisioned", "local_execution": "session_runner_engine_user_session_folder", "process_confinement": "reported_by_session_runner", "network_confinement": "not_supplied_for_local_commands"}
}

func safeMCPConfigURL(raw string) string {
	if secret.ContainsInlineCredential(raw) {
		return "configured_in_file"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "configured_in_file"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (m *mcpManagement) PutServer(ctx context.Context, p auth.Principal, tenant model.TenantID, version int64, id string, in auth.MCPGatewayServerInput) (auth.MCPGatewaySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != "store" {
		return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayFileOwned
	}
	in = in.WithDefaults()
	for _, reference := range in.EnvSecretRefs {
		ref, ok := secret.ParseReference(reference)
		if !ok || ref.Scheme != secret.SchemeStore || reference != "store:"+ref.Locator || !strings.HasPrefix(ref.Locator, "mcp/") {
			return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayInvalid
		}
		if m.secrets == nil {
			return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayUnavailable
		}
		if _, found, err := m.secrets.Get(ctx, tenant, ref.Locator); err != nil {
			return auth.MCPGatewaySnapshot{}, err
		} else if !found {
			return auth.MCPGatewaySnapshot{}, auth.ErrSecretNotFound
		}
	}
	if in.CredentialRef != "" {
		ref, ok := secret.ParseReference(in.CredentialRef)
		if !ok || ref.Scheme != secret.SchemeStore || in.CredentialRef != "store:"+ref.Locator || !strings.HasPrefix(ref.Locator, "mcp/") {
			return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayInvalid
		}
		if m.secrets == nil {
			return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayUnavailable
		}
		_, found, err := m.secrets.Get(ctx, tenant, ref.Locator)
		if err != nil {
			return auth.MCPGatewaySnapshot{}, err
		}
		if !found {
			return auth.MCPGatewaySnapshot{}, auth.ErrSecretNotFound
		}
	}
	if in.Enabled && in.Trust.Resource != "" && !managedMCPResourcePath(in.Trust.Resource, tenant, id) {
		return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayInvalid
	}
	if in.Enabled && in.Trust.Resource != "" && m.eng != nil {
		if _, err := m.buildServer(tenant, auth.MCPGatewayServer{ID: id, MCPGatewayServerInput: in}); err != nil {
			return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayInvalid
		}
	}
	out, err := m.store.PutServer(ctx, p, tenant, version, id, in)
	if err != nil {
		return out, err
	}
	m.invalidate(tenant)
	return m.Get(ctx, tenant)
}

func (m *mcpManagement) DeleteServer(ctx context.Context, p auth.Principal, tenant model.TenantID, version int64, id string) (auth.MCPGatewaySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != "store" {
		return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayFileOwned
	}
	if _, err := m.store.DeleteServer(ctx, p, tenant, version, id); err != nil {
		return auth.MCPGatewaySnapshot{}, err
	}
	m.invalidate(tenant)
	return m.Get(ctx, tenant)
}

func (m *mcpManagement) SetSessionTools(ctx context.Context, p auth.Principal, tenant model.TenantID, version int64, enabled bool) (auth.MCPGatewaySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != "store" {
		return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayFileOwned
	}
	if _, err := m.store.SetSessionTools(ctx, p, tenant, version, enabled); err != nil {
		return auth.MCPGatewaySnapshot{}, err
	}
	m.invalidate(tenant)
	return m.Get(ctx, tenant)
}

func (m *mcpManagement) invalidate(tenant model.TenantID) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	m.invalidateSessions(tenant)
	for key := range m.cache {
		if strings.HasPrefix(key, tenant.String()+"/") {
			delete(m.cache, key)
		}
	}
}

func managedMCPResourcePath(resource string, tenant model.TenantID, id string) bool {
	u, err := url.Parse(resource)
	return err == nil && id != "" && u.EscapedPath() == "/mcp/gateway/"+tenant.String()+"/"+id
}

var errManagedMCPEgress = errors.New("managed MCP destination not admitted")
var errManagedMCPRefused = errors.New("managed MCP authentication refused")
var errManagedMCPInvalidResponse = errors.New("managed MCP invalid response")
var errManagedMCPRedirect = errors.New("managed MCP redirect refused")

// errManagedMCPResolve is a name that did not resolve. It is not an egress refusal: the
// policy never saw an address.
var errManagedMCPResolve = errors.New("managed MCP host did not resolve")

type managedMCPTransport struct {
	inner    http.RoundTripper
	endpoint string
}

func (t managedMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.endpoint || req.Method != http.MethodPost {
		return nil, errManagedMCPEgress
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errManagedMCPRedirect
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		_ = resp.Body.Close()
		return nil, errManagedMCPRefused
	}
	return resp, nil
}

// The policy is evaluated at connection time and its exact addresses are dialed.
// No environment proxy, redirect, second DNS lookup or arbitrary private-network
// flag can move an upstream credential to a different destination.
func newManagedMCPClient(in auth.MCPGatewayServerInput, timeout time.Duration) (*http.Client, error) {
	destination, err := egress.ParseDestination(in.URL)
	if err != nil {
		return nil, errManagedMCPEgress
	}
	policy := egress.Policy{InForce: true, Allow: []egress.Rule{{Host: destination.Host, Ports: []egress.PortRange{{Low: destination.Port, High: destination.Port}}}}}
	for _, cidr := range in.EgressCIDRs {
		policy.Allow = append(policy.Allow, egress.Rule{CIDR: cidr, Ports: []egress.PortRange{{Low: destination.Port, High: destination.Port}}})
	}
	if policy.Validate() != nil {
		return nil, errManagedMCPEgress
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DisableKeepAlives = true // every dispatch re-evaluates DNS and current address admission
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errManagedMCPEgress
		}
		canonical, err := egress.CanonicalHost(host)
		if err != nil || canonical != destination.Host || port != destinationPort(in.URL) {
			return nil, errManagedMCPEgress
		}
		ips, err := egress.Resolve(ctx, egress.NetResolver{}, destination)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errManagedMCPResolve, err)
		}
		// The shared evaluator permits loopback for existing local connectors.
		// Managed gateway admission is explicit even for that address class.
		for _, ip := range ips {
			if !egress.ReservedAddress(ip) {
				continue
			}
			admitted := false
			for _, cidr := range in.EgressCIDRs {
				_, network, _ := net.ParseCIDR(cidr)
				if network != nil && network.Contains(ip) {
					admitted = true
					break
				}
			}
			if !admitted {
				return nil, errManagedMCPEgress
			}
		}
		decision := egress.Evaluate(policy, destination, ips)
		if !decision.Permitted {
			return nil, errManagedMCPEgress
		}
		ctx = egress.WithPin(ctx, decision.Pin)
		ctx = egress.WithReservedAuthorization(ctx, decision.ReservedAuthorized)
		return egress.DialPinned(ctx, dialer, network, address)
	}
	// cli-transport-exempt: ENGINE→upstream MCP server, not a CLI-to-control-plane call.
	// The tenant's upstream credential uses exact destination and DNS-pinned egress;
	// a human CLI context must not supply its transport, credential or tenant scope.
	return &http.Client{Transport: managedMCPTransport{inner: base, endpoint: in.URL}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errManagedMCPRedirect }}, nil
}

func destinationPort(endpoint string) string {
	u, _ := url.Parse(endpoint)
	if u.Port() != "" {
		return u.Port()
	}
	return "443"
}

type tenantMCPCredentialProvider struct {
	store  *auth.SecretStore
	tenant model.TenantID
	ref    string
	target string
}

func (p tenantMCPCredentialProvider) Credential(ctx context.Context, target string) (string, error) {
	if target != p.target {
		return "", errManagedMCPEgress
	}
	if p.ref == "" {
		return "", nil
	}
	ref, ok := secret.ParseReference(p.ref)
	if !ok || ref.Scheme != secret.SchemeStore || p.store == nil || p.ref != "store:"+ref.Locator || !strings.HasPrefix(ref.Locator, "mcp/") {
		return "", auth.ErrMCPGatewayUnavailable
	}
	raw, err := p.store.Resolve(ctx, p.tenant, ref.Locator)
	if err != nil {
		return "", auth.ErrSecretNotFound
	}
	value := string(raw)
	if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return "", auth.ErrMCPGatewayInvalid
	}
	// The reference names an Authorization header value, as in file upstream_auth.
	return value, nil
}

func (m *mcpManagement) TestServer(ctx context.Context, p auth.Principal, tenant model.TenantID, version int64, id string) (auth.MCPGatewaySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != "store" {
		return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayFileOwned
	}
	snapshot, err := m.store.Get(ctx, tenant)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Version != version {
		return auth.MCPGatewaySnapshot{}, store.ErrConflict
	}
	var row *auth.MCPGatewayServer
	for i := range snapshot.Servers {
		if snapshot.Servers[i].ID == id {
			row = &snapshot.Servers[i]
			break
		}
	}
	if row == nil {
		return auth.MCPGatewaySnapshot{}, store.ErrNotFound
	}
	probe := auth.MCPGatewayProbe{State: "unreachable", TestedAt: time.Now().UTC().Format(time.RFC3339), Tools: []auth.MCPGatewayTool{}}
	if row.Transport == "stdio" {
		tools, probeErr := m.probeLocalServer(ctx, tenant, *row)
		if probeErr == nil {
			probe.State = "ok"
			probe.Tools = managedMCPProbeTools(tools)
		} else if errors.Is(probeErr, mcpc.ErrUpstreamCredentialDisclosure) {
			probe.State = "invalid_response"
		} else if errors.Is(probeErr, auth.ErrSecretNotFound) {
			probe.State = "credential_unavailable"
		} else {
			probe.Reason, probe.Detail = stdioProbeFailure(probeErr)
			m.warnStdioFailure(id, probeErr)
		}
	} else {
		client, err := newManagedMCPClient(row.MCPGatewayServerInput, 10*time.Second)
		if err != nil {
			probe.State = "egress_denied"
		} else {
			credential := tenantMCPCredentialProvider{store: m.secrets, tenant: tenant, ref: row.CredentialRef, target: row.URL}
			header, cerr := credential.Credential(ctx, row.URL)
			if cerr == nil {
				_, cerr = mcpCredentialPatterns(header)
				if cerr != nil && m.eng != nil && m.eng.log != nil {
					m.eng.log.Warn("mcp-gateway: upstream credential configuration refused", "reason", cerr)
				}
			}
			if cerr != nil {
				probe.State = "credential_unavailable"
			} else {
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				status := &mcpProbeStatus{inner: mcpCredentialTransport{inner: client.Transport}}
				client.Transport = status
				reader, createErr := mcpc.NewHTTPInspectionClient(row.URL, map[string]string{"Authorization": header}, client)
				if createErr != nil {
					return auth.MCPGatewaySnapshot{}, auth.ErrMCPGatewayUnavailable
				}
				defer reader.Close()
				init, initErr := reader.Initialize(ctx)
				err = initErr
				if err == nil && init.ProtocolVersion != "2025-11-25" {
					err = errManagedMCPInvalidResponse
				}
				var tools []mcpc.Tool
				if err == nil {
					tools, err = reader.ListTools(ctx)
				}
				switch {
				case err == nil:
					probe.State = "ok"
					probe.Tools = managedMCPProbeTools(tools)
				case errors.Is(err, mcpc.ErrUpstreamCredentialDisclosure):
					probe.State = "invalid_response"
					if m.eng != nil && m.eng.log != nil {
						m.eng.log.Warn("mcp-gateway: upstream credential response refused", "reason", mcpc.ErrUpstreamCredentialDisclosure)
					}
				case errors.Is(err, errManagedMCPRefused):
					probe.State = "refused"
				case errors.Is(err, errManagedMCPInvalidResponse), errors.Is(err, mcpc.ErrInspectionCatalogInvalid):
					probe.State = "invalid_response"
				case errors.Is(err, errManagedMCPRedirect):
					probe.State = "redirect_refused"
				case errors.Is(err, errManagedMCPEgress):
					probe.State = "egress_denied"
				default:
					probe.State = "unreachable"
					probe.Reason, probe.HTTPStatus = mcpProbeReason(err, status.code)
				}
			}
		}
	}
	_, err = m.store.SaveProbe(ctx, p, tenant, version, id, probe)
	if errors.Is(err, auth.ErrMCPGatewayInvalid) {
		probe.State = "invalid_response"
		probe.Reason, probe.Detail, probe.HTTPStatus = "", "", 0
		probe.Tools = []auth.MCPGatewayTool{}
		_, err = m.store.SaveProbe(ctx, p, tenant, version, id, probe)
	}
	if err != nil {
		return auth.MCPGatewaySnapshot{}, err
	}
	m.invalidate(tenant)
	return m.Get(ctx, tenant)
}

// mcpProbeStatus remembers the HTTP status of a connection test's last response, so a
// failed test can say "the server answered 404" instead of "unreachable".
type mcpProbeStatus struct {
	inner http.RoundTripper
	code  int
}

func (t *mcpProbeStatus) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if resp != nil {
		t.code = resp.StatusCode
	}
	return resp, err
}

// mcpProbeReason names why an otherwise "unreachable" test failed: the name did not
// resolve, TLS failed, the server answered an HTTP error, nothing accepted the
// connection, or nothing answered in time. "" when the error has no finer class.
func mcpProbeReason(err error, status int) (string, int) {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var authorityErr x509.UnknownAuthorityError
	var invalidErr x509.CertificateInvalidError
	var recordErr tls.RecordHeaderError
	var netErr net.Error
	switch {
	case status >= 400 && status <= 599:
		return "http_status", status
	case errors.Is(err, errManagedMCPResolve), errors.As(err, &dnsErr):
		return "dns", 0
	case errors.As(err, &certErr), errors.As(err, &hostErr), errors.As(err, &authorityErr),
		errors.As(err, &invalidErr), errors.As(err, &recordErr):
		return "tls", 0
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout", 0
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused", 0
	}
	return "", 0
}

func (m *mcpManagement) buildServer(tenant model.TenantID, row auth.MCPGatewayServer) (*mcpc.ResourceServer, error) {
	if m.eng == nil {
		return nil, auth.ErrMCPGatewayUnavailable
	}
	client, err := newManagedMCPClient(row.MCPGatewayServerInput, 60*time.Second)
	if err != nil {
		return nil, err
	}
	upstream := &mcpUpstreamForwarder{url: row.URL, client: client, credProv: tenantMCPCredentialProvider{store: m.secrets, tenant: tenant, ref: row.CredentialRef, target: row.URL}}
	enableManagedMCPForwarding(upstream)
	cfg := &mcpGatewayConfig{Resource: row.Trust.Resource, Issuer: row.Trust.Issuer, IssuerJWKS: row.Trust.JWKS, JWKSURL: row.Trust.JWKSURL,
		AuthorizationServers: []string{row.Trust.Issuer}, Tenant: tenant.String(), UpstreamURL: row.URL, UpstreamRevision: "2025-11-25", RevisionMode: "legacy",
		managedUpstream: upstream, managedUpstreamDescriptor: "managed-https:" + row.URL + "|server:" + row.ID + "|credential-ref:" + row.CredentialRef}
	for _, policy := range row.AllowedTools {
		cfg.Tools = append(cfg.Tools, mcpc.ToolPolicy{Name: policy.Name, RequiredScope: policy.RequiredScope, Destructive: policy.Destructive})
	}
	rs, _, err := buildMCPResourceServer(m.eng, cfg, m.eng.log)
	return rs, err
}

func (m *mcpManagement) ServeGatewayHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m.source != "store" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	tenant, err := model.ParseTenantID(chi.URLParam(r, "tenant"))
	id := chi.URLParam(r, "id")
	parsed, idErr := model.ParseID(id)
	if err != nil || idErr != nil || parsed.IsZero() || parsed.String() != id || tenant.String() != chi.URLParam(r, "tenant") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot, err := m.store.Get(r.Context(), tenant)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var row *auth.MCPGatewayServer
	for i := range snapshot.Servers {
		if snapshot.Servers[i].ID == id && snapshot.Servers[i].Enabled {
			row = &snapshot.Servers[i]
			break
		}
	}
	if row == nil || row.Transport == "stdio" || row.Trust.Resource == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Building a server per request would lose its operation/task inventory. Cache
	// publication has its own lock; API writes remain blocked by the read gate.
	rs, err := m.cachedServer(tenant, snapshot.Version, *row)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	rs.ServeHTTP(w, r)
}

func (m *mcpManagement) cachedServer(tenant model.TenantID, version int64, row auth.MCPGatewayServer) (*mcpc.ResourceServer, error) {
	// Called under mu.RLock: a separate cache mutex avoids a read-to-write upgrade.
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	key := tenant.String() + "/" + row.ID
	if cached, ok := m.cache[key]; ok && cached.version == version {
		return cached.server, nil
	}
	if _, ok := m.cache[key]; !ok && len(m.cache) >= 128 {
		return nil, auth.ErrMCPGatewayUnavailable
	}
	rs, err := m.buildServer(tenant, row)
	if err != nil {
		return nil, err
	}
	m.cache[key] = managedMCPServer{version: version, server: rs}
	return rs, nil
}

func (m *mcpManagement) ServeSessionHTTP(w http.ResponseWriter, r *http.Request) {
	if m.source != "store" || m.eng == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	scope := sessionOrchestrationWorkScope{st: m.eng.store, module: m.eng.sessionsMod}
	authenticator := sessionMCPAuthenticator(m.eng.authr)
	if m.sessionAuthenticator != nil {
		authenticator = m.sessionAuthenticator
	}
	h := &sessionMCPHandler{authr: authenticator, issuedSessionOnly: m.sessionAuthenticator != nil, work: m.eng.sessionsMod.CallSessionWork, managed: m.serveManagedSession, managedTools: m.aggregateSessionTools, managedCall: m.aggregateSessionCall, enabled: func(ctx context.Context, tenant model.TenantID) (bool, error) {
		snapshot, err := m.store.Get(ctx, tenant)
		return sessionMCPAvailable(snapshot), err
	},
		checkOrchestration: func(ctx context.Context, p auth.Principal, tenant model.TenantID) error {
			return scope.WithScope(ctx, p, tenant, false, func(store.Scope) error { return nil })
		}}
	h.ServeHTTP(w, r)
}

func managedMCPProbeTools(tools []mcpc.Tool) []auth.MCPGatewayTool {
	out := make([]auth.MCPGatewayTool, 0, len(tools))
	for _, tool := range tools {
		encoded, _ := json.Marshal(tool)
		hash := sha256.Sum256(encoded)
		// MCP destructiveHint applies only when readOnlyHint is false.
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint
		out = append(out, auth.MCPGatewayTool{Name: tool.Name, Fingerprint: hex.EncodeToString(hash[:]), ReadOnly: readOnly})
	}
	return out
}

// UseSessionCredentials binds PEP's single in-process session credential service.
// When bound, legacy work/communication and operator bearers have no fallback.
func (m *mcpManagement) UseSessionCredentials(service sessionMCPAuthenticator) {
	m.sessionAuthenticator = service
}
