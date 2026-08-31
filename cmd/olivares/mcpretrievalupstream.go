// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
)

// retrievalUpstream is an in-process mcp.Upstream that translates MCP tools/call
// for search_kb and fetch_document into the knowledge module's governed retrieval
// pipeline. It is wired by the composition root when the operator enables the
// retrieval MCP surface; it runs the EXACT same pipeline as the REST
// POST /kbs/{id}/query, with the SAME deny-closed governance (RBAC → grants →
// scope → residency → embed → classification+ACL+DLP → rank → lineage).
//
// Identity binding: the token's validated subject (UpstreamRequest.Subject) IS the
// agent_ref passed to the retrieval guard. The body's agent_ref is IGNORED — the
// authenticated token subject is the sole source of identity. This is
// anti-confused-deputy by construction (hardens it further).
type retrievalUpstream struct {
	mod    *knowledge.Module
	st     store.Store
	tenant model.TenantID
	role   string
	log    *slog.Logger
}

// retrievalUpstreamConfig is the construction parameters for the retrieval upstream.
type retrievalUpstreamConfig struct {
	Module *knowledge.Module
	Store  store.Store
	Tenant model.TenantID
	Role   string
	Log    *slog.Logger
}

func newRetrievalUpstream(cfg retrievalUpstreamConfig) (*retrievalUpstream, error) {
	if cfg.Module == nil {
		return nil, fmt.Errorf("mcp retrieval: knowledge module is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("mcp retrieval: store is required")
	}
	if cfg.Tenant.IsZero() {
		return nil, fmt.Errorf("mcp retrieval: tenant is required")
	}
	role := cfg.Role
	if role == "" {
		role = "viewer"
	}
	return &retrievalUpstream{
		mod:    cfg.Module,
		st:     cfg.Store,
		tenant: cfg.Tenant,
		role:   role,
		log:    cfg.Log,
	}, nil
}

// Forward dispatches an admitted MCP method to the in-process retrieval backend.
//
// Dispatch classification: this upstream executes READ-ONLY retrieval
// in-process — there is no transport between the gateway and the executor, so an
// error is a proven local failure with no external side effect (not_sent), never
// the ambiguous post-transmit shape a network forwarder must report (unknown); a
// returned result is a completed dispatch.
func (u *retrievalUpstream) Forward(ctx context.Context, req mcp.UpstreamRequest) (mcp.UpstreamResult, error) {
	var raw json.RawMessage
	var err error
	switch req.Method {
	case "tools/list":
		raw, err = u.handleToolsList()
	case "tools/call":
		raw, err = u.handleToolsCall(ctx, req)
	case "initialize":
		raw, err = u.handleInitialize()
	default:
		err = fmt.Errorf("mcp retrieval: unsupported method %q", req.Method)
	}
	if err != nil {
		return mcp.UpstreamResult{State: mcp.DispatchNotSent}, err
	}
	return mcp.UpstreamResult{Result: raw, State: mcp.DispatchCompleted}, nil
}

// handleInitialize returns a minimal server info response.
func (u *retrievalUpstream) handleInitialize() (json.RawMessage, error) {
	info := map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    "olivares-retrieval",
			"version": "0.1.0",
		},
	}
	return json.Marshal(info)
}

// retrievalToolDefs are the MCP tool definitions for the governed retrieval
// surface. Each tool's inputSchema follows the MCP Tool type.
var retrievalToolDefs = []map[string]any{
	{
		"name":        retrievalToolSearchKB,
		"description": "Search a governed knowledge base using semantic retrieval. Results are filtered by the caller's identity, classification clearance, ACL groups, data residency, and DLP policy. Every search is recorded in an append-only lineage ledger proving origin-to-answer and that the data stayed in the perimeter.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kb_id": map[string]any{
					"type":        "string",
					"description": "Knowledge base ID to search.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search query text (max 8192 characters).",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 10, max 100).",
				},
			},
			"required": []string{"kb_id", "query"},
		},
	},
	{
		"name":        retrievalToolFetchDocument,
		"description": "Fetch metadata and provenance of an ingested document. Returns source kind, title, classification, content hash, and chunk count. Never returns the raw document body.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"document_id": map[string]any{
					"type":        "string",
					"description": "Document ID to fetch.",
				},
			},
			"required": []string{"document_id"},
		},
	},
	{
		"name":        retrievalToolListKBs,
		"description": "List available knowledge bases with their names, classification levels, and status. Use this to discover which knowledge bases are available for search.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

const (
	retrievalToolSearchKB      = "search_kb"
	retrievalToolFetchDocument = "fetch_document"
	retrievalToolListKBs       = "list_kbs"
)

// handleToolsList returns the retrieval tool definitions.
func (u *retrievalUpstream) handleToolsList() (json.RawMessage, error) {
	result := map[string]any{"tools": retrievalToolDefs}
	return json.Marshal(result)
}

// handleToolsCall dispatches a tools/call to the appropriate retrieval function.
func (u *retrievalUpstream) handleToolsCall(ctx context.Context, req mcp.UpstreamRequest) (json.RawMessage, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		return toolErrorResult("invalid tools/call params"), nil
	}

	mc := u.moduleContext(req.Subject)

	switch call.Name {
	case retrievalToolSearchKB:
		return u.callSearchKB(ctx, mc, call.Arguments, req.Subject)
	case retrievalToolFetchDocument:
		return u.callFetchDocument(ctx, mc, call.Arguments)
	case retrievalToolListKBs:
		return u.callListKBs(ctx, mc)
	default:
		return nil, fmt.Errorf("mcp retrieval: unknown tool %q", call.Name)
	}
}

// moduleContext constructs an api.ModuleContext for the MCP caller. The principal
// is a synthetic ScopedPrincipal — it carries the tenant grant so RBAC passes,
// and the retrieval guard resolves the real identity from subject (= agent_ref).
// AgentIdentity is set from the validated token subject so the source-scope
// resolver and knowledge Query use the authenticated identity, not any body-declared
// agent_ref, closing the confused-deputy path.
func (u *retrievalUpstream) moduleContext(subject string) api.ModuleContext {
	principalID := model.ID(subject)
	p := auth.ScopedPrincipal(principalID, "mcp:"+subject, u.tenant, u.role).
		WithAgentIdentity(subject)
	return api.ModuleContext{
		Principal: p,
		Tenant:    u.tenant,
		Data:      api.NewScopedData(u.st, u.tenant),
	}
}

// callSearchKB invokes the governed retrieval pipeline.
func (u *retrievalUpstream) callSearchKB(ctx context.Context, mc api.ModuleContext, args json.RawMessage, subject string) (json.RawMessage, error) {
	var a struct {
		KBID  string `json:"kb_id"`
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return toolErrorResult("invalid search_kb arguments: " + err.Error()), nil
	}
	// F-03: the effective agent identity travels on the principal
	// (WithAgentIdentity(subject), set in moduleContext) — QueryRequest no
	// longer carries an agent_ref, so there is nothing body-declared to spoof.
	result, err := u.mod.Query(ctx, mc, knowledge.QueryRequest{
		KBID:  a.KBID,
		Query: a.Query,
		TopK:  a.TopK,
	})
	if err != nil {
		if qe, ok := knowledge.IsQueryError(err); ok {
			return toolErrorResult(qe.Message), nil
		}
		return toolErrorResult("retrieval failed"), nil
	}
	return toolSuccessResult(result)
}

// callFetchDocument invokes the governed document metadata fetch.
func (u *retrievalUpstream) callFetchDocument(ctx context.Context, mc api.ModuleContext, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return toolErrorResult("invalid fetch_document arguments: " + err.Error()), nil
	}
	result, err := u.mod.FetchDocument(ctx, mc, a.DocumentID)
	if err != nil {
		if qe, ok := knowledge.IsQueryError(err); ok {
			return toolErrorResult(qe.Message), nil
		}
		return toolErrorResult("document fetch failed"), nil
	}
	return toolSuccessResult(result)
}

// callListKBs lists the available knowledge bases.
func (u *retrievalUpstream) callListKBs(ctx context.Context, mc api.ModuleContext) (json.RawMessage, error) {
	result, err := u.mod.ListKBs(ctx, mc)
	if err != nil {
		if qe, ok := knowledge.IsQueryError(err); ok {
			return toolErrorResult(qe.Message), nil
		}
		return toolErrorResult("list knowledge bases failed"), nil
	}
	return toolSuccessResult(result)
}

// toolSuccessResult wraps a result value into an MCP tools/call success response.
func toolSuccessResult(v any) (json.RawMessage, error) {
	text, err := json.Marshal(v)
	if err != nil {
		return toolErrorResult("result serialization failed"), nil
	}
	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(text)},
		},
	}
	return json.Marshal(result)
}

// toolErrorResult wraps an error message into an MCP tools/call isError:true
// response (SEP-1303 Tool Execution Error — the model can self-correct).
func toolErrorResult(msg string) json.RawMessage {
	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": strings.TrimSpace(msg)},
		},
		"isError": true,
	}
	raw, _ := json.Marshal(result)
	return raw
}

// RetrievalToolPolicies returns the ToolPolicy entries for the retrieval tools,
// ready to merge into the RS's toolset. The scope is the OAuth scope a caller's
// token must carry (default "knowledge:retrieval:read"). None are destructive.
func RetrievalToolPolicies(scope string) []mcp.ToolPolicy {
	if scope == "" {
		scope = "knowledge:retrieval:read"
	}
	return []mcp.ToolPolicy{
		{Name: retrievalToolSearchKB, RequiredScope: scope},
		{Name: retrievalToolFetchDocument, RequiredScope: scope},
		{Name: retrievalToolListKBs, RequiredScope: scope},
	}
}
