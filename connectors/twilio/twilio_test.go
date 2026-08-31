// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package twilio

import (
	"context"
	"io"
	"net/http"
	"net/url"
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
	url    string
	header http.Header
	body   []byte
}

func (d *captureDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	d.reqs = append(d.reqs, capturedReq{url: req.URL.String(), header: req.Header.Clone(), body: body})
	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: make(http.Header)}, nil
}

const (
	testSID   = "AC0000000000000000000000000000000"
	testToken = "twilio-auth-token-secret"
)

func openTwilio(t *testing.T, doer *captureDoer, extra map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := map[string]string{
		cfgAccountSID: testSID, cfgAuthToken: testToken,
		cfgFrom: "+15017122661", cfgTo: "+15558675310", cfgAPIBase: "https://api.twilio.test",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestSendSMS(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{"sid":"SM1","status":"queued"}`}}}
	o := openTwilio(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{Title: "SEV1", Body: "prod down"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.reqs[0]
	if req.url != "https://api.twilio.test/2010-04-01/Accounts/"+testSID+"/Messages.json" {
		t.Fatalf("url = %s", req.url)
	}
	if got := req.header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type = %q", got)
	}
	if !strings.HasPrefix(req.header.Get("Authorization"), "Basic ") {
		t.Fatalf("missing Basic auth")
	}
	form, _ := url.ParseQuery(string(req.body))
	if form.Get("To") != "+15558675310" || form.Get("From") != "+15017122661" {
		t.Fatalf("form To/From = %v", form)
	}
	if form.Get("Body") != "SEV1\nprod down" {
		t.Fatalf("body = %q", form.Get("Body"))
	}
}

func TestMessagingServiceSidMutuallyExclusiveWithFrom(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 201, body: `{}`}}}
	o := New()
	o.doer = doer
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgAccountSID: testSID, cfgAuthToken: testToken, cfgMsgService: "MG1", cfgTo: "+1555", cfgAPIBase: "https://api.twilio.test",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	form, _ := url.ParseQuery(string(doer.reqs[0].body))
	if form.Get("MessagingServiceSid") != "MG1" || form.Has("From") {
		t.Fatalf("expected MessagingServiceSid, no From: %v", form)
	}
}

func TestOpenValidation(t *testing.T) {
	// Missing token.
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{cfgAccountSID: testSID}}); err == nil {
		t.Fatal("expected error for missing auth_token")
	}
	// Missing both from and messaging service.
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{cfgAccountSID: testSID, cfgAuthToken: testToken}}); err == nil {
		t.Fatal("expected error for missing sender")
	}
}

func TestNoSecretLeak(t *testing.T) {
	doer := &captureDoer{responses: []stubResp{{status: 401, body: `{"code":20003,"message":"Authenticate"}`}}}
	o := openTwilio(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("SECURITY: auth token leaked into error: %v", err)
	}
}
