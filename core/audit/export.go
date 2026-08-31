// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
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
const cefVersion = "1.0"

// syslogPEN is the structured-data enterprise number (IANA example PEN; a real
// registered PEN is a release-time concern).
const syslogPEN = 32473

// auditDevice is the device identity stamped on every CEF/LEEF audit record.
var auditDevice = siemwire.Device{Vendor: "Olivares", Product: "ControlPlane", Version: cefVersion}

// FormatEvent renders one audit event in the requested format. Output is
// byte-deterministic for a given event (golden-testable). It never emits Meta or any
// request/response content; PayloadHash is the only payload-derived value.
// sealedForExport reports whether an event carries the integrity fields every
// projection promises. It checks what the WIRE claims — the commitment, the chain
// linkage and a real sequence — not what the store happens to hold: a caller that
// fabricated an event, or forwarded a degrade-spool sentinel, must not be able to
// publish a line that a consumer would try to verify.
// carriesCommitment is the present-IFF-reconstructable rule, in one place so the
// five dialects cannot drift from each other.
//
// A line carries the metadata commitment exactly when the value is HIDING — that
// is, when the row was sealed with a blind. The tempting shorter test, "carry it
// whenever it is non-empty", is wrong in the direction that matters: the row
// decoder resolves a commitment for EVERY row, so a pre-blinding row arrives here
// holding its legacy unblinded digest, and that digest is a deterministic function
// of the metadata alone. Emitting it would publish a dictionary-checkable value —
// and publish it precisely on the records that predate the protection, which is
// the worst possible place to leak one.
//
// The consequence is stated rather than hidden: a legacy row's line cannot be
// reconstructed offline, because the input its hash consumes is the very value
// that must not travel. Reconstruction is a property of blinded records, and the
// export says so by omitting the key rather than by emitting a value that lies.
func carriesCommitment(ev model.AuditEvent) bool {
	return ev.MetaBlinded && len(ev.MetaCommitment) > 0
}

func sealedForExport(ev model.AuditEvent) error {
	switch {
	case ev.Seq <= 0:
		return fmt.Errorf("audit: refusing to export an unsealed event (seq %d)", ev.Seq)
	// ABSENT is legal and meaningful; WRONG-WIDTH never is. A row sealed before
	// metadata blinding existed has no commitment to carry, and the projections omit
	// the field entirely for it rather than emit an empty one — see the comment on
	// the field builders. A present-but-short value is a programming error and must
	// not reach the wire.
	case len(ev.MetaCommitment) != 0 && len(ev.MetaCommitment) != sha256.Size:
		return fmt.Errorf("audit: refusing to export an event whose metadata commitment is %d bytes, want 0 or %d",
			len(ev.MetaCommitment), sha256.Size)
	// A blinded row must actually carry its commitment: claiming the hiding rule
	// while carrying nothing would silently drop the one field that makes the line
	// reconstructable, and no caller could tell the difference from a legacy row.
	case ev.MetaBlinded && len(ev.MetaCommitment) != sha256.Size:
		return fmt.Errorf("audit: refusing to export an event marked blinded whose metadata commitment is %d bytes, want %d",
			len(ev.MetaCommitment), sha256.Size)
	case len(ev.PayloadHash) != 0 && len(ev.PayloadHash) != sha256.Size:
		return fmt.Errorf("audit: refusing to export an event whose payload hash is %d bytes, want 0 or %d",
			len(ev.PayloadHash), sha256.Size)
	case len(ev.Hash) != sha256.Size:
		return fmt.Errorf("audit: refusing to export an event whose chain hash is %d bytes, want %d",
			len(ev.Hash), sha256.Size)
	case len(ev.PrevHash) != sha256.Size:
		return fmt.Errorf("audit: refusing to export an event whose prev hash is %d bytes, want %d",
			len(ev.PrevHash), sha256.Size)
	}
	return nil
}

func FormatEvent(ev model.AuditEvent, f Format) (string, error) {
	// Deny-closed on an unsealed or half-populated event, BEFORE choosing an
	// encoder. Every projection renders the metadata commitment as hex, and a nil
	// one renders as the empty string — a line that looks like evidence, decodes to
	// an empty digest, gets zero-padded by the preimage encoder and then fails
	// reconstruction in a way indistinguishable from tampering. Refusing to emit is
	// the only honest answer: the alternative is publishing pseudo-evidence that
	// accuses a legitimate ledger.
	//
	// This also catches the zero AuditEvent that the degrade-spool path returns
	// beside a nil error, which no producer should project but nothing else stops.
	if err := sealedForExport(ev); err != nil {
		return "", err
	}
	// Canonical resolves the catalog's one alias (otlp_envelope → otlp) so both
	// spellings reach the same encoder — byte equality between them is a held
	// test, not a hope. It never widens validity: an unknown token passes
	// through unchanged and falls to the error below.
	switch siemwire.Canonical(f) {
	case FormatCEF:
		return formatCEF(ev), nil
	case FormatLEEF:
		return formatLEEF(ev), nil
	case FormatSyslog:
		return formatSyslog(ev), nil
	case FormatOTLP:
		return formatOTLPEnvelope(ev)
	case FormatOTLPLogRecord:
		return formatOTLPLogRecord(ev)
	case FormatOCSF:
		return formatOCSF(ev)
	default:
		return "", fmt.Errorf("audit: unknown export format %q", f)
	}
}

// cefExtFields builds the ordered CEF/LEEF extension carrying the event plus the
// payload fingerprint and chain-integrity fields. The order is fixed (semantic,
// not sorted) so output is deterministic and an operator reads the same layout
// every time. olvSig is present only when the event was signed (a checkpoint),
// exactly as before.
func cefExtFields(ev model.AuditEvent) []siemwire.Field {
	fields := make([]siemwire.Field, 0, 16)
	// The wire-encoding version, FIRST, so a consumer knows which decoder the rest of
	// the line was written with before it reads any of it.
	//
	// It exists because the encoding changed. A line sealed before carried a
	// value's bytes verbatim, so the literal text "%0A" meant those three characters;
	// a line sealed after it means a line feed. Without a marker the two are
	// indistinguishable, and a consumer applying the new decoder to an old line would
	// silently return a value that was never emitted — the same class of failure as
	// the substitution this replaced, arriving from the other direction.
	fields = append(fields, siemwire.Field{Key: "olvWireEnc", Value: siemwire.ControlEncodingVersion})
	// An event with no recorded time gets no time on the wire: Go's zero time is
	// year 1, whose epoch milliseconds are a large negative number, and a SIEM
	// told "1754" is worse off than a SIEM left to fall back to receipt time.
	if !ev.OccurredAt.Time().IsZero() {
		fields = append(fields, siemwire.Field{Key: "rt", Value: strconv.FormatInt(ev.OccurredAt.Time().UnixMilli(), 10)})
	}
	fields = append(fields, []siemwire.Field{
		// olvOccurredAt differs from rt in KIND, not just precision: it is the
		// ledger's canonical layout text — the exact bytes canon.EventHash hashes
		// (core/internal/store/canon/canon.go:106, core/model/time.go:16),
		// RFC3339-shaped and fixed-width for four-digit years; an extreme year
		// renders wider yet stays framing-safe (TestExtremeYearsStayFramingSafe-
		// InEveryDialect). Carried verbatim like olvHash, so a consumer holding
		// this line holds the hashed input. rt is the SIEM's event-time
		// semantics, lossy milliseconds and omitted when unknown; olvOccurredAt
		// is evidence and always present, because the zero Timestamp hashes as
		// its canonical epoch-zero text, not as "no value".
		{Key: "olvOccurredAt", Value: ev.OccurredAt.String()},
		{Key: "externalId", Value: ev.ID.String()},
		{Key: "suser", Value: ev.Actor},
		{Key: "act", Value: ev.Action},
		{Key: "olvSeq", Value: strconv.FormatInt(ev.Seq, 10)},
		{Key: "olvTenant", Value: ev.TenantID.String()},
		{Key: "olvActorKind", Value: ev.ActorKind},
		{Key: "olvTargetKind", Value: string(ev.TargetKind)},
		{Key: "olvTargetId", Value: ev.TargetID.String()},
	}...)
	// PRESENT IFF RECONSTRUCTABLE, and in its fixed position. A row sealed before
	// metadata blinding existed has no commitment, and emitting an empty one would be
	// pseudo-evidence: a consumer would decode it to an empty digest, the preimage
	// encoder would zero-pad it, and the reconstruction would fail in a way
	// indistinguishable from tampering. Exporting that row's UNBLINDED digest instead
	// is worse still — it is a deterministic function of the metadata alone, so it
	// hands a holder of the line the dictionary attack the blind exists to prevent,
	// on exactly the records that predate the protection. Absence states the true
	// thing: this line does not support single-line re-derivation.
	if carriesCommitment(ev) {
		fields = append(fields, siemwire.Field{
			Key: "olvMetaCommitment", Value: hex.EncodeToString(ev.MetaCommitment)})
	}
	fields = append(fields, []siemwire.Field{
		{Key: "olvPayloadHash", Value: hex.EncodeToString(ev.PayloadHash)},
		{Key: "olvPrevHash", Value: hex.EncodeToString(ev.PrevHash)},
		{Key: "olvHash", Value: hex.EncodeToString(ev.Hash)},
	}...)
	if len(ev.Sig) > 0 {
		fields = append(fields, siemwire.Field{Key: "olvSig", Value: base64.StdEncoding.EncodeToString(ev.Sig)})
	}
	return fields
}

// formatCEF renders the event as an ArcSight CEF line via the shared encoder. The
// SignatureID and Name are the action; severity is informational (3); the extension
// carries the chain-integrity fields.
func formatCEF(ev model.AuditEvent) string {
	return siemwire.CEF(auditDevice, ev.Action, ev.Action, 3, cefExtFields(ev))
}

// formatLEEF renders the event as an IBM QRadar LEEF 2.0 line via the shared
// encoder, carrying the SAME chain-integrity extension as CEF plus a sev attribute,
// so a QRadar shop can ingest and re-verify the tamper-evident chain (OBS-08).
func formatLEEF(ev model.AuditEvent) string {
	attrs := []siemwire.Field{{Key: "sev", Value: "3"}}
	if !ev.OccurredAt.Time().IsZero() {
		// LEEF 2.0: a 13-digit epoch devTime needs no devTimeFormat. Omitted for an
		// event with no recorded time, for the reason given in cefExtFields.
		attrs = append(attrs, siemwire.Field{Key: "devTime", Value: strconv.FormatInt(ev.OccurredAt.Time().UnixMilli(), 10)})
	}
	return siemwire.LEEF(auditDevice, ev.Action, append(attrs, cefExtFields(ev)...))
}

// formatSyslog renders the event as an RFC 5424 syslog line via the shared encoder.
// PRI 134 = local0(16)*8 + info(6); the structured data carries the chain-integrity
// fields under the olivares@<PEN> SD-ID; the MSG is the action.
func formatSyslog(ev model.AuditEvent) string {
	params := []siemwire.Field{
		// See cefExtFields: the decoder version comes first, because a consumer must
		// know how to read the values before it reads them.
		{Key: "wire_enc", Value: siemwire.ControlEncodingVersion},
		{Key: "seq", Value: strconv.FormatInt(ev.Seq, 10)},
		{Key: "tenant", Value: ev.TenantID.String()},
		// The canonical hashed text, verbatim. The RFC 5424 TIMESTAMP up in
		// the header cannot carry it: TIME-SECFRAC allows at most 6 fractional
		// digits and the shared encoder emits exactly 6 (siemwire.go:255),
		// while the hash is over the 9-digit text. See cefExtFields on
		// olvOccurredAt for the evidence-vs-event-time distinction.
		{Key: "occurred_at", Value: ev.OccurredAt.String()},
		{Key: "actor", Value: ev.Actor},
		{Key: "actor_kind", Value: ev.ActorKind},
		{Key: "action", Value: ev.Action},
		{Key: "target_kind", Value: string(ev.TargetKind)},
		{Key: "target_id", Value: ev.TargetID.String()},
	}
	// PRESENT IFF RECONSTRUCTABLE — see cefExtFields.
	if carriesCommitment(ev) {
		params = append(params, siemwire.Field{
			Key: "meta_commitment", Value: hex.EncodeToString(ev.MetaCommitment)})
	}
	params = append(params, []siemwire.Field{
		{Key: "payload_hash", Value: hex.EncodeToString(ev.PayloadHash)},
		{Key: "prev_hash", Value: hex.EncodeToString(ev.PrevHash)},
		{Key: "hash", Value: hex.EncodeToString(ev.Hash)},
		{Key: "sig", Value: base64.StdEncoding.EncodeToString(ev.Sig)},
	}...)
	return siemwire.Syslog5424(siemwire.SyslogRecord{
		PRI:     134,
		Time:    ev.OccurredAt.Time(),
		AppName: "olivares",
		MsgID:   "audit",
		SDID:    siemwire.DefaultSDID(syslogPEN),
		Params:  params,
		Msg:     ev.Action,
	})
}

// otlpLog is a minimal OTLP-logs LogRecord projection. Field order is fixed so
// json.Marshal is deterministic.
type otlpLog struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	SeverityText string         `json:"severityText"`
	Body         otlpValue      `json:"body"`
	Attributes   []otlpKeyValue `json:"attributes"`
}

type otlpValue struct {
	StringValue string `json:"stringValue"`
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

// otlpTimeUnixNano encodes an instant the way OTLP's two timestamp fields require,
// and it is the ONLY place either OTLP format derives a time from. It delegates to
// siemwire.OTLPTimeUnixNano — the SAME guarded encoder the notification feed uses
// (connectors/internal/siemfmt) — so the repository has ONE definition of this
// conversion, which the earlier private copy here deliberately did not claim.
//
// The full rationale and the verified boundary witnesses live on siemwire.OTLPTime:
// OTLP's timestamp fields are UNSIGNED 64-bit nanoseconds with 0 meaning "unknown",
// time.UnixNano is int64 and undefined outside 1678-2262, a pre-epoch instant wraps
// NEGATIVE (the official decoder rejects the request loudly), and an instant past
// 2554-07-21T23:34:33.709551615Z wraps modulo 2^64 onto values that can decode
// cleanly as a plausible FALSE date (2554-07-21T23:34:34Z reads back as
// 1970-01-01T00:00:00.290448384Z). Those behaviors stay pinned HERE, against this
// package's own output, by TestOTLPEnvelopeCoversTheWholeUnsignedTimestampDomain
// and TestNeitherOTLPFormatEmitsANegativeTimestamp — the tests survive the
// delegation precisely so a regression in either layer is caught at this boundary.
// 0 remains reserved for instants with no OTLP representation: the zero time,
// anything pre-epoch, and anything past the uint64 ceiling. The record's
// attributes still carry the ledger's canonical occurred_at text, and the ledger
// itself remains authoritative.
func otlpTimeUnixNano(t time.Time) string {
	return strconv.FormatUint(siemwire.OTLPTimeUnixNano(t), 10)
}

// otlpEventTimeUnixNano is the timestamp of the record in BOTH OTLP formats — the
// bare LogRecord projection and the request envelope — so the two can never
// disagree about what an instant is.
func otlpEventTimeUnixNano(ev model.AuditEvent) string {
	return otlpTimeUnixNano(ev.OccurredAt.Time())
}

// otlpEventAttributes is the ordered record-level attribute set shared by both
// OTLP formats: the ledger's own vocabulary, carrying the chain-integrity fields
// verbatim. The keys live under ai.olivares.audit.* — the product's reverse-DNS
// namespace, the same root the envelope's resource (ai.olivares.tenant.id), the
// notification feed's reserved namespace (connectors/internal/siemfmt) and the
// CloudEvents type prefix already use. They were olivares.audit.* until
// froze the namespace: read as reverse DNS that spelling claims the TLD "audit",
// and nothing is published, so the rename is free exactly once — now. The hash
// DOMAIN SEPARATORS (olivares.audit.v1 and friends) are NOT attribute names and
// are deliberately untouched: changing one changes every signature and chain
// hash derived under it.
//
// occurred_at carries the canonical layout text canon.EventHash hashes —
// timeUnixNano is an epoch conversion with no representation outside the uint64
// domain; the attribute is evidence (see cefExtFields on olvOccurredAt). The
// order is fixed (semantic, not sorted) so both formats stay byte-deterministic
// for a given event.
func otlpEventAttributes(ev model.AuditEvent) []otlpKeyValue {
	attrs := []otlpKeyValue{
		{"ai.olivares.audit.seq", otlpValue{strconv.FormatInt(ev.Seq, 10)}},
		{"ai.olivares.audit.tenant", otlpValue{ev.TenantID.String()}},
		{"ai.olivares.audit.occurred_at", otlpValue{ev.OccurredAt.String()}},
		{"ai.olivares.audit.actor", otlpValue{ev.Actor}},
		{"ai.olivares.audit.actor_kind", otlpValue{ev.ActorKind}},
		{"ai.olivares.audit.target_kind", otlpValue{string(ev.TargetKind)}},
		{"ai.olivares.audit.target_id", otlpValue{ev.TargetID.String()}},
	}
	// PRESENT IFF RECONSTRUCTABLE — see cefExtFields for why absence beats both an
	// empty value and a pre-blinding row's unblinded digest.
	if carriesCommitment(ev) {
		attrs = append(attrs, otlpKeyValue{
			"ai.olivares.audit.meta_commitment", otlpValue{hex.EncodeToString(ev.MetaCommitment)}})
	}
	attrs = append(attrs, []otlpKeyValue{
		{"ai.olivares.audit.payload_hash", otlpValue{hex.EncodeToString(ev.PayloadHash)}},
		{"ai.olivares.audit.prev_hash", otlpValue{hex.EncodeToString(ev.PrevHash)}},
		{"ai.olivares.audit.hash", otlpValue{hex.EncodeToString(ev.Hash)}},
		{"ai.olivares.audit.sig", otlpValue{base64.StdEncoding.EncodeToString(ev.Sig)}},
	}...)
	return attrs
}

// --- OTLP/HTTP JSON request envelope -----------------------------------------

// auditEmitterVersion is the version of the audit EMITTING component — the wire
// contract of these records — not the running binary's CalVer. It deliberately
// tracks cefVersion (the audit schema revision) so every dialect stamps the same
// identity, and it is a constant so replaying a historical event after an upgrade
// still renders the same bytes (a SIEM de-duplicates on them). The ledger event
// carries no producer version of its own by design (core/model/audit.go:11-13);
// stamping the build version here would require sealing one into the event first.
const auditEmitterVersion = cefVersion

// otlpScopeName identifies the emitter as an instrumentation scope. The Go
// import path is the convention for a first-party emitter.
const otlpScopeName = "github.com/olivaresai/olivares/core/audit"

// otlpSeverityText / otlpSeverityNumber: the ledger records governance facts, not
// failures, so every record is informational. severityNumber is the numeric OTLP
// enum (9 = INFO); a backend cannot filter on severityText alone, which is why the
// bare projection's text-only shape is not enough for the envelope.
const (
	otlpSeverityText   = "INFO"
	otlpSeverityNumber = 9
)

// The envelope mirrors opentelemetry.proto.collector.logs.v1.ExportLogsServiceRequest
// in its ProtoJSON form. It is hand-rolled rather than built with the generated
// types + protojson for two reasons: protojson does not promise byte-stable output
// across library versions (the ledger export must be byte-deterministic), and the
// same shape has to be emitted by a package that stays free of that dependency in
// its non-test build. A test decodes the output with the OFFICIAL generated type
// and unknown fields rejected, so the hand-rolled shape cannot drift from the spec
// silently. No omitempty anywhere on this path: a zero value must have one byte
// shape, not two.
type otlpExportLogsServiceRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope      otlpScope         `json:"scope"`
	LogRecords []otlpEnvelopeLog `json:"logRecords"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// otlpEnvelopeLog is the LogRecord as it appears INSIDE the envelope. The 64-bit
// time fields are pre-formatted strings: ProtoJSON encodes 64-bit integers as
// strings, and a bare JSON number also loses precision in JavaScript-class
// consumers. severityNumber is an enum, so it stays a number.
type otlpEnvelopeLog struct {
	TimeUnixNano         string         `json:"timeUnixNano"`
	ObservedTimeUnixNano string         `json:"observedTimeUnixNano"`
	SeverityNumber       int32          `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 otlpValue      `json:"body"`
	Attributes           []otlpKeyValue `json:"attributes"`
}

// formatOTLPEnvelope renders the event as one complete OTLP/HTTP JSON export
// request. The resource carries the emitter's identity plus the tenant: semconv
// has no general-purpose tenant attribute (its `tenant.id` means an Active
// Directory tenant, which this is not), so the tenant travels under a key in our
// own reverse domain. The event-level `ai.olivares.audit.tenant` attribute stays
// too — the resource key adds identity, it does not rename the ledger's
// vocabulary.
func formatOTLPEnvelope(ev model.AuditEvent) (string, error) {
	when := otlpEventTimeUnixNano(ev)
	req := otlpExportLogsServiceRequest{
		ResourceLogs: []otlpResourceLogs{{
			Resource: otlpResource{Attributes: []otlpKeyValue{
				{"service.name", otlpValue{auditDevice.Product}},
				{"service.version", otlpValue{auditEmitterVersion}},
				{"ai.olivares.tenant.id", otlpValue{ev.TenantID.String()}},
			}},
			ScopeLogs: []otlpScopeLogs{{
				Scope: otlpScope{Name: otlpScopeName, Version: auditEmitterVersion},
				LogRecords: []otlpEnvelopeLog{{
					TimeUnixNano: when,
					// Equal to TimeUnixNano ON PURPOSE, and the equality is a
					// verified fact rather than a convenience: the ledger's
					// OccurredAt is SERVER-ASSIGNED — AuditDraft carries no caller
					// time field and the store stamps its own clock at append
					// (core/model/audit.go:25-27; sqlstore buildEvent sets
					// OccurredAt: a.clock.Now()) — so the instant the event
					// occurred IS the instant Olivares' collection system observed
					// it, which is exactly the value the OTel Logs Data Model says
					// a first-party event gives ObservedTimestamp at generation.
					// An draft replaced this with 0 ("unknown") on the theory
					// that the export observes nothing and the next collector
					// would stamp its own; the adversarial review killed both
					// halves — the value was never invented here, and the stock
					// Collector OTLP receiver passes records through WITHOUT
					// backfilling a zero observed time. The notification feed
					// (connectors/internal/siemfmt) legitimately differs: its
					// Notification.Time is SOURCE-supplied, the feed did not
					// observe the event at that instant, so copying it there WOULD
					// fabricate. The two producers treat the field differently
					// because their inputs' provenance differs, not because one of
					// them is wrong.
					ObservedTimeUnixNano: when,
					SeverityNumber:       otlpSeverityNumber,
					SeverityText:         otlpSeverityText,
					Body:                 otlpValue{StringValue: ev.Action},
					Attributes:           otlpEventAttributes(ev),
				}},
			}},
		}},
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func formatOTLPLogRecord(ev model.AuditEvent) (string, error) {
	rec := otlpLog{
		TimeUnixNano: otlpEventTimeUnixNano(ev),
		SeverityText: otlpSeverityText,
		Body:         otlpValue{StringValue: ev.Action},
		Attributes:   otlpEventAttributes(ev),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// formatOCSF renders the event as an OCSF v1.8.0 API Activity (6003) JSON object via
// the shared sdk/siemwire encoder — the SAME encoder the findings feed uses. The
// payload fingerprint and chain-integrity fields (payload_hash/seq/prev_hash/hash/sig)
// ride under the OCSF `unmapped` container, carried verbatim and never re-derived
// (docs/SECURITY-HARDENING.md,§5; OBS-02/08).
func formatOCSF(ev model.AuditEvent) (string, error) {
	actID, actName := auditOCSFActivity(ev.Action)
	// Same namespace freeze as otlpEventAttributes; occurred_at is the
	// canonical hashed text (the OCSF `time` field is the class's own epoch view
	// of the instant). The tenant key is ai.olivares.tenant.id — one spelling for
	// the authoritative tenant across every projection, matching the OTLP
	// envelope's resource and the notification feed.
	unmapped := map[string]any{
		"ai.olivares.audit.seq":          ev.Seq,
		"ai.olivares.audit.occurred_at":  ev.OccurredAt.String(),
		"ai.olivares.audit.prev_hash":    hex.EncodeToString(ev.PrevHash),
		"ai.olivares.audit.hash":         hex.EncodeToString(ev.Hash),
		"ai.olivares.audit.actor_kind":   ev.ActorKind,
		"ai.olivares.audit.target_kind":  string(ev.TargetKind),
		"ai.olivares.audit.target_id":    ev.TargetID.String(),
		"ai.olivares.audit.payload_hash": hex.EncodeToString(ev.PayloadHash),
		"ai.olivares.tenant.id":          ev.TenantID.String(),
	}
	// PRESENT IFF RECONSTRUCTABLE — see cefExtFields.
	if carriesCommitment(ev) {
		unmapped["ai.olivares.audit.meta_commitment"] = hex.EncodeToString(ev.MetaCommitment)
	}
	if len(ev.Sig) > 0 {
		unmapped["ai.olivares.audit.sig"] = base64.StdEncoding.EncodeToString(ev.Sig)
	}
	// API Activity 6003 registers the ai_operation profile in 1.8.0 (verified
	//); audit events carry no profile attributes, so none are declared.
	// The former cloud.provider field is gone: `cloud` belongs to the cloud
	// profile and did not validate against the 1.8.0 class schema.
	b, err := siemwire.OCSF(siemwire.OCSFInput{
		ActivityID:   actID,
		ActivityName: actName,
		SeverityID:   1, // audit events are informational
		StatusID:     1, // a recorded action succeeded
		Time:         ev.OccurredAt.Time(),
		Message:      ev.Action,
		Device:       auditDevice,
		Operation:    ev.Action,
		ActorAppName: ev.Actor,
		SrcName:      ev.Actor,
		Unmapped:     unmapped,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// auditOCSFActivity maps an audit action verb onto the OCSF 6003 activity_id.
func auditOCSFActivity(action string) (int, string) {
	switch {
	case strings.Contains(action, "create"):
		return 1, "Create"
	case strings.Contains(action, "read") || strings.Contains(action, "export"):
		return 2, "Read"
	case strings.Contains(action, "update") || strings.Contains(action, "upsert") || strings.Contains(action, "write"):
		return 3, "Update"
	case strings.Contains(action, "delete"):
		return 4, "Delete"
	default:
		return 99, "Other"
	}
}
