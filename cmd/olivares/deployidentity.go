// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/deploy"
)

// This file is the VII IdentityBinder seam adapter. It implements
// deploy.IdentityBinder (modules/deploy/ports.go) by binding an agent to its
// per-agent NHI identity through governed endpoint
// (POST /v1/m/governance/agents/{agentID}/identity, modules/governance/identity.go),
// IN-PROCESS over the engine's own handler — the same captureWriter mechanism the
// Approval bridge uses (hitl.go), so the full authenticate→tenant→authorize→
// handler→audit chain runs with zero new code path. The module never sets
// Agent.IdentityID directly (that would re-implement bridge); it asks here.
//
// Firm attribution closes the PERMITTED side of the access map (row
// III): a binding is FIRM only when a single, unambiguous per-agent identity is
// bound. If the identity is shared across agents, or the agent is unknown, or no
// binding credential is configured, attribution is DEGRADED — surfaced honestly,
// never faked (the module then publishes an "agent" origin, not an "identity" one).
//
// Like the approval bridge, the binding credential is an operator-provisioned,
// per-tenant SERVICE token (identity-admin scope, NOT in the approver pool); it
// lives only in the OLIVARES_DEPLOY_EXECUTOR_CONFIG file, never in the store and
// never logged. The engine handler is late-bound in boot() after api.New (a call
// before binding fails closed → degraded).

// identityTenantCfg maps one tenant to the service token the binder calls as.
type identityTenantCfg struct {
	Tenant string `json:"tenant"`
	Token  string `json:"token"`
}

// deployIdentityBinder implements deploy.IdentityBinder against in-process.
type deployIdentityBinder struct {
	creds map[model.TenantID]string // tenant -> identity-admin service token (secret; never logged)
	log   *slog.Logger

	handlerMu sync.RWMutex
	handler   http.Handler

	warned sync.Map // model.TenantID -> struct{}
}

var _ deploy.IdentityBinder = (*deployIdentityBinder)(nil)

// newDeployIdentityBinder builds the binder from per-tenant config. A bad tenant id
// or empty token is skipped with a warning (a visible misconfiguration, never a
// silently-faked binding). It returns nil when no usable tenant is configured — the
// honest absence that leaves the module's degraded-attribution default in place.
func newDeployIdentityBinder(tenants []identityTenantCfg, log *slog.Logger) *deployIdentityBinder {
	creds := map[model.TenantID]string{}
	for _, tc := range tenants {
		tid, present, err := parseBusinessTenant("deploy-identity config: tenant", tc.Tenant)
		if err != nil || !present {
			log.Warn("deploy-identity: tenant entry has an invalid tenant id; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.Token) == "" {
			log.Warn("deploy-identity: tenant entry has no service token; skipped (per-agent attribution stays degraded)", "tenant", tc.Tenant)
			continue
		}
		creds[tid] = tc.Token
	}
	if len(creds) == 0 {
		return nil
	}
	log.Info("deploy-identity: firm IdentityBinder wired (per-agent NHI binding)", "tenants", len(creds))
	return &deployIdentityBinder{creds: creds, log: log}
}

// useHandler late-binds the engine's API handler (boot.go, after api.New).
func (b *deployIdentityBinder) useHandler(h http.Handler) {
	b.handlerMu.Lock()
	b.handler = h
	b.handlerMu.Unlock()
}

func (b *deployIdentityBinder) currentHandler() http.Handler {
	b.handlerMu.RLock()
	defer b.handlerMu.RUnlock()
	return b.handler
}

// degraded returns an honest degraded result (Firm=false), never an error, so the
// module records degraded attribution rather than treating it as a hard failure.
func degraded(reason string) (deploy.BoundIdentity, error) {
	return deploy.BoundIdentity{IdentityRef: "", Firm: false, Reason: reason}, nil
}

// EnsureAgentIdentity binds the agent's per-agent NHI through and reports whether
// the resulting attribution is firm.
func (b *deployIdentityBinder) EnsureAgentIdentity(ctx context.Context, tenant model.TenantID, agentRef, identityRef string, mint bool) (deploy.BoundIdentity, error) {
	token, ok := b.creds[tenant]
	if !ok {
		b.warnUnconfigured(tenant)
		return degraded("no identity-binding credential configured for this tenant; per-agent attribution degraded")
	}
	if b.currentHandler() == nil {
		return degraded("governance handler not yet bound; per-agent attribution degraded")
	}
	if strings.TrimSpace(identityRef) == "" && !mint {
		return degraded("no identity_ref declared and mint=false; nothing to bind firmly")
	}

	agentID, found := b.resolveAgentID(ctx, tenant, token, agentRef)
	if !found {
		return degraded("agent not found in the roster; per-agent attribution degraded")
	}

	body := map[string]any{}
	if strings.TrimSpace(identityRef) != "" {
		body["identity_ref"] = identityRef
	} else {
		body["mint"] = true
	}
	code, raw := b.do(ctx, tenant, token, http.MethodPost, "/v1/m/governance/agents/"+url.PathEscape(agentID)+"/identity", body)
	if code != http.StatusOK {
		return degraded("Identity binding did not succeed; per-agent attribution degraded")
	}
	var resp struct {
		IdentityID  string `json:"identity_id"`
		IdentityRef string `json:"identity_ref"`
		Shared      bool   `json:"shared"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return degraded("Identity binding response malformed; per-agent attribution degraded")
	}
	ref := resp.IdentityRef
	if ref == "" {
		ref = resp.IdentityID
	}
	if resp.Shared || ref == "" {
		// Bound, but the identity is shared across agents — per-agent attribution is
		// collapsed; surface it as degraded (honest), never faked firm.
		return deploy.BoundIdentity{IdentityRef: ref, Firm: false, Reason: "identity is shared across agents; per-agent attribution degraded"}, nil
	}
	return deploy.BoundIdentity{
		IdentityRef: ref,
		Firm:        true, //verifier-truth:allow HTTP 200 plus a non-shared, non-empty identity checked above
		Reason:      "per-agent NHI bound via",
	}, nil
}

// resolveAgentID maps the deployment's subject ref (an agent name / external id) to
// the core Agent id the binding endpoint keys on, by listing the tenant's agents
// and matching on external id or name. A miss returns found=false (degraded).
func (b *deployIdentityBinder) resolveAgentID(ctx context.Context, tenant model.TenantID, token, agentRef string) (string, bool) {
	ref := strings.TrimSpace(agentRef)
	if ref == "" {
		return "", false
	}
	cursor := ""
	for i := 0; i < 100; i++ { // bounded paging
		path := "/v1/agents?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		code, raw := b.do(ctx, tenant, token, http.MethodGet, path, nil)
		if code != http.StatusOK {
			return "", false
		}
		var resp struct {
			Items []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				ExternalID string `json:"external_id"`
			} `json:"items"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", false
		}
		for _, it := range resp.Items {
			if it.ExternalID == ref || it.Name == ref {
				return it.ID, true
			}
		}
		if !resp.HasMore || resp.Cursor == "" {
			return "", false
		}
		cursor = resp.Cursor
	}
	return "", false
}

// do performs one in-process governed API call as the tenant's service principal,
// over the engine's own handler (the same mechanism as hitl.go / approvalbridge.go).
// A nil handler returns 0 (deny/degraded).
func (b *deployIdentityBinder) do(ctx context.Context, tenant model.TenantID, token, method, path string, body any) (int, []byte) {
	h := b.currentHandler()
	if h == nil {
		return 0, nil
	}
	rdr := strings.NewReader("")
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil
		}
		rdr = strings.NewReader(string(bs))
	}
	req, err := http.NewRequestWithContext(loopbackContext(ctx), method, path, rdr)
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	return rec.status, rec.body.Bytes()
}

// warnUnconfigured emits the "no binding credential" warning once per tenant.
func (b *deployIdentityBinder) warnUnconfigured(tenant model.TenantID) {
	if _, loaded := b.warned.LoadOrStore(tenant, struct{}{}); loaded {
		return
	}
	b.log.Warn("deploy-identity: no identity-binding credential for this tenant; per-agent attribution will be DEGRADED — add it to OLIVARES_DEPLOY_EXECUTOR_CONFIG to make the PERMITTED side firm", "tenant", tenant.String())
}
