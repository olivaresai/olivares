// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type managedSessionServer struct {
	version  int64
	context  context.Context
	cancel   context.CancelFunc
	stdio    *managedStdio
	upstream mcpc.Upstream
	server   *mcpc.SessionToolServer
	probe    auth.MCPGatewayProbe
}

func (s *managedSessionServer) close() {
	s.cancel()
	if s.stdio != nil {
		s.stdio.Close()
	}
}

// A caller reaches this callback only after private-purpose authentication and
// orchestration grant checking in sessionMCPHandler. It cannot choose a tenant,
// folder, runner, scope grant or the identity written to the effect journal.
func (m *mcpManagement) serveManagedSession(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID, request sessionMCPRequest) {
	id := r.URL.Query().Get("server")
	parsed, err := model.ParseID(id)
	if err != nil || parsed.IsZero() || parsed.String() != id || m.eng.sessionsMod == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	companion, err := m.eng.sessionsMod.RuntimeCompanion(r.Context(), tenant, p)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	m.mu.RLock()
	snapshot, err := m.store.Get(r.Context(), tenant)
	if err != nil {
		m.mu.RUnlock()
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
	if row == nil {
		m.mu.RUnlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entry, err := m.cachedSessionServer(r.Context(), tenant, p, companion, snapshot.Version, *row)
	m.mu.RUnlock()
	if err != nil {
		sessionRPCError(w, request.ID, -32001, "Server could not start; test it and check its command, environment or URL")
		return
	}
	stop := context.AfterFunc(entry.context, cancel)
	defer stop()
	switch request.Method {
	case "initialize":
		sessionRPCResult(w, request.ID, map[string]any{"protocolVersion": sessionMCPRevision, "serverInfo": map[string]any{"name": row.Name, "version": "26.10"}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Tools use this exact session's policy, approval and audit. Local commands run on the engine node with the session runner and folder. Process confinement is reported by that runner; no additional network sandbox is supplied by MCP."})
	case "ping":
		sessionRPCResult(w, request.ID, map[string]any{})
	case "tools/list":
		tools, err := listManagedTools(ctx, entry.upstream)
		if err != nil || !managedCatalogueMatches(entry.probe, tools) {
			sessionRPCError(w, request.ID, -32001, "Tool catalogue changed or is unavailable; test the server again")
			return
		}
		allowed := map[string]bool{}
		for _, policy := range row.AllowedTools {
			if policy.RequiredScope == "" || policy.RequiredScope == "tools:call" {
				allowed[policy.Name] = true
			}
		}
		filtered := []mcpc.Tool{}
		for _, tool := range tools {
			if allowed[tool.Name] {
				if entry.stdio != nil {
					tool = withManagedConfinement(tool, entry.stdio.confinement())
				}
				filtered = append(filtered, tool)
			}
		}
		sessionRPCListTools(w, request.ID, filtered)
	case "tools/call":
		callCtx, endCall, err := m.eng.sessionsMod.BeginSessionCall(ctx, p)
		if err != nil {
			sessionRPCError(w, request.ID, -31001, "The session turn has ended")
			return
		}
		defer endCall()
		entry.server.CallTool(w, r.WithContext(callCtx), mcpc.SessionToolIdentity{Subject: p.SessionIdentity, ClientID: companion.LaunchID.String(), Scopes: []string{"tools:call"}}, request.ID, request.Params)
	default:
		sessionRPCError(w, request.ID, -32601, "Method not supported by session tools")
	}
}

func (m *mcpManagement) cachedSessionServer(ctx context.Context, tenant model.TenantID, p auth.Principal, companion sessions.RuntimeCompanion, version int64, row auth.MCPGatewayServer) (_ *managedSessionServer, resultErr error) {
	defer func() {
		if row.Transport == "stdio" && resultErr != nil && companion.Context.Err() == nil {
			m.warnStdioFailure(row.ID, resultErr)
		}
	}()
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	key := tenant.String() + "/" + p.SessionRunRef + "/" + companion.LaunchID.String() + "/" + row.ID
	if entry, ok := m.sessionCache[key]; ok && entry.version == version && entry.context.Err() == nil && (entry.stdio == nil || entry.stdio.context.Err() == nil) {
		return entry, nil
	}
	if old := m.sessionCache[key]; old != nil {
		old.close()
		delete(m.sessionCache, key)
	}
	if len(m.sessionCache) >= 128 {
		return nil, auth.ErrMCPGatewayUnavailable
	}
	lifetime, cancel := context.WithCancel(companion.Context)
	entry := &managedSessionServer{version: version, context: lifetime, cancel: cancel, probe: row.Probe}
	failed := true
	defer func() {
		if failed {
			entry.close()
		}
	}()
	setup, stopSetup := context.WithTimeout(ctx, 10*time.Second)
	stopLifetime := context.AfterFunc(lifetime, stopSetup)
	defer stopSetup()
	defer stopLifetime()
	if row.Transport == "stdio" {
		env, patterns, err := m.localEnvironment(setup, tenant, row)
		if err != nil {
			return nil, err
		}
		spec := companion.LaunchSpec
		spec.Program, spec.Args = row.Command, row.Args
		spec.Env = env
		if err := m.confineLocalServer(&spec); err != nil {
			return nil, newManagedStdioFailure("process_start", err, patterns)
		}
		client, err := launchManagedStdio(lifetime, companion.Runner, spec, patterns)
		if err != nil {
			return nil, err
		}
		entry.stdio, entry.upstream = client, client
		if err := client.Initialize(setup); err != nil {
			return nil, err
		}
	} else {
		client, err := newManagedMCPClient(row.MCPGatewayServerInput, 60*time.Second)
		if err != nil {
			return nil, err
		}
		up := &mcpUpstreamForwarder{
			url: row.URL, client: client,
			credProv:        tenantMCPCredentialProvider{store: m.secrets, tenant: tenant, ref: row.CredentialRef, target: row.URL},
			sessionIdentity: &mcpc.SessionToolIdentity{Subject: p.SessionIdentity, ClientID: companion.LaunchID.String(), Scopes: []string{"tools:call"}},
		}
		enableManagedMCPForwarding(up)
		if _, err := up.Forward(setup, mcpc.UpstreamRequest{Method: "initialize", Params: []byte(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"olivares-session","version":"1"}}`)}); err != nil {
			return nil, err
		}
		if _, err := up.Forward(setup, mcpc.UpstreamRequest{Method: "notifications/initialized", Params: []byte(`{}`)}); err != nil {
			return nil, err
		}
		entry.upstream = up
	}
	tools, err := listManagedTools(setup, entry.upstream)
	if err != nil {
		return nil, err
	}
	if !managedCatalogueMatches(row.Probe, tools) {
		return nil, errManagedMCPInvalidResponse
	}
	policies := []mcpc.ToolPolicy{}
	for _, policy := range row.AllowedTools {
		policies = append(policies, mcpc.ToolPolicy{Name: policy.Name, RequiredScope: policy.RequiredScope, Destructive: policy.Destructive})
	}
	toolset, err := mcpc.NewToolset(policies)
	if err != nil {
		return nil, err
	}
	inner := entry.upstream
	guarded := managedSessionUpstream{inner: inner, probe: row.Probe, check: func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if lifetime.Err() != nil {
			return auth.ErrMCPGatewayUnavailable
		}
		currentPrincipal := p
		if m.eng.sessionHooks != nil && m.eng.sessionHooks.SessionCredentials != nil {
			resolved, scope, err := m.eng.sessionHooks.ResolveRun(ctx, tenant, p.SessionRunRef)
			if err != nil || scope.SessionRef != p.SessionIdentity || resolved.SessionFence != p.SessionFence || resolved.SessionWorkspaceID != p.SessionWorkspaceID {
				return auth.ErrUnauthenticated
			}
			currentPrincipal = resolved
		}
		if _, err := m.eng.sessionsMod.RuntimeCompanion(ctx, tenant, currentPrincipal); err != nil {
			return err
		}
		current, err := m.store.Get(ctx, tenant)
		if err != nil || current.Version != version {
			return auth.ErrMCPGatewayUnavailable
		}
		return nil
	}}
	var gate mcpc.ApprovalGate
	if m.eng.engineApprovals != nil && m.eng.sessionHooks != nil && m.eng.sessionHooks.SessionCredentials != nil {
		gate = managedSessionApprovalGate{m: m, tenant: tenant, principal: p, serverID: row.ID, serverName: row.Name, check: guarded.check}
	}
	input, _ := json.Marshal(row.MCPGatewayServerInput)
	digest := sha256.Sum256(input)
	server, err := mcpc.NewSessionToolServer(mcpc.SessionToolServerConfig{Tenant: tenant.String(), ServerID: row.ID, Descriptor: "managed-session:" + hex.EncodeToString(digest[:]), Toolset: toolset, Upstream: guarded, Gate: gate, Auditor: mcpGateAuditor{log: m.eng.log, store: m.eng.store, tenant: tenant}})
	if err != nil {
		return nil, err
	}
	entry.server = server
	if m.sessionCache == nil {
		m.sessionCache = map[string]*managedSessionServer{}
	}
	m.sessionCache[key] = entry
	context.AfterFunc(lifetime, func() {
		entry.close()
		m.cacheMu.Lock()
		defer m.cacheMu.Unlock()
		if m.sessionCache[key] == entry {
			delete(m.sessionCache, key)
		}
	})
	failed = false
	return entry, nil
}

type managedSessionUpstream struct {
	inner mcpc.Upstream
	probe auth.MCPGatewayProbe
	check func(context.Context) error
}

func (u managedSessionUpstream) Forward(ctx context.Context, req mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	if err := u.check(ctx); err != nil {
		return mcpc.UpstreamResult{State: mcpc.DispatchBlocked}, auth.ErrMCPGatewayUnavailable
	}
	tools, err := listManagedTools(ctx, u.inner)
	if err != nil || !managedCatalogueMatches(u.probe, tools) {
		return mcpc.UpstreamResult{State: mcpc.DispatchBlocked}, errManagedMCPInvalidResponse
	}
	if err := u.check(ctx); err != nil {
		return mcpc.UpstreamResult{State: mcpc.DispatchBlocked}, auth.ErrMCPGatewayUnavailable
	}
	return u.inner.Forward(ctx, req)
}

func managedCatalogueMatches(probe auth.MCPGatewayProbe, tools []mcpc.Tool) bool {
	if probe.State != "ok" || len(probe.Tools) != len(tools) {
		return false
	}
	observed := managedMCPProbeTools(tools)
	allowed := map[string]string{}
	for _, tool := range probe.Tools {
		allowed[tool.Name] = tool.Fingerprint
	}
	for _, tool := range observed {
		if allowed[tool.Name] != tool.Fingerprint {
			return false
		}
	}
	return true
}

func (m *mcpManagement) invalidateSessions(tenant model.TenantID) {
	for key, entry := range m.sessionCache {
		if strings.HasPrefix(key, tenant.String()+"/") {
			entry.close()
			delete(m.sessionCache, key)
		}
	}
}
