// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func TestRequirePinnedDirectoryAdminPosture(t *testing.T) {
	boot := guardRoleFact{Role: "olivares_admin", Known: true}
	for _, replicationRole := range []string{"origin", "local"} {
		posture := dialect.RolePosture{
			Role: boot.Role, BypassRLS: true, ReplicationRole: replicationRole,
		}
		if err := requirePinnedDirectoryAdminPosture(posture, boot); err != nil {
			t.Errorf("safe replication role %q rejected: %v", replicationRole, err)
		}
	}

	for _, test := range []struct {
		name    string
		posture dialect.RolePosture
		boot    guardRoleFact
	}{
		{
			name: "boot role unknown",
			posture: dialect.RolePosture{
				Role: "olivares_admin", BypassRLS: true, ReplicationRole: "origin",
			},
			boot: guardRoleFact{Role: "olivares_admin"},
		},
		{
			name: "boot role empty",
			posture: dialect.RolePosture{
				Role: "olivares_admin", BypassRLS: true, ReplicationRole: "origin",
			},
			boot: guardRoleFact{Known: true},
		},
		{
			name: "live role changed",
			posture: dialect.RolePosture{
				Role: "other_admin", BypassRLS: true, ReplicationRole: "origin",
			},
			boot: boot,
		},
		{
			name: "bypass removed",
			posture: dialect.RolePosture{
				Role: boot.Role, ReplicationRole: "origin",
			},
			boot: boot,
		},
		{
			name: "superuser",
			posture: dialect.RolePosture{
				Role: boot.Role, Superuser: true, BypassRLS: true, ReplicationRole: "origin",
			},
			boot: boot,
		},
		{
			name: "triggers disabled",
			posture: dialect.RolePosture{
				Role: boot.Role, BypassRLS: true, ReplicationRole: "replica",
			},
			boot: boot,
		},
		{
			name: "trigger posture unread",
			posture: dialect.RolePosture{
				Role: boot.Role, BypassRLS: true,
			},
			boot: boot,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requirePinnedDirectoryAdminPosture(test.posture, test.boot)
			if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
				t.Fatalf("posture error = %v, want ErrEnumerationNotAuthoritative", err)
			}
		})
	}
}

func TestListOrgsVisibleAdminPathPinsOneReadOnlySnapshot(t *testing.T) {
	source, err := os.ReadFile("system.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	requiredInOrder := []string{
		"sys.s.adminDB.BeginTx(ctx, &sql.TxOptions{",
		"Isolation: sql.LevelRepeatableRead",
		"ReadOnly:  true",
		"sys.s.dia.ConnRolePosture(ctx, adminTx)",
		"verifyDirectoryActivationDatabaseIdentity(",
		"directoryActivationWitnesses{admin: adminTx}",
		"sys.listOrgsVisibleRows(ctx, adminTx)",
		"adminTx.Commit()",
	}
	position := 0
	for _, fragment := range requiredInOrder {
		next := strings.Index(text[position:], fragment)
		if next < 0 {
			t.Fatalf("system.go lacks ordered admin snapshot fragment %q", fragment)
		}
		position += next + len(fragment)
	}
	if strings.Contains(text, "sys.s.adminDB.QueryContext(ctx, q)") {
		t.Fatal("ListOrgsVisible queries adminDB outside its attested transaction")
	}
}
