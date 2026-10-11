// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfterSecondsCannotWrapIntoShortWait(t *testing.T) {
	for _, value := range []string{"9223372037", "9223372036854775807"} {
		resp := &http.Response{Header: http.Header{"Retry-After": {value}}}
		delay, ok := retryAfter(resp, time.Now())
		if !ok || delay <= maxRetryAfter {
			t.Fatalf("oversized seconds became a permitted wait: ok=%v delay=%s", ok, delay)
		}
	}
}

func TestGet429OversizedRetryAfterDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "9223372037")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := New(server.URL, server.Client(), nil, nil).GetJSON(ctx, "/fixture", nil, nil)
	var status *StatusError
	if !errors.As(err, &status) || status.Status != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("oversized wait must return the original 429 without retry: calls=%d, err=%v", calls.Load(), err)
	}
}
