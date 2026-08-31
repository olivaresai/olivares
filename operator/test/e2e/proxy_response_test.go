// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package e2e

import (
	"errors"
	"net/http"
	"testing"
)

func TestTransientPodProxyErrorDistinguishesAPIServerFromEngine(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("request returned a non-success status")
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{
			name: "pod_without_an_address_is_transient",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":400}`,
			want: true,
		},
		{
			name: "engine_not_leader_remains_an_upstream_response",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
		},
		{
			name: "arbitrary_engine_bad_request_remains_an_anomaly",
			code: http.StatusBadRequest,
			body: `{"error":{"code":"invalid_request","message":"bad request"}}`,
		},
		{
			name: "other_kubernetes_failures_are_not_silently_retryable",
			code: http.StatusForbidden,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"pods is forbidden","reason":"Forbidden","code":403}`,
		},
		{
			name: "other_kubernetes_bad_requests_are_not_silently_retryable",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"invalid proxy path","reason":"BadRequest","code":400}`,
		},
		{
			name: "status_body_code_must_match",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":503}`,
		},
		{
			name: "http_code_must_match",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":400}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := transientPodProxyError(tc.code, []byte(tc.body), requestErr)
			if tc.want {
				if !errors.Is(err, errPodProxyTargetNotAddressable) {
					t.Fatalf("transient target error = false, want true (err %v)", err)
				}
			} else if err != nil {
				t.Fatalf("non-target response returned error: %v", err)
			}
		})
	}
}

func TestTransientPodProxyErrorRequiresARequestFailure(t *testing.T) {
	t.Parallel()
	body := []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure",` +
		`"message":"address not allowed","reason":"BadRequest","code":400}`)
	if err := transientPodProxyError(http.StatusBadRequest, body, nil); err != nil {
		t.Fatalf("successful request classified as transient: %v", err)
	}
}

func TestNotLeaderResponseDistinguishesEngineFromAPIServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{
			name: "engine_not_leader",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
			want: true,
		},
		{
			name: "kubernetes_service_unavailable",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"service unavailable","reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "engine_service_unavailable_with_another_code",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"temporarily_unavailable","message":"retry later"}}`,
		},
		{
			name: "not_leader_with_the_wrong_http_status",
			code: http.StatusBadRequest,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
		},
		{
			name: "malformed_body",
			code: http.StatusServiceUnavailable,
			body: `{`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotLeaderResponse(tc.code, tc.body); got != tc.want {
				t.Fatalf("isNotLeaderResponse() = %t, want %t", got, tc.want)
			}
		})
	}
}
