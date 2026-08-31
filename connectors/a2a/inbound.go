// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxInboundBody bounds an inbound SendMessage request independently from the
// remote peer's advertised limits. Content is handed to the governed router only
// after authentication and structural validation.
const maxInboundBody = 4 << 20

// InboundPart is the bounded connector-neutral A2A Part projection accepted by
// the kernel ingress. Text is sanitized plain text; structured data is retained
// only for a version-pinned ProtocolBinding mapper; raw/URL file values cross
// the boundary only as a reference plus their canonical wire digest.
type InboundPart struct {
	Kind      string
	Text      string
	Data      json.RawMessage
	Reference string
	Digest    string
}

// InboundMessage is the authenticated, protocol-shaped request passed across the
// Apache/AGPL boundary. PeerAuthority and PeerSubject come only from the verified
// bearer token. InterfaceTenant is an opaque A2A routing value and is never a
// local Olivares tenant identifier.
type InboundMessage struct {
	PeerAuthority   string
	PeerSubject     string
	Protocol        string
	InterfaceTenant string
	MessageID       string
	ContextID       string
	Role            string
	Parts           []InboundPart
	Metadata        map[string]json.RawMessage
	// ReplayID and ReplayExpiresAt are verified bearer claims for the durable
	// composition replay guard. They are excluded from content projections.
	ReplayID        string    `json:"-"`
	ReplayExpiresAt time.Time `json:"-"`
}

// InboundResult is the A2A SendMessage response union. A governed router returns
// either a durable Task reference or a synchronous Message reference. Remote IDs
// are protocol evidence; the composition adapter remains responsible for local
// WorkItem, Message and ProtocolBinding persistence.
type InboundResult struct {
	ResultKind string
	TaskID     string
	MessageID  string
	ContextID  string
	State      TaskState
	Role       string
}

// InboundRouter persists and routes one authenticated A2A message. Implementations
// must make retries idempotent by (peer authority, message id); the HTTP handler
// deliberately owns no process-local task authority.
type InboundRouter interface {
	RouteInboundA2A(context.Context, InboundMessage) (InboundResult, error)
}

// InboundTaskRequest identifies an existing task served by the inbound endpoint.
// PeerAuthority and replay claims are copied only from the verified bearer token;
// InterfaceTenant remains the operator-owned interface routing value.
type InboundTaskRequest struct {
	PeerAuthority   string
	PeerSubject     string
	InterfaceTenant string
	TaskID          string
	HistoryLength   int
	ReplayID        string
	ReplayExpiresAt time.Time
}

// InboundTaskRouter is the durable lifecycle surface for tasks previously
// returned by RouteInboundA2A. Implementations resolve task identity through a
// ProtocolBinding and its WorkItem; the connector owns no task cache.
type InboundTaskRouter interface {
	GetInboundA2ATask(context.Context, InboundTaskRequest) (InboundResult, error)
	CancelInboundA2ATask(context.Context, InboundTaskRequest) (InboundResult, error)
}

// InboundRouteError lets a governed router return a stable JSON-RPC/A2A code
// without exposing backend error text. Message must be non-sensitive vocabulary.
type InboundRouteError struct {
	Code    int
	Message string
}

func (e *InboundRouteError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// InboundServerConfig configures the authenticated A2A JSON-RPC application
// server. Its trust controls intentionally match PushReceiverConfig so an
// operator can pin the same peer authority on the command and notification
// surfaces. Router is required; a server without durable routing never mounts.
type InboundServerConfig struct {
	Audience        string
	IssuerJWKS      []byte
	JWKSURL         string
	AllowedIssuers  []string
	InterfaceTenant string
	Router          InboundRouter
	ReplayTTL       time.Duration
	Clock           func() time.Time
	Doer            httpGetter
	// DurableReplay delegates jti authority to Router. The connector still
	// verifies the token, but does not pre-burn it in process memory.
	DurableReplay bool

	RequireClientAttestation bool
	AttesterJWKS             []byte
}

// InboundServer is the authenticated JSON-RPC SendMessage/GetTask/CancelTask
// endpoint. It reuses the push receiver's issuer/JWKS/attestation verifier but
// has an independent replay cache and body parser.
type InboundServer struct {
	auth            *PushReceiver
	interfaceTenant string
	router          InboundRouter
	tasks           InboundTaskRouter
	durableReplay   bool
}

// NewInboundServer builds a deny-closed A2A application server.
func NewInboundServer(cfg InboundServerConfig) (*InboundServer, error) {
	if cfg.Router == nil {
		return nil, fmt.Errorf("a2a: inbound server requires a durable router")
	}
	auth, err := NewPushReceiver(PushReceiverConfig{
		Audience:                 cfg.Audience,
		IssuerJWKS:               cfg.IssuerJWKS,
		JWKSURL:                  cfg.JWKSURL,
		AllowedIssuers:           cfg.AllowedIssuers,
		ReplayTTL:                cfg.ReplayTTL,
		Clock:                    cfg.Clock,
		Doer:                     cfg.Doer,
		RequireClientAttestation: cfg.RequireClientAttestation,
		AttesterJWKS:             cfg.AttesterJWKS,
	})
	if err != nil {
		return nil, err
	}
	tasks, _ := cfg.Router.(InboundTaskRouter)
	return &InboundServer{
		auth:            auth,
		interfaceTenant: strings.TrimSpace(cfg.InterfaceTenant),
		router:          cfg.Router,
		tasks:           tasks,
		durableReplay:   cfg.DurableReplay,
	}, nil
}

// ServeHTTP authenticates, validates and durably routes one SendMessage request.
// It acknowledges the RPC only after RouteInboundA2A succeeds.
func (s *InboundServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeInboundRPCError(w, http.StatusMethodNotAllowed, nil, -32600, "invalid request")
		return
	}
	if strings.TrimSpace(req.Header.Get(a2aVersionHeader)) != a2aVersionWire {
		writeInboundRPCError(w, http.StatusBadRequest, nil, -32009, "VersionNotSupportedError")
		return
	}
	claims, ok := s.authenticate(w, req)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxInboundBody+1))
	if err != nil || len(body) > maxInboundBody {
		writeInboundRPCError(w, http.StatusBadRequest, nil, -32600, "invalid request")
		return
	}
	var rpc inboundRPCRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&rpc); err != nil || hasMoreJSON(dec) {
		writeInboundRPCError(w, http.StatusBadRequest, nil, -32700, "parse error")
		return
	}
	if !validInboundRPCID(rpc.ID) || rpc.JSONRPC != "2.0" {
		writeInboundRPCError(w, http.StatusBadRequest, nil, -32600, "invalid request")
		return
	}
	result, err := s.routeRPC(req.Context(), rpc, claims)
	if err != nil {
		if errors.Is(err, ErrReplay) {
			http.Error(w, "replay", http.StatusUnauthorized)
			return
		}
		code, message := inboundRouteFailure(err)
		writeInboundRPCError(w, inboundRouteStatus(code), rpc.ID, code, message)
		return
	}
	projected, err := projectInboundRPCResult(rpc.Method, result)
	if err != nil {
		writeInboundRPCError(w, http.StatusServiceUnavailable, rpc.ID, -32603, "internal error")
		return
	}
	writeInboundRPCResult(w, rpc.ID, projected)
}

func projectInboundRPCResult(method string, result InboundResult) (any, error) {
	if method != methodGetTask && method != methodCancelTask {
		return projectInboundResult(result)
	}
	if result.ResultKind != "task" || result.TaskID == "" ||
		result.ContextID != strings.TrimSpace(result.ContextID) || !result.State.known() {
		return nil, fmt.Errorf("invalid task result")
	}
	return map[string]any{
		"id": result.TaskID, "contextId": result.ContextID,
		"status": map[string]any{"state": result.State},
	}, nil
}

func (s *InboundServer) routeRPC(
	ctx context.Context,
	rpc inboundRPCRequest,
	claims inboundPeerClaims,
) (InboundResult, error) {
	switch rpc.Method {
	case methodSendMessage:
		message, err := parseInboundMessage(rpc.Params, claims.Issuer, claims.Subject, s.interfaceTenant)
		if err != nil {
			return InboundResult{}, &InboundRouteError{Code: -32602, Message: "invalid params"}
		}
		message.ReplayID = claims.ReplayID
		message.ReplayExpiresAt = claims.ReplayExpiresAt
		return s.router.RouteInboundA2A(ctx, message)
	case methodGetTask, methodCancelTask:
		if s.tasks == nil {
			return InboundResult{}, &InboundRouteError{Code: -32601, Message: "method not found"}
		}
		request, err := parseInboundTaskRequest(rpc.Params, claims, s.interfaceTenant)
		if err != nil {
			return InboundResult{}, &InboundRouteError{Code: -32602, Message: "invalid params"}
		}
		if rpc.Method == methodGetTask {
			return s.tasks.GetInboundA2ATask(ctx, request)
		}
		return s.tasks.CancelInboundA2ATask(ctx, request)
	default:
		return InboundResult{}, &InboundRouteError{Code: -32601, Message: "method not found"}
	}
}

func (s *InboundServer) authenticate(w http.ResponseWriter, req *http.Request) (claims inboundPeerClaims, ok bool) {
	if s.auth.attest != nil {
		if _, err := s.auth.attest.verify(req, s.auth.now()); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="client attestation required"`)
			http.Error(w, "client attestation required", http.StatusUnauthorized)
			return inboundPeerClaims{}, false
		}
	}
	token := bearerToken(req.Header.Get("Authorization"))
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		http.Error(w, "missing bearer", http.StatusUnauthorized)
		return inboundPeerClaims{}, false
	}
	verified, err := s.auth.verifyToken(req.Context(), token)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return inboundPeerClaims{}, false
	}
	if verified.ID == "" || (!s.durableReplay &&
		!s.auth.replay.admit(verified.ID, timeOfDate(verified.Expiry))) {
		http.Error(w, "replay", http.StatusUnauthorized)
		return inboundPeerClaims{}, false
	}
	return inboundPeerClaims{
		Issuer: verified.Issuer, Subject: verified.Subject,
		ReplayID: verified.ID, ReplayExpiresAt: timeOfDate(verified.Expiry),
	}, true
}

type inboundPeerClaims struct {
	Issuer          string
	Subject         string
	ReplayID        string
	ReplayExpiresAt time.Time
}

type inboundRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type inboundSendParams struct {
	Tenant  string `json:"tenant"`
	Message struct {
		Role      string                     `json:"role"`
		Parts     []json.RawMessage          `json:"parts"`
		MessageID string                     `json:"messageId"`
		ContextID string                     `json:"contextId"`
		Metadata  map[string]json.RawMessage `json:"metadata"`
	} `json:"message"`
}

type inboundTaskParams struct {
	Tenant        string `json:"tenant"`
	ID            string `json:"id"`
	HistoryLength int    `json:"historyLength"`
}

func parseInboundTaskRequest(
	raw json.RawMessage,
	claims inboundPeerClaims,
	interfaceTenant string,
) (InboundTaskRequest, error) {
	var params inboundTaskParams
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil ||
		(interfaceTenant != "" && params.Tenant != interfaceTenant) ||
		params.ID == "" || params.ID != strings.TrimSpace(params.ID) || len(params.ID) > 512 ||
		params.HistoryLength < 0 || params.HistoryLength > 1000 {
		return InboundTaskRequest{}, fmt.Errorf("invalid task params")
	}
	return InboundTaskRequest{
		PeerAuthority: claims.Issuer, PeerSubject: claims.Subject,
		InterfaceTenant: params.Tenant, TaskID: params.ID,
		HistoryLength: params.HistoryLength, ReplayID: claims.ReplayID,
		ReplayExpiresAt: claims.ReplayExpiresAt,
	}, nil
}

func parseInboundMessage(raw json.RawMessage, issuer, subject, interfaceTenant string) (InboundMessage, error) {
	var params inboundSendParams
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return InboundMessage{}, fmt.Errorf("invalid params")
	}
	if interfaceTenant != "" && params.Tenant != interfaceTenant {
		return InboundMessage{}, fmt.Errorf("interface tenant mismatch")
	}
	if !validReplyIdentifier(params.Message.MessageID) ||
		!validReplyIdentifier(params.Message.ContextID) ||
		(params.Message.Role != roleUser && params.Message.Role != roleAgent) || len(params.Message.Parts) == 0 {
		return InboundMessage{}, fmt.Errorf("invalid message")
	}
	parts, err := parseInboundParts(params.Message.Parts)
	if err != nil {
		return InboundMessage{}, err
	}
	metadata := make(map[string]json.RawMessage, len(params.Message.Metadata))
	for key, value := range params.Message.Metadata {
		key = strings.TrimSpace(key)
		if key == "" || !json.Valid(value) {
			return InboundMessage{}, fmt.Errorf("invalid metadata")
		}
		metadata[key] = append(json.RawMessage(nil), value...)
	}
	return InboundMessage{
		PeerAuthority: issuer, PeerSubject: subject, Protocol: ProtocolVersion,
		InterfaceTenant: params.Tenant, MessageID: params.Message.MessageID,
		ContextID: params.Message.ContextID, Role: params.Message.Role,
		Parts: parts, Metadata: metadata,
	}, nil
}

func parseInboundPart(raw json.RawMessage) (InboundPart, error) {
	parts, err := parseInboundParts([]json.RawMessage{raw})
	if err != nil {
		return InboundPart{}, err
	}
	return parts[0], nil
}

func parseInboundParts(rawParts []json.RawMessage) ([]InboundPart, error) {
	projected, err := projectMessageResultParts(rawParts)
	if err != nil {
		return nil, err
	}
	parts := make([]InboundPart, 0, len(projected))
	for index, projection := range projected {
		part := InboundPart{
			Kind: projection.Kind, Text: projection.Text,
			Reference: projection.Reference, Digest: projection.Digest,
		}
		if projection.Kind == "data" {
			var wire struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(rawParts[index], &wire) != nil || len(wire.Data) == 0 {
				return nil, fmt.Errorf("invalid data part")
			}
			var value any
			decoder := json.NewDecoder(bytes.NewReader(wire.Data))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return nil, fmt.Errorf("invalid data part")
			}
			canonical, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return nil, fmt.Errorf("canonicalize data part: %w", marshalErr)
			}
			part.Data = canonical
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func projectInboundResult(result InboundResult) (any, error) {
	switch result.ResultKind {
	case "task":
		if result.TaskID == "" || result.ContextID != strings.TrimSpace(result.ContextID) || !result.State.known() {
			return nil, fmt.Errorf("invalid task result")
		}
		return map[string]any{"task": map[string]any{
			"id": result.TaskID, "contextId": result.ContextID,
			"status": map[string]any{"state": result.State},
		}}, nil
	case "message":
		role := result.Role
		if role == "" {
			role = roleAgent
		}
		if result.MessageID == "" || (role != roleUser && role != roleAgent) {
			return nil, fmt.Errorf("invalid message result")
		}
		return map[string]any{"message": map[string]any{
			"messageId": result.MessageID, "contextId": result.ContextID,
			"role": role, "parts": []any{},
		}}, nil
	default:
		return nil, fmt.Errorf("invalid result kind")
	}
}

func inboundRouteFailure(err error) (int, string) {
	var routed *InboundRouteError
	if errors.As(err, &routed) && routed != nil &&
		((routed.Code <= -32001 && routed.Code >= -32099) || routed.Code == -32601 || routed.Code == -32602) &&
		strings.TrimSpace(routed.Message) != "" {
		return routed.Code, routed.Message
	}
	return -32603, "internal error"
}

func inboundRouteStatus(code int) int {
	switch code {
	case -32601, -32001:
		return http.StatusNotFound
	case -32002:
		return http.StatusConflict
	case -32602:
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

func validInboundRPCID(id json.RawMessage) bool {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) || !json.Valid(id) {
		return false
	}
	var value any
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func hasMoreJSON(dec *json.Decoder) bool {
	var extra any
	return dec.Decode(&extra) != io.EOF
}

func writeInboundRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result,
	})
}

func writeInboundRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}
