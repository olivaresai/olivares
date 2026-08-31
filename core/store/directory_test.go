// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestDirectoryPrincipalRefIsCanonical(t *testing.T) {
	const (
		principal model.ID = "018f2000-0000-7000-8000-000000000001"
		workspace model.ID = "018f2000-0000-7000-8000-000000000002"
	)
	for name, ref := range map[string]DirectoryPrincipalRef{
		"user": {
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  principal,
		},
		"identity without workspace": {
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  principal,
		},
		"agent with workspace": {
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  principal,
			WorkspaceRef:  workspace,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ref.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}

	for name, ref := range map[string]DirectoryPrincipalRef{
		"unknown kind": {
			PrincipalKind: "session",
			PrincipalRef:  principal,
		},
		"zero principal": {
			PrincipalKind: model.DirectoryPrincipalAgent,
		},
		"v4 principal": {
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  "123e4567-e89b-12d3-a456-426614174000",
		},
		"user workspace": {
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  principal,
			WorkspaceRef:  workspace,
		},
		"user nil UUID workspace": {
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  principal,
			WorkspaceRef:  "00000000-0000-0000-0000-000000000000",
		},
		"nil UUID workspace": {
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  principal,
			WorkspaceRef:  "00000000-0000-0000-0000-000000000000",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ref.Validate(); !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDirectoryEvidence", err)
			}
		})
	}
}
