// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
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
	testSite  = "https://acme.atlassian.net"
	testEmail = "bot@acme.io"
	testToken = "atlassian-api-token-secret"
)

func openIssue(t *testing.T, doer *captureDoer, extra map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := map[string]string{cfgBaseURL: testSite, cfgEmail: testEmail, cfgAPIToken: testToken, cfgProjectKey: "OPS"}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestCreateIssueWithADF(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"key":"OPS-1"}`}}}
	o := openIssue(t, doer, map[string]string{cfgIssueType: "Incident"})
	if err := o.Notify(context.Background(), sdk.Notification{Title: "Drift", Body: "1 unexpected access"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if req.method != "POST" || req.url != testSite+"/rest/api/3/issue" {
		t.Fatalf("request = %s %s", req.method, req.url)
	}
	// Basic auth = base64(email:token).
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testEmail+":"+testToken))
	if req.header.Get("Authorization") != want {
		t.Fatalf("auth header mismatch")
	}
	var ic struct {
		Fields struct {
			Project     map[string]string `json:"project"`
			Summary     string            `json:"summary"`
			IssueType   map[string]string `json:"issuetype"`
			Description json.RawMessage   `json:"description"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(req.body, &ic); err != nil {
		t.Fatalf("body: %v", err)
	}
	if ic.Fields.Project["key"] != "OPS" || ic.Fields.Summary != "Drift" || ic.Fields.IssueType["name"] != "Incident" {
		t.Fatalf("fields = %+v", ic.Fields)
	}
	// Description must be ADF (a JSON doc object), not a plain string.
	var adfDoc map[string]any
	if err := json.Unmarshal(ic.Fields.Description, &adfDoc); err != nil {
		t.Fatalf("description not a JSON object (ADF): %v", err)
	}
	if adfDoc["type"] != "doc" || adfDoc["version"] != float64(1) {
		t.Fatalf("description not ADF: %v", adfDoc)
	}
}

func TestTransition(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 204, body: ""}}}
	o := openIssue(t, doer, nil)
	n := sdk.Notification{Fields: map[string]string{fieldJiraAction: actionTransition, fieldIssueKey: "OPS-7", fieldTransitionID: "31"}}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if req.url != testSite+"/rest/api/3/issue/OPS-7/transitions" {
		t.Fatalf("transition url = %s", req.url)
	}
	var tb struct {
		Transition map[string]string `json:"transition"`
	}
	if err := json.Unmarshal(req.body, &tb); err != nil {
		t.Fatalf("body: %v", err)
	}
	if tb.Transition["id"] != "31" {
		t.Fatalf("transition id = %q", tb.Transition["id"])
	}
}

func TestTransitionRequiresKeyAndID(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 204, body: ""}}}
	o := openIssue(t, doer, nil)
	n := sdk.Notification{Fields: map[string]string{fieldJiraAction: actionTransition, fieldIssueKey: "OPS-7"}}
	if err := o.Notify(context.Background(), n); err == nil {
		t.Fatal("expected error: transition without transition_id")
	}
	if doer.calls != 0 {
		t.Fatal("must not call the API without the required fields")
	}
}

func TestJSMRequest(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"issueKey":"SD-1"}`}}}
	o := New()
	o.doer = doer
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgBaseURL: testSite, cfgEmail: testEmail, cfgAPIToken: testToken,
		cfgRecordType: recordJSM, cfgServiceDeskID: "10", cfgRequestTypeID: "25",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "help", Body: "details"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if req.url != testSite+"/rest/servicedeskapi/request" {
		t.Fatalf("jsm url = %s", req.url)
	}
	var jr struct {
		ServiceDeskID string            `json:"serviceDeskId"`
		RequestTypeID string            `json:"requestTypeId"`
		Values        map[string]string `json:"requestFieldValues"`
	}
	if err := json.Unmarshal(req.body, &jr); err != nil {
		t.Fatalf("body: %v", err)
	}
	if jr.ServiceDeskID != "10" || jr.RequestTypeID != "25" || jr.Values["summary"] != "help" {
		t.Fatalf("jsm request = %+v", jr)
	}
}

func TestBearerAuth(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{}`}}}
	o := New()
	o.doer = doer
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgBaseURL: testSite, cfgAuthMode: authBearer, cfgToken: "3lo-token", cfgProjectKey: "OPS",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.reqs[0].header.Get("Authorization"); got != "Bearer 3lo-token" {
		t.Fatalf("auth = %q", got)
	}
}

func TestNoSecretLeak(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 401, body: `{"errorMessages":["unauthorized"]}`}}}
	o := openIssue(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("SECURITY: API token leaked into error: %v", err)
	}
}
