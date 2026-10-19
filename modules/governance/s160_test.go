// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestThreadEventsSourceErrorIs502(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	h.threads.err = context.DeadlineExceeded
	r := h.do("GET", "/v1/m/claude-agents/sessions/sesn_1/events", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("a wired-but-failing thread source = %d, want 502 (%s)", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "UNKNOWN") {
		t.Fatalf("the outage must be named as unknown, not absent: %s", r.raw)
	}
}
