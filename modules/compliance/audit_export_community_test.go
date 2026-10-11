// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package compliance

import "testing"

func TestCommunityAuditExportEvidenceAbsent(t *testing.T) {
	for _, c := range PublicCatalog().Capabilities {
		if c.Key == "audit_export" {
			t.Fatalf("Community claims audit export: %+v", c)
		}
	}
}
