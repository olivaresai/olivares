// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// sampleNotification has out-of-order fields (to prove sorting) and a value with
// metacharacters (to prove escaping).
func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "least-privilege drift",
		Body:     "role billing can write public.invoices",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields: map[string]string{
			"resource": "public.invoices",
			"origin":   "billing",
			"mode":     "readwrite",
		},
		Time: time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC),
	}
}

func TestCEFGolden(t *testing.T) {
	got := CEF(DefaultDevice(), sampleNotification())
	want := "CEF:0|Olivares.AI|ControlPlane|1|finding.reported|least-privilege drift|7|" +
		"mode=readwrite origin=billing resource=public.invoices tenant=acme " +
		"msg=role billing can write public.invoices"
	if got != want {
		t.Errorf("CEF mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestCEFEscaping(t *testing.T) {
	n := sdk.Notification{
		Type:     "a|b", // header pipe must be escaped
		Title:    `c\d`, // header backslash must be escaped
		Severity: model.SeverityInfo,
		Fields:   map[string]string{"k": "v=1\nv=2"}, // ext value escapes = and newline
	}
	got := CEF(DefaultDevice(), n)
	if !strings.Contains(got, `|a\|b|`) {
		t.Errorf("pipe not escaped in header: %q", got)
	}
	if !strings.Contains(got, `|c\\d|`) {
		t.Errorf("backslash not escaped in header: %q", got)
	}
	if !strings.Contains(got, `k=v\=1\nv\=2`) {
		t.Errorf("equals/newline not escaped in extension: %q", got)
	}
}

func TestLEEFGolden(t *testing.T) {
	n := sampleNotification()
	got := LEEF(DefaultDevice(), n)
	want := "LEEF:2.0|Olivares.AI|ControlPlane|1|finding.reported|0x09|" +
		strings.Join([]string{
			"sev=7",
			"devTime=" + strconv.FormatInt(n.Time.UnixMilli(), 10),
			"title=least-privilege drift",
			"mode=readwrite",
			"origin=billing",
			"resource=public.invoices",
			"tenant=acme",
			"msg=role billing can write public.invoices",
		}, "\t")
	if got != want {
		t.Errorf("LEEF mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestLEEFSeverityRangeStartsAtOne(t *testing.T) {
	got := LEEF(DefaultDevice(), sdk.Notification{Type: "evt"})
	if !strings.Contains(got, "|0x09|sev=1") {
		t.Fatalf("LEEF 2.0 sev must be in the 1-10 range: %q", got)
	}
}

func TestLEEFReservedAttributesCannotBeOverridden(t *testing.T) {
	n := sampleNotification()
	n.Fields["sev"] = "0"
	n.Fields["devTime"] = "not-an-epoch"
	n.Fields["devTimeFormat"] = "yyyy-MM-dd"

	got := LEEF(DefaultDevice(), n)
	if strings.Count(got, "sev=") != 1 || strings.Contains(got, "sev=0") {
		t.Fatalf("caller field overrode the LEEF 2.0 sev mapping: %q", got)
	}
	wantTime := "devTime=" + strconv.FormatInt(n.Time.UnixMilli(), 10)
	if strings.Count(got, "devTime=") != 1 || !strings.Contains(got, wantTime) {
		t.Fatalf("caller field overrode epoch devTime %q: %q", wantTime, got)
	}
	if strings.Contains(got, "devTimeFormat=") {
		t.Fatalf("13-digit epoch devTime must not carry devTimeFormat: %q", got)
	}
}

func TestLEEFDelimiterInValueNeutralized(t *testing.T) {
	n := sdk.Notification{
		Type:     "evt",
		Severity: model.SeverityLow,
		// A tab is the declared LEEF delimiter and a newline ends the record: neither
		// may reach the wire raw. They are now ESCAPED rather than replaced by a
		// space, so the framing guarantee is unchanged and the bytes are no longer
		// lost — a value that carried a tab used to be indistinguishable from one
		// that carried a space.
		Fields: map[string]string{"k": "a\tb\nc"},
	}
	got := LEEF(DefaultDevice(), n)
	if strings.ContainsAny(got, "\t\r\n"[1:]) || strings.Contains(got, "a\tb") {
		t.Errorf("a framing byte survived into the value: %q", got)
	}
	if !strings.Contains(got, "k=a%09b%0Ac") {
		t.Errorf("expected the encoded value: %q", got)
	}
	// And it is genuinely recoverable, not merely encoded.
	back, ok := siemwire.UnescapeControlBytes("a%09b%0Ac")
	if !ok || back != "a\tb\nc" {
		t.Errorf("value did not round-trip: %q ok=%v", back, ok)
	}
}

func TestSyslog5424Golden(t *testing.T) {
	got := Syslog5424(DefaultDevice(), SyslogOptions{Hostname: "edge-01"}, sampleNotification())
	want := "<131>1 2026-06-03T10:30:00.000000Z edge-01 ControlPlane - finding.reported " +
		`[olivares@32473 mode="readwrite" origin="billing" resource="public.invoices" tenant="acme"] ` +
		"least-privilege drift — role billing can write public.invoices"
	if got != want {
		t.Errorf("syslog mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestSyslogPRIComputation(t *testing.T) {
	// local0 (16) * 8 + crit (2) = 130.
	n := sdk.Notification{Type: "x", Severity: model.SeverityCritical, Time: time.Unix(0, 0).UTC()}
	got := Syslog5424(DefaultDevice(), SyslogOptions{}, n)
	if !strings.HasPrefix(got, "<130>1 ") {
		t.Errorf("PRI for critical/local0 should be 130: %q", got)
	}
	// low is notice(5), NOT info(6): local0 (16)*8 + 5 = 133. A collector selector
	// that forwards notice-and-above must keep receiving low-severity findings, and
	// info(6) would make a low finding byte-identical to an informational one.
	low := sdk.Notification{Type: "x", Severity: model.SeverityLow}
	gotLow := Syslog5424(DefaultDevice(), SyslogOptions{}, low)
	if !strings.HasPrefix(gotLow, "<133>1 ") {
		t.Errorf("PRI for low/local0 should be 133 (notice): %q", gotLow)
	}
	// Custom facility local1 (17) * 8 + info (6) = 142.
	n2 := sdk.Notification{Type: "x", Severity: model.SeverityInfo}
	got2 := Syslog5424(DefaultDevice(), SyslogOptions{Facility: 17}, n2)
	if !strings.HasPrefix(got2, "<142>1 ") {
		t.Errorf("PRI for info/local1 should be 142: %q", got2)
	}
}

func TestSyslogStructuredDataEscaping(t *testing.T) {
	n := sdk.Notification{
		Type:     "x",
		Severity: model.SeverityInfo,
		Fields:   map[string]string{"k": `a"b\c]d`},
	}
	got := Syslog5424(DefaultDevice(), SyslogOptions{}, n)
	if !strings.Contains(got, `k="a\"b\\c\]d"`) {
		t.Errorf("SD value escaping wrong: %q", got)
	}
}

func TestSyslogEmptyFieldsNilSD(t *testing.T) {
	// A notification with no fields and no tenant has no structured data at all, so the
	// SD position is RFC 5424's NILVALUE. The type does not change that: it travels in
	// MSGID, not as a field. (An earlier revision of this test claimed a synthetic
	// "eventType" field kept SD populated and then asserted nothing about it; no such
	// field is emitted in any format.)
	typed := Syslog5424(DefaultDevice(), SyslogOptions{}, sdk.Notification{
		Type: "x", Severity: model.SeverityInfo, Title: "hi",
	})
	if !strings.Contains(typed, " - hi") {
		t.Errorf("a typed notification with no fields should still yield NILVALUE SD: %q", typed)
	}
	if !strings.Contains(typed, " x ") {
		t.Errorf("the type should appear as MSGID: %q", typed)
	}
	untyped := Syslog5424(DefaultDevice(), SyslogOptions{}, sdk.Notification{
		Severity: model.SeverityInfo, Title: "hi",
	})
	if !strings.Contains(untyped, " - hi") {
		t.Errorf("empty fields should yield NILVALUE '-' for SD: %q", untyped)
	}
}

func TestOTLPLogsData(t *testing.T) {
	ld, err := OTLPLogsData(DefaultDevice(), sampleNotification())
	if err != nil {
		t.Fatalf("OTLPLogsData: %v", err)
	}
	if len(ld.ResourceLogs) != 1 {
		t.Fatalf("want 1 ResourceLogs, got %d", len(ld.ResourceLogs))
	}
	rl := ld.ResourceLogs[0]
	if len(rl.ScopeLogs) != 1 || len(rl.ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("want 1 scope/1 record")
	}
	rec := rl.ScopeLogs[0].LogRecords[0]
	if rec.SeverityText != "HIGH" {
		t.Errorf("severity text = %q, want HIGH", rec.SeverityText)
	}
	if rec.SeverityNumber.String() != "SEVERITY_NUMBER_ERROR" {
		t.Errorf("severity number = %v, want ERROR", rec.SeverityNumber)
	}
	if rec.TimeUnixNano != uint64(time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC).UnixNano()) {
		t.Errorf("time mismatch: %d", rec.TimeUnixNano)
	}
	body := rec.Body.GetStringValue()
	if body != "least-privilege drift — role billing can write public.invoices" {
		t.Errorf("body = %q", body)
	}
	// The event type travels in OTLP's dedicated member, not as a synthetic attribute:
	// LogRecord.event_name exists for exactly this, and an "eventType" attribute could be
	// shadowed by a caller field of the same name — a duplicate key, which OTLP forbids.
	if rec.EventName != "finding.reported" {
		t.Errorf("event name = %q, want finding.reported", rec.EventName)
	}
	// Attributes are the ordered caller fields plus the AUTHORITATIVE tenant, which is a
	// product key (ai.olivares.tenant.id) precisely so a caller field named "tenant"
	// cannot replace it. The text formats keep the caller-owned "tenant" spelling; OTLP
	// has a reserved namespace and uses it.
	want := map[string]string{
		"mode": "readwrite", "origin": "billing", "resource": "public.invoices",
		"ai.olivares.tenant.id": "acme",
	}
	got := map[string]string{}
	for _, a := range rec.Attributes {
		got[a.Key] = a.Value.GetStringValue()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("record has %d attributes, want exactly %d: %v", len(got), len(want), got)
	}
	if _, ok := got["eventType"]; ok {
		t.Error("the synthetic eventType attribute is still emitted")
	}
}

func TestOTLPLogJSONRoundtrips(t *testing.T) {
	b, err := OTLPLogJSON(DefaultDevice(), sampleNotification())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "resourceLogs") {
		t.Errorf("OTLP/JSON should contain resourceLogs: %s", b)
	}
}

func TestDeterministicOrdering(t *testing.T) {
	n := sampleNotification()
	cef, leef, syslog := CEF(DefaultDevice(), n), LEEF(DefaultDevice(), n), Syslog5424(DefaultDevice(), SyslogOptions{}, n)
	// Re-render many times (each pass iterates the Fields map afresh) and require
	// byte-identity to the first render — proving the key sort defeats map order.
	for i := 0; i < 50; i++ {
		if got := CEF(DefaultDevice(), n); got != cef {
			t.Fatalf("CEF output not deterministic:\n %q\n %q", cef, got)
		}
		if got := LEEF(DefaultDevice(), n); got != leef {
			t.Fatalf("LEEF output not deterministic:\n %q\n %q", leef, got)
		}
		if got := Syslog5424(DefaultDevice(), SyslogOptions{}, n); got != syslog {
			t.Fatalf("syslog output not deterministic:\n %q\n %q", syslog, got)
		}
	}
}

func TestDeviceOverride(t *testing.T) {
	got := CEF(Device{Vendor: "Acme", Product: "P", Version: "9"}, sdk.Notification{Type: "t", Severity: model.SeverityInfo})
	if !strings.HasPrefix(got, "CEF:0|Acme|P|9|t||1|") {
		t.Errorf("device override not applied: %q", got)
	}
}

func TestLEEFOwnsReservedAttributesWithoutATimestampToo(t *testing.T) {
	// The dangerous branch is the one WITHOUT a typed timestamp: there the encoder
	// has no devTime of its own, and a caller field would otherwise reach QRadar as
	// the record's devTime — unvalidated, and with no devTimeFormat to parse it by.
	// The encoder owns the reserved attributes unconditionally, and nothing is lost:
	// the caller's values travel under olv-prefixed keys.
	n := sdk.Notification{Type: "evt", Fields: map[string]string{
		"sev":           "9",
		"devTime":       "not-an-epoch",
		"devTimeFormat": "yyyy-MM-dd",
	}}
	got := LEEF(DefaultDevice(), n)

	if strings.Contains(got, "devTime=not-an-epoch") || strings.Contains(got, "devTimeFormat=yyyy-MM-dd") {
		t.Errorf("caller metadata became the record's LEEF timestamp: %q", got)
	}
	if strings.Count(got, "sev=") != 1 || !strings.Contains(got, "|0x09|sev=1") {
		t.Errorf("caller field overrode the normalized sev: %q", got)
	}
	for _, want := range []string{"olvSev=9", "olvDevTime=not-an-epoch", "olvDevTimeFormat=yyyy-MM-dd"} {
		if !strings.Contains(got, want) {
			t.Errorf("caller field silently dropped, want %q in %q", want, got)
		}
	}
}

func TestLEEFReservedAttributeGuardIsCaseInsensitive(t *testing.T) {
	// LEEF attribute names are conventionally camelCase; a caller field differing
	// only in case must not risk a duplicate/ambiguous attribute at the receiver.
	n := sampleNotification()
	n.Fields["DevTime"] = "9999"
	n.Fields["SEV"] = "9"
	got := LEEF(DefaultDevice(), n)
	// Anchored on the tab delimiter: an attribute NAME starts a field, so this
	// cannot be satisfied by the olv-prefixed rewrite.
	if strings.Contains(got, "\tDevTime=9999") || strings.Contains(got, "\tSEV=9") {
		t.Errorf("case-variant reserved keys reached the wire verbatim: %q", got)
	}
	if !strings.Contains(got, "olvDevTime=9999") || !strings.Contains(got, "olvSev=9") {
		t.Errorf("case-variant reserved keys were dropped instead of re-keyed: %q", got)
	}
}

// TestSeverityColumnsStayDistinct pins the property that five green suites missed
// when the syslog column was rebanded: every severity the product can determine
// must remain distinguishable in each format's scale. A collector selector, an
// ArcSight rule or a Sentinel DCR filters on the emitted number, so two product
// severities sharing one number is a filtering distinction destroyed at the wire,
// silently and irreversibly.
func TestSeverityColumnsStayDistinct(t *testing.T) {
	determined := []model.Severity{
		model.SeverityInfo, model.SeverityLow, model.SeverityMedium,
		model.SeverityHigh, model.SeverityCritical,
	}
	seenSyslog := map[int]model.Severity{}
	seenCEF := map[int]model.Severity{}
	for _, s := range determined {
		m := mapSeverity(s)
		if other, dup := seenSyslog[m.syslog]; dup {
			t.Errorf("syslog severity %d is shared by %q and %q", m.syslog, other, s)
		}
		if other, dup := seenCEF[m.cef]; dup {
			t.Errorf("CEF severity %d is shared by %q and %q", m.cef, other, s)
		}
		seenSyslog[m.syslog], seenCEF[m.cef] = s, s
		if m.syslog < 0 || m.syslog > 7 {
			t.Errorf("%q: syslog severity %d is outside the RFC 5424 0-7 range", s, m.syslog)
		}
		if m.cef < 0 || m.cef > 10 {
			t.Errorf("%q: CEF severity %d is outside the 0-10 range", s, m.cef)
		}
	}
	// An undetermined severity is allowed to share info's PRI — it carries no
	// determination to lose — but must stay CEF 0 (Unknown), not a fabricated band.
	if m := mapSeverity(""); m.cef != 0 {
		t.Errorf("unknown severity must map to CEF 0 (Unknown), got %d", m.cef)
	}
}

// TestCarriedRecordsAreFramingSafeWithoutASecondPass is the invariant that makes
// SyslogWithMsg's pass-through legal, and it is checked here rather than asserted in
// a comment because the safety is a property of the CEF and LEEF encoders, not
// something the syslog layer can know on its own.
//
// The regression it guards against was real and silent: percent-encoding an
// already-built LEEF record turned its DECLARED tab delimiters into "%09", so a
// QRadar receiver splitting MSG on 0x09 — as the "|0x09|" in the record's own header
// instructs it to — saw one attribute where there were several. CEF fared no better:
// a literal "%" in "50% done" is already "%25" inside the record, and a second pass
// made it "%2525". There was no test for CEF-or-LEEF-over-syslog at all.
func TestCarriedRecordsAreFramingSafeWithoutASecondPass(t *testing.T) {
	n := sdk.Notification{
		Type:     "evt",
		Severity: model.SeverityHigh,
		Title:    "T",
		// Every byte that could break either the enclosed dialect or the syslog frame.
		Fields: map[string]string{"note": "a\tb\nc\rd\x00e", "pct": "50% done"},
	}
	dev := DefaultDevice()

	for _, tc := range []struct {
		name     string
		built    string
		wireLine string
	}{
		{name: "cef", built: CEF(dev, n), wireLine: SyslogWithMsg(dev, SyslogOptions{}, n, CEF(dev, n))},
		{name: "leef", built: LEEF(dev, n), wireLine: SyslogWithMsg(dev, SyslogOptions{}, n, LEEF(dev, n))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 1. The enclosed record reaches the wire BYTE-FOR-BYTE. Anything else means
			//    the syslog layer re-encoded a record that was already encoded.
			if !strings.HasSuffix(tc.wireLine, tc.built) {
				t.Fatalf("the carried record was altered on the way out:\n built: %q\n wire:  %q",
					tc.built, tc.wireLine)
			}
			// 2. Passing it through is only SAFE because the record carries no framing
			//    byte of its own. If a dialect ever stops guaranteeing that, this fails
			//    here rather than at a receiver.
			for i := 0; i < len(tc.built); i++ {
				if c := tc.built[i]; c == '\r' || c == '\n' || c == 0x00 {
					t.Fatalf("the %s record carries a framing byte %#02x, so it cannot be passed through", tc.name, c)
				}
			}
			// 3. And the whole syslog line still holds together.
			if strings.ContainsAny(tc.wireLine, "\r\n\x00") {
				t.Fatalf("a framing byte reached the syslog line: %q", tc.wireLine)
			}
		})
	}

	// LEEF specifically: the declared TAB delimiters must survive, because they are
	// the record's structure and not incidental whitespace.
	leefWire := SyslogWithMsg(dev, SyslogOptions{}, n, LEEF(dev, n))
	if got := strings.Count(leefWire, "\t"); got < 2 {
		t.Errorf("LEEF carried over syslog kept %d tab delimiters, want the record's own: %q", got, leefWire)
	}

	// The FREE-TEXT path is unaffected and still encodes: a title with a newline must
	// not put a raw LF on the wire.
	plain := Syslog5424(dev, SyslogOptions{}, sdk.Notification{
		Type: "evt", Severity: model.SeverityLow, Title: "line one\nline two",
	})
	if strings.ContainsAny(plain, "\r\n") {
		t.Errorf("a free-text MSG put a raw newline on the wire: %q", plain)
	}
	if !strings.Contains(plain, "%0A") {
		t.Errorf("a free-text MSG lost its newline instead of encoding it: %q", plain)
	}
}
