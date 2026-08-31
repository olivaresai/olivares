// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoy

import (
	"context"
	"io"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
	"github.com/olivaresai/olivares/sdk/model"
)

// Process is the Envoy external processor server. Envoy bidi-streams the
// request/response phases (headers AND body) of one HTTP transaction; the connector
// observes them and ALWAYS RESPONDS CONTINUE WITH NO MUTATION — it is read-first,
// never an inline rewriter (docs/SECURITY-HARDENING.md). It gives body-level visibility (prompt /
// tool-args / response) WITHOUT ever emitting a raw body: a body is scanned in memory
// for secrets and only a SHA-256 of the redacted detail travels in a finding
// (docs/SECURITY-HARDENING.md). A test asserts the never-mutate / no-raw-body invariants.
func (o *obsServer) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	ctx := stream.Context()
	// One ext_proc stream is one HTTP transaction; the request-headers phase
	// establishes the edge context (FQDN/method/identity/trace) reused by the body
	// phases.
	cur := meshobs.Record{Source: SignalEnvoyExtProc, Tool: "envoy.ext_proc"}
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		var resp *extprocv3.ProcessingResponse
		switch v := req.Request.(type) {
		case *extprocv3.ProcessingRequest_RequestHeaders:
			cur = o.recordFromHeaders(v.RequestHeaders)
			_ = cur.Emit(ctx, o.sink) // observed L7 edge
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: continueHeaders()},
			}
		case *extprocv3.ProcessingRequest_ResponseHeaders:
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: continueHeaders()},
			}
		case *extprocv3.ProcessingRequest_RequestBody:
			o.observeBody(ctx, cur, v.RequestBody.GetBody(), "request")
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_RequestBody{RequestBody: continueBody()},
			}
		case *extprocv3.ProcessingRequest_ResponseBody:
			o.observeBody(ctx, cur, v.ResponseBody.GetBody(), "response")
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseBody{ResponseBody: continueBody()},
			}
		default:
			// Trailers or an unknown phase: acknowledge with an empty response (an
			// unset oneof is an implicit continue with no mutation).
			resp = &extprocv3.ProcessingResponse{}
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// recordFromHeaders builds the edge context from the request-headers phase, reading
// only the HTTP/2 pseudo-headers (:authority, :method) and the W3C traceparent.
// ext_proc surfaces headers, not the validated mTLS peer cert, so the identity is
// left unattributed (Approximate) — the connector never claims a verified identity it
// did not cryptographically observe (ARCHITECTURE.md).
func (o *obsServer) recordFromHeaders(h *extprocv3.HttpHeaders) meshobs.Record {
	m := headerMapToStrings(h.GetHeaders())
	return meshobs.Record{
		FQDN:       m[":authority"],
		Method:     m[":method"],
		Source:     SignalEnvoyExtProc,
		Tool:       "envoy.ext_proc",
		Trace:      tracecontext.FromHeaderMap(m),
		ObservedAt: o.now(),
	}
}

// observeBody scans a request/response body for secrets IN MEMORY and emits a finding
// ONLY on a hit — never the body itself. The detail is redacted before it is hashed,
// so no raw value ever leaves the connector (docs/SECURITY-HARDENING.md). A clean body produces no
// observation (the edge was already emitted at the headers phase).
func (o *obsServer) observeBody(ctx context.Context, base meshobs.Record, body []byte, phase string) {
	if len(body) == 0 {
		return
	}
	if !redact.ContainsSecret(string(body)) {
		return
	}
	host := base.FQDN
	if host == "" {
		// A body phase can arrive before/without a request-headers phase (a buffered
		// vs streamed body). Keep the subject internally consistent rather than empty.
		host = "unknown"
	}
	f := model.FindingReport{
		Kind:        "sensitive_egress",
		Severity:    model.SeverityHigh,
		SubjectKind: "net.egress",
		SubjectRef:  host,
		Title:       "sensitive content in ext_proc " + phase + " body to " + host,
		DetailHash:  redact.Hash(redact.Clean(string(body))),
		OccurredAt:  o.now(),
		// OWASP Top 10 for LLM Applications 2025: Sensitive Information Disclosure.
		OWASPLLM: []string{"LLM02:2025"},
	}
	_ = o.sink.Emit(ctx, f)
}

// continueHeaders / continueBody build the read-first CONTINUE responses with NO
// mutation: an empty CommonResponse whose status is the zero value CONTINUE. They
// carry no header or body mutation, so the connector never alters the data path.
func continueHeaders() *extprocv3.HeadersResponse {
	return &extprocv3.HeadersResponse{Response: &extprocv3.CommonResponse{Status: extprocv3.CommonResponse_CONTINUE}}
}

func continueBody() *extprocv3.BodyResponse {
	return &extprocv3.BodyResponse{Response: &extprocv3.CommonResponse{Status: extprocv3.CommonResponse_CONTINUE}}
}
