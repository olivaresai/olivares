// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// actuatorDoer captures the actuation request and returns a scripted response.
type actuatorDoer struct {
	status int
	body   string
	header http.Header

	calls     int
	gotMethod string
	gotPath   string
	gotHeader http.Header
	gotBody   []byte
}

func (d *actuatorDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.gotMethod = req.Method
	d.gotPath = req.URL.Path
	d.gotHeader = req.Header.Clone()
	if req.Body != nil {
		d.gotBody, _ = io.ReadAll(req.Body)
	}
	h := d.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader(d.body)), Header: h}, nil
}

const testAdminKey = "sk-ant-admin01-test-credential"

func testActuator(doer *actuatorDoer) *Actuator {
	a := NewActuator("https://api.test", testAdminKey, doer)
	a.now = func() time.Time { return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC) }
	return a
}

func keyRequest() identitysource.ActuationRequest {
	return identitysource.ActuationRequest{Ref: "apikey_123", Kind: kindAPIKey}
}

// keyBody renders the Admin API update response (the APIKey object) with status.
func keyBody(status string) string {
	return `{"id":"apikey_123","type":"api_key","name":"ci-key","workspace_id":"wrkspc_1","status":"` + status + `","partial_key_hint":"sk-ant-api03-R2D...igAA","created_at":"2026-01-01T00:00:00Z"}`
}

func TestActuatorSetsStatus(t *testing.T) {
	cases := []struct {
		name string
		call func(*Actuator, context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error)
		op   identitysource.LifecycleOp
		want string
	}{
		{"disable", (*Actuator).Disable, identitysource.OpDisable, keyStatusInactive},
		{"restore", (*Actuator).Restore, identitysource.OpRestore, keyStatusActive},
		{"finalize", (*Actuator).Finalize, identitysource.OpFinalize, keyStatusArchived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &actuatorDoer{status: 200, body: keyBody(tc.want)}
			a := testActuator(doer)

			rcpt, err := tc.call(a, context.Background(), keyRequest())
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			// One POST to the verified update endpoint with the Admin API headers.
			if doer.calls != 1 || doer.gotMethod != http.MethodPost {
				t.Errorf("calls = %d, method = %q", doer.calls, doer.gotMethod)
			}
			if doer.gotPath != "/v1/organizations/api_keys/apikey_123" {
				t.Errorf("path = %q", doer.gotPath)
			}
			if got := doer.gotHeader.Get("x-api-key"); got != testAdminKey {
				t.Errorf("x-api-key = %q", got)
			}
			if got := doer.gotHeader.Get("anthropic-version"); got != defaultAnthropicVersion {
				t.Errorf("anthropic-version = %q", got)
			}
			if got := doer.gotHeader.Get("content-type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("content-type = %q", got)
			}

			// The body is exactly {"status": want}.
			var body map[string]string
			if err := json.Unmarshal(doer.gotBody, &body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if len(body) != 1 || body["status"] != tc.want {
				t.Errorf("body = %s", doer.gotBody)
			}

			// The receipt carries the resulting status and Provider anthropic.
			if rcpt.Op != tc.op || rcpt.Ref != "apikey_123" {
				t.Errorf("receipt = %+v", rcpt)
			}
			if rcpt.Provider != identitysource.SourceAnthropic {
				t.Errorf("provider = %q", rcpt.Provider)
			}
			if !strings.Contains(rcpt.Detail, `"`+tc.want+`"`) {
				t.Errorf("Detail = %q (want resulting status %q)", rcpt.Detail, tc.want)
			}
			if !rcpt.OccurredAt.Equal(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)) {
				t.Errorf("OccurredAt = %v", rcpt.OccurredAt)
			}
		})
	}
}

func TestActuatorStatusMismatch(t *testing.T) {
	// Asked inactive, provider echoed active back: the actuation is NOT confirmed.
	doer := &actuatorDoer{status: 200, body: keyBody("active")}
	a := testActuator(doer)

	_, err := a.Disable(context.Background(), keyRequest())
	if err == nil {
		t.Fatal("expected error on status mismatch")
	}
	if !strings.Contains(err.Error(), `"active"`) || !strings.Contains(err.Error(), `"inactive"`) {
		t.Errorf("error = %q (want both statuses)", err)
	}
}

func TestActuatorRotateRetireUnsupported(t *testing.T) {
	doer := &actuatorDoer{status: 200, body: keyBody("active")}
	a := testActuator(doer)

	if _, err := a.Rotate(context.Background(), keyRequest()); !errors.Is(err, identitysource.ErrUnsupportedOperation) {
		t.Errorf("Rotate err = %v (want ErrUnsupportedOperation)", err)
	}
	if _, err := a.Retire(context.Background(), keyRequest()); !errors.Is(err, identitysource.ErrUnsupportedOperation) {
		t.Errorf("Retire err = %v (want ErrUnsupportedOperation)", err)
	}
	if doer.calls != 0 {
		t.Errorf("unsupported ops must not invoke the doer (calls = %d)", doer.calls)
	}

	// The capability matrix matches: disable/restore/finalize on api_key, nothing else.
	caps := a.Capabilities()
	if len(caps) != 3 {
		t.Fatalf("capabilities = %d, want 3", len(caps))
	}
	for _, op := range []identitysource.LifecycleOp{identitysource.OpDisable, identitysource.OpRestore, identitysource.OpFinalize} {
		if _, ok := identitysource.FindCapability(caps, op, kindAPIKey); !ok {
			t.Errorf("missing capability %s/%s", op, kindAPIKey)
		}
	}
	for _, op := range []identitysource.LifecycleOp{identitysource.OpRotate, identitysource.OpRetire} {
		if _, ok := identitysource.FindCapability(caps, op, kindAPIKey); ok {
			t.Errorf("capability %s must NOT be declared (Console-only in the Admin API)", op)
		}
	}
}

func TestActuatorUnconfigured(t *testing.T) {
	doer := &actuatorDoer{status: 200, body: keyBody("inactive")}
	a := NewActuator("https://api.test", "", doer)

	_, err := a.Disable(context.Background(), keyRequest())
	if err == nil {
		t.Fatal("expected error with no admin key")
	}
	if doer.calls != 0 {
		t.Errorf("unconfigured actuator must not invoke the doer (calls = %d)", doer.calls)
	}
}

func TestActuatorRejectsWrongKindAndEmptyRef(t *testing.T) {
	doer := &actuatorDoer{status: 200, body: keyBody("inactive")}
	a := testActuator(doer)

	bad := keyRequest()
	bad.Kind = kindServiceAccount
	if _, err := a.Disable(context.Background(), bad); !errors.Is(err, identitysource.ErrUnsupportedOperation) {
		t.Errorf("wrong kind err = %v (want ErrUnsupportedOperation)", err)
	}
	empty := keyRequest()
	empty.Ref = ""
	if _, err := a.Disable(context.Background(), empty); err == nil {
		t.Error("empty ref must error")
	}
	if doer.calls != 0 {
		t.Errorf("rejected requests must not invoke the doer (calls = %d)", doer.calls)
	}
}

func TestActuatorRejected(t *testing.T) {
	h := make(http.Header)
	h.Set("request-id", "req_abc")
	doer := &actuatorDoer{
		status: 404,
		body:   `{"type":"error","error":{"type":"not_found_error","message":"API key not found"},"request_id":"req_xyz"}`,
		header: h,
	}
	a := testActuator(doer)

	_, err := a.Finalize(context.Background(), keyRequest())
	if err == nil {
		t.Fatal("expected error for 404")
	}
	msg := err.Error()
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "not_found_error") {
		t.Errorf("error = %q (want status + error type)", msg)
	}
	// The body request_id wins over the header (the exchangeError pattern).
	if !strings.Contains(msg, "req_xyz") {
		t.Errorf("error = %q (want request id)", msg)
	}
	if strings.Contains(msg, testAdminKey) {
		t.Error("error must never contain the admin key")
	}
}
