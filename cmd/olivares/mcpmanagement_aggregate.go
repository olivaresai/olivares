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
)

// One session endpoint names tools deterministically. The original name is
// preserved at the upstream and in its server's tool policy and effect journal.
func managedToolAlias(serverID, name string) string {
	prefix := "mcp_" + strings.ReplaceAll(serverID, "-", "") + "_"
	if len(name) <= 91 {
		return prefix + name
	}
	hash := sha256.Sum256([]byte(name))
	return prefix + name[:74] + "_" + hex.EncodeToString(hash[:8])
}

func (m *mcpManagement) aggregateSessionTools(ctx context.Context, p auth.Principal, tenant model.TenantID) ([]mcpc.Tool, error) {
	tools := []mcpc.Tool{}
	if p.IsCommunicationSessionCredential() {
		return tools, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	companion, err := m.eng.sessionsMod.RuntimeCompanion(ctx, tenant, p)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot, err := m.store.Get(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, row := range snapshot.Servers {
		if !row.Enabled {
			continue
		}
		entry, err := m.cachedSessionServer(ctx, tenant, p, companion, snapshot.Version, row)
		if err != nil {
			continue
		} // one unavailable integration does not hide the others
		catalogue, err := listManagedTools(ctx, entry.upstream)
		if err != nil || !managedCatalogueMatches(row.Probe, catalogue) {
			continue
		}
		allowed := map[string]bool{}
		for _, policy := range row.AllowedTools {
			if policy.RequiredScope == "" || policy.RequiredScope == "tools:call" {
				allowed[policy.Name] = true
			}
		}
		for _, tool := range catalogue {
			if allowed[tool.Name] {
				tool.Name = managedToolAlias(row.ID, tool.Name)
				tool.Title = row.Name + ": " + tool.Title
				if entry.stdio != nil {
					tool = withManagedConfinement(tool, entry.stdio.confinement())
				}
				tools = append(tools, tool)
			}
		}
	}
	return tools, nil
}

func withManagedConfinement(tool mcpc.Tool, state map[string]any) mcpc.Tool {
	meta := map[string]json.RawMessage{}
	_ = json.Unmarshal(tool.Meta, &meta)
	if meta == nil {
		meta = map[string]json.RawMessage{}
	}
	meta["olivares.ai/confinement"], _ = json.Marshal(state)
	tool.Meta, _ = json.Marshal(meta)
	return tool
}

func (m *mcpManagement) aggregateSessionCall(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID, in sessionMCPRequest) bool {
	var params map[string]json.RawMessage
	// Apply the connector's duplicate-rejecting parser before rewriting the name.
	wrapped := append([]byte(`{"jsonrpc":"2.0","id":1,"result":`), in.Params...)
	wrapped = append(wrapped, '}')
	if _, _, err := mcpc.ParseStrictJSONRPCResponse(wrapped, 1); err != nil || json.Unmarshal(in.Params, &params) != nil {
		sessionRPCError(w, in.ID, -32602, "Invalid tools/call parameters")
		return true
	}
	var name string
	if json.Unmarshal(params["name"], &name) != nil || !strings.HasPrefix(name, "mcp_") {
		return false
	}
	snapshot, err := m.store.Get(r.Context(), tenant)
	if err != nil {
		sessionRPCError(w, in.ID, -32001, "Server configuration unavailable")
		return true
	}
	for _, row := range snapshot.Servers {
		if !row.Enabled {
			continue
		}
		for _, tool := range row.Probe.Tools {
			if name != managedToolAlias(row.ID, tool.Name) {
				continue
			}
			params["name"], _ = json.Marshal(tool.Name)
			in.Params, _ = json.Marshal(params)
			copy := r.Clone(r.Context())
			target := *r.URL
			query := target.Query()
			query.Set("server", row.ID)
			target.RawQuery = query.Encode()
			copy.URL = &target
			m.serveManagedSession(w, copy, p, tenant, in)
			return true
		}
	}
	sessionRPCError(w, in.ID, -32602, "Tool unavailable; inspect tools/list")
	return true
}
