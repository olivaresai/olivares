// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Format is an audit export wire format. It is a type ALIAS for the SDK
// catalog's token type — one identity, so the engine, the connectors and the
// catalog cannot hold three diverging notions of "a format name" — while every
// caller keeps compiling against audit.Format and the constants below.
type Format = siemwire.FormatToken

// The supported export formats. Each carries the payload fingerprint and chain-
// integrity fields (payload_hash, seq, prev_hash, hash, sig) so an external WORM/SIEM
// can correlate content without receiving it, check the chain LINKAGE offline
// (prev_hash of n+1 equals hash of n) and verify a checkpoint signature over hash.
//
// It ALSO carries every input canon.EventHash consumes, so a consumer holding one
// emitted line can recompute this event's chain hash: every dialect includes the
// canonical occurred_at TEXT (the exact bytes that are hashed; the per-dialect epoch
// conversions are lossy, three of them down to milliseconds) and the stored metadata
// COMMITMENT — blinded per record, so it discloses nothing about the metadata behind
// it while remaining exactly the value the preimage consumes.
//
// That recomputation is UNCONDITIONAL for syslog, CEF and LEEF: it does not depend on
// which bytes the values carry, invalid UTF-8 included. The three OTLP spellings stay
// CONDITIONAL on UTF-8 validity and that limit is theirs, not this encoder's: they
// carry every field as a JSON string, and encoding/json replaces an invalid byte with
// U+FFFD, so the line no longer reproduces the hash printed beside it. Proven, not
// assumed — TestReconstructionRejectsWhatItCannotCarry seals an event whose actor
// holds a lone 0xFF and requires the text dialects to recover it and the OTLP ones to
// be excluded. It used to depend
// on exactly that, because the text dialects substituted a space for the bytes they
// could not frame — CR and LF in a syslog SD-PARAM, and in LEEF the TAB that is its
// own declared delimiter — so a value carrying one produced a line that could no
// longer reproduce the hash printed beside it, silently. Control bytes now travel
// percent-encoded (siemwire.EscapeControlBytes) underneath each dialect's own
// escaping, in an alphabet those escapes never touch, so the passes compose and a
// consumer unwinds them in a fixed order. Proven against a real store round trip by
// TestReconstructionHoldsForValuesCarryingFramingBytes, which seals an event whose
// values carry CR, LF, TAB, NUL and both escape introducers.
//
// OCSF is the one text projection still excluded, for a reason no encoding change
// reaches: it gives actor and action no verbatim `unmapped` channel and defaults an
// empty actor to the device product, so the line does not CARRY the inputs.
//
// Three claims stay distinct and no surface may collapse them into "independent
// verification": (1) PREIMAGE RECOMPUTATION — the carried fields rebuild the carried
// hash, which is what these projections now support; (2) SIGNATURE AUTHENTICITY —
// verifying the Ed25519 checkpoint signature needs an externally trusted key, which
// no line carries; (3) CHAIN COMPLETENESS — detecting omission or reordering needs
// adjacent records and a checkpoint, so a single line cannot show it. The
// archive path remains the artifact that carries the metadata itself alongside its
// blind (core/audit/archive.go), which is the strictly stronger position: it can
// also answer WHICH metadata a commitment covers.
// The CEF/LEEF/syslog grammar and escaping are the shared sdk/siemwire encoder —
// the SAME one the findings export uses (connectors/internal/siemfmt) — so the
// ledger and the findings feed speak one SIEM dialect (OBS-08: no format drift).
const (
	// FormatCEF is ArcSight Common Event Format (one line per event).
	FormatCEF = siemwire.TokenCEF
	// FormatLEEF is IBM QRadar LEEF 2.0 (one line per event). Added in OBS-08 so a
	// QRadar/LEEF shop can ingest the tamper-evident chain, which it could not before.
	FormatLEEF = siemwire.TokenLEEF
	// FormatSyslog is RFC 5424 syslog with structured data (one line per event).
	FormatSyslog = siemwire.TokenSyslog
	// FormatOTLP is one complete OTLP/HTTP JSON `ExportLogsServiceRequest` per
	// event: the resource identity, the instrumentation scope and the record a
	// collector needs, POSTable to /v1/logs as-is. This is what the catalog
	// remap made the token mean: until then "otlp" named the BARE LogRecord
	// projection on this surface while meaning the full envelope on the
	// notification surfaces — one token, two wire shapes, pinned as a known
	// defect by modules/siemforward's bridge test until the remap flipped it.
	// One token, one shape is the contract now; the bare projection kept its
	// bytes under FormatOTLPLogRecord below. Nothing is published, so the
	// meaning change was free exactly once — the CHANGELOG names it, and stored
	// eventing subscriptions that selected otlp for audit.recorded events are
	// called out by a startup warning (the pre-1.0 breaking-correction policy in
	// sessions-format-catalog.md).
	//
	// Two transports carry the ledger's OTLP forms and NEITHER is blocked: the
	// offline /v1/audit/export pull and the server→collector PUSH the
	// eventing engine drives via core/audit.Forwarder (forward.go). Still open,
	// and not blockers: an OTLP/gRPC (:4317) lane on top of today's HTTP push
	//, and the generic eventing push does not yet read
	// ExportLogsServiceResponse.partialSuccess, so it counts any 2xx as
	// delivered (modules/eventing/dispatch.go:471-473); the dedicated otlplog
	// connector does parse it.
	FormatOTLP = siemwire.TokenOTLP
	// FormatOTLPEnvelope is the EXACT alias of FormatOTLP — same bytes, held by
	// a byte-equality test. It was the spelling that first shipped the envelope
	// (when "otlp" still meant the bare projection here); it stays because
	// nothing is removed and the spelling is harmlessly explicit. New
	// configuration should say otlp; FormatEvent resolves the alias via
	// siemwire.Canonical at dispatch, so both spellings reach one encoder.
	FormatOTLPEnvelope = siemwire.TokenOTLPEnvelope
	// FormatOTLPLogRecord is the minimal OTLP-logs LogRecord *projection* (one
	// JSON object per event): the field shape mirrors a single OTLP LogRecord,
	// NOT a full request envelope — valuable for file/NDJSON consumption, not
	// POSTable to /v1/logs. These are byte-for-byte the bytes the token "otlp"
	// produced on this surface before the catalog remap; the golden that pins
	// them (TestBareOTLPProjectionExactBytes) moved token, not bytes. The
	// history of this shape is unchanged: the shared timestamp guard corrected
	// its out-of-domain bytes (see otlpTimeUnixNano), and the namespace freeze
	// plus the canonical occurred_at text moved the in-domain bytes once,
	// deliberately (see otlpEventAttributes).
	FormatOTLPLogRecord = siemwire.TokenOTLPLogRecord
	// FormatOCSF is an OCSF v1.8.0 API Activity (6003) JSON projection (one object
	// per event) so a SOC that ACCEPTS OCSF 1.8.0 reads the tamper-evident chain
	// without a bespoke parser (OBS-02). The integrity fields ride under the OCSF
	// `unmapped` container, never re-derived.
	//
	// This is NOT native Amazon Security Lake ingest. AWS states it in one sentence
	// — "For custom sources, Security Lake supports OCSF version 1.3 and earlier" —
	// written as Apache Parquet under a partitioned prefix
	// (https://docs.aws.amazon.com/security-lake/latest/userguide/custom-sources.html,
	// consulted 2026-08-02 by re-checked 2026-08-06). This projection is 1.8.0
	// JSON, so it does not land there as-is. Athena over a lake you load yourself is
	// fine; Security Lake native ingest is a declared gap, not an oversight — the
	// public reference page has said so in all seven locales since 2026-07-24, and
	// scripts/check-ocsf-claims.sh now fails the build if this comment loses the limit.
	FormatOCSF = siemwire.TokenOCSF
)

// ledgerFormats is this surface's slice of the sdk/siemwire CATALOG — the one
// ordered source every surface (this registry, the eventing sink validator, the
// notification connectors, the console) derives from. The registry pattern
// started here (the duplicated literal lists rotted: the CLI once advertised
// three formats while the engine accepted five, hiding the LEEF export from
// every operator who read --help) and unit C lifted it into the SDK so the
// Apache-side connectors, which may not import /core, derive from the same
// source instead of keeping the private copies that had already diverged.
var ledgerFormats = siemwire.LedgerExportFormats()

// Formats returns the supported export formats in their canonical order. Callers
// that must render a list to an operator (help text, completion, error messages)
// should build it from here rather than repeating the values.
func Formats() []Format {
	return ledgerFormats.Tokens()
}

// FormatList renders the supported formats as an operator-facing choice list,
// e.g. "cef|leef|syslog|otlp|otlp_envelope|otlp_log_record|ocsf".
func FormatList() string {
	return ledgerFormats.List()
}

// ValidFormat reports whether f, as submitted, is a supported export format.
// The alias spelling is a member of the set; an unknown spelling never becomes
// valid by canonicalization (that happens only at encoder dispatch).
func ValidFormat(f Format) bool {
	return ledgerFormats.Valid(f)
}

// DefaultFormat is the ledger-export surface's default token, applied wherever
// a caller leaves the format unspecified (the export API, the forensic case
// export, the CLI flag default). One derivation replaces the hand copies that
// each restated "cef" separately.
func DefaultFormat() Format {
	return ledgerFormats.Default()
}

// cefVersion is the CEF/LEEF "Device Version" header — the engine's audit schema rev.
