// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import "strings"

// This file is the format-token CATALOG: the one ordered source of the selection
// tokens every Olivares surface uses to name a wire format, and of which subset
// each surface accepts. It exists for the same reason the encoders above it do —
// before it, the token vocabulary lived in at least six independent literal
// copies (the ledger registry in core/audit, the eventing sink validator, four
// notification connectors' private const blocks, a hand-mirrored web array and
// per-connector error strings), the copies had ALREADY diverged (one connector
// accepted eight tokens, its sibling seven), and none of them had a drift guard.
// The ledger's registry comment (core/audit/export.go) records how that story
// ends: "the duplicated literal lists rotted". Every surface now derives its
// accepted set, its ordering, its default and its operator-facing choice list
// from here; the license frontier is the same one the encoders rely on — this is
// the only package both the Apache-2.0 connectors and the AGPL engine may import.
//
// The catalog deliberately models SUBSETS, not one union list: the four
// selection vocabularies are different ON PURPOSE (the ledger export has no
// "json" passthrough; the eventing sink adds it; the notification connectors add
// "asim"; the syslog connector transports only line-oriented dialects), and a
// single flat list would falsely advertise tokens on surfaces that cannot render
// them. ECS and UDM are absent by decision, not omission: they are fixed
// destination schemas consumed by dedicated connectors, never user-selected
// members of these format fields.

// FormatToken names a wire format on a selection surface (an export query, a
// sink_format field, a connector's format option). The token is a contract with
// operators and stored configuration: renaming one is a breaking change and gets
// a CHANGELOG entry, not a quiet edit.
type FormatToken string

// The token vocabulary, one constant per distinct spelling.
//
// The OTLP family carries the product's one deliberate alias: TokenOTLP is the
// canonical name for a spec-complete OTLP/HTTP JSON ExportLogsServiceRequest
// (what a shop POSTing to /v1/logs expects "otlp" to mean), TokenOTLPEnvelope is
// its EXACT alias — same bytes, kept so the spelling that first shipped the
// envelope keeps working — and TokenOTLPLogRecord is the honest name for the
// bare single-LogRecord projection (valuable for file/NDJSON consumption, not
// postable to /v1/logs). Before this catalog the token "otlp" meant the bare
// projection on the ledger surface and the full envelope on the notification
// surfaces at the same time; one token, one wire shape is the contract now, and
// modules/siemforward's bridge tests hold it.
const (
	// TokenJSON is the raw-JSON format — never a SIEM dialect. What it delivers
	// depends on the surface: the eventing sink posts the captured event
	// envelope (the structured passthrough, no dialect transform), while the
	// notification connectors render a minimal notification projection (the
	// displayable fields, not the original payload).
	TokenJSON FormatToken = "json"
	// TokenCEF is ArcSight Common Event Format, one line per event.
	TokenCEF FormatToken = "cef"
	// TokenLEEF is IBM QRadar LEEF 2.0, one line per event.
	TokenLEEF FormatToken = "leef"
	// TokenSyslog is RFC 5424 syslog with structured data, one line per event.
	TokenSyslog FormatToken = "syslog"
	// TokenOTLP is a complete OTLP/HTTP JSON ExportLogsServiceRequest per event.
	TokenOTLP FormatToken = "otlp"
	// TokenOTLPEnvelope is the exact alias of TokenOTLP (see the family note).
	TokenOTLPEnvelope FormatToken = "otlp_envelope"
	// TokenOTLPLogRecord is the bare OTLP LogRecord projection per event.
	TokenOTLPLogRecord FormatToken = "otlp_log_record"
	// TokenOCSF is an OCSF v1.8.0 API Activity JSON projection per event.
	TokenOCSF FormatToken = "ocsf"
	// TokenASIM is a Microsoft Sentinel ASIM AgentEvent JSON row per event.
	TokenASIM FormatToken = "asim"
)

// Canonical resolves the catalog's alias: TokenOTLPEnvelope maps to TokenOTLP;
// EVERY other input — member tokens, unknown tokens, the empty string — returns
// unchanged. Canonical never validates, never trims or lowercases, and never
// applies a surface default: it is for ENCODER SELECTION only, so two spellings
// of the same dialect reach one encoder. Validation is FormatSet.Valid with the
// submitted spelling; persistence and audit records keep the submitted spelling
// too (what the operator wrote is what history shows they wrote).
func Canonical(t FormatToken) FormatToken {
	if t == TokenOTLPEnvelope {
		return TokenOTLP
	}
	return t
}

// FormatSet is one surface's accepted tokens: an ordered subset of the catalog
// with the surface's default. The zero value is not a valid set; obtain one from
// the surface constructors below. Accessors copy — a caller cannot mutate the
// catalog through a returned slice.
type FormatSet struct {
	surface string
	tokens  []FormatToken
	def     FormatToken
}

// Surface names the surface this set governs, for error and log text.
func (s FormatSet) Surface() string { return s.surface }

// Tokens returns the accepted tokens in their canonical presentation order.
// The slice is a defensive copy.
func (s FormatSet) Tokens() []FormatToken {
	out := make([]FormatToken, len(s.tokens))
	copy(out, s.tokens)
	return out
}

// List renders the operator-facing choice list, e.g. "cef|leef|syslog|…".
// Help text, completion and error messages build from here rather than
// repeating the values — repeated values are how the pre-catalog copies rotted.
func (s FormatSet) List() string {
	parts := make([]string, len(s.tokens))
	for i, t := range s.tokens {
		parts[i] = string(t)
	}
	return strings.Join(parts, "|")
}

// Valid reports whether t, AS SUBMITTED, is a member of this surface's set.
// Aliases are members where the surface accepts them — Valid does not
// canonicalize, so an unknown spelling never becomes valid by resolution.
func (s FormatSet) Valid(t FormatToken) bool {
	for _, known := range s.tokens {
		if t == known {
			return true
		}
	}
	return false
}

// Default returns the token this surface uses when the operator selects none.
// The default is part of the surface contract and is always a member of the set.
func (s FormatSet) Default() FormatToken { return s.def }

// LedgerExportFormats is the audit-ledger export surface (the pull export's
// format query and the push renderer's ledger path): every SIEM dialect plus
// both OTLP projections, no raw-JSON passthrough (the ledger's JSON forms ARE
// the two OTLP projections), CEF-first for its established default.
func LedgerExportFormats() FormatSet {
	return FormatSet{
		surface: "ledger export",
		tokens: []FormatToken{
			TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP, TokenOTLPEnvelope,
			TokenOTLPLogRecord, TokenOCSF,
		},
		def: TokenCEF,
	}
}

// EventingSinkFormats is the eventing subscription sink_format surface. It adds
// the raw-JSON passthrough and defaults to OCSF (the SIEM-sink default since
//). It does NOT accept TokenOTLPLogRecord: an eventing sink POSTs one
// rendered body per event to an HTTP destination across ALL selected event
// types, and a bare LogRecord line is not an OTLP /v1/logs request — the
// surface only offers dialects every subscribed event can be delivered in.
// The downstream renderer connectors/siemsink implements THIS surface minus
// TokenJSON: the structured passthrough is intercepted by the eventing engine
// before any SIEM dialect renderer runs, so json never reaches it.
func EventingSinkFormats() FormatSet {
	return FormatSet{
		surface: "eventing sink",
		tokens: []FormatToken{
			TokenOCSF, TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP,
			TokenOTLPEnvelope, TokenJSON,
		},
		def: TokenOCSF,
	}
}

// NotificationConnectorFormats is the notification/file connector surface
// (filelog, splunkhec, s3archive, siem): the full dialect roster including
// ASIM, JSON-first for its established default. siemsink is NOT on this
// surface — it renders for eventing SIEM sinks (see EventingSinkFormats).
func NotificationConnectorFormats() FormatSet {
	return FormatSet{
		surface: "notification connector",
		tokens: []FormatToken{
			TokenJSON, TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP,
			TokenOTLPEnvelope, TokenOCSF, TokenASIM,
		},
		def: TokenJSON,
	}
}

// SyslogConnectorFormats is the syslog transport connector's deliberate narrow
// subset: only line-oriented dialects that are at home inside a syslog MSG
// field. JSON object streams and OTLP requests are not syslog payloads.
func SyslogConnectorFormats() FormatSet {
	return FormatSet{
		surface: "syslog connector",
		tokens:  []FormatToken{TokenSyslog, TokenCEF, TokenLEEF},
		def:     TokenSyslog,
	}
}
