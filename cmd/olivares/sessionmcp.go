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
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	sessionMCPRevision     = "2025-11-25"
	maxSessionPeerKeyChars = 256
)

var sessionWorkCommands = map[string]bool{
	"item.create": true, "item.update": true, "item.assign": true, "item.ready": true,
	"item.block": true, "item.unblock": true, "item.submit": true, "item.complete": true,
	"item.fail": true, "item.cancel": true, "dependency.add": true, "dependency.remove": true,
	"acceptance.add": true, "acceptance.update": true, "acceptance.evaluate": true,
	"decision.set": true, "decision.supersede": true,
	"lease.acquire": true, "lease.renew": true, "lease.release": true,
}

// This endpoint is the product's own session surface, separate from the external
// OAuth resource server. It uses the resolved session principal at in-process
// module ports; the session bearer never reaches the general REST authenticator.
type sessionMCPAuthenticator interface {
	Authenticate(context.Context, string) (auth.Principal, error)
}

type sessionMCPHandler struct {
	enabled            func(context.Context, model.TenantID) (bool, error)
	authr              sessionMCPAuthenticator
	issuedSessionOnly  bool
	work               func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID)
	checkOrchestration func(context.Context, auth.Principal, model.TenantID) error
	managed            func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID, sessionMCPRequest)
	managedTools       func(context.Context, auth.Principal, model.TenantID) ([]mcpc.Tool, error)
	managedCall        func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID, sessionMCPRequest) bool
}

type sessionMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (h *sessionMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not admitted", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.authr == nil {
		http.Error(w, "session tools unavailable", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="olivares-session"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p, err := h.authr.Authenticate(r.Context(), parts[1])
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !h.issuedSessionOnly && !p.IsWorkSessionCredential() && !p.IsCommunicationSessionCredential() && !p.IsOrchestrationSessionCredential() {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	tenants := p.Tenants()
	if len(tenants) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	tenant := tenants[0]
	if h.enabled != nil {
		enabled, err := h.enabled(r.Context(), tenant)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !enabled {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	if p.IsOrchestrationSessionCredential() {
		if h.checkOrchestration == nil || h.checkOrchestration(r.Context(), p, tenant) != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if revision := r.Header.Get("MCP-Protocol-Version"); revision != "" && revision != sessionMCPRevision {
		http.Error(w, "unsupported session MCP revision", http.StatusBadRequest)
		return
	}
	var in sessionMCPRequest
	if err := api.DecodeRequestBody(w, r, &in, api.RequestBodySpec{}); err != nil || in.JSONRPC != "2.0" || in.Method == "" || !validSessionRPCID(in.ID) {
		sessionRPCError(w, nil, -32600, "Invalid JSON-RPC request")
		return
	}
	if len(in.ID) == 0 {
		if in.Method != "notifications/initialized" && in.Method != "notifications/cancelled" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if r.URL.Query().Has("server") {
		if h.managed == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.managed(w, r, p, tenant, in)
		return
	}
	switch in.Method {
	case "initialize":
		sessionRPCResult(w, in.ID, map[string]any{"protocolVersion": sessionMCPRevision, "serverInfo": map[string]any{"name": "olivares-session-tools", "version": "26.10"}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Use this session's credential. Tools are filtered by each server's policy and retain approval and audit. Local servers run in this session's folder with its runner. Their actual process confinement is reported with the tools; MCP does not restrict network egress."})
	case "ping":
		sessionRPCResult(w, in.ID, map[string]any{})
	case "tools/list":
		tools := sessionMCPTools(p, h.issuedSessionOnly)
		if h.managedTools != nil {
			extra, err := h.managedTools(r.Context(), p, tenant)
			if err != nil {
				sessionRPCError(w, in.ID, -32001, "Session tools unavailable")
				return
			}
			tools = append(tools, extra...)
		}
		sessionRPCListTools(w, in.ID, tools)
	case "tools/call":
		if h.managedCall != nil && h.managedCall(w, r, p, tenant, in) {
			return
		}
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if err := strictSessionJSON(bytes.NewReader(in.Params), &call); err != nil {
			sessionRPCError(w, in.ID, -32602, "Provide name and arguments from tools/list")
			return
		}
		allowed := false
		for _, tool := range sessionMCPTools(p, h.issuedSessionOnly) {
			if tool.Name == call.Name {
				allowed = true
				break
			}
		}
		if !allowed {
			sessionRPCError(w, in.ID, -32602, "Tool unavailable for this session purpose or grant; inspect tools/list")
			return
		}
		request, err := sessionToolRequest(r.Context(), p, call.Name, call.Arguments)
		if err != nil {
			sessionRPCError(w, in.ID, -32602, err.Error())
			return
		}
		if h.work == nil {
			sessionRPCError(w, in.ID, -32001, "Session work tools unavailable")
			return
		}
		response := &sessionAPIResponse{header: make(http.Header)}
		h.work(response, request, p, tenant)
		if response.overflow {
			sessionToolResult(w, in.ID, http.StatusBadGateway, []byte(`{"code":"response_too_large","message":"Use a smaller page limit"}`), nil)
			return
		}
		raw := bytes.ReplaceAll(response.body.Bytes(), []byte(parts[1]), []byte("[redacted]"))
		sessionToolResult(w, in.ID, response.status, raw, response.header)
	default:
		sessionRPCError(w, in.ID, -32601, "Method not found")
	}
}

func strictSessionJSON(r io.Reader, target any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
func validSessionRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var id any
	if json.Unmarshal(raw, &id) != nil {
		return false
	}
	switch id.(type) {
	case string, float64:
		return true
	}
	return false
}
func sessionRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// Omit absent optional MCP fields at the HTTP edge. The connector's Tool
// encoding also fingerprints persisted probes, so changing its tags would make
// previously tested, unchanged servers disappear until tested again.
func sessionRPCListTools(w http.ResponseWriter, id json.RawMessage, tools []mcpc.Tool) {
	wire := make([]map[string]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil {
			sessionRPCError(w, id, -32603, "Invalid tool catalogue")
			return
		}
		for _, field := range []string{"annotations", "outputSchema", "_meta", "icons"} {
			if bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
				delete(fields, field)
			}
		}
		if annotations, present := fields["annotations"]; present {
			var hints map[string]json.RawMessage
			if json.Unmarshal(annotations, &hints) != nil {
				sessionRPCError(w, id, -32603, "Invalid tool annotations")
				return
			}
			for _, field := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
				if bytes.Equal(bytes.TrimSpace(hints[field]), []byte("null")) {
					delete(hints, field)
				}
			}
			fields["annotations"], _ = json.Marshal(hints)
		}
		wire = append(wire, fields)
	}
	sessionRPCResult(w, id, map[string]any{"tools": wire})
}

func sessionRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}
func sessionToolResult(w http.ResponseWriter, id json.RawMessage, status int, raw []byte, headers http.Header) {
	if status == 0 {
		status = http.StatusOK
	}
	var body any
	if json.Unmarshal(raw, &body) != nil {
		status = http.StatusBadGateway
		body = map[string]any{"code": "invalid_api_response"}
	}
	out := map[string]any{"http_status": status, "body": body}
	for _, name := range []string{"ETag", "Idempotency-Replayed"} {
		if value := headers.Get(name); value != "" {
			out[strings.ToLower(name)] = value
		}
	}
	text, _ := json.Marshal(out)
	sessionRPCResult(w, id, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": out, "isError": status >= 400})
}

type sessionAPIResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *sessionAPIResponse) Header() http.Header { return w.header }
func (w *sessionAPIResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *sessionAPIResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(b) > 1<<20 {
		w.overflow = true
		return len(b), nil
	}
	return w.body.Write(b)
}

type sessionWorkListArgs struct {
	Kind    string            `json:"kind,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Cursor  string            `json:"cursor,omitempty"`
	Filters map[string]string `json:"filters,omitempty"`
}
type sessionGetArgs struct {
	Kind string   `json:"kind,omitempty"`
	ID   model.ID `json:"id"`
}
type sessionWorkCommandArgs struct {
	Mode           string               `json:"mode"`
	Command        sessions.WorkCommand `json:"command"`
	Version        int64                `json:"version,omitempty"`
	IdempotencyKey string               `json:"idempotency_key,omitempty"`
	PlanHash       string               `json:"if_plan_hash,omitempty"`
}
type sessionPeerSendArgs struct {
	ToSID          string `json:"to_sid"`
	Title          string `json:"title"`
	BriefMD        string `json:"brief_md"`
	Priority       string `json:"priority,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type sessionPeerInboxArgs struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// Schemas derive the public JSON fields of the work envelopes. The sessions
// module remains the source of command validation and domain invariants.
func sessionJSONSchema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return sessionJSONSchema(t.Elem())
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": sessionJSONSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": sessionJSONSchema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.PkgPath != "" || tag == "-" || tag == "" {
				continue
			}
			props[tag] = sessionJSONSchema(f.Type)
		}
		return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	default:
		return map[string]any{}
	}
}
func sessionTool(name, description string, input any, required []string, read bool) mcpc.Tool {
	schema := sessionJSONSchema(reflect.TypeOf(input))
	properties := schema["properties"].(map[string]any)
	if name == "olivares_work_command" {
		properties["mode"].(map[string]any)["enum"] = []string{"validate", "plan", "apply"}
		commands := make([]string, 0, len(sessionWorkCommands))
		for command := range sessionWorkCommands {
			commands = append(commands, command)
		}
		sort.Strings(commands)
		properties["command"].(map[string]any)["properties"].(map[string]any)["command"].(map[string]any)["enum"] = commands
	}
	if name == "olivares_peer_send" {
		properties["priority"].(map[string]any)["enum"] = []string{"p0", "p1", "p2", "p3"}
		properties["priority"].(map[string]any)["default"] = "p2"
		key := properties["idempotency_key"].(map[string]any)
		key["minLength"], key["maxLength"] = 1, maxSessionPeerKeyChars
	}
	if limit, ok := properties["limit"].(map[string]any); ok {
		limit["minimum"], limit["maximum"] = 1, 200
	}
	if version, ok := properties["version"].(map[string]any); ok {
		version["minimum"] = 1
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	raw, _ := json.Marshal(schema)
	destructive, idempotent, open := !read, read, false
	return mcpc.Tool{Name: name, Title: name, Description: description, InputSchema: raw, Annotations: &mcpc.ToolAnnotations{ReadOnlyHint: &read, DestructiveHint: &destructive, IdempotentHint: &idempotent, OpenWorldHint: &open}}
}
func sessionMCPTools(p auth.Principal, issued bool) []mcpc.Tool {
	tools := []mcpc.Tool{}
	role, _ := p.RoleIn(p.SessionScope())
	writable := p.IsWorkSessionCredential() || (issued && auth.RoleRank(role) >= auth.RoleRank(auth.RoleEditor))
	if binding, ok := p.OrchestrationSessionGrant(); ok {
		for _, cap := range binding.Capabilities {
			if cap == "work.create" || cap == "work.assign" || cap == "work.review" || cap == "decision.write" {
				writable = true
			}
		}
	}
	if issued || p.IsWorkSessionCredential() || p.IsOrchestrationSessionCredential() {
		if writable {
			tools = append(tools, sessionTool("olivares_work_command", "Validate, plan or apply one governed work command. Use command.command (item.create/assign/ready/submit/complete, lease.acquire/renew/release, acceptance.evaluate, decision.set). Other fields follow the REST WorkCommand. Apply requires a stable idempotency_key and current version except item.create. On 412 refresh the version; on 403 ask the operator to review the grant; never retry with broader authority.", sessionWorkCommandArgs{}, []string{"mode", "command"}, false))
		}
		readable := issued
		if binding, ok := p.OrchestrationSessionGrant(); ok {
			for _, cap := range binding.Capabilities {
				readable = readable || cap == "work.read" || cap == "decision.read"
			}
		}
		if readable {
			tools = append(tools, sessionTool("olivares_work_list", "Read this workspace's work or decisions (kind work or decision); limit 1..200 and cursor paginate. Read capability is checked separately for each kind.", sessionWorkListArgs{}, nil, true), sessionTool("olivares_work_get", "Read a work snapshot or decision by UUID (kind work or decision). Returns current version and acceptance/dependencies. Cross-workspace rows stay concealed.", sessionGetArgs{}, []string{"id"}, true))
		}
		if issued {
			if writable {
				tools = append(tools, sessionTool("olivares_peer_send", "Send a message or work request to an allowed live peer using its canonical osn_ session ID. Creates a draft work item owned by that peer; the sender and workspace are supplied by the server. Reuse a stable idempotency_key of 1 to 256 characters for retries. The peer can ready, lease and complete it with olivares_work_command and reply with this tool. Permission refusals are audited.", sessionPeerSendArgs{}, []string{"to_sid", "title", "brief_md", "idempotency_key"}, false))
			}
			tools = append(tools, sessionTool("olivares_peer_inbox", "Check between tasks for work and messages addressed to this session. The server supplies the recipient; limit 1..200 and cursor paginate. Draft items can be read, readied, leased and completed with the work tools when your grant permits it.", sessionPeerInboxArgs{}, nil, true))
		}
	}
	return tools
}

func sessionToolRequest(ctx context.Context, p auth.Principal, name string, raw json.RawMessage) (*http.Request, error) {
	method, path, query := http.MethodGet, "", url.Values{}
	var body any
	headers := http.Header{}
	decode := func(v any) error {
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		if strictSessionJSON(bytes.NewReader(raw), v) != nil {
			return errors.New("Invalid arguments; use the schema from tools/list")
		}
		return nil
	}
	switch name {
	case "olivares_work_list", "olivares_peer_inbox":
		var args sessionWorkListArgs
		if name == "olivares_peer_inbox" {
			var inbox sessionPeerInboxArgs
			if err := decode(&inbox); err != nil {
				return nil, err
			}
			if !validSessionPeerSID(p.SessionIdentity) {
				return nil, errors.New("This session has no canonical recipient")
			}
			args = sessionWorkListArgs{Limit: inbox.Limit, Cursor: inbox.Cursor, Filters: map[string]string{"owner_kind": "session", "owner_ref": p.SessionIdentity}}
		} else if err := decode(&args); err != nil {
			return nil, err
		}
		path = "/work-items"
		if args.Kind == "decision" {
			path = "/decisions"
		} else if args.Kind != "" && args.Kind != "work" {
			return nil, errors.New("kind must be work or decision")
		}
		if args.Limit < 0 || args.Limit > 200 {
			return nil, errors.New("limit must be 1..200")
		}
		if args.Limit > 0 {
			query.Set("limit", strconv.Itoa(args.Limit))
		}
		if args.Cursor != "" {
			query.Set("cursor", args.Cursor)
		}
		for key, value := range args.Filters {
			if key == "limit" || key == "cursor" || key == "workspace_id" {
				return nil, errors.New("filters cannot override pagination or workspace")
			}
			query.Set(key, value)
		}
	case "olivares_peer_send":
		var args sessionPeerSendArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		workspace, confined := p.ConfinedWorkspaceIn(p.SessionScope())
		if !confined || workspace.IsZero() || !validSessionPeerSID(p.SessionIdentity) || !validSessionPeerSID(args.ToSID) {
			return nil, errors.New("to_sid must name a canonical session in this session's workspace")
		}
		if args.IdempotencyKey == "" || utf8.RuneCountInString(args.IdempotencyKey) > maxSessionPeerKeyChars {
			return nil, errors.New("Use a stable idempotency_key with 1 to 256 characters")
		}
		if args.Priority == "" {
			args.Priority = "p2"
		}
		method, path = http.MethodPost, "/work-items"
		query.Set("mode", "apply")
		// Keep work apply's UUID contract while isolating retries by the
		// authenticated sender. The domain and SID cannot contain a separator.
		key := []byte("olivares_peer_send\x00" + p.SessionIdentity + "\x00" + args.IdempotencyKey)
		headers.Set("Idempotency-Key", uuid.NewHash(sha256.New(), uuid.NameSpaceURL, key, 8).String())
		body = sessions.WorkCommand{
			Command: "item.create", WorkspaceID: workspace, WorkKind: "message", Title: args.Title, BriefMD: args.BriefMD, Priority: args.Priority,
			OwnerKind: "session", OwnerRef: args.ToSID, ProvenanceKind: "mcp", ProvenanceRef: p.SessionIdentity,
			Acceptance: []sessions.AcceptanceInput{{Key: "read-or-reply", Ordinal: 1, Statement: "Read the message and record the response or completed work.", Required: true}},
		}
	case "olivares_work_get":
		var args sessionGetArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) {
			return nil, errors.New("id must be a UUIDv7")
		}
		path = "/work-items/" + args.ID.String()
		if args.Kind == "decision" {
			path = "/decisions/" + args.ID.String()
		} else if args.Kind != "" && args.Kind != "work" {
			return nil, errors.New("kind must be work or decision")
		}
	case "olivares_work_command":
		var args sessionWorkCommandArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if args.Mode != "validate" && args.Mode != "plan" && args.Mode != "apply" {
			return nil, errors.New("mode must be validate, plan or apply")
		}
		command, _ := json.Marshal(args.Command)
		var doc map[string]any
		_ = json.Unmarshal(command, &doc)
		route, ok := workCommandRoutes[args.Command.Command]
		if !ok || !sessionWorkCommands[args.Command.Command] {
			return nil, errors.New("Unknown work command")
		}
		var err error
		path, err = route.path(doc)
		if err != nil {
			return nil, errors.New("Provide the UUID fields required by this work command")
		}
		method, body = route.method, args.Command
		query.Set("mode", args.Mode)
		if args.Mode == "apply" && args.IdempotencyKey == "" {
			return nil, errors.New("apply requires a stable idempotency_key")
		}
		if args.Version < 0 {
			return nil, errors.New("version must be positive")
		}
		if args.Version > 0 {
			headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		}
		headers.Set("Idempotency-Key", args.IdempotencyKey)
		headers.Set("If-Plan-Hash", args.PlanHash)
		// Lease REST routes accept a narrower body than WorkCommand. Keep it exact.
		if strings.HasPrefix(args.Command.Command, "lease.") {
			fields := map[string]bool{"command": true, "holder_sid": true, "holder_run_ref": true, "holder_agent_ref": true, "ttl_seconds": true, "fence": true, "force": true, "unblock": true, "changes_requested": true, "reason": true, "decision_id": true, "evidence_ref": true, "plan_hash": true}
			lease := map[string]any{}
			for key, value := range doc {
				if fields[key] {
					lease[key] = value
				} else if key != "work_item_id" {
					return nil, errors.New("Lease command contains unrelated work fields")
				}
			}
			body = lease
		}
	default:
		return nil, errors.New("Unknown session tool")
	}
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		encoded = bytes.NewReader(data)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, workAPIBase+path, encoded)
	if err != nil {
		return nil, err
	}
	r.Header = headers
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}
func validSessionToolID(id model.ID) bool {
	parsed, err := uuid.Parse(id.String())
	return err == nil && parsed.Version() == 7 && parsed.String() == id.String()
}

func validSessionPeerSID(sid string) bool {
	return strings.HasPrefix(sid, "osn_") && validSessionToolID(model.ID(strings.TrimPrefix(sid, "osn_")))
}
