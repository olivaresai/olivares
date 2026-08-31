// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package otlplog is the Olivares AI output connector that emits audit/governance
// notifications as OpenTelemetry logs (OTLP LogRecord) over OTLP/HTTP. It is the
// egress complement to the INGEST side: the OTel GenAI semconv ingest profile (spans
// the engine receives) and the ledger push seam already exist; this connector EMITS
// the engine's
// own audit/governance events as OTLP logs to whatever OTLP collector the SOC
// already runs (an OpenTelemetry Collector, a vendor OTLP endpoint), so those
// events land in the same pipeline as the rest of the estate's telemetry.
//
// The record is resolved by connectors/internal/siemfmt (OTLPRequestFor), which maps the
// shared severity scale onto the OTLP SeverityNumber, puts the notification type in
// OTLP's dedicated event_name member, and carries the ordered fields as attributes. This
// connector resolves once and then projects that one request into whichever encoding is
// configured: the generated protobuf types for the binary body, or the SDK's declared
// JSON layout (sdk/siemwire) for the JSON body — it does not wrap a pre-built LogsData
// for both. Two encodings are offered: OTLP/JSON
// (default) and OTLP/protobuf (application/x-protobuf). A 200 with a non-empty
// partial_success.rejected_log_records is a logical rejection (the OTLP analog of
// Splunk HEC code!=0) and is surfaced as an error in BOTH encodings (the response is
// decoded from the bounded raw body per the request encoding).
//
// It reuses the shared connectors/internal/delivery transport for reliable HTTP
// (backoff, honored Retry-After, transient-vs-terminal). Minimal data (docs/SECURITY-HARDENING.md):
// the LogRecord carries only the already-displayable Notification fields. The
// optional bearer credential is held in memory only and never logged. It imports
// only the SDK, siemfmt, delivery and the OTLP proto — never the engine.
package otlplog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.otlplog"

// encoding is the OTLP/HTTP payload encoding.
type encoding string

const (
	encodingJSON  encoding = "json"     // OTLP/JSON, application/json (default)
	encodingProto encoding = "protobuf" // OTLP/protobuf, application/x-protobuf
)

const (
	defaultEncoding    = encodingJSON
	defaultMaxAttempts = 4
	// logsPath is the OTLP/HTTP logs path appended to a base endpoint per the OTLP
	// spec ("the target URL ... <base>/v1/logs").
	logsPath = "/v1/logs"
)

// Output is the OTLP logs output connector. Open builds the reusable delivery
// client and resolves the /v1/logs target; Notify encodes one notification as an
// ExportLogsServiceRequest and delivers it; Close releases nothing.
type Output struct {
	endpoint string // resolved target URL ending in /v1/logs
	token    string // optional bearer credential — memory only
	encoding encoding
	device   siemfmt.Device

	maxAttempts int
	doer        delivery.Doer // optional injected transport (tests); nil => default
	client      *delivery.Client
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns an OTLP logs output connector with default configuration (OTLP/JSON).
func New() *Output {
	return &Output{encoding: defaultEncoding, maxAttempts: defaultMaxAttempts}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "OTLP Logs",
		Description: "Emits audit/governance notifications as OpenTelemetry logs (LogRecord) over OTLP/HTTP to an OTLP collector. JSON (default) or protobuf encoding.",
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Required: true, Description: "OTLP/HTTP base URL (e.g. https://collector:4318) or full /v1/logs URL."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "Optional bearer token sent as 'Authorization: Bearer <token>'. Held in memory only, never logged."},
			{Key: "encoding", Type: sdk.FieldString, Default: string(defaultEncoding), Description: "OTLP/HTTP encoding: json (default) | protobuf. partial_success rejections are detected in both."},
			{Key: "vendor", Type: sdk.FieldString, Description: "Device vendor override; emitted as the ai.olivares.device.vendor resource attribute."},
			{Key: "product", Type: sdk.FieldString, Description: "Device product override; emitted as the service.name resource attribute."},
			{Key: "version", Type: sdk.FieldString, Description: "Device HEADER version override (CEF/LEEF field 4); emitted as ai.olivares.device.version. This is NOT service.version."},
			{Key: "service_version", Type: sdk.FieldString, Description: "The running service's version, emitted as the service.version resource attribute. Left unset, service.version is omitted rather than guessed."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Maximum HTTP delivery attempts (including the first) per notification."},
		},
	}
}

// Open reads and validates configuration and builds the reusable delivery client.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	switch encoding(strings.ToLower(strings.TrimSpace(cfg.Get("encoding")))) {
	case encodingJSON, "":
		o.encoding = encodingJSON
	case encodingProto:
		o.encoding = encodingProto
	default:
		return fmt.Errorf("otlplog: unknown encoding %q (want json|protobuf)", cfg.Get("encoding"))
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Get("endpoint")), "/")
	if endpoint == "" {
		return fmt.Errorf("otlplog: endpoint is required")
	}
	if !strings.HasSuffix(endpoint, logsPath) {
		endpoint += logsPath
	}
	o.endpoint = endpoint

	o.token = cfg.Get("token")
	o.device = siemfmt.DefaultDevice()
	if v := cfg.Get("vendor"); v != "" {
		o.device.Vendor = v
	}
	if v := cfg.Get("product"); v != "" {
		o.device.Product = v
	}
	if v := cfg.Get("version"); v != "" {
		o.device.Version = v
	}
	// service.version is a DIFFERENT concept from the device header revision: the semantic
	// conventions define it as the service's API or implementation version, while an operator
	// may set the header to a reseller's branding. Left unset it stays empty and the OTLP
	// resource omits service.version, which is honest; deriving it from the header would not
	// be, and that is exactly the false metadata this connector used to emit.
	if v := cfg.Get("service_version"); v != "" {
		o.device.ServiceVersion = v
	}

	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	// Narrow the transport's retry set to the one the OTLP specification names.
	// The default HTTP heuristic retries 408, 425 and every 5xx, so a 500 was
	// re-sent several times inside a single delivery BEFORE any classification
	// could call it terminal — and the specification says the client MUST NOT retry
	// the same telemetry data on a failure outside 429/502/503/504. Classifying
	// afterwards cannot recall requests that already left.
	o.client = delivery.New(o.doer, delivery.Options{
		MaxAttempts: o.maxAttempts,
		Retryable:   func(status int) bool { return classifyOTLPStatus(status) == sdk.OutcomeUnavailable },
	})
	return nil
}

// Notify encodes n as an OTLP ExportLogsServiceRequest and delivers it. It returns
// an error on delivery failure or when the collector reports a partial success with
// rejected log records under HTTP 200 (detected in both encodings).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("otlplog: connector not opened")
	}
	body, contentType, err := o.encode(n)
	if err != nil {
		return err
	}
	hdr := map[string]string{"Content-Type": contentType}
	if o.token != "" {
		hdr["Authorization"] = "Bearer " + o.token
	}
	res, err := o.client.Send(ctx, delivery.Request{URL: o.endpoint, Header: hdr, Body: body})
	if err != nil {
		// Classify the HTTP failure instead of handing the engine a bare error it can
		// only read as "retry". The OTLP specification names the retryable statuses
		// exactly — 429, 502, 503 and 504 — and a 400 is explicitly NOT retryable, so
		// returning it unclassified made the engine re-send a request the collector
		// had rejected outright, through the whole ladder.
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{
				Outcome: classifyOTLPStatus(res.StatusCode), Sent: 1, Rejected: -1,
				Locator: sdk.LocatorAggregateCount, FirstRejected: -1, Code: res.StatusCode,
			},
			fmt.Errorf("otlplog: deliver: %w", err))
	}
	// 200 with partial_success.rejected_log_records>0 is a logical rejection (the
	// OTLP analog of Splunk HEC code!=0). The response uses the same encoding as
	// the request; we decode the bounded raw response body accordingly.
	// Fail CLOSED on a body we could not read whole. Every parser below treats a
	// body it cannot decode as "not one of ours, the 2xx stands", so a truncated or
	// partially read rejection would be indistinguishable from an accepted delivery.
	// This is a DIFFERENT case from the empty-or-unparseable body that records
	// as a deliberate policy choice: there we read the whole answer and it said
	// nothing we recognize; here we never saw the whole answer at all.
	if !res.BodyComplete {
		return fmt.Errorf("otlplog: refusing to treat HTTP %d as delivered: the response body was not read in full (%d bytes read), so a logical rejection could not be ruled out: %w",
			res.StatusCode, len(res.RawBody), errIncompleteResponse)
	}
	rejected, perr := partialSuccessRejected(o.encoding, res.RawBody)
	if perr != nil {
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{Outcome: sdk.OutcomeIndeterminate, Sent: 1, Rejected: -1}, perr)
	}
	if rejected != 0 {
		// The OpenTelemetry specification is explicit: "The client MUST NOT retry the
		// request when it receives a partial success response where the partial_success
		// is populated." So this is reported as a TERMINAL outcome, not as a generic
		// error the engine would put back on the retry ladder — which is what it used
		// to do, re-sending records the collector had already refused for roughly forty
		// minutes before dead-lettering them anyway.
		//
		// One notification is one log record here, so a populated rejection count is a
		// total rejection of what was sent; ClassifyCount is still used rather than
		// hard-coding that, because the day this connector batches, the classification
		// must follow the batch and not this comment.
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{
				Outcome:  sdk.ClassifyCount(1, int(rejected)),
				Sent:     1,
				Rejected: int(rejected),
				Locator:  sdk.LocatorAggregateCount,
			},
			fmt.Errorf("otlplog: collector rejected %d record(s)", rejected))
	}
	return nil
}

// errIncompleteResponse marks a response whose body could not be read whole, so
// no delivery verdict can be drawn from it.
var errIncompleteResponse = errors.New("incomplete response body")

// Close releases resources; this connector holds none beyond the stateless
// delivery client.
func (o *Output) Close(context.Context) error { return nil }

// encode builds the ExportLogsServiceRequest for n and serializes it in the
// configured encoding, returning the bytes and the matching Content-Type.
func (o *Output) encode(n sdk.Notification) ([]byte, string, error) {
	// Resolved ONCE, then projected. Shared resolution means neither encoding RECOMPUTES a
	// severity, event name, body or timestamp — it does NOT make them structurally
	// incapable of differing, because each still lays out its own member set: removing a
	// member from one projection alone produces exactly that divergence, which is why the
	// whole-message parity test in encode_test.go exists rather than a claim here. Both do
	// apply the same siemwire request validation, so a request is deliverable in both
	// encodings or in neither. The earlier shape built the full generated message graph
	// even in JSON mode and then discarded it, resolving the notification a second time.
	resolved := siemfmt.OTLPRequestFor(o.device, n)
	if o.encoding == encodingProto {
		data, err := siemfmt.OTLPLogsDataFrom(resolved)
		if err != nil {
			return nil, "", fmt.Errorf("otlplog: build protobuf: %w", err)
		}
		req := &collogspb.ExportLogsServiceRequest{ResourceLogs: data.ResourceLogs}
		b, err := proto.Marshal(req)
		if err != nil {
			return nil, "", fmt.Errorf("otlplog: marshal protobuf: %w", err)
		}
		return b, "application/x-protobuf", nil
	}
	// The JSON body comes from the shared SDK encoder, not protojson: default protojson
	// emits a non-zero severity enum by NAME (OTLP/JSON requires integers and forbids the
	// names) and omits zero values, so an unknown severity produced no severityNumber at
	// all. It also makes no byte-stability promise across library versions. The binary
	// encoding above still marshals the generated types, where neither question arises.
	b, err := siemwire.OTLPExportRequestJSON(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("otlplog: marshal json: %w", err)
	}
	return b, "application/json", nil
}

// partialSuccessError decodes an OTLP ExportLogsServiceResponse from the (bounded)
// raw response body — protobuf or OTLP/JSON per the request encoding — and returns
// an error when partial_success reports rejected log records. JSON decoding uses
// DiscardUnknown so a collector that adds an unrecognized field does not turn a real
// rejection into a false success. An empty or unparseable body is success (the 2xx
// stands; many collectors answer with an empty body).
// partialSuccessRejected returns how many log records the collector refused, or an
// error when the answer could not be interpreted at all. A zero count with a nil
// error is a clean acceptance.
//
// The error message the collector supplies is deliberately NOT returned: it is
// remote text, and remote text does not enter a delivery verdict that gets
// recorded. The COUNT is a number and is safe to keep.
func partialSuccessRejected(enc encoding, body []byte) (int64, error) {
	var resp collogspb.ExportLogsServiceResponse
	if enc == encodingProto {
		// A ZERO-BYTE body is the valid protobuf serialization of an
		// ExportLogsServiceResponse with no fields set, so it is a clean acceptance —
		// and the exemption belongs to protobuf ALONE. It used to be tested before the
		// encoding was consulted, which quietly extended it to JSON, where an empty
		// body is not a document at all and the success response is "{}".
		if len(body) == 0 {
			return 0, nil
		}
		// Binary protobuf must NOT be trimmed — a leading 0x0A (field-1 tag) is a
		// valid byte, not whitespace.
		if err := proto.Unmarshal(body, &resp); err != nil {
			// Non-empty bytes we cannot decode are not "zero rejections". The collector
			// answered something, and reading it as an acceptance is the remaining route
			// by which a refusal becomes a success.
			return 0, fmt.Errorf("otlplog: the collector's response could not be decoded, so the delivery cannot be confirmed: %w", err)
		}
	} else {
		jb := bytes.TrimSpace(body)
		if len(jb) == 0 {
			// Empty, or nothing but whitespace. JSON has no such document: the OTLP/JSON
			// success response is an object, and "{}" is how a collector spells the empty
			// one. Something that is not the collector answered — a proxy, a load
			// balancer, an endpoint that is not /v1/logs — and an acceptance may not be
			// read from it.
			return 0, fmt.Errorf("otlplog: HTTP success carried no JSON response document, so the delivery cannot be confirmed")
		}
		if string(jb) == "{}" {
			return 0, nil
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jb, &resp); err != nil {
			return 0, fmt.Errorf("otlplog: the collector's response could not be decoded, so the delivery cannot be confirmed: %w", err)
		}
	}
	ps := resp.GetPartialSuccess()
	if ps == nil {
		return 0, nil
	}
	return ps.GetRejectedLogRecords(), nil
}

// classifyOTLPStatus maps an HTTP failure to a delivery outcome using the set the
// OTLP specification names as retryable.
//
// The specification is explicit that only 429, 502, 503 and 504 are worth
// retrying, and that other 4xx responses are non-retryable failures for which "the
// client MUST NOT retry sending the same telemetry data". A status of 0 means no
// response was ever received — a transport failure, which is retryable and is not
// a statement by the collector about the payload.
func classifyOTLPStatus(status int) sdk.DeliveryOutcome {
	switch {
	case status == 0:
		return sdk.OutcomeIndeterminate
	case status == 429, status == 502, status == 503, status == 504:
		return sdk.OutcomeUnavailable
	default:
		// EVERYTHING else is non-retryable. The specification does not describe a
		// permissive middle ground: it names 429, 502, 503 and 504 as the retryable
		// statuses and says that for any other failure "the client MUST NOT retry
		// sending the same telemetry data". Treating an unlisted 5xx as transient
		// because it looks like a server problem is exactly the reading the
		// specification forecloses, and it is how a collector that is permanently
		// misconfigured keeps receiving the same rejected batch.
		return sdk.OutcomeRejected
	}
}
