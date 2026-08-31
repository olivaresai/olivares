// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/connectors/siemsink"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Renderer implements eventing.SinkRenderer: it re-shapes a captured event into the
// SIEM control tower's native dialect (the body, via core/audit.FormatEvent for the
// ledger or siemfmt-behind-siemsink for findings) and envelope+auth (via
// connectors/siemsink). It holds no state and no credentials — the engine opens the
// sealed credential and passes it in the profile, and owns the transport — so a
// single instance serves every tenant and every sink concurrently. Construct it once
// and wire it with eventing.WithSinkRenderer / UseSinkRenderer.
type Renderer struct{}

// NewRenderer returns the SIEM-sink renderer.
func NewRenderer() *Renderer { return &Renderer{} }

// Compile-time proof Renderer satisfies the engine's seam.
var _ eventing.SinkRenderer = (*Renderer)(nil)

// Render shapes one captured event for one sink. It is deny-closed: an unknown sink
// kind or an unrenderable format returns an error and the engine retries-then-dead-
// letters the delivery (never an unauthenticated or wrong-shaped send).
func (r *Renderer) Render(ev eventing.SinkEvent, p eventing.SinkProfile) (eventing.SinkRequest, error) {
	kind := siemsink.Kind(p.Kind)
	if !kind.Valid() {
		return eventing.SinkRequest{}, fmt.Errorf("siemforward: unknown sink kind %q", p.Kind)
	}
	// Deny-closed on the FORMAT too, not just the kind: the stored spelling must
	// be a member of the eventing surface ("" = the per-sink default). The
	// audit.recorded path below delegates to core/audit.FormatEvent, whose
	// encoder serves the WIDER ledger surface — without this check a corrupted
	// otlp_log_record (a ledger token eventing deliberately does not declare)
	// would deliver a bare LogRecord while every other event type fails.
	if p.Format != "" && !siemwire.EventingSinkFormats().Valid(siemwire.FormatToken(p.Format)) {
		return eventing.SinkRequest{}, fmt.Errorf("siemforward: unrecognized stored sink format %q", p.Format)
	}
	sink := siemsink.Sink{Kind: kind, Endpoint: p.Endpoint, Cred: p.Cred, Opts: p.Opts}

	// "json": a structured passthrough for every type — the minimal-data event
	// envelope, no dialect transform. Useful for a generic collector that parses
	// our shape itself.
	if p.Format == "json" {
		return wrap(siemsink.Render(sink, r.jsonEvent(ev)))
	}

	switch ev.Type {
	case "audit.recorded":
		body, isJSON, message, tags, err := auditBody(ev.Payload, p.Format)
		if err != nil {
			return eventing.SinkRequest{}, err
		}
		return wrap(siemsink.Render(sink, siemsink.Event{
			Body: body, BodyIsJSON: isJSON, Message: message,
			Time: ev.Time, Source: ev.Source, Tags: tags,
		}))
	case "finding.reported":
		n, tags, err := findingNotification(ev.Payload, ev.Tenant, ev.Time)
		if err != nil {
			return eventing.SinkRequest{}, err
		}
		return wrap(siemsink.RenderNotification(sink, p.Format, n, tags))
	default:
		n, tags := genericNotification(r.generic(ev))
		return wrap(siemsink.RenderNotification(sink, p.Format, n, tags))
	}
}

// jsonEvent builds the structured-passthrough siemsink Event for the "json" format.
func (r *Renderer) jsonEvent(ev eventing.SinkEvent) siemsink.Event {
	g := r.generic(ev)
	body, _ := json.Marshal(g)
	return siemsink.Event{
		Body: body, BodyIsJSON: true, Message: ev.Type,
		Time: ev.Time, Source: ev.Source,
		Tags: map[string]string{"tenant": ev.Tenant, "event_type": ev.Type},
	}
}

// generic projects a SinkEvent into the format-neutral envelope used by the json and
// generic paths.
func (r *Renderer) generic(ev eventing.SinkEvent) genericEvent {
	return genericEvent{
		ID: ev.ID, Type: ev.Type, Tenant: ev.Tenant, Source: ev.Source,
		Time: ev.Time.UTC().Format("2006-01-02T15:04:05Z07:00"), Seq: ev.Seq,
		Payload: json.RawMessage(ev.Payload),
	}
}

// wrap converts a siemsink.Request into the engine's SinkRequest (or propagates the
// error).
func wrap(req siemsink.Request, err error) (eventing.SinkRequest, error) {
	if err != nil {
		return eventing.SinkRequest{}, err
	}
	return eventing.SinkRequest{URL: req.URL, Headers: req.Header, Body: req.Body}, nil
}
