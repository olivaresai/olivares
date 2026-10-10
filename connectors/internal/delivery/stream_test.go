// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendStopsReadingAtResponseBudget(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, strings.Repeat("x", maxBodyExcerpt+1))
				w.(http.Flusher).Flush()
				// A destination can keep the body open after the diagnostic budget.
				// Delivery must return without waiting for its remaining bytes.
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			res, err := New(server.Client(), Options{MaxAttempts: 1}).Send(ctx, Request{URL: server.URL})
			if ctx.Err() != nil {
				t.Fatalf("delivery waited for the unbounded response tail: %v", ctx.Err())
			}
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("status %d: error = %v", status, err)
			}
			if res.StatusCode != status || res.Attempts != 1 || res.BodyComplete {
				t.Fatalf("result = %+v, want original status, one attempt and incomplete body", res)
			}
			if len(res.RawBody) > maxBodyExcerpt || (status == http.StatusOK && len(res.RawBody) != maxBodyExcerpt) {
				t.Fatalf("response excerpt = %d bytes; successful bytes must fill the budget, rejections stay bounded", len(res.RawBody))
			}
		})
	}
}
