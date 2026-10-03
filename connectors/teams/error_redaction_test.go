// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/olivaresai/olivares/sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifyRedactsReflectedWebhookCredentials(t *testing.T) {
	for _, tc := range []struct{ name, decoded, transmitted string }{
		{"literal", "opaque-webhook-fixture", "opaque-webhook-fixture"},
		{"encoded", "opaque+fixture", "opaque%2bfixture"},
	} {
		for _, query := range []bool{false, true} {
			for _, raw := range []bool{false, true} {
				name := tc.name + "/path/decoded"
				if query {
					name = tc.name + "/query/decoded"
				}
				if raw {
					name = strings.TrimSuffix(name, "decoded") + "raw"
				}
				t.Run(name, func(t *testing.T) {
					received := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received++
						value := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
						if raw {
							value = r.URL.EscapedPath()[strings.LastIndex(r.URL.EscapedPath(), "/")+1:]
						}
						if query {
							value = r.URL.Query().Get("sig")
							if raw {
								_, value, _ = strings.Cut(r.URL.RawQuery, "sig=")
							}
						}
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(map[string]string{"message": "denied " + value})
					}))
					endpoint := server.URL + "/hooks/" + tc.transmitted
					if query {
						endpoint = server.URL + "/hook?sig=" + tc.transmitted
					}
					o := New()
					o.doer = server.Client()
					if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"webhook_url": endpoint, "max_attempts": "1"}}); err != nil {
						server.Close()
						t.Fatal("fixture configuration failed")
					}
					err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"})
					server.Close()
					if received != 1 || err == nil || strings.Contains(err.Error(), tc.decoded) || strings.Contains(err.Error(), tc.transmitted) || strings.Contains(err.Error(), endpoint) || !strings.Contains(err.Error(), "400") {
						t.Fatal("webhook error lost the rejection status or disclosed a URL credential")
					}
				})
			}
		}
	}
}

func TestNotifyOmitsWrappedStructuredDiagnostic(t *testing.T) {
	const credential = "opaque-wrapped-fixture"
	encoded := ""
	for _, r := range credential {
		encoded += fmt.Sprintf("\\u%04x", r)
	}
	for _, wrapper := range []string{"bom", "html", "prefix"} {
		t.Run(wrapper, func(t *testing.T) {
			diagnostic := `{"message":"denied ` + encoded + `"}`
			switch wrapper {
			case "bom":
				diagnostic = "\ufeff" + diagnostic
			case "html":
				diagnostic = "<html><body>" + diagnostic + "</body></html>"
			case "prefix":
				diagnostic = "provider said: " + diagnostic
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(diagnostic))
			}))
			defer server.Close()
			o := New()
			o.doer = server.Client()
			if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"webhook_url": server.URL + "/hooks/" + credential, "max_attempts": "1"}}); err != nil {
				t.Fatal("fixture configuration failed")
			}
			err := o.Notify(context.Background(), sdk.Notification{Title: "fixture"})
			if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), encoded) {
				t.Fatal("wrapped provider diagnostic disclosed a recoverable URL credential")
			}
		})
	}
}
