// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestTheDisarmRequesterInOrgSettingsOnlyRestricts: the system organization's
// settings hold the account that asked to disarm the restore gate's dual
// control, which the gate then refuses. The settings column declares that
// reference as one that only restricts: scanned for the account, never counted.
func TestTheDisarmRequesterInOrgSettingsOnlyRestricts(t *testing.T) {
	var decl *model.ColumnDecl
	for _, f := range orgDescriptor.Fields {
		if f.Name == "settings" {
			decl = f.Principal
		}
	}
	if decl == nil {
		t.Fatal("core.org declares no settings column")
	}
	if decl.Form != model.FormScan || decl.Class != model.ClassRestrict {
		t.Errorf("core.org.settings is declared %s %q, want a restricting scan: it holds the disarm requester", decl.Form, decl.Class)
	}
	if decl.Counted() {
		t.Errorf("core.org.settings is counted, but a restriction never lets its account act")
	}
}
