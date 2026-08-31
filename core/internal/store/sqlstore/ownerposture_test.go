// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// TestOwnerPostureError pins the owner-pool admission policy WITHOUT a PostgreSQL
// server, because the pool wiring cannot be exercised here and a test that only
// called RolePosture.TriggersDisabled would stay green if the barrier in
// openOwnerPool were deleted outright.
//
// The two regressions it must catch, named explicitly:
//
//  1. deleting the TriggersDisabled barrier;
//  2. moving the AllowPrivilegedRole early return BEFORE it — which would let an
//     operator who opted out of the RLS bar also silently opt out of the trigger
//     bar, and those are not the same trade.
func TestOwnerPostureError(t *testing.T) {
	safe := dialect.RolePosture{Role: "olivares_owner", ReplicationRole: "origin"}
	local := dialect.RolePosture{Role: "olivares_owner", ReplicationRole: "local"}
	replica := dialect.RolePosture{Role: "olivares_owner", ReplicationRole: "replica"}
	future := dialect.RolePosture{Role: "olivares_owner", ReplicationRole: "some_future_mode"}
	privileged := dialect.RolePosture{Role: "postgres", ReplicationRole: "origin", Superuser: true}
	privilegedReplica := dialect.RolePosture{
		Role: "postgres", ReplicationRole: "replica", Superuser: true,
	}

	for _, tc := range []struct {
		name            string
		posture         dialect.RolePosture
		allowPrivileged bool
		wantErr         bool
		wantContains    string
	}{
		{name: "origin owner is accepted", posture: safe},
		{name: "local owner is accepted", posture: local},
		{
			name: "replica owner is refused", posture: replica,
			wantErr: true, wantContains: "session_replication_role",
		},
		{
			// The trigger bar must run BEFORE the opt-out: this is the ordering
			// regression. AllowPrivilegedRole waives the RLS backstop only.
			name:    "replica owner is refused even with AllowPrivilegedRole",
			posture: replica, allowPrivileged: true,
			wantErr: true, wantContains: "session_replication_role",
		},
		{
			name: "an unknown replication mode is refused", posture: future,
			wantErr: true, wantContains: "session_replication_role",
		},
		{
			name: "a superuser owner is refused by the RLS bar", posture: privileged,
			wantErr: true, wantContains: "row-level security",
		},
		{
			name:    "a superuser owner is accepted once the RLS bar is waived",
			posture: privileged, allowPrivileged: true,
		},
		{
			// Both bars fail: the trigger one must win, because it has no opt-out.
			name:    "a privileged replica owner is refused for the trigger reason",
			posture: privilegedReplica, allowPrivileged: true,
			wantErr: true, wantContains: "session_replication_role",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ownerPostureError(tc.posture, tc.allowPrivileged)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("owner posture %+v (allowPrivileged=%v) was accepted",
						tc.posture, tc.allowPrivileged)
				}
				if !strings.Contains(err.Error(), tc.wantContains) {
					t.Fatalf("error %q does not mention %q", err, tc.wantContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("owner posture %+v (allowPrivileged=%v) was refused: %v",
					tc.posture, tc.allowPrivileged, err)
			}
		})
	}
}
