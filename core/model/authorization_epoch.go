// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"errors"
	"fmt"
)

// AuthorizationEpochKind is the exact durable fact kind used to fence a
// tenant's authorization-generation snapshot.
const AuthorizationEpochKind Kind = "core.authorization_epoch"

// ErrInvalidAuthorizationEpoch marks a malformed durable authorization epoch.
// Callers must treat it as unavailable evidence, never as generation zero.
var ErrInvalidAuthorizationEpoch = errors.New("invalid authorization epoch")

// AuthorizationEpoch is the single tenant-local authorization-generation
// fact. ID and TenantID are the same real UUIDv7 and Version is the generation.
// The system tenant never has a row.
type AuthorizationEpoch struct {
	BaseFields
}

// Validate verifies the complete durable authorization epoch shape.
func (e AuthorizationEpoch) Validate() error {
	if err := validateDirectoryTenant(e.TenantID); err != nil {
		return fmt.Errorf("%w: tenant: %v", ErrInvalidAuthorizationEpoch, err)
	}
	if err := validateDirectoryID(e.ID, "authorization epoch id"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAuthorizationEpoch, err)
	}
	if e.ID.String() != e.TenantID.String() {
		return fmt.Errorf("%w: id must equal tenant id", ErrInvalidAuthorizationEpoch)
	}
	if e.Version < 1 {
		return fmt.Errorf("%w: version must be at least one", ErrInvalidAuthorizationEpoch)
	}
	return nil
}
