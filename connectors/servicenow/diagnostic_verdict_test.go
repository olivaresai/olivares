// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import (
	"context"
	"github.com/olivaresai/olivares/sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceNowCannotConfirmOmittedDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"failure","error":{"message":"denied","type":"` + strings.Repeat("\u2028", 500) + `"}}`))
	}))
	defer server.Close()
	o := New()
	o.doer = server.Client()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgInstanceURL: server.URL, cfgUsername: testUser, cfgPassword: testPass}}); err != nil {
		t.Fatal("fixture configuration failed")
	}
	err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"})
	if err == nil || sdk.ReportFor(err).Outcome != sdk.OutcomeIndeterminate {
		t.Fatal("omitted ServiceNow rejection falsely confirmed delivery")
	}
}

func TestServiceNowPreservesStatusWithCredentialCollision(t *testing.T) {
	for _, prefix := range []string{"", "\xef\xbb\xbf"} {
		for _, bearer := range []bool{false, true} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(prefix + `{"status":"failure"}`)) }))
			o := New()
			o.doer = server.Client()
			settings := map[string]string{cfgInstanceURL: server.URL, cfgUsername: "fixture-user", cfgPassword: "failure"}
			if bearer {
				delete(settings, cfgUsername)
				delete(settings, cfgPassword)
				settings[cfgAuthMode] = authBearer
				settings[cfgToken] = "failure"
			}
			if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
				server.Close()
				t.Fatal("fixture configuration failed")
			}
			err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"})
			server.Close()
			if err == nil || strings.Contains(err.Error(), "failure") {
				t.Fatal("sanitizing the public status erased rejection or retained a credential")
			}
		}
	}
}
