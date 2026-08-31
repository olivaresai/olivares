// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package datadog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "governance.policy.denied",
		Title:    "policy denied tool call",
		Body:     "agent claude-1 blocked from write",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1", "decision": "deny"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

type capture struct {
	method      string
	path        string
	contentType string
	apiKey      string
	body        []byte
}

func newServer(t *testing.T, status int, respBody string, cap *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.contentType = r.Header.Get("Content-Type")
		cap.apiKey = r.Header.Get("DD-API-KEY")
		cap.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
}

func open(t *testing.T, o *Output, settings map[string]string) {
	t.Helper()
	if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestNotifyRoundTrip(t *testing.T) {
	var cap capture
	// Datadog acknowledges a valid batch with 202 Accepted (empty body).
	srv := newServer(t, http.StatusAccepted, "", &cap)
	defer srv.Close()

	o := New()
	open(t, o, map[string]string{
		"endpoint": srv.URL + "/api/v2/logs",
		"api_key":  "sekret-key",
	})
	defer o.Close(context.Background())

	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/api/v2/logs" {
		t.Errorf("path = %q, want /api/v2/logs", cap.path)
	}
	if cap.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", cap.contentType)
	}
	if cap.apiKey != "sekret-key" {
		t.Errorf("DD-API-KEY = %q, want sekret-key", cap.apiKey)
	}

	// The body is a JSON ARRAY of exactly one log object.
	var entries []map[string]any
	if err := json.Unmarshal(cap.body, &entries); err != nil {
		t.Fatalf("body is not a JSON array: %v (body=%s)", err, cap.body)
	}
	if len(entries) != 1 {
		t.Fatalf("body has %d entries, want 1", len(entries))
	}
	entry := entries[0]

	msg, _ := entry["message"].(string)
	if msg == "" {
		t.Errorf("message is empty; want non-empty")
	}

	ddtags, _ := entry["ddtags"].(string)
	if !strings.Contains(ddtags, "severity:high") {
		t.Errorf("ddtags = %q, want it to contain severity:high", ddtags)
	}
	// The other derived tags must also be present (deterministic ordering).
	for _, want := range []string{"tenant:acme", "type:governance.policy.denied"} {
		if !strings.Contains(ddtags, want) {
			t.Errorf("ddtags = %q, want it to contain %q", ddtags, want)
		}
	}

	if entry["ddsource"] != "olivares" {
		t.Errorf("ddsource = %v, want olivares", entry["ddsource"])
	}
	if entry["service"] != defaultService {
		t.Errorf("service = %v, want %s", entry["service"], defaultService)
	}

	// Structural fields ride under the non-reserved "olivares" object, never as
	// reserved attributes; the severity label is mirrored there too.
	olv, ok := entry["olivares"].(map[string]any)
	if !ok {
		t.Fatalf("entry has no olivares object: %v", entry["olivares"])
	}
	if olv["agent"] != "claude-1" {
		t.Errorf("olivares.agent = %v, want claude-1", olv["agent"])
	}
	if olv["severity"] != "high" {
		t.Errorf("olivares.severity = %v, want high", olv["severity"])
	}

	// The API key must NEVER appear in the request body.
	if strings.Contains(string(cap.body), "sekret-key") {
		t.Errorf("request body leaked the API key")
	}
}

func TestHostnameReservedAttribute(t *testing.T) {
	// With a hostname, it is the reserved top-level attribute, not under olivares.
	var cap capture
	srv := newServer(t, http.StatusAccepted, "", &cap)
	defer srv.Close()
	o := New()
	open(t, o, map[string]string{"endpoint": srv.URL + "/api/v2/logs", "api_key": "k", "hostname": "h1"})
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	_ = json.Unmarshal(cap.body, &entries)
	if entries[0]["hostname"] != "h1" {
		t.Errorf("hostname = %v, want h1 (reserved attribute)", entries[0]["hostname"])
	}
	if olv, _ := entries[0]["olivares"].(map[string]any); olv["hostname"] != nil {
		t.Errorf("hostname must not be duplicated under olivares")
	}

	// Without a hostname, omitempty drops the key entirely.
	var cap2 capture
	srv2 := newServer(t, http.StatusAccepted, "", &cap2)
	defer srv2.Close()
	o2 := New()
	open(t, o2, map[string]string{"endpoint": srv2.URL + "/api/v2/logs", "api_key": "k"})
	defer o2.Close(context.Background())
	if err := o2.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	var entries2 []map[string]any
	_ = json.Unmarshal(cap2.body, &entries2)
	if _, ok := entries2[0]["hostname"]; ok {
		t.Errorf("hostname key must be omitted when unset, got %v", entries2[0]["hostname"])
	}
}

func TestNotifyServerErrorIsError(t *testing.T) {
	var cap capture
	// A 403 (e.g. bad API key) is a terminal client error: delivery surfaces it.
	srv := newServer(t, http.StatusForbidden, `{"errors":["Forbidden"]}`, &cap)
	defer srv.Close()

	o := New()
	open(t, o, map[string]string{
		"endpoint": srv.URL + "/api/v2/logs",
		"api_key":  "bad-key",
	})
	defer o.Close(context.Background())

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a 403 response must surface as an error")
	}
	// The error must not leak the API key.
	if strings.Contains(err.Error(), "bad-key") {
		t.Errorf("error leaked the API key: %v", err)
	}
}

func TestSiteDerivedEndpoint(t *testing.T) {
	for _, tc := range []struct {
		site string
		want string
	}{
		{"", "https://http-intake.logs.datadoghq.com/api/v2/logs"},
		{"datadoghq.eu", "https://http-intake.logs.datadoghq.eu/api/v2/logs"},
		{"us5.datadoghq.com", "https://http-intake.logs.us5.datadoghq.com/api/v2/logs"},
	} {
		o := New()
		settings := map[string]string{"api_key": "k"}
		if tc.site != "" {
			settings["site"] = tc.site
		}
		open(t, o, settings)
		if o.endpoint != tc.want {
			t.Errorf("site %q: endpoint = %q, want %q", tc.site, o.endpoint, tc.want)
		}
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{},                                   // missing api_key
		{"api_key": "k", "site": "evil.com"}, // unknown site (and no endpoint override)
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func TestMessageFallbacks(t *testing.T) {
	for _, tc := range []struct {
		n    sdk.Notification
		want string
	}{
		{sdk.Notification{Title: "t", Body: "b"}, "t — b"},
		{sdk.Notification{Title: "t"}, "t"},
		{sdk.Notification{Body: "b"}, "b"},
		{sdk.Notification{}, "olivares notification"},
	} {
		if got := message(tc.n); got != tc.want {
			t.Errorf("message(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
