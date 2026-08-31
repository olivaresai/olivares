// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package elastic is the Olivares AI output connector that ships audit/governance
// notifications to Elasticsearch through the _bulk API, encoded as Elastic Common
// Schema (ECS) documents. It is the egress complement to the SIEM-format work in
// connectors/internal/siemfmt: that package turns one sdk.Notification into a
// single ECS (9.4.0) document; this connector wraps that document in the two-line
// _bulk NDJSON envelope (a "create" action line plus the source line) and delivers
// it to {endpoint}/_bulk.
//
// The ECS document itself is built by siemfmt.ECS — this connector never
// hand-rolls ECS or invents a second severity scale; severity already lives inside
// the document (siemfmt maps the shared severity scale onto ECS event.severity).
// The connector only supplies the device identity (siemfmt.DefaultDevice with the
// optional vendor/product/version config overrides, exactly like the otlplog
// connector) and the target index for the action line.
//
// It is minimal-data (docs/SECURITY-HARDENING.md): it forwards only the displayable Notification
// fields that siemfmt already projects, with no enrichment. The operator's
// credential — an Elasticsearch API key or a bearer token — is a Secret config
// field, held in memory only, placed solely in the request's Authorization header,
// and NEVER logged or wrapped into an error. The shared connectors/internal/delivery
// transport handles within-call retry of transient failures and never logs the
// request body or headers, so the credential cannot leak through a diagnostic.
//
// _bulk's HTTP semantics make a 200 ambiguous: a 200 only means the request was
// parsed, not that every document indexed. The response carries {"errors":bool,
// "items":[...]}, and when "errors" is true at least one document failed; this
// connector parses that and surfaces it as an error (the 200-with-logical-error
// pattern, the Elasticsearch analog of Splunk HEC code!=0). The first failing
// item's error type/reason is included in the message — never the request body or
// the credential.
//
// It imports only the SDK, siemfmt and the shared delivery transport — never the
// engine.
package elastic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.elastic"

const (
	defaultMaxAttempts = 4
	// bulkPath is the Elasticsearch _bulk API path appended to the base endpoint.
	bulkPath = "/_bulk"
	// contentType is the _bulk API's required content type: newline-delimited JSON.
	contentType = "application/x-ndjson"
)

// Output is the Elasticsearch _bulk / ECS output connector. Open validates the
// configuration and builds the reusable delivery client; Notify encodes one
// notification as a one-document _bulk request and delivers it; Close releases
// nothing beyond the stateless delivery client.
type Output struct {
	endpoint string // resolved target URL ending in /_bulk
	index    string // target data stream / index for the _bulk action line
	apiKey   string // optional Elasticsearch API key — memory only, header only
	bearer   string // optional bearer token — memory only, header only
	device   siemfmt.Device

	maxAttempts int
	doer        delivery.Doer // optional injected transport (tests); nil => default
	client      *delivery.Client
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns an Elasticsearch output connector with default configuration.
func New() *Output {
	return &Output{maxAttempts: defaultMaxAttempts}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Elasticsearch (ECS)",
		Description: "Delivers notifications to Elasticsearch via the _bulk API as Elastic Common Schema (ECS) documents.",
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Required: true, Description: "Elasticsearch base URL (e.g. https://es:9200). The _bulk path is appended."},
			{Key: "index", Type: sdk.FieldString, Required: true, Description: "Target data stream or index for the _bulk create action."},
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Elasticsearch API key (the already-base64 'id:api_key' value), sent as 'Authorization: ApiKey <api_key>'. Wins over bearer. Held in memory only, never logged."},
			{Key: "bearer", Type: sdk.FieldString, Secret: true, Description: "Bearer token sent as 'Authorization: Bearer <bearer>' when no api_key is set. Held in memory only, never logged."},
			{Key: "vendor", Type: sdk.FieldString, Description: "siemfmt device vendor override (ECS observer.vendor / event.provider)."},
			{Key: "product", Type: sdk.FieldString, Description: "siemfmt device product override (ECS observer.product / service.name)."},
			{Key: "version", Type: sdk.FieldString, Description: "siemfmt device version override."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Maximum HTTP delivery attempts (including the first) per notification."},
		},
	}
}

// Open reads and validates configuration and builds the reusable delivery client.
// The endpoint and index are required: without them the connector cannot address a
// document, so Open fails fast rather than deferring the error to Notify.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Get("endpoint")), "/")
	if endpoint == "" {
		return fmt.Errorf("elastic: endpoint is required")
	}
	o.endpoint = endpoint + bulkPath

	o.index = strings.TrimSpace(cfg.Get("index"))
	if o.index == "" {
		return fmt.Errorf("elastic: index is required")
	}

	o.apiKey = cfg.Get("api_key")
	o.bearer = cfg.Get("bearer")

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

	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	// Keep the transport's internal retry set and the verdict classifier in
	// agreement. They disagreed: the default heuristic retried 408 and 425 while the
	// classifier called every 4xx a deterministic refusal, so those two statuses
	// burned the attempt budget and were then declared unretryable — the worst of
	// both readings.
	o.client = delivery.New(o.doer, delivery.Options{
		MaxAttempts: o.maxAttempts,
		Retryable:   func(status int) bool { return classifyESStatus(status) == sdk.OutcomeUnavailable },
	})
	return nil
}

// Notify encodes n as a one-document _bulk request and delivers it. It returns an
// error on delivery failure or — under HTTP 200 — when the _bulk response reports
// errors:true for the document. The credential never appears in a log or error.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("elastic: Notify called before Open")
	}

	body, err := o.encode(n)
	if err != nil {
		return err
	}

	hdr := map[string]string{"Content-Type": contentType}
	// api_key wins over bearer when both are configured.
	if o.apiKey != "" {
		hdr["Authorization"] = "ApiKey " + o.apiKey
	} else if o.bearer != "" {
		hdr["Authorization"] = "Bearer " + o.bearer
	}

	res, err := o.client.Send(ctx, delivery.Request{URL: o.endpoint, Header: hdr, Body: body})
	if err != nil {
		// delivery already redacts: its error carries only status + a bounded body
		// excerpt, never the request body or the Authorization header.
		//
		// Classify it rather than hand the engine a bare error it can only read as
		// "retry". Elasticsearch documents 429 (and 503 via unavailable shards) as the
		// conditions to retry with backoff; a 4xx describes the request itself and
		// never succeeds on a retry of the same bytes, so returning it unclassified
		// burned the whole ladder before dead-lettering.
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{
				Outcome: classifyESStatus(res.StatusCode), Sent: 1, Rejected: -1,
				Locator: sdk.LocatorOrdinal, FirstRejected: -1, Code: res.StatusCode,
			},
			fmt.Errorf("elastic: deliver: %w", err))
	}

	// A 2xx from _bulk does not mean the document indexed — parse the per-item
	// outcome and surface a logical failure as an error.
	// Fail CLOSED on a body we could not read whole. Every parser below treats a
	// body it cannot decode as "not one of ours, the 2xx stands", so a truncated or
	// partially read rejection would be indistinguishable from an accepted delivery.
	// This is a DIFFERENT case from the empty-or-unparseable body that records
	// as a deliberate policy choice: there we read the whole answer and it said
	// nothing we recognize; here we never saw the whole answer at all.
	if !res.BodyComplete {
		return fmt.Errorf("elastic: refusing to treat HTTP %d as delivered: the response body was not read in full (%d bytes read), so a logical rejection could not be ruled out: %w",
			res.StatusCode, len(res.RawBody), errIncompleteResponse)
	}
	// A 2xx whose body is NOT a _bulk document is not an acceptance we can vouch
	// for. Elasticsearch answers _bulk with a _bulk document; an empty body, an HTML
	// error page from a proxy in front of it, or a JSON object with no items member
	// means something other than Elasticsearch answered, or Elasticsearch answered
	// something we do not understand. Treating that as delivered was the remaining
	// path by which a refusal became a success — the read-completeness guard closed
	// the truncation route, not this one.
	//
	// The count check is the sharpest case: we sent one action line, so a response
	// carrying zero items is not ambiguous, it is wrong. Reporting Indeterminate
	// keeps it retryable (the payload may never have landed) while refusing to
	// record an acceptance nobody stated.
	outcome, rejected, first, status, ordinals, ok := bulkVerdict(res.Body)
	if !ok {
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{Outcome: sdk.OutcomeIndeterminate, Sent: 1, Rejected: -1, FirstRejected: -1},
			fmt.Errorf("elastic: HTTP %d carried no readable _bulk response, so the delivery cannot be confirmed", res.StatusCode))
	}
	if ok && outcome.Accepted() {
		// Return HERE. Falling through to bulkResultError would re-read the same body,
		// see errors:true and report a failure — undoing the one case this
		// classification exists to get right: a 409 version conflict on OUR OWN stable
		// _id, which means the previous attempt already indexed this exact document.
		// The earlier shape did fall through, so an idempotent redelivery was still
		// reported as a refusal and dead-lettered an event that was in the index.
		return nil
	}
	if ok && !outcome.Accepted() {
		detail := bulkFirstDetail(res.Body)
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{
				Outcome: outcome, Sent: 1, Rejected: rejected,
				Locator: sdk.LocatorOrdinal, FirstRejected: first, Code: status,
				RejectedOrdinals: ordinals,
			},
			// The item's type and reason stay in the ERROR, which reaches logs and the
			// operator. They deliberately do not enter the DeliveryReport: that is
			// recorded state, and recorded state carries numbers we control rather than
			// text a destination chooses. Dropping the detail here would have removed a
			// diagnostic operators depend on to find the offending field.
			fmt.Errorf("elastic: _bulk refused %d item(s), first at ordinal %d: %s", rejected, first, detail))
	}
	// Unreachable: bulkVerdict returned ok, so exactly one of the two branches above
	// has already returned. The fall-through that used to live here called
	// bulkResultError, which treated an unparseable body as success — the precise
	// reading the classifier above exists to refuse. It is removed rather than left
	// as dead code, because a dead branch that CONTRADICTS the live one is worse than
	// no branch: the next author reads it as the intended behavior.
	return nil
}

// Close releases resources; this connector holds none beyond the stateless
// delivery client.
func (o *Output) Close(context.Context) error { return nil }

// encode builds the two-line _bulk NDJSON body for one notification: a "create"
// action line naming the target index, then the ECS source document, then the
// required trailing newline. The ECS document comes from siemfmt.ECS — never
// hand-rolled here.
//
// The action carries the delivery's STABLE idempotency key as _id when the engine
// supplied one. Without it Elasticsearch mints a fresh identifier per request, so
// every at-least-once redelivery — the ordinary consequence of an ambiguous
// failure, a timeout after the write landed, a stale-claim rescue — creates ANOTHER
// copy of the same event. Counts drawn from that index then overstate reality, and
// nothing in the response reveals it. With the key set, the "create" action refuses
// a document whose id already exists, which turns the retry into a no-op instead of
// a duplicate.
//
// A caller that does NOT go through the durable outbox supplies no key, and this
// deliberately does not invent one. A Notification carries no stable identity, so
// the only candidate would be a hash of its content — which would also collapse two
// genuinely DISTINCT events that happen to be identical, silently dropping one. For
// an evidence pipeline, fabricating an identity to simulate idempotency trades a
// duplicate for a loss, which is the worse direction. Such a caller stays
// at-least-once, as it always was, and the outbox path is the one that gets
// effectively-once.
func (o *Output) encode(n sdk.Notification) ([]byte, error) {
	action, err := json.Marshal(bulkCreate{Create: bulkCreateTarget{
		Index: o.index,
		ID:    n.Fields[sdk.IdempotencyKeyField],
	}})
	if err != nil {
		return nil, fmt.Errorf("elastic: marshal action line: %w", err)
	}
	doc, err := siemfmt.ECS(o.device, n)
	if err != nil {
		return nil, fmt.Errorf("elastic: encode ECS document: %w", err)
	}

	// _bulk requires NDJSON: action line, source line, trailing newline. The body
	// must not be pretty-printed (json.Marshal already emits compact JSON).
	var b strings.Builder
	b.Grow(len(action) + len(doc) + 2)
	b.Write(action)
	b.WriteByte('\n')
	b.Write(doc)
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// bulkCreate is the _bulk action line for one document: a "create" action that
// names the target index (the source line follows on the next NDJSON line).
type bulkCreate struct {
	Create bulkCreateTarget `json:"create"`
}

type bulkCreateTarget struct {
	Index string `json:"_index"`
	// ID is the delivery's stable idempotency key. It is omitted when the engine
	// supplied none (a synchronous send outside the durable outbox), in which case
	// Elasticsearch mints one and delivery stays at-least-once, as before.
	ID string `json:"_id,omitempty"`
}

// bulkResponse is the subset of the _bulk response this connector inspects:
// "errors" reports whether any item failed, and "items" carries the per-document
// outcome whose error type/reason is surfaced on failure.
type bulkResponse struct {
	// Errors is a POINTER so an ABSENT member is distinguishable from errors:false.
	// As a plain bool the two were the same value, and a document carrying items but
	// no "errors" — which _bulk never produces, so it is not Elasticsearch answering
	// — read as a clean success. Elastic documents both members as always present.
	Errors *bool      `json:"errors"`
	Items  []bulkItem `json:"items"`
}

// bulkItem is one entry in the _bulk response's items array. The action key
// ("create"/"index"/...) varies; this connector emits "create" but also reads
// "index" defensively so an operator's ingest rewrite does not hide the error.
type bulkItem struct {
	Create *bulkItemResult `json:"create,omitempty"`
	Index  *bulkItemResult `json:"index,omitempty"`
}

type bulkItemResult struct {
	Status int            `json:"status"`
	Error  *bulkItemError `json:"error,omitempty"`
}

type bulkItemError struct {
	Type   string `json:"type,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// errIncompleteResponse marks a response whose body could not be read whole, so
// no delivery verdict can be drawn from it.
var errIncompleteResponse = errors.New("incomplete response body")

// firstItemError returns a "type: reason" description of the first item that
// reports an error, or "" when none of the items carries one.
func firstItemError(items []bulkItem) string {
	for _, it := range items {
		r := it.Create
		if r == nil {
			r = it.Index
		}
		if r == nil || r.Error == nil {
			continue
		}
		e := r.Error
		switch {
		case e.Type != "" && e.Reason != "":
			return fmt.Sprintf("status %d: %s: %s", r.Status, e.Type, e.Reason)
		case e.Reason != "":
			return fmt.Sprintf("status %d: %s", r.Status, e.Reason)
		case e.Type != "":
			return fmt.Sprintf("status %d: %s", r.Status, e.Type)
		}
	}
	return ""
}

// bulkActionLinesPerRequest is how many action lines encode() writes. The verdict
// checks the answer against it, so the day this connector batches, the invariant
// moves with it instead of silently becoming wrong.
const bulkActionLinesPerRequest = 1

// bulkVerdict decodes a _bulk response and classifies it item by item. ok is false
// when the body is not a _bulk document, in which case the caller falls back to the
// existing handling rather than inventing a verdict.
func bulkVerdict(body string) (sdk.DeliveryOutcome, int, int, int, []int, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, 0, 0, 0, nil, false
	}
	var resp bulkResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return 0, 0, 0, 0, nil, false
	}
	// A _bulk answer states one item per action line, so the count is an invariant
	// and not a formality. This connector sends exactly one action line; anything
	// other than one item back is Elasticsearch answering a request we did not make,
	// or something in front of it answering for Elasticsearch. Zero was the obvious
	// case, but MORE than one is the same contradiction and was previously read as a
	// verdict about our document — a response describing other people's writes would
	// have decided the fate of ours.
	if len(resp.Items) != bulkActionLinesPerRequest {
		return 0, 0, 0, 0, nil, false
	}
	// "errors" must be PRESENT. Elastic documents it as always sent, so a document
	// carrying items without it is not a _bulk response — and as a plain bool its
	// absence was indistinguishable from errors:false, which is the reading that
	// turns a stranger's document into an acceptance of ours.
	if resp.Errors == nil {
		return 0, 0, 0, 0, nil, false
	}
	if !*resp.Errors {
		return sdk.OutcomeDelivered, 0, -1, 0, nil, true
	}
	outcome, rejected, first, status, ordinals := classifyBulk(resp.Items)
	return outcome, rejected, first, status, ordinals, true
}

// bulkFirstDetail renders the first failing item's type and reason for the error
// message, falling back to a generic phrase rather than fabricating a cause.
func bulkFirstDetail(body string) string {
	var resp bulkResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &resp); err != nil {
		return "no item detail in response"
	}
	if d := firstItemError(resp.Items); d != "" {
		return d
	}
	return "no item detail in response"
}

// classifyESStatus maps a request-level HTTP failure to a delivery outcome.
//
// Elasticsearch documents 429 (and 503 through unavailable shards) as the
// conditions to retry with backoff; a 4xx describes the request itself. A status
// of 0 means no response arrived at all, which is a transport problem rather than
// a statement about the payload.
func classifyESStatus(status int) sdk.DeliveryOutcome {
	switch {
	case status == 0:
		return sdk.OutcomeIndeterminate
	case status == 408, status == 425, status == 429, status == 502, status == 503, status == 504:
		// 408 and 425 are the transport asking for the request again; 429 and the
		// gateway 5xx are capacity. All are conditions that clear on their own.
		return sdk.OutcomeUnavailable
	case status >= 400 && status < 500:
		return sdk.OutcomeRejected
	case status >= 500:
		return sdk.OutcomeUnavailable
	default:
		return sdk.OutcomeIndeterminate
	}
}
