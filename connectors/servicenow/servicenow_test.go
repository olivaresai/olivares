// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

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
	d.reqs = append(d.reqs, capturedReq{method: req.Method, url: req.URL.String(), header: req.Header.Clone(), body: body})
	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: make(http.Header)}, nil
}

const (
	testInstance = "https://acme.service-now.com"
	testUser     = "svc_integration"
	testPass     = "sup3r-s3cret-pw"
)

func openBasic(t *testing.T, doer *captureDoer, extra map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := map[string]string{
		cfgInstanceURL: testInstance, cfgUsername: testUser, cfgPassword: testPass,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestDescriptorAndOpen(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeOutput {
		t.Fatalf("descriptor = %+v", d)
	}
	// Missing instance fails fast.
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("expected error for missing instance_url")
	}
	// Basic without password fails fast.
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{cfgInstanceURL: testInstance, cfgUsername: "u"}}); err == nil {
		t.Fatal("expected error for missing password")
	}
	// Unknown record type fails fast.
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{cfgInstanceURL: testInstance, cfgUsername: "u", cfgPassword: "p", cfgRecordType: "bogus"}}); err == nil {
		t.Fatal("expected error for unknown record_type")
	}
}

func TestIncidentTableWrite(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"result":{"sys_id":"abc","number":"INC0010001"}}`}}}
	o := openBasic(t, doer, nil)

	n := sdk.Notification{
		Title: "Over-permissioned NHI", Body: "ci can write prod-secrets",
		Severity: model.SeverityHigh, Tenant: "acme",
		Fields: map[string]string{"category": "security"},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if req.method != "POST" || req.url != testInstance+"/api/now/table/incident" {
		t.Fatalf("request = %s %s", req.method, req.url)
	}
	// Basic auth header present, never the raw password in the URL.
	if !strings.HasPrefix(req.header.Get("Authorization"), "Basic ") {
		t.Fatalf("missing Basic auth header")
	}
	var rec map[string]string
	if err := json.Unmarshal(req.body, &rec); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if rec["short_description"] != n.Title || rec["description"] != n.Body {
		t.Fatalf("record = %v", rec)
	}
	if rec["urgency"] != "1" || rec["impact"] != "1" {
		t.Fatalf("high severity should map urgency/impact 1/1: %v", rec)
	}
	if rec["category"] != "security" {
		t.Fatalf("field not folded into columns: %v", rec)
	}
}

func TestSIRAndTaskTables(t *testing.T) {
	for _, tc := range []struct {
		record string
		path   string
	}{
		{recordSIR, "/api/now/table/sn_si_incident"},
		{recordTask, "/api/now/table/task"},
	} {
		doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"result":{}}`}}}
		o := openBasic(t, doer, nil)
		n := sdk.Notification{Title: "x", Fields: map[string]string{fieldRecord: tc.record}}
		if err := o.Notify(context.Background(), n); err != nil {
			t.Fatalf("%s Notify: %v", tc.record, err)
		}
		if got := doer.reqs[0].url; got != testInstance+tc.path {
			t.Fatalf("%s wrote to %s, want %s", tc.record, got, tc.path)
		}
	}
}

func TestEmEvent(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200, body: `{"result":{}}`}}}
	o := openBasic(t, doer, map[string]string{cfgRecordType: recordEvent, cfgEventSource: "olivares"})
	n := sdk.Notification{
		Title: "MCP down", Severity: model.SeverityCritical,
		Fields: map[string]string{"node": "mcp-1", "resource": "memory", "dedup_key": "k-9", "extra": "z"},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.reqs[0].url; got != testInstance+"/api/global/em/jsonv2" {
		t.Fatalf("em_event url = %s", got)
	}
	var env struct {
		Records []map[string]string `json:"records"`
	}
	if err := json.Unmarshal(doer.reqs[0].body, &env); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(env.Records) != 1 {
		t.Fatalf("records = %d", len(env.Records))
	}
	ev := env.Records[0]
	if ev["source"] != "olivares" || ev["node"] != "mcp-1" || ev["resource"] != "memory" {
		t.Fatalf("event = %v", ev)
	}
	if ev["severity"] != "1" { // critical -> 1
		t.Fatalf("severity = %q, want 1 (critical)", ev["severity"])
	}
	if ev["message_key"] != "k-9" {
		t.Fatalf("message_key = %q", ev["message_key"])
	}
	// additional_info is a JSON STRING, not a nested object.
	if !strings.Contains(ev["additional_info"], `"extra":"z"`) {
		t.Fatalf("additional_info = %q", ev["additional_info"])
	}
}

func TestImportSet(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"result":[{"status":"inserted"}]}`}}}
	o := openBasic(t, doer, map[string]string{cfgRecordType: recordImport, cfgStagingTable: "u_imp_incident"})
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.reqs[0].url; got != testInstance+"/api/now/import/u_imp_incident" {
		t.Fatalf("import url = %s", got)
	}
}

func TestBearerAuth(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{}`}}}
	o := New()
	o.doer = doer
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgInstanceURL: testInstance, cfgAuthMode: authBearer, cfgToken: "tok-123",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.reqs[0].header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestLogicalErrorOn2xx(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 200, body: `{"error":{"message":"ACL exception","detail":"no write"},"status":"failure"}`}}}
	o := openBasic(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "ACL exception") {
		t.Fatalf("expected logical error surfaced, got %v", err)
	}
}

func TestNoSecretLeak(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 401, body: "Unauthorized"}}}
	o := openBasic(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if strings.Contains(err.Error(), testPass) {
		t.Fatalf("SECURITY: password leaked into error: %v", err)
	}
	// Descriptor defaults never embed a secret.
	for _, f := range New().Descriptor().ConfigFields {
		if f.Secret && f.Default != "" {
			t.Errorf("secret field %q has a non-empty default", f.Key)
		}
	}
}
