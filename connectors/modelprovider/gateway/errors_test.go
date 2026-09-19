// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestMapHTTPStatusProductCodes(t *testing.T) {
	cases := []struct {
		in   int
		code string
	}{
		{http.StatusBadRequest, CodeBadRequest},
		{http.StatusUnauthorized, CodeUnauthenticated},
		{http.StatusForbidden, CodeForbidden},
		{http.StatusNotFound, CodeNotFound},
		{http.StatusPaymentRequired, CodeBudgetDenied},
		{http.StatusTooManyRequests, CodeRateLimited},
		{http.StatusNotImplemented, CodeNotImplemented},
		{http.StatusInternalServerError, CodeInternal},
		{http.StatusBadGateway, CodeUnavailable},
		{http.StatusServiceUnavailable, CodeUnavailable},
		{529, CodeUnavailable},
	}
	for _, tc := range cases {
		code, status, _ := MapHTTPStatus(tc.in)
		if code != tc.code {
			t.Errorf("status %d: code = %s, want %s", tc.in, code, tc.code)
		}
		if status != tc.in {
			t.Errorf("status %d: mapped HTTPStatus = %d", tc.in, status)
		}
	}
}

func TestEnvelopeOf(t *testing.T) {
	err := &Error{Code: CodeRateLimited, HTTPStatus: 429, Message: "rate limited"}
	status, env := EnvelopeOf(err)
	if status != 429 || env.Error.Code != CodeRateLimited || env.Error.Message != "rate limited" {
		t.Fatalf("envelope = %d %+v", status, env.Error)
	}
	status, env = EnvelopeOf(context.Canceled)
	if status != 499 || env.Error.Code != CodeCanceled {
		t.Fatalf("canceled envelope = %d %s", status, env.Error.Code)
	}
}

func TestMapTransportErrorDoesNotLeakCredential(t *testing.T) {
	api := &modelprovider.APIError{Method: "POST", Path: "/v1/chat/completions", Status: 401, Body: "bad key sk-secret"}
	err := mapTransportError(api)
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("mapped error leaked body/credential: %s", err.Error())
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != CodeUnauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestErrorUnwrapCanceled(t *testing.T) {
	err := mapTransportError(context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Unwrap lost context.Canceled: %v", err)
	}
}

func TestMatrixHonestyLabels(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Matrix() {
		switch c.Status {
		case StatusImplemented, StatusTested, StatusDesignOnly:
		default:
			t.Errorf("cell %+v: unknown status %q", c, c.Status)
		}
		key := string(c.Driver) + "/" + string(c.Protocol) + "/" + string(c.Transport)
		if seen[key] {
			t.Errorf("duplicate cell %s", key)
		}
		seen[key] = true
		if strings.Contains(strings.ToLower(c.Notes), "µs") || strings.Contains(strings.ToLower(c.Notes), "rps") {
			t.Errorf("cell %s copies a vendor benchmark: %s", key, c.Notes)
		}
	}
	for _, want := range []string{
		"openai-compat/openai-compatible/http",
		"openai-compat/openai-compatible/sse",
		"anthropic-messages/anthropic-messages/http",
		"anthropic-messages/anthropic-messages/sse",
		"ollama/ollama/http",
	} {
		if !seen[want] {
			t.Errorf("missing required cell %s", want)
		}
	}
}

func TestNewDriverRequiresBaseURL(t *testing.T) {
	if _, err := NewOpenAICompat(Config{}); err == nil {
		t.Fatal("expected error")
	}
}
