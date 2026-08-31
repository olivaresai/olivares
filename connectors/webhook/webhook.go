// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package webhook is the Olivares AI generic signed-webhook output connector. It
// delivers an sdk.Notification to an operator-supplied HTTP endpoint as a stable
// JSON document, and — when a signing secret is configured — signs each delivery
// with an HMAC-SHA256 signature over a timestamped payload, in the same scheme
// Stripe and GitHub use: the receiver recomputes HMAC(secret, "<ts>.<body>") and
// compares it in constant time, which both authenticates the sender and bounds
// replay (the timestamp is signed, so a captured request cannot be re-dated).
//
// It is minimal-data (docs/SECURITY-HARDENING.md-3): the body carries only the non-sensitive
// Notification fields (type, title, body, severity, tenant, structured fields,
// time). The signing secret is an operator credential declared Secret in the
// config; it is held in memory only, used solely as the HMAC key, and NEVER
// written into the body, a header, a log line, or an error — the delivery layer
// never logs bodies or headers, and this package puts the secret nowhere but the
// hmac.New key. It imports only the SDK and the shared reliable-delivery
// transport, never the engine.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.webhook"

// Header names. They are deliberately vendor-namespaced so a receiver behind a
// shared gateway can tell an Olivares delivery apart.
const (
	headerTimestamp    = "X-Olivares-Timestamp"
	headerSignature    = "X-Olivares-Signature"
	defaultContentTyp  = "application/json"
	defaultMaxAttempts = 4
)

// Delivery formats (E3). The default keeps the historical proprietary Olivares
// payload; "cloudevents" wraps the same minimal-data notification in a CloudEvents
// 1.0.2 envelope (reusing connectors/internal/cloudevents) so an Event Grid / Eventarc
// / EventBridge consumer can route it natively. The X-Olivares-Signature is preserved
// in every format — it always signs the exact delivered body.
const (
	formatOlivares    = "olivares"
	formatCloudEvents = "cloudevents"
	ceModeStructured  = "structured"
	ceModeBinary      = "binary"
	defaultCESource   = "/olivares"
)

// Output is the generic signed-webhook output connector. It satisfies
// sdk.OutputConnector. A single instance is configured once in Open and then
// services every Notify over a shared reliable-delivery client.
type Output struct {
	url         string
	signingKey  string // operator credential; HMAC key only — never logged/persisted
	contentType string
	maxAttempts int
	format      string // "" / "olivares" (default) | "cloudevents"
	ceMode      string // "structured" (default) | "binary" — only when format=cloudevents
	ceSource    string // CloudEvents source URI-reference (default "/olivares")
	client      *delivery.Client
	doer        delivery.Doer                              // optional injected transport (tests); nil => default
	sleep       func(context.Context, time.Duration) error // optional injected sleep (tests)
	now         func() time.Time                           // injectable clock (tests); nil => time.Now
	newID       func() (string, error)                     // injectable CloudEvents id source (tests); nil => random
}

// Compile-time proof that Output satisfies the OutputConnector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a webhook output connector with default configuration.
func New() *Output {
	return &Output{
		contentType: defaultContentTyp,
		maxAttempts: defaultMaxAttempts,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Signed Webhook",
		Description: "Delivers a notification to an HTTP endpoint as stable JSON, HMAC-SHA256 signed (Stripe/GitHub scheme) when a signing secret is set.",
		ConfigFields: []sdk.ConfigField{
			{Key: "url", Type: sdk.FieldString, Required: true, Description: "Destination webhook URL (https). Required."},
			{Key: "signing_secret", Type: sdk.FieldString, Secret: true, Description: "HMAC-SHA256 signing key (never persisted/logged). Empty = send unsigned."},
			{Key: "content_type", Type: sdk.FieldString, Default: defaultContentTyp, Description: "Content-Type header for the delivery body (format=olivares only; CloudEvents sets its own)."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total HTTP delivery attempts including the first (1 = no retry)."},
			{Key: "format", Type: sdk.FieldString, Default: formatOlivares, Description: "Delivery body format: olivares (proprietary JSON, default) | cloudevents (CloudEvents 1.0.2 envelope for Event Grid/Eventarc/EventBridge)."},
			{Key: "cloudevents_mode", Type: sdk.FieldString, Default: ceModeStructured, Description: "CloudEvents content mode (format=cloudevents): structured (whole event in the signed body, default) | binary (context attributes as ce-* headers, data as body)."},
			{Key: "cloudevents_source", Type: sdk.FieldString, Default: defaultCESource, Description: "CloudEvents source URI-reference identifying the producing context (format=cloudevents)."},
		},
	}
}

// Open reads configuration and builds the reliable-delivery client. It fails fast
// if the required url is missing; everything else falls back to a default. The
// signing secret, when present, is retained in memory only as the HMAC key.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.url = strings.TrimSpace(cfg.Get("url"))
	if o.url == "" {
		return fmt.Errorf("webhook: url is required")
	}
	o.signingKey = cfg.Get("signing_secret")
	if v := strings.TrimSpace(cfg.Get("content_type")); v != "" {
		o.contentType = v
	}
	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)

	// Delivery format (E3). Unknown/empty ⇒ the historical olivares format, so an
	// old config never silently changes shape.
	switch strings.ToLower(strings.TrimSpace(cfg.Get("format"))) {
	case formatCloudEvents:
		o.format = formatCloudEvents
	default:
		o.format = formatOlivares
	}
	o.ceMode = ceModeStructured
	if strings.ToLower(strings.TrimSpace(cfg.Get("cloudevents_mode"))) == ceModeBinary {
		o.ceMode = ceModeBinary
	}
	o.ceSource = defaultCESource
	if v := strings.TrimSpace(cfg.Get("cloudevents_source")); v != "" {
		o.ceSource = v
	}

	o.client = delivery.New(o.doer, delivery.Options{
		MaxAttempts: o.maxAttempts,
		Sleep:       o.sleep,
	})
	return nil
}

// payload is the stable on-the-wire shape of a delivered notification. Field
// order in JSON follows struct order; omitempty keeps an absent time/severity out
// of the document so the signed bytes are predictable for the receiver.
type payload struct {
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Time     string            `json:"time,omitempty"`
}

// marshalBody renders a Notification to the stable JSON body. The zero Time is
// omitted; a present Time is encoded as RFC3339 in UTC for a stable, parseable
// form. It carries only non-sensitive Notification fields — never the secret.
func marshalBody(n sdk.Notification) ([]byte, error) {
	p := payload{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: string(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	}
	if !n.Time.IsZero() {
		p.Time = n.Time.UTC().Format(time.RFC3339)
	}
	return json.Marshal(p)
}

// Notify renders the notification, signs it (when a secret is configured) and
// delivers it over the reliable-delivery client. It returns an error when
// delivery ultimately fails (the runtime owns durable retry); a 2xx is success.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("webhook: Notify before Open")
	}
	body, header, err := o.renderBody(n)
	if err != nil {
		return err
	}

	// The X-Olivares-Signature always covers the EXACT delivered body, in every format —
	// for CloudEvents structured mode that is the whole envelope; for binary mode it is
	// the data body (the ce-* context headers are transport metadata, unsigned, exactly
	// as a broker relays them).
	if o.signingKey != "" {
		ts := strconv.FormatInt(o.clock().UTC().Unix(), 10)
		sig := Sign(o.signingKey, ts, body)
		header[headerTimestamp] = ts
		header[headerSignature] = formatSignatureHeader(ts, sig)
	}

	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.url,
		Header: header,
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("webhook: deliver to endpoint: %w", err)
	}
	_ = res // 2xx; nothing further to inspect for a generic webhook
	return nil
}

// renderBody produces the delivery body and the format-specific headers for a
// notification. For the olivares format it is the proprietary JSON with the configured
// Content-Type; for cloudevents it builds a CloudEvents 1.0.2 event (reusing the shared
// package) and renders it in structured mode (whole envelope, application/cloudevents+
// json) or binary mode (ce-* context headers + the data body). It never carries the
// signing secret. The returned header map is the caller's to add the signature headers to.
func (o *Output) renderBody(n sdk.Notification) ([]byte, map[string]string, error) {
	if o.format == formatCloudEvents {
		id, err := o.eventID()
		if err != nil {
			return nil, nil, fmt.Errorf("webhook: cloudevents id: %w", err)
		}
		ev, err := cloudevents.FromNotification(id, o.ceSource, n)
		if err != nil {
			return nil, nil, fmt.Errorf("webhook: build cloudevent: %w", err)
		}
		if o.ceMode == ceModeBinary {
			header, data, err := ev.BinaryHTTP()
			if err != nil {
				return nil, nil, fmt.Errorf("webhook: cloudevents binary: %w", err)
			}
			return data, header, nil
		}
		ct, body, err := ev.StructuredHTTP()
		if err != nil {
			return nil, nil, fmt.Errorf("webhook: cloudevents structured: %w", err)
		}
		return body, map[string]string{"Content-Type": ct}, nil
	}
	body, err := marshalBody(n)
	if err != nil {
		return nil, nil, fmt.Errorf("webhook: marshal notification: %w", err)
	}
	return body, map[string]string{"Content-Type": o.contentType}, nil
}

// eventID returns a fresh unique CloudEvents id (a Notification has none of its own and
// the producer owns uniqueness). Tests inject o.newID for a deterministic id.
func (o *Output) eventID() (string, error) {
	if o.newID != nil {
		return o.newID()
	}
	return newEventID()
}

// newEventID returns 16 cryptographically-random bytes, hex-encoded (the same scheme the
// messaging connectors use for their CloudEvents ids).
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Close releases resources; this connector holds none beyond the shared client.
func (o *Output) Close(context.Context) error { return nil }

// clock returns the connector's time source (injectable for tests).
func (o *Output) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// formatSignatureHeader renders the X-Olivares-Signature value in the
// scheme-versioned form "t=<ts>,v1=<hexsig>", mirroring Stripe's header so a
// receiver can extract the version it understands and ignore future ones.
func formatSignatureHeader(ts, sig string) string {
	return "t=" + ts + ",v1=" + sig
}

// Sign computes the hex HMAC-SHA256 signature over the signed payload
// "<ts>.<body>" with the given secret as the key. It is the exact value placed in
// the v1 component of the signature header and is what Verify recomputes. An empty
// secret still produces a (useless) MAC; callers gate on a non-empty secret.
func Sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify validates a received delivery. It accepts the secret, the timestamp and
// signature exactly as delivered (the signature may be the raw hex v1 value or the
// full "t=...,v1=..." header — both are accepted), and the raw request body, and
// reports whether the signature is authentic. The comparison is constant-time
// (hmac.Equal) so a verifier does not leak how much of the signature matched.
//
// Receivers and tests use this to authenticate an Olivares webhook. It does NOT
// itself enforce timestamp freshness — a receiver should additionally reject a ts
// outside its tolerance window to bound replay (use VerifyWithin) — but because ts
// is part of the signed payload, a tampered timestamp fails verification here.
func Verify(secret, ts, signature string, body []byte) bool {
	if secret == "" {
		return false
	}
	got := extractV1(signature)
	if got == "" {
		return false
	}
	gotRaw, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(Sign(secret, ts, body))
	if err != nil {
		return false
	}
	return hmac.Equal(gotRaw, want)
}

// DefaultReplayWindow is the tolerance VerifyWithin uses by convention and the
// hardened receiver applies to an inbound callback: a delivery whose signed
// timestamp is more than five minutes from the receiver's clock (in either
// direction) is rejected even when the signature is authentic. Five minutes is the
// same bound Slack's verifying-requests guidance recommends.
const DefaultReplayWindow = 5 * time.Minute

// VerifyWithin authenticates a received delivery AND bounds replay. It first checks
// the HMAC signature in constant time (Verify), then rejects a timestamp that is not
// within tolerance of now in EITHER direction — a future-dated timestamp is as
// suspect as a stale one (clock skew is symmetric; a far-future ts is a tampering or
// replay signal). The bare Verify proves authenticity but not freshness, so a
// captured request with a valid signature and an old timestamp would pass Verify;
// VerifyWithin closes that replay window. It is the verifier a hardened inbound
// receiver uses, and it never leaks timing about how much of the signature matched
// (the freshness check runs only after a successful constant-time signature compare).
//
// ts is the timestamp string exactly as delivered (the X-Olivares-Timestamp header,
// equivalently the t= field of the signature header). A non-positive tolerance
// disables the freshness check (signature-only, equivalent to Verify). A timestamp
// that is absent or not an integer fails closed.
func VerifyWithin(secret, ts, signature string, body []byte, now time.Time, tolerance time.Duration) bool {
	if !Verify(secret, ts, signature, body) {
		return false
	}
	if tolerance <= 0 {
		return true
	}
	t, ok := parseUnixTimestamp(ts)
	if !ok {
		return false
	}
	delta := now.Sub(t)
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// parseUnixTimestamp parses a base-10 Unix-seconds timestamp string. It fails closed
// (ok=false) on an empty or non-integer value so a malformed timestamp can never be
// treated as fresh.
func parseUnixTimestamp(ts string) (time.Time, bool) {
	secs, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// SignatureTimestamp extracts the t= field from a full "t=<ts>,v1=<sig>" header, so a
// receiver that has only the signature header (not a separate X-Olivares-Timestamp)
// can recover the timestamp for the freshness check. It returns "" when no t= field
// is present (a bare hex signature carries no timestamp).
func SignatureTimestamp(signature string) string {
	signature = strings.TrimSpace(signature)
	for _, part := range strings.Split(signature, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "t="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// extractV1 pulls the v1 hex signature from either a bare hex string or a full
// "t=<ts>,v1=<sig>[,v2=...]" header. It returns "" when no v1 component is found.
func extractV1(signature string) string {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return ""
	}
	if !strings.Contains(signature, "=") {
		return signature // already a bare hex signature
	}
	for _, part := range strings.Split(signature, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "v1="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
