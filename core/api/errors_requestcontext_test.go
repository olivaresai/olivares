// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// TestStatusFor_RequestContext separates the two context errors, which look alike
// and mean opposite things.
//
// A CANCELED request context is the caller hanging up: answering 5xx would log an
// error, page an operator and count a client's disconnect against the engine's own
// availability. A DEADLINE is the engine's own budget expiring — every
// context.WithTimeout in this tree produces one — and this mapper is shared by
// REST, gRPC and module errors, so answering 4xx would move a server timeout out
// of the availability SLI and out of the 5xx alert
// (docs/17-PRODUCTION-READINESS-SLO.md §1). A server fault must stay a server
// fault, so the deadline keeps the 500 it has always had.
//
// Both are wrapped as well as bare, because a context error reaches this mapper
// through whatever the handler wrapped it in.
func TestStatusFor_RequestContext(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"canceled: the caller hung up", context.Canceled, http.StatusRequestTimeout, "request_abandoned"},
		{"canceled, wrapped", fmt.Errorf("login: %w", context.Canceled), http.StatusRequestTimeout, "request_abandoned"},
		{"deadline: the engine's own budget", context.DeadlineExceeded, http.StatusInternalServerError, "internal"},
		{"deadline, wrapped", fmt.Errorf("store read: %w", context.DeadlineExceeded), http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := statusFor(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)", tc.err, status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}
