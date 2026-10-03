// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"fmt"
	"strings"
)

// The group-origin contract (generic Community seam): every user_groups row
// records WHO provisions it, and that one field is the boundary every writer
// respects. Origins:
//
//   - "" (SQL NULL): today's SCIM/IdP-managed row. SCIM and the operator
//     console write it exactly as before — this seam changes nothing about it.
//   - "operator": a local group managed from the console. The console may
//     write it; SCIM and the login-time reconcile may not.
//   - any other value: the slug of the provisioner that owns the group (each
//     one names itself). ONLY that provisioner may write its identity and
//     roster; the operator manages MappedRole under the tenant role ceiling.
//
// No writer may move a group between claimed origins: adoption is possible
// only while a row is unclaimed (""), and only by the writer claiming it.
// A named slug is opaque to this package — it is validated as a slug, never
// interpreted, so a provisioner is a name with a write boundary and nothing
// more. No path here names any product, edition or paid feature.

// GroupProvisionerOperator is the origin of a group the console manages.
const GroupProvisionerOperator = "operator"

// The two boundaries the writers surface. They are distinct on purpose: a
// read-only group (someone else owns it) and an adoption refusal (this row is
// already claimed) tell an operator two different things, and collapsing them
// would send a console user hunting a permission nobody can grant.
var (
	// ErrGroupOriginReadOnly is returned to a writer that is not the group's
	// provisioner: the row exists, its membership is visible, and modifying it
	// belongs to whoever owns the origin.
	ErrGroupOriginReadOnly = errors.New("auth: this group is managed by another provisioner")
	// ErrGroupOriginAdopted is returned when a write would move a group out of
	// a claimed origin into another (including back to IdP-managed).
	ErrGroupOriginAdopted = errors.New("auth: a claimed group cannot change provisioner")
	// ErrBadGroupProvisioner is returned when an origin value is not a valid
	// slug (or one of the two reserved words misused).
	ErrBadGroupProvisioner = errors.New("auth: invalid group provisioner slug")
)

// ValidateGroupProvisioner accepts the closed origin vocabulary: "", the
// reserved "operator", or a provisioner slug ([a-z][a-z0-9-]{0,31}). The slug
// grammar matches the IdP alias one (validateFederationAlias) so operators
// read the same shape everywhere.
func ValidateGroupProvisioner(origin string) error {
	if origin == "" || origin == GroupProvisionerOperator {
		return nil
	}
	if len(origin) > 31 || origin[0] < 'a' || origin[0] > 'z' {
		return fmt.Errorf("%w: %q must be 1-31 chars of [a-z0-9-] starting with a letter",
			ErrBadGroupProvisioner, origin)
	}
	for i := 0; i < len(origin); i++ {
		c := origin[i]
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return fmt.Errorf("%w: %q must be 1-31 chars of [a-z0-9-] starting with a letter",
				ErrBadGroupProvisioner, origin)
		}
	}
	return nil
}

// mayWriteGroupOrigin is the source boundary. writer is "" for the SCIM/IdP
// paths, GroupProvisionerOperator for the console paths, or a provisioner slug.
//
//	origin ""       → writable by "" and by the operator (today's behavior,
//	                  byte for byte: SCIM provisions it, the console maps and
//	                  nests it)
//	origin "x"      → writable by writer "x" only (operator included)
func mayWriteGroupOrigin(origin, writer string) bool {
	if origin == "" {
		return writer == "" || writer == GroupProvisionerOperator
	}
	return origin == writer
}

// mayAdoptGroupOrigin reports whether writer may move a group from origin
// `from` to `to`. Only an UNCLAIMED row can be claimed (from == ""), and only
// by the writer doing the claiming; a claimed row never changes hands — not to
// another provisioner, not back to IdP-managed (clearing an origin is the
// provisioner deleting the group instead).
func mayAdoptGroupOrigin(from, to, writer string) bool {
	if err := ValidateGroupProvisioner(to); err != nil {
		return false
	}
	if from == to {
		return true // a no-op rewrite is not an adoption
	}
	return from == "" && to == writer
}

// QualifyGroupExternalID qualifies a connector's external group id with an
// opaque connector uuid, so the per-tenant unique index on
// (target_tenant_id, external_id) never merges two connectors that happen to
// reuse an identifier. The SCIM path stores ids UNQUALIFIED (today's rows must
// keep correlating); only a connector that owns its own provisioning qualifies,
// and the uuid is opaque to this package — a stable string the connector
// generates once and keeps.
//
// The form is "<uuid>/<external-id>", which cannot collide with an unqualified
// id because a UUIDv7 contains "-" and hex only while the separator "/" is
// outside both alphabets an IdP correlation key uses in practice — and, more
// strongly, because two DIFFERENT connectors produce different prefixes even
// on identical local ids.
func QualifyGroupExternalID(connectorUUID, externalID string) string {
	if connectorUUID == "" {
		return externalID
	}
	return connectorUUID + "/" + externalID
}

// UnqualifyGroupExternalID splits a qualified id back into its connector uuid
// and local id. An unqualified id returns a zero uuid (today's SCIM rows).
func UnqualifyGroupExternalID(externalID string) (connectorUUID, localID string) {
	if i := strings.IndexByte(externalID, '/'); i >= 0 {
		return externalID[:i], externalID[i+1:]
	}
	return "", externalID
}
