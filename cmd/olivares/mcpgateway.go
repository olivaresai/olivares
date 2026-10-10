// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// mcpgateway.go wires the agent-protocols GATEWAY in the composition root: the
// inline MCP Resource-Server PEP (connectors/mcp) and the A2A push-notification
// receiver (connectors/a2a), each mounted on a dedicated socket like the HITL receiver.
// The connectors own the protocol + the deny-closed seams (token validation, the
// tools/call gate, push JWT verification); internal/mcpgateway binds those seams to
// the AGPL plane the connectors may not import (operator config, evidence journal and
// ledger, upstream tool backend, task kill-switch sweep). This file only wires it from
// the engine and adapts the engine's ApprovalGate (for destructive tools) and
// FinOps admission to the connector's gate seams.
//
// It is loaded from OLIVARES_AGENT_GATEWAY_CONFIG (operator-provisioned, secret-bearing,
// out of the store). Absent/invalid ⇒ nothing mounted (the safe default; an un-wired
// node serves no inbound agent surface). Every governance seam stays deny-closed.

// loadAgentGatewayConfig reads the optional OLIVARES_AGENT_GATEWAY_CONFIG JSON. A
// missing path yields an empty config (nothing mounted); a supplied path must be readable
// and contain valid JSON or startup fails closed.
func loadAgentGatewayConfig(_ *slog.Logger) (mcpgateway.Config, error) {
	path := osGetenv("OLIVARES_AGENT_GATEWAY_CONFIG")
	if path == "" {
		return mcpgateway.Config{}, nil
	}
	var cfg mcpgateway.Config
	if err := loadOperatorJSONConfig("OLIVARES_AGENT_GATEWAY_CONFIG", path, &cfg); err != nil {
		return mcpgateway.Config{}, err
	}
	if err := mcpgateway.ValidateSource(cfg); err != nil {
		return mcpgateway.Config{}, err
	}
	return cfg, nil
}

// buildAgentGatewayServer constructs the inbound agent-protocols server (MCP RS +
// A2A push receiver) on its own socket, or nil when neither is configured. The MCP
// tools/call HITL gate bridges to the SAME approval bridge the rest of Phase K
// uses; the upstream forwarder uses a SEPARATE credential (no token passthrough).
func buildAgentGatewayServer(eng *engine, log *slog.Logger) (*http.Server, error) {
	var cfg mcpgateway.Config
	if eng != nil && eng.gatewayConfig != nil {
		cfg = *eng.gatewayConfig
	} else {
		var err error
		cfg, err = loadAgentGatewayConfig(log)
		if err != nil {
			return nil, fmt.Errorf("load agent gateway operator config: %w", err)
		}
	}
	mux := http.NewServeMux()
	mounted := false
	var mcpRS *mcpc.ResourceServer
	var mcpTenant model.TenantID
	if cfg.SessionTools {
		if eng == nil || eng.authr == nil || eng.api == nil || eng.sessionsMod == nil {
			return nil, errors.New("agent-gateway: session_tools requires the session authority and API")
		}
		scope := sessionOrchestrationWorkScope{st: eng.store, module: eng.sessionsMod}
		authenticator := sessionMCPAuthenticator(eng.authr)
		issued := eng.sessionHooks != nil && eng.sessionHooks.SessionCredentials != nil
		if issued {
			authenticator = eng.sessionHooks.SessionCredentials
		}
		mux.Handle("/session/mcp", &sessionMCPHandler{authr: authenticator, issuedSessionOnly: issued, admits: eng.admits(), work: eng.sessionsMod.CallSessionWork,
			configurer: eng, publisher: eng.sessionPublisher(),
			checkOrchestration: func(ctx context.Context, p auth.Principal, tenant model.TenantID) error {
				return scope.WithScope(ctx, p, tenant, false, func(store.Scope) error { return nil })
			}})
		mounted = true
		log.Info("agent-gateway: private session MCP tools mounted", "path", "/session/mcp")
	}

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
		addr = mcpgateway.DefaultListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("agent-gateway: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token/JWT verification, not network isolation", "addr", addr)
	}
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = mux
	mcpgateway.StartTaskKillSwitchSweep(srv, mcpRS, eng.killSwitch, mcpTenant, defaultStopSweepInterval, log)
	return srv, nil
}

// buildMCPResourceServer builds the inline MCP PEP from config, binding the tools/call
// HITL gate to the approval bridge and the upstream to a no-passthrough forwarder.
func buildMCPResourceServer(eng *engine, cfg *mcpgateway.MCPConfig, log *slog.Logger) (*mcpc.ResourceServer, model.TenantID, error) {
	return buildMCPResourceServerWithDurableTaskStore(eng, cfg, log, nil)
}

// buildMCPResourceServerWithDurableTaskStore keeps the production builder and
// its integration fixtures on the same composition path. Production callers
// pass nil and derive the store exclusively from durable_tasks configuration;
// focused tests may supply an explicit implementation.
func buildMCPResourceServerWithDurableTaskStore(
	eng *engine,
	cfg *mcpgateway.MCPConfig,
	log *slog.Logger,
	durableTaskStore mcpc.DurableTaskStore,
) (*mcpc.ResourceServer, model.TenantID, error) {
	// Validate the operator's revision pair FIRST, before anything is
	// constructed. An unknown, empty or self-contradictory pair refuses to mount,
	// which is how this composition already refuses malformed provisioning: the
	// caller logs PROVISIONED BUT NOT MOUNTED and never builds a listener for it.
	revisionMode, err := mcpgateway.ResolveRevisionMode(cfg)
	if err != nil {
		return nil, "", err
	}

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
	upstream := cfg.ManagedUpstream // nil ⇒ deny-closed (admitted/gated but not actuated)
	var subscriptionUpstream mcpc.SubscriptionUpstream
	// upstreamDescriptor is the STABLE upstream/credential-profile identity bound
	// into every tools/call EffectDigest (round-2): the identity fields the
	// config carries TODAY — the upstream base URL and the credential provider's
	// Go type (build-dependent: static in community, token-exchange minter in
	// enterprise) — NEVER the secret itself. A re-pointed backend therefore
	// changes the effect identity: a keyed retry rebinds instead of replaying.
	upstreamDescriptor := cfg.ManagedUpstreamDescriptor

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
		credProv := thisEdition.upstreamCredentialProvider(cfg.UpstreamAuth)
		forwarder := &mcpgateway.UpstreamForwarder{
			URL:      strings.TrimSpace(cfg.UpstreamURL),
			CredProv: credProv,
			// cli-transport-exempt: ENGINE→upstream MCP server, not a CLI path. The
			// gateway forwards on behalf of a governed session; its credentials come
			// from the upstream credential provider, never from a client context.
			Client: &http.Client{Timeout: 60 * time.Second},
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
	ri := thisEdition.mcpRenderInspector.get(osGetenv, log)
	em := thisEdition.mcpElicitationMediator.get(osGetenv, log)
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
		Auditor:               mcpgateway.GateAuditor{Log: log, Store: eng.store, Tenant: rsTenant},
		PinVerifier:           eng.pinVerifier,
		RenderInspector:       ri,
		ElicitationMediator:   em,

		// The validated EXPLICIT mode, or "" when the operator stated none.
		// The legacy knob below is passed exactly as before on purpose — the
		// connector's resolver consults it only while RevisionMode is empty
		// (rsconfig.go:438-452), so the absent-mode path stays identical to the
		// composition that shipped, and the explicit path reaches the same single
		// resolver instead of a second one.
		RevisionMode:               revisionMode,
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
	// The read-back is emitted LAST, once every seam is wired, so it only
	// ever describes a Resource Server this function is about to return. Emitting
	// it earlier would publish an effective configuration for a composition that
	// then failed to mount, which is the exact shape of a misleading startup line.
	mcpgateway.LogEffectiveConfig(log, mcpgateway.EffectiveConfig{
		// The RESOLVED mode is read back from the Resource Server that was built,
		// not predicted here: it is the value that server enforces.
		RevisionMode:         rs.RevisionMode(),
		RevisionModeSource:   mcpgateway.RevisionModeSource(cfg, revisionMode),
		Upstream:             mcpgateway.UpstreamKind(upstreamDescriptor),
		SubscriptionUpstream: mcpgateway.SeamState(subscriptionUpstream != nil),
		SubscriptionLedger:   mcpgateway.SeamState(subscriptionLedger != nil),
		DurableTaskStore:     mcpgateway.SeamState(durableTasks != nil),
	})
	return rs, rsTenant, nil
}

// buildMCPSubscriptionLedger resolves the optional operator-owned workspace
// into the narrow sessions port. An omitted block leaves the ledger nil so the
// connector returns 503 for subscriptions/listen without affecting other MCP
// methods.
func buildMCPSubscriptionLedger(
	eng *engine,
	tenant model.TenantID,
	cfg *mcpgateway.DurableSubscriptionsConfig,
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
	cfg *mcpgateway.DurableTasksConfig,
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
	// approved grant AND the approval bridge's break-glass fallback — an active
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
	if strings.Contains(req.Rule, "/condition:") {
		// A Cedar condition asked, not the destructive flag: name it for the approver.
		reason = "MCP tools/call held by " + req.Rule + ": " + req.Tool
	}
	// tools/call is re-issued on every retry of the same call and holds no approval
	// between calls. The MCP 2026-07-28 approval round trip spends the approval for
	// its one call; a call without a round trip keeps the published reuse of an
	// approved grant inside its time box.
	spend := reuseInWindow
	if req.ConsumerID != "" {
		spend = spendOnce
	}
	a, err := gateEffect(ctx, g.bridge, gatedEffect{
		Tenant: g.tenant, Action: "mcp.tool.call", SubjectKind: "tool", SubjectRef: req.Tool, PlanHash: req.PlanHash,
		Reason: reason, RequestedBy: req.RequestedBy, Consumer: req.ConsumerID, PolicyVersion: "mcp-toolcall-v1", Spend: spend,
	})
	if a.Failed == stepSpend {
		if g.bridge.Log != nil {
			g.bridge.Log.Warn("mcp gateway: approval could not be spent for a round trip; tools/call denied (deny-closed)",
				"tenant", g.tenant.String(), "approval_ref", a.Ref, "err", err)
		}
		return mcpc.GateDecision{}, fmt.Errorf("mcp gateway: approval could not be spent; tools/call denied (deny-closed): %w", err)
	}
	if err != nil {
		return mcpc.GateDecision{}, err
	}
	status := mapMCPGateStatus(a.Status)
	if status == mcpc.StatusApproved && a.Outcome != effectAllowed {
		// Approved, but not spendable for this round trip: another one spent it.
		status = mcpc.StatusRejected
	}
	return mcpc.GateDecision{ApprovalRef: a.Ref, Status: status, PlanHash: a.BoundHash, Spent: a.Spent}, nil
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

// mcpTaskGate adapts FinOps admission to durable MCP task creation. A durable task is
// treated as long-lived session spend: definitive block/throttle caps deny the handle
// before the client learns it, and a ledger that cannot be read denies it too, like the
// other budget adapters.
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
	// The key is the task's own id, so every question about one task lands on one
	// admission row. A task's cost is accounted as it runs, not here, so the gate holds
	// nothing it would have to settle (engineGateNoEstimate, budgetgate.go).
	key := strings.TrimSpace(intent.TaskID)
	if key == "" {
		key = model.NewID().String()
	}
	res, err := g.fin.Reserve(ctx, g.tenant, finops.AdmissionRequest{
		Scope: finops.AdmissionScopeScheduledJob,
		Dims: finops.SpendDims{
			AgentRef:   intent.Subject,
			SessionRef: intent.TaskID,
			Gateway:    "mcp",
			CostType:   "task",
		},
		EstimateMicroUSD: engineGateNoEstimate,
		IdempotencyKey:   "mcp_task/" + key,
		Unreachable:      engineReserveUnreachable,
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("mcp task-gate: budget admission failed; denying task creation (fail-closed)", "err", err)
		}
		return mcpTaskBudgetUnavailable(), nil
	}
	if !res.Allowed {
		switch res.Reason {
		case finops.ReasonStoreUnreachable:
			return mcpTaskBudgetUnavailable(), nil
		case finops.ReasonAdmissionIntegrity:
			// Refused in every posture: the ledger answered, and the refusal is not a cap.
			return mcpc.TaskGateDecision{
				Allow: false, Reason: finops.ReasonAdmissionIntegrity,
				DeniedStatus: http.StatusServiceUnavailable,
			}, nil
		}
		return mcpc.TaskGateDecision{
			Allow: false, Reason: "task budget " + budgetActionLabel(res.Action),
			DeniedStatus: budgetStatus(res.Action),
		}, nil
	}
	return mcpc.TaskGateDecision{Allow: true}, nil
}

// mcpTaskBudgetUnavailable is the task gate's refusal when admission could not be
// established: the budget control is unavailable, and the task is not created.
func mcpTaskBudgetUnavailable() mcpc.TaskGateDecision {
	return mcpc.TaskGateDecision{
		Allow: false, Reason: "task budget control unavailable (deny-closed)",
		DeniedStatus: http.StatusServiceUnavailable,
	}
}
