// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/store"
)

// agreeRestoreDestination measures the actual sessions selected for this control
// operation. The coordination session will do the DDL itself. Witness pools may
// route differently; independently probing their DSNs cannot certify these pins.
func agreeRestoreDestination(ctx context.Context, cfg store.Config, coord *drCoordination, app, ddl, admin *sql.Conn) (roles restoreControlRoles, dest drDestination, err error) {
	fail := func(label, why string) error {
		return fmt.Errorf("%w: restore destination agreement: %s: %s", ErrRestoreCoordinationUnknown, label, why)
	}
	holder := probeRetainedConnAuthority(ctx, coord.conn)
	validate := func(label string, a retainedConnAuthority) error {
		if !a.Posture.Reachable {
			return fail(label, a.Posture.Err)
		}
		if a.SystemIdentifier == "" || a.SystemIdentifierErr != "" {
			return fail(label, "system identifier unavailable: "+a.SystemIdentifierErr)
		}
		if a.Database == "" || a.DatabaseOID <= 0 || !a.SchemaExists || a.SchemaOID <= 0 {
			return fail(label, "database or pinned engine schema identity unavailable")
		}
		if a.SessionRole == "" || a.SessionRole != a.CurrentRole || a.RoleOID <= 0 {
			return fail(label, "session/current role identity does not agree")
		}
		if a.InRecovery {
			return fail(label, "session is in recovery")
		}
		if label != "admin" && a.SessionReadOnly {
			return fail(label, "session is read-only")
		}
		if a.SystemIdentifier != holder.SystemIdentifier || a.Database != holder.Database || a.DatabaseOID != holder.DatabaseOID || a.Schema != holder.Schema || a.SchemaOID != holder.SchemaOID {
			return fail(label, "retained session names a different cluster, database or engine schema")
		}
		return nil
	}
	if err := validate("coordination", holder); err != nil {
		return roles, dest, err
	}
	if !holder.CanCreate {
		return roles, dest, fail("coordination", "DDL role lacks CREATE on the pinned engine schema")
	}
	if err := ownerPostureError(holder.rolePosture, cfg.AllowPrivilegedRole); err != nil {
		return roles, dest, fail("coordination", err.Error())
	}
	// readDestination established the outer lock key. Do not replace its identity
	// with later measurements if a connection route changed underneath it.
	if holder.Database != coord.dest.Database || holder.Schema != coord.dest.Schema || !coord.dest.SystemIdentifierKnown || holder.SystemIdentifier != coord.dest.SystemIdentifier || holder.BackendPID != coord.backendPID {
		return roles, dest, fail("coordination", "identity differs from the retained restore lock destination")
	}
	witnessPIDs := make(map[*sql.Conn]int)
	for _, w := range []struct {
		label string
		conn  *sql.Conn
	}{{"application", app}, {"DDL", ddl}, {"admin", admin}} {
		if w.conn == nil {
			continue
		}
		a := probeRetainedConnAuthority(ctx, w.conn)
		if err := validate(w.label, a); err != nil {
			return roles, dest, err
		}
		if pid, seen := witnessPIDs[w.conn]; seen && pid != a.BackendPID {
			return roles, dest, fail(w.label, "retained connection changed its backend")
		}
		witnessPIDs[w.conn] = a.BackendPID
		switch w.label {
		case "application":
			roles.app, roles.appOID = a.CurrentRole, a.RoleOID
		case "DDL":
			if a.CurrentRole != holder.CurrentRole || a.RoleOID != holder.RoleOID || !a.CanCreate {
				return roles, dest, fail(w.label, "DDL authority differs from the coordination role")
			}
			roles.owner, roles.ownerOID = a.CurrentRole, a.RoleOID
		case "admin":
			if !a.Posture.RLSUnsafe() || (a.Posture.Superuser && !cfg.AllowPrivilegedRole) {
				return roles, dest, fail(w.label, "supplied admin role lacks its required read posture")
			}
			roles.admin, roles.adminOID = a.CurrentRole, a.RoleOID
		}
	}
	challenge, err := beginRetainedServerChallenge(ctx, coord.conn)
	if err != nil {
		return roles, dest, fail("coordination challenge", err.Error())
	}
	defer func() {
		if rerr := challenge.rollback(); rerr != nil {
			err = errors.Join(err, fail("coordination challenge rollback", rerr.Error()))
		}
	}()
	if challenge.backendPID != holder.BackendPID {
		return roles, dest, fail("coordination challenge", "retained connection changed its backend")
	}
	for _, w := range []struct {
		label string
		conn  *sql.Conn
	}{{"application", app}, {"DDL", ddl}, {"admin", admin}} {
		if w.conn == nil || (w.label == "DDL" && ddl == app) {
			continue
		}
		v := challenge.witness(ctx, w.conn, w.label)
		if !v.SameServer || v.Err != "" {
			return roles, dest, fail(w.label, fmt.Sprintf("live-server challenge refused (acquired=%t): %s", v.AcquiredHoldersLock, v.Err))
		}
		if v.backendPID != witnessPIDs[w.conn] {
			return roles, dest, fail(w.label, "retained witness changed its backend")
		}
	}
	dest = coord.dest
	dest.DatabaseOID, dest.SchemaOID = holder.DatabaseOID, holder.SchemaOID
	return roles, dest, nil
}
