// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxOTLPBody caps an OTLP/HTTP request body. Cowork batches are small; this guards
// against a hostile or runaway poster on the receiver socket.
const maxOTLPBody = 16 << 20 // 16 MiB

// receiver turns incoming Cowork OTLP/HTTP logs into parsed events. It owns no
// sockets — cowork.go binds and runs the HTTP server around the handler it exposes
// — so the receiver itself is trivially testable. Every recognized Cowork log
// record is handed to onEvent.
type receiver struct {
	onEvent        func(coworkEvent)
	requireService string
	authHeader     string
	authToken      string
	now            func() time.Time
}

// ingestLogs walks an OTLP logs export request and forwards each recognized Cowork
// event to onEvent, stamping receive time when the record carried none.
// Resource-level attributes (service.name, identity) are merged into each record.
func (r *receiver) ingestLogs(req *collogspb.ExportLogsServiceRequest) {
	for _, rl := range req.GetResourceLogs() {
		resAttrs := rl.GetResource().GetAttributes()
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				ev, ok := parseLogRecord(rec, resAttrs, r.requireService)
				if !ok {
					continue
				}
				if ev.at.IsZero() {
					ev.at = r.now()
				}
				r.onEvent(ev)
			}
		}
	}
}

// httpHandler builds the OTLP/HTTP mux: the configured logs path (mapped), plus
// /v1/metrics and /v1/traces which are ACCEPTED and DISCARDED (Cowork emits neither
// today, but acking them means an OTEL collector batching all signals to this
// endpoint is never failed). Every route is wrapped by the auth gate.
func (r *receiver) httpHandler(logsPath string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(logsPath, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e collogspb.ExportLogsServiceRequest
		decodeAndAck(w, req, &e, func() { r.ingestLogs(&e) }, &collogspb.ExportLogsServiceResponse{})
	}))
	// Discard-ack the other OTLP signals so a misdirected metrics/traces batch is not
	// rejected (Cowork is logs-only; nothing is mapped from these).
	mux.Handle("/v1/metrics", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e colmetricspb.ExportMetricsServiceRequest
		decodeAndAck(w, req, &e, func() {}, &colmetricspb.ExportMetricsServiceResponse{})
	}))
	mux.Handle("/v1/traces", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e coltracepb.ExportTraceServiceRequest
		decodeAndAck(w, req, &e, func() {}, &coltracepb.ExportTraceServiceResponse{})
	}))
	return r.withAuth(mux)
}

// withAuth wraps a handler with the shared-secret gate. When no auth_token is
// configured (the loopback-sidecar case) every request passes. When a token is set,
// a request whose auth_header does not match (constant-time) is rejected 401 BEFORE
// the body is read — so an unauthenticated poster cannot forge Cowork telemetry on a
// reachable endpoint (the inbound-push threat model, see Open).
func (r *receiver) withAuth(next http.Handler) http.Handler {
	if r.authToken == "" {
		return next
	}
	want := []byte(r.authToken)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got := []byte(req.Header.Get(r.authHeader))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, req)
	})
}

// decodeAndAck handles one OTLP/HTTP request: it POST-guards, decodes the body
// (protobuf or JSON) into `into`, runs `ingest`, and answers 200 with `resp` in the
// request's encoding. The JSON path is permissive (DiscardUnknown) so a newer Cowork
// field never fails ingest.
func decodeAndAck(w http.ResponseWriter, req *http.Request, into proto.Message, ingest func(), resp proto.Message) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxOTLPBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	isJSON := isJSONContent(req.Header.Get("Content-Type"))
	if derr := unmarshalOTLP(body, isJSON, into); derr != nil {
		http.Error(w, "decode error", http.StatusBadRequest)
		return
	}
	ingest()
	writeOTLP(w, isJSON, resp)
}

// isJSONContent reports whether a Content-Type selects OTLP/JSON (vs protobuf).
func isJSONContent(ct string) bool {
	return strings.HasPrefix(strings.TrimSpace(ct), "application/json")
}

// unmarshalOTLP decodes an OTLP body as JSON or protobuf into msg. The JSON path is
// permissive (DiscardUnknown) so a newer Cowork field never fails ingest.
func unmarshalOTLP(body []byte, isJSON bool, msg proto.Message) error {
	if isJSON {
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, msg)
	}
	return proto.Unmarshal(body, msg)
}

// writeOTLP writes a 200 OTLP response in the matching encoding. A marshal error is
// swallowed to a bare 200 (the body is an empty ack; the client only needs the
// status), so the receiver never fails a successful ingest on response encoding.
func writeOTLP(w http.ResponseWriter, isJSON bool, msg proto.Message) {
	var body []byte
	var err error
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		body, err = protojson.Marshal(msg)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		body, err = proto.Marshal(msg)
	}
	w.WriteHeader(http.StatusOK)
	if err == nil {
		_, _ = w.Write(body)
	}
}
