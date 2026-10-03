// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pagerduty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestNotifyPreservesCompleteRejectionDuringRedaction(t *testing.T) {
	for _, action := range []string{"trigger", "change"} {
		for index, filler := range []string{"<>&", "\u2028"} {
			t.Run(action+[]string{"-html", "-unicode"}[index], func(t *testing.T) {
				body := `{"status":"invalid","message":"` + strings.Repeat(filler, 350) + `"}`
				received := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					received++
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(body))
				}))
				o := New()
				o.doer = server.Client()
				if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"routing_key": "fixture-routing-key", "events_url": server.URL, "change_events_url": server.URL, "max_attempts": "1"}}); err != nil {
					t.Fatal("fixture configuration failed")
				}
				n := sdk.Notification{Title: "fixture"}
				if action == "change" {
					n.Fields = map[string]string{fieldEventAction: actionChange}
				}
				err := o.Notify(context.Background(), n)
				server.Close()
				if received != 1 || err == nil || !strings.Contains(err.Error(), "invalid") {
					t.Fatal("complete provider rejection became an acceptance or lost its status")
				}
			})
		}
	}
}

func TestNotifyCannotConfirmOmittedDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"invalid","message":"` + strings.Repeat("x", 4096) + `"}`))
	}))
	defer server.Close()
	o := New()
	o.doer = server.Client()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"routing_key": "fixture-routing-key", "events_url": server.URL}}); err != nil {
		t.Fatal("fixture configuration failed")
	}
	err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"})
	if err == nil || sdk.ReportFor(err).Outcome != sdk.OutcomeIndeterminate {
		t.Fatal("omitted diagnostic falsely confirmed a delivery")
	}
}

func TestNotifyPreservesSuccessWithCredentialCollision(t *testing.T) {
	for _, prefix := range []string{"", "\xef\xbb\xbf"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(prefix + `{"status":"success"}`))
		}))
		defer server.Close()
		o := New()
		o.doer = server.Client()
		if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"events_url": server.URL, "routing_key": "success", "max_attempts": "1"}}); err != nil {
			t.Fatal("fixture configuration failed")
		}
		if err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"}); err != nil {
			t.Fatal("sanitizing the public status turned acceptance into rejection")
		}
	}
}
