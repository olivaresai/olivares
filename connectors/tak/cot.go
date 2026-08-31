// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// cot.go is a CLEAN-ROOM implementation of the Cursor-on-Target base-event wire
// format, written from the public-release MITRE specification only. See doc.go for
// the provenance record and the list of GPL sources this package must never touch.
//
// Every rule below is traceable to one of two documents:
//
//	[XSD]   Event-PUBLIC.xsd, "CoT Event data model (Version 2.0)", MITRE Case #11-3895.
//	[GUIDE] "The Developer's Guide to Cursor on Target", Butler, MITRE, Aug 2005,
//	        DTIC ADA637348, MITRE Case #06-0249.
//
// The base event carries exactly TWELVE required attributes [GUIDE: "CoT's base
// class has only 12 required attributes"]: seven on <event> (version, type, uid,
// time, start, stale, how) and five on <point> (lat, lon, hae, ce, le) [XSD:
// use="required"]. Three more on <event> are optional: access, qos, opex [XSD:
// use="optional"]. <point> occurs exactly once; <detail> zero or one time [XSD:
// minOccurs="0"].

// UnboundedError is the value CoT uses for an error bound the producer cannot put
// an upper bound on: "If they can't put an upper bound on their errors we use
// 9999999 meters since we are confident that their coordinates are within
// +/-10,000,000 meters" [GUIDE]. It is a sentinel, not a measurement — a consumer
// must not treat it as a 9999 km circle, it means "unknown".
const UnboundedError = 9999999.0

// Default parse limits. They bound memory and CPU for a listener facing an
// untrusted network; a CoT event is a single small XML document by design.
const (
	// DefaultMaxEventBytes caps one serialized event.
	DefaultMaxEventBytes = 64 << 10
	// DefaultMaxDetailBytes caps the opaque <detail> span within it.
	DefaultMaxDetailBytes = 32 << 10
	// maxUIDBytes and maxTypeBytes bound the two attributes that flow into
	// resource/origin references downstream.
	maxUIDBytes  = 256
	maxTypeBytes = 128
	maxHowBytes  = 64
)

// Parse errors. They are sentinel-wrapped so a listener can count rejections by
// class without string matching, and so a fuzz/regression test can assert the
// exact reason a malformed event was refused.
var (
	ErrEmpty          = errors.New("tak: empty CoT message")
	ErrTooLarge       = errors.New("tak: CoT message exceeds the configured size limit")
	ErrDoctype        = errors.New("tak: CoT message carries an XML directive (DOCTYPE/ENTITY) — refused")
	ErrNotEvent       = errors.New("tak: CoT root element is not <event>")
	ErrNamespaced     = errors.New("tak: CoT elements must be namespace-free")
	ErrUnknownAttr    = errors.New("tak: unknown attribute on a CoT element")
	ErrUnknownChild   = errors.New("tak: unknown child element of <event>")
	ErrMissingAttr    = errors.New("tak: required CoT attribute is missing")
	ErrDuplicateChild = errors.New("tak: duplicate <point> or <detail> in one event")
	ErrNoPoint        = errors.New("tak: CoT event has no <point>")
	ErrBadValue       = errors.New("tak: CoT attribute value is out of range or malformed")
	ErrTrailingData   = errors.New("tak: trailing data after </event>")
	ErrDetailTooLarge = errors.New("tak: CoT <detail> exceeds the configured size limit")
)

// Limits bounds one parse. A zero value means "use the defaults".
type Limits struct {
	MaxEventBytes  int
	MaxDetailBytes int
}

func (l Limits) withDefaults() Limits {
	if l.MaxEventBytes <= 0 {
		l.MaxEventBytes = DefaultMaxEventBytes
	}
	if l.MaxDetailBytes <= 0 {
		l.MaxDetailBytes = DefaultMaxDetailBytes
	}
	if l.MaxDetailBytes > l.MaxEventBytes {
		l.MaxDetailBytes = l.MaxEventBytes
	}
	return l
}

// Point is the CoT base location: a cylinder around a WGS-84 coordinate, with an
// explicit circular (ce) and linear (le) error bound. "CoT makes these error
// bounds explicit. We express coordinates like a machinist would express
// measurements" [GUIDE].
type Point struct {
	// Lat is WGS-84 latitude in signed decimal degrees, -90..+90 [XSD].
	Lat float64
	// Lon is WGS-84 longitude in signed decimal degrees, -180..+180 [XSD].
	Lon float64
	// HAE is height above the WGS-84 ellipsoid, in meters [XSD: xs:decimal].
	HAE float64
	// CE is the circular (horizontal) 1-sigma error bound, in meters.
	CE float64
	// LE is the linear (vertical) 1-sigma error bound, in meters.
	LE float64
}

// CEUnbounded reports whether the producer declined to bound its horizontal error.
func (p Point) CEUnbounded() bool { return p.CE >= UnboundedError }

// LEUnbounded reports whether the producer declined to bound its vertical error.
func (p Point) LEUnbounded() bool { return p.LE >= UnboundedError }

// Event is one parsed CoT base event. The <detail> sub-schema is deliberately NOT
// modeled (it is "XML schema defined outside of this document" [XSD]): it is
// reduced to a size and a digest, and its bytes never leave this package.
type Event struct {
	// Version is the CoT schema version; the schema requires >= 2 [XSD].
	Version float64
	// UID is an opaque producer-assigned identifier: "Its sole purpose is to
	// ensure global uniqueness" [GUIDE]. It is NOT a person's name, but it does
	// identify a device, so callers hash it unless the operator opts out.
	UID string
	// Type is the hyphen-separated path through CoT's object hierarchy, e.g.
	// "a-h-G-E-V-A-T-t" for atoms::hostile::ground::equipment::vehicle::armored::
	// tank::t72 [GUIDE].
	Type string
	// How describes how the position was derived.
	How string
	// Access, QoS and Opex are the three optional event attributes [XSD].
	Access string
	QoS    string
	Opex   string
	// Time is when the information was generated; Start and Stale bound the
	// interval over which it is valid [GUIDE: "Three Time Fields?"].
	Time  time.Time
	Start time.Time
	Stale time.Time
	// Point is the required base location.
	Point Point
	// HasDetail reports whether a <detail> element was present.
	HasDetail bool
	// DetailBytes is the size of the raw <detail> span, 0 when absent or empty.
	DetailBytes int
	// DetailDigest is the hex SHA-256 over the raw <detail> span, "" when absent.
	// It exists so a consumer can correlate or de-duplicate identical detail
	// payloads without the payload ever being transmitted or stored.
	DetailDigest string
	// Bytes is the size of the serialized event this was parsed from.
	Bytes int
}

// IsDropTrack reports whether this event cancels previously published data.
//
// This is NOT an error case. "By convention, CoT allows the stale time to predate
// the start time. This reversal is how CoT indicates 'drop track.' If the
// information was stale before it started, it's considered an overt indication
// that the sender wishes to cancel the data." [GUIDE]
//
// A parser that rejected stale < start as invalid would silently swallow every
// cancellation. We accept it and surface it.
func (e Event) IsDropTrack() bool { return e.Stale.Before(e.Start) }

// Stale reports whether the event's validity interval has closed at now.
func (e Event) IsStaleAt(now time.Time) bool { return now.After(e.Stale) }

// TypeAtoms splits Type into its hyphen-separated atoms. CoT "abbreviates the
// path to short hyphen-separated strings" [GUIDE].
func (e Event) TypeAtoms() []string {
	if e.Type == "" {
		return nil
	}
	return strings.Split(e.Type, "-")
}

// Affiliation returns the CoT affiliation atom for an "atoms" event (type "a-*"),
// e.g. "h" for hostile in "a-h-G". It returns "" for any other top-level branch
// (bits, tasking, reply, capability), where the second atom is not an affiliation.
//
// We deliberately do NOT decode the full MIL-STD-2525 / CoT taxonomy: the atoms
// are surfaced verbatim and classification is left to policy.
func (e Event) Affiliation() string {
	a := e.TypeAtoms()
	if len(a) < 2 || a[0] != "a" {
		return ""
	}
	return a[1]
}

// ParseEvent parses and strictly validates one serialized CoT base event.
//
// Strictness beyond the XSD, and why:
//
//   - An XML directive (DOCTYPE, ENTITY) is refused outright. Go's decoder does
//     not expand external entities, but a document that declares any is not a CoT
//     event and we will not spend cycles proving it harmless.
//   - Unknown attributes and unknown child elements are refused. A field we do not
//     understand might change the meaning of one we do.
//   - Timestamps must carry an explicit timezone offset. xs:dateTime permits a
//     local time with no offset; accepting one would put an ambiguous instant into
//     a tamper-evident governance ledger.
//   - Negative ce/le are refused. xs:decimal permits them; an error radius cannot
//     be negative.
//
// stale < start is accepted (drop-track, see IsDropTrack). time > start is
// accepted ("Note that time needn't be earlier than start" [GUIDE]).
func ParseEvent(raw []byte, lim Limits) (Event, error) {
	lim = lim.withDefaults()
	if len(bytes.TrimSpace(raw)) == 0 {
		return Event{}, ErrEmpty
	}
	if len(raw) > lim.MaxEventBytes {
		return Event{}, fmt.Errorf("%w: %d > %d bytes", ErrTooLarge, len(raw), lim.MaxEventBytes)
	}

	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	dec.Entity = nil // only the five predefined entities; anything else is an error

	var (
		ev         Event
		seenRoot   bool
		closedRoot bool
		seenPoint  bool
		seenDetail bool
	)
	ev.Bytes = len(raw)

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Event{}, fmt.Errorf("tak: CoT is not well-formed XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.Directive:
			return Event{}, ErrDoctype

		case xml.ProcInst:
			// Only the XML declaration, and only before the root element.
			if seenRoot || t.Target != "xml" {
				return Event{}, fmt.Errorf("%w: a processing instruction other than the XML declaration", ErrBadValue)
			}

		case xml.Comment:
			// Harmless; carries no data into the model.

		case xml.CharData:
			if closedRoot && len(bytes.TrimSpace(t)) > 0 {
				return Event{}, ErrTrailingData
			}
			// <event> has element-only content; text between its children is a
			// malformed document, not an extension point. (Character data INSIDE
			// <detail> never reaches this loop: dec.Skip consumes it.)
			if seenRoot && !closedRoot && len(bytes.TrimSpace(t)) > 0 {
				return Event{}, fmt.Errorf("%w: character data inside <event>", ErrBadValue)
			}

		case xml.StartElement:
			if t.Name.Space != "" {
				return Event{}, fmt.Errorf("%w: %q", ErrNamespaced, t.Name.Local)
			}
			if closedRoot {
				return Event{}, ErrTrailingData
			}
			if !seenRoot {
				if t.Name.Local != "event" {
					return Event{}, fmt.Errorf("%w: <%s>", ErrNotEvent, t.Name.Local)
				}
				if err := applyEventAttrs(&ev, t.Attr); err != nil {
					return Event{}, err
				}
				seenRoot = true
				continue
			}
			switch t.Name.Local {
			case "point":
				if seenPoint {
					return Event{}, fmt.Errorf("%w: <point>", ErrDuplicateChild)
				}
				p, err := parsePointAttrs(t.Attr)
				if err != nil {
					return Event{}, err
				}
				ev.Point = p
				seenPoint = true
				// <point> is an empty element in the schema.
				if err := expectEmptyElement(dec, "point"); err != nil {
					return Event{}, err
				}
			case "detail":
				if seenDetail {
					return Event{}, fmt.Errorf("%w: <detail>", ErrDuplicateChild)
				}
				seenDetail = true
				ev.HasDetail = true
				start := dec.InputOffset()
				if err := dec.Skip(); err != nil {
					return Event{}, fmt.Errorf("tak: malformed <detail>: %w", err)
				}
				end := dec.InputOffset()
				span := detailSpan(raw, start, end)
				if len(span) > lim.MaxDetailBytes {
					return Event{}, fmt.Errorf("%w: %d > %d bytes", ErrDetailTooLarge, len(span), lim.MaxDetailBytes)
				}
				ev.DetailBytes = len(span)
				if len(span) > 0 {
					sum := sha256.Sum256(span)
					ev.DetailDigest = hex.EncodeToString(sum[:])
				}
			default:
				return Event{}, fmt.Errorf("%w: <%s>", ErrUnknownChild, t.Name.Local)
			}

		case xml.EndElement:
			if t.Name.Local == "event" && seenRoot {
				closedRoot = true
			}
		}
	}

	if !seenRoot || !closedRoot {
		return Event{}, ErrNotEvent
	}
	if !seenPoint {
		return Event{}, ErrNoPoint
	}
	if err := requireEventAttrs(ev); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// detailSpan returns the raw bytes the decoder consumed for a <detail> element's
// content plus its end tag. It is used only as a digest preimage and a size bound,
// never decoded, so the trailing "</detail>" is harmless and the span is stable
// for identical inputs. A self-closing <detail/> yields an empty span.
func detailSpan(raw []byte, start, end int64) []byte {
	if start < 0 || end < start || end > int64(len(raw)) {
		return nil
	}
	return raw[start:end]
}

// expectEmptyElement consumes the remainder of an element that the schema declares
// empty, refusing any child element or non-whitespace text.
func expectEmptyElement(dec *xml.Decoder, name string) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("tak: malformed <%s>: %w", name, err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == name {
				return nil
			}
			return fmt.Errorf("%w: unbalanced </%s>", ErrBadValue, t.Name.Local)
		case xml.StartElement:
			return fmt.Errorf("%w: <%s> inside <%s>", ErrUnknownChild, t.Name.Local, name)
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("%w: character data inside <%s>", ErrBadValue, name)
			}
		case xml.Comment:
		default:
			return fmt.Errorf("%w: unexpected token inside <%s>", ErrBadValue, name)
		}
	}
}

func applyEventAttrs(ev *Event, attrs []xml.Attr) error {
	var err error
	// Go's XML decoder does not reject a repeated attribute; it hands us both.
	// `<event version="2" version="9">` is not well-formed XML, and silently taking
	// the last one would let a producer smuggle a value past a validating proxy that
	// read the first.
	seen := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			return fmt.Errorf("%w: xmlns on <event>", ErrNamespaced)
		}
		if seen[a.Name.Local] {
			return fmt.Errorf("%w: duplicate event/@%s", ErrBadValue, a.Name.Local)
		}
		seen[a.Name.Local] = true
		switch a.Name.Local {
		case "version":
			// version is xs:decimal with minInclusive 2 [XSD]. It MUST go through
			// parseDecimal, not strconv.ParseFloat: a raw ParseFloat accepts "NaN",
			// and NaN defeats BOTH guards below — `NaN < 2` is false, so the range
			// check passes, and `NaN == 0` is false, so requireEventAttrs does not
			// see it as missing. An event with no valid schema version would be
			// accepted with Version = NaN.
			if ev.Version, err = parseDecimal(a.Value); err != nil {
				return fmt.Errorf("%w: event/@version is not an xs:decimal", ErrBadValue)
			}
			if ev.Version < 2 {
				return fmt.Errorf("%w: event/@version is below the schema minimum of 2", ErrBadValue)
			}
		case "uid":
			if ev.UID, err = safeToken(a.Value, maxUIDBytes, "uid"); err != nil {
				return err
			}
		case "type":
			if ev.Type, err = safeToken(a.Value, maxTypeBytes, "type"); err != nil {
				return err
			}
			if !validCoTType(ev.Type) {
				return fmt.Errorf("%w: event/@type is not a hyphen-separated atom path", ErrBadValue)
			}
		case "how":
			if ev.How, err = safeToken(a.Value, maxHowBytes, "how"); err != nil {
				return err
			}
		case "time":
			if ev.Time, err = parseCoTTime("time", a.Value); err != nil {
				return err
			}
		case "start":
			if ev.Start, err = parseCoTTime("start", a.Value); err != nil {
				return err
			}
		case "stale":
			if ev.Stale, err = parseCoTTime("stale", a.Value); err != nil {
				return err
			}
		case "access":
			if ev.Access, err = safeToken(a.Value, maxTypeBytes, "access"); err != nil {
				return err
			}
		case "qos":
			if ev.QoS, err = safeToken(a.Value, maxTypeBytes, "qos"); err != nil {
				return err
			}
		case "opex":
			if ev.Opex, err = safeToken(a.Value, maxTypeBytes, "opex"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: event/@%s", ErrUnknownAttr, a.Name.Local)
		}
	}
	return nil
}

// requireEventAttrs enforces the seven required <event> attributes [XSD].
func requireEventAttrs(ev Event) error {
	missing := make([]string, 0, 7)
	if ev.Version == 0 {
		missing = append(missing, "version")
	}
	if ev.UID == "" {
		missing = append(missing, "uid")
	}
	if ev.Type == "" {
		missing = append(missing, "type")
	}
	if ev.How == "" {
		missing = append(missing, "how")
	}
	if ev.Time.IsZero() {
		missing = append(missing, "time")
	}
	if ev.Start.IsZero() {
		missing = append(missing, "start")
	}
	if ev.Stale.IsZero() {
		missing = append(missing, "stale")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: event/@%s", ErrMissingAttr, strings.Join(missing, ",@"))
	}
	return nil
}

// parsePointAttrs enforces the five required <point> attributes [XSD] and their
// documented ranges.
func parsePointAttrs(attrs []xml.Attr) (Point, error) {
	var (
		p    Point
		seen = map[string]bool{}
		err  error
	)
	for _, a := range attrs {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			return Point{}, fmt.Errorf("%w: xmlns on <point>", ErrNamespaced)
		}
		var dst *float64
		switch a.Name.Local {
		case "lat":
			dst = &p.Lat
		case "lon":
			dst = &p.Lon
		case "hae":
			dst = &p.HAE
		case "ce":
			dst = &p.CE
		case "le":
			dst = &p.LE
		default:
			return Point{}, fmt.Errorf("%w: point/@%s", ErrUnknownAttr, a.Name.Local)
		}
		if seen[a.Name.Local] {
			return Point{}, fmt.Errorf("%w: duplicate point/@%s", ErrBadValue, a.Name.Local)
		}
		seen[a.Name.Local] = true
		if *dst, err = parseDecimal(a.Value); err != nil {
			return Point{}, fmt.Errorf("%w: point/@%s is not an xs:decimal", ErrBadValue, a.Name.Local)
		}
	}
	for _, k := range []string{"lat", "lon", "hae", "ce", "le"} {
		if !seen[k] {
			return Point{}, fmt.Errorf("%w: point/@%s", ErrMissingAttr, k)
		}
	}
	if p.Lat < -90 || p.Lat > 90 {
		return Point{}, fmt.Errorf("%w: point/@lat is outside -90..90", ErrBadValue)
	}
	if p.Lon < -180 || p.Lon > 180 {
		return Point{}, fmt.Errorf("%w: point/@lon is outside -180..180", ErrBadValue)
	}
	if p.CE < 0 || p.LE < 0 {
		return Point{}, fmt.Errorf("%w: point/@ce or point/@le is a negative error bound", ErrBadValue)
	}
	return p, nil
}

// parseDecimal parses an xs:decimal: an optional sign, then digits with at most
// one decimal point. Nothing else.
//
// It does NOT hand the string straight to strconv.ParseFloat, which also accepts
// "NaN", "Inf", underscore separators and Go hex-float literals ("0x1p-2"). None
// of those is an xs:decimal, and a NaN coordinate would poison every downstream
// comparison (NaN fails both the < and > range checks silently).
func parseDecimal(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrBadValue
	}
	body := s
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return 0, ErrBadValue
	}
	dots, digits := 0, 0
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
			if dots > 1 {
				return 0, ErrBadValue
			}
		default:
			return 0, ErrBadValue
		}
	}
	if digits == 0 {
		return 0, ErrBadValue
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, ErrBadValue
	}
	return v, nil
}

// parseCoTTime parses an xs:dateTime, REQUIRING an explicit timezone offset.
func parseCoTTime(attr, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("%w: event/@%s is empty", ErrBadValue, attr)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: event/@%s is not an xs:dateTime with an explicit timezone offset", ErrBadValue, attr)
	}
	// A zero instant is indistinguishable from "unset" in Event; refuse it.
	if t.IsZero() {
		return time.Time{}, fmt.Errorf("%w: event/@%s is the zero instant", ErrBadValue, attr)
	}
	return t.UTC(), nil
}

// safeToken bounds an attribute and rejects control characters, so a hostile
// producer cannot inject newlines into a log line or a resource reference.
func safeToken(v string, max int, name string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%w: event/@%s is empty", ErrBadValue, name)
	}
	if len(v) > max {
		return "", fmt.Errorf("%w: event/@%s is %d bytes (max %d)", ErrBadValue, name, len(v), max)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: event/@%s contains a control character", ErrBadValue, name)
		}
	}
	return v, nil
}

// validCoTType accepts the hyphen-separated atom path CoT uses for its object
// hierarchy. Atoms are alphanumeric; '.' and '_' appear in some community
// extensions. An empty atom (leading, trailing or doubled hyphen) is refused.
func validCoTType(s string) bool {
	if s == "" {
		return false
	}
	for _, atom := range strings.Split(s, "-") {
		if atom == "" {
			return false
		}
		for _, r := range atom {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			default:
				return false
			}
		}
	}
	return true
}
