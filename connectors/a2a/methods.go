// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

// A2A v1.0 JSON-RPC 2.0 method surface (the v1.0 rename of the v0.x `*/​*` forms),
// pinned to a2aproject/A2A v1.0.1 (2026-05-28). Verified against the canonical
// spec §9.4 method list at tag v1.0.1 (NOT the whats-new page, which mis-states
// several v1.0 wire details):
//
//	message/send                       -> SendMessage
//	message/stream                     -> SendStreamingMessage   (SSE)
//	tasks/get                          -> GetTask
//	tasks/cancel                       -> CancelTask
//	tasks/resubscribe                  -> SubscribeToTask         (SSE resume)
//	agent/getAuthenticatedExtendedCard -> GetExtendedAgentCard    (authenticated)
//	(new in v1.0)                      -> ListTasks               (pageToken pagination)
//	tasks/pushNotificationConfig/set   -> CreateTaskPushNotificationConfig
//	tasks/pushNotificationConfig/get   -> GetTaskPushNotificationConfig
//	tasks/pushNotificationConfig/list  -> ListTaskPushNotificationConfigs   (plural)
//	tasks/pushNotificationConfig/delete-> DeleteTaskPushNotificationConfig
//
// There is NO WebSocket binding in A2A v1.0: the spec defines exactly three
// equivalent transport bindings over one canonical Protocol-Buffers model —
// JSON-RPC 2.0 (the binding this client speaks; Content-Type application/json per
// §9.1 — the v1.0.1 application/a2a+json preference is scoped to the HTTP+JSON/REST
// binding and webhook payloads), gRPC, and HTTP+JSON/REST — each declared per
// AgentInterface in the card's supportedInterfaces. gRPC / HTTP+JSON are reserved
// behind the transport seam (transport.go), not claimed. methodSendMessage lives in
// emit_task.go (the emission primitive); the rest are the surface.
const (
	methodSendStreamingMessage = "SendStreamingMessage" // v1.0 rename of message/stream (SSE)
	methodGetTask              = "GetTask"              // v1.0 rename of tasks/get
	methodCancelTask           = "CancelTask"           // v1.0 rename of tasks/cancel
	methodListTasks            = "ListTasks"            // NEW in v1.0 (pageToken pagination)
	methodSubscribeToTask      = "SubscribeToTask"      // v1.0 rename of tasks/resubscribe (SSE resume)
	methodGetExtendedAgentCard = "GetExtendedAgentCard" // v1.0 rename of agent/getAuthenticatedExtendedCard

	methodCreatePushConfig = "CreateTaskPushNotificationConfig"
	methodGetPushConfig    = "GetTaskPushNotificationConfig"
	methodListPushConfigs  = "ListTaskPushNotificationConfigs" // plural "Configs" (§9.4.7)
	methodDeletePushConfig = "DeleteTaskPushNotificationConfig"
)

// ProtocolVersion is the A2A release this client is built and verified against,
// pinned to a2aproject/A2A tag v1.0.1 (published 2026-05-28; erratas #1753 content
// type, #1627 transcoding error mappings, #1801 TaskStatus values — all doc-level,
// the proto wire model is byte-identical to v1.0.0).
const ProtocolVersion = "1.0.1"

// a2aVersionWire is the value of the mandatory A2A-Version service parameter sent
// as an HTTP header on every protocol request (spec §3.6.1: clients MUST send it;
// an EMPTY header is interpreted as protocol 0.3). The format is Major.Minor —
// "patch numbers MUST NOT be used" in version negotiation, so this is "1.0", not
// the v1.0.1 pin above.
const a2aVersionWire = "1.0"

// a2aVersionHeader is the registered A2A-Version header name (spec §14.2.1).
const a2aVersionHeader = "A2A-Version"

// a2aErrorNames maps the v1.0 A2A-specific JSON-RPC error codes (§5.4, range
// -32001..-32099) to their spec names, so a remote refusal surfaces diagnosably
// (the name is spec vocabulary, never remote-controlled text).
var a2aErrorNames = map[int]string{
	-32001: "TaskNotFoundError",
	-32002: "TaskNotCancelableError",
	-32003: "PushNotificationNotSupportedError",
	-32004: "UnsupportedOperationError",
	-32005: "ContentTypeNotSupportedError",
	-32006: "InvalidAgentResponseError",
	-32007: "ExtendedAgentCardNotConfiguredError",
	-32008: "ExtensionSupportRequiredError",
	-32009: "VersionNotSupportedError",
}

// a2aErrorName returns the spec name for an A2A error code, or "" for a standard
// JSON-RPC / unknown code.
func a2aErrorName(code int) string { return a2aErrorNames[code] }
