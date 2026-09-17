// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package webaddr

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// THE TWO GOLDEN TABLES, AND WHY A TABLE RATHER THAN A SECOND IMPLEMENTATION.
//
// The console has to make the same judgement this package makes — "is the address
// in the browser's bar one where a passkey ceremony can run?" — and it cannot
// import Go. The answer is NOT to write the classifier twice. It is to emit what
// this package decided and have the console's test drive the same rows, so a
// divergence is a red test rather than a support ticket.
//
// browser-origins.json carries a second job: it is the corpus for a DIFFERENTIAL
// test against the WHATWG URL implementation installed in this repository (Node).
// That test measures Node live rather than replaying a recorded verdict, so it
// cannot go stale, and every row where the two deliberately disagree carries the
// reason in the row itself. A disagreement with no reason is a failure on both
// sides.

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden tables in testdata")

// classificationRow is one canonical host and everything the console needs to
// know about it.
type classificationRow struct {
	Host         string `json:"host"`
	Kind         string `json:"kind"`
	IP           bool   `json:"ip"`
	Loopback     bool   `json:"loopback"`
	RelyingParty bool   `json:"relyingParty"`
}

// divergence explains a row where this parser and a browser deliberately or
// measurably disagree. An empty Why on a divergent row is not allowed.
type originRow struct {
	Input      string `json:"input"`
	Accepted   bool   `json:"accepted"`
	Origin     string `json:"origin,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       string `json:"port,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Code       string `json:"code,omitempty"`
	Divergence string `json:"divergence,omitempty"`
	Why        string `json:"why,omitempty"`
}

// The hosts the console classifies. Every one of them is a canonical host as a
// browser would report it in window.location.hostname, which is what the console
// actually reads.
var classificationHosts = []string{
	"panel.example.com", "panel.example.com.", "xn--bcher-kva.example",
	"localhost", "localhost.", "foo.localhost", "foo.localhost.",
	"my_host.example.com", "-lead.example", "trail-.example", "olivares",
	// The single label WITH the root dot. The engine and the console disagreed
	// about it and this table could not see the disagreement, because the row did
	// not exist. It exists now.
	"olivares.", "example..",
	"127.0.0.1", "127.1.1.1", "192.168.1.10", "10.0.0.7", "0.0.0.0",
	"[::1]", "[::]", "[2001:db8::1]", "[::ffff:7f00:1]",
}

// codeNames keeps the JSON stable against a reordering of the Code constants: a
// number in a golden file is a number nobody can review.
var codeNames = map[Code]string{
	CodeControlCharacter:   "control_character",
	CodeNotAURL:            "not_a_url",
	CodeNoScheme:           "no_scheme",
	CodeUnsupportedScheme:  "unsupported_scheme",
	CodeUserInfoPresent:    "userinfo_present",
	CodePathPresent:        "path_present",
	CodeQueryPresent:       "query_present",
	CodeFragmentPresent:    "fragment_present",
	CodeNoHost:             "no_host",
	CodePortOutOfRange:     "port_out_of_range",
	CodeHostZoneIdentifier: "host_zone_identifier",
	CodeHostNotAnAddress:   "host_not_an_address",
	CodeHostNotAName:       "host_not_a_name",
}

// The differential corpus: the published table, plus the classes root named —
// percent escapes, Bidi and joiners, numeric overflow, leading and empty labels,
// hyphens — plus the controls that keep each of those honest.
var differentialCorpus = []struct {
	in         string
	divergence string
	why        string
}{
	// The published table.
	{in: "https://panel.example.com:8443"}, {in: "https://panel.example.com:443"},
	{in: "http://localhost:80"}, {in: "https://panel.example.com:0443"},
	{in: "HTTPS://Panel.Example.COM"}, {in: "https://panel.example.com."},
	{in: "https://bücher.example"}, {in: "https://xn--bcher-kva.example"},
	{in: "https://my_host.example.com"}, {in: "https://olivares"},
	{in: "https://LOCALHOST:8443"}, {in: "https://localhost."}, {in: "https://foo.localhost"},
	{in: "https://127.0.0.1:8443"}, {in: "https://192.168.1.10:8443"}, {in: "https://127.1:8443"},
	{in: "https://0x7f000001"}, {in: "https://2130706433"}, {in: "https://0177.0.0.1"},
	{in: "https://[2001:0DB8:0:0:0:0:0:1]:8443"}, {in: "https://[::1]"},
	{in: "https://0.0.0.0:8443"}, {in: "https://[::]:8443"}, {in: "https://0"},
	// IPv6 serialization, including the one form where netip and a browser differ.
	{in: "https://[::ffff:127.0.0.1]"}, {in: "https://[::ffff:1.2.3.4]"},
	{in: "https://[0:0:0:0:0:0:0:1]"}, {in: "https://[1:2:3:4:5:6:7:8]"},
	{in: "https://[2001:db8::1:0:0:1]"}, {in: "https://[::1.2.3.4]"},
	// Numeric overflow, and its controls on the accepting side.
	{in: "https://256.1.1.1"}, {in: "https://99999999999999999999.1"}, {in: "https://1.2.3.4.5"},
	{in: "https://4294967296"}, {in: "https://0x100000000"}, {in: "https://0xffffffff"},
	{in: "https://255.255.255.255"},
	// Wider than uint64, and the LAST part is the wide one so nothing earlier fails
	// first. This class was accepted as a domain until an independent review's
	// overlay probe put it in front of the live comparison below.
	{in: "https://0xffffffffffffffffffff"}, {in: "https://panel.0x10000000000000000"},
	{in: "https://panel.example.0x1ffffffffffffffff"}, {in: "https://a.18446744073709551616"},
	{in: "https://panel.99999999999999999999"},
	// The non-numeric SUFFIX rows: the neighbour that decides is inside the same
	// label, after the digits that overflow. These kill a parser that stops
	// scanning at the first overflow — it would refuse what a browser accepts.
	{in: "https://panel.0xfffffffffffffffffffzz"}, {in: "https://panel.0xzz"},
	{in: "https://panel.0x1g"}, {in: "https://99999999999999999999z.example"},
	{in: "https://panel.1a"},
	// Leading, empty and trailing labels.
	{in: "https://.panel.example.com"}, {in: "https://.a.example"}, {in: "https://a..example"},
	{in: "https://a.example."}, {in: "https://.."},
	{in: "https://example.."}, {in: "https://panel.example.com.."},
	{in: "https://xn--.example"}, {in: "https://xn--a.example"},
	{in: "https://XN--.example"}, {in: "https://xn--\u3002example"},
	// The compatibility spellings of the prefix, and the ignored-only labels they
	// must NOT be confused with. Both directions are measured against the
	// installed WHATWG implementation on every run of the console suite.
	{in: "https://\uff58\uff4e\uff0d\uff0d.example"}, {in: "https://xn\uff0d\uff0d.example"},
	{in: "https://xn-\u00ad-.example"}, {in: "https://\u00adxn--.example"},
	{in: "https://panel.\uff58\uff4e\uff0d\uff0d"},
	{in: "https://\u00ad.example"}, {in: "https://a.\u200b.example"},
	// Separator mapping and ignorable deletion, which change the label structure.
	{in: "https://a\u3002example"}, {in: "https://a\uff61example"},
	{in: "https://panel\uff0eexample\uff0ecom"}, {in: "https://a.\u00ad.example"},
	// Hyphens: browsers do not apply CheckHyphens, so all three open.
	{in: "https://-lead.example"}, {in: "https://trail-.example"}, {in: "https://ab--cd.example"},
	{in: "https://aa--bb.example"},
	// Bidi and joiners.
	{in: "https://א.example"}, {in: "https://א1.example"}, {in: "https://xn--4dbrk0ce.example"},
	// The joiner characters are written as escapes on purpose: a zero-width
	// codepoint in a source literal is invisible to a reviewer and to a diff.
	{in: "https://a\u200db.example"}, {in: "https://a\u200cb.example"},
	{in: "https://例え.テスト"}, {in: "https://exÄmple.com"},
	{
		in:         "https://1א.example",
		divergence: "library-difference",
		why: "golang.org/x/net/idna's BidiRule refuses a bidi domain whose first character is a European number (RFC 5893 rule 1); " +
			"the ICU implementation behind the installed Node URL accepts it. We refuse MORE than a browser here. Recorded rather than " +
			"worked around: the alternative is dropping the Bidi rule, which the URL Standard requires, for an IDN class no deployment uses.",
	},
	// Percent escapes.
	{in: "https://a%2Fb.example"}, {in: "https://a%00b.example"},
	{
		in:         "https://ex%61mple.com",
		divergence: "console-origin-restriction",
		why: "net/url refuses any percent escape in a host, so a percent-escaped spelling of a legal host is refused instead of decoded. " +
			"Declare the host literally. Refusing is the safe direction: this parser never accepts an address a browser would reject.",
	},
	{
		in:         "https://%41.example",
		divergence: "console-origin-restriction",
		why:        "as above: a percent-escaped host is refused rather than decoded.",
	},
	// The console-origin restrictions: a browser parses these, and none of them is
	// a value this field may carry.
	{in: "https://panel.example.com:70000"}, {in: "https://"}, {in: "https://my host.example"},
	{in: "https://[fe80::1%25eth0]:8443"},
	{
		in:         "https://panel.example.com:0",
		divergence: "console-origin-restriction",
		why:        "port 0 means \"let the kernel choose\" to a listener and nothing at all to a browser; nobody can open it.",
	},
	{
		in:         "https://panel.example.com/olivares",
		divergence: "console-origin-restriction",
		why:        "the console is served at the root of its origin, so a path would print an address that does not answer.",
	},
	{
		in:         "https://panel.example.com?a=1",
		divergence: "console-origin-restriction",
		why:        "a query is not part of an origin, and its values can carry secrets.",
	},
	{
		in:         "https://panel.example.com#x",
		divergence: "console-origin-restriction",
		why:        "a fragment is not part of an origin and never reaches the server.",
	},
	{
		in:         "https://ops:pw@panel.example.com",
		divergence: "console-origin-restriction",
		why:        "userinfo is not part of an origin, and this is the value class that made the old work print a password into the process log.",
	},
	{
		in:         "ftp://panel.example.com",
		divergence: "console-origin-restriction",
		why:        "the console is served over https, or over http behind a TLS-terminating proxy. No other scheme reaches it.",
	},
	{
		in:         "panel.example.com:8443",
		divergence: "console-origin-restriction",
		why:        "a browser parses this as an opaque URL with a null origin, which is not an address anybody can be sent to.",
	},
}

func writeGolden(t *testing.T, name string, v any) {
	t.Helper()
	want, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("testdata", name)
	got, readErr := os.ReadFile(path)
	if *updateGolden {
		if readErr == nil && string(got) == string(want) {
			return
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", path)
		return
	}
	if readErr != nil {
		t.Fatalf("%s is missing; run: go test ./core/webaddr -update-golden", path)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale. The console's test drives this file, so an edit to the Go classification that is not regenerated makes the two disagree silently.\nRun: go test ./core/webaddr -update-golden", path)
	}
}

// T6. The tables the console consumes are current. Mutants: change a
// classification in Go and do not regenerate; edit the JSON by hand.
func TestGoldenTablesAreCurrent(t *testing.T) {
	rows := make([]classificationRow, 0, len(classificationHosts))
	for _, h := range classificationHosts {
		host, kind, code := ParseHost(h)
		if code != 0 {
			t.Fatalf("ParseHost(%q) refused with code %d; the classification table may only carry hosts a browser produces", h, code)
		}
		a := Address{Scheme: "https", Host: host, Kind: kind, Origin: originOf("https", host, kind, "")}
		rows = append(rows, classificationRow{
			Host: host, Kind: kind.String(), IP: a.IsIP(), Loopback: a.IsLoopback(),
			RelyingParty: a.CanBeRelyingParty(),
		})
	}
	writeGolden(t, "address-classification.json", rows)

	origins := make([]originRow, 0, len(differentialCorpus))
	for _, c := range differentialCorpus {
		if (c.divergence == "") != (c.why == "") {
			t.Fatalf("%q: a divergent row must say why, and an agreeing row must not claim a divergence", c.in)
		}
		row := originRow{Input: c.in, Divergence: c.divergence, Why: c.why}
		a, err := Parse("--public-url", c.in)
		if err != nil {
			var ref *Refusal
			if !asRefusal(err, &ref) {
				t.Fatalf("Parse(%q) returned %T, want *Refusal", c.in, err)
			}
			name, ok := codeNames[ref.Code]
			if !ok {
				t.Fatalf("Code %d has no stable name; add it to codeNames", ref.Code)
			}
			row.Code = name
		} else {
			row.Accepted = true
			row.Origin, row.Host, row.Port, row.Kind = a.Origin, a.Host, a.Port, a.Kind.String()
		}
		origins = append(origins, row)
	}
	writeGolden(t, "browser-origins.json", origins)
}

func asRefusal(err error, out **Refusal) bool {
	r, ok := err.(*Refusal)
	if ok {
		*out = r
	}
	return ok
}
