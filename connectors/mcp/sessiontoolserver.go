// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// SessionToolIdentity is authority already authenticated and fenced by the
// hosting session runtime. This seam does not accept a bearer or OAuth bypass.
type SessionToolIdentity struct {
	Subject, ClientID string
	Scopes            []string
}

type SessionToolServerConfig struct {
	Tenant, ServerID, Descriptor string
	Toolset                      *Toolset
	Upstream                     Upstream
	Gate                         ApprovalGate
	Auditor                      GateAuditor
}

// SessionToolServer applies the SAME tool policy, approval and durable effect
// journal as the external OAuth resource server to an internal exact session.
// It is deliberately not an http.Handler; authentication belongs to its host.
type SessionToolServer struct{ server *ResourceServer }

func NewSessionToolServer(cfg SessionToolServerConfig) (*SessionToolServer, error) {
	if cfg.Tenant == "" || cfg.ServerID == "" || cfg.Descriptor == "" || cfg.Upstream == nil || cfg.Auditor == nil {
		return nil, errors.New("mcp: session tool server requires tenant, backend and evidence")
	}
	instance, err := newInstanceID()
	if err != nil {
		return nil, err
	}
	apps, err := newAppSet(nil)
	if err != nil {
		return nil, err
	}
	rs := &ResourceServer{
		resource: "urn:olivares:session-mcp:" + cfg.ServerID, tenant: cfg.Tenant,
		upstreamDescriptor: cfg.Descriptor, toolset: cfg.Toolset, upstream: cfg.Upstream,
		gate: cfg.Gate, auditor: cfg.Auditor, now: time.Now, taskLedger: newTaskLedger(0, time.Now),
		revisionMode: revisionModeLegacy, upstreamRevision: "2025-11-25", instanceID: instance,
		apps: apps, consent: denyConsentStore{},
	}
	if rs.gate == nil {
		rs.gate = denyApprovalGate{}
	}
	return &SessionToolServer{server: rs}, nil
}

func (s *SessionToolServer) CallTool(w http.ResponseWriter, r *http.Request, identity SessionToolIdentity, id, params json.RawMessage) {
	if identity.Subject == "" || identity.ClientID == "" {
		s.server.writeRPCError(w, http.StatusForbidden, id, rpcAccessDenied, "exact session required")
		return
	}
	token := validatedToken{Subject: identity.Subject, ClientID: identity.ClientID, Scopes: map[string]struct{}{}, Roles: map[string]struct{}{}, Issuer: "session-runtime", Audience: []string{s.server.resource}, TokenType: "opaque", Binding: "bearer"}
	for _, scope := range identity.Scopes {
		token.Scopes[scope] = struct{}{}
	}
	s.server.handleToolsCall(r.Context(), w, r, rsRequest{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: params}, token)
}
