// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// captureDoer records every request (method, URL, headers and the body bytes) and
// replays a queued sequence of responses, so a test asserts what was delivered and
// drives the retry path deterministically without a live network call.
type captureDoer struct {
	responses []stubResp
	calls     int
	reqs      []capturedReq
}

type stubResp struct {
	status int
	body   string
}

type capturedReq struct {
	method string
	url    string
	header http.Header
	body   []byte
}

func (d *captureDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	// Clone the header so later mutation by the transport cannot race the record.
	h := req.Header.Clone()
	d.reqs = append(d.reqs, capturedReq{method: req.Method, url: req.URL.String(), header: h, body: body})

	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

const testSecret = "whsec_super_secret_value_do_not_leak"

func fixedClock() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }

// wantTS is the unix-seconds string the fixed clock produces.
var wantTS = strconv.FormatInt(fixedClock().Unix(), 10)

// noSleep makes the delivery backoff instantaneous and deterministic in tests.
func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "Over-permissioned service principal",
		Body:     "svc-deploy can write to prod-secrets",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"resource": "prod-secrets", "link": "https://app/findings/42"},
		Time:     time.Date(2026, 6, 3, 11, 59, 0, 0, time.UTC),
	}
}

// newOutput builds an opened connector wired to the given doer and an optional
// signing secret. It uses the fixed clock and an instant sleep.
func newOutput(t *testing.T, doer *captureDoer, secret string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	o.now = fixedClock
	o.sleep = noSleep
	cfg := sdk.Config{Settings: map[string]string{
		"url":            "https://hooks.example.com/ingest",
		"signing_secret": secret,
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeOutput || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor = %+v", d)
	}
	var urlRequired, secretIsSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "url" && f.Required {
			urlRequired = true
		}
		if f.Key == "signing_secret" && f.Secret {
			secretIsSecret = true
		}
	}
	if !urlRequired {
		t.Error("url must be declared Required")
	}
	if !secretIsSecret {
		t.Error("signing_secret must be declared Secret")
	}
}

func TestOpen_RequiresURL(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("expected an error for missing url")
	}
}

// TestNotify_CloudEventsFormat proves the E3 format option: a cloudevents delivery
// is a valid CloudEvents 1.0 envelope (structured) or ce-* headers + data body (binary),
// and the X-Olivares-Signature is preserved and verifies over the EXACT delivered body.
func TestNotify_CloudEventsFormat(t *testing.T) {
	build := func(t *testing.T, mode string) (*Output, *captureDoer) {
		t.Helper()
		doer := &captureDoer{responses: []stubResp{{status: 200, body: "ok"}}}
		o := New()
		o.doer = doer
		o.now = fixedClock
		o.sleep = noSleep
		o.newID = func() (string, error) { return "evt-fixed-id", nil } // deterministic id
		cfg := sdk.Config{Settings: map[string]string{
			"url":              "https://hooks.example.com/ingest",
			"signing_secret":   testSecret,
			"format":           "cloudevents",
			"cloudevents_mode": mode,
		}}
		if err := o.Open(context.Background(), cfg); err != nil {
			t.Fatalf("Open: %v", err)
		}
		return o, doer
	}
	assertSig := func(t *testing.T, req capturedReq) {
		t.Helper()
		ts, sig := req.header.Get(headerTimestamp), req.header.Get(headerSignature)
		if ts == "" || sig == "" {
			t.Fatal("X-Olivares signature headers missing on a cloudevents delivery")
		}
		if !Verify(testSecret, ts, sig, req.body) {
			t.Error("signature does not verify over the delivered cloudevents body")
		}
	}

	t.Run("structured", func(t *testing.T) {
		o, doer := build(t, "structured")
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		req := doer.reqs[0]
		if ct := req.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/cloudevents+json") {
			t.Errorf("content-type = %q, want application/cloudevents+json", ct)
		}
		var env map[string]any
		if err := json.Unmarshal(req.body, &env); err != nil {
			t.Fatalf("structured body is not JSON: %v", err)
		}
		if env["specversion"] != "1.0" || env["id"] != "evt-fixed-id" || env["source"] != "/olivares" {
			t.Errorf("envelope context attrs = %+v", env)
		}
		if env["type"] != "ai.olivares.finding.reported" {
			t.Errorf("type = %v, want ai.olivares.finding.reported", env["type"])
		}
		if _, ok := env["data"]; !ok {
			t.Error("structured envelope missing data member")
		}
		assertSig(t, req)
	})

	t.Run("binary", func(t *testing.T) {
		o, doer := build(t, "binary")
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		req := doer.reqs[0]
		if req.header.Get("ce-id") != "evt-fixed-id" || req.header.Get("ce-source") != "/olivares" ||
			req.header.Get("ce-type") != "ai.olivares.finding.reported" || req.header.Get("ce-specversion") != "1.0" {
			t.Errorf("ce-* context headers = %+v", req.header)
		}
		var data map[string]any
		if err := json.Unmarshal(req.body, &data); err != nil {
			t.Fatalf("binary data body is not JSON: %v", err)
		}
		if data["type"] != "finding.reported" {
			t.Errorf("data.type = %v, want finding.reported (the raw notification)", data["type"])
		}
		assertSig(t, req)
	})
}

func TestNotify_SignedDelivery_HeaderFormatAndVerify(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200, body: "accepted"}}}
	o := newOutput(t, doer, testSecret)

	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("delivered %d requests, want 1", len(doer.reqs))
	}
	req := doer.reqs[0]
	if req.method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.method)
	}
	if req.url != "https://hooks.example.com/ingest" {
		t.Errorf("url = %s", req.url)
	}
	if ct := req.header.Get("Content-Type"); ct != defaultContentTyp {
		t.Errorf("Content-Type = %q, want %q", ct, defaultContentTyp)
	}

	// Timestamp header is the fixed-clock unix seconds.
	if ts := req.header.Get(headerTimestamp); ts != wantTS {
		t.Errorf("%s = %q, want %q", headerTimestamp, ts, wantTS)
	}
	// Signature header is the scheme-versioned "t=<ts>,v1=<hex>" form.
	sig := req.header.Get(headerSignature)
	if !strings.HasPrefix(sig, "t="+wantTS+",v1=") {
		t.Fatalf("%s = %q, want t=%s,v1=...", headerSignature, sig, wantTS)
	}

	// The signature the connector produced must verify against the delivered body
	// — both with the full header value and with the bare v1 hex.
	if !Verify(testSecret, wantTS, sig, req.body) {
		t.Error("Verify rejected an authentic delivery (full header)")
	}
	bareV1 := extractV1(sig)
	if bareV1 == "" {
		t.Fatal("could not extract v1 from signature header")
	}
	if !Verify(testSecret, wantTS, bareV1, req.body) {
		t.Error("Verify rejected an authentic delivery (bare v1)")
	}
}

func TestNotify_BodyIsStableJSONAndCarriesNotificationFields(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 202}}}
	o := newOutput(t, doer, testSecret)
	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var p payload
	if err := json.Unmarshal(doer.reqs[0].body, &p); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if p.Type != n.Type || p.Title != n.Title || p.Body != n.Body {
		t.Errorf("payload core = %+v", p)
	}
	if p.Severity != string(n.Severity) || p.Tenant != n.Tenant {
		t.Errorf("payload severity/tenant = %q/%q", p.Severity, p.Tenant)
	}
	if p.Fields["resource"] != "prod-secrets" || p.Fields["link"] != "https://app/findings/42" {
		t.Errorf("payload fields = %v", p.Fields)
	}
	// Time is RFC3339 UTC, not the zero value.
	if p.Time != "2026-06-03T11:59:00Z" {
		t.Errorf("payload time = %q, want RFC3339 UTC", p.Time)
	}
}

func TestMarshalBody_OmitsZeroTime(t *testing.T) {
	body, err := marshalBody(sdk.Notification{Type: "x.y", Title: "t"})
	if err != nil {
		t.Fatalf("marshalBody: %v", err)
	}
	if strings.Contains(string(body), `"time"`) {
		t.Errorf("zero Time should be omitted, got %s", body)
	}
}

func TestVerify_TamperedBodyFails(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200}}}
	o := newOutput(t, doer, testSecret)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	sig := req.header.Get(headerSignature)

	tampered := append([]byte(nil), req.body...)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the last byte
	if Verify(testSecret, wantTS, sig, tampered) {
		t.Error("Verify accepted a tampered body")
	}
}

func TestVerify_WrongSecretFails(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200}}}
	o := newOutput(t, doer, testSecret)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	sig := req.header.Get(headerSignature)
	if Verify("whsec_a_different_secret", wantTS, sig, req.body) {
		t.Error("Verify accepted a signature under the wrong secret")
	}
}

func TestVerify_TamperedTimestampFails(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200}}}
	o := newOutput(t, doer, testSecret)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	sig := req.header.Get(headerSignature)
	// Re-dating a captured request (the ts is signed) must fail verification.
	otherTS := strconv.FormatInt(fixedClock().Add(time.Hour).Unix(), 10)
	if Verify(testSecret, otherTS, sig, req.body) {
		t.Error("Verify accepted a re-dated (replayed) timestamp")
	}
}

func TestVerify_RejectsEmptySecretAndMalformedSignature(t *testing.T) {
	body := []byte(`{"type":"x"}`)
	if Verify("", wantTS, "v1=deadbeef", body) {
		t.Error("empty secret must never verify")
	}
	if Verify(testSecret, wantTS, "", body) {
		t.Error("empty signature must not verify")
	}
	if Verify(testSecret, wantTS, "t=1,v2=abc", body) {
		t.Error("signature without a v1 component must not verify")
	}
	if Verify(testSecret, wantTS, "v1=zzzz_not_hex", body) {
		t.Error("non-hex signature must not verify")
	}
}

func TestNotify_UnsignedModeOmitsSignatureHeaders(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200}}}
	o := newOutput(t, doer, "") // no signing secret
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if got := req.header.Get(headerTimestamp); got != "" {
		t.Errorf("unsigned: %s should be absent, got %q", headerTimestamp, got)
	}
	if got := req.header.Get(headerSignature); got != "" {
		t.Errorf("unsigned: %s should be absent, got %q", headerSignature, got)
	}
	// The body is still delivered.
	if len(req.body) == 0 {
		t.Error("unsigned delivery sent no body")
	}
}

func TestNotify_RetriesTransient503ThenSucceeds(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{
		{status: 503, body: "unavailable"},
		{status: 200, body: "ok"},
	}}
	o := newOutput(t, doer, testSecret)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify after retry: %v", err)
	}
	if doer.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry after 503)", doer.calls)
	}
	// Every attempt resent an identical, correctly-signed body+headers.
	for i, req := range doer.reqs {
		if !Verify(testSecret, req.header.Get(headerTimestamp), req.header.Get(headerSignature), req.body) {
			t.Errorf("attempt %d delivery did not verify", i+1)
		}
	}
}

func TestNotify_TerminalErrorReturnsError(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 400, body: "bad payload"}}}
	o := newOutput(t, doer, testSecret)
	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("expected an error on a terminal 4xx")
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", doer.calls)
	}
	// The secret must not appear in the surfaced error.
	if strings.Contains(err.Error(), testSecret) {
		t.Errorf("error leaked the signing secret: %v", err)
	}
}

func TestNotify_BeforeOpenFails(t *testing.T) {
	o := New() // never Opened
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify before Open must fail")
	}
}

// TestSecurity_SecretNeverAppearsInBodyOrHeaders is the core minimal-data
// invariant: across signed delivery (including retries), the raw signing secret
// is present nowhere on the wire — not in the body, not in any header value, not
// in any header name.
func TestSecurity_SecretNeverAppearsInBodyOrHeaders(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{
		{status: 503},
		{status: 200},
	}}
	o := newOutput(t, doer, testSecret)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	for i, req := range doer.reqs {
		if strings.Contains(string(req.body), testSecret) {
			t.Errorf("attempt %d body leaked the secret", i+1)
		}
		for name, vals := range req.header {
			if strings.Contains(name, testSecret) {
				t.Errorf("attempt %d header NAME leaked the secret: %q", i+1, name)
			}
			for _, v := range vals {
				if strings.Contains(v, testSecret) {
					t.Errorf("attempt %d header %q value leaked the secret", i+1, name)
				}
			}
		}
	}
}

func TestSign_Deterministic(t *testing.T) {
	body := []byte(`{"type":"x","title":"t"}`)
	a := Sign(testSecret, wantTS, body)
	b := Sign(testSecret, wantTS, body)
	if a != b {
		t.Fatal("Sign is not deterministic for identical inputs")
	}
	// 32-byte HMAC-SHA256 => 64 hex chars.
	if len(a) != 64 {
		t.Fatalf("hex signature length = %d, want 64", len(a))
	}
	// A different timestamp yields a different signature (ts is in the signed bytes).
	if Sign(testSecret, "0", body) == a {
		t.Fatal("signature did not change with the timestamp")
	}
}
