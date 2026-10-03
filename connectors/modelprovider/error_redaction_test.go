// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The upstream deliberately reflects synthetic secrets. These checks exercise
// the real HTTP exchange and the typed fields that callers can persist.
func TestProviderErrorsRedactReflectedCredentials(t *testing.T) {
	const credential = "q7-canary-opaque"
	methods := []struct {
		name string
		call func(*Client, *InferenceClient) error
	}{
		{"metadata JSON", func(c *Client, _ *InferenceClient) error { return c.GetJSON(context.Background(), "/fail", nil, nil) }},
		{"metadata text", func(c *Client, _ *InferenceClient) error {
			_, err := c.GetText(context.Background(), "/fail", nil)
			return err
		}},
		{"inference JSON", func(_ *Client, c *InferenceClient) error {
			return c.PostJSON(context.Background(), "/fail", map[string]string{"prompt": "synthetic-private-prompt"}, nil, nil)
		}},
		{"inference raw", func(_ *Client, c *InferenceClient) error {
			_, err := c.PostJSONRaw(context.Background(), "/fail", nil, nil)
			return err
		}},
		{"inference stream", func(_ *Client, c *InferenceClient) error {
			_, err := c.PostStream(context.Background(), "/fail", nil, nil)
			return err
		}},
		{"inference GET", func(_ *Client, c *InferenceClient) error {
			return c.GetJSON(context.Background(), "/fail", nil, nil, nil)
		}},
		{"inference DELETE", func(_ *Client, c *InferenceClient) error {
			return c.DeleteJSON(context.Background(), "/fail", nil, nil, nil)
		}},
		{"results download", func(_ *Client, c *InferenceClient) error {
			_, err := c.GetBytes(context.Background(), "/fail", nil)
			return err
		}},
		{"multipart", func(_ *Client, c *InferenceClient) error {
			return c.PostMultipart(context.Background(), "/fail", nil, "file", "fixture.txt", []byte("synthetic-private-file"), nil, nil)
		}},
	}
	cases := []struct{ name, body, forbidden string }{
		{"opaque reflection", "rejected " + credential, credential},
		{"short auth header", "Authorization: SSWS k7", "k7"},
		{"known header", `{"X-OpenIDM-Password":"unknown-header-canary"}`, "unknown-header-canary"},
		{"escaped reflection", `{"error":"rejected \u0071\u0037-canary-opaque"}`, credential},
		{"token shape", "rejected sk-abcdefghijklmnopqrstuvwxyz012345", "sk-abcdefghijklmnopqrstuvwxyz012345"},
		{"request reflection", `{"request":{"body":"synthetic-private-prompt","headers":{"X-API-Key":"unknown-canary"}}}`, "synthetic-private-prompt"},
		{"credential across cap", strings.Repeat(".", maxErrBody-3) + credential + strings.Repeat(".", maxInferenceErrBody), "q7-"},
		{"safe reason", "quota exceeded", ""},
	}
	for _, method := range methods {
		for _, tc := range cases {
			t.Run(method.name+"/"+tc.name, func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+credential {
						t.Error("credential was not sent to the provider")
					}
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer s.Close()
				err := method.call(NewClient(s.URL, s.Client(), AuthBearer, credential, nil), NewInferenceClient(s.URL, s.Client(), AuthBearer, credential, nil))
				if err == nil {
					t.Fatal("missing provider rejection")
				}
				if !strings.Contains(err.Error(), "status 403") {
					t.Fatal("HTTP status lost")
				}
				var api *APIError
				if errors.As(err, &api) {
					if api.Status != http.StatusForbidden || api.Body == "" || len(api.Body) > maxInferenceErrBody {
						t.Fatal("invalid bounded typed rejection")
					}
					if tc.forbidden != "" && strings.Contains(api.Body, tc.forbidden) {
						t.Fatal("typed body retained synthetic secret")
					}
					if tc.forbidden == "" && api.Body != tc.body {
						t.Fatal("safe provider reason changed")
					}
				}
				if tc.forbidden != "" && diagnosticContains(err.Error(), tc.forbidden) {
					t.Fatal("error retained synthetic secret")
				}
			})
		}
	}
}

// APIError.Body is JSON relayed to API callers, who decode escape sequences.
// Inspect the decoded representation as well as the literal wire bytes.
func diagnosticContains(detail, canary string) bool {
	if strings.Contains(detail, canary) {
		return true
	}
	if start := strings.IndexByte(detail, '{'); start >= 0 {
		var value any
		if json.Unmarshal([]byte(detail[start:]), &value) == nil {
			decoded, _ := json.Marshal(value)
			return strings.Contains(string(decoded), canary)
		}
	}
	return false
}

func TestProviderErrorsRedactAllAuthSchemes(t *testing.T) {
	for _, scheme := range []AuthScheme{AuthBearer, AuthAnthropicKey, AuthGoogleKey, AuthFalKey} {
		t.Run(string(scheme), func(t *testing.T) {
			const credential = "canary-id:canary-secret"
			d := &stubDoer{status: http.StatusUnauthorized, body: "rejected " + credential}
			err := NewClient("https://fixture.invalid", d, scheme, credential, nil).GetJSON(context.Background(), "/fail", nil, nil)
			var api *APIError
			if !errors.As(err, &api) || api.Status != http.StatusUnauthorized {
				t.Fatal("typed HTTP status lost")
			}
			if strings.Contains(api.Body, "canary-secret") || strings.Contains(err.Error(), "canary-secret") {
				t.Fatal("credential escaped through provider rejection")
			}
		})
	}
}

func TestInferenceGetBytesRedactsSignedQueryCredentials(t *testing.T) {
	const headerCredential = "header-fixture-opaque"
	const queryCredential = "query-fixture/+?=&opaque"
	for _, parameter := range []string{"sig", "signature", "download_grant", "X-Amz-Signature"} {
		for _, representation := range []string{"decoded", "transmitted", "transmitted-lowercase"} {
			name := parameter + "/" + representation
			t.Run(name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Header.Get("Authorization") != "Bearer "+headerCredential || r.URL.Query().Get(parameter) != queryCredential {
						t.Error("signed download did not send its independent credentials")
					}
					reflection := queryCredential
					if representation != "decoded" {
						_, reflection, _ = strings.Cut(r.URL.RawQuery, "=")
					}
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "denied " + reflection + " " + headerCredential})
				}))
				defer server.Close()
				client := NewInferenceClient(server.URL, server.Client(), AuthBearer, headerCredential, nil)
				query := url.Values{parameter: []string{queryCredential}}.Encode()
				if representation == "transmitted-lowercase" {
					query = strings.ReplaceAll(query, "%2B", "%2b")
				}
				_, rawCredential, _ := strings.Cut(query, "=")
				resultsURL := server.URL + "/results?" + query
				body, err := client.GetBytes(context.Background(), resultsURL, nil)
				if err == nil || body != nil || calls != 1 || !strings.Contains(err.Error(), "status 403") {
					t.Fatal("signed-results rejection lost its request, status or error")
				}
				for _, value := range []string{headerCredential, queryCredential, rawCredential, url.QueryEscape(queryCredential), url.PathEscape(queryCredential)} {
					if diagnosticContains(err.Error(), value) {
						t.Fatal("signed-results diagnostic retained a synthetic credential")
					}
				}
			})
		}
	}
}
