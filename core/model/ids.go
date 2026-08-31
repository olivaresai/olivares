// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"fmt"

	"github.com/google/uuid"
)

// ID is the primary key of every entity: a UUIDv7 in canonical lowercase
// 36-character text form. v7 is time-ordered (index-friendly, low B-tree churn)
// yet carries 122 unguessable bits, which closes cross-tenant enumeration /
// IDOR — a multi-tenant security requirement. It is stored as TEXT on both
// engines (identical bytes, identical to the form hashed into the audit chain).
type ID string

// TenantID is the isolation boundary stamped on every row. It is a UUIDv7 in
// the same canonical text form as ID, but a distinct Go type so a tenant can
// never be passed where an entity id is expected, and vice versa.
type TenantID string

// SystemTenantID is the reserved tenant for cross-tenant/system audit events
// (migrations, tenant provisioning, global verification). It is the max UUID, a
// value the v7 generator never produces, so it cannot collide with a real
// tenant. It is non-zero, so it is distinguishable from the unset tenant.
const SystemTenantID TenantID = "ffffffff-ffff-ffff-ffff-ffffffffffff"

// nilUUID is the all-zero UUID, treated as "unset" for both ID and TenantID.
const nilUUID = "00000000-0000-0000-0000-000000000000"

// NewID returns a fresh time-ordered UUIDv7 ID. It panics only if the system
// entropy source fails, which is unrecoverable and must not be papered over.
func NewID() ID {
	return ID(uuid.Must(uuid.NewV7()).String())
}

// NewTenantID returns a fresh time-ordered UUIDv7 TenantID.
func NewTenantID() TenantID {
	return TenantID(uuid.Must(uuid.NewV7()).String())
}

// ParseID validates s as a canonical UUID and returns it as an ID.
func ParseID(s string) (ID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse id: %w", err)
	}
	return ID(u.String()), nil
}

// ParseTenantID validates s as a canonical UUID and returns it as a TenantID.
func ParseTenantID(s string) (TenantID, error) {
	if s == string(SystemTenantID) {
		return SystemTenantID, nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse tenant id: %w", err)
	}
	return TenantID(u.String()), nil
}

// String returns the canonical text form of the id.
func (id ID) String() string { return string(id) }

// IsZero reports whether the id is unset (empty or the all-zero UUID).
func (id ID) IsZero() bool { return id == "" || string(id) == nilUUID }

// String returns the canonical text form of the tenant id.
func (t TenantID) String() string { return string(t) }

// IsZero reports whether the tenant id is unset (empty or the all-zero UUID).
func (t TenantID) IsZero() bool { return t == "" || string(t) == nilUUID }

// IsSystem reports whether t is the reserved system tenant.
func (t TenantID) IsSystem() bool { return t == SystemTenantID }
