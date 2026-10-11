// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/internal/loopback"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/catalog"
)

// catalogDeploy forwards to the one deployment engine with the original caller's
// credentials. It never borrows a service principal or calls an arbitrary URL.
type catalogDeploy struct{ handler http.Handler }

func (b *catalogDeploy) activate(r *http.Request, mc api.ModuleContext, in catalog.ActivationRequest) (int, json.RawMessage) {
	if b.handler == nil {
		return http.StatusServiceUnavailable, json.RawMessage(`{"error":"catalog deployment path is not configured"}`)
	}
	id := strings.TrimPrefix(in.TargetRef, "deployment:")
	parsed, err := model.ParseID(id)
	if err != nil {
		return http.StatusConflict, json.RawMessage(`{"error":"target_ref must name a deployment definition: deployment:<id>"}`)
	}
	id = parsed.String()
	subject := "agent"
	switch in.EntryKind {
	case "agent":
	case "mcp":
		subject = "mcp_server"
	default:
		return http.StatusConflict, json.RawMessage(`{"error":"catalog provisioning supports agent and MCP deployment specs"}`)
	}
	body, err := json.Marshal(map[string]any{"approval_ref": in.ApprovalRef, "catalog_source": map[string]any{"source_ref": "catalog entry " + in.EntryID, "subject_kind": subject, "spec": in.Spec}})
	if err != nil {
		return http.StatusBadGateway, json.RawMessage(`{"error":"cannot encode catalog deployment"}`)
	}
	req, err := http.NewRequestWithContext(loopback.Context(r.Context()), http.MethodPost, "/v1/m/deploy/definitions/"+id+"/apply", bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, json.RawMessage(`{"error":"cannot create catalog deployment request"}`)
	}
	req.Header = r.Header.Clone()
	req.Header.Set("X-Olivares-Tenant", mc.Tenant.String())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("Content-Length")
	req.RemoteAddr = r.RemoteAddr
	req.Host, req.TLS = r.Host, r.TLS
	rec := loopback.NewRecorder()
	b.handler.ServeHTTP(rec, req)
	return rec.Status, rec.Body.Bytes()
}
