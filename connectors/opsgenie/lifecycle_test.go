// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opsgenie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// parseReqURL splits a recorded request URL into its path and identifierType
// query, so a test can assert the lifecycle endpoint shape precisely.
func parseReqURL(t *testing.T, raw string) (path, idType string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("request URL not parseable: %v", err)
	}
	return u.Path, u.Query().Get("identifierType")
}

func TestNotify_CloseByAlias(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: `{"result":"Request will be processed","requestId":"r-1"}`}}}
	o := openOutput(t, doer, nil)

	n := sdk.Notification{
		Title:    "Kill-switch reviewed",
		Body:     "Estate kill-switch reviewed and cleared.",
		Severity: model.SeverityInfo,
		Fields: map[string]string{
			fieldAction: actionClose,
			fieldAlias:  "olivares-gov-killswitch-stop42",
		},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("close by alias: %v", err)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1", doer.calls)
	}
	req := doer.reqs[0]
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	path, idType := parseReqURL(t, req.URL.String())
	if path != "/v2/alerts/olivares-gov-killswitch-stop42/close" {
		t.Fatalf("path = %q", path)
	}
	if idType != idTypeAlias {
		t.Fatalf("identifierType = %q, want alias", idType)
	}
	if got := req.Header.Get("Authorization"); got != "GenieKey "+testKey {
		t.Fatalf("Authorization = %q", got)
	}
	var b lifecycleBody
	if err := json.Unmarshal([]byte(doer.bodies[0]), &b); err != nil {
		t.Fatalf("body not valid JSON: %v\n%s", err, doer.bodies[0])
	}
	if b.Source != alertSource {
		t.Fatalf("source = %q, want %q", b.Source, alertSource)
	}
	if b.Note != "Estate kill-switch reviewed and cleared." {
		t.Fatalf("note = %q", b.Note)
	}
}

func TestNotify_CloseByAlertIDPreferred(t *testing.T) {
	// When both alert_id and alias are present, the explicit id wins (identifierType=id).
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	n := sdk.Notification{
		Title: "x",
		Fields: map[string]string{
			fieldAction:  actionClose,
			fieldAlertID: "0e0a-uuid-99",
			fieldAlias:   "ignored-when-id-present",
		},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("close by id: %v", err)
	}
	path, idType := parseReqURL(t, doer.reqs[0].URL.String())
	if path != "/v2/alerts/0e0a-uuid-99/close" {
		t.Fatalf("path = %q", path)
	}
	if idType != idTypeID {
		t.Fatalf("identifierType = %q, want id", idType)
	}
}

func TestNotify_AcknowledgeByAlias(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	n := sdk.Notification{
		Fields: map[string]string{fieldAction: "Acknowledge", fieldAlias: "appr-7"}, // case-insensitive
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	path, idType := parseReqURL(t, doer.reqs[0].URL.String())
	if path != "/v2/alerts/appr-7/acknowledge" {
		t.Fatalf("path = %q", path)
	}
	if idType != idTypeAlias {
		t.Fatalf("identifierType = %q, want alias", idType)
	}
}

func TestNotify_EURegionLifecycleHost(t *testing.T) {
	// A lifecycle action inherits the region-resolved host (EU here).
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, map[string]string{"alerts_url": "", "region": "eu"})
	if err := o.Notify(context.Background(), sdk.Notification{
		Fields: map[string]string{fieldAction: actionClose, fieldAlias: "eu-1"},
	}); err != nil {
		t.Fatalf("eu close: %v", err)
	}
	got := doer.reqs[0].URL.String()
	if !strings.HasPrefix(got, euAlertsURL+"/eu-1/close") {
		t.Fatalf("EU lifecycle URL = %q, want prefix %q", got, euAlertsURL+"/eu-1/close")
	}
}

func TestNotify_LifecycleWithoutIdentifierIsTerminal(t *testing.T) {
	// Closing "some alert" is never guessed: no alias and no alert_id is a terminal
	// configuration error, and the engine/transport is never reached (deny-closed).
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{
		Fields: map[string]string{fieldAction: actionClose},
	})
	if err == nil {
		t.Fatal("close without alias/alert_id must error")
	}
	if doer.calls != 0 {
		t.Fatalf("calls = %d, want 0 (no HTTP call without an identifier)", doer.calls)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error must never echo the api key: %v", err)
	}
}

func TestNotify_Lifecycle202IsSuccess(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: `{"result":"Request will be processed"}`}}}
	o := openOutput(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{
		Fields: map[string]string{fieldAction: actionClose, fieldAlias: "a1"},
	}); err != nil {
		t.Fatalf("202 should be success, got %v", err)
	}
}

func TestNotify_Lifecycle404TerminalNoRetry(t *testing.T) {
	// A 404 (no open alert with that alias) is terminal: not retried, surfaced as an
	// error, and the api key never appears in the message.
	doer := &recordingDoer{responses: []stubResp{
		{status: 404, body: `{"message":"Alert not found","took":0.0}`},
		{status: 202}, // would succeed if (incorrectly) retried
	}}
	o := openOutput(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{
		Fields: map[string]string{fieldAction: actionClose, fieldAlias: "missing"},
	})
	if err == nil {
		t.Fatal("404 must produce an error")
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on terminal 404)", doer.calls)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should carry the status: %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error must never contain the api key: %v", err)
	}
}

func TestNotify_LifecycleNoSecretLeak(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{
		Body:   "closing note, no credential here",
		Fields: map[string]string{fieldAction: actionClose, fieldAlias: "a1", fieldUser: "alice@corp"},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if strings.Contains(doer.bodies[0], testKey) {
		t.Fatalf("api key leaked into request body: %s", doer.bodies[0])
	}
	headers := doer.allHeaders.String()
	if strings.Count(headers, testKey) != 1 {
		t.Fatalf("api key should appear exactly once in headers, got:\n%s", headers)
	}
	if !strings.Contains(headers, "Authorization:GenieKey "+testKey) {
		t.Fatalf("api key not on the Authorization header:\n%s", headers)
	}
}

// TestNotify_CreateStillDefault proves the lifecycle dispatch does not disturb the
// default create path (no action field, or an unknown action, opens an alert).
func TestNotify_CreateStillDefault(t *testing.T) {
	for _, action := range []string{"", "create", "bogus"} {
		doer := &recordingDoer{responses: []stubResp{{status: 202}}}
		o := openOutput(t, doer, nil)
		fields := map[string]string{"alias": "x"}
		if action != "" {
			fields[fieldAction] = action
		}
		if err := o.Notify(context.Background(), sdk.Notification{Title: "open me", Severity: model.SeverityHigh, Fields: fields}); err != nil {
			t.Fatalf("action %q: %v", action, err)
		}
		path, _ := parseReqURL(t, doer.reqs[0].URL.String())
		if path != "/v2/alerts" {
			t.Fatalf("action %q: create path = %q, want /v2/alerts", action, path)
		}
	}
}
