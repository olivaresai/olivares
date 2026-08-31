// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// mcpgateway.go wires the agent-protocols GATEWAY in the composition root: the
// inline MCP Resource-Server PEP (connectors/mcp) and the A2A push-notification
// receiver (connectors/a2a), each mounted on a dedicated socket like the HITL receiver.
// The connectors own the protocol + the deny-closed seams (token validation, the
// tools/call gate, push JWT verification); this file binds those seams to the AGPL
// plane the connectors may not import: the ApprovalGate (for destructive tools),
// the ledger/log, and the upstream tool backend.
//
// It is loaded from OLIVARES_AGENT_GATEWAY_CONFIG (operator-provisioned, secret-bearing,
// out of the store). Absent/invalid ⇒ nothing mounted (the safe default; an un-wired
// node serves no inbound agent surface). Every governance seam stays deny-closed.

// loadAgentGatewayConfig reads the optional OLIVARES_AGENT_GATEWAY_CONFIG JSON. A
// missing path yields an empty config (nothing mounted); a supplied path must be readable
// and contain valid JSON or startup fails closed.
func loadAgentGatewayConfig(_ *slog.Logger) (agentGatewayConfig, error) {
	path := os.Getenv("OLIVARES_AGENT_GATEWAY_CONFIG")
	if path == "" {
		return agentGatewayConfig{}, nil
	}
	var cfg agentGatewayConfig
	if err := loadOperatorJSONConfig("OLIVARES_AGENT_GATEWAY_CONFIG", path, &cfg); err != nil {
		return agentGatewayConfig{}, err
	}
	return cfg, nil
}

// agentGatewayConfig is the operator provisioning for the inbound agent surface.
type agentGatewayConfig struct {
	Listen     string            `json:"listen"`
	MCP        *mcpGatewayConfig `json:"mcp"`
	A2APush    *a2aPushConfig    `json:"a2a_push"`
	A2AInbound *a2aInboundConfig `json:"a2a_inbound"`
	// MCPRegistry provisions the embedded PRIVATE MCP sub-registry: the
	// generic registry OpenAPI /v0.1 served per tenant under /mcp-registry/
	// (tenant paths /mcp-registry/t/{tenant}/v0.1/..., default tenant on the bare
	// /mcp-registry/v0.1/...). The entries are the operator-APPROVED set — the
	// Internal registry elevated to a served registry (the official preview
	// registry rejects private servers; GitHub's org/enterprise MCP registries
	// require exactly this bring-your-own /v0.1 surface). Serving approved
	// modules/catalog kindMCP entries from the store is the follow-up provider
	// seam — provisioning is config-declared today, like the toolset.
	MCPRegistry *mcpc.SubRegistryConfig `json:"mcp_registry"`
}

// mcpGatewayConfig provisions the inline MCP Resource-Server PEP. Token trust is
// ISSUER-KEYED: `issuers` is the multi-issuer form; the legacy single-issuer
// fields remain accepted and are folded in by the connector. `issuer` is REQUIRED
// when any legacy anchor field is set — an RS that cannot validate the iss claim of
// every token (RFC 9068 §4) refuses to mount instead of skipping the check.
type mcpGatewayConfig struct {
	Resource             string            `json:"resource"`
	AuthorizationServers []string          `json:"authorization_servers"`
	ScopesSupported      []string          `json:"scopes_supported"`
	Issuers              []mcpIssuerTrust  `json:"issuers"`
	Issuer               string            `json:"issuer"`
	IssuerJWKS           json.RawMessage   `json:"issuer_jwks"`
	JWKSURL              string            `json:"jwks_url"`
	IntrospectionURL     string            `json:"introspection_url"`
	IntrospectionAuth    string            `json:"introspection_auth"` // secret: the RS's OWN introspection credential
	Tenant               string            `json:"tenant"`
	Tools                []mcpc.ToolPolicy `json:"tools"`
	// RoleClaim is the token claim the per-role tool allowlist (E1) reads roles from
	// (default "roles"); per-tool AllowedRoles ride inside each Tools entry.
	RoleClaim      string   `json:"role_claim"`
	AllowedOrigins []string `json:"allowed_origins"`
	// RequireDPoP requires every authenticated request to present a DPoP-bound
	// access token with a matching proof.
	RequireDPoP bool `json:"require_dpop"`
	// RequireDPoPNonce additionally requires the RS nonce in each DPoP proof; the
	// client retries after the use_dpop_nonce challenge.
	RequireDPoPNonce bool `json:"require_dpop_nonce"`
	// AcceptMTLSBoundTokens verifies RFC 8705 x5t#S256-bound access tokens against
	// the TLS peer certificate. It only works when this process terminates TLS with
	// client-cert negotiation; behind a TLS-terminating proxy, the peer certificate
	// is not visible and bound tokens fail closed.
	AcceptMTLSBoundTokens bool   `json:"accept_mtls_bound_tokens"`
	UpstreamURL           string `json:"upstream_url"`
	UpstreamAuth          string `json:"upstream_auth"` // secret: a SEPARATE upstream credential (NEVER the inbound token)
	// UpstreamRevision RECORDS the MCP protocol revision the upstream speaks, as
	// CONFIGURATION (round-5 R5-05). Nothing here negotiates or discovers it.
	//
	// ROUND-7 R7-07: an empty value ASSUMES the connector baseline (2026-07-28).
	// The round-6 wording "it is not a guess" was false — an unset field is exactly
	// an assumption, and it is the OPERATOR's to get right; the connector-side
	// comments were corrected for this and this one was missed. What IS true is the
	// failure direction: the operator reconciliation read synthesizes a
	// Tasks-extension request for the configured revision, and an upstream declared
	// as a revision whose Tasks extension this connector does not implement has that
	// read REFUSED rather than answered with a fabricated legacy shape — deny-closed,
	// so a mismatch retains the record instead of draining it on an unreadable
	// answer. Correcting a wrong value normally rebuilds the RS, which loses the
	// process-local task inventory (see connectors/mcp/taskreconcile.go).
	UpstreamRevision string `json:"upstream_revision"`
	// NextRevisionHeaders controls the MCP 2026-07-28 L7 header gate
	// (Mcp-Method/Mcp-Name deny-closed before body parse). Default OFF at the
	// operator-config level for backward-compat; set to true to enable (maps to
	// DisableNextRevisionHeaders:false on the RS). Omit for new deployments that
	// speak 2026-07-28 — the RS layer defaults ON.
	NextRevisionHeaders bool `json:"next_revision_headers"`
	// Retrieval enables the in-process governed retrieval upstream: the
	// knowledge module's RAG pipeline exposed as MCP tools (search_kb,
	// fetch_document, list_kbs). When enabled the retrieval tools are merged into
	// the toolset and an in-process upstream replaces (or supplements) the external
	// forwarder. The retrieval scope defaults to "knowledge:retrieval:read".
	Retrieval *mcpRetrievalConfig `json:"retrieval"`
	// DurableTasks binds the optional MCP Tasks extension to the K5 WorkKernel and
	// ProtocolBinding authorities. The route is entirely operator-owned: request
	// metadata can never select a workspace, binding generation, or local owner.
	// Omit this block to keep ordinary synchronous MCP forwarding available while
	// the connector removes Tasks from its advertised capabilities.
	DurableTasks *mcpDurableTasksConfig `json:"durable_tasks"`
	// DurableSubscriptions binds subscriptions/listen to the sessions-backed
	// cursor/event ledger. Omit it to leave the streaming method unavailable
	// (503) while preserving ordinary synchronous MCP forwarding.
	DurableSubscriptions *mcpDurableSubscriptionsConfig `json:"durable_subscriptions"`
}

// mcpRetrievalConfig enables the in-process governed retrieval MCP surface.
type mcpRetrievalConfig struct {
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope"` // OAuth scope for retrieval tools (default "knowledge:retrieval:read")
}

// mcpDurableTasksConfig is the JSON-facing local route for MCP durable tasks.
// Tenant comes from the enclosing MCP Resource Server configuration so the
// authentication, evidence, work, and protocol-binding namespaces stay identical.
type mcpDurableTasksConfig struct {
	WorkspaceID                  string   `json:"workspace_id"`
	BindingSpecID                string   `json:"binding_spec_id"`
	BindingSpecGeneration        int64    `json:"binding_spec_generation"`
	OwnerKind                    string   `json:"owner_kind"`
	OwnerRef                     string   `json:"owner_ref"`
	ProtocolRuleRefs             []string `json:"protocol_rule_refs"`
	ProtocolPermissionProfileRef string   `json:"protocol_permission_profile_ref"`
	InterruptChannelID           string   `json:"interrupt_channel_id"`
	InterruptSenderUserID        string   `json:"interrupt_sender_user_id"`
	InterruptRecipientUserID     string   `json:"interrupt_recipient_user_id"`
}

// mcpDurableSubscriptionsConfig fixes the local workspace of every relayed
// stream. Tenant comes from the enclosing Resource Server and peer authority
// comes from upstream_url; neither can be supplied by a listen request.
type mcpDurableSubscriptionsConfig struct {
	WorkspaceID string `json:"workspace_id"`
}

// mcpIssuerTrust is one trusted token issuer for the MCP PEP: the EXACT iss value
// (the lookup key — compared byte-for-byte, no normalization) plus that issuer's own
// trust anchors and the RS's own credential at that issuer's introspection endpoint.
type mcpIssuerTrust struct {
	Issuer            string          `json:"issuer"`
	IssuerJWKS        json.RawMessage `json:"issuer_jwks"`
	JWKSURL           string          `json:"jwks_url"`
	IntrospectionURL  string          `json:"introspection_url"`
	IntrospectionAuth string          `json:"introspection_auth"` // secret: per-issuer RS credential
}

// a2aPushConfig provisions the inbound A2A push-notification receiver.
type a2aPushConfig struct {
	Audience       string               `json:"audience"`
	IssuerJWKS     json.RawMessage      `json:"issuer_jwks"`
	JWKSURL        string               `json:"jwks_url"`
	AllowedIssuers []string             `json:"allowed_issuers"`
	Routes         []a2aPushRouteConfig `json:"routes"`
}

type a2aPushRouteConfig struct {
	PeerAuthority            string `json:"peer_authority"`
	Tenant                   string `json:"tenant"`
	WorkspaceID              string `json:"workspace_id"`
	InterruptChannelID       string `json:"interrupt_channel_id"`
	InterruptSenderUserID    string `json:"interrupt_sender_user_id"`
	InterruptRecipientUserID string `json:"interrupt_recipient_user_id"`
}

// a2aInboundConfig provisions the authenticated A2A SendMessage application
// endpoint. Routes are operator-owned local authority: a peer-supplied tenant,
// owner or WorkItem reference is never accepted as a routing decision.
type a2aInboundConfig struct {
	Audience                 string                  `json:"audience"`
	IssuerJWKS               json.RawMessage         `json:"issuer_jwks"`
	JWKSURL                  string                  `json:"jwks_url"`
	AllowedIssuers           []string                `json:"allowed_issuers"`
	InterfaceTenant          string                  `json:"interface_tenant"`
	RequireClientAttestation bool                    `json:"require_client_attestation"`
	AttesterJWKS             json.RawMessage         `json:"attester_jwks"`
	Routes                   []a2aInboundRouteConfig `json:"routes"`
}

type a2aInboundRouteConfig struct {
	PeerAuthority                string   `json:"peer_authority"`
	Tenant                       string   `json:"tenant"`
	WorkspaceID                  string   `json:"workspace_id"`
	BindingSpecID                string   `json:"binding_spec_id"`
	BindingSpecGeneration        int64    `json:"binding_spec_generation"`
	ChannelID                    string   `json:"channel_id"`
	SenderUserID                 string   `json:"sender_user_id"`
	RecipientUserID              string   `json:"recipient_user_id"`
	OwnerKind                    string   `json:"owner_kind"`
	OwnerRef                     string   `json:"owner_ref"`
	WorkKind                     string   `json:"work_kind"`
	Priority                     string   `json:"priority"`
	ProtocolRuleRefs             []string `json:"protocol_rule_refs"`
	ProtocolPermissionProfileRef string   `json:"protocol_permission_profile_ref"`
}

// defaultAgentGatewayListen is the loopback-default bind (secure default): an
// operator that must receive remote MCP/A2A traffic fronts it with their ingress.
const defaultAgentGatewayListen = "127.0.0.1:8446"

// envMCPTaskKillSwitchSweep controls the MCP durable-task cancellation sweep cadence.
// Empty uses the session kill-switch sweep default; "0" disables active cancellation.
const envMCPTaskKillSwitchSweep = "OLIVARES_MCP_TASK_KILLSWITCH_SWEEP"

// buildAgentGatewayServer constructs the inbound agent-protocols server (MCP RS +
// A2A push receiver) on its own socket, or nil when neither is configured. The MCP
// tools/call HITL gate bridges to the SAME approval bridge the rest of Phase K
// uses; the upstream forwarder uses a SEPARATE credential (no token passthrough).
func buildAgentGatewayServer(eng *engine, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadAgentGatewayConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load agent gateway operator config: %w", err)
	}
	mux := http.NewServeMux()
	mounted := false
	var mcpRS *mcpc.ResourceServer
	var mcpTenant model.TenantID

	if cfg.MCP != nil && strings.TrimSpace(cfg.MCP.Resource) != "" {
		rs, rsTenant, err := buildMCPResourceServer(eng, cfg.MCP, log)
		if err != nil {
			// Error, not Warn: this is a PROVISIONED surface that refused to mount
			// (deny-closed) — e.g. the hardening rejecting a legacy config whose
			// trust anchors carry no `issuer`. The process boots, but the operator's
			// deliberate provisioning is not in effect; that must not read like noise.
			log.Error("agent-gateway: MCP Resource Server PROVISIONED BUT NOT MOUNTED (deny-closed); the MCP surface is down until the config is fixed — since every trust anchor requires its `issuer` (RFC 9068 §4); use the `issuers` array for multi-issuer trust", "err", err)
		} else {
			// rs.ServeHTTP self-routes: GET /.well-known/oauth-protected-resource →
			// metadata; any other path → the gated JSON-RPC endpoint. Mount it as the
			// catch-all so both the well-known doc and the /mcp endpoint reach it.
			mux.Handle("/", rs)
			mounted = true
			mcpRS = rs
			mcpTenant = rsTenant
			log.Info("agent-gateway: inline MCP Resource Server PEP mounted", "resource", cfg.MCP.Resource, "tools", len(cfg.MCP.Tools))
		}
	}
	if cfg.A2APush != nil && strings.TrimSpace(cfg.A2APush.Audience) != "" {
		pr, err := buildA2APushReceiver(eng, cfg.A2APush, log)
		if err != nil {
			log.Warn("agent-gateway: A2A push receiver not mounted", "err", err)
		} else {
			mux.Handle("/a2a/push", pr)
			mounted = true
			log.Info("agent-gateway: A2A push-notification receiver mounted", "audience", cfg.A2APush.Audience)
		}
	}
	if cfg.A2AInbound != nil {
		inbound, err := buildA2AInboundServer(eng, cfg.A2AInbound)
		if err != nil {
			log.Error("agent-gateway: A2A inbound server PROVISIONED BUT NOT MOUNTED (deny-closed); fix a2a_inbound routing/trust configuration", "err", err)
		} else {
			mux.Handle("/a2a", inbound)
			mounted = true
			log.Info("agent-gateway: authenticated A2A SendMessage endpoint mounted", "routes", len(cfg.A2AInbound.Routes))
		}
	}
	// Any provisioned (non-nil) block reaches the constructor: an empty/misspelled
	// tenants map must hit the deny-closed PROVISIONED-BUT-NOT-MOUNTED error below,
	// never a silent skip.
	if cfg.MCPRegistry != nil {
		reg, err := mcpc.NewSubRegistry(*cfg.MCPRegistry)
		if err != nil {
			// Error, not Warn (the convention): a PROVISIONED surface refused to
			// mount deny-closed — an invalid private registry must not serve a partial
			// or mis-namespaced view.
			log.Error("agent-gateway: MCP sub-registry PROVISIONED BUT NOT MOUNTED (deny-closed); fix the mcp_registry provisioning", "err", err)
		} else {
			// The /mcp-registry/ prefix is more specific than the RS catch-all "/",
			// so both surfaces coexist on the socket; the registry self-routes its
			// /v0.1 and /t/{tenant}/v0.1 paths after the prefix strip.
			mux.Handle("/mcp-registry/", http.StripPrefix("/mcp-registry", reg))
			mounted = true
			tenants := 0
			entries := 0
			for _, tc := range cfg.MCPRegistry.Tenants {
				tenants++
				entries += len(tc.Servers)
			}
			log.Info("agent-gateway: embedded private MCP sub-registry mounted (generic registry OpenAPI /v0.1, read-only)", "tenants", tenants, "servers", entries)
		}
	}
	if !mounted {
		return nil, nil
	}
	addr := strings.TrimSpace(cfg.Listen)
	if addr == "" {
		addr = defaultAgentGatewayListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("agent-gateway: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token/JWT verification, not network isolation", "addr", addr)
	}
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = mux
	startMCPTaskKillSwitchSweep(srv, mcpRS, eng.killSwitch, mcpTenant, log)
	return srv, nil
}

// buildMCPResourceServer builds the inline MCP PEP from config, binding the tools/call
// HITL gate to the approval bridge and the upstream to a no-passthrough forwarder.
func buildMCPResourceServer(eng *engine, cfg *mcpGatewayConfig, log *slog.Logger) (*mcpc.ResourceServer, model.TenantID, error) {
	return buildMCPResourceServerWithDurableTaskStore(eng, cfg, log, nil)
}

// buildMCPResourceServerWithDurableTaskStore keeps the production builder and
// its integration fixtures on the same composition path. Production callers
// pass nil and derive the store exclusively from durable_tasks configuration;
// focused tests may supply an explicit implementation.
func buildMCPResourceServerWithDurableTaskStore(
	eng *engine,
	cfg *mcpGatewayConfig,
	log *slog.Logger,
	durableTaskStore mcpc.DurableTaskStore,
) (*mcpc.ResourceServer, model.TenantID, error) {
	// merge retrieval tool policies into the operator-declared toolset when
	// the in-process governed retrieval surface is enabled.
	tools := append([]mcpc.ToolPolicy(nil), cfg.Tools...)
	if cfg.Retrieval != nil && cfg.Retrieval.Enabled {
		tools = append(tools, RetrievalToolPolicies(cfg.Retrieval.Scope)...)
	}
	ts, err := mcpc.NewToolset(tools)
	if err != nil {
		return nil, "", err
	}
	// The RS is single-tenant by config; the kill switch and the approval gate
	// both key on it. An invalid tenant refuses to mount (deny-closed).
	// deny-closed through the shared policy. This used to check the parse error
	// ONLY, so the reserved system tenant (which ParseTenantID returns with a nil error)
	// and the all-zero "unset" UUID both got through — the first became the enforcement
	// anchor the kill switch and approval gate key on, the second silently left the
	// gate unwired. An ABSENT tenant is still legitimate and leaves rsTenant zero.
	rsTenant, _, terr := parseBusinessTenant("mcp gateway config: tenant", cfg.Tenant)
	if terr != nil {
		return nil, "", terr
	}
	if durableTaskStore != nil && cfg.DurableTasks != nil {
		return nil, "", fmt.Errorf("mcp gateway: durable task store supplied twice")
	}
	durableTasks := durableTaskStore
	if durableTasks == nil {
		durableTasks, err = buildMCPDurableTaskStore(eng, rsTenant, cfg.DurableTasks)
		if err != nil {
			return nil, "", err
		}
	}
	var gate mcpc.ApprovalGate // nil ⇒ the connector's deny-closed default
	if eng.approvalBridge != nil && !rsTenant.IsZero() {
		gate = mcpToolGate{bridge: eng.approvalBridge, tenant: rsTenant, guard: eng.killSwitch, rec: eng.stopDeny}
	}
	var upstream mcpc.Upstream // nil ⇒ deny-closed (admitted/gated but not actuated)
	var subscriptionUpstream mcpc.SubscriptionUpstream
	// upstreamDescriptor is the STABLE upstream/credential-profile identity bound
	// into every tools/call EffectDigest (round-2): the identity fields the
	// config carries TODAY — the upstream base URL and the credential provider's
	// Go type (build-dependent: static in community, token-exchange minter in
	// enterprise) — NEVER the secret itself. A re-pointed backend therefore
	// changes the effect identity: a keyed retry rebinds instead of replaying.
	upstreamDescriptor := ""

	// wire the in-process governed retrieval upstream when enabled.
	if cfg.Retrieval != nil && cfg.Retrieval.Enabled && eng.knowledgeMod != nil && !rsTenant.IsZero() {
		ru, rerr := newRetrievalUpstream(retrievalUpstreamConfig{
			Module: eng.knowledgeMod,
			Store:  eng.store,
			Tenant: rsTenant,
			Role:   "viewer",
			Log:    log,
		})
		if rerr != nil {
			return nil, "", fmt.Errorf("mcp gateway: retrieval upstream: %w", rerr)
		}
		upstream = ru
		upstreamDescriptor = "in-process:governed-retrieval"
		log.Info("mcp gateway: in-process governed retrieval upstream wired", "tenant", rsTenant.String(), "tools", "search_kb,fetch_document,list_kbs")
	} else if cfg.Retrieval != nil && cfg.Retrieval.Enabled {
		log.Warn("mcp gateway: retrieval enabled but knowledge module or tenant not available; retrieval tools will be in the toolset but the upstream is deny-closed")
	}

	if upstream == nil && strings.TrimSpace(cfg.UpstreamURL) != "" {
		credProv := newUpstreamCredentialProvider(cfg.UpstreamAuth)
		forwarder := &mcpUpstreamForwarder{
			url:      strings.TrimSpace(cfg.UpstreamURL),
			credProv: credProv,
			// cli-transport-exempt: ENGINE→upstream MCP server, not a CLI path. The
			// gateway forwards on behalf of a governed session; its credentials come
			// from the upstream credential provider, never from a client context.
			client: &http.Client{Timeout: 60 * time.Second},
		}
		upstream = forwarder
		subscriptionUpstream = forwarder
		upstreamDescriptor = fmt.Sprintf("https-forward:%s|cred-provider:%T",
			strings.TrimSpace(cfg.UpstreamURL), credProv)
	}
	// the estate kill switch freezes EVERY forwarded method (tools/call
	// included) while a stop is active — wrapped here so the connector (which
	// may not import /core) never holds the state. F-06: when a kill switch is
	// configured but there is NO tenant to key it on, an actuating surface
	// (upstream != nil) would forward around the stop. Refuse to mount rather
	// than warn — a governed control plane must never expose a forwarding
	// surface its emergency stop cannot reach (deny-closed).
	if upstream != nil && eng.killSwitch != nil {
		if rsTenant.IsZero() {
			return nil, "", fmt.Errorf("mcp gateway: kill switch configured but no tenant to key it on; refusing to mount an MCP forwarding surface the estate stop cannot govern (set tenant)")
		}
		wrapped := killSwitchUpstream{guard: eng.killSwitch, tenant: rsTenant, rec: eng.stopDeny, inner: upstream}
		upstream = wrapped
		if subscriptionUpstream != nil {
			subscriptionUpstream = killSwitchSubscriptionUpstream{
				guard: eng.killSwitch, tenant: rsTenant, rec: eng.stopDeny,
				inner: subscriptionUpstream,
			}
		}
	}
	durableAdapter, durableAdapterOK := durableTasks.(*mcpDurableTaskStore)
	if durableAdapterOK && upstreamDescriptor != "" {
		if err := durableAdapter.bindUpstreamDescriptor(upstreamDescriptor); err != nil {
			return nil, "", fmt.Errorf("mcp gateway: bind durable task upstream: %w", err)
		}
	}
	subscriptionLedger, err := buildMCPSubscriptionLedger(
		eng, rsTenant, cfg.DurableSubscriptions, strings.TrimSpace(cfg.UpstreamURL),
	)
	if err != nil {
		return nil, "", err
	}
	issuers := make([]mcpc.IssuerTrust, 0, len(cfg.Issuers))
	for _, it := range cfg.Issuers {
		issuers = append(issuers, mcpc.IssuerTrust{
			Issuer:            it.Issuer,
			JWKS:              []byte(it.IssuerJWKS),
			JWKSURL:           it.JWKSURL,
			IntrospectionURL:  it.IntrospectionURL,
			IntrospectionAuth: it.IntrospectionAuth,
		})
	}
	ri := newMCPRenderInspector(os.Getenv, log)
	em := newMCPElicitationMediator(os.Getenv, log)
	mcpContentGateLog(log, ri, em)

	var taskGate mcpc.TaskGate
	if eng.finops != nil && !rsTenant.IsZero() {
		taskGate = mcpTaskGate{fin: eng.finops, tenant: rsTenant, log: log}
	}

	rs, err := mcpc.NewResourceServer(mcpc.ResourceServerConfig{
		Resource:             cfg.Resource,
		AuthorizationServers: cfg.AuthorizationServers,
		ScopesSupported:      cfg.ScopesSupported,
		Issuers:              issuers,
		Issuer:               cfg.Issuer,
		IssuerJWKS:           []byte(cfg.IssuerJWKS),
		JWKSURL:              cfg.JWKSURL,
		IntrospectionURL:     cfg.IntrospectionURL,
		IntrospectionAuth:    cfg.IntrospectionAuth,
		Toolset:              ts,
		AllowedOrigins:       cfg.AllowedOrigins,
		// Review round-1 P0: the CANONICAL tenant string (rsTenant.String()), the
		// SAME form the evidence journal keys on — NEVER the raw cfg.Tenant. A
		// non-canonical config tenant (uppercase/whitespace UUID) would otherwise
		// derive a DIFFERENT OperationID/EffectDigest than a later normalized restart,
		// splitting the idempotency namespace into a double effect. rsTenant is "" for
		// an empty config tenant (unchanged behavior).
		Tenant:                rsTenant.String(),
		UpstreamDescriptor:    upstreamDescriptor,
		UpstreamRevision:      cfg.UpstreamRevision,
		RoleClaim:             cfg.RoleClaim,
		RequireDPoP:           cfg.RequireDPoP,
		RequireDPoPNonce:      cfg.RequireDPoPNonce,
		AcceptMTLSBoundTokens: cfg.AcceptMTLSBoundTokens,
		Gate:                  gate,
		TaskGate:              taskGate,
		DurableTaskStore:      durableTasks,
		Upstream:              upstream,
		SubscriptionUpstream:  subscriptionUpstream,
		SubscriptionLedger:    subscriptionLedger,
		Auditor:               mcpGateAuditor{log: log, store: eng.store, tenant: rsTenant},
		PinVerifier:           eng.pinVerifier,
		RenderInspector:       ri,
		ElicitationMediator:   em,

		DisableNextRevisionHeaders: !cfg.NextRevisionHeaders,
	})
	if err != nil {
		return nil, "", err
	}
	if durableAdapterOK && eng != nil && eng.sessionsMod != nil {
		eng.sessionsMod.AddProtocolBindingSpecValidator(sessions.BindingProtocolMCP, durableAdapter)
	}
	if durableAdapterOK && upstream != nil && eng != nil && eng.protocolBindingReconciler != nil {
		reconciler, err := newMCPProtocolBindingReconciler(durableAdapter, upstream)
		if err != nil {
			return nil, "", err
		}
		if err := eng.protocolBindingReconciler.Use(sessions.BindingProtocolMCP, reconciler); err != nil {
			return nil, "", fmt.Errorf("mcp gateway: wire protocol binding reconcile adapter: %w", err)
		}
	}
	return rs, rsTenant, nil
}

// buildMCPSubscriptionLedger resolves the optional operator-owned workspace
// into the narrow sessions port. An omitted block leaves the ledger nil so the
// connector returns 503 for subscriptions/listen without affecting other MCP
// methods.
func buildMCPSubscriptionLedger(
	eng *engine,
	tenant model.TenantID,
	cfg *mcpDurableSubscriptionsConfig,
	peerAuthority string,
) (mcpc.SubscriptionLedger, error) {
	if cfg == nil {
		return nil, nil
	}
	if eng == nil || eng.sessionsMod == nil {
		return nil, fmt.Errorf("mcp durable subscriptions: configured but sessions kernel is unavailable")
	}
	workspaceID, err := model.ParseID(strings.TrimSpace(cfg.WorkspaceID))
	if err != nil || workspaceID.IsZero() {
		return nil, fmt.Errorf("mcp durable subscriptions: invalid workspace_id")
	}
	ledger, err := newMCPSubscriptionLedger(tenant, workspaceID, peerAuthority, eng.sessionsMod)
	if err != nil {
		return nil, err
	}
	return ledger, nil
}

// buildMCPDurableTaskStore translates the JSON route into the strongly typed
// composition adapter. A nil block is an explicit OFF state; a present but
// incomplete block is refused instead of silently degrading to process-local Tasks.
func buildMCPDurableTaskStore(
	eng *engine,
	tenant model.TenantID,
	cfg *mcpDurableTasksConfig,
) (mcpc.DurableTaskStore, error) {
	if cfg == nil {
		return nil, nil
	}
	if eng == nil || eng.sessionsMod == nil {
		return nil, fmt.Errorf("mcp durable tasks: configured but sessions kernel is unavailable")
	}
	workspaceID, err := model.ParseID(strings.TrimSpace(cfg.WorkspaceID))
	if err != nil || workspaceID.IsZero() {
		return nil, fmt.Errorf("mcp durable tasks: invalid workspace_id")
	}
	bindingSpecID, err := model.ParseID(strings.TrimSpace(cfg.BindingSpecID))
	if err != nil || bindingSpecID.IsZero() {
		return nil, fmt.Errorf("mcp durable tasks: invalid binding_spec_id")
	}
	interruptChannelID, err := model.ParseID(strings.TrimSpace(cfg.InterruptChannelID))
	if err != nil || interruptChannelID.IsZero() {
		return nil, fmt.Errorf("mcp durable tasks: invalid interrupt_channel_id")
	}
	interruptSenderUserID, err := model.ParseID(strings.TrimSpace(cfg.InterruptSenderUserID))
	if err != nil || interruptSenderUserID.IsZero() {
		return nil, fmt.Errorf("mcp durable tasks: invalid interrupt_sender_user_id")
	}
	interruptRecipientUserID, err := model.ParseID(strings.TrimSpace(cfg.InterruptRecipientUserID))
	if err != nil || interruptRecipientUserID.IsZero() || interruptRecipientUserID == interruptSenderUserID {
		return nil, fmt.Errorf("mcp durable tasks: invalid interrupt_recipient_user_id")
	}
	policy, err := resolveProtocolRuntimePolicy(
		cfg.ProtocolRuleRefs, cfg.ProtocolPermissionProfileRef, mcpTaskRuntimePolicy,
	)
	if err != nil {
		return nil, fmt.Errorf("mcp durable tasks: invalid protocol policy: %w", err)
	}
	store, err := newMCPDurableTaskStore(tenant, eng.sessionsMod, mcpDurableTaskStoreConfig{
		WorkspaceID: workspaceID, BindingSpecID: bindingSpecID,
		Generation: cfg.BindingSpecGeneration,
		OwnerKind:  strings.TrimSpace(cfg.OwnerKind), OwnerRef: strings.TrimSpace(cfg.OwnerRef),
		InterruptRoute: sessions.ProtocolInterruptRoute{
			ChannelID: interruptChannelID, SenderUserID: interruptSenderUserID,
			RecipientUserID: interruptRecipientUserID,
		},
		Policy: policy,
	})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// buildA2APushReceiver builds the inbound A2A push receiver from config.
func buildA2APushReceiver(eng *engine, cfg *a2aPushConfig, log *slog.Logger) (*a2a.PushReceiver, error) {
	var durableUpdate func(context.Context, a2a.TaskUpdate) error
	var durableReply func(context.Context, a2a.ReplyEvent) error
	if len(cfg.Routes) > 0 {
		if eng == nil || eng.sessionsMod == nil {
			return nil, fmt.Errorf("a2a push: durable routes require the sessions kernel")
		}
		settler, err := newA2APushSettlement(eng.sessionsMod, cfg.Routes, cfg.AllowedIssuers)
		if err != nil {
			return nil, err
		}
		durableUpdate = settler.Record
		durableReply = settler.RecordReply
	}
	return a2a.NewPushReceiver(a2a.PushReceiverConfig{
		Audience:        cfg.Audience,
		IssuerJWKS:      []byte(cfg.IssuerJWKS),
		JWKSURL:         cfg.JWKSURL,
		AllowedIssuers:  cfg.AllowedIssuers,
		OnUpdateDurable: durableUpdate,
		OnReplyDurable:  durableReply,
		OnUpdate: func(_ context.Context, u a2a.TaskUpdate) {
			// Minimal-data operational record of a VERIFIED push (docs/SECURITY-HARDENING.md: the task
			// reference + state only). Turning it into an observe-side edge is the
			// producer seam; the receiver's job is verify + deliver.
			log.Info("a2a-push: verified task update", "task", u.TaskID, "state", string(u.State), "sender", u.Sender, "interrupt", u.Interrupt, "terminal", u.Terminal)
		},
		OnReply: func(_ context.Context, reply a2a.ReplyEvent) {
			// Reply bodies never enter operational logs. The connector has already
			// reduced non-text values to references, but even bounded text remains K3 data.
			log.Info("a2a-push: verified reply", "kind", string(reply.Kind),
				"message", reply.MessageID, "task", reply.TaskID,
				"artifact", reply.ArtifactID, "sender", reply.Sender)
		},
	})
}

func startMCPTaskKillSwitchSweep(srv *http.Server, rs *mcpc.ResourceServer, guard killSwitchGuard, tenant model.TenantID, log *slog.Logger) {
	if srv == nil || rs == nil || guard == nil || tenant.IsZero() {
		return
	}
	interval := loadMCPTaskKillSwitchSweepInterval(os.Getenv, log)
	if interval == 0 {
		if log != nil {
			log.Info("mcp gateway: task kill-switch sweep disabled", "env", envMCPTaskKillSwitchSweep)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.RegisterOnShutdown(cancel)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweepMCPTasksForKillSwitch(ctx, rs, guard, tenant, log)
			case <-ctx.Done():
				return
			}
		}
	}()
	if log != nil {
		log.Info("mcp gateway: task kill-switch sweep wired", "tenant", tenant.String(), "interval", interval.String())
	}
}

func loadMCPTaskKillSwitchSweepInterval(getenv func(string) string, log *slog.Logger) time.Duration {
	raw := strings.TrimSpace(getenv(envMCPTaskKillSwitchSweep))
	if raw == "" {
		return defaultStopSweepInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		if log != nil {
			log.Warn("mcp gateway: invalid task kill-switch sweep interval; using the default",
				"env", envMCPTaskKillSwitchSweep, "value", raw, "default", defaultStopSweepInterval.String())
		}
		return defaultStopSweepInterval
	}
	return d
}

func sweepMCPTasksForKillSwitch(ctx context.Context, rs *mcpc.ResourceServer, guard killSwitchGuard, tenant model.TenantID, log *slog.Logger) {
	st, err := guard.KillSwitchState(ctx, tenant)
	if err != nil {
		if log != nil {
			log.Error("mcp gateway: task kill-switch sweep could not read stop state", "tenant", tenant.String(), "err", err)
		}
		return
	}
	if !st.Any() {
		return
	}
	tenantKey := tenant.String()
	if st.EstateStopped {
		reason := "kill-switch estate stop " + st.EstateStopID.String()
		n, cerr := rs.CancelActiveTasks(ctx, func(rec mcpc.TaskRecord) bool {
			return rec.Tenant == tenantKey
		}, reason)
		logMCPSweepResult(log, tenant, "estate", "", n, cerr)
		return
	}
	for subject, stopID := range st.AgentRefs {
		subject := strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		reason := "kill-switch agent stop " + stopID.String()
		n, cerr := rs.CancelActiveTasks(ctx, func(rec mcpc.TaskRecord) bool {
			return rec.Tenant == tenantKey && rec.Subject == subject
		}, reason)
		logMCPSweepResult(log, tenant, "agent", subject, n, cerr)
	}
}

func logMCPSweepResult(log *slog.Logger, tenant model.TenantID, scope, subject string, canceled int, err error) {
	if log == nil || (canceled == 0 && err == nil) {
		return
	}
	attrs := []any{"tenant", tenant.String(), "scope", scope, "cancelled", canceled}
	if subject != "" {
		attrs = append(attrs, "subject", subject)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
		log.Warn("mcp gateway: task kill-switch sweep completed with cancellation errors", attrs...)
		return
	}
	log.Info("mcp gateway: task kill-switch sweep canceled active tasks", attrs...)
}

// mcpToolGate adapts the approval bridge to the MCP connector's ApprovalGate seam:
// a destructive tools/call opens (or idempotently finds) a governed approval bound to
// the tool-call PlanHash and reports its effective status. It NEVER decides — Does.
type mcpToolGate struct {
	bridge *approvalBridge
	tenant model.TenantID
	guard  killSwitchGuard   // nil ⇒ no kill-switch consult (boot always wires it)
	rec    *stopDenyRecorder // throttled tamper-evident deny evidence
}

func (g mcpToolGate) Authorize(ctx context.Context, req mcpc.ToolApprovalRequest) (mcpc.GateDecision, error) {
	// Estate kill switch, BEFORE the approval path: a stop outranks an
	// approved grant AND the break-glass fallback gateOnce carries — an active
	// emergency grant must not re-authorize destructive tools during a stop.
	// Fail-closed on a state read error (the connector maps a gate error to deny).
	if g.guard != nil {
		st, kerr := g.guard.KillSwitchState(ctx, g.tenant)
		if kerr != nil {
			return mcpc.GateDecision{}, fmt.Errorf("mcp gateway: kill-switch state unreadable; tools/call denied (deny-closed)")
		}
		if stopID, stopped := st.Stopped(strings.TrimSpace(req.RequestedBy)); stopped {
			g.rec.record(ctx, g.tenant, stopID, "mcp-tools-call", req.Tool, req.RequestedBy)
			return mcpc.GateDecision{Status: mcpc.StatusRejected, ApprovalRef: "killswitch:" + stopID.String()}, nil
		}
	}
	reason := "MCP destructive tools/call: " + req.Tool
	// gateOnce, not request: tools/call is a ONE-SHOT gate re-issued on every retry
	// of the same call, holding no ref between calls — like the hooks PEP, it
	// must re-derive the decision from the plan hash each time and REUSE an
	// already-approved grant within its time-box. request() (the two-phase open the
	// deploy/orchestration gates pair with a later status()) never reuses an
	// approved grant, so a human approval could never take effect here: the next
	// identical call would open a fresh pending approval, deny-forever. gateOnce
	// also brings the break-glass fallback (audited emergency authorization).
	ref, status, boundHash, err := g.bridge.gateOnce(ctx, g.tenant, "mcp.tool.call", "tool", req.Tool, req.PlanHash, reason, req.RequestedBy)
	if err != nil {
		return mcpc.GateDecision{}, err
	}
	return mcpc.GateDecision{ApprovalRef: ref, Status: mapMCPGateStatus(status), PlanHash: boundHash}, nil
}

// mapMCPGateStatus maps the bridge's neutral status onto the MCP gate vocabulary; every
// non-approved value is a deny. A break-glass authorization proceeds (the "breakglass:"
// reference + the engine-side use trail keep it distinguishable).
func mapMCPGateStatus(neutral string) mcpc.GateStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return mcpc.StatusApproved
	case nbPending:
		return mcpc.StatusPending
	case nbRejected, nbCanceled:
		return mcpc.StatusRejected
	case nbExpired:
		return mcpc.StatusExpired
	default:
		return mcpc.StatusNoGate
	}
}

// mcpTaskGate adapts the FinOps pre-flight budget check to durable MCP task creation.
// A durable task is treated as long-lived session spend: definitive block/throttle caps
// deny the handle before the client learns it; checker errors fail open like the other
// budget adapters.
type mcpTaskGate struct {
	fin    budgetChecker
	tenant model.TenantID
	log    *slog.Logger
}

var _ mcpc.TaskGate = mcpTaskGate{}

func (g mcpTaskGate) AuthorizeTask(ctx context.Context, intent mcpc.TaskIntent) (mcpc.TaskGateDecision, error) {
	if g.fin == nil || g.tenant.IsZero() {
		return mcpc.TaskGateDecision{Allow: true}, nil
	}
	chk, err := g.fin.CheckBudget(ctx, g.tenant, finops.SpendDims{
		AgentRef:   intent.Subject,
		SessionRef: intent.TaskID,
		Gateway:    "mcp",
		CostType:   "task",
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("mcp task-gate: budget check failed; allowing task creation (fail-open)", "err", err)
		}
		return mcpc.TaskGateDecision{Allow: true}, nil
	}
	if !chk.Allowed {
		return mcpc.TaskGateDecision{
			Allow: false, Reason: "task budget " + budgetActionLabel(chk.Action),
			DeniedStatus: budgetStatus(chk.Action),
		}, nil
	}
	return mcpc.TaskGateDecision{Allow: true}, nil
}

// mcpGateAuditor is the evidence seam adapter of the MCP gateway: it backs the
// connector's GateAuditor with the durable evidence operation journal
// (store.ClaimEvidenceOperation / SettleEvidenceOperation) for the ENFORCED
// surfaces, and keeps the historical best-effort ledger anchor for denials and the
// not-yet-enforced legacy surfaces (zero binding — stages 4-6).
type mcpGateAuditor struct {
	log    *slog.Logger
	store  store.Store    // journal + ledger; nil ⇒ enforced allows REFUSE (ledger_unwired)
	tenant model.TenantID // the RS's single tenant (the enforcement anchor)
}

const (
	mcpDecisionDomain = "olivares.mcp.tool.decision.v1"
	// mcpGatewaySurface names this PEP surface in the evidence operation journal.
	mcpGatewaySurface = "mcp.gateway"
)

// mcpToolCallAction is the journal action verb of one enforced tools/call,
// labeled with the OperationID provenance (design §3: a request_instance entry
// must never read as client-keyed idempotency). The paired ledger events are
// "<action>.claim" / "<action>.settle".
func mcpToolCallAction(idKind string) string {
	switch idKind {
	case "keyed", "request_instance":
		return "mcp.tool.call." + idKind
	default:
		return "mcp.tool.call"
	}
}

// mcpEffectAction resolves the journal action of one enforced claim: the
// connector-supplied EffectAction when present (stage 4 — the task
// surface: mcp.task.get.<kind> / mcp.task.cancel.<kind> / mcp.task.update.<kind>
// / mcp.task.track / mcp.task.cancel.compensation / mcp.task.cancel.sweep),
// else the historical tools/call action. ADDITIVE: an empty EffectAction keeps
// the existing mcp.tool.call.* events byte-identical.
func mcpEffectAction(d mcpc.ToolDecision) string {
	if action := strings.TrimSpace(d.EffectAction); action != "" {
		return action
	}
	return mcpToolCallAction(d.OperationIDKind)
}

// refusedMCPGateRecord builds the refused GateRecord for a fault.
func refusedMCPGateRecord(binding sdk.EvidenceBinding, fault sdk.EvidenceFault, failure sdk.FailureClass) mcpc.GateRecord {
	return mcpc.GateRecord{
		Binding: binding,
		Receipt: sdk.EvidenceReceipt{
			OperationID: binding.OperationID, EffectDigest: binding.EffectDigest, Fault: fault,
		},
		State:        mcpc.GateRecordRefused,
		FailureClass: failure,
	}
}

// enforcedTenant resolves the tenant an ENFORCED operation is journaled under: the
// configured RS tenant, with the decision tenant accepted only when EMPTY (fallback)
// or exactly equal. A malformed or mismatched non-empty decision tenant refuses
// (the design flagged the historical silent fallback as a defect: evidence
// must never be silently attributed to a tenant the decision did not name).
func (a mcpGateAuditor) enforcedTenant(decisionTenant string) (model.TenantID, bool) {
	if a.tenant.IsZero() {
		return "", false
	}
	tid, present, err := parseBusinessTenant("mcp decision tenant", decisionTenant)
	if err != nil {
		return "", false
	}
	if !present {
		return a.tenant, true
	}
	if tid != a.tenant {
		return "", false
	}
	return a.tenant, true
}

func (a mcpGateAuditor) Record(ctx context.Context, d mcpc.ToolDecision, binding sdk.EvidenceBinding) mcpc.GateRecord {
	a.log.Info("mcp-gateway: tools/call decision",
		"tool", d.Tool, "subject", d.Subject, "allowed", d.Allowed, "reason", d.Reason,
		"required_scope", d.RequiredScope, "approval_ref", d.ApprovalRef, "task_id", d.TaskID, "mcp", d.MCPTag,
		// SEP-414: the W3C trace id correlating this PEP decision with the
		// gen_ai spans of the same trace (an identifier, never a payload).
		"traceparent", d.TraceParent)

	if !d.Allowed || !binding.Valid() {
		// Denials (best-effort by doctrine — a policy deny NEVER depends on
		// evidence success) and the zero-binding legacy surfaces (stages 4-6).
		a.bestEffortAnchor(ctx, d)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultLedgerUnwired, sdk.FailureEvidenceFault)
	}

	// ENFORCED allow: claim the operation single-use + anchor the evidence in one
	// journal transaction. Every refusal below blocks the effect (deny-closed).
	tenant, ok := a.enforcedTenant(d.Tenant)
	if !ok {
		a.log.Error("mcp-gateway: decision tenant unresolved for enforced tools/call; effect refused",
			"tool", d.Tool, "subject", d.Subject, "decision_tenant", d.Tenant)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultTenantUnresolved, sdk.FailureEvidenceFault)
	}
	actorKind := model.ActorSystem
	if strings.TrimSpace(d.Subject) != "" {
		actorKind = model.ActorAgent
	}
	outcome, err := store.ClaimEvidenceOperation(ctx, a.store, tenant, store.EvidenceClaim{
		OperationID:  string(binding.OperationID),
		EffectDigest: string(binding.EffectDigest),
		Surface:      mcpGatewaySurface,
		Action:       mcpEffectAction(d),
		Actor:        firstNonEmpty(d.Subject, model.ActorSystem),
		ActorKind:    actorKind,
	})
	switch {
	case errors.Is(err, store.ErrEvidenceRebind):
		// Same OperationID, different EffectDigest: the single-use claim is bound
		// to another effect (sdk.FailureReplay — the 409/-31011 wire shape).
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultWriteError, sdk.FailureReplay)
	case err != nil:
		a.log.Error("mcp-gateway: evidence claim failed; effect refused", "tool", d.Tool, "err", err)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultWriteError, sdk.FailureEvidenceFault)
	case outcome.Receipt.MustRefuse(binding):
		// Evidence fault (unwired/unavailable/spool/degrade/...): the specific
		// fault stays server-side; the caller answers 503/-31010.
		a.log.Error("mcp-gateway: evidence claim not anchored; effect refused",
			"tool", d.Tool, "fault", string(outcome.Receipt.Fault))
		return mcpc.GateRecord{
			Binding: binding, Receipt: outcome.Receipt,
			State: mcpc.GateRecordRefused, FailureClass: sdk.FailureEvidenceFault,
		}
	case outcome.Fresh:
		return mcpc.GateRecord{
			Binding: binding, Receipt: outcome.Receipt, State: mcpc.GateRecordFresh,
			// The claim's durable leadership epoch is the fence token BeforeEffect
			// re-verifies immediately before dispatch (opaque to the connector).
			FenceToken: strconv.FormatUint(outcome.Op.LeaderEpoch, 10),
		}
	default:
		// Exact replay (same operation, same digest): return the recorded state,
		// never a second effect.
		rec := mcpc.GateRecord{Binding: binding, Receipt: outcome.Receipt, State: mcpc.GateRecordReplayPending}
		if outcome.Op.State.Terminal() {
			rec.State = mcpc.GateRecordReplaySettled
			rec.Recorded = &mcpc.RecordedOutcome{
				State:        mcpc.DispatchState(outcome.Op.State),
				ResultDigest: outcome.Op.ResultDigest,
				OutcomeRef:   outcome.Op.OutcomeEvidenceRef,
			}
		}
		return rec
	}
}

// BeforeEffect re-verifies the claim's durable leadership fence IMMEDIATELY before
// the upstream dispatch (store.EvidenceEpochFence: held lock session + persisted
// epoch). Any failure refuses; the claim stays claimed and is never re-dispatched.
func (a mcpGateAuditor) BeforeEffect(ctx context.Context, rec mcpc.GateRecord) sdk.EvidenceReceipt {
	refuse := func(fault sdk.EvidenceFault) sdk.EvidenceReceipt {
		return sdk.EvidenceReceipt{
			OperationID: rec.Binding.OperationID, EffectDigest: rec.Binding.EffectDigest, Fault: fault,
		}
	}
	if a.store == nil {
		return refuse(sdk.EvidenceFaultLedgerUnwired)
	}
	epoch, err := strconv.ParseUint(strings.TrimSpace(rec.FenceToken), 10, 64)
	if err != nil {
		// A fresh record always carries the claim's epoch; a malformed token is a
		// caller bug and fails closed, never open.
		return refuse(sdk.EvidenceFaultWriteError)
	}
	if err := store.EvidenceEpochFence(ctx, a.store.Leader(), epoch); err != nil {
		a.log.Error("mcp-gateway: pre-effect leadership fence refused the dispatch", "err", err)
		return refuse(sdk.EvidenceFaultLedgerUnavailable)
	}
	return rec.Receipt
}

// Settle durably records the dispatch outcome against the claim. A refusing
// settlement means the outcome did NOT commit — the caller withholds the response
// and the operation remains claimed/ambiguous (status replay only).
func (a mcpGateAuditor) Settle(ctx context.Context, out mcpc.GateOutcome) mcpc.GateSettlement {
	refused := mcpc.GateSettlement{FailureClass: sdk.FailureEvidenceFault}
	if a.store == nil || a.tenant.IsZero() {
		return refused
	}
	outcome, err := store.SettleEvidenceOperation(ctx, a.store, a.tenant, store.EvidenceSettlement{
		OperationID:  string(out.Record.Binding.OperationID),
		EffectDigest: string(out.Record.Binding.EffectDigest),
		State:        model.EvidenceOperationState(out.State),
		ResultDigest: out.ResultDigest,
		DispatchRef:  out.DispatchRef,
		// The settlement is the GATEWAY's observation of the outcome, not a
		// subject action: system attribution.
		Actor:     model.ActorSystem,
		ActorKind: model.ActorSystem,
	})
	if err != nil {
		a.log.Error("mcp-gateway: evidence settlement failed; response withheld",
			"operation_state", string(out.State), "err", err)
		return refused
	}
	if outcome.Receipt.MustRefuse(out.Record.Binding) {
		a.log.Error("mcp-gateway: evidence settlement not anchored; response withheld",
			"operation_state", string(out.State), "fault", string(outcome.Receipt.Fault))
		return refused
	}
	return mcpc.GateSettlement{
		Outcome: mcpc.RecordedOutcome{
			State:        out.State,
			ResultDigest: outcome.Op.ResultDigest,
			OutcomeRef:   outcome.Op.OutcomeEvidenceRef,
		},
		EvidenceRef: outcome.Receipt.EvidenceRef,
	}
}

// bestEffortAnchor is the historical decision anchor, now serving denials and
// the zero-binding legacy surfaces only (evidence-or-loud-gap; the enforced
// tools/call path journals claim/settle events instead). Carries no raw arguments
// or tokens (docs/SECURITY-HARDENING.md).
func (a mcpGateAuditor) bestEffortAnchor(ctx context.Context, d mcpc.ToolDecision) {
	decision := "deny"
	if d.Allowed {
		decision = "allow"
	}
	tenant := a.tenant
	// Tenant-fallback fix: a MALFORMED non-empty decision tenant is never silently
	// re-attributed to the configured tenant — loud gap. Routes it through the
	// shared policy, which also rejects the reserved system tenant: this branch is
	// reached WITHOUT going through enforcedTenant (see Record), so this is the only
	// check on the path, and anchoring a business decision under the system tenant
	// would file the evidence outside every business boundary.
	tid, present, terr := parseBusinessTenant("mcp decision tenant", d.Tenant)
	if terr != nil {
		// The wording is the operator-facing contract (asserted by
		// TestMCPEvidenceTenantResolution and greppable in logs); the precise reason —
		// unparseable, unset, or the reserved system tenant — rides in `err`.
		a.log.Error("mcp-gateway: malformed decision tenant; decision NOT anchored (evidence gap)",
			"tool", d.Tool, "subject", d.Subject, "decision_tenant", d.Tenant, "err", terr)
		return
	}
	if present {
		tenant = tid
	}
	if a.store == nil || tenant.IsZero() {
		a.log.Error("mcp-gateway: no ledger store/tenant; decision NOT anchored (evidence gap)", "tool", d.Tool, "subject", d.Subject)
		return
	}
	actorKind := model.ActorSystem
	if strings.TrimSpace(d.Subject) != "" {
		actorKind = model.ActorAgent
	}
	ph := mcpDecisionHash(tenant.String(), d.Subject, d.Tool, d.RequiredScope, decision, d.ApprovalRef, d.TaskID, d.MCPTag, d.TokenBinding)
	meta := map[string]any{
		"tool": d.Tool, "subject": d.Subject, "allowed": d.Allowed, "reason": d.Reason,
		"required_scope": d.RequiredScope, "approval_ref": d.ApprovalRef, "task_id": d.TaskID,
		"mcp": d.MCPTag, "token_binding": d.TokenBinding, "decision": decision,
	}
	addAuditDelegationMeta(meta, auditDelegation{isDelegated: d.IsDelegated, actAs: d.ActAs})
	err := a.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:       firstNonEmpty(d.Subject, model.ActorSystem),
			ActorKind:   actorKind,
			Action:      "mcp.tool." + decision,
			TargetKind:  "mcp.tool",
			TargetID:    model.ID(d.Tool),
			PayloadHash: ph,
			Meta:        meta,
		})
		if aerr == nil && ev.Seq == 0 {
			a.log.Error("mcp-gateway: decision evidence dropped by degrade spool (evidence gap)", "tool", d.Tool)
		}
		return aerr
	})
	if err != nil {
		a.log.Error("mcp-gateway: ledger anchor failed (evidence gap)", "tool", d.Tool, "subject", d.Subject, "err", err)
	}
}

func mcpDecisionHash(tenant, subject, tool, requiredScope, decision, approvalRef, taskID, mcpTag, tokenBinding string) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(mcpDecisionDomain))
	writeLenPrefixed(h, []byte(tenant))
	writeLenPrefixed(h, []byte(subject))
	writeLenPrefixed(h, []byte(tool))
	writeLenPrefixed(h, []byte(requiredScope))
	writeLenPrefixed(h, []byte(decision))
	writeLenPrefixed(h, []byte(approvalRef))
	writeLenPrefixed(h, []byte(taskID))
	writeLenPrefixed(h, []byte(mcpTag))
	writeLenPrefixed(h, []byte(tokenBinding))
	return h.Sum(nil)
}

// mcpUpstreamForwarder forwards an admitted/gated MCP method to the upstream tool
// backend over JSON-RPC. CRITICAL (no token passthrough): it authenticates with its OWN
// credential (via credProv) and NEVER sees the inbound bearer — the
// mcpc.UpstreamRequest it receives carries no token, so the inbound credential is
// structurally unreachable from this request.
type mcpUpstreamForwarder struct {
	url      string
	credProv UpstreamCredentialProvider // resolves the SEPARATE upstream credential; NEVER the inbound token
	client   *http.Client
}

var _ mcpc.SubscriptionUpstream = (*mcpUpstreamForwarder)(nil)

// mcpForwardMaxResponse caps an upstream response body. The forwarder reads ONE
// byte past it so an over-limit body is DETECTED (a truncated body can never be
// validated, so it can never confirm an outcome).
const mcpForwardMaxResponse = 8 << 20

// Forward classifies every leg per the dispatch contract (mcpc.Upstream):
// errors BEFORE http.Client.Do are proven not_sent; after invoking Do, the ONLY
// path to `completed` is a 2xx response whose body is a STRICTLY VALID JSON-RPC
// 2.0 response CORRELATED to the sent id and carrying exactly one of
// result|error (mcpc.ParseStrictJSONRPCResponse — a valid JSON-RPC error object
// IS a completed round-trip). EVERYTHING else after Do — timeout, reset,
// cancellation, read failure, over-limit body, non-2xx status, malformed or
// uncorrelated body — is `unknown`: the request may have been transmitted and
// nothing observed can confirm the outcome.
func (f *mcpUpstreamForwarder) Forward(ctx context.Context, req mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	notSent := mcpc.UpstreamResult{State: mcpc.DispatchNotSent}
	unknown := mcpc.UpstreamResult{State: mcpc.DispatchUnknown}
	// sentID correlates the response: the SAME constant is sent and validated.
	sentID := int64(1)
	env := map[string]any{"jsonrpc": "2.0", "id": sentID, "method": req.Method}
	if len(req.Params) > 0 {
		env["params"] = json.RawMessage(req.Params)
	}
	body, err := json.Marshal(env)
	if err != nil {
		return notSent, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return notSent, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if f.credProv != nil {
		authHdr, cerr := f.credProv.Credential(ctx, f.url)
		if cerr != nil {
			// An ENTITLEMENT refusal is not a transport fault, and collapsing the two is
			// how "your license lapsed" reaches an operator dressed as "the upstream is
			// down". The enterprise credential minter (credminter) raises
			// license.ErrAddonRequiresLicense; give it a stable, self-describing wrapper
			// so logs, tests and the client can tell the two apart.
			//
			// The dispatch state stays not_sent, which is PROVABLY true here: we refuse
			// before http.Client.Do, so nothing was transmitted (mcpc.Upstream contract).
			if addon, op, ok := license.AddonRefusal(cerr); ok {
				return notSent, mcpAddonRefusal(addon, op, cerr)
			}
			return notSent, fmt.Errorf("mcp gateway: credential provider: %w", cerr)
		}
		if authHdr != "" {
			httpReq.Header.Set("Authorization", authHdr) // separate credential — no passthrough
		}
	}
	if req.TraceParent != "" {
		httpReq.Header.Set("traceparent", req.TraceParent)
	}
	// propagate the operation identity + fence token for receiver-side
	// idempotency/fencing (design §6 — identifiers only, never credentials). An
	// upstream that understands them can reject a stale epoch; one that does not
	// ignores unknown headers (the check-to-effect window residual remains).
	if req.OperationID != "" {
		httpReq.Header.Set("Olivares-Operation-Id", string(req.OperationID))
	}
	if req.EffectDigest != "" {
		httpReq.Header.Set("Olivares-Effect-Digest", string(req.EffectDigest))
	}
	// Round-5 R5-05: the MCP ROUTING MIRRORS, derived by the connector from
	// these exact params (mcpc.UpstreamRoutingHeaders) so a header can never
	// contradict the body it mirrors. They are written LAST, after the operator and
	// credential headers, for the same reason the connector's own client writes them
	// last: a mirror that lied about the body is a -32020 HeaderMismatch, and a
	// strict RC upstream refuses a tasks/* request that carries none — which would
	// leave a retained task record permanently unreadable and therefore undrainable.
	for k, v := range mcpc.UpstreamRoutingHeaders(req.Method, req.Params) {
		httpReq.Header.Set(k, v)
	}
	if req.FenceToken != "" {
		httpReq.Header.Set("Olivares-Fence-Token", req.FenceToken)
	}
	resp, err := f.client.Do(httpReq)
	if err != nil {
		// After invoking Do the request may have reached the wire: unknown.
		return unknown, fmt.Errorf("mcp gateway: upstream forward: %w", err)
	}
	defer resp.Body.Close()
	// limit+1: an over-limit body is DETECTED, never silently truncated — a valid
	// prefix followed by overflow data must not be validated as a response.
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, mcpForwardMaxResponse+1))
	if rerr != nil {
		return unknown, fmt.Errorf("mcp gateway: read upstream response: %w", rerr)
	}
	if len(raw) > mcpForwardMaxResponse {
		return unknown, fmt.Errorf("mcp gateway: upstream response exceeds %d bytes; cannot validate (outcome unknown)", mcpForwardMaxResponse)
	}
	// Review round-1 P1 + round-2 NEW-1: "completed" means an upstream response was
	// OBSERVED and STRICTLY CONFIRMED. A non-2xx status is not a confirmed JSON-RPC
	// outcome (an intermediary may have produced it after the origin possibly
	// acted) → unknown. A 2xx body must pass the connector's strict validation
	// (exact member casing, duplicate-key + trailing-data rejection, correlated
	// integer id == sentID, exactly one of result|error, error carrying an integer
	// code + string message) → anything else is unknown.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unknown, fmt.Errorf("mcp gateway: upstream http %d (outcome unknown)", resp.StatusCode)
	}
	result, rpcErr, verr := mcpc.ParseStrictJSONRPCResponse(raw, sentID)
	if verr != nil {
		return unknown, fmt.Errorf("mcp gateway: upstream response failed strict validation (outcome unknown): %w", verr)
	}
	if rpcErr != nil {
		// A strictly valid JSON-RPC error IS a completed round-trip (design §1).
		return mcpc.UpstreamResult{State: mcpc.DispatchCompleted},
			fmt.Errorf("mcp gateway: upstream rpc %d %s", rpcErr.Code, rpcErr.Message)
	}
	return mcpc.UpstreamResult{Result: result, State: mcpc.DispatchCompleted}, nil
}

// Listen opens one long-lived upstream subscriptions/listen request. It uses
// the forwarder's separate credential provider and a copy of its HTTP client
// with no whole-request timeout: the downstream request context is the stream
// lifetime and canceling it is MCP's unsubscribe signal.
func (f *mcpUpstreamForwarder) Listen(
	ctx context.Context,
	req mcpc.SubscriptionListenRequest,
	emit func(mcpc.SubscriptionEvent) error,
) error {
	const requestID int64 = 1
	body, params, err := mcpc.MarshalSubscriptionUpstreamRequest(requestID, req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if f.credProv != nil {
		authHeader, credentialErr := f.credProv.Credential(ctx, f.url)
		if credentialErr != nil {
			if addon, operation, ok := license.AddonRefusal(credentialErr); ok {
				return mcpAddonRefusal(addon, operation, credentialErr)
			}
			return fmt.Errorf("mcp gateway: subscription credential provider: %w", credentialErr)
		}
		if authHeader != "" {
			httpReq.Header.Set("Authorization", authHeader)
		}
	}
	if req.TraceParent != "" {
		httpReq.Header.Set("traceparent", req.TraceParent)
	}
	for key, value := range mcpc.UpstreamRoutingHeaders(mcpc.SubscriptionListenMethod, params) {
		httpReq.Header.Set(key, value)
	}

	client := http.DefaultClient
	if f.client != nil {
		streamClient := *f.client
		streamClient.Timeout = 0
		client = &streamClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: upstream listen: %v", mcpc.ErrSubscriptionRelayTruncated, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, mcpForwardMaxResponse))
		return fmt.Errorf("mcp gateway: upstream listen http %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("mcp gateway: upstream listen is not an event stream (%q)", resp.Header.Get("Content-Type"))
	}
	err = mcpc.ConsumeSubscriptionUpstreamStream(resp.Body, requestID, emit)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
