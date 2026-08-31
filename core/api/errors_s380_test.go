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

func TestStatusForAuditSpoolFull(t *testing.T) {
	for _, err := range []error{
		store.ErrAuditSpoolFull,
		fmt.Errorf("append evidence: %w", store.ErrAuditSpoolFull),
	} {
		status, code := statusFor(err)
		if status != http.StatusServiceUnavailable || code != "audit_spool_full" {
			t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)", err, status, code, http.StatusServiceUnavailable, "audit_spool_full")
		}
	}
}
