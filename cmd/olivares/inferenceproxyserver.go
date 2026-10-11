// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// inferenceproxyserver.go wires the OPTIONAL, OPT-IN inline inference PEP: it reads
// the operator config, builds the connector's protocol shell around the governed Decider
// (internal/inferencepep) and mounts both on the proxy's own listener.

// The enterprise overlay and the open wire files name the proxy's optional gate seams with
// these identifiers; the definitions live in internal/inferencepep.
type (
	serverToolEgressGate = inferencepep.ServerToolEgressGate
	contentInspector     = inferencepep.ContentInspector
	computerUseGate      = inferencepep.ComputerUseGate
)

// proxyApprovals hands the inference PEP the bridge's notify and gateOnce.
type proxyApprovals struct{ b *approvalBridge }

// proxyApprovalsFor keeps a nil bridge a nil opener: the proxy then denies with a finding
// only, as it did when it held the bridge pointer itself.
func proxyApprovalsFor(b *approvalBridge) inferencepep.ApprovalOpener {
	if b == nil {
		return nil
	}
	return proxyApprovals{b}
}

func (a proxyApprovals) Notify(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) error {
	return a.b.Notify(ctx, tenant, action, subjectKind, subjectRef, planHash, reason, requestedBy)
}

func (a proxyApprovals) GateOnce(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error) {
	return a.b.GateOnce(ctx, tenant, action, subjectKind, subjectRef, planHash, reason, requestedBy)
}

func hexSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// defaultInferenceProxyListen preserves the published endpoint for existing clients.
// Co-enabled Codex hooks need a distinct explicit listen address.
const defaultInferenceProxyListen = "127.0.0.1:8448"

// awsExternalAnthropicService is the SigV4 service name of the Claude-Platform-on-AWS
// inference surface (surfaces.go).
const awsExternalAnthropicService = "aws-external-anthropic"

// inferenceProxyConfig is the operator provisioning for the inline proxy (out of the
// store, secret-bearing — same pattern as OLIVARES_HOOK_PEP_CONFIG). One instance fronts
// ONE surface; the upstream credential lives here and is NEVER the inbound caller's.
type inferenceProxyConfig struct {
	Listen              string `json:"listen"`
	Surface             string `json:"surface"`               // direct | claude-platform-aws (v1)
	BaseURL             string `json:"base_url"`              // override; default per surface (with {region})
	Region              string `json:"region"`                // {region} substitution + SigV4 region (AWS)
	InferenceGeo        string `json:"inference_geo"`         // the surface's declared inference geo ("" = per-request)
	AnthropicVersion    string `json:"anthropic_version"`     // anthropic-version header ("" = client default)
	Tenant              string `json:"tenant"`                // optional fixed tenant ("" = infer from credential)
	PublicURL           string `json:"public_url"`            // externally visible origin for apps-gateway OAuth discovery
	ManagedSettingsPath string `json:"managed_settings_path"` // single-document managed settings JSON
	// UpstreamKey is the OPERATOR's Anthropic inference (workspace) API key for the direct
	// surface. SECRET — held in memory, never logged. NEVER the inbound caller's credential.
	UpstreamKey string `json:"upstream_key"`
	// AWS SigV4 credentials for the claude-platform-aws surface (SECRET).
	AWSAccessKeyID  string `json:"aws_access_key_id"`
	AWSSecretKey    string `json:"aws_secret_access_key"`
	AWSSessionToken string `json:"aws_session_token"`
}

// loadInferenceProxyConfig reads the optional OLIVARES_INFERENCE_PROXY_CONFIG JSON. A
// missing path yields an empty config (nothing mounted); a supplied path must be readable
// and contain valid JSON or startup fails closed.
func loadInferenceProxyConfig(_ *slog.Logger) (inferenceProxyConfig, error) {
	path := osGetenv("OLIVARES_INFERENCE_PROXY_CONFIG")
	if path == "" {
		return inferenceProxyConfig{}, nil
	}
	var cfg inferenceProxyConfig
	if err := loadOperatorJSONConfig("OLIVARES_INFERENCE_PROXY_CONFIG", path, &cfg); err != nil {
		return inferenceProxyConfig{}, err
	}
	return cfg, nil
}

// --- composition root ---------------------------------------------------------------

// proxyAuditor is the minimal-data SOC logger handed to the connector shell.
type proxyAuditor struct{ log *slog.Logger }

func (a proxyAuditor) Record(_ context.Context, ev claudeapi.ProxyAuditEvent) {
	a.log.Info("inference-proxy: decision",
		"decision", ev.Decision, "model", ev.Model, "streamed", ev.Streamed,
		"upstream_status", ev.UpstreamStatus, "req_bytes", ev.ReqBytes, "resp_bytes", ev.RespBytes,
		"reason", ev.Reason)
}

// buildClaudeMessagesProxyServer constructs the inline inference PEP server on its own
// loopback socket, or nil when not provisioned (or unsupported/unwired). It is built
// AFTER boot, so the engine's authenticator, models/finops modules, kill-switch, residency
// registry and store are all live. The server is a DEDICATED *http.Server with
// WriteTimeout 0 — the core API's 60s WriteTimeout would cut a streaming SSE response.
func buildClaudeMessagesProxyServer(eng *engine, log *slog.Logger, consoleURL string) (*http.Server, error) {
	cfg, err := loadInferenceProxyConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load inference proxy operator config: %w", err)
	}
	if strings.TrimSpace(cfg.Surface) == "" {
		eng.contentFirewall.record(nil)
		return nil, nil // not provisioned
	}
	if !eng.moduleProfile.Active("inferenceproxy") {
		log.Warn("inference-proxy: configured, but the inferenceproxy module is not enabled on this node; NOT mounted")
		eng.contentFirewall.record(nil)
		return nil, nil
	}
	// the fixed tenant is validated FIRST, before anything else this config drives,
	// and deny-closed like the five sibling readers of a configured tenant. Until an
	// operator typo here only produced a startup log.Warn and the proxy kept going with an
	// empty hint — so a config that said "serve ONLY this organization" silently became
	// "serve whichever organization the credential names". An ABSENT tenant is a different
	// and legitimate case (the documented "" = infer from the credential) and is NOT an
	// error: parseBusinessTenant reports it as not-present with a nil error.
	tenantHint, _, terr := parseBusinessTenant("inference-proxy config: tenant", cfg.Tenant)
	if terr != nil {
		return nil, terr
	}
	surface := sdkmodel.Gateway(strings.TrimSpace(cfg.Surface))
	if surface != sdkmodel.GatewayDirect && surface != sdkmodel.GatewayClaudePlatformAWS {
		log.Error("inference-proxy: surface not supported in v1 (use direct or claude-platform-aws); NOT mounted", "surface", surface)
		eng.contentFirewall.record(nil)
		return nil, nil
	}
	// The governed decision needs all of these; in production they are always wired. If any
	// is missing, do NOT mount an ungoverned proxy (deny-closed posture).
	if eng.authr == nil || eng.models == nil || eng.finops == nil || eng.killSwitch == nil || eng.inferenceProxy == nil || eng.store == nil {
		log.Error("inference-proxy: governance dependencies not wired; NOT mounted (deny-closed)")
		eng.contentFirewall.record(nil)
		return nil, nil
	}

	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		if s, ok := claudeapi.SurfaceFor(surface); ok {
			base = strings.ReplaceAll(s.BaseURLPattern, "{region}", strings.TrimSpace(cfg.Region))
		}
	}
	apiKey := strings.TrimSpace(cfg.UpstreamKey)
	var doer modelprovider.Doer
	if eng.tracer != nil {
		// The dedicated proxy is outside the core API router, so it must opt into the
		// same minimal-data GenAI transport explicitly. The transport records bounded
		// body fingerprints plus model/token metadata; it never attaches content.
		doer = eng.tracer.AnthropicHTTPClient(nil)
	}
	if surface == sdkmodel.GatewayClaudePlatformAWS {
		// SigV4-signed forward (service aws-external-anthropic); no x-api-key on this surface.
		doer = claudeapi.NewSigV4Doer(doer, cfg.AWSAccessKeyID, cfg.AWSSecretKey, cfg.AWSSessionToken, awsExternalAnthropicService, strings.TrimSpace(cfg.Region), nil)
		apiKey = ""
	}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		BaseURL: base, APIKey: apiKey, AnthropicVersion: strings.TrimSpace(cfg.AnthropicVersion),
		Gateway: surface, Doer: doer,
	})

	dec := &inferencepep.Decider{
		Surface: surface, SurfaceGeo: strings.TrimSpace(cfg.InferenceGeo), TenantHint: tenantHint,
		Inference: inf, Auth: eng.authr, Models: eng.models, Budget: eng.finops, KillSwitch: eng.killSwitch,
		ContextPolicy: eng.knowledgeMod,
		Policy:        eng.inferenceProxy, Residency: eng.residencyReg, Store: eng.store, Bus: eng.bus,
		// Server-tool egress gate (P0 #1): the real gate under -tags enterprise WITH an egress
		// config, else nil (observe-only, unchanged). approvals reuses the HITL bridge.
		Egress: thisEdition.serverToolEgressGate.get(osGetenv, log), Approvals: proxyApprovalsFor(eng.approvalBridge),
		// Content firewall (P1): the real inspector under -tags enterprise WITH a firewall
		// config, else nil (no deep inspection, unchanged). Reuses the same approval bridge.
		Inspector: thisEdition.contentInspector.get(osGetenv, log),
		// Computer-use governance: the real gate under -tags enterprise WITH a
		// computer-use config, else nil (ungoverned, unchanged).
		ComputerUse: thisEdition.computerUseGate.get(osGetenv, log),
		// Runtime circuit-breaker (wired in): the SAME instance the finding
		// rail drives, taken from the engine rather than constructed again here — two
		// instances would mean the breaker that trips is not the breaker consulted.
		CircuitBreaker: eng.circuitBreaker,
		Clock:          time.Now, Log: log,
	}
	if eng.sessionHooks != nil {
		dec.SessionCredentials = eng.sessionHooks.SessionCredentials
	}
	proxy := claudeapi.NewMessagesProxy(inf, dec, proxyAuditor{log: log}, time.Now)
	credSource, credKind := sessionCredentialSource(osGetenv, eng.wifBroker)
	if strings.TrimSpace(cfg.PublicURL) != "" && credSource == nil {
		log.Warn("inference-proxy: apps-gateway OAuth enabled without a sessions credential source; approved device grants cannot mint until one is configured")
	} else if strings.TrimSpace(cfg.PublicURL) != "" {
		log.Info("inference-proxy: apps-gateway OAuth credential source wired", "source", credKind)
	}
	grantStore, _ := eng.inferenceProxy.(deviceGrantStore)
	spendAdmin, _ := eng.finops.(spendLimitAdmin)

	mux := http.NewServeMux()
	apps := newAppsGatewayHandler(cfg, tenantHint, eng.authr, grantStore, spendAdmin, credSource, time.Now, version, eng.admits())
	apps.consoleURL = consoleURL
	mountAppsGatewayHandlers(mux, apps)
	mux.Handle("/", appsGatewayRootHandler(proxy))
	var handler http.Handler = mux
	if eng.tracer != nil {
		// This listener does not pass through api.Server's middleware chain. Apply the
		// same method-only server span here; raw paths and bodies never become attributes.
		handler = eng.tracer.HTTPMiddleware(handler)
	}
	// #429: a model call whose bearer is a live session's inference credential
	// carries that session's agent-span context, so the server span above and the
	// chat span the transport emits land in the session's trace. The lookup is a
	// digest map hit that authenticates nothing — the governed decision inside
	// still authenticates; telemetry never gates.
	var resolve func(string) (oteltrace.SpanContext, string, bool)
	if eng.sessionsMod != nil {
		resolve = eng.sessionsMod.SessionTraceForToken
	}
	handler = linkSessionTrace(handler, resolve)
	addr := strings.TrimSpace(cfg.Listen)
	if addr == "" {
		addr = defaultInferenceProxyListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("inference-proxy: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token verification + the governed decision, not network isolation", "addr", addr)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		// WriteTimeout MUST be 0: a streaming /v1/messages SSE response runs longer than any
		// fixed deadline. The core API server's 60s WriteTimeout (server.go) would cut it.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	log.Info("inference-proxy: inline /v1/messages PEP mounted (opt-in, governed)",
		"addr", addr, "surface", surface, "geo", firstNonEmpty(cfg.InferenceGeo, "per-request"),
		"tenant_hint", tenantHint.String(), "residency", eng.residencyReg != nil && eng.residencyReg.Enforces())
	eng.contentFirewall.record(dec)
	return srv, nil
}
