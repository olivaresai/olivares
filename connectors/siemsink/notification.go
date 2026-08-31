// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemsink

import (
	"fmt"

	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// device is the SIEM device identity stamped on every notification-derived record.
var device = siemfmt.DefaultDevice()

// formatSet is the surface this renderer serves: the eventing sink_format
// vocabulary from the sdk/siemwire catalog. TokenJSON is a member of that
// surface but never reaches a dialect renderer — the eventing engine
// intercepts the structured passthrough upstream — so EncodeNotification
// rejects it like any non-member. The hand-written switch this replaced was
// one of six diverged format copies.
func formatSet() siemwire.FormatSet { return siemwire.EventingSinkFormats() }

// RenderNotification encodes a minimal-data sdk.Notification into the requested SIEM
// dialect via connectors/internal/siemfmt — the SAME encoders the notify path uses,
// so a forwarded finding and a notified finding are byte-identical on the wire — and
// then wraps it in the sink envelope+auth. format "" defaults to OCSF (the AI-aware
// schema). tags are the non-secret labels a log sink exposes for filtering. This is
// the findings/events arm; the ledger arm uses Render with a body pre-encoded by
// core/audit.FormatEvent (which a connector may not import).
func RenderNotification(s Sink, format string, n sdk.Notification, tags map[string]string) (Request, error) {
	body, isJSON, err := EncodeNotification(format, n)
	if err != nil {
		return Request{}, err
	}
	return Render(s, Event{
		Body: body, BodyIsJSON: isJSON, Message: n.Title,
		Time: n.Time, Source: n.Type, Tags: tags,
	})
}

// EncodeNotification renders a Notification into a SIEM dialect, returning the body
// bytes and whether the body is a JSON document. Exposed so a caller can pre-encode
// for a sink that wants the body alone. The accepted vocabulary derives from the
// catalog's eventing-sink surface (minus the json passthrough); "" resolves to the
// surface default. Spellings arrive here already normalized by the subscription
// layer, so resolution is exact-match on purpose.
func EncodeNotification(format string, n sdk.Notification) ([]byte, bool, error) {
	set := formatSet()
	tok := siemwire.FormatToken(format)
	if format == "" {
		tok = set.Default()
	}
	if !set.Valid(tok) || tok == siemwire.TokenJSON {
		return nil, false, fmt.Errorf("siemsink: unknown notification format %q", format)
	}
	switch siemwire.Canonical(tok) {
	case siemwire.TokenCEF:
		return []byte(siemfmt.CEF(device, n)), false, nil
	case siemwire.TokenLEEF:
		return []byte(siemfmt.LEEF(device, n)), false, nil
	case siemwire.TokenSyslog:
		return []byte(siemfmt.Syslog5424(device, siemfmt.SyslogOptions{}, n)), false, nil
	case siemwire.TokenOTLP:
		// Canonical folds the alias: both spellings render the complete
		// ExportLogsServiceRequest envelope, as they now do on every surface. The
		// pre-remap asymmetry — a bare ledger otlp next to this renderer's enveloped
		// otlp — is gone, and with it the reason the two spellings ever differed.
		b, err := siemfmt.OTLPLogJSON(device, n)
		return b, true, err
	case siemwire.TokenOCSF:
		b, err := siemfmt.OCSF(device, n)
		return b, true, err
	default:
		return nil, false, fmt.Errorf("siemsink: unrecognized stored format %q", format)
	}
}
