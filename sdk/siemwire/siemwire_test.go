// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var dev = Device{Vendor: "Olivares.AI", Product: "ControlPlane", Version: "1"}

func TestCEFStructureAndEscaping(t *testing.T) {
	got := CEF(dev, "evt.type", "the title", 7, []Field{
		{Key: "k", Value: "v=1\nv=2"}, // ext value escapes = and newline
		{Key: "plain", Value: "ok"},
	})
	want := "CEF:0|Olivares.AI|ControlPlane|1|evt.type|the title|7|" + `k=v\=1\nv\=2 plain=ok`
	if got != want {
		t.Fatalf("CEF mismatch:\n got: %q\nwant: %q", got, want)
	}
	// Header pipe and backslash escaping.
	h := CEF(dev, "a|b", `c\d`, 1, nil)
	if !strings.Contains(h, `|a\|b|c\\d|1|`) {
		t.Errorf("header escaping wrong: %q", h)
	}
	// A carriage return in an ext value is escaped (line-safe), not dropped.
	cr := CEF(dev, "t", "n", 0, []Field{{Key: "k", Value: "a\rb"}})
	if !strings.Contains(cr, `k=a\rb`) {
		t.Errorf("CR not escaped in ext: %q", cr)
	}
}

func TestCEFHeaderFieldLimitsAreRuneSafe(t *testing.T) {
	// Multi-byte input at every field limit: the cut must land on a rune boundary
	// and, under the octet reading of the Size column, three-byte runes fit a third
	// of the published number.
	got := CEF(
		Device{
			Vendor:  strings.Repeat("界", 63) + "v",
			Product: strings.Repeat("界", 63) + "p",
			Version: strings.Repeat("界", 31) + "v",
		},
		strings.Repeat("界", 1023)+"i",
		strings.Repeat("界", 512)+"n",
		1,
		nil,
	)
	parts := splitCEFHeader(got)
	if len(parts) != 8 {
		t.Fatalf("CEF header fields = %d, want 8: %q", len(parts), got)
	}
	for _, tc := range []struct {
		name string
		got  string
		max  int
	}{
		{name: "deviceVendor", got: parts[1], max: cefDeviceVendorMax},
		{name: "deviceProduct", got: parts[2], max: cefDeviceProductMax},
		{name: "deviceVersion", got: parts[3], max: cefDeviceVersionMax},
		{name: "deviceEventClassId", got: parts[4], max: cefEventClassIDMax},
		{name: "name", got: parts[5], max: cefNameMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !utf8.ValidString(tc.got) {
				t.Fatalf("truncation split a UTF-8 rune: %q", tc.got)
			}
			if n := len(tc.got); n > tc.max {
				t.Fatalf("%d octets, over the CEF V27 maximum %d", n, tc.max)
			}
			// Nothing is given away either: one more three-byte rune would break it.
			if n := utf8.RuneCountInString(tc.got); n != tc.max/3 {
				t.Fatalf("runes = %d, want %d (the most that fit in %d octets)", n, tc.max/3, tc.max)
			}
		})
	}
}

// splitCEFHeaderRaw splits a CEF record into its 8 fields on the UNESCAPED pipes
// only, keeping each field exactly as it appears on the wire. strings.Split
// cannot be used for this: it also splits on the escaped pipes inside a value.
func splitCEFHeaderRaw(record string) []string {
	var (
		parts []string
		cur   strings.Builder
		esc   bool
	)
	for _, r := range record {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && len(parts) < 7:
			cur.WriteRune(r)
			esc = true
		case r == '|' && len(parts) < 7:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, cur.String())
}

// splitCEFHeader is splitCEFHeaderRaw plus the receiver's decode step (\\ and \|
// back to their characters) for the seven header fields — what a SmartConnector
// must do to recover the values it stores. The extension is left verbatim.
func splitCEFHeader(record string) []string {
	parts := splitCEFHeaderRaw(record)
	for i := range parts {
		if i == len(parts)-1 {
			break
		}
		parts[i] = strings.NewReplacer(`\|`, "|", `\\`, `\`).Replace(parts[i])
	}
	return parts
}

func TestLEEFStructureAndDelimiter(t *testing.T) {
	got := LEEF(dev, "evt", []Field{
		{Key: "sev", Value: "7"},
		// Tab and newline are ESCAPED, not substituted. The tab is the declared
		// delimiter, so the property under test is that neither byte reaches the
		// wire raw (which would forge an attribute boundary) AND that both remain
		// recoverable — the substitution this replaced could only manage the first.
		{Key: "k", Value: "a\tb\nc"},
	})
	want := "LEEF:2.0|Olivares.AI|ControlPlane|1|evt|0x09|sev=7\tk=a%09b%0Ac"
	if got != want {
		t.Fatalf("LEEF mismatch:\n got: %q\nwant: %q", got, want)
	}
	// The delimiter split must still see exactly two attributes: an encoded tab is
	// three printable characters to a receiver that knows nothing about the encoding.
	attrs := strings.Split(got[strings.Index(got, "|0x09|")+len("|0x09|"):], "\t")
	if len(attrs) != 2 {
		t.Fatalf("escaped tab forged an attribute boundary: %q", attrs)
	}
}

func TestSyslogStructure(t *testing.T) {
	r := SyslogRecord{
		PRI:      134,
		Time:     time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC),
		Hostname: "edge-01",
		AppName:  "ControlPlane",
		MsgID:    "finding.reported",
		Params:   []Field{{Key: "k", Value: `a"b\c]d`}},
		Msg:      "title — body\nsecond line",
	}
	got := Syslog5424(r)
	// The MSG's newline is ENCODED rather than collapsed to a space: the record
	// still occupies one line (which is what LF-terminated framing requires) and
	// the break is still legible as a break.
	want := `<134>1 2026-06-03T10:30:00.000000Z edge-01 ControlPlane - finding.reported ` +
		`[olivares@32473 k="a\"b\\c\]d"] title — body%0Asecond line`
	if got != want {
		t.Fatalf("syslog mismatch:\n got: %q\nwant: %q", got, want)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("a raw CR/LF reached the wire and would split the record: %q", got)
	}
}

// TestControlEncodingIsReversible is the property the whole change exists for:
// every byte a value can hold survives the round trip through the rendered line.
// It is a byte-level sweep rather than a handful of interesting cases, because the
// failure it replaces was exactly a byte nobody thought to write a case for.
func TestControlEncodingIsReversible(t *testing.T) {
	for b := 0; b < 256; b++ {
		in := string([]byte{'a', byte(b), 'z'})
		enc := EscapeControlBytes(in)
		for i := 0; i < len(enc); i++ {
			if c := enc[i]; c < 0x20 || c == 0x7f {
				t.Fatalf("byte %#02x: a framing byte survived encoding: %q", b, enc)
			}
		}
		out, ok := UnescapeControlBytes(enc)
		if !ok {
			t.Fatalf("byte %#02x: %q did not decode", b, enc)
		}
		if out != in {
			t.Fatalf("byte %#02x: round trip %q -> %q -> %q", b, in, enc, out)
		}
	}
}

// TestControlEncodingComposesWithTheRFCLayer is the reason the alphabet is percent
// and not backslash, pinned as a property rather than left to the comment that
// explains it. A consumer unwinds RFC 5424 §6.3.3's three mandated escapes first
// and the control layer second; a value must survive BOTH passes unchanged.
//
// The backslash design this replaced fails the `\n` case here: it emits `\\n`, the
// RFC pass yields `\n`, and a backslash-based control decoder then reads a line
// feed — returning a value that was never sent, without any error to show for it.
func TestControlEncodingComposesWithTheRFCLayer(t *testing.T) {
	rfcUnescape := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\]`, `]`)
	for _, in := range []string{
		`\n`, `\r`, `\`, `\\`, `%0A`, `%`, `%%`, `"`, `]`, `a"b\c]d`,
		"\n", "\r", "\t", "\x00", "line\nbreak", `100% of "x"`,
	} {
		wire := escapeSDValue(in)
		if strings.ContainsAny(wire, "\r\n\t\x00") {
			t.Fatalf("%q: a framing byte reached the wire: %q", in, wire)
		}
		out, ok := UnescapeControlBytes(rfcUnescape.Replace(wire))
		if !ok {
			t.Fatalf("%q encoded to %q which did not decode", in, wire)
		}
		if out != in {
			t.Errorf("layers do not compose: %q -> %q -> %q", in, wire, out)
		}
	}
}

// TestUnescapeRejectsWhatThisEncoderNeverEmits: the decoder feeds a hash
// comparison, so a malformed value must fail loudly rather than be repaired into a
// plausible one that was never hashed. It also refuses NON-CANONICAL escapes — a
// spelling the encoder never produces — so one value has exactly one wire form.
func TestUnescapeRejectsWhatThisEncoderNeverEmits(t *testing.T) {
	for _, bad := range []string{
		`a%`, `%`, `%0`, `%ZZ`, `%0Z`, `a%0`,
		`%41`, `%20`, `%2F`, // printable ASCII: never encoded, so never decodable
	} {
		if out, ok := UnescapeControlBytes(bad); ok {
			t.Errorf("decoded %q to %q, want refusal", bad, out)
		}
	}
}

// TestInvalidUTF8IsEncodedRatherThanPassedThroughOrRepaired. Three options exist
// for a byte that is not part of a well-formed UTF-8 sequence and only one is
// right: passing it through leaves a record a conforming RFC 5424 reader must
// refuse (§6.3.3 defines PARAM-VALUE as a UTF-8 string, and this repository's own
// decoder enforces it), repairing it into U+FFFD produces a value that no longer
// hashes to its record, and encoding keeps the line both well-formed and
// reversible.
func TestInvalidUTF8IsEncodedRatherThanPassedThroughOrRepaired(t *testing.T) {
	for _, in := range []string{
		"a\xffz",         // a lone continuation-range byte
		"\xc3",           // a truncated two-byte sequence
		"ok\xed\xa0\x80", // a surrogate half, which UTF-8 forbids
	} {
		enc := EscapeControlBytes(in)
		if !utf8.ValidString(enc) {
			t.Errorf("%q encoded to %q, which is still not valid UTF-8", in, enc)
		}
		out, ok := UnescapeControlBytes(enc)
		if !ok {
			t.Fatalf("%q encoded to %q which did not decode", in, enc)
		}
		if out != in {
			t.Errorf("invalid UTF-8 did not round-trip: %q -> %q -> %q", in, enc, out)
		}
	}
	// Valid multi-byte UTF-8 is left ALONE: encoding it would triple the size of
	// every non-ASCII value for no gain.
	for _, in := range []string{"café", "日本語", "—"} {
		if got := EscapeControlBytes(in); got != in {
			t.Errorf("valid UTF-8 %q was needlessly encoded to %q", in, got)
		}
	}
}

func TestSyslogEmptyParamsNilSD(t *testing.T) {
	got := Syslog5424(SyslogRecord{PRI: 14, AppName: "app", MsgID: "m", Msg: "hi"})
	// No params => the SD element is the NILVALUE "-". No time => "-".
	if !strings.Contains(got, " m - hi") {
		t.Errorf("empty params should yield '-' SD: %q", got)
	}
	if !strings.HasPrefix(got, "<14>1 - ") {
		t.Errorf("zero time should yield '-' timestamp: %q", got)
	}
}

func TestSyslogProcIDAndHostNilValue(t *testing.T) {
	got := Syslog5424(SyslogRecord{PRI: 1, AppName: "a", MsgID: "m"})
	// HOSTNAME and PROCID both NILVALUE; APP present.
	if !strings.HasPrefix(got, "<1>1 - - a - m ") {
		t.Errorf("nil host/procid wrong: %q", got)
	}
}

func TestDefaultSDID(t *testing.T) {
	if DefaultSDID(32473) != "olivares@32473" {
		t.Fatalf("DefaultSDID = %q", DefaultSDID(32473))
	}
}

func syslogHeaderFields(t *testing.T, record string) [4]string {
	t.Helper()
	parts := strings.SplitN(record, " ", 7)
	if len(parts) != 7 {
		t.Fatalf("syslog record has %d space-delimited header parts, want 7: %q", len(parts), record)
	}
	return [4]string{parts[2], parts[3], parts[4], parts[5]}
}

func TestSyslogHeaderFieldLimits(t *testing.T) {
	// RFC 5424 §6 assigns a different octet budget to each identifier. Exercising
	// each boundary independently prevents a shared helper from collapsing distinct
	// host or process identities merely because APP-NAME has the shortest budget.
	tests := []struct {
		name  string
		max   int
		index int
		set   func(*SyslogRecord, string)
	}{
		{name: "hostname", max: 255, index: 0, set: func(r *SyslogRecord, value string) { r.Hostname = value }},
		{name: "app-name", max: 48, index: 1, set: func(r *SyslogRecord, value string) { r.AppName = value }},
		{name: "procid", max: 128, index: 2, set: func(r *SyslogRecord, value string) { r.ProcID = value }},
		{name: "msgid", max: 32, index: 3, set: func(r *SyslogRecord, value string) { r.MsgID = value }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sizes := []int{tc.max, tc.max + 1}
			if tc.name == "hostname" {
				sizes = append([]int{200}, sizes...)
			}
			for _, size := range sizes {
				t.Run(strconv.Itoa(size), func(t *testing.T) {
					record := SyslogRecord{
						PRI: 1, Hostname: "host", AppName: "app", ProcID: "proc", MsgID: "msg",
					}
					value := strings.Repeat("x", size)
					tc.set(&record, value)

					got := syslogHeaderFields(t, Syslog5424(record))[tc.index]
					wantLen := min(size, tc.max)
					if got != value[:wantLen] {
						t.Fatalf("field = %q (%d octets), want %d preserved octets", got, len(got), wantLen)
					}
				})
			}
		})
	}
}

func TestSyslogHeaderFoldsNonPrintUSASCIIByOctet(t *testing.T) {
	// RFC 5424 §6.2 makes the HEADER an ASCII wire structure, so neither a valid
	// multibyte rune nor a malformed UTF-8 byte may make the whole frame undecodable.
	hostile := "aé\xff \x00\x7fz"
	record := SyslogRecord{
		PRI:      1,
		Hostname: hostile,
		AppName:  hostile,
		ProcID:   hostile,
		MsgID:    hostile,
	}
	for i, got := range syslogHeaderFields(t, Syslog5424(record)) {
		const want = "a______z"
		if got != want {
			t.Errorf("header field %d = %q, want one underscore per forbidden input octet: %q", i, got, want)
		}
		for j := 0; j < len(got); j++ {
			if got[j] < '!' || got[j] > '~' {
				t.Errorf("header field %d contains non-PRINTUSASCII byte %#02x: %q", i, got[j], got)
			}
		}
	}
}

func TestSyslogSDParamNamesConformToSDName(t *testing.T) {
	// An empty SDK key still needs a legal on-wire identity, while truncating after
	// sanitization keeps hostile and multibyte inputs inside §6's 32-octet grammar.
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty", key: "", want: "_"},
		{name: "one-over", key: strings.Repeat("k", 33), want: strings.Repeat("k", 32)},
		{name: "non-ascii-and-invalid-utf8", key: "aé\xff", want: "a___"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Syslog5424(SyslogRecord{
				PRI: 1, AppName: "app", MsgID: "msg", Params: []Field{{Key: tc.key, Value: "v"}},
			})
			want := `<1>1 - - app - msg [olivares@32473 ` + tc.want + `="v"]`
			if got != want {
				t.Fatalf("Syslog5424() = %q, want %q", got, want)
			}
		})
	}
}

func TestSyslogSDIDConformsToSDName(t *testing.T) {
	// SD-ID shares SD-NAME's grammar. Treating it as trusted would let a caller's
	// closing bracket manufacture a second structured-data fragment.
	tests := []struct {
		name string
		sdid string
		want string
	}{
		{name: "framing-injection", sdid: "olive\n] forged=\"yes", want: "olive___forged__yes"},
		{name: "one-over", sdid: strings.Repeat("s", 33), want: strings.Repeat("s", 32)},
		{name: "non-ascii-and-invalid-utf8", sdid: "aé\xff", want: "a___"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Syslog5424(SyslogRecord{
				PRI: 1, AppName: "app", MsgID: "msg", SDID: tc.sdid, Params: []Field{{Key: "k", Value: "v"}},
			})
			want := `<1>1 - - app - msg [` + tc.want + ` k="v"]`
			if got != want {
				t.Fatalf("Syslog5424() = %q, want %q", got, want)
			}
		})
	}
}

func TestTransportBudgetMeasuresTheEncodedRecordInBytes(t *testing.T) {
	// The budget is a BYTE count over the record that goes on the wire, not a rune
	// count and not the caller's input size: a record of two-byte runes reaches a
	// receiver's limit at half the character count. Measured on real encoder output
	// so the seam cannot drift away from what an emitter actually sends.
	small := Syslog5424(SyslogRecord{PRI: 134, AppName: "olivares", MsgID: "audit", Msg: "ok"})
	if ExceedsTransportBudget(small, ArcSightSyslogDaemonMaxPayloadBytes) {
		t.Fatalf("a %d-byte record must fit the 1024-byte ArcSight budget", len(small))
	}

	// 600 two-byte runes: under 1024 CHARACTERS, over 1024 BYTES.
	big := Syslog5424(SyslogRecord{
		PRI: 134, AppName: "olivares", MsgID: "audit",
		Params: []Field{{Key: "evidence", Value: strings.Repeat("é", 600)}},
		Msg:    "ok",
	})
	if n := utf8.RuneCountInString(big); n > ArcSightSyslogDaemonMaxPayloadBytes {
		t.Fatalf("test setup: record must be under the budget in RUNES to prove bytes are what count, got %d", n)
	}
	if !ExceedsTransportBudget(big, ArcSightSyslogDaemonMaxPayloadBytes) {
		t.Errorf("a %d-byte record must exceed the 1024-byte ArcSight budget", len(big))
	}
	if ExceedsTransportBudget(big, QRadarTCPDefaultMaxPayloadBytes) {
		t.Errorf("a %d-byte record must still fit the 4096-byte QRadar TCP default", len(big))
	}

	// The boundary is inclusive: exactly at the budget fits, one byte past does not.
	if ExceedsTransportBudget(big, len(big)) {
		t.Error("a record exactly at the budget must fit")
	}
	if !ExceedsTransportBudget(big, len(big)-1) {
		t.Error("a record one byte over the budget must not fit")
	}
}

// TestCEFHeaderFitsBothReadingsOfTheSizeLimit: CEF V27 publishes a Size per
// header field but never says whether it counts decoded characters or the UTF-8
// octets that go on the wire (primary-source check 2026-07-24: the column is
// unlabelled and there is no over-length handling rule). Guessing one reading
// leaves the other violated, so the encoder satisfies both — and, because the cut
// is applied to the value and the escapes are added afterwards, it does so
// without ever splitting a rune or an escape pair.
func TestCEFHeaderFitsBothReadingsOfTheSizeLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "ascii", value: strings.Repeat("a", 400)},
		{name: "cjk", value: strings.Repeat("界", 400)},
		{name: "pipes", value: strings.Repeat("|", 400)},
		{name: "backslashes", value: strings.Repeat(`\`, 400)},
		{name: "mixed", value: strings.Repeat(`a|界\`, 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CEF(Device{Vendor: "v", Product: "p", Version: tc.value}, "sig", "name", 1, nil)
			wire := splitCEFHeaderRaw(got)[3]
			decoded := splitCEFHeader(got)[3]

			if n := len(wire); n > cefDeviceVersionMax {
				t.Errorf("wire field is %d UTF-8 octets, over the %d limit", n, cefDeviceVersionMax)
			}
			if n := utf8.RuneCountInString(decoded); n > cefDeviceVersionMax {
				t.Errorf("decoded value is %d runes, over the %d limit", n, cefDeviceVersionMax)
			}
			if !utf8.ValidString(wire) {
				t.Errorf("truncation split a UTF-8 rune: %q", wire)
			}
			if trailing := len(wire) - len(strings.TrimRight(wire, `\`)); trailing%2 != 0 {
				t.Errorf("dangling escape at the end of the field: %q", wire)
			}
			if parts := splitCEFHeader(got); len(parts) != 8 || parts[4] != "sig" || parts[5] != "name" {
				t.Errorf("header structure broken: %q", got)
			}
			// Nothing is dropped that both readings could have carried: the field is
			// only short when the next rune would have broken one of the two limits.
			if utf8.RuneCountInString(decoded) < utf8.RuneCountInString(tc.value) && len(wire) == 0 {
				t.Errorf("field truncated to nothing: %q", got)
			}
		})
	}
}

// TestFoldedSDNamesDoNotCollide. sanitizeSDName is LOSSY — RFC 5424 forbids '=', ' ',
// ']' and '"' in an SD-NAME, so all of them fold to '_'. Two distinct caller keys can
// therefore land on one name, and emitting both produced two SD-PARAMs sharing it in a
// grammar where a consumer reasonably reads the first, or the last, or errors.
func TestFoldedSDNamesDoNotCollide(t *testing.T) {
	got := Syslog5424(SyslogRecord{PRI: 134, AppName: "app", MsgID: "m", Params: []Field{
		{Key: "a=b", Value: "one"},
		{Key: "a]b", Value: "two"},
		{Key: "a b", Value: "three"},
	}})
	names := map[string]int{}
	for _, part := range strings.Fields(got[strings.Index(got, "["):]) {
		if k, _, ok := strings.Cut(part, "="); ok {
			names[k]++
		}
	}
	for n, c := range names {
		if c > 1 {
			t.Errorf("SD-PARAM name %q appears %d times: %q", n, c, got)
		}
	}
	// Every field is still present — the fix keeps them all rather than dropping the
	// collisions, which is what a caller supplying three fields is entitled to.
	for _, v := range []string{`"one"`, `"two"`, `"three"`} {
		if !strings.Contains(got, v) {
			t.Errorf("value %s was dropped: %q", v, got)
		}
	}
	// And every emitted name stays inside RFC 5424's 32-octet SD-NAME limit.
	long := strings.Repeat("k", rfc5424SDNameMaxOctets)
	got = Syslog5424(SyslogRecord{PRI: 134, AppName: "a", MsgID: "m", Params: []Field{
		{Key: long, Value: "1"}, {Key: long, Value: "2"}, {Key: long, Value: "3"},
	}})
	for _, part := range strings.Fields(got[strings.Index(got, "["):]) {
		if k, _, ok := strings.Cut(part, "="); ok && len(k) > rfc5424SDNameMaxOctets {
			t.Errorf("SD-NAME %q is %d octets, over the RFC 5424 limit", k, len(k))
		}
	}
}

// TestAPreEncodedMSGCannotSplitTheFrame. MsgCarriesEncodedRecord is trusted for the
// ENCODING — re-encoding a CEF or LEEF record destroys its own delimiters — and must
// never be trusted for the FRAMING. The flag is exported, so "the caller promises"
// cannot be the only thing between a stray line feed and a syslog record split into
// two unparseable halves.
func TestAPreEncodedMSGCannotSplitTheFrame(t *testing.T) {
	got := Syslog5424(SyslogRecord{
		PRI: 134, AppName: "app", MsgID: "m",
		Msg:                     "CEF:0|v|p|1|e|n|3|k=a\nsecond line\x00tail",
		MsgCarriesEncodedRecord: true,
	})
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("a pre-encoded MSG put a framing byte on the wire: %q", got)
	}
	// And nothing ELSE was touched: a percent and a tab survive verbatim, because
	// re-encoding them is exactly what destroys the enclosed dialect.
	got = Syslog5424(SyslogRecord{
		PRI: 134, AppName: "app", MsgID: "m",
		Msg:                     "LEEF:2.0|v|p|1|e|0x09|k=50% done\tj=2",
		MsgCarriesEncodedRecord: true,
	})
	if !strings.HasSuffix(got, "LEEF:2.0|v|p|1|e|0x09|k=50% done\tj=2") {
		t.Errorf("a pre-encoded MSG was altered: %q", got)
	}
}

// TestTheDecoderAcceptsOnlyWhatTheEncoderEmits. Its output feeds a hash comparison, so
// two wire forms decoding to one value is exactly what "canonical" has to exclude.
func TestTheDecoderAcceptsOnlyWhatTheEncoderEmits(t *testing.T) {
	for _, bad := range []string{
		"%0a",    // lower-case hex: the encoder emits upper only
		"%c3%a9", // lower-case, and an escape of a byte the encoder never escapes
		"a\nb",   // a raw control byte: the encoder never emits one
		"a\x00b", //
		"a\xffb", // raw invalid UTF-8: the encoder encodes it
		"%41",    // an escape of a printable byte
	} {
		if out, ok := UnescapeControlBytes(bad); ok {
			t.Errorf("decoded non-canonical %q to %q", bad, out)
		}
	}
	// The canonical forms still decode.
	for _, good := range []string{"%0A", "%09", "%25", "%FF", "plain", "café"} {
		if _, ok := UnescapeControlBytes(good); !ok {
			t.Errorf("refused canonical %q", good)
		}
	}
}

// TestCEFAndLEEFHeadersAreValidUTF8. They folded control bytes and passed invalid
// UTF-8 through, leaving the whole record invalid UTF-8 — a state several receivers
// refuse outright, and one the VALUE layer already refuses to create.
func TestCEFAndLEEFHeadersAreValidUTF8(t *testing.T) {
	bad := Device{Vendor: "v\xffx", Product: "p\xffx", Version: "1"}
	for name, line := range map[string]string{
		"cef":  CEF(bad, "sig\xffid", "name\xffx", 3, []Field{{Key: "k", Value: "v"}}),
		"leef": LEEF(bad, "evt\xffid", []Field{{Key: "k", Value: "v"}}),
	} {
		if !utf8.ValidString(line) {
			t.Errorf("%s header left the record invalid UTF-8: %q", name, line)
		}
	}
}

// TestIsEncoderEscapedByteDomain pins the whole 256-byte domain of the predicate the
// decoder admits with, and pins it against the ESCAPER rather than against a second
// copy of the same boolean expression: for every byte, "the encoder escapes it" and
// "the decoder accepts %XX for it" must be the same answer. Spot cases (`%41`, `%20`)
// already existed; a spot case cannot see a term that was dropped or inverted for a
// range nobody sampled, which is exactly the risk in rewriting a negated disjunction.
//
// The byte that shows this test earns its place, measured 2026-08-17: widen the predicate
// with `|| v == 0x40` so the decoder admits `%40`, a byte no pre-existing case samples.
// On the tree BEFORE this test the whole siemwire suite stays green (rc=0); here it fails
// at 0x40 on both legs. The three narrower mutants — dropping the DEL term, widening the
// C0 bound, neutering the call site's negation — do fail here at 0x7f, 0x20 and 0x41/0x7e,
// but TestControlEncodingIsReversible (siemwire_test.go:183) already caught all three, so
// they are regression cover, not evidence of new coverage.
func TestIsEncoderEscapedByteDomain(t *testing.T) {
	for v := 0; v < 256; v++ {
		b := byte(v)
		raw := string([]byte{b})
		escaped := EscapeControlBytes(raw) != raw
		if got := isEncoderEscapedByte(b); got != escaped {
			t.Errorf("byte %#02x: isEncoderEscapedByte=%v but the encoder escaping it is %v",
				b, got, escaped)
		}
		wire := "%" + strings.ToUpper(strconv.FormatInt(int64(v), 16))
		if v < 0x10 {
			wire = "%0" + strings.ToUpper(strconv.FormatInt(int64(v), 16))
		}
		_, ok := UnescapeControlBytes(wire)
		if ok != escaped {
			t.Errorf("byte %#02x: decoder accepts %q = %v, want %v (what the encoder emits)",
				b, wire, ok, escaped)
		}
	}
}
