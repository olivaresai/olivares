// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
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
	"time"

	"github.com/google/uuid"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

const sessionMCPRevision = "2025-11-25"

var sessionWorkCommands = map[string]bool{
	"item.create": true, "item.update": true, "item.assign": true, "item.ready": true,
	"item.block": true, "item.unblock": true, "item.submit": true, "item.complete": true,
	"item.fail": true, "item.cancel": true, "dependency.add": true, "dependency.remove": true,
	"acceptance.add": true, "acceptance.update": true, "acceptance.evaluate": true,
	"decision.set": true, "decision.supersede": true,
	"lease.acquire": true, "lease.renew": true, "lease.release": true,
}

// This endpoint is the product's own session surface, separate from the external
// OAuth resource server. It accepts only server-issued private session purposes.
// Its only backend is this engine's existing REST handler, under the SAME bearer.
type sessionMCPHandler struct {
	enabled            func(context.Context, model.TenantID) (bool, error)
	authr              *auth.Authenticator
	api                http.Handler
	checkOrchestration func(context.Context, auth.Principal, model.TenantID) error
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
	if h.authr == nil || h.api == nil {
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
	if !p.IsWorkSessionCredential() && !p.IsCommunicationSessionCredential() && !p.IsOrchestrationSessionCredential() {
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
	if err := strictSessionJSON(http.MaxBytesReader(w, r.Body, 1<<20), &in); err != nil || in.JSONRPC != "2.0" || in.Method == "" || !validSessionRPCID(in.ID) {
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
	switch in.Method {
	case "initialize":
		sessionRPCResult(w, in.ID, map[string]any{"protocolVersion": sessionMCPRevision, "serverInfo": map[string]any{"name": "olivares-session-tools", "version": "26.10"}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Use the work bearer for work tools and the communication bearer for exact-session messages. Commands retain REST versions, idempotency and Claim fencing. A denied session never falls back to operator authority."})
	case "ping":
		sessionRPCResult(w, in.ID, map[string]any{})
	case "tools/list":
		sessionRPCResult(w, in.ID, map[string]any{"tools": sessionMCPTools(p)})
	case "tools/call":
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
		for _, tool := range sessionMCPTools(p) {
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
		request.Header.Set("Authorization", "Bearer "+parts[1])
		request.Header.Set("X-Olivares-Tenant", tenant.String())
		// Preserve the transport peer used by the API's throttle and request evidence.
		request.RemoteAddr = r.RemoteAddr
		response := &sessionAPIResponse{header: make(http.Header)}
		h.api.ServeHTTP(response, request)
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
type sessionInboxArgs struct {
	Limit        int    `json:"limit,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}
type sessionSendArgs struct {
	ChannelID      model.ID `json:"channel_id"`
	ToSID          string   `json:"to_sid"`
	Subject        string   `json:"subject"`
	Text           string   `json:"text"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type sessionAckArgs struct {
	ID             model.ID `json:"id"`
	Version        int64    `json:"version"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type sessionHandoffInboxArgs struct {
	State        string `json:"state,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}
type sessionHandoffOfferArgs struct {
	ChannelID          model.ID                `json:"channel_id"`
	WorkItemID         model.ID                `json:"work_item_id"`
	ToSID              string                  `json:"to_sid"`
	Handoff            sessions.HandoffContent `json:"handoff"`
	AckDeadline        string                  `json:"ack_deadline"`
	ExpectedOwnerEpoch int64                   `json:"expected_owner_epoch"`
	Version            int64                   `json:"version"`
	IdempotencyKey     string                  `json:"idempotency_key"`
}
type sessionHandoffRespondArgs struct {
	ID             model.ID                             `json:"id"`
	Transition     sessions.HandoffTransition           `json:"transition"`
	Reason         *sessions.CommunicationReasonContent `json:"reason,omitempty"`
	Version        int64                                `json:"version"`
	IdempotencyKey string                               `json:"idempotency_key"`
}

// Schemas derive only the public JSON fields of these typed REST envelopes. The
// API remains the source of command-specific validation and domain invariants.
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
	if limit, ok := properties["limit"].(map[string]any); ok {
		limit["minimum"], limit["maximum"] = 1, 200
	}
	if version, ok := properties["version"].(map[string]any); ok {
		version["minimum"] = 1
	}
	if epoch, ok := properties["expected_owner_epoch"].(map[string]any); ok {
		epoch["minimum"] = 1
	}
	if deadline, ok := properties["ack_deadline"].(map[string]any); ok {
		deadline["format"] = "date-time"
	}
	if transition, ok := properties["transition"].(map[string]any); ok {
		transition["enum"] = []string{"accept", "reject"}
	}
	if state, ok := properties["state"].(map[string]any); ok {
		state["enum"] = []string{"offered", "accepted", "rejected", "withdrawn", "expired"}
	}
	if name == "olivares_session_ack" || name == "olivares_session_handoff_offer" || name == "olivares_session_handoff_respond" {
		properties["idempotency_key"].(map[string]any)["pattern"] = "^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	raw, _ := json.Marshal(schema)
	destructive, idempotent, open := !read, read, false
	return mcpc.Tool{Name: name, Title: name, Description: description, InputSchema: raw, Annotations: &mcpc.ToolAnnotations{ReadOnlyHint: &read, DestructiveHint: &destructive, IdempotentHint: &idempotent, OpenWorldHint: &open}}
}
func sessionMCPTools(p auth.Principal) []mcpc.Tool {
	tools := []mcpc.Tool{}
	writable := p.IsWorkSessionCredential()
	if binding, ok := p.OrchestrationSessionGrant(); ok {
		for _, cap := range binding.Capabilities {
			if cap == "work.create" || cap == "work.assign" || cap == "work.review" || cap == "decision.write" {
				writable = true
			}
		}
	}
	if p.IsWorkSessionCredential() || p.IsOrchestrationSessionCredential() {
		if writable {
			tools = append(tools, sessionTool("olivares_work_command", "Validate, plan or apply one governed work command. Use command.command (item.create/assign/ready/submit/complete, lease.acquire/renew/release, acceptance.evaluate, decision.set). Other fields follow the REST WorkCommand. Apply requires a stable idempotency_key and current version except item.create. Obtain assigned IDs/versions via messages if this worker cannot read backlog. On 412 refresh the version; on 403 ask the operator to review the grant; never retry with broader authority.", sessionWorkCommandArgs{}, []string{"mode", "command"}, false))
		}
		if binding, ok := p.OrchestrationSessionGrant(); ok {
			for _, cap := range binding.Capabilities {
				if cap == "work.read" || cap == "decision.read" {
					tools = append(tools, sessionTool("olivares_work_list", "Read this workspace's work or decisions (kind work or decision); limit 1..200 and cursor paginate. Filters use the existing REST names. Read capability is checked separately for each kind.", sessionWorkListArgs{}, nil, true), sessionTool("olivares_work_get", "Read a work snapshot or decision by UUID (kind work or decision). Returns current version and acceptance/dependencies. Cross-workspace rows stay concealed.", sessionGetArgs{}, []string{"id"}, true))
					break
				}
			}
		}
	}
	if p.IsCommunicationSessionCredential() {
		tools = append(tools,
			sessionTool("olivares_session_inbox", "Read only this authenticated session's inbox in its workspace. Limit and continuation paginate without acknowledging messages; retain delivery IDs and versions for ack.", sessionInboxArgs{}, nil, true),
			sessionTool("olivares_session_delivery", "Read one delivery owned by this exact session, including its message content. ID is a delivery UUID, not a message UUID.", sessionGetArgs{}, []string{"id"}, true),
			sessionTool("olivares_session_send", "Send subject and plain-text content to one exact canonical SID in the same workspace through an operator-authorized channel. Use to_sid (osn_UUID), channel_id UUID and a stable idempotency_key. Crossed workspace, stale Claim and missing channel grants deny.", sessionSendArgs{}, []string{"channel_id", "to_sid", "subject", "text", "idempotency_key"}, false),
			sessionTool("olivares_session_ack", "Acknowledge this exact session's delivery using its current version and a canonical UUIDv7 idempotency_key retained for exact retries. On 412 read the delivery again.", sessionAckArgs{}, []string{"id", "version", "idempotency_key"}, false),
			sessionTool("olivares_session_handoff_inbox", "List content-free handoff offers addressed to this exact session in its workspace. State is one of offered (default), accepted, rejected, withdrawn or expired. Read each carrier delivery through handoff_get before responding.", sessionHandoffInboxArgs{}, nil, true),
			sessionTool("olivares_session_handoff_get", "Read the protected handoff context for one carrier delivery owned by this exact session. ID is the delivery UUID, not the handoff UUID. The result supplies handoff id/version/etag and current work ownership; read before responding.", sessionGetArgs{}, []string{"id"}, true),
			sessionTool("olivares_session_handoff_offer", "Offer this session's current work ownership to one exact canonical SID through an authorized same-workspace channel. Supply current work version and expected_owner_epoch, handoff summary/next_action and a future RFC3339 ack_deadline. The UUIDv7 idempotency_key must be retained for exact retries. Offer leaves ownership unchanged until accepted; never substitute operator authority.", sessionHandoffOfferArgs{}, []string{"channel_id", "work_item_id", "to_sid", "handoff", "ack_deadline", "expected_owner_epoch", "version", "idempotency_key"}, false),
			sessionTool("olivares_session_handoff_respond", "Accept or reject a handoff addressed to this exact session. ID is the handoff UUID from protected handoff_get, version is its current handoff version. Reject requires reason.code; accept must omit reason. Use a UUIDv7 idempotency_key retained for retries. Accept atomically transfers ownership and fences the old lease; conflicts require rereading.", sessionHandoffRespondArgs{}, []string{"id", "transition", "version", "idempotency_key"}, false))
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
	case "olivares_work_list":
		var args sessionWorkListArgs
		if err := decode(&args); err != nil {
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
	case "olivares_work_get", "olivares_session_delivery", "olivares_session_handoff_get":
		var args sessionGetArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) {
			return nil, errors.New("id must be a UUIDv7")
		}
		path = "/work-items/" + args.ID.String()
		if name == "olivares_session_delivery" || name == "olivares_session_handoff_get" {
			if args.Kind != "" {
				return nil, errors.New("delivery does not accept kind")
			}
			path = "/deliveries/" + args.ID.String()
			if name == "olivares_session_handoff_get" {
				path += "/handoff"
			}
		} else if args.Kind == "decision" {
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
	case "olivares_session_inbox":
		var args sessionInboxArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		path = "/inbox"
		query.Set("workspace_id", p.SessionWorkspaceID.String())
		if args.Limit < 0 || args.Limit > 200 {
			return nil, errors.New("limit must be 1..200")
		}
		if args.Limit > 0 {
			query.Set("limit", strconv.Itoa(args.Limit))
		}
		if args.Continuation != "" {
			query.Set("continuation", args.Continuation)
		}
	case "olivares_session_send":
		var args sessionSendArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ChannelID) || !validSessionToolSID(args.ToSID) || args.Subject == "" || args.Text == "" || args.IdempotencyKey == "" {
			return nil, errors.New("Provide channel_id UUIDv7, exact to_sid, subject, text and stable idempotency_key")
		}
		method, path = http.MethodPost, "/messages/send"
		headers.Set("Idempotency-Key", args.IdempotencyKey)
		body = sessions.DirectNoticePublishCommand{ChannelID: args.ChannelID, Recipient: sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: args.ToSID}, Content: sessions.MessageContent{Subject: args.Subject, Blocks: []sessions.MessageContentBlock{{Type: sessions.ContentBlockText, Format: sessions.TextPlain, Text: args.Text}}}}
	case "olivares_session_ack":
		var args sessionAckArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) || args.Version < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) {
			return nil, errors.New("Provide delivery id, current version and canonical UUIDv7 idempotency_key")
		}
		method, path, body = http.MethodPost, "/deliveries/"+args.ID.String()+"/ack", map[string]any{}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
	case "olivares_session_handoff_inbox":
		var args sessionHandoffInboxArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if args.Limit < 0 || args.Limit > 200 {
			return nil, errors.New("limit must be 1..200")
		}
		if args.State == "" {
			args.State = "offered"
		}
		switch args.State {
		case "offered", "accepted", "rejected", "withdrawn", "expired":
		default:
			return nil, errors.New("Provide one handoff state from tools/list")
		}
		path = "/inbox/handoffs"
		query.Set("workspace_id", p.SessionWorkspaceID.String())
		query.Set("state", args.State)
		if args.Limit > 0 {
			query.Set("limit", strconv.Itoa(args.Limit))
		}
		if args.Continuation != "" {
			query.Set("continuation", args.Continuation)
		}
	case "olivares_session_handoff_offer":
		var args sessionHandoffOfferArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		deadline, err := time.Parse(time.RFC3339Nano, args.AckDeadline)
		if err != nil || !validSessionToolID(args.ChannelID) || !validSessionToolID(args.WorkItemID) || !validSessionToolSID(args.ToSID) || args.Version < 1 || args.ExpectedOwnerEpoch < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) {
			return nil, errors.New("Provide exact channel/work/SID, current work version/owner epoch, RFC3339 deadline and UUIDv7 idempotency_key")
		}
		method, path = http.MethodPost, "/handoffs"
		body = sessions.WorkItemHandoffOfferCommand{ChannelID: args.ChannelID, WorkItemID: args.WorkItemID, Recipient: sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: args.ToSID}, Content: args.Handoff, AckDeadline: deadline, ExpectedOwnerEpoch: args.ExpectedOwnerEpoch}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
	case "olivares_session_handoff_respond":
		var args sessionHandoffRespondArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) || args.Version < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) || (args.Transition != sessions.HandoffAccept && args.Transition != sessions.HandoffReject) {
			return nil, errors.New("Provide handoff id/current version, accept or reject, and UUIDv7 idempotency_key")
		}
		if args.Transition == sessions.HandoffAccept && args.Reason != nil || args.Transition == sessions.HandoffReject && args.Reason == nil {
			return nil, errors.New("Reject requires a reason; accept must omit it")
		}
		method, path = http.MethodPost, "/handoffs/"+args.ID.String()+"/responses"
		body = sessions.HandoffResponseCommand{Transition: args.Transition, Reason: args.Reason}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
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
func validSessionToolSID(sid string) bool {
	return strings.HasPrefix(sid, "osn_") && validSessionToolID(model.ID(strings.TrimPrefix(sid, "osn_")))
}
