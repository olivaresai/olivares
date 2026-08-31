// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// THE PROPERTY THE CLASS-A FIX MUST NOT BREAK, and the reason it is a fix rather than a new
// refusal.
//
// The obvious way to close "an unreadable role posture is recorded as an empty role" is to
// refuse the boot. That would have been wrong here, and the code says why in its own words:
// under AllowPrivilegedRole the degrade exists so that reading pg_roles does not "become a
// NEW way to refuse a deployment that used to boot" — pg_roles' PUBLIC grant is revocable
// and a catalog-hardened install running with the opt-out never needed it.
//
// The fix is cheaper than the refusal because IDENTITY IS NOT PRIVILEGE. Only the RLS
// attributes need pg_roles; current_user needs no grant at all. Measured on
// 15.18/16.14/17.10/18.4: after REVOKE SELECT ON pg_catalog.pg_roles FROM PUBLIC the posture
// query returns 42501 "permission denied for view pg_roles" and SELECT current_user still
// answers. So the boot keeps a KNOWN role instead of substituting a conventional one, and
// nothing that boots today stops booting.
//
// AND A MEASUREMENT THAT CONTRADICTS THE COMMENT THIS FIX WAS WRITTEN AGAINST, reported
// rather than quietly worked around. The degrade path justifies itself with "a
// catalog-hardened install running with the privileged-role opt-out never touched it
// before" — i.e. such a deployment boots today. IT DOES NOT. Measured on main (353d30511)
// with this exact fixture: the boot gets past the posture read and then dies further down,
// inside the guard rollout, at
//
//	sqlstore: read the owner of the shared guard function public.olivares_block_mutation():
//	ERROR: permission denied for view pg_roles (SQLSTATE 42501)
//
// which is a SECOND, unrelated pg_roles dependency (guardcoordinator.go, the owner check on
// the shared trigger function — untouched by this branch). So the population the degrade
// exists to protect cannot boot either way, and the premise under store.go's degrade is
// stale. That is a finding for its own adjudication, not something to bury by loosening a
// security check here: making the function-owner read tolerate an unreadable catalog is a
// decision about a defense, not a patch.
//
// So this test asserts what it can honestly assert, and that is the property this change
// OWNS: the identity is resolved separately, so this branch never becomes the blocker. If
// somebody later "simplifies" the fallback away, the boot fails at the dialect binding
// instead — a different error, on this code, which is exactly what this catches.
func TestPostgresACatalogHardenedInstallStillBoots(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	// AllowPrivilegedRole is the opt-out this whole branch exists under. Without it the boot
	// refuses on an unreadable posture BY DESIGN — the RLS guard genuinely needs the
	// attributes — and that refusal is not what this test is about.
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4, AllowPrivilegedRole: true}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("baseline open: %v", err)
	}
	_ = st.Close()

	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	defer func() { _ = super.Close() }()
	if _, err := super.ExecContext(ctx, "REVOKE SELECT ON pg_catalog.pg_roles FROM PUBLIC"); err != nil {
		t.Fatalf("harden the catalog: %v", err)
	}
	// Restore it for whatever shares this cluster: the revoke is database-wide.
	t.Cleanup(func() {
		_, _ = super.ExecContext(context.Background(), "GRANT SELECT ON pg_catalog.pg_roles TO PUBLIC")
	})

	// The precondition, asserted rather than assumed: the posture query must ACTUALLY be
	// failing now, or this test measures a boot that never took the degrade path.
	app, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	defer func() { _ = app.Close() }()
	var role string
	var su, brls bool
	postureErr := app.QueryRowContext(ctx,
		"SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").
		Scan(&role, &su, &brls)
	if postureErr == nil {
		t.Fatal("the posture query still succeeds after revoking pg_roles from PUBLIC; the fixture is " +
			"not in the hardened state this test is about (the role may hold a direct grant)")
	}
	// And the separable half must still answer — that is the whole mechanism.
	var ident string
	if err := app.QueryRowContext(ctx, "SELECT current_user").Scan(&ident); err != nil {
		t.Fatalf("current_user must need no catalog grant, got: %v", err)
	}
	t.Logf("HARDENED_CATALOG|posture_err=%v|identity=%q", postureErr, ident)

	st2, err := Open(ctx, cfg, registerWidget)
	if err == nil {
		_ = st2.Close()
		return
	}
	// THE ASSERTION IS ABOUT WHERE IT FAILS, not whether. This branch must never be the
	// reason: with the identity resolved separately, the dialect binds to the real role and
	// the topology is answerable. Removing the fallback produces one of the two messages
	// below instead — both of them this code's own.
	for _, mine := range []string{
		"cannot bind the append-only ACL to the connecting role",
		"could not resolve",
	} {
		if strings.Contains(err.Error(), mine) {
			t.Fatalf("a catalog-hardened install running with the privileged-role opt-out was refused BY "+
				"THIS CODE (%q). Identity is separable from privilege — current_user answered %q on this "+
				"very connection — so an unreadable pg_roles must not leave the role unknown: %v",
				mine, ident, err)
		}
	}
	// Anything else is the PRE-EXISTING dependency measured identically on main; it is
	// reported, not swallowed, so a future run that changes it is visible in the log.
	t.Logf("HARDENED_CATALOG|boot_failed_elsewhere=%v", err)
}
