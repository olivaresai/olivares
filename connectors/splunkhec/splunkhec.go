// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package splunkhec is the Olivares AI output connector that ships notifications to
// the Splunk HTTP Event Collector (HEC) with the FULL HEC contract the generic SIEM
// connector lacked: the JSON event envelope (event/time/host/source/
// sourcetype/index), the "Authorization: Splunk <token>" scheme, and — the piece
// that makes delivery durable — indexer acknowledgement (a per-channel GUID, the
// ackID returned on submit, and a poll of /services/collector/ack until the event
// is confirmed replicated/indexed).
//
// Source verification. The Splunk docs.splunk.com HEC pages returned
// HTTP 403 to the contract verifier; the HEC contract implemented here (the
// /services/collector{,/event,/raw,/ack} endpoints, the "Splunk <token>" auth
// scheme, the {"text","code"} body with code!=0 = logical rejection, the
// X-Splunk-Request-Channel GUID + {"acks":[id]} -> {"acks":{"id":bool}} ack poll)
// is the standard documented HEC contract, re-verified verbatim against the
// equivalent first-party help.splunk.com / dev.splunk.com pages. No field or
// endpoint is invented; where the page 403'd, the standard contract is used and
// labeled as such — never fabricated.
//
// Two layers do the work. connectors/internal/siemfmt encodes the event payload
// (canonical JSON, or a SIEM wire format); connectors/internal/delivery does the
// reliable HTTP (backoff, honored Retry-After, retry only transient failures, never
// logs body/headers). This connector adds the HEC envelope, the channel/ack
// protocol, and the code!=0 logical-error check. The token is held in memory only
// and never logged. It imports only the SDK, siemfmt and delivery — never the engine.
package splunkhec

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.splunkhec"

// formatSet is this connector's slice of the sdk/siemwire format catalog: the
// notification-connector subset (json-first default, full dialect roster,
// otlp_envelope as the exact alias of otlp). The accepted set, the default, the
// operator-facing list and the alias resolution derive from the catalog via
// siemfmt.ResolveFormat — the private const block this replaced was one of six
// diverged hand copies.
func formatSet() siemwire.FormatSet { return siemwire.NotificationConnectorFormats() }

const (
	defaultMaxAttempts = 4
	defaultSourcetype  = "olivares"
	defaultAckTimeout  = 30 * time.Second
	defaultAckPoll     = 1 * time.Second

	pathEvent = "/services/collector/event"
	pathRaw   = "/services/collector/raw"
	pathAck   = "/services/collector/ack"
)

// Output is the Splunk HEC output connector. Open builds the reusable delivery
// client (and a per-instance channel GUID when ack is enabled); Notify encodes one
// notification, posts it with the HEC envelope/auth, checks code==0, and — when ack
// is enabled — confirms durable indexing; Close releases nothing.
type Output struct {
	endpoint   string               // HEC base URL (e.g. https://host:8088)
	token      string               // secret: HEC token — memory only
	format     siemwire.FormatToken // canonical encoder key, resolved at Open
	index      string
	sourcetype string
	source     string
	host       string
	device     siemfmt.Device

	useACK   bool
	channel  string // per-instance request channel GUID (ack mode)
	ackEvery time.Duration
	ackUntil time.Duration

	maxAttempts int
	doer        delivery.Doer // optional injected transport (tests); nil => default
	client      *delivery.Client
	sleep       func(ctx context.Context, d time.Duration) error // injectable for tests
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Splunk HEC output connector with default configuration.
func New() *Output {
	return &Output{
		format:      siemwire.Canonical(formatSet().Default()),
		sourcetype:  defaultSourcetype,
		maxAttempts: defaultMaxAttempts,
		ackEvery:    defaultAckPoll,
		ackUntil:    defaultAckTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Splunk HEC",
		Description: "Ships notifications to the Splunk HTTP Event Collector with the full HEC envelope, 'Splunk <token>' auth, code!=0 logical-error check and optional indexer acknowledgement.",
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Required: true, Description: "HEC base URL, e.g. https://splunk.corp:8088."},
			{Key: "token", Type: sdk.FieldString, Required: true, Secret: true, Description: "HEC token, sent as 'Authorization: Splunk <token>'. Held in memory only, never logged."},
			{Key: "format", Type: sdk.FieldString, Default: string(formatSet().Default()), Description: "Event payload format: " + strings.ReplaceAll(formatSet().List(), "|", " | ") + " (otlp_envelope is an exact alias of otlp). Text formats use /raw; JSON-ish formats use /event."},
			{Key: "index", Type: sdk.FieldString, Description: "Target Splunk index (must be in the token's allowed list)."},
			{Key: "sourcetype", Type: sdk.FieldString, Default: defaultSourcetype, Description: "Splunk sourcetype tag."},
			{Key: "source", Type: sdk.FieldString, Description: "Splunk source field."},
			{Key: "host", Type: sdk.FieldString, Description: "Event host field."},
			{Key: "use_ack", Type: sdk.FieldBool, Default: "false", Description: "Enable indexer acknowledgement (the token must have useACK=true). Sends a channel GUID and polls /ack until the event is confirmed durably indexed."},
			{Key: "ack_timeout", Type: sdk.FieldDuration, Default: defaultAckTimeout.String(), Description: "Maximum time to wait for an indexer ack before returning an error."},
			{Key: "ack_poll_interval", Type: sdk.FieldDuration, Default: defaultAckPoll.String(), Description: "Delay between indexer-ack polls."},
			{Key: "vendor", Type: sdk.FieldString, Description: "siemfmt device vendor override."},
			{Key: "product", Type: sdk.FieldString, Description: "siemfmt device product override."},
			{Key: "version", Type: sdk.FieldString, Description: "siemfmt device version override."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Maximum HTTP delivery attempts (including the first) per submit/poll request."},
		},
	}
}

// Open reads and validates configuration, builds the delivery client and (when ack
// is enabled) a per-instance request-channel GUID. Missing endpoint/token or an
// unknown format is reported here.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	tok, err := siemfmt.ResolveFormat(formatSet(), cfg.Get("format"))
	if err != nil {
		return fmt.Errorf("splunkhec: %w", err)
	}
	o.format = tok

	o.endpoint = strings.TrimRight(strings.TrimSpace(cfg.Get("endpoint")), "/")
	if o.endpoint == "" {
		return fmt.Errorf("splunkhec: endpoint is required")
	}
	o.token = cfg.Get("token")
	if o.token == "" {
		return fmt.Errorf("splunkhec: token is required")
	}
	o.index = strings.TrimSpace(cfg.Get("index"))
	o.source = strings.TrimSpace(cfg.Get("source"))
	o.host = strings.TrimSpace(cfg.Get("host"))
	if v := strings.TrimSpace(cfg.Get("sourcetype")); v != "" {
		o.sourcetype = v
	}

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

	o.useACK = cfg.GetBool("use_ack", false)
	o.ackUntil = cfg.GetDuration("ack_timeout", defaultAckTimeout)
	o.ackEvery = cfg.GetDuration("ack_poll_interval", defaultAckPoll)
	if o.useACK {
		ch, err := newChannelGUID()
		if err != nil {
			return fmt.Errorf("splunkhec: generate request channel: %w", err)
		}
		o.channel = ch
	}

	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAttempts})
	return nil
}

// Notify encodes n, submits it to HEC, verifies the HEC body code is 0, and — when
// ack is enabled — confirms the event was durably indexed.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("splunkhec: connector not opened")
	}
	req, err := o.buildRequest(n)
	if err != nil {
		return err
	}
	res, err := o.client.Send(ctx, req)
	if err != nil {
		// Classify from the BODY even here. Splunk sends most of its status codes with
		// a non-2xx HTTP status — code 7 "Incorrect index" arrives as 400, code 9
		// "Server is busy" as 503 — and the shared client returns an error for any
		// non-2xx. Returning that error unclassified sent every one of those down the
		// engine's default path, which retries: a wrong index burned the whole ladder
		// and then dead-lettered, having re-sent the same rejected bytes four times.
		//
		// It also made the code table nearly unreachable, since only the codes that
		// travel on a 2xx (0, 17, 24, 25) ever reached it. The body is present on this
		// path — Send returns the Result alongside the error — so there is no reason to
		// discard what Splunk said about why.
		if res.BodyComplete {
			// Same invariant as the 2xx path: only draw a verdict from something HEC
			// actually said. A proxy's error page on a 502 parses as JSON but is not a
			// HEC status document, and reading a code out of it would attribute a
			// refusal to Splunk that Splunk never made.
			if resp, perr := parseHECResponse(res.Body); resp.wellFormed && resp.code() != 0 {
				outcome := ClassifyHECCode(resp.code())
				rejected := 0
				if outcome == sdk.OutcomeRejected {
					rejected = 1
				}
				return sdk.NewDeliveryError(
					sdk.DeliveryReport{
						Outcome: outcome, Sent: 1, Rejected: rejected,
						Locator: sdk.LocatorPrefixBoundary, FirstRejected: resp.firstInvalid(),
						Code: resp.code(),
					},
					fmt.Errorf("splunkhec: submit: HTTP %d, HEC code %d: %w", res.StatusCode, resp.code(), errors.Join(err, perr)))
			}
		}
		return fmt.Errorf("splunkhec: submit: %w", err)
	}
	// Fail CLOSED on a body we could not read whole. Every parser below treats a
	// body it cannot decode as "not one of ours, the 2xx stands", so a truncated or
	// partially read rejection would be indistinguishable from an accepted delivery.
	// This is a DIFFERENT case from the empty-or-unparseable body that records
	// as a deliberate policy choice: there we read the whole answer and it said
	// nothing we recognize; here we never saw the whole answer at all.
	if !res.BodyComplete {
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{Outcome: sdk.OutcomeIndeterminate, Sent: 1, Rejected: -1},
			fmt.Errorf("splunkhec: refusing to treat HTTP %d as delivered: the response body was not read in full (%d bytes read), so a logical rejection could not be ruled out: %w",
				res.StatusCode, len(res.RawBody), errIncompleteResponse))
	}
	resp, perr := parseHECResponse(res.Body)
	// A 2xx that is not a HEC status document cannot confirm anything. HEC always
	// answers with text and code; an empty body, an HTML page from a proxy, or
	// invalid JSON means something other than HEC replied. This was the remaining
	// route by which a refusal read as a success — the read-completeness guard
	// closed truncation, not this.
	if !resp.wellFormed {
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{Outcome: sdk.OutcomeIndeterminate, Sent: 1, Rejected: -1, FirstRejected: -1},
			fmt.Errorf("splunkhec: HTTP %d carried no readable HEC status document, so the delivery cannot be confirmed", res.StatusCode))
	}
	// The CODE decides, not the presence of an error. A non-zero code is not
	// uniformly a refusal: 24 and 25 are capacity warnings over an ACCEPTED event,
	// and reporting those as failures makes the engine retry data Splunk already
	// indexed. See classifyHECCode for the full table and its source.
	outcome := ClassifyHECCode(resp.code())
	// An ACCEPTED payload never becomes an error, and that is the whole point of the
	// table: codes 24 and 25 arrive under HTTP 200 with the event indexed, so
	// returning an error for them would make the engine retry and duplicate data
	// Splunk already holds.
	//
	// STATED LIMIT: the warning itself is not surfaced anywhere today. A connector
	// carries no logger (this module is stdlib-only by design) and Notify's contract
	// is error-or-nil, so returning non-nil for a success would break every caller
	// that reads nil as "delivered". Carrying the warning to the operator needs a
	// reporting seam the engine owns; until then the correct behavior on the axis
	// that loses data — do not retry an acceptance — is what ships.
	if !outcome.Accepted() {
		rejected := 0
		if outcome == sdk.OutcomeRejected {
			rejected = 1
		}
		return sdk.NewDeliveryError(
			sdk.DeliveryReport{
				Outcome: outcome, Sent: 1, Rejected: rejected,
				Locator: sdk.LocatorPrefixBoundary, FirstRejected: resp.firstInvalid(),
				Code: resp.code(),
			},
			fmt.Errorf("splunkhec: HEC returned code %d: %w", resp.code(), perr))
	}
	if o.useACK {
		ackID := resp.ackID()
		if ackID == nil {
			return fmt.Errorf("splunkhec: ack enabled but submit response carried no ackID")
		}
		if err := o.confirmAck(ctx, *ackID); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none beyond the stateless delivery
// client.
func (o *Output) Close(context.Context) error { return nil }

// buildRequest assembles the HEC submit request. Text formats (CEF/LEEF/syslog) go
// to /raw with metadata as query parameters; JSON-ish formats go to /event wrapped
// in the HEC envelope. The channel GUID rides as a header (and as a query param on
// /raw) when ack is enabled.
func (o *Output) buildRequest(n sdk.Notification) (delivery.Request, error) {
	hdr := map[string]string{"Authorization": "Splunk " + o.token}
	if o.channel != "" {
		hdr["X-Splunk-Request-Channel"] = o.channel
	}

	if o.isTextFormat() {
		body, err := o.encode(n)
		if err != nil {
			return delivery.Request{}, err
		}
		q := url.Values{}
		q.Set("sourcetype", o.sourcetype)
		if o.index != "" {
			q.Set("index", o.index)
		}
		if o.host != "" {
			q.Set("host", o.host)
		}
		if o.source != "" {
			q.Set("source", o.source)
		}
		if o.channel != "" {
			q.Set("channel", o.channel)
		}
		hdr["Content-Type"] = "text/plain"
		return delivery.Request{URL: o.endpoint + pathRaw + "?" + q.Encode(), Header: hdr, Body: body}, nil
	}

	payload, err := o.encode(n)
	if err != nil {
		return delivery.Request{}, err
	}
	env := hecEnvelope{
		Event:      json.RawMessage(payload),
		Sourcetype: o.sourcetype,
		Index:      o.index,
		Host:       o.host,
		Source:     o.source,
	}
	if !n.Time.IsZero() {
		env.Time = float64(n.Time.UTC().UnixNano()) / 1e9
	}
	enc, err := json.Marshal(env)
	if err != nil {
		return delivery.Request{}, fmt.Errorf("splunkhec: marshal envelope: %w", err)
	}
	hdr["Content-Type"] = "application/json"
	return delivery.Request{URL: o.endpoint + pathEvent, Header: hdr, Body: enc}, nil
}

// isTextFormat reports whether the configured format is a raw SIEM text format
// (CEF/LEEF/syslog) carried on the /raw endpoint rather than a JSON document on
// /event.
func (o *Output) isTextFormat() bool {
	switch o.format {
	case siemwire.TokenCEF, siemwire.TokenLEEF, siemwire.TokenSyslog:
		return true
	default:
		return false
	}
}

// encode renders n in the configured payload format. JSON is an EXPLICIT case
// and an unrecognized stored value is an error (deny-closed, matching the four
// notification connectors' agreed behavior — the old default branch silently
// mislabeled corrupted internal state as JSON).
func (o *Output) encode(n sdk.Notification) ([]byte, error) {
	switch o.format {
	case siemwire.TokenCEF:
		return []byte(siemfmt.CEF(o.device, n)), nil
	case siemwire.TokenLEEF:
		return []byte(siemfmt.LEEF(o.device, n)), nil
	case siemwire.TokenSyslog:
		return []byte(siemfmt.Syslog5424(o.device, siemfmt.SyslogOptions{Hostname: o.host}, n)), nil
	case siemwire.TokenOTLP:
		return siemfmt.OTLPLogJSON(o.device, n)
	case siemwire.TokenOCSF:
		return siemfmt.OCSF(o.device, n)
	case siemwire.TokenASIM:
		return siemfmt.ASIMAgentEvent(o.device, n)
	case siemwire.TokenJSON:
		return json.Marshal(newJSONView(n))
	default:
		return nil, fmt.Errorf("splunkhec: unrecognized stored format %q", o.format)
	}
}

// confirmAck polls /services/collector/ack until the ackID is confirmed (true) or
// the ack timeout elapses. A confirmed ack means the event reached the configured
// replication factor and is durably indexed.
func (o *Output) confirmAck(ctx context.Context, ackID int64) error {
	deadline := time.Now().Add(o.ackUntil)
	for {
		ok, err := o.pollAck(ctx, ackID)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("splunkhec: indexer ack %d not confirmed within %s", ackID, o.ackUntil)
		}
		if err := o.doSleep(ctx, o.ackEvery); err != nil {
			return err
		}
	}
}

// pollAck posts {"acks":[ackID]} to /ack and reports whether the ackID mapped to
// true in the {"acks":{"<id>":bool}} response.
func (o *Output) pollAck(ctx context.Context, ackID int64) (bool, error) {
	body, err := json.Marshal(map[string][]int64{"acks": {ackID}})
	if err != nil {
		return false, fmt.Errorf("splunkhec: marshal ack request: %w", err)
	}
	hdr := map[string]string{
		"Authorization":            "Splunk " + o.token,
		"Content-Type":             "application/json",
		"X-Splunk-Request-Channel": o.channel,
	}
	res, err := o.client.Send(ctx, delivery.Request{URL: o.endpoint + pathAck, Header: hdr, Body: body})
	if err != nil {
		return false, fmt.Errorf("splunkhec: ack poll: %w", err)
	}
	// A truncated ack answer must not read as "not yet acknowledged". The map lookup
	// below returns false for an absent key, and an ack document cut off at the
	// excerpt budget decodes into an empty or partial map — so a CONFIRMED event
	// would poll until the ack window expired and then be reported as undelivered.
	// Splunk answers a batched poll with one entry per outstanding id, so this
	// document grows with the channel's backlog and is not bounded by our request.
	if !res.BodyComplete {
		return false, fmt.Errorf("splunkhec: ack poll: the response was not read in full (%d bytes), so an acknowledgement could not be ruled in or out: %w",
			len(res.RawBody), errIncompleteResponse)
	}
	var r ackResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Body)), &r); err != nil {
		return false, fmt.Errorf("splunkhec: parse ack response: %w", err)
	}
	return r.Acks[strconv.FormatInt(ackID, 10)], nil
}

// doSleep waits for d honoring ctx, using the injected sleep when present (tests).
func (o *Output) doSleep(ctx context.Context, d time.Duration) error {
	if o.sleep != nil {
		return o.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// hecEnvelope is the Splunk HEC /event request body. Optional metadata is omitted
// when empty (omitempty) so HEC applies the token defaults. Event is raw JSON so the
// already-encoded payload embeds without re-encoding.
type hecEnvelope struct {
	Event      json.RawMessage `json:"event"`
	Time       float64         `json:"time,omitempty"`
	Host       string          `json:"host,omitempty"`
	Source     string          `json:"source,omitempty"`
	Sourcetype string          `json:"sourcetype,omitempty"`
	Index      string          `json:"index,omitempty"`
}

// hecResponse is the HEC submit acknowledgement: {"text","code"[,"ackID"]}. Splunk
// has used both "ackID" and "ackId" historically, so both are accepted.
type hecResponse struct {
	// Text and Code are POINTERS so an absent member is distinguishable from a zero
	// one. code 0 IS success, so a document that simply lacks the member would
	// otherwise read as a confirmed delivery.
	Text *string `json:"text"`
	Code *int    `json:"code"`
	// wellFormed reports that both members HEC always sends were present. It is set
	// by parseHECResponse and is not a wire field.
	wellFormed bool `json:"-"`
	// InvalidEventNumber is Splunk's position of the first event it refused. It is
	// a PREFIX BOUNDARY: everything before it was accepted and everything from it
	// was dropped, which is the most precise attribution HEC offers.
	InvalidEventNumber *int `json:"invalid-event-number"`
	// AckID/AckIDAlt are json.Number so BOTH a bare number and a quoted one decode.
	// Splunk has published the value in both shapes, and the strict *int64 form did
	// not merely lose the ack: unmarshalling the WHOLE response failed, which sent
	// the caller down the "not a HEC status document, the 2xx stands" path and
	// reported a delivery nobody had confirmed. A field this connector treats as
	// optional must never be able to void the verdict of the fields it is not.
	AckID    json.Number `json:"ackID"`
	AckIDAlt json.Number `json:"ackId"`
}

// code returns the reported status, or 0 when the member was absent. Callers that
// need to know whether it was PRESENT must consult wellFormed.
func (r hecResponse) code() int {
	if r.Code == nil {
		return 0
	}
	return *r.Code
}

// firstInvalid returns the prefix boundary Splunk reported, or -1 when it named
// none. It is never defaulted to 0: position zero means "the very first event was
// refused", which is a different and much stronger statement than "unknown".
func (r hecResponse) firstInvalid() int {
	if r.InvalidEventNumber == nil {
		return -1
	}
	return *r.InvalidEventNumber
}

func (r hecResponse) ackID() *int64 {
	for _, raw := range []json.Number{r.AckID, r.AckIDAlt} {
		if raw == "" {
			continue
		}
		v, err := raw.Int64()
		if err != nil {
			// A value present but not an integer is not silently ignored: the caller
			// treats a missing ack as a hard error, and quietly dropping an
			// unparseable one would turn a protocol surprise into a confirmed
			// delivery.
			continue
		}
		return &v
	}
	return nil
}

// ackResponse is the /services/collector/ack poll response: {"acks":{"<id>":bool}}.
type ackResponse struct {
	Acks map[string]bool `json:"acks"`
}

// parseHECResponse parses a HEC submit response and returns an error when the body
// reports a non-zero code (a logical rejection delivered under HTTP 200). An empty
// or unparseable body is treated as success (delivery already confirmed a 2xx, and
// some HEC variants answer with no JSON body).
// errIncompleteResponse marks a response whose body could not be read whole, so
// no delivery verdict can be drawn from it.
var errIncompleteResponse = errors.New("incomplete response body")

func parseHECResponse(body string) (hecResponse, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return hecResponse{}, nil
	}
	var r hecResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return hecResponse{}, nil // not a HEC status document
	}
	// A genuine HEC reply carries at least one of the members HEC defines. Requiring
	// text AND code was too strict: the acknowledgement-enabled submit response is
	// documented as carrying an ackID, and demanding the other two rejected a
	// legitimate answer before the ack could even be polled — turning a working
	// ack-mode destination into an unconfirmable one.
	//
	// What this still refuses is a document that parses but says nothing HEC says: a
	// proxy's error envelope, a load balancer's health page, an empty object. Those
	// carry none of these members, so no verdict is drawn from them.
	r.wellFormed = r.Text != nil || r.Code != nil || r.AckID != "" || r.AckIDAlt != ""
	if r.code() != 0 {
		msg := "non-zero status"
		if r.Text != nil && *r.Text != "" {
			msg = *r.Text
		}
		return r, fmt.Errorf("code %d: %s", r.code(), msg)
	}
	return r, nil
}

// newChannelGUID returns a random RFC 4122 v4 UUID for the HEC request channel
// (which "must be a Globally Unique Identifier (GUID) but can be randomly generated").
func newChannelGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// jsonView is the canonical one-line notification JSON shape (the same projection
// the webhook/SIEM connectors ship).
type jsonView struct {
	Type     string            `json:"type,omitempty"`
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Time     string            `json:"time,omitempty"`
}

// jsonView builds the canonical view from n (constructor kept distinct from the type
// for readability).
func newJSONView(n sdk.Notification) jsonView {
	v := jsonView{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: severityString(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	}
	if !n.Time.IsZero() {
		v.Time = n.Time.UTC().Format(time.RFC3339)
	}
	return v
}

func severityString(s model.Severity) string {
	switch s {
	case model.SeverityInfo:
		return "info"
	case model.SeverityLow:
		return "low"
	case model.SeverityMedium:
		return "medium"
	case model.SeverityHigh:
		return "high"
	case model.SeverityCritical:
		return "critical"
	default:
		return ""
	}
}
