// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siem

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capture is the single HTTP request a stubDoer recorded, decomposed into the
// pieces a test asserts on.
type capture struct {
	method string
	url    *url.URL
	header http.Header
	body   string
}

// stubDoer is an injected delivery.Doer: it records every request it sees and
// returns a fixed response. It never touches the network, so the connector's
// request shaping is asserted deterministically.
type stubDoer struct {
	status   int
	respBody string
	captures []capture
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
	}
	// Clone the header map so later mutations by net/http cannot affect the record.
	h := http.Header{}
	for k, v := range req.Header {
		h[k] = append([]string(nil), v...)
	}
	d.captures = append(d.captures, capture{
		method: req.Method,
		url:    req.URL,
		header: h,
		body:   bodyStr,
	})
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.respBody)),
		Header:     http.Header{},
	}, nil
}

func (d *stubDoer) last() capture {
	if len(d.captures) == 0 {
		return capture{}
	}
	return d.captures[len(d.captures)-1]
}

// sampleNotification mirrors the testdata fixture and the siemfmt sample so the
// connector's formatted bodies can be compared against siemfmt's golden output.
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

const secretToken = "super-secret-credential-do-not-log"

// openOutput builds and opens an Output with the given settings and an injected
// stubDoer + no-wait Sleep so retries never actually pause.
func openOutput(t *testing.T, doer *stubDoer, settings map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	o.sleep = func(context.Context, time.Duration) error { return nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

// assertNoTokenLeak fails if the secret credential appears anywhere except the
// Authorization header (where it legitimately belongs). It scans the request
// body and every non-Authorization header value, and the error string.
func assertNoTokenLeak(t *testing.T, c capture, errStr string) {
	t.Helper()
	if strings.Contains(c.body, secretToken) {
		t.Errorf("secret token leaked into request body: %q", c.body)
	}
	for k, vs := range c.header {
		if http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		for _, v := range vs {
			if strings.Contains(v, secretToken) {
				t.Errorf("secret token leaked into header %q: %q", k, v)
			}
		}
	}
	if strings.Contains(errStr, secretToken) {
		t.Errorf("secret token leaked into error: %q", errStr)
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.siem" || d.Type != sdk.TypeOutput || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor identity wrong: %+v", d)
	}
	// The credential field must be declared Secret so the engine masks it.
	var tokenField *sdk.ConfigField
	for i := range d.ConfigFields {
		if d.ConfigFields[i].Key == "token" {
			tokenField = &d.ConfigFields[i]
		}
	}
	if tokenField == nil || !tokenField.Secret {
		t.Fatalf("token config field must exist and be Secret: %+v", tokenField)
	}
}

func TestOpenValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		wantErr  string
	}{
		{"missing destination", map[string]string{"endpoint": "https://x"}, "destination is required"},
		{"unknown destination", map[string]string{"destination": "kafka", "endpoint": "https://x"}, "unknown destination"},
		{"missing endpoint", map[string]string{"destination": "http"}, "endpoint is required"},
		{"unknown format", map[string]string{"destination": "http", "endpoint": "https://x", "format": "xml"}, "unknown format"},
		{"elastic needs index", map[string]string{"destination": "elastic", "endpoint": "https://x"}, "requires an index"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New().Open(context.Background(), sdk.Config{Settings: tc.settings})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestNotifyBeforeOpen(t *testing.T) {
	if err := New().Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify before Open should error")
	}
}

// --- Splunk ------------------------------------------------------------------

func TestSplunkJSONEvent(t *testing.T) {
	doer := &stubDoer{respBody: `{"text":"Success","code":0}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "splunk", "format": "json",
		"endpoint": "https://hec.example:8088", "token": secretToken,
		"index": "main", "hostname": "edge-01",
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := doer.last()
	if c.method != http.MethodPost {
		t.Errorf("method = %s, want POST", c.method)
	}
	if c.url.Path != "/services/collector/event" {
		t.Errorf("path = %q, want /services/collector/event", c.url.Path)
	}
	if got := c.header.Get("Authorization"); got != "Splunk "+secretToken {
		t.Errorf("auth = %q, want Splunk <token>", got)
	}
	// The HEC envelope must wrap the notification object and carry metadata.
	var env struct {
		Event      notificationView `json:"event"`
		Sourcetype string           `json:"sourcetype"`
		Index      string           `json:"index"`
		Host       string           `json:"host"`
		Time       int64            `json:"time"`
	}
	if err := json.Unmarshal([]byte(c.body), &env); err != nil {
		t.Fatalf("envelope not JSON: %v\n%s", err, c.body)
	}
	if env.Sourcetype != "olivares" || env.Index != "main" || env.Host != "edge-01" {
		t.Errorf("envelope metadata wrong: %+v", env)
	}
	if env.Time != n.Time.Unix() {
		t.Errorf("time = %d, want %d", env.Time, n.Time.Unix())
	}
	if env.Event.Type != "finding.reported" || env.Event.Severity != "high" || env.Event.Fields["resource"] != "public.invoices" {
		t.Errorf("event payload wrong: %+v", env.Event)
	}
	assertNoTokenLeak(t, c, "")
}

func TestSplunkRawTextFormat(t *testing.T) {
	doer := &stubDoer{respBody: `{"text":"Success","code":0}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "splunk", "format": "cef",
		"endpoint": "https://hec.example:8088", "token": secretToken,
		"index": "sec", "hostname": "edge-01",
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := doer.last()
	if c.url.Path != "/services/collector/raw" {
		t.Errorf("raw path = %q, want /services/collector/raw", c.url.Path)
	}
	// Metadata rides as query params on the raw endpoint.
	q := c.url.Query()
	if q.Get("sourcetype") != "olivares" || q.Get("index") != "sec" || q.Get("host") != "edge-01" {
		t.Errorf("raw query params wrong: %v", q)
	}
	if got := c.header.Get("Authorization"); got != "Splunk "+secretToken {
		t.Errorf("auth = %q", got)
	}
	// The body must be exactly siemfmt's CEF output — no re-implementation.
	want := siemfmt.CEF(siemfmt.DefaultDevice(), n)
	if c.body != want {
		t.Errorf("raw body mismatch:\n got: %q\nwant: %q", c.body, want)
	}
	assertNoTokenLeak(t, c, "")
}

func TestSplunkHECLogicalErrorBecomesError(t *testing.T) {
	// HTTP 200 but a non-zero HEC code is a logical rejection => must error.
	doer := &stubDoer{status: 200, respBody: `{"text":"Invalid token","code":4}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "splunk", "format": "json",
		"endpoint": "https://hec.example:8088", "token": secretToken,
	})
	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("expected error from HEC code != 0")
	}
	if !strings.Contains(err.Error(), "code 4") || !strings.Contains(err.Error(), "Invalid token") {
		t.Errorf("error should carry HEC code + text: %v", err)
	}
	assertNoTokenLeak(t, doer.last(), err.Error())
}

func TestSplunkHECCodeZeroSucceeds(t *testing.T) {
	doer := &stubDoer{respBody: `{"text":"Success","code":0}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "splunk", "endpoint": "https://hec.example:8088", "token": secretToken,
	})
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("code 0 should succeed: %v", err)
	}
}

func TestSplunkOTLPWrappedAsEventString(t *testing.T) {
	doer := &stubDoer{respBody: `{"text":"Success","code":0}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "splunk", "format": "otlp",
		"endpoint": "https://hec.example:8088", "token": secretToken,
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := doer.last()
	if c.url.Path != "/services/collector/event" {
		t.Errorf("otlp path = %q, want event endpoint", c.url.Path)
	}
	// The event must be a JSON STRING (the OTLP/JSON doc embedded), decodable
	// back into the OTLP document containing resourceLogs.
	var env struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal([]byte(c.body), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if !strings.Contains(env.Event, "resourceLogs") {
		t.Errorf("OTLP event string should contain resourceLogs: %q", env.Event)
	}
}

// --- Elasticsearch -----------------------------------------------------------

func TestElasticJSONDoc(t *testing.T) {
	doer := &stubDoer{status: 201, respBody: `{"result":"created"}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "elastic", "format": "json",
		"endpoint": "https://es.example:9200", "token": secretToken,
		"index": "olivares-findings",
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := doer.last()
	if c.url.Path != "/olivares-findings/_doc" {
		t.Errorf("path = %q, want /olivares-findings/_doc", c.url.Path)
	}
	if got := c.header.Get("Authorization"); got != "ApiKey "+secretToken {
		t.Errorf("auth = %q, want ApiKey <token>", got)
	}
	if ct := c.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var doc notificationView
	if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
		t.Fatalf("doc not JSON: %v\n%s", err, c.body)
	}
	if doc.Type != "finding.reported" || doc.Severity != "high" || doc.Tenant != "acme" {
		t.Errorf("doc wrong: %+v", doc)
	}
	assertNoTokenLeak(t, c, "")
}

func TestElasticTextFormatWrapsMessage(t *testing.T) {
	doer := &stubDoer{status: 201, respBody: `{"result":"created"}`}
	o := openOutput(t, doer, map[string]string{
		"destination": "elastic", "format": "leef",
		"endpoint": "https://es.example:9200", "token": secretToken,
		"index": "olivares-raw",
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := doer.last()
	var doc struct {
		Message   string           `json:"message"`
		Timestamp string           `json:"@timestamp"`
		Olivares  notificationView `json:"olivares"`
	}
	if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
		t.Fatalf("doc not JSON: %v\n%s", err, c.body)
	}
	want := siemfmt.LEEF(siemfmt.DefaultDevice(), n)
	if doc.Message != want {
		t.Errorf("message mismatch:\n got: %q\nwant: %q", doc.Message, want)
	}
	if doc.Timestamp != n.Time.UTC().Format(time.RFC3339) {
		t.Errorf("@timestamp = %q", doc.Timestamp)
	}
	if doc.Olivares.Type != "finding.reported" {
		t.Errorf("structured notification missing: %+v", doc.Olivares)
	}
	assertNoTokenLeak(t, c, "")
}

func TestElasticIndexPathEscaped(t *testing.T) {
	doer := &stubDoer{status: 201}
	o := openOutput(t, doer, map[string]string{
		"destination": "elastic", "endpoint": "https://es.example:9200",
		"index": "weird index/name",
	})
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// The raw path must keep the escaped index segment, not split on the slash.
	if !strings.HasPrefix(doer.last().url.EscapedPath(), "/weird%20index%2Fname/_doc") {
		t.Errorf("index not path-escaped: %q", doer.last().url.EscapedPath())
	}
}

// --- generic HTTP ------------------------------------------------------------

func TestHTTPDestinationFormats(t *testing.T) {
	n := sampleNotification()
	cases := []struct {
		format      string
		contentType string
		wantBody    func() string
	}{
		{"json", "application/json", func() string {
			b, _ := json.Marshal(notificationJSON(n))
			return string(b)
		}},
		{"cef", "text/plain", func() string { return siemfmt.CEF(siemfmt.DefaultDevice(), n) }},
		{"leef", "text/plain", func() string { return siemfmt.LEEF(siemfmt.DefaultDevice(), n) }},
		{"syslog", "text/plain", func() string {
			return siemfmt.Syslog5424(siemfmt.DefaultDevice(), siemfmt.SyslogOptions{Hostname: "edge-01"}, n)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			doer := &stubDoer{status: 202}
			o := openOutput(t, doer, map[string]string{
				"destination": "http", "format": tc.format,
				"endpoint": "https://collector.example/ingest", "token": secretToken,
				"hostname": "edge-01",
			})
			if err := o.Notify(context.Background(), n); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			c := doer.last()
			if c.url.String() != "https://collector.example/ingest" {
				t.Errorf("url = %q", c.url.String())
			}
			if got := c.header.Get("Authorization"); got != "Bearer "+secretToken {
				t.Errorf("auth = %q, want Bearer <token>", got)
			}
			if ct := c.header.Get("Content-Type"); ct != tc.contentType {
				t.Errorf("content-type = %q, want %q", ct, tc.contentType)
			}
			if c.body != tc.wantBody() {
				t.Errorf("body mismatch:\n got: %q\nwant: %q", c.body, tc.wantBody())
			}
			assertNoTokenLeak(t, c, "")
		})
	}
}

func TestHTTPNoTokenOmitsAuth(t *testing.T) {
	doer := &stubDoer{status: 200}
	o := openOutput(t, doer, map[string]string{
		"destination": "http", "endpoint": "https://collector.example/ingest",
	})
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.last().header.Get("Authorization"); got != "" {
		t.Errorf("auth header should be absent without a token, got %q", got)
	}
}

// --- delivery integration: terminal 4xx surfaces as an error ----------------

func TestDeliveryTerminalErrorPropagates(t *testing.T) {
	doer := &stubDoer{status: 403, respBody: "forbidden"}
	o := openOutput(t, doer, map[string]string{
		"destination": "http", "endpoint": "https://collector.example/ingest", "token": secretToken,
	})
	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("expected error on terminal 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should carry status: %v", err)
	}
	// Only one attempt for a terminal 4xx (delivery does not retry it).
	if len(doer.captures) != 1 {
		t.Errorf("attempts = %d, want 1 (4xx is terminal)", len(doer.captures))
	}
	assertNoTokenLeak(t, doer.last(), err.Error())
}

func TestDeviceOverrideUsedInFormatting(t *testing.T) {
	doer := &stubDoer{status: 200}
	o := openOutput(t, doer, map[string]string{
		"destination": "http", "format": "cef",
		"endpoint": "https://c.example", "vendor": "Acme", "product": "P", "version": "9",
	})
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	want := siemfmt.CEF(siemfmt.Device{Vendor: "Acme", Product: "P", Version: "9"}, n)
	if doer.last().body != want {
		t.Errorf("device override not threaded into siemfmt:\n got: %q\nwant: %q", doer.last().body, want)
	}
	if !strings.HasPrefix(doer.last().body, "CEF:0|Acme|P|9|") {
		t.Errorf("expected device-overridden CEF header: %q", doer.last().body)
	}
}

// TestTestdataFixtureMatchesSample guards that the recorded fixture stays in sync
// with the sample used across the table tests (the JSON shape the connector emits
// for the json format).
func TestTestdataFixtureMatchesSample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "notification.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture notificationView
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("fixture not JSON: %v", err)
	}
	got := notificationJSON(sampleNotification())
	gb, _ := json.Marshal(got)
	fb, _ := json.Marshal(fixture)
	if string(gb) != string(fb) {
		t.Errorf("fixture drifted from sample:\n fixture: %s\n sample:  %s", fb, gb)
	}
}
