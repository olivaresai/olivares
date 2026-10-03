// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siem

import (
	"context"
	"github.com/olivaresai/olivares/sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplunkPreservesRejectionDuringRedaction(t *testing.T) {
	for _, tc := range []struct{ token, body string }{
		{"fixture-token", `{"code":7,"text":"` + strings.Repeat("<>&", 350) + `"}`},
		{"fixture-token", `{"code":7,"text":"` + strings.Repeat("\u2028", 350) + `"}`},
		{"7", `{"code":7,"text":"invalid"}`},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
		o := New()
		o.doer = server.Client()
		if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"destination": "splunk", "endpoint": server.URL, "token": tc.token, "max_attempts": "1"}}); err != nil {
			t.Fatal("fixture configuration failed")
		}
		err := o.Notify(context.Background(), sampleNotification())
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "code 7") {
			t.Fatal("complete HEC rejection became an acceptance or lost its exact code")
		}
	}
}

func TestSplunkCannotConfirmOmittedDiagnostic(t *testing.T) {
	// The complete response fits on the wire, but its oversized status remains
	// too large even after verbose diagnostic fields are compacted.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"` + strings.Repeat("\u2028", 600) + `"}`))
	}))
	defer server.Close()
	o := New()
	o.doer = server.Client()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"destination": "splunk", "endpoint": server.URL, "token": "fixture-token", "max_attempts": "1"}}); err != nil {
		t.Fatal("fixture configuration failed")
	}
	err := o.Notify(context.Background(), sampleNotification())
	if err == nil || sdk.ReportFor(err).Outcome != sdk.OutcomeIndeterminate {
		t.Fatal("omitted HEC diagnostic falsely confirmed delivery")
	}
}
