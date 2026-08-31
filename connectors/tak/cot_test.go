// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixtures ---------------------------------------------------------------

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// --- event builders ---------------------------------------------------------
//
// A CoT event is rendered from ordered attribute lists so a single test can drop
// or replace exactly one attribute at a time. The defaults are a canonical,
// well-formed base event; every malformed case is one edit away from valid, which
// is the property that makes a "each required attr missing" sweep trustworthy.

type kv struct{ k, v string }

func defEventAttrs() []kv {
	return []kv{
		{"version", "2.0"},
		{"uid", "ANDROID-1"},
		{"type", "a-h-G"},
		{"how", "m-g"},
		{"time", "2021-06-01T12:00:00Z"},
		{"start", "2021-06-01T12:00:00Z"},
		{"stale", "2021-06-01T12:10:00Z"},
	}
}

func defPointAttrs() []kv {
	return []kv{
		{"lat", "30.0090"},
		{"lon", "-85.9080"},
		{"hae", "-42.6"},
		{"ce", "45.3"},
		{"le", "99.5"},
	}
}

// without returns list with the first entry keyed key removed.
func without(list []kv, key string) []kv {
	out := make([]kv, 0, len(list))
	for _, e := range list {
		if e.k == key {
			continue
		}
		out = append(out, e)
	}
	return out
}

// with returns list with key set to val (replacing in place, or appended).
func with(list []kv, key, val string) []kv {
	out := make([]kv, len(list))
	copy(out, list)
	for i := range out {
		if out[i].k == key {
			out[i].v = val
			return out
		}
	}
	return append(out, kv{key, val})
}

func attrs(list []kv) string {
	var b strings.Builder
	for _, a := range list {
		b.WriteString(" ")
		b.WriteString(a.k)
		b.WriteString(`="`)
		b.WriteString(a.v)
		b.WriteString(`"`)
	}
	return b.String()
}

// render builds "<event ...><point .../>CHILDREN</event>".
func render(event, point []kv, children string) string {
	return "<event" + attrs(event) + "><point" + attrs(point) + "/>" + children + "</event>"
}

// renderNoPoint builds an event with no <point> element.
func renderNoPoint(event []kv, children string) string {
	return "<event" + attrs(event) + ">" + children + "</event>"
}

func validRaw() string { return render(defEventAttrs(), defPointAttrs(), "") }

// --- VALID cases ------------------------------------------------------------

func TestParseEventValid(t *testing.T) {
	t.Run("canonical_fixture_all_attrs_no_detail", func(t *testing.T) {
		ev, err := ParseEvent(readFixture(t, "valid.xml"), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if ev.Version != 2.0 {
			t.Errorf("Version = %v, want 2", ev.Version)
		}
		if ev.UID != "ANDROID-359e5f4b8d2a" {
			t.Errorf("UID = %q", ev.UID)
		}
		if ev.Type != "a-h-G-E-V-A-T-t" {
			t.Errorf("Type = %q", ev.Type)
		}
		if ev.How != "m-g" {
			t.Errorf("How = %q", ev.How)
		}
		if ev.Time.IsZero() || ev.Start.IsZero() || ev.Stale.IsZero() {
			t.Errorf("time fields not all parsed: %v %v %v", ev.Time, ev.Start, ev.Stale)
		}
		if ev.Point.Lat != 30.009 || ev.Point.Lon != -85.908 {
			t.Errorf("Point = %+v", ev.Point)
		}
		if ev.Point.HAE != -42.6 || ev.Point.CE != 45.3 || ev.Point.LE != 99.5 {
			t.Errorf("Point error/hae = %+v", ev.Point)
		}
		if ev.HasDetail {
			t.Errorf("HasDetail = true, want false")
		}
		if ev.IsDropTrack() {
			t.Errorf("IsDropTrack = true, want false")
		}
		if ev.Affiliation() != "h" {
			t.Errorf("Affiliation = %q, want h", ev.Affiliation())
		}
	})

	t.Run("optional_attrs_access_qos_opex", func(t *testing.T) {
		e := with(with(with(defEventAttrs(), "access", "Undefined"), "qos", "9-r-c"), "opex", "e-drill")
		ev, err := ParseEvent([]byte(render(e, defPointAttrs(), "")), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if ev.Access != "Undefined" || ev.QoS != "9-r-c" || ev.Opex != "e-drill" {
			t.Errorf("optional attrs = %q/%q/%q", ev.Access, ev.QoS, ev.Opex)
		}
	})

	t.Run("detail_nested_has_digest", func(t *testing.T) {
		raw := render(defEventAttrs(), defPointAttrs(), `<detail><contact callsign="X"/><track speed="1"/></detail>`)
		ev, err := ParseEvent([]byte(raw), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if !ev.HasDetail {
			t.Fatal("HasDetail = false, want true")
		}
		if ev.DetailBytes <= 0 {
			t.Errorf("DetailBytes = %d, want > 0", ev.DetailBytes)
		}
		if len(ev.DetailDigest) != 64 {
			t.Errorf("DetailDigest = %q, want 64 hex chars", ev.DetailDigest)
		}
	})

	t.Run("self_closing_detail_empty_digest", func(t *testing.T) {
		ev, err := ParseEvent([]byte(render(defEventAttrs(), defPointAttrs(), "<detail/>")), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if !ev.HasDetail {
			t.Error("HasDetail = false, want true")
		}
		if ev.DetailBytes != 0 {
			t.Errorf("DetailBytes = %d, want 0", ev.DetailBytes)
		}
		if ev.DetailDigest != "" {
			t.Errorf("DetailDigest = %q, want empty", ev.DetailDigest)
		}
	})

	t.Run("droptrack_stale_before_start_parses", func(t *testing.T) {
		ev, err := ParseEvent(readFixture(t, "droptrack.xml"), Limits{})
		if err != nil {
			t.Fatalf("drop-track MUST parse, got: %v", err)
		}
		if !ev.IsDropTrack() {
			t.Fatal("IsDropTrack = false; a parser that hid this would swallow every cancellation")
		}
	})

	t.Run("time_after_start_parses", func(t *testing.T) {
		// "Note that time needn't be earlier than start" [GUIDE].
		e := with(defEventAttrs(), "time", "2021-06-01T12:05:00Z") // start is 12:00, stale 12:10
		if _, err := ParseEvent([]byte(render(e, defPointAttrs(), "")), Limits{}); err != nil {
			t.Fatalf("time after start MUST parse, got: %v", err)
		}
	})

	t.Run("ce_unbounded_sentinel", func(t *testing.T) {
		p := with(defPointAttrs(), "ce", "9999999")
		ev, err := ParseEvent([]byte(render(defEventAttrs(), p, "")), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if !ev.Point.CEUnbounded() {
			t.Error("CEUnbounded = false, want true")
		}
		if ev.Point.LEUnbounded() {
			t.Error("LEUnbounded = true, want false")
		}
	})

	t.Run("le_unbounded_sentinel", func(t *testing.T) {
		p := with(defPointAttrs(), "le", "9999999")
		ev, err := ParseEvent([]byte(render(defEventAttrs(), p, "")), Limits{})
		if err != nil {
			t.Fatalf("ParseEvent: %v", err)
		}
		if !ev.Point.LEUnbounded() {
			t.Error("LEUnbounded = false, want true")
		}
		if ev.Point.CEUnbounded() {
			t.Error("CEUnbounded = true, want false")
		}
	})

	t.Run("xml_declaration_accepted", func(t *testing.T) {
		raw := `<?xml version="1.0" encoding="UTF-8"?>` + validRaw()
		if _, err := ParseEvent([]byte(raw), Limits{}); err != nil {
			t.Fatalf("XML declaration should be accepted, got: %v", err)
		}
	})

	t.Run("comment_inside_event_accepted", func(t *testing.T) {
		raw := render(defEventAttrs(), defPointAttrs(), "<!-- situational awareness -->")
		if _, err := ParseEvent([]byte(raw), Limits{}); err != nil {
			t.Fatalf("comment inside <event> should be accepted, got: %v", err)
		}
	})

	bounds := []struct {
		name     string
		key, val string
	}{
		{"lat_min_-90", "lat", "-90"},
		{"lat_max_90", "lat", "90"},
		{"lon_min_-180", "lon", "-180"},
		{"lon_max_180", "lon", "180"},
	}
	for _, b := range bounds {
		t.Run("bound_"+b.name, func(t *testing.T) {
			p := with(defPointAttrs(), b.key, b.val)
			if _, err := ParseEvent([]byte(render(defEventAttrs(), p, "")), Limits{}); err != nil {
				t.Fatalf("exact bound %s=%s should parse, got: %v", b.key, b.val, err)
			}
		})
	}
}

// --- REJECT cases (specific sentinels) --------------------------------------

func TestParseEventReject(t *testing.T) {
	xxe := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
		`<event version="2.0" uid="a-1" type="a-f" how="m" time="2021-06-01T12:00:00Z" ` +
		`start="2021-06-01T12:00:00Z" stale="2021-06-01T12:10:00Z">` +
		`<point lat="0" lon="0" hae="0" ce="1" le="1"/><remark>&xxe;</remark></event>`

	nsRoot := `<x:event xmlns:x="urn:x" version="2.0" uid="a-1" type="a-f" how="m" ` +
		`time="2021-06-01T12:00:00Z" start="2021-06-01T12:00:00Z" stale="2021-06-01T12:10:00Z">` +
		`<point lat="0" lon="0" hae="0" ce="1" le="1"/></x:event>`

	cases := []struct {
		name string
		raw  string
		lim  Limits
		want error
	}{
		{"empty", "", Limits{}, ErrEmpty},
		{"whitespace_only", "  \n\t  ", Limits{}, ErrEmpty},
		{"oversize", validRaw(), Limits{MaxEventBytes: 16}, ErrTooLarge},
		{"doctype_billion_laughs", string(mustDoctypeFixture()), Limits{}, ErrDoctype},
		{"doctype_xxe_external_entity", xxe, Limits{}, ErrDoctype},
		{"root_not_event", "<notevent/>", Limits{}, ErrNotEvent},
		{"namespaced_root", nsRoot, Limits{}, ErrNamespaced},
		{"unknown_event_attr", render(with(defEventAttrs(), "bogus", "x"), defPointAttrs(), ""), Limits{}, ErrUnknownAttr},
		{"unknown_point_attr", render(defEventAttrs(), with(defPointAttrs(), "bogus", "x"), ""), Limits{}, ErrUnknownAttr},
		{"unknown_child", render(defEventAttrs(), defPointAttrs(), "<bogus/>"), Limits{}, ErrUnknownChild},
		{"two_points", render(defEventAttrs(), defPointAttrs(), `<point lat="1" lon="1" hae="1" ce="1" le="1"/>`), Limits{}, ErrDuplicateChild},
		{"two_details", render(defEventAttrs(), defPointAttrs(), "<detail/><detail/>"), Limits{}, ErrDuplicateChild},
		{"missing_point", renderNoPoint(defEventAttrs(), ""), Limits{}, ErrNoPoint},
		{"lat_over", render(defEventAttrs(), with(defPointAttrs(), "lat", "90.1"), ""), Limits{}, ErrBadValue},
		{"lat_under", render(defEventAttrs(), with(defPointAttrs(), "lat", "-90.1"), ""), Limits{}, ErrBadValue},
		{"lon_over", render(defEventAttrs(), with(defPointAttrs(), "lon", "180.1"), ""), Limits{}, ErrBadValue},
		{"lon_under", render(defEventAttrs(), with(defPointAttrs(), "lon", "-180.1"), ""), Limits{}, ErrBadValue},
		{"lat_nan", render(defEventAttrs(), with(defPointAttrs(), "lat", "NaN"), ""), Limits{}, ErrBadValue},
		{"lon_inf", render(defEventAttrs(), with(defPointAttrs(), "lon", "Inf"), ""), Limits{}, ErrBadValue},
		{"hae_hexfloat", render(defEventAttrs(), with(defPointAttrs(), "hae", "0x1p-2"), ""), Limits{}, ErrBadValue},
		{"ce_scientific", render(defEventAttrs(), with(defPointAttrs(), "ce", "1e3"), ""), Limits{}, ErrBadValue},
		{"ce_negative", render(defEventAttrs(), with(defPointAttrs(), "ce", "-1"), ""), Limits{}, ErrBadValue},
		{"le_negative", render(defEventAttrs(), with(defPointAttrs(), "le", "-1"), ""), Limits{}, ErrBadValue},
		{"version_below_2", render(with(defEventAttrs(), "version", "1"), defPointAttrs(), ""), Limits{}, ErrBadValue},

		// Regression: `version` once used strconv.ParseFloat directly. NaN defeated
		// BOTH guards — `NaN < 2` is false, so the range check passed, and
		// `NaN == 0` is false, so requireEventAttrs did not see it as missing. An
		// event with no valid schema version parsed clean, with Version = NaN.
		{"version_nan", render(with(defEventAttrs(), "version", "NaN"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"version_inf", render(with(defEventAttrs(), "version", "Inf"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"version_infinity", render(with(defEventAttrs(), "version", "Infinity"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"version_exponent", render(with(defEventAttrs(), "version", "1e9"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"version_go_hex_float", render(with(defEventAttrs(), "version", "0x4p0"), defPointAttrs(), ""), Limits{}, ErrBadValue},

		// Regression: Go's XML decoder hands back BOTH copies of a repeated
		// attribute rather than rejecting the document. Taking the last silently
		// would let a producer smuggle a value past a validating proxy that read
		// the first.
		{"duplicate_event_attr", `<event version="2.0" version="9.0" uid="a-1" type="a-f" how="m" ` +
			`time="2021-06-01T12:00:00Z" start="2021-06-01T12:00:00Z" stale="2021-06-01T12:10:00Z">` +
			`<point lat="0" lon="0" hae="0" ce="1" le="1"/></event>`, Limits{}, ErrBadValue},
		{"time_no_offset", render(with(defEventAttrs(), "time", "2005-04-05T11:43:38"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"uid_control_char", render(with(defEventAttrs(), "uid", "bad&#10;uid"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"type_doubled_hyphen", render(with(defEventAttrs(), "type", "a--f"), defPointAttrs(), ""), Limits{}, ErrBadValue},
		{"detail_too_large", render(defEventAttrs(), defPointAttrs(), "<detail>"+strings.Repeat("A", 200)+"</detail>"), Limits{MaxEventBytes: 64 << 10, MaxDetailBytes: 16}, ErrDetailTooLarge},
		{"trailing_element", validRaw() + "<extra/>", Limits{}, ErrTrailingData},
		{"trailing_text", validRaw() + "garbage", Limits{}, ErrTrailingData},
		{"chardata_inside_event", render(defEventAttrs(), defPointAttrs(), "stray text"), Limits{}, ErrBadValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvent([]byte(tc.raw), tc.lim)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseEvent error = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

func mustDoctypeFixture() []byte {
	b, err := os.ReadFile(filepath.Join("testdata", "doctype.xml"))
	if err != nil {
		panic(err)
	}
	return b
}

// TestParseEventMissingRequiredAttrs drops each required attribute one at a time
// and asserts the specific ErrMissingAttr sentinel — the whole point of separating
// "absent" (ErrMissingAttr) from "present but malformed" (ErrBadValue).
func TestParseEventMissingRequiredAttrs(t *testing.T) {
	for _, a := range defEventAttrs() {
		t.Run("event_missing_"+a.k, func(t *testing.T) {
			raw := render(without(defEventAttrs(), a.k), defPointAttrs(), "")
			_, err := ParseEvent([]byte(raw), Limits{})
			if !errors.Is(err, ErrMissingAttr) {
				t.Fatalf("dropping event/@%s: error = %v, want ErrMissingAttr", a.k, err)
			}
		})
	}
	for _, a := range defPointAttrs() {
		t.Run("point_missing_"+a.k, func(t *testing.T) {
			raw := render(defEventAttrs(), without(defPointAttrs(), a.k), "")
			_, err := ParseEvent([]byte(raw), Limits{})
			if !errors.Is(err, ErrMissingAttr) {
				t.Fatalf("dropping point/@%s: error = %v, want ErrMissingAttr", a.k, err)
			}
		})
	}
}

// TestParseEventDoctypeFixtureRejected pins the fixture path used across the suite.
func TestParseEventDoctypeFixtureRejected(t *testing.T) {
	if _, err := ParseEvent(readFixture(t, "doctype.xml"), Limits{}); !errors.Is(err, ErrDoctype) {
		t.Fatalf("doctype.xml error = %v, want ErrDoctype", err)
	}
}

// TestParseEventDoesNotPanicOnRandomInput mutates a valid event deterministically
// and asserts ParseEvent is total: it never panics and, when it returns nil error,
// the Event it returns satisfies every invariant the parser claims to enforce.
func TestParseEventDoesNotPanicOnRandomInput(t *testing.T) {
	base := []byte(validRaw())
	rng := rand.New(rand.NewSource(1))

	for i := 0; i < 600; i++ {
		mutated := mutate(rng, base)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ParseEvent panicked on mutation %d (%q): %v", i, mutated, r)
				}
			}()
			ev, err := ParseEvent(mutated, Limits{})
			if err != nil {
				return
			}
			// err == nil => a fully valid Event.
			if ev.Version < 2 {
				t.Fatalf("accepted event with Version %v < 2 (input %q)", ev.Version, mutated)
			}
			if ev.UID == "" || ev.Type == "" || ev.How == "" {
				t.Fatalf("accepted event with empty required token (input %q)", mutated)
			}
			if ev.Time.IsZero() || ev.Start.IsZero() || ev.Stale.IsZero() {
				t.Fatalf("accepted event with zero time (input %q)", mutated)
			}
			if ev.Point.Lat < -90 || ev.Point.Lat > 90 || ev.Point.Lon < -180 || ev.Point.Lon > 180 {
				t.Fatalf("accepted out-of-range point %+v (input %q)", ev.Point, mutated)
			}
			if ev.Point.CE < 0 || ev.Point.LE < 0 {
				t.Fatalf("accepted negative error bound %+v (input %q)", ev.Point, mutated)
			}
			if ev.Bytes != len(mutated) {
				t.Fatalf("Bytes = %d, want %d (input %q)", ev.Bytes, len(mutated), mutated)
			}
		}()
	}
}

// mutate returns a copy of base with one to three byte-level edits applied.
func mutate(rng *rand.Rand, base []byte) []byte {
	out := make([]byte, len(base))
	copy(out, base)
	edits := 1 + rng.Intn(3)
	for e := 0; e < edits && len(out) > 0; e++ {
		pos := rng.Intn(len(out))
		switch rng.Intn(3) {
		case 0: // flip
			out[pos] = byte(rng.Intn(256))
		case 1: // delete
			out = append(out[:pos], out[pos+1:]...)
		case 2: // insert
			out = append(out[:pos], append([]byte{byte(rng.Intn(256))}, out[pos:]...)...)
		}
	}
	return out
}

// TestParseErrorsNeverEchoAttributeValues is a privacy regression guard.
//
// ParseEvent is exported and its errors are wrapped with %w by callers. A CoT
// event carries the position of a person. If a parse error embedded the offending
// attribute VALUE, any future caller that logs the error — or wraps it into a
// finding — would publish a coordinate, a device uid, or an emitter-controlled
// string into the tamper-evident ledger. The listener discards the error today
// (listener.go dispatch only keeps the reason class), but that is a property of
// one caller, not of the API.
//
// So: for every rejecting input built from a distinctive sentinel value, the error
// text must not contain that value.
func TestParseErrorsNeverEchoAttributeValues(t *testing.T) {
	const (
		secretLat = "41.386513"
		secretLon = "-73.907154"
		secretUID = "ANDROID-super-secret-device-id"
		secretTyp = "a--INJECTED"
	)
	cases := []struct {
		name   string
		raw    string
		secret string
	}{
		{"out_of_range_lat", render(defEventAttrs(), with(defPointAttrs(), "lat", "91."+strings.TrimPrefix(secretLat, "41.")), ""), "386513"},
		{"bad_decimal_lon", render(defEventAttrs(), with(defPointAttrs(), "lon", "NaN"+secretLon), ""), secretLon},
		{"negative_ce", render(defEventAttrs(), with(defPointAttrs(), "ce", "-1"), ""), "-1"},
		{"bad_type", render(with(defEventAttrs(), "type", secretTyp), defPointAttrs(), ""), secretTyp},
		{"bad_time", render(with(defEventAttrs(), "time", "2021-06-01T12:00:00"), defPointAttrs(), ""), "2021-06-01T12:00:00"},
		{"bad_version", render(with(defEventAttrs(), "version", "NaN"), defPointAttrs(), ""), "NaN"},
		{"control_char_uid", render(with(defEventAttrs(), "uid", secretUID+"&#10;x"), defPointAttrs(), ""), secretUID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvent([]byte(tc.raw), Limits{})
			if err == nil {
				t.Fatalf("input was expected to be rejected, but parsed clean")
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("parse error leaked the attribute value %q:\n  %v", tc.secret, err)
			}
		})
	}
}
