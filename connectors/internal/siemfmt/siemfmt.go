// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package siemfmt encodes an sdk.Notification into the wire formats an enterprise
// SIEM ingests: ArcSight CEF, IBM QRadar LEEF, RFC 5424 syslog, OpenTelemetry OTLP
// logs, and (for AI-aware SOCs) OCSF and Microsoft Sentinel ASIM. It is the
// formatting half of the SIEM output connector; the WORM / immutable-external-copy
// guarantee (docs/SECURITY-HARDENING.md) is the operator keeping these records outside the product,
// and this package's job is to emit each format exactly to spec so that copy is
// parseable.
//
// The CEF/LEEF/syslog grammar and escaping are NOT implemented here: they live in
// the shared, pure-Go sdk/siemwire so the audit-ledger export (core/audit/export)
// and this findings export emit ONE dialect, ending the OBS-08 format drift. This
// package maps an sdk.Notification onto siemwire's primitives (deciding field order
// and severity) and keeps the dependency-bearing formats (OTLP-proto, OCSF/ASIM
// JSON) local.
//
// Two invariants make the output trustworthy:
//   - Deterministic field order. Notification.Fields is a Go map; this package
//     sorts keys before emitting, so the same notification always produces
//     byte-identical output — golden tests pin it, two records can be diffed, and a
//     re-delivery is the same payload. Whether a given destination keys on that
//     payload is destination-specific and not claimed here.
//   - Correct per-format escaping (delegated to siemwire). A value that contains a
//     format metacharacter cannot break the record or inject a second event.
//
// Minimal data (docs/SECURITY-HARDENING.md): a Notification already carries only non-sensitive,
// displayable fields, so this package adds no enrichment that could leak — it
// formats what it is given and nothing else.
package siemfmt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Device identifies the emitting product in a SIEM record header. The SIEM
// connector builds one from operator config (vendor/product/version are operator-
// overridable so a fork or a reseller can rebrand the feed).
type Device struct {
	// Vendor is the device vendor (CEF/LEEF header field 1/2).
	Vendor string
	// Product is the device product.
	Product string
	// Version is the device/product HEADER version (CEF header field 4, LEEF's
	// devVersion). An operator may set it to a reseller's branding revision, so it is
	// NOT the service version: OTLP's service.version means the service's API or
	// implementation version, and ServiceVersion carries that separately.
	Version string
	// ServiceVersion is the RUNNING service's version, for OTLP's service.version. It is
	// deliberately separate from Version and has no default: when it is unknown the OTLP
	// resource omits service.version entirely, because an absent attribute is honest and
	// a device header revision presented as the service version is not.
	ServiceVersion string
}

// DefaultDevice is the Olivares AI device identity used when the operator does
// not override it.
func DefaultDevice() Device {
	return Device{Vendor: "Olivares.AI", Product: "ControlPlane", Version: "1"}
}

func (d Device) orDefault() Device {
	def := DefaultDevice()
	if d.Vendor == "" {
		d.Vendor = def.Vendor
	}
	if d.Product == "" {
		d.Product = def.Product
	}
	if d.Version == "" {
		d.Version = def.Version
	}
	return d
}

// wire converts the (defaulted) device identity to the shared siemwire form.
func (d Device) wire() siemwire.Device {
	d = d.orDefault()
	return siemwire.Device{Vendor: d.Vendor, Product: d.Product, Version: d.Version}
}

// kv is one ordered, already-stringified field pair.
type kv struct{ k, v string }

// orderedFields returns the notification's fields as a deterministically ordered
// slice: the Fields map sorted by key, then the synthetic "tenant" key appended
// only when a provided field has not already claimed it (a caller's explicit
// field always wins). The notification Type is NOT added here: every format has a
// dedicated place for it — CEF SignatureID, LEEF EventID, syslog MSGID, and OTLP's
// LogRecord.event_name — so repeating it as a field would be redundant in all of
// them. Empty values are kept (a SIEM key with an empty value is meaningful), but
// empty keys are dropped.
func orderedFields(n sdk.Notification) []kv {
	keys := make([]string, 0, len(n.Fields))
	for k := range n.Fields {
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys)+1)
	seen := make(map[string]struct{}, len(keys)+1)
	for _, k := range keys {
		out = append(out, kv{k, n.Fields[k]})
		seen[k] = struct{}{}
	}
	if _, ok := seen["tenant"]; !ok && n.Tenant != "" {
		out = append(out, kv{"tenant", n.Tenant})
	}
	return out
}

// wireFields converts the ordered kv slice to siemwire fields.
func wireFields(in []kv) []siemwire.Field {
	out := make([]siemwire.Field, 0, len(in))
	for _, f := range in {
		out = append(out, siemwire.Field{Key: f.k, Value: f.v})
	}
	return out
}

// severityMap is the single source of truth mapping the product's severity scale
// onto each SIEM format's scale. It is documented in the contract so a SIEM
// rule author knows exactly how a "high" finding lands.
type severityMap struct {
	cef    int                   // CEF severity 0..10
	syslog int                   // RFC 5424 severity 0..7
	otlp   logspb.SeverityNumber // OTLP SeverityNumber
	text   string                // OTLP SeverityText / human label
	ecs    int                   // Elastic ECS event.severity (numeric, reuses the CEF 0..10 scale)
	udm    string                // Google Chronicle UDM security_result.severity enum ("" = omit)
}

func mapSeverity(s model.Severity) severityMap {
	switch s {
	case model.SeverityInfo:
		return severityMap{cef: 1, syslog: 6, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_INFO, text: "INFO", ecs: 1, udm: "INFORMATIONAL"}
	case model.SeverityLow:
		// RFC 5424 notice(5), "normal but significant condition": the product's five
		// severities stay distinct in the syslog column (see the injectivity test).
		// A CEF-band mapping would put low on info(6) with SeverityInfo, and a
		// collector selector cannot recover a distinction the PRI no longer carries.
		return severityMap{cef: 3, syslog: 5, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_INFO2, text: "LOW", ecs: 3, udm: "LOW"}
	case model.SeverityMedium:
		return severityMap{cef: 5, syslog: 4, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_WARN, text: "MEDIUM", ecs: 5, udm: "MEDIUM"}
	case model.SeverityHigh:
		return severityMap{cef: 7, syslog: 3, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, text: "HIGH", ecs: 7, udm: "HIGH"}
	case model.SeverityCritical:
		// RFC 5424 crit(2). Neither CEF nor RFC 5424 defines a CEF-severity-to-PRI
		// mapping (verified 2026-07-24, an internal design note (not shipped)
		// verification.md §2); the only vendor mapping that exists — OpenText's
		// configurable transport.cefsyslog.header.severitymap, default 7,6,5,3,2 —
		// puts very-high on crit(2) as well. alert(1) would be a product policy
		// change with no conformance gain, breaking PRI-literal rules downstream.
		return severityMap{cef: 10, syslog: 2, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, text: "CRITICAL", ecs: 10, udm: "CRITICAL"}
	default:
		// Unknown/empty: honest CEF Unknown (0 — V27 relabelled 0 from Low to
		// Unknown), informational syslog and OTLP UNSPECIFIED. LEEF applies its own
		// 1-10 floor at its caller below, since LEEF 2.0 has no value for "unknown".
		return severityMap{cef: 0, syslog: 6, otlp: logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, text: "UNKNOWN", ecs: 0, udm: ""}
	}
}

// --- CEF ---------------------------------------------------------------------

// CEF encodes n as an ArcSight Common Event Format v0 record (grammar + escaping in
// siemwire). SignatureID is the notification Type, Name is the Title, Severity is
// the 0..10 mapping, and the extension is the ordered fields plus a "msg" key
// carrying the body. CEF V27 time-valued Notification.Fields such as rt, start
// and end must already contain decimal epoch milliseconds.
func CEF(d Device, n sdk.Notification) string {
	sev := mapSeverity(n.Severity)
	ext := wireFields(orderedFields(n))
	if n.Body != "" {
		ext = append(ext, siemwire.Field{Key: "msg", Value: n.Body})
	}
	return siemwire.CEF(d.wire(), n.Type, n.Title, sev.cef, ext)
}

// --- LEEF --------------------------------------------------------------------

// LEEF encodes n as an IBM QRadar LEEF 2.0 record (grammar + escaping in
// siemwire). EventID is the notification Type; attributes are sev (the LEEF
// severity), epoch-millisecond devTime when Time is set, the title, the ordered
// fields and the body.
func LEEF(d Device, n sdk.Notification) string {
	sev := mapSeverity(n.Severity)
	attrs := make([]siemwire.Field, 0, len(n.Fields)+4)
	leefSeverity := sev.cef
	if leefSeverity < 1 {
		leefSeverity = 1 // LEEF 2.0: sev is an integer in the inclusive range 1-10.
	}
	attrs = append(attrs, siemwire.Field{Key: "sev", Value: strconv.Itoa(leefSeverity)})
	if !n.Time.IsZero() {
		// LEEF 2.0: a 13-digit epoch devTime needs no devTimeFormat.
		attrs = append(attrs, siemwire.Field{Key: "devTime", Value: strconv.FormatInt(n.Time.UnixMilli(), 10)})
	}
	if n.Title != "" {
		attrs = append(attrs, siemwire.Field{Key: "title", Value: n.Title})
	}
	for _, f := range orderedFields(n) {
		// LEEF 2.0 reserves sev/devTime/devTimeFormat for the values this encoder
		// derives from the typed notification. Caller metadata never overrides them
		// — including when the notification has no timestamp, where an unvalidated
		// caller devTime would otherwise become the record's event time — and is
		// never silently dropped either: it travels under an olv-prefixed key, the
		// same convention the ledger export uses for its own fields.
		if k := reservedLEEFKey(f.k); k != "" {
			attrs = append(attrs, siemwire.Field{Key: k, Value: f.v})
			continue
		}
		attrs = append(attrs, siemwire.Field{Key: f.k, Value: f.v})
	}
	if n.Body != "" {
		attrs = append(attrs, siemwire.Field{Key: "msg", Value: n.Body})
	}
	return siemwire.LEEF(d.wire(), n.Type, attrs)
}

// reservedLEEFKey returns the olv-prefixed key a caller field must travel under
// when its name collides with a LEEF 2.0 attribute the encoder owns, or "" when
// the field passes through unchanged.
//
// The comparison is case-INSENSITIVE deliberately. IBM enumerates the exact
// camelCase names but never states whether a receiver matches attribute keys
// case-sensitively (checked 2026-07-24), and devTime, when recognized, takes
// precedence over the syslog timestamp — so a caller field differing only in case
// could silently redate the event on a receiver that folds case. Re-keying costs
// a caller nothing (the value survives verbatim under olv*), guessing wrong costs
// the event its time.
func reservedLEEFKey(k string) string {
	switch {
	case strings.EqualFold(k, "sev"):
		return "olvSev"
	case strings.EqualFold(k, "devTime"):
		return "olvDevTime"
	case strings.EqualFold(k, "devTimeFormat"):
		return "olvDevTimeFormat"
	default:
		return ""
	}
}

// --- RFC 5424 syslog ---------------------------------------------------------

// SyslogOptions tunes a syslog record beyond the device identity.
type SyslogOptions struct {
	// Hostname is the HOSTNAME field; "-" (NILVALUE) when empty.
	Hostname string
	// Facility is the syslog facility (0..23); default local0 (16).
	Facility int
}

// Syslog5424 encodes n as an RFC 5424 syslog record (grammar + escaping in
// siemwire). PRI is facility*8+severity; APP-NAME is the device product; MSGID is
// the notification Type; the structured data carries the ordered fields; and MSG is
// "Title — Body".
func Syslog5424(d Device, opts SyslogOptions, n sdk.Notification) string {
	msg := strings.TrimSpace(n.Title)
	if n.Body != "" {
		if msg != "" {
			msg += " — "
		}
		msg += n.Body
	}
	// The default MSG is FREE TEXT (a title and a body an operator or a producer
	// wrote), so it is encoded like any other value. Only the carrier path below
	// declares its MSG pre-encoded.
	return syslogRecord(d, opts, n, msg, false)
}

// SyslogWithMsg builds an RFC 5424 syslog record for n with a caller-supplied MSG
// instead of the default "Title — Body". Its purpose is to carry an alternative
// wire payload — a CEF:0 or LEEF 2.0 record — inside a spec-correct syslog frame,
// which is exactly how ArcSight and QRadar ingest those formats over syslog
//. The PRI (facility*8+severity), timestamp, APP-NAME, MSGID and
// the structured-data params are still derived from n, so the syslog envelope is
// identical across formats and a rule author keys on the same SD-ID.
//
// The MSG is passed through VERBATIM: it is declared as an already-encoded record
// (MsgCarriesEncodedRecord), so the syslog layer applies no value encoding of its
// own. Applying one would destroy the enclosed dialect — percent-encoding a LEEF
// record turns its declared TAB delimiters into "%09" and a QRadar receiver sees one
// attribute instead of many, and it re-encodes a CEF value's escapes a second time.
// CEF and LEEF already keep every framing byte off the wire themselves, so there is
// nothing left for this layer to protect against.
func SyslogWithMsg(d Device, opts SyslogOptions, n sdk.Notification, msg string) string {
	return syslogRecord(d, opts, n, msg, true)
}

// syslogRecord is the shared body. carriesEncodedRecord distinguishes the two
// callers, and getting it wrong is destructive in BOTH directions: a free-text MSG
// left unencoded can break the frame with a raw CR/LF, and an already-encoded record
// passed through the encoder loses the enclosed dialect's own framing.
func syslogRecord(d Device, opts SyslogOptions, n sdk.Notification, msg string, carriesEncodedRecord bool) string {
	dev := d.orDefault()
	sev := mapSeverity(n.Severity)
	facility := opts.Facility
	// 0 means "unset": default to local0 (16). Facility 0 (kernel) is not a
	// meaningful source for an application's alert feed, so an operator who wants a
	// specific facility passes a positive value; the zero value gets the default.
	if facility <= 0 || facility > 23 {
		facility = 16 // local0
	}
	return siemwire.Syslog5424(siemwire.SyslogRecord{
		PRI:                     facility*8 + sev.syslog,
		Time:                    n.Time,
		Hostname:                opts.Hostname,
		AppName:                 dev.Product,
		MsgID:                   n.Type,
		Params:                  wireFields(orderedFields(n)),
		Msg:                     msg,
		MsgCarriesEncodedRecord: carriesEncodedRecord,
	})
}

// --- OTLP logs ---------------------------------------------------------------

// The OTLP instrumentation scope. It names the component that PRODUCES these records —
// this shared encoder — not the transport that ships them: five connectors render through
// OTLPLogJSON (filelog, splunkhec with format=otlp, s3archive, siemsink and the generic
// siem output) and otlplog projects the same resolved request itself, so naming any one
// transport would be false for the others.
//
// The version is this ENCODER's own and moves whenever the emitted shape changes, so a
// backend can tell which layout produced a record. It is deliberately NOT Device.Version,
// which an operator can set to a reseller's CEF header revision. It starts at "2" because
// "1" is the shape emitted before the event_name/reserved-namespace/timestamp-fallback
// change: the first layout is retired, not unversioned.
const (
	otlpScopeName    = "ai.olivares.siemfmt"
	otlpScopeVersion = "2"
)

// Product-owned OTLP attribute keys. Reverse-DNS namespaced, like the ledger export's
// resource attributes (ai.olivares.tenant.id) and the CloudEvents type prefix, so a
// product key can never collide with a caller's field name or with an OpenTelemetry
// semantic convention.
const (
	// otlpAttrTenant is the authoritative tenant from Notification.Tenant. It is a
	// product key precisely so a caller field named "tenant" cannot replace it.
	otlpAttrTenant = "ai.olivares.tenant.id"
	// otlpAttrEventTimeSeconds and otlpAttrEventTimeNanos carry the authoritative instant
	// as machine-comparable integers whenever OTLP's own timestamp field cannot express
	// it. Signed seconds plus a 0..999999999 remainder is exact over the whole time.Time
	// domain Unix() reports, needs no arbitrary-precision arithmetic, and — unlike a
	// "would-be" uint64 — cannot recreate the wrapped value this work exists to remove.
	otlpAttrEventTimeSeconds = "ai.olivares.event.time.unix_seconds"
	otlpAttrEventTimeNanos   = "ai.olivares.event.time.nanos"
	// otlpAttrEventTimeRFC3339 is the human form, emitted ONLY when it is genuinely RFC
	// 3339. time.Format(RFC3339Nano) happily produces "10000-01-02T…" and "-0001-01-02T…"
	// for years outside the four-digit range, which no RFC 3339 parser accepts — so the
	// value is verified by parsing it back before it is emitted.
	otlpAttrEventTimeRFC3339 = "ai.olivares.event.time.rfc3339"
	// otlpAttrEventTimeStatus says WHY the timestamp field does not carry the instant,
	// using siemwire's stable status token.
	otlpAttrEventTimeStatus = "ai.olivares.event.time.status"
	// otlpAttrAdjustments is the structured record of every alteration made so the
	// request could be encoded at all: one entry per adjustment, each naming the exact
	// location, the operation, the emitted value and the ORIGINAL bytes. A comma-joined
	// string cannot do this — a caller's own key may contain the delimiter, and a string
	// cannot carry bytes that were not valid UTF-8 in the first place.
	otlpAttrAdjustments = "ai.olivares.wire.adjustments"
	// otlpAttrAdjustmentCount is the same information reduced to one number, for a SIEM
	// rule that wants to alert on "this record was altered" without parsing structure.
	otlpAttrAdjustmentCount = "ai.olivares.wire.adjustment.count"
	// otlpAttrDeviceVendor is the CEF/LEEF device vendor. It is NOT service.namespace:
	// that convention means a deployment grouping, and putting display branding in it
	// would give a standard key the wrong meaning.
	otlpAttrDeviceVendor = "ai.olivares.device.vendor"
	// otlpAttrDeviceVersion is the CEF/LEEF device header revision. It is NOT
	// service.version, which the OpenTelemetry semantic conventions define as the
	// service's API or implementation version — an operator may set the device header to
	// a reseller's branding revision, and asserting that as the service version would be
	// the same category of false metadata the scope version was fixed for.
	otlpAttrDeviceVersion = "ai.olivares.device.version"
	// otlpReservedPrefix is the product-owned attribute namespace. A caller field here is
	// re-homed rather than allowed to shadow a product key.
	otlpReservedPrefix = "ai.olivares."
	// otlpRetiredPrefix is the RETIRED pre-freeze spelling of the product namespace
	// (froze on ai.olivares.*). It is reserved for the same reason the live
	// prefix is: a caller field spelled olivares.<x> would be indistinguishable from
	// legacy product data to any receiver that still maps the old names, so it is
	// re-homed under the caller prefix and the rename recorded.
	otlpRetiredPrefix = "olivares."
	// otlpCallerPrefix is where a re-homed caller field lands.
	otlpCallerPrefix = "caller."
)

// Adjustment operations. These are wire tokens in the structured adjustment record, so
// they are constants rather than inline strings.
const (
	otlpAdjustUTF8   = "utf8_replace"
	otlpAdjustRename = "rename"
	otlpAdjustKeyGen = "generated_key"
)

// OTLPRequestFor resolves n into the single, transport-neutral description of the OTLP
// request this encoder emits: the resource identity, the scope, the mapped severity, the
// event name, the body, the ordered attributes and the GUARDED timestamp.
//
// It exists so the typed-protobuf projection and the JSON projection resolve the same
// source values instead of each deciding independently what the severity, body or
// timestamp of a notification is. It does NOT by itself make them structurally incapable
// of disagreeing — each still lays out its own member set — so a full-message parity test
// guards that (connectors/otlplog, TestEncodeBothEncodingsAgree).
//
// Three policies live here rather than in the SDK encoder, because they are product
// decisions and the encoder's contract is that its bytes mean exactly what it was given:
//
//   - A time OTLP cannot express is never simply dropped. OTLP's field is unsigned
//     nanoseconds with 0 meaning "unknown", so a pre-epoch instant, an instant past
//     2554-07-21T23:34:33.709551615Z, and the epoch itself all encode to 0 — the same byte
//     as "no time at all". Emitting only that would trade a wrong date for a missing one,
//     and a backend that substitutes ingestion time for a missing timestamp would then
//     index a 1969 event as today's. So the authoritative instant travels as attributes
//     with the reason, and the record stays deliverable.
//   - A value JSON cannot carry unchanged is substituted VISIBLY, with the original bytes
//     preserved. encoding/json turns every invalid UTF-8 sequence into U+FFFD, which would
//     make two different values serialize identically. The SDK encoder rejects such input
//     outright; here it is replaced, the ORIGINAL is carried in a bytesValue, and the exact
//     location is named — so the record still reaches the SIEM, the alteration is auditable,
//     and the evidence is recoverable.
//   - A caller field can neither shadow a product key nor collide with another field. The
//     ai.olivares.* namespace is reserved and a field landing there is re-homed; an empty
//     key, which nothing can query, is given a generated one rather than dropped. Every
//     such move is recorded in the same structured adjustment list.
func OTLPRequestFor(d Device, n sdk.Notification) siemwire.OTLPRequest {
	d = d.orDefault()
	sev := mapSeverity(n.Severity)

	body := strings.TrimSpace(n.Title)
	if n.Body != "" {
		if body != "" {
			body += " — "
		}
		body += n.Body
	}

	set := &otlpAttrSet{}
	// The caller's own fields first, in the sorted order orderedFields fixed — including
	// the ones it drops, which are handled here rather than silently discarded. The event
	// type does NOT join them: OTLP has a dedicated member for exactly that
	// (LogRecord.event_name), and a synthetic "eventType" attribute could be shadowed by a
	// caller-supplied field of the same name — a duplicate key, which OTLP forbids and
	// whose handling a receiver is explicitly free to decide.
	for _, f := range sortedFieldPairs(n.Fields) {
		set.putCaller(f.k, f.v)
	}

	// The authoritative tenant is a PRODUCT key, so a caller field named "tenant" cannot
	// replace it (orderedFields lets that happen for the text formats, where the field
	// vocabulary is the caller's; OTLP has a reserved namespace and can do better).
	if n.Tenant != "" {
		set.put(otlpAttrTenant, siemwire.OTLPString(set.clean("record.attributes["+otlpAttrTenant+"]", n.Tenant)))
	}

	stamp, status := siemwire.OTLPTime(n.Time)
	if status != siemwire.OTLPTimeExact && status != siemwire.OTLPTimeAbsent {
		set.put(otlpAttrEventTimeSeconds, siemwire.OTLPInt(n.Time.Unix()))
		set.put(otlpAttrEventTimeNanos, siemwire.OTLPInt(int64(n.Time.Nanosecond())))
		set.put(otlpAttrEventTimeStatus, siemwire.OTLPString(status.String()))
		if human, ok := rfc3339Nano(n.Time); ok {
			set.put(otlpAttrEventTimeRFC3339, siemwire.OTLPString(human))
		}
	}

	// Every scalar is cleaned into a local BEFORE the literal below, so the adjustment
	// record finish() appends accounts for all of them. Relying on the evaluation order of
	// a composite literal's fields for that would be true but unreadable.
	product := set.clean("resource.attributes[service.name]", d.Product)
	vendor := set.clean("resource.attributes["+otlpAttrDeviceVendor+"]", d.Vendor)
	deviceVersion := set.clean("resource.attributes["+otlpAttrDeviceVersion+"]", d.Version)
	serviceVersion := set.clean("resource.attributes[service.version]", d.ServiceVersion)
	severityText := set.clean("record.severityText", sev.text)
	eventName := set.clean("record.eventName", n.Type)
	body = set.clean("record.body", body)

	resource := []siemwire.OTLPKeyValue{
		{Key: "service.name", Value: siemwire.OTLPString(product)},
		{Key: otlpAttrDeviceVendor, Value: siemwire.OTLPString(vendor)},
		{Key: otlpAttrDeviceVersion, Value: siemwire.OTLPString(deviceVersion)},
	}
	// service.version is emitted ONLY when the running service's version is actually
	// known. An absent resource attribute is honest; asserting the device header revision
	// as the service's implementation version would not be.
	if serviceVersion != "" {
		resource = append(resource, siemwire.OTLPKeyValue{
			Key: "service.version", Value: siemwire.OTLPString(serviceVersion),
		})
	}

	return siemwire.OTLPRequest{
		ResourceAttributes: resource,
		ScopeName:          otlpScopeName,
		ScopeVersion:       otlpScopeVersion,
		Record: siemwire.OTLPRecord{
			TimeUnixNano: stamp,
			// ObservedTimeUnixNano stays 0 ("unknown") deliberately: sdk.Notification
			// carries only the event's own occurrence time, so claiming an observation
			// instant would be inventing one.
			ObservedTimeUnixNano: 0,
			SeverityNumber:       int32(sev.otlp),
			SeverityText:         severityText,
			EventName:            eventName,
			Body:                 body,
			Attributes:           set.finish(),
		},
	}
}

// rfc3339Nano formats t and reports whether the result is genuinely RFC 3339. Go's
// RFC3339Nano layout does not restrict the year to four digits, so it emits text such as
// "10000-01-02T03:04:05Z" and "-0001-01-02T03:04:05Z" that no RFC 3339 parser — including
// Go's own — accepts. Rather than label such text RFC 3339, the value is parsed back and
// only emitted when it round-trips to the same instant.
func rfc3339Nano(t time.Time) (string, bool) {
	s := t.UTC().Format(time.RFC3339Nano)
	back, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !back.Equal(t) {
		return "", false
	}
	return s, true
}

// sortedFieldPairs returns the notification's Fields map sorted by key, INCLUDING the
// empty key. orderedFields drops an empty key because the text formats have no way to
// express it; the OTLP path can give it a generated name and say so, which preserves the
// value instead of discarding it.
func sortedFieldPairs(fields map[string]string) []kv {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv{k, fields[k]})
	}
	return out
}

// otlpAttrSet accumulates one record's attributes while enforcing the rules the SDK
// encoder validates and refuses to repair: every key non-empty and unique, every string
// valid UTF-8. Every adjustment it makes is recorded structurally, with the ORIGINAL bytes,
// so a record that was altered is never indistinguishable from one that was not and the
// evidence is recoverable.
//
// Why adjust rather than reject: a rejected encoding drops a governance event from the OTLP
// feed while the CEF and syslog feeds still carry it. A marked, complete record is strictly
// more information than a missing one. The structured marking is what keeps that honest
// rather than merely convenient.
type otlpAttrSet struct {
	fields      []siemwire.OTLPKeyValue
	seen        map[string]struct{}
	adjustments []siemwire.OTLPValue
}

// record appends one structured adjustment. `original` is the exact bytes before the
// change, carried as a bytesValue because it may not be valid UTF-8 — which is the whole
// reason an adjustment was needed.
func (s *otlpAttrSet) record(operation, location, emitted string, original []byte) {
	s.adjustments = append(s.adjustments, siemwire.OTLPKVList(
		siemwire.OTLPKeyValue{Key: "operation", Value: siemwire.OTLPString(operation)},
		siemwire.OTLPKeyValue{Key: "location", Value: siemwire.OTLPString(location)},
		siemwire.OTLPKeyValue{Key: "emitted", Value: siemwire.OTLPString(emitted)},
		siemwire.OTLPKeyValue{Key: "original", Value: siemwire.OTLPBytes(original)},
	))
}

// clean returns v unchanged when it is valid UTF-8, and otherwise its replacement-character
// form, recording the exact location and the original bytes. Valid input costs one
// utf8.ValidString and allocates nothing.
//
// The substitution is strings.ToValidUTF8, which collapses each RUN of invalid bytes to one
// U+FFFD — deliberately not the same as encoding/json's one-per-byte, and the reason the
// original travels alongside: the emitted form is not reversible on its own.
func (s *otlpAttrSet) clean(location, v string) string {
	if utf8.ValidString(v) {
		return v
	}
	emitted := strings.ToValidUTF8(v, string(utf8.RuneError))
	s.record(otlpAdjustUTF8, location, emitted, []byte(v))
	return emitted
}

// take reserves key and returns the key actually used. A collision is resolved by appending
// an incrementing suffix until the name is free — it LOOPS rather than suffixing once,
// because a caller is free to supply a field literally named "foo#1", so a single-shot
// suffix can land on an occupied name. Deterministic: the same input always produces the
// same name.
func (s *otlpAttrSet) take(key string) string {
	if s.seen == nil {
		s.seen = make(map[string]struct{}, 8)
	}
	candidate := key
	for n := 1; ; n++ {
		if _, dup := s.seen[candidate]; !dup {
			s.seen[candidate] = struct{}{}
			return candidate
		}
		candidate = key + "#" + strconv.Itoa(n)
	}
}

// put adds a product-owned attribute. Product keys are distinct compile-time constants and
// putCaller has already moved any caller field out of the reserved namespace, so a
// collision here can only be a bug in THIS file — never something an input can cause. It
// panics rather than quietly emitting a suffixed product key, because a canonical
// attribute silently splitting into two is exactly the kind of defect that reaches
// production unnoticed. TestProductKeysAreDistinct covers the constants.
func (s *otlpAttrSet) put(key string, value siemwire.OTLPValue) {
	if used := s.take(key); used != key {
		panic("siemfmt: product OTLP attribute " + key + " emitted twice (encoder bug, not input-dependent)")
	}
	s.fields = append(s.fields, siemwire.OTLPKeyValue{Key: key, Value: value})
}

// putCaller adds a caller-supplied field. Its key is sanitized, given a generated name if
// empty, re-homed out of the product's reserved namespace if it landed there, and made
// unique — in that order, so a sanitized or re-homed key that then collides is still
// resolved. Every departure from the original key is recorded.
func (s *otlpAttrSet) putCaller(key, value string) {
	index := len(s.fields)
	original := key
	operation := ""

	switch {
	case key == "":
		// Nothing can query an attribute with no key, but the VALUE is still evidence, so
		// it gets a generated name rather than being dropped.
		key = otlpReservedPrefix + "caller.field." + strconv.Itoa(index)
		operation = otlpAdjustKeyGen
	case !utf8.ValidString(key):
		key = strings.ToValidUTF8(key, string(utf8.RuneError))
		operation = otlpAdjustUTF8
	}
	if (strings.HasPrefix(key, otlpReservedPrefix) || strings.HasPrefix(key, otlpRetiredPrefix)) &&
		operation != otlpAdjustKeyGen {
		key = otlpCallerPrefix + key
		if operation == "" {
			operation = otlpAdjustRename
		}
	}
	used := s.take(key)
	if used != key && operation == "" {
		operation = otlpAdjustRename
	}
	if operation != "" {
		s.record(operation, "record.attributes["+strconv.Itoa(index)+"].key", used, []byte(original))
	}
	s.fields = append(s.fields, siemwire.OTLPKeyValue{
		Key:   used,
		Value: siemwire.OTLPString(s.clean("record.attributes["+strconv.Itoa(index)+"].value for "+used, value)),
	})
}

// finish appends the adjustment record, if any, and returns the set. The adjustment
// attributes go last and through put, whose product-key reservation guarantees they cannot
// be shadowed by a caller field (putCaller re-homes anything in the reserved namespace).
func (s *otlpAttrSet) finish() []siemwire.OTLPKeyValue {
	if len(s.adjustments) == 0 {
		return s.fields
	}
	s.put(otlpAttrAdjustmentCount, siemwire.OTLPInt(int64(len(s.adjustments))))
	s.put(otlpAttrAdjustments, siemwire.OTLPArray(s.adjustments...))
	return s.fields
}

// OTLPLogsData builds an OTLP LogsData with a single LogRecord representing n. The resource
// carries the device identity; the record carries severity, the event name, the body
// (Title — Body) and every ordered field as an attribute. It is the TYPED form, kept as the
// source for the binary protobuf encoding and for callers that need the generated message;
// the JSON body comes from OTLPLogJSON.
//
// It resolves n and then projects it. A caller that already holds the resolved request —
// because it also needs the JSON body, or chooses between the two encodings — should call
// OTLPLogsDataFrom instead and resolve exactly once.
func OTLPLogsData(d Device, n sdk.Notification) (*logspb.LogsData, error) {
	return OTLPLogsDataFrom(OTLPRequestFor(d, n))
}

// OTLPLogsDataFrom projects an already-resolved request onto the generated protobuf types.
// It is the typed counterpart of siemwire.OTLPExportRequestJSON: same input, two encodings,
// no second resolution that could disagree with the first.
//
// It applies siemwire.ValidateOTLPRequest first, and returns an error, because the request
// contract belongs to the REQUEST and not to one encoding. Without it the two projections
// have different contracts: duplicate attribute keys and invalid UTF-8 marshal happily into
// protobuf while the JSON encoder refuses them, so the same resolved request would be
// deliverable in one encoding and not the other.
func OTLPLogsDataFrom(req siemwire.OTLPRequest) (*logspb.LogsData, error) {
	if err := siemwire.ValidateOTLPRequest(req); err != nil {
		return nil, fmt.Errorf("siemfmt: OTLP request: %w", err)
	}
	resourceAttrs, err := otlpProtoAttrs(req.ResourceAttributes)
	if err != nil {
		return nil, err
	}
	recordAttrs, err := otlpProtoAttrs(req.Record.Attributes)
	if err != nil {
		return nil, err
	}
	return &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: resourceAttrs},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope: &commonpb.InstrumentationScope{Name: req.ScopeName, Version: req.ScopeVersion},
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano:         req.Record.TimeUnixNano,
					ObservedTimeUnixNano: req.Record.ObservedTimeUnixNano,
					SeverityNumber:       logspb.SeverityNumber(req.Record.SeverityNumber),
					SeverityText:         req.Record.SeverityText,
					EventName:            req.Record.EventName,
					Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: req.Record.Body}},
					Attributes:           recordAttrs,
				}},
			}},
		}},
	}, nil
}

// otlpProtoAttrs converts the SDK's AnyValue key/values to the generated protobuf ones. It
// goes through the JSON form for the value: siemwire owns the value model, and decoding its
// canonical rendering is what keeps ONE definition of what an OTLP value IS rather than a
// second switch here that could drift from it.
func otlpProtoAttrs(pairs []siemwire.OTLPKeyValue) ([]*commonpb.KeyValue, error) {
	out := make([]*commonpb.KeyValue, 0, len(pairs))
	for _, p := range pairs {
		// The common case — a plain string — needs no round trip at all.
		if s, ok := p.Value.IsString(); ok {
			out = append(out, strAttr(p.Key, s))
			continue
		}
		raw, err := siemwire.OTLPValueJSON(p.Value)
		if err != nil {
			return nil, fmt.Errorf("siemfmt: OTLP attribute %q: %w", p.Key, err)
		}
		var value commonpb.AnyValue
		if err := protojson.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("siemfmt: OTLP attribute %q is not a decodable AnyValue: %w", p.Key, err)
		}
		out = append(out, &commonpb.KeyValue{Key: p.Key, Value: &value})
	}
	return out, nil
}

// OTLPLogJSON serializes n as an OTLP/HTTP JSON export request. That document is the body an
// OTLP/HTTP logs endpoint accepts at /v1/logs, and it is also what the destinations that
// carry an OTLP payload inside their own envelope send: a file archive, Splunk HEC, object
// storage and the generic SIEM output all call this, so the shape is not exclusive to the
// OTLP transport.
//
// It no longer goes through protojson. Default protojson emitted the severity enum by NAME
// (which OTLP/JSON forbids: enum values must be integers) and omitted zero values, so an
// unknown severity produced no severityNumber member and an unset time produced no
// timeUnixNano at all. The shared SDK encoder declares one byte form per declared value
// instead, and is byte-deterministic — which protojson explicitly does not promise across
// library versions.
func OTLPLogJSON(d Device, n sdk.Notification) ([]byte, error) {
	b, err := siemwire.OTLPExportRequestJSON(OTLPRequestFor(d, n))
	if err != nil {
		return nil, fmt.Errorf("siemfmt: marshal OTLP log: %w", err)
	}
	return b, nil
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
