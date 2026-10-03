// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPErrorDiagnosticSafety(t *testing.T) {
	const canary = "opaque-known-canary"
	req, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid/", nil)
	req.Header.Set("X-API-Key", canary)
	tests := []struct{ name, raw, forbidden string }{
		{"opaque", "rejected " + canary, canary},
		{"escaped", `{"code":7,"text":"rejected \u006fpaque-known-canary"}`, canary},
		{"header", `{"Authorization":"SSWS k7"}`, "k7"},
		{"cookie", "Cookie: session=a; second=b", "second=b"},
		{"unknown password", `{"X-OpenIDM-Password":"unrecognized-credential"}`, "unrecognized-credential"},
		{"request envelope", `{"request":{"headers":{"X-Test":"private-header"},"body":"private-prompt"}}`, "private-prompt"},
		{"debug envelope", `{"debug":"curl --data private-prompt"}`, "private-prompt"},
		{"duplicate field", `{"text":"opaque-known-canary","text":"quota exceeded"}`, canary},
		{"escaped duplicate field", `{"text":"\u006fpaque-known-canary","text":"quota exceeded"}`, canary},
		{"broken JSON", `{"error":"\u006fpaque-known-canary`, "canary"},
		{"trailing JSON", `{"code":7} {"error":"\u006fpaque-known-canary"}`, "canary"},
		{"PEM", "-----BEGIN " + "PRIVATE KEY-----\nsynthetic-key-material\n-----END PRIVATE KEY-----", "synthetic-key-material"},
		{"oversized", strings.Repeat(".", 126) + canary, "op"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := HTTPError([]byte(tc.raw), 128, req)
			if containsDiagnostic(out, tc.forbidden) || len(out) > 128 {
				t.Fatal("credential retained or diagnostic budget exceeded")
			}
			if json.Valid([]byte(tc.raw)) && !json.Valid([]byte(out)) {
				t.Fatal("complete structured diagnostic lost JSON encoding")
			}
		})
	}
	benign := ` {"code":7,"text":"quota exceeded","counter":9007199254740993} `
	var fields map[string]json.RawMessage
	out := HTTPError([]byte(benign), 128, req)
	if json.Unmarshal([]byte(out), &fields) != nil || string(fields["code"]) != "7" ||
		string(fields["text"]) != `"quota exceeded"` || string(fields["counter"]) != "9007199254740993" {
		t.Fatal("safe reason or exact numeric protocol code changed")
	}
	req.SetBasicAuth("b7", "p9")
	if out := HTTPError([]byte("rejected b7 and p9"), 128, req); strings.Contains(out, "b7") || strings.Contains(out, "p9") {
		t.Fatal("short Basic credentials survived")
	}
}

// Walk every JSON string, including repeated fields and escaped values, so an
// unsafe raw duplicate cannot disappear inside the test's own decoded map.
func containsDiagnostic(out, forbidden string) bool {
	if strings.Contains(out, forbidden) {
		return true
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if text, ok := token.(string); ok && strings.Contains(text, forbidden) {
			return true
		}
	}
}

func TestHTTPErrorPreservesProtocolFieldNamesWithShortCredential(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid/", nil)
	req.Header.Set("X-API-Key", "de")
	out := HTTPError([]byte(`{"code":7,"text":"quota exceeded"}`), 128, req)
	var verdict struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(out), &verdict) != nil || verdict.Code != 7 {
		t.Fatal("coincidental short credential erased a protocol field name")
	}
}

type interruptedDiagnostic struct{}

func (interruptedDiagnostic) Read(p []byte) (int, error) {
	return copy(p, "opaque-"), io.ErrUnexpectedEOF
}

func TestHTTPErrorReadDoesNotExposePartialCredential(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid/", nil)
	req.Header.Set("X-API-Key", "opaque-known-canary")
	out := ReadHTTPError(interruptedDiagnostic{}, 128, req)
	if out != OmittedHTTPError || strings.Contains(out, "opaque-") {
		t.Fatal("failed read retained a partial credential")
	}
	reader := strings.NewReader(strings.Repeat("x", 1024))
	if out := ReadHTTPError(reader, 128, req); out != OmittedHTTPError || reader.Len() != 1024-129 {
		t.Fatal("diagnostic read exceeded its bound or disclosed an oversized prefix")
	}
}

func TestHTTPErrorPreservesProtocolVerdictAtBudget(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid/", nil)
	req.Header.Set("X-API-Key", "k7")
	raw := `{"code":7,"text":"` + strings.Repeat("x", 106) + `k7"}`
	if len(raw) != 128 {
		t.Fatal("fixture must exactly fill its budget")
	}
	out := HTTPError([]byte(raw), 128, req)
	var verdict struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(out), &verdict) != nil || verdict.Code != 7 || len(out) > 128 || strings.Contains(out, "k7") {
		t.Fatal("masking a short credential at the budget erased the protocol rejection")
	}
}

func TestHTTPErrorRedactsReflectedJSONScalars(t *testing.T) {
	for _, credential := range []string{"1234", "true"} {
		req, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid/", nil)
		req.Header.Set("X-API-Key", credential)
		out := HTTPError([]byte(`{"code":7,"reason":`+credential+`}`), 128, req)
		var verdict struct {
			Code int `json:"code"`
		}
		if strings.Contains(out, credential) || json.Unmarshal([]byte(out), &verdict) != nil || verdict.Code != 7 {
			t.Fatal("scalar credential survived or unrelated protocol status changed")
		}
	}
}

func TestHTTPErrorRedactsEncodedFormCredentials(t *testing.T) {
	for _, credential := range []string{"opaque+fixture", "opaque%fixture", "opaque fixture"} {
		for _, encoded := range []string{url.QueryEscape(credential), url.PathEscape(credential)} {
			raw, _ := json.Marshal(map[string]any{"code": 7, "error_description": encoded})
			out := HTTPError(raw, 2048, nil, credential)
			if containsDiagnostic(out, credential) || containsDiagnostic(out, encoded) {
				t.Fatal("transmitted form credential survived the diagnostic")
			}
		}
	}
}

func TestHTTPErrorPreservesNumericAndBooleanVerdicts(t *testing.T) {
	for _, tc := range []struct{ credential, raw, field, verdict string }{
		{"7", `{"code":7,"reason":7}`, "code", "7"},
		{"false", `{"ok":false,"reason":false}`, "ok", "false"},
	} {
		out := HTTPError([]byte(tc.raw), 128, nil, tc.credential)
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(out), &fields) != nil || string(fields[tc.field]) != tc.verdict || string(fields["reason"]) == tc.credential {
			t.Fatal("public protocol verdict was replaced or non-protocol reflection survived")
		}
	}
}
