// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package siemwire is the single, shared, pure-Go encoder for the SIEM text wire
// formats every Olivares feed emits: ArcSight CEF, IBM QRadar LEEF, and RFC 5424
// syslog. It exists to END the format drift OBS-08 flagged: before it, the audit
// ledger export (core/audit/export.go) and the findings/notifications export
// (connectors/internal/siemfmt) each carried their OWN copy of the CEF/syslog
// escaping rules, and the two had already diverged (e.g. one stripped a carriage
// return, the other escaped it). A SIEM that ingests both feeds must not see two
// dialects. This package is the one definition of "how a CEF/LEEF/syslog line is
// built and escaped"; both callers build a record and delegate here.
//
// It lives in the Apache-2.0, zero-third-party-dependency SDK on purpose: that is
// the only place importable by BOTH the Apache-2.0 connectors AND the AGPL-3.0
// engine (which already imports the SDK) without breaching the license frontier
// (LICENSING.md, scripts/check-boundary.sh). It imports only the standard library,
// so the SDK's zero-dependency guarantee is preserved.
//
// Design: the builders are PRIMITIVE and parameterized — the caller supplies the
// device identity, the already-mapped severity, the already-ordered fields, and
// (for syslog) the app/msgid/PRI — so each caller keeps its own field vocabulary
// and severity policy while sharing the one escaping/grammar. Output is
// byte-deterministic for a given input: golden tests pin it, two records can be
// diffed meaningfully, and a re-delivery of the same event is the same payload. Some
// destinations key on that payload; which ones, and how, is destination-specific and
// is not a claim this package makes about SIEMs in general.
//
// OTLP: the JSON wire GRAMMAR, the timestamp conversion and the input validation now
// live here too (otlpjson.go), and they need no dependency at all. An earlier revision
// of this comment said OTLP was "intentionally NOT here" because it would require
// opentelemetry-proto — true of the generated protobuf TYPES, which remain with the
// callers that emit the binary encoding, but not of the JSON grammar, which is a
// declared field layout over the standard library. Keeping it out cost what every
// duplicated encoder in this package's history has cost: the ledger export and the
// notification feed each grew their own OTLP timestamp conversion, and both were wrong
// — the notification feed wrapped a pre-epoch instant into a plausible 2554 date, and
// the ledger formatted the SIGNED nanoseconds, emitting a negative number into a field
// that is unsigned. BOTH feeds now delegate to OTLPTimeUnixNano here — the ledger's
// private copy of the algorithm was retired in — so a later correction to the
// conversion reaches every emitter, and "one definition" is finally a true claim, held
// by the ledger's own boundary tests (core/audit/export_otlp_envelope_test.go), which
// pin this package's behavior from the caller's side.
//
// The package has since grown beyond the three text dialects the paragraph above
// names: the OCSF 1.8.0 encoder (ocsf.go) and the SARIF 2.1.0 encoder (sarif.go)
// live here too, for the same one-definition reason. And the format-token CATALOG
// (catalog.go) makes the naming side match the encoding side: the selection tokens
// every surface advertises, which subset each surface accepts, each surface's
// default, and the one alias in the vocabulary all have a single ordered source,
// so the accepted lists can no longer drift apart the way the escaping rules once
// did.
package siemwire

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultEnterpriseNumber is the IANA Private Enterprise Number used in the RFC
// 5424 structured-data SD-ID when a caller does not supply one. 32473 is the number
// RFC 5612 reserves for documentation/examples; an operator with a registered PEN
// overrides it.
const DefaultEnterpriseNumber = 32473

// Device identifies the emitting product in a CEF/LEEF header. Vendor/Product/
// Version are operator-overridable so a fork or reseller can rebrand the feed.
type Device struct {
	Vendor  string
	Product string
	Version string
}

// DefaultDevice is the Olivares AI device identity used when a caller supplies no
// override (the same identity the findings encoder defaults to).
func DefaultDevice() Device {
	return Device{Vendor: "Olivares.AI", Product: "ControlPlane", Version: "1"}
}

// Field is one already-stringified, already-ordered key/value pair. siemwire never
// sorts: the caller decides field order (the findings encoder sorts alphabetically
// for determinism; the audit encoder uses a fixed semantic order), and siemwire
// only escapes and lays them out.
type Field struct {
	Key   string
	Value string
}

// --- CEF ---------------------------------------------------------------------

// CEF V27 header field limits (Header Field Definitions, Size column). The spec
// publishes the numbers but never says what unit they count — decoded characters
// or the UTF-8 octets on the wire — and defines no receiver behavior for an
// over-length field, so truncateCEFHeaderValue honors BOTH readings rather than
// betting on one. A non-ASCII value therefore fits fewer characters than the
// number suggests; that is the conservative direction, and only the header is
// bounded — the extension, which carries the auditable content, is never
// truncated. Truncation here is silent by design: the header is the record's
// fixed-width identity, and an emitter cannot negotiate it with the receiver.
const (
	cefDeviceVendorMax  = 63
	cefDeviceProductMax = 63
	cefDeviceVersionMax = 31
	cefEventClassIDMax  = 1023
	cefNameMax          = 512
)

var cefHeaderReplacer = strings.NewReplacer(`\`, `\\`, `|`, `\|`)
var cefExtReplacer = strings.NewReplacer(`\`, `\\`, `=`, `\=`, "\n", `\n`, "\r", `\r`)

// foldControls replaces EVERY C0 control and DEL with a space. It is for the
// HEADER fields of CEF and LEEF, which are the record's fixed-width identity: the
// CEF standard forbids multi-line content outside an extension value, an emitter
// cannot negotiate the header with a receiver, and no header field is a hashed
// input — so destroying is correct here in a way it never was for a value.
//
// It replaced a replacer that folded only CR and LF, which left every other
// control on the wire. That was not a smaller version of the same behavior: a NUL
// truncates the record at any receiver that stores the line as a C string, so the
// bytes after it were lost with no error anywhere, and a TAB ends a field for a
// receiver that splits on whitespace.
func foldControls(s string) string {
	if !needsHeaderFold(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			b.WriteByte(' ')
			i++
			continue
		}
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		// A byte that is not part of a well-formed UTF-8 sequence is folded too. It was
		// passed through, which left the whole record invalid UTF-8 — a state several
		// receivers refuse outright, and one the value layer already refuses to create.
		// A header is an identifier rather than a hashed input, so folding is the right
		// treatment here where encoding is the right one for a value.
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(' ')
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

func needsHeaderFold(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// escapeCEFExtValue encodes a CEF extension value. CEF's own standard already
// specifies a reversible encoding for the line breaks — "Multi-line fields can be
// sent by CEF by encoding the newline character as \n or \r" (CEF Implementation
// Standard, What is CEF) — and cefExtReplacer implements exactly that, so the
// standard's spelling is kept rather than replaced.
//
// What the standard says nothing about is every OTHER C0 control. Those reached
// the wire RAW: a NUL truncates the record at a receiver that stores the line as a
// C string, and a TAB or a vertical tab can end a field for a receiver that splits
// on whitespace. They are percent-encoded here, in the same alphabet the syslog
// and LEEF layers use, and the two conventions compose because they share no
// character: a consumer unwinds CEF's backslash escapes and then the percent ones.
//
// The percent pass runs FIRST on encode so it cannot see the backslashes CEF adds,
// and CR/LF are left for CEF's own escape rather than percent-encoded, which is
// what keeps the emitted line conformant with the published standard.
func escapeCEFExtValue(s string) string {
	return cefExtReplacer.Replace(escapeCEFControls(s))
}

// sanitizeCEFKey makes a value safe for a CEF extension KEY. It closes an
// injection this builder had left open: the key was written with no treatment at
// all, while the extension section is SP-separated `key=value` pairs, so a caller
// field named `x y` emitted `… x y=v …` and forged an extension pair — and a field
// name containing '=' moved the key/value boundary outright.
//
// Callers do supply these names: connectors/internal/siemfmt feeds arbitrary
// notification field names through here. A name is an identifier rather than a
// hashed input, so folding is the right treatment — there is nothing to recover —
// but it must be folded, not trusted.
func sanitizeCEFKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '=' || r == ' ' || r == '|' || r == '\\':
			b.WriteByte('_')
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeCEFControls percent-encodes '%', every C0 control EXCEPT CR and LF (which
// the CEF standard's own `\n`/`\r` escape covers), and every byte that is not part
// of a well-formed UTF-8 sequence.
//
// The invalid-UTF-8 arm keeps CEF in step with the other dialects. Without it CEF was
// the ONE projection that put a raw invalid byte on the wire while carrying the same
// `pct-c0-v1` marker as lines that did not — one version token, two encodings — and
// the line was not valid UTF-8, which several receivers require.
func escapeCEFControls(s string) string {
	needs := func(c byte) bool {
		return c == '%' || c == 0x7f || (c < 0x20 && c != '\r' && c != '\n')
	}
	need := !utf8.ValidString(s)
	if !need {
		for i := 0; i < len(s); i++ {
			if needs(s[i]) {
				need = true
				break
			}
		}
	}
	if !need {
		return s
	}
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		c := s[i]
		if needs(c) {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
			i++
			continue
		}
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// CEF builds an ArcSight Common Event Format v0 record:
//
//	CEF:0|Vendor|Product|Version|SignatureID|Name|Severity|Extension
//
// severity is the already-mapped CEF V27 0..10 value (0 Unknown, 1-3 Low,
// 4-6 Medium, 7-8 High, 9-10 Very-High). ext is the ordered extension fields; an
// empty ext yields an empty extension section. The header escapes backslash and
// pipe (collapsing any CR/LF to a space so a crafted value cannot break the
// pipe-delimited header); extension values escape backslash, equals and CR/LF (as
// \r/\n) so a value cannot inject a second key or a second line. Field values are
// already stringified: CEF V27 dictionary time keys such as rt, start and end must
// be supplied by the caller as decimal epoch milliseconds.
func CEF(d Device, signatureID, name string, severity int, ext []Field) string {
	var b strings.Builder
	b.WriteString("CEF:0|")
	b.WriteString(cefHeaderReplacer.Replace(foldControls(truncateCEFHeaderValue(d.Vendor, cefDeviceVendorMax))))
	b.WriteByte('|')
	b.WriteString(cefHeaderReplacer.Replace(foldControls(truncateCEFHeaderValue(d.Product, cefDeviceProductMax))))
	b.WriteByte('|')
	b.WriteString(cefHeaderReplacer.Replace(foldControls(truncateCEFHeaderValue(d.Version, cefDeviceVersionMax))))
	b.WriteByte('|')
	b.WriteString(cefHeaderReplacer.Replace(foldControls(truncateCEFHeaderValue(signatureID, cefEventClassIDMax))))
	b.WriteByte('|')
	b.WriteString(cefHeaderReplacer.Replace(foldControls(truncateCEFHeaderValue(name, cefNameMax))))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(severity))
	b.WriteByte('|')
	for i, f := range ext {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(sanitizeCEFKey(f.Key))
		b.WriteByte('=')
		b.WriteString(escapeCEFExtValue(f.Value))
	}
	return b.String()
}

// --- LEEF --------------------------------------------------------------------

// LEEFDelimiter is the LEEF 2.0 attribute delimiter declared in the header. Tab
// (0x09) is QRadar's default and the most widely compatible.
const LEEFDelimiter = "\t"

var leefHeaderReplacer = strings.NewReplacer(`\`, `\\`, `|`, `\|`)

// escapeLEEFValue encodes a LEEF 2.0 attribute value reversibly, with the SAME
// alphabet as the syslog control layer so a consumer needs one decoder for both
// dialects. LEEF defines no escaping of its own, so there is no second layer here.
//
// It replaced a replacer that substituted a space for TAB, CR and LF. TAB is the
// case that makes encoding the only honest answer rather than a preference: it is
// the DELIMITER this builder declares in the header (0x09), so a raw one inside a
// value does not merely lose a byte — it forges an attribute boundary, and
// everything after it is read by QRadar as a different field. The old code was
// right to keep it off the wire and wrong about how: `%09` can neither split a
// record nor be mistaken for a delimiter, and it can be read back; a space can do
// neither.
//
// It is safe against a receiver that knows nothing about the convention: `%09` is
// three ordinary printable characters, so a parser splitting on the declared
// delimiter sees exactly the attributes the emitter intended. What such a receiver
// does not get is the ORIGINAL byte back, and that is the consumer's decision to
// make with UnescapeControlBytes — it is not information the wire has destroyed.
func escapeLEEFValue(s string) string { return EscapeControlBytes(s) }

// sanitizeLEEFKey makes a value safe for a LEEF attribute NAME. A name is an
// identifier we choose rather than a hashed input, so it is folded rather than
// encoded — but it must never contain '=' (which would move the key/value boundary
// and rename the field) nor any control character (which would forge an attribute
// or break the record).
func sanitizeLEEFKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '=':
			b.WriteByte('_')
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LEEF builds an IBM QRadar LEEF 2.0 record with an explicit tab delimiter:
//
//	LEEF:2.0|Vendor|Product|Version|EventID|0x09|key=val<TAB>key=val...
//
// attrs are the ordered attributes (the caller decides which, including any sev/
// title/msg). Per this package's design the builder does NOT validate them: LEEF
// 2.0 requires sev to be 1-10 and a devTime to be a 10- or 13-digit epoch value
// unless accompanied by devTimeFormat, and honoring that is the caller's job —
// connectors/internal/siemfmt and core/audit/export are the two callers that do.
// What this builder guarantees is the grammar: an attribute NAME is folded so it
// can never move the key/value boundary, and a VALUE is escaped REVERSIBLY
// (escapeLEEFValue) so the tab delimiter, CR, LF and every other C0 control can
// neither inject an extra attribute nor break the record into two — and can still
// be read back byte-for-byte with UnescapeSDValue.
func LEEF(d Device, eventID string, attrs []Field) string {
	var b strings.Builder
	b.WriteString("LEEF:2.0|")
	b.WriteString(leefHeaderReplacer.Replace(foldControls(d.Vendor)))
	b.WriteByte('|')
	b.WriteString(leefHeaderReplacer.Replace(foldControls(d.Product)))
	b.WriteByte('|')
	b.WriteString(leefHeaderReplacer.Replace(foldControls(d.Version)))
	b.WriteByte('|')
	b.WriteString(leefHeaderReplacer.Replace(foldControls(eventID)))
	b.WriteByte('|')
	b.WriteString("0x09|") // declare the tab delimiter
	for i, a := range attrs {
		if i > 0 {
			b.WriteString(LEEFDelimiter)
		}
		b.WriteString(sanitizeLEEFKey(a.Key))
		b.WriteByte('=')
		b.WriteString(escapeLEEFValue(a.Value))
	}
	return b.String()
}

// --- RFC 5424 syslog ---------------------------------------------------------

const (
	// RFC 5424 §6 ABNF: HOSTNAME = NILVALUE / 1*255PRINTUSASCII.
	rfc5424HostnameMaxOctets = 255
	// RFC 5424 §6 ABNF: APP-NAME = NILVALUE / 1*48PRINTUSASCII.
	rfc5424AppNameMaxOctets = 48
	// RFC 5424 §6 ABNF: PROCID = NILVALUE / 1*128PRINTUSASCII.
	rfc5424ProcIDMaxOctets = 128
	// RFC 5424 §6 ABNF: MSGID = NILVALUE / 1*32PRINTUSASCII.
	rfc5424MsgIDMaxOctets = 32
	// RFC 5424 §6 ABNF: SD-NAME = 1*32PRINTUSASCII.
	rfc5424SDNameMaxOctets = 32
)

// Receiver payload budgets an emitter can hold a record against. They are
// REFERENCE VALUES for an operator configuring a destination, not limits this
// package enforces: each is a property of the receiver and its deployment, and
// none of the sources define whether the count includes the syslog header, so
// measure the complete serialized record in UTF-8 octets (the conservative
// reading). Verified against primary sources 2026-07-24; see
// an internal design note (not shipped) §5.
const (
	// RFC5424MinReceiverBytes is the message size every RFC 5424 receiver MUST
	// support (§6.1); RFC5424RecommendedReceiverBytes is the size the RFC says
	// implementations SHOULD support. Beyond it a receiver may truncate or discard.
	RFC5424MinReceiverBytes         = 480
	RFC5424RecommendedReceiverBytes = 2048
	// ArcSightSyslogDaemonMaxPayloadBytes is the size past which ArcSight's own
	// SmartConnector guides say a syslog-daemon message "might be split" — a
	// conditional deployment caveat, not a deterministic receiver rule, and one
	// that does not apply to the file or pipe paths.
	ArcSightSyslogDaemonMaxPayloadBytes = 1024
	// QRadarTCPDefaultMaxPayloadBytes is QRadar's default maximum TCP syslog
	// payload, past which it splits the message (event ID 1003). An operator can
	// raise it — IBM documents 8192 as the supported step and 32000 as the ceiling
	// — so it is a safe default assumption, not a fixed limit.
	QRadarTCPDefaultMaxPayloadBytes = 4096
)

// sdRFCReplacer applies the escaping RFC 5424 §6.3.3 MANDATES inside a
// PARAM-VALUE, and only that: '"', '\' and ']'. It is deliberately unchanged from
// the escaping this package always applied — what changed is that CR and LF no
// longer arrive here as raw bytes to be substituted, because EscapeControlBytes
// has already encoded them.
var sdRFCReplacer = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`)

// escapeSDValue encodes an RFC 5424 SD-PARAM value reversibly, in the two layers
// a consumer unwinds in the opposite order: the control bytes become `%XX`, then
// the RFC's own three characters are backslash-escaped.
//
// The layering is the point. The RFC pass is exactly what the standard specifies,
// so a conforming receiver reads the field correctly with no knowledge of this
// package; the percent pass rides underneath it in an alphabet the RFC pass never
// touches, so unwinding one cannot corrupt the other. The replacer this succeeds
// escaped the same three characters but SUBSTITUTED a space for CR and LF, so
// those two bytes were lost and the ledger's offline-reconstruction claim had to
// be qualified by the alphabet of the values it happened to emit.
func escapeSDValue(s string) string { return sdRFCReplacer.Replace(EscapeControlBytes(s)) }

// escapeMsg encodes the RFC 5424 MSG. MSG is free-form (§6.4) and the RFC defines
// no escaping for it at all, so only the control-byte layer applies — a quote or a
// bracket in a message means nothing to the grammar and is left alone, which keeps
// ordinary prose readable in a SIEM's message column.
//
// It encodes rather than substitutes for the same reason SD-PARAM does, and the
// cost is worth naming: a literal '%' in prose now renders as `%25`. The
// alternative was to keep collapsing a multi-line body into spaces, which drops
// content from a governance feed without saying so — and a reader who sees `%0A`
// can tell a line break was there, whereas a reader who sees a space cannot tell
// that anything happened at all.
func escapeMsg(s string) string { return EscapeControlBytes(s) }

// SyslogRecord is the fully-resolved input to Syslog5424: the caller has already
// computed the PRI (facility*8 + severity) and chosen the app/msgid, so siemwire
// only assembles and escapes. This keeps the per-caller policy (PRI, app name,
// msgid, SD-ID) out of siemwire while the escaping/grammar stays shared.
type SyslogRecord struct {
	// PRI is the already-computed priority (facility*8 + severity).
	PRI int
	// Time is the event time; the zero time renders the NILVALUE "-".
	Time time.Time
	// Hostname is the HOSTNAME field (sanitized to the 255-octet limit); "" renders "-".
	Hostname string
	// AppName is the APP-NAME field (sanitized to the 48-octet limit).
	AppName string
	// ProcID is the PROCID field (sanitized to the 128-octet limit); "" renders "-".
	ProcID string
	// MsgID is the MSGID field (sanitized to the 32-octet limit).
	MsgID string
	// SDID is the structured-data SD-ID (e.g. "olivares@32473"). When empty,
	// DefaultSDID is used; either value is sanitized as an SD-NAME.
	SDID string
	// Params are the SD-PARAM key/value pairs; empty params render the NILVALUE "-"
	// in place of the structured-data element.
	Params []Field
	// Msg is the MSG field.
	Msg string
	// MsgCarriesEncodedRecord declares that Msg is ALREADY a complete record in a
	// self-framing dialect — a CEF:0 or LEEF 2.0 line carried inside a syslog frame,
	// which is how ArcSight and QRadar ingest those formats over syslog.
	//
	// It exists because encoding such a MSG a second time DESTROYS it, and destroys
	// it in a way no receiver can undo. LEEF declares TAB as its attribute delimiter
	// in its own header, so percent-encoding the MSG turns every attribute boundary
	// into the three characters "%09" and a QRadar receiver that splits on 0x09 — as
	// the header instructs it to — sees one attribute where there were six. CEF gets
	// off no more lightly: a literal "%" a user wrote in "50% done" is already "%25"
	// inside the CEF record, and a second pass makes it "%2525".
	//
	// Skipping the pass is SAFE rather than merely necessary: a record built by this
	// package's CEF or LEEF builder contains no raw CR, LF or NUL — the extension and
	// attribute values encode every C0 control and the headers fold them — so the
	// syslog frame it travels in cannot be broken by it. TestCarriedRecordsAreFraming-
	// SafeWithoutASecondPass pins exactly that, because the safety of skipping is a
	// property of the enclosed encoders and not something this flag can assert on its
	// own.
	MsgCarriesEncodedRecord bool
}

// DefaultSDID returns the standard Olivares SD-ID for the given enterprise number.
func DefaultSDID(pen int) string { return "olivares@" + strconv.Itoa(pen) }

// Syslog5424 builds an RFC 5424 syslog record:
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD] MSG
//
// The structured data carries the ordered params under the record's SD-ID; an empty
// params list renders the NILVALUE "-". SD-PARAM values and the MSG are encoded
// REVERSIBLY by escapeSDValue: the three characters RFC 5424 §6.3.3 requires
// (", \, ]) plus CR, LF, TAB and every other C0 control, none of which reaches the
// wire as a raw byte. Nothing is substituted and nothing is dropped, so a consumer
// holding only the rendered line recovers the exact input with UnescapeSDValue —
// which is what lets a line re-derive the ledger hash preimage it also carries.
//
// HEADER identifiers and structured-data names are intentionally different:
// RFC 5424 requires PRINTUSASCII there, so unsupported octets are folded to '_'
// before the field-specific wire limits are applied. Emitting UTF-8 verbatim would
// make the entire record non-conforming, while refusing to frame the event would
// lose more audit data than folding these non-evidentiary identifiers.
//
// This corrects the package's older behavior, where CR and LF were replaced by a
// space and were therefore LOST. That substitution was never required by the RFC:
// §6.3.3 permits control characters outright and only says an application MAY
// modify them, so the loss was this package's choice, and reversibility is the
// better exercise of the same permission.
func Syslog5424(r SyslogRecord) string {
	ts := "-"
	if !r.Time.IsZero() {
		ts = r.Time.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	}
	host := nilValue(r.Hostname, rfc5424HostnameMaxOctets)
	app := nilValue(r.AppName, rfc5424AppNameMaxOctets)
	procid := nilValue(r.ProcID, rfc5424ProcIDMaxOctets)
	msgid := nilValue(r.MsgID, rfc5424MsgIDMaxOctets)
	sdid := r.SDID
	if sdid == "" {
		sdid = DefaultSDID(DefaultEnterpriseNumber)
	}
	sdid = sanitizeSDName(sdid)

	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(strconv.Itoa(r.PRI))
	b.WriteString(">1 ")
	b.WriteString(ts)
	b.WriteByte(' ')
	b.WriteString(host)
	b.WriteByte(' ')
	b.WriteString(app)
	b.WriteByte(' ')
	b.WriteString(procid)
	b.WriteByte(' ')
	b.WriteString(msgid)
	b.WriteByte(' ')

	if len(r.Params) == 0 {
		b.WriteString("-")
	} else {
		b.WriteByte('[')
		b.WriteString(sdid)
		// A folded name can COLLIDE: sanitizeSDName maps both `a=b` and `a]b` onto
		// `a_b`, and emitting both produced two SD-PARAMs with one name in a grammar
		// where a consumer reasonably reads the first — or the last, or errors. The
		// suffix keeps every field present and every name distinct, which is the
		// behavior a caller supplying two fields is entitled to.
		seen := make(map[string]int, len(r.Params))
		for _, p := range r.Params {
			name := uniqueSDName(sanitizeSDName(p.Key), seen)
			b.WriteByte(' ')
			b.WriteString(name)
			b.WriteString(`="`)
			b.WriteString(escapeSDValue(p.Value))
			b.WriteByte('"')
		}
		b.WriteByte(']')
	}

	// `var` y no `:= r.Msg`: las dos ramas de abajo la reasignan, asi que el valor inicial
	// moria sin usarse (ineffassign). No era un bug — pero un valor que se calcula y se tira
	// invita a leer que alguna rama lo conserva, y ninguna lo hace.
	var msgOut string
	if r.MsgCarriesEncodedRecord {
		// Trusted for the ENCODING, never for the FRAMING. A record built by this
		// package's CEF or LEEF builder carries no CR, LF or NUL, so this fold is a
		// no-op for every caller in this tree — and the flag is exported, so "the
		// caller promises" cannot be the only thing standing between a stray line feed
		// and a syslog frame split into two unparseable halves. What it must NOT do is
		// re-encode: that is what destroyed the enclosed dialect's own delimiters.
		msgOut = foldFramingBytes(r.Msg)
	} else {
		msgOut = escapeMsg(r.Msg)
	}
	if msg := msgOut; msg != "" {
		b.WriteByte(' ')
		b.WriteString(msg)
	}
	return b.String()
}

// --- shared helpers ----------------------------------------------------------

// ExceedsTransportBudget reports whether an encoded record is larger than the
// supplied byte budget (Go string length is already a byte count). It is
// deliberately observational and never truncates: what an emitter does with an
// oversize record — refuse it, split it deliberately, or send it anyway — is the
// emitter's policy, and this SDK will not silently shorten an auditable record.
// The two budgets above are the documented receiver defaults an emitter can pass.
func ExceedsTransportBudget(encoded string, budgetBytes int) bool {
	return len(encoded) > budgetBytes
}

// truncateCEFHeaderValue bounds a header value so BOTH readings of the CEF V27
// Size column hold: at most limit decoded runes, AND at most limit UTF-8 octets once
// the header escapes are applied. The spec gives the numbers but never says which
// unit they count (primary-source check 2026-07-24), so satisfying only one
// leaves the other violated at a receiver that reads it the other way.
//
// The bound is `limit`, not `max`: naming it `max` shadowed the predeclared `max`
// builtin inside the one function that has to reason about two competing maxima
// (revive redefines-builtin-id).
//
// The cut is applied to the VALUE and the escapes are added whole afterwards, so
// it can never split a UTF-8 rune, and can never leave a trailing lone backslash
// that would escape the pipe delimiter and swallow the following field — the
// failure mode of cutting the escaped form instead.
func truncateCEFHeaderValue(s string, limite int) string {
	runes, wire := 0, 0
	for i, r := range s {
		w := utf8.RuneLen(r)
		switch {
		case r == '\\' || r == '|':
			w = 2 // escaped to two ASCII octets
		case r == '\n' || r == '\r':
			w = 1 // collapsed to a space
		case w < 0:
			w = 1 // invalid byte, passed through as-is
		}
		if runes+1 > limite || wire+w > limite {
			return s[:i]
		}
		runes, wire = runes+1, wire+w
	}
	return s
}

// nilValue returns RFC 5424's NILVALUE for an empty HEADER identifier and applies
// the caller's field-specific octet budget after sanitization. The limit cannot be
// shared: §6 gives HOSTNAME, APP-NAME, PROCID and MSGID four distinct maxima, and
// shortening the first three to APP-NAME's budget can collapse emitter identities.
func nilValue(s string, maxOctets int) string {
	s = foldToUnderscore(s)
	if len(s) > maxOctets {
		s = s[:maxOctets]
	}
	if s == "" {
		return "-"
	}
	return s
}

// foldToUnderscore keeps RFC 5424 HEADER identifiers inside §6.2's seven-bit
// ASCII requirement and §6's PRINTUSASCII (%d33-126) productions. Folding each
// forbidden octet independently avoids decoding malformed UTF-8, guarantees the
// result itself is ASCII, and makes the subsequent field limit a count of the
// exact octets placed on the wire.
func foldToUnderscore(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < '!' || c > '~' {
			b.WriteByte('_')
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// sanitizeSDName enforces RFC 5424 §6's 1*32PRINTUSASCII production for both
// SD-ID (§6.3.2) and PARAM-NAME (§6.3.3), including the exclusions for '=', SP,
// ']' and '"'. Byte-wise folding makes malformed UTF-8 harmless before it can
// affect framing. An empty identifier becomes "_" instead of dropping its value:
// this string-returning builder must emit a non-empty name, and preserving the
// pair is less destructive than silently discarding it.
func sanitizeSDName(s string) string {
	if s == "" {
		return "_"
	}
	if len(s) > rfc5424SDNameMaxOctets {
		s = s[:rfc5424SDNameMaxOctets]
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < '!' || c > '~' || c == '=' || c == ']' || c == '"':
			b.WriteByte('_')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// foldFramingBytes replaces only the bytes that would BREAK a syslog frame — CR, LF
// and NUL — leaving everything else exactly as the caller supplied it. It is the
// minimum a pre-encoded MSG can be subjected to without altering the dialect it
// carries.
func foldFramingBytes(s string) string {
	if strings.IndexByte(s, '\r') < 0 && strings.IndexByte(s, '\n') < 0 && strings.IndexByte(s, 0) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\r' || c == '\n' || c == 0 {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// uniqueSDName returns a name not already used in this element, appending a numeric
// suffix when the folded name collides. The suffix LOOPS rather than trying once:
// two invalid keys can fold onto one base while a caller also supplies that base with
// the first suffix already appended, so the third needs the second — the same
// reasoning the OTLP attribute layer records for its own key uniqueness.
//
// The result stays inside RFC 5424's 32-octet SD-NAME limit: the base is trimmed to
// make room for the suffix rather than the suffix being dropped, because a name over
// the limit is not a name at all.
func uniqueSDName(base string, seen map[string]int) string {
	if _, dup := seen[base]; !dup {
		seen[base] = 1
		return base
	}
	for n := seen[base]; ; n++ {
		suffix := "_" + strconv.Itoa(n)
		trimmed := base
		if len(trimmed)+len(suffix) > rfc5424SDNameMaxOctets {
			trimmed = trimmed[:rfc5424SDNameMaxOctets-len(suffix)]
		}
		cand := trimmed + suffix
		if _, dup := seen[cand]; !dup {
			seen[base] = n + 1
			seen[cand] = 1
			return cand
		}
	}
}

// --- reversible value encoding -----------------------------------------------

// ControlEncodingVersion names the value encoding EscapeControlBytes produces, so
// an emitted line can say which decoder reads it.
//
// It is not decoration. The encoding CHANGED: a line written before this carried a
// value's bytes verbatim, so the literal text "%0A" meant those three characters,
// while a line written after it means a line feed. A consumer applying this decoder
// to an older line would silently return a value that was never emitted. A version
// on the line is what makes the two distinguishable without out-of-band knowledge of
// which build produced it — and an evidence feed is read by consumers who do not
// have that knowledge, sometimes years later.
const ControlEncodingVersion = "pct-c0-v1"

// EscapeControlBytes encodes the bytes that FRAME a record — every C0 control,
// plus DEL — as `%XX`, and the percent sign itself as `%25`. It is the reversible
// replacement for the space-substitution this package used to apply, and it gives
// two properties the substitution could not give together:
//
//   - no framing byte survives into the output. No CR or LF to split one auditable
//     event into two unparseable halves, no NUL to truncate it at a receiver that
//     stores the line as a C string, no TAB to forge an extra LEEF attribute.
//   - the original bytes are RECOVERABLE from the emitted line alone, which is what
//     lets a rendered line re-derive the ledger hash preimage it also carries.
//
// # Why percent and not backslash
//
// Backslash is the obvious choice and it is the WRONG one here, for a reason that
// only shows up when the layers are composed. RFC 5424 §6.3.3 already defines
// backslash escapes for '"', '\' and ']' inside a PARAM-VALUE, so a conforming
// receiver unescapes those three before anything else sees the value. An
// extension that also spelled a line feed `\n` would then be read AFTER that pass,
// and the two are not separable: a value that genuinely contained the characters
// '\' and 'n' is emitted as `\\n`, which the RFC pass turns into `\n`, which the
// extension pass would decode as a line feed. The value would come back CHANGED,
// silently, and it would still parse.
//
// Percent-encoding uses a character the RFC layer does not touch, so the two
// passes commute and compose exactly. A strictly-conforming receiver that
// unescapes only the three mandated characters hands its consumer a value this
// decoder still reads correctly, and a receiver that knows nothing about either
// convention passes the bytes through unharmed.
//
// # Why this is allowed
//
// §6.3.3 says of control characters: "It MUST NOT fail if control characters are
// present in PARAM-VALUE. The syslog application MAY modify messages containing
// control characters." Modifying them is permitted. What the RFC never asked for
// is modifying them IRREVERSIBLY, which is what this package used to do, so
// reversibility is the better exercise of the same permission rather than a
// departure from it.
func EscapeControlBytes(s string) string {
	if !needsControlEscape(s) {
		return s // the overwhelmingly common case allocates nothing
	}
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		c := s[i]
		if c == '%' || c < 0x20 || c == 0x7f {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
			i++
			continue
		}
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		// A byte that is not part of a well-formed UTF-8 sequence is ENCODED, not
		// passed through and not repaired. Passing it through was the earlier
		// behavior and it was wrong in a way that only shows up at the receiver:
		// RFC 5424 §6.3.3 defines PARAM-VALUE as a UTF-8 string, so a raw invalid
		// byte makes the record unparseable to a conforming reader — including this
		// repository's own decoder, which refuses it. Repairing it into U+FFFD would
		// be worse still: the value would no longer hash to the record carrying it.
		// Encoding is the third option, and the only one that keeps the line both
		// well-formed and reversible.
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// needsControlEscape reports whether EscapeControlBytes would change s, so the
// common case — a UUID, a hex digest, a dotted action verb — returns the input
// string itself instead of rebuilding it.
func needsControlEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '%' || c < 0x20 || c == 0x7f {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// UnescapeControlBytes reverses EscapeControlBytes. It is EXPORTED because a
// reversibility claim is worth nothing without a decoder a consumer can actually
// run: "the bytes are recoverable in principle" is not a property anyone can use.
//
// For an RFC 5424 SD-PARAM the consumer applies the RFC's own unescaping first
// (the three characters §6.3.3 mandates) and this second; the order is fixed and
// the two do not interfere, which is the whole reason the alphabet is percent and
// not backslash. For a LEEF attribute value there is no first pass.
//
// It is STRICT: a truncated or non-hex escape returns ok=false rather than a
// best-effort reading, because this decoder's output feeds a hash comparison. A
// value silently repaired here would produce either a mismatch the consumer
// cannot explain or, worse, a match against a preimage the emitter never hashed.
func UnescapeControlBytes(s string) (string, bool) {
	// A value this encoder produced contains NO raw control byte and IS valid UTF-8 —
	// it encodes both. Either here means the input is not something it emitted.
	// Accepting them made the decoder lenient in the one place leniency costs: its
	// output feeds a hash comparison, and two wire forms decoding to one value is
	// exactly what "canonical" has to exclude.
	//
	// The OUTPUT may of course be invalid UTF-8: `%FF` decodes to 0xFF, which is the
	// whole reason that escape exists. It is the wire form that must be well-formed.
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	if !utf8.ValidString(s) {
		return "", false
	}
	i := strings.IndexByte(s, '%')
	if i < 0 {
		return s, true
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	for ; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		// UPPER-CASE hex only: the encoder emits one spelling, so `%0a` is a form it
		// never produced and accepting it would give one value two wire representations.
		if !isUpperHex(s[i+1]) || !isUpperHex(s[i+2]) {
			return "", false
		}
		hi, ok1 := hexNibble(s[i+1])
		lo, ok2 := hexNibble(s[i+2])
		if !ok1 || !ok2 {
			return "", false
		}
		v := hi<<4 | lo
		// Only escapes this encoder EMITS are accepted. `%41` would otherwise decode
		// to "A", giving two wire spellings for one value — harmless for preimage
		// recomputation, and exactly the kind of latitude that stops being harmless
		// the moment anything downstream compares encoded forms.
		if !isEncoderEscapedByte(v) {
			return "", false
		}
		b.WriteByte(v)
		i += 2
	}
	return b.String(), true
}

// isUpperHex reports whether c is a hex digit in the ONE case the encoder emits.
func isUpperHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

// isEncoderEscapedByte reports whether the escaping side of this pair would have
// percent-escaped v: the escape marker itself, a C0 control, DEL, or any non-ASCII
// octet. The decoder accepts ONLY these, so one value keeps one wire spelling.
//
// It is a named predicate rather than a negated disjunction at the call site
// (staticcheck QF1001) because the negation is the load-bearing half — `%41` must
// NOT decode — and De Morgan's four-term rewrite of it is the shape where a future
// edit inverts a term without anyone noticing. The byte domain is small enough to
// pin exhaustively, and TestIsEncoderEscapedByteDomain does.
func isEncoderEscapedByte(v byte) bool {
	return v == '%' || v < 0x20 || v == 0x7f || v >= utf8.RuneSelf
}

// hexNibble decodes one hex digit in either case.
func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
