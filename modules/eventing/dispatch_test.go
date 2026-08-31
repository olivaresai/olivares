// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/siemsink"
)

// Endpoint validation is the authoring-time SSRF/posture gate: https-only,
// no credentials, no reserved literal IPs; loopback (and plain http to it)
// only when explicitly allowed.
func TestValidateEndpointURL(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		allowLoopback bool
		wantErr       string // "" = valid
	}{
		{"https public host", "https://hooks.example.com/x", false, ""},
		{"https public host with port", "https://hooks.example.com:8443/x", false, ""},
		{"plain http refused", "http://hooks.example.com/x", false, "https"},
		{"empty refused", "", false, "required"},
		{"relative refused", "/hook", false, "absolute"},
		{"userinfo refused", "https://u:p@h.example.com/x", false, "credentials"},
		{"private ip refused", "https://10.0.0.5/x", false, "not allowed"},
		{"link-local metadata refused", "https://169.254.169.254/latest", false, "not allowed"},
		{"loopback refused by default", "https://127.0.0.1/x", false, "not allowed"},
		{"localhost refused by default", "https://localhost/x", false, "not allowed"},
		{"loopback ok when allowed", "https://127.0.0.1:9443/x", true, ""},
		{"http loopback ok when allowed", "http://127.0.0.1:9090/x", true, ""},
		{"http localhost ok when allowed", "http://localhost:9090/x", true, ""},
		{"http public still refused when loopback allowed", "http://hooks.example.com/x", true, "https"},
		{"private ip still refused when loopback allowed", "https://192.168.1.7/x", true, "not allowed"},
		{"unspecified refused", "https://0.0.0.0/x", false, "not allowed"},
		{"ipv6 loopback refused by default", "https://[::1]/x", false, "not allowed"},
		{"ipv6 ula refused", "https://[fd00::1]/x", false, "not allowed"},
		{"overlong refused", "https://h.example.com/" + strings.Repeat("a", maxEndpointLen), false, "too long"},
		{"weird scheme refused", "ftp://hooks.example.com/x", false, "https"},
	}
	for _, tc := range cases {
		got := validateEndpointURL(tc.raw, tc.allowLoopback)
		if tc.wantErr == "" && got != "" {
			t.Errorf("%s: unexpected error %q", tc.name, got)
		}
		if tc.wantErr != "" && !strings.Contains(got, tc.wantErr) {
			t.Errorf("%s: error %q, want containing %q", tc.name, got, tc.wantErr)
		}
	}
}

// The dial-time guard re-checks the CONCRETE resolved IP (DNS rebinding): every
// reserved range refuses, loopback only when allowed.
func TestCheckDialIP(t *testing.T) {
	refuse := []string{"10.1.2.3", "172.16.0.9", "192.168.0.1", "169.254.169.254", "224.0.0.1", "0.0.0.0", "fd12::1", "fe80::1", "::"}
	for _, s := range refuse {
		if err := checkDialIP(net.ParseIP(s), false); err == nil {
			t.Errorf("checkDialIP(%s) must refuse", s)
		}
		if err := checkDialIP(net.ParseIP(s), true); err == nil {
			t.Errorf("checkDialIP(%s, allowLoopback) must still refuse non-loopback reserved ranges", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1"} {
		if err := checkDialIP(net.ParseIP(s), false); err == nil {
			t.Errorf("checkDialIP(%s) must refuse by default", s)
		}
		if err := checkDialIP(net.ParseIP(s), true); err != nil {
			t.Errorf("checkDialIP(%s, allowLoopback) should pass: %v", s, err)
		}
	}
	for _, s := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if err := checkDialIP(net.ParseIP(s), false); err != nil {
			t.Errorf("checkDialIP(%s) should pass: %v", s, err)
		}
	}
}

// jitter stays within ±20% and never goes negative or zero for a positive d.
func TestJitterBounds(t *testing.T) {
	d := 10 * time.Second
	for i := 0; i < 200; i++ {
		j := jitter(d)
		if j < 8*time.Second || j > 12*time.Second {
			t.Fatalf("jitter(%v) = %v, outside ±20%%", d, j)
		}
	}
	if jitter(0) != 0 {
		t.Error("jitter(0) must be 0")
	}
}

// TestATwoHundredThatRefusesIsNotDelivered is the repro for the most consequential
// gap in this path: it carries the EVIDENCE LEDGER to a SIEM, and it used to
// classify purely on HTTP status while discarding the response body entirely. A
// destination that answered 200 and refused the payload was recorded as delivered,
// so the ledger's own delivery record claimed a completeness it did not have.
//
// The table is the CONNECTOR's (splunkhec.HECVerdict), not a copy. This path used to
// hold its own and the two had already diverged: it read code 17 — a health answer —
// and an empty body as deliveries, and it required both text and code to be present,
// so the documented submit-with-ack response was unrecognizable to it.
func TestATwoHundredThatRefusesIsNotDelivered(t *testing.T) {
	hec := string(siemsink.KindSplunkHEC)
	for _, tc := range []struct {
		name string
		kind string
		body string
		want string
	}{
		{"hec refusal", hec, `{"text":"Incorrect index","code":7}`, statusDead},
		{"hec busy is retried", hec, `{"text":"Server is busy","code":9}`, statusQueued},
		{"hec capacity warning is an acceptance", hec, `{"text":"queue is approaching its capacity limit","code":24}`, statusDelivered},
		{"hec success", hec, `{"text":"Success","code":0}`, statusDelivered},
		// A body that is NOT a HEC status document is requeued, not counted. HEC always
		// answers with at least one of the members it defines, so an empty body or a
		// stranger's JSON means something that is not HEC replied — a proxy, a load
		// balancer, an endpoint that is not the collector. This lane ships the audit
		// LEDGER, so recording "delivered" for an event nobody confirmed is the exact
		// failure the delivery-truth work exists to remove; it is the same verdict the
		// splunkhec connector draws from the same bytes, and it is now literally the
		// same table. Queued rather than dead: the fail-safe on this path points toward
		// keeping the notification.
		{"an unrecognized body is not a verdict and is not an acceptance", hec, `{"whatever":true}`, statusQueued},
		{"an empty body is not a HEC answer", hec, ``, statusQueued},

		// The gate. This dispatcher POSTs to an operator-configured URL, so matching a
		// body structurally without knowing what is on the other end manufactures
		// refusals a destination never made. A generic collector answering with its own
		// "code" member must be left alone.
		{"a generic collector's code is NOT a hec verdict", "", `{"code":200,"message":"ok"}`, statusDelivered},
		{"a generic collector's errors member is NOT a verdict", "", `{"errors":true}`, statusDelivered},
		{"another sink kind is not probed", string(siemsink.KindDatadog), `{"text":"x","code":7}`, statusDelivered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, named := classifyDispatchBody(tc.kind, []byte(tc.body), true)
			if tc.want == statusDelivered {
				if named {
					t.Fatalf("kind %q body %q overrode a 2xx with %q; it must leave the status standing", tc.kind, tc.body, got)
				}
				return
			}
			if !named || got != tc.want {
				t.Fatalf("kind %q body %q classified (%q, named=%v), want %q", tc.kind, tc.body, got, named, tc.want)
			}
		})
	}
}

// TestAnUnreadableAnswerIsRequeuedNotDiscarded pins the direction of the fail-safe
// on this path. An answer we could not read whole is not evidence of refusal — the
// delivery may well have landed — so it is retried rather than dead-lettered. That
// is the opposite of the connector-side choice, and deliberately so: there the
// caller can still surface an error to an operator, whereas discarding a
// notification here would lose it silently.
func TestAnUnreadableAnswerIsRequeuedNotDiscarded(t *testing.T) {
	got, named := classifyDispatchBody(string(siemsink.KindSplunkHEC), []byte(`{"code":`), false)
	if !named || got != statusQueued {
		t.Fatalf("an incomplete answer classified (%q, named=%v), want it requeued", got, named)
	}
	// But only for a kind whose protocol we would have interpreted. A generic
	// collector says nothing by answering at length, so requeuing its 2xx would
	// retry a delivery that succeeded, for a reason the operator cannot act on.
	if _, named := classifyDispatchBody("", []byte(`a very long opaque body`), false); named {
		t.Fatal("an opaque destination's large answer must leave its 2xx standing")
	}
}
