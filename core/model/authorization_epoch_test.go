// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"errors"
	"testing"
)

func TestAuthorizationEpochValidationIsClosed(t *testing.T) {
	tenant := TenantID(NewID())
	valid := AuthorizationEpoch{BaseFields: BaseFields{
		ID: ID(tenant), TenantID: tenant, Version: 1,
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid authorization epoch: %v", err)
	}

	tests := map[string]AuthorizationEpoch{
		"zero tenant":     {BaseFields: BaseFields{ID: ID(tenant), Version: 1}},
		"system tenant":   {BaseFields: BaseFields{ID: ID(SystemTenantID), TenantID: SystemTenantID, Version: 1}},
		"zero id":         {BaseFields: BaseFields{TenantID: tenant, Version: 1}},
		"mismatched id":   {BaseFields: BaseFields{ID: NewID(), TenantID: tenant, Version: 1}},
		"zero generation": {BaseFields: BaseFields{ID: ID(tenant), TenantID: tenant}},
	}
	for name, epoch := range tests {
		t.Run(name, func(t *testing.T) {
			err := epoch.Validate()
			if !errors.Is(err, ErrInvalidAuthorizationEpoch) {
				t.Fatalf("Validate() error = %v, want ErrInvalidAuthorizationEpoch", err)
			}
		})
	}
}
