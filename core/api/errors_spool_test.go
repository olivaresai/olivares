// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestStatusFor_AuditSpoolFull(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"sentinel", store.ErrAuditSpoolFull},
		{"wrapped", fmt.Errorf("append audit event: %w", store.ErrAuditSpoolFull)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := statusFor(tc.err)
			if status != http.StatusServiceUnavailable || code != "audit_spool_full" {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)",
					tc.err, status, code, http.StatusServiceUnavailable, "audit_spool_full")
			}
		})
	}
}
