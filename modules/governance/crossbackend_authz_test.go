// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/store"
)

// (T2) — the SAME authorization assertions run against BOTH storage
// backends. The governance scoped-grant/custom-role/Cedar suite historically
// exercised only SQLite; the access_edges Upsert bug (SQLSTATE 42702, latent on
// SQLite, live on Postgres) proved SQLite can mask a Postgres-only defect. This
// runs the RBAC deny + cross-tenant isolation on each engine through the real
// API+store path — SQLite ALWAYS, Postgres when a Postgres server is configured
// (CI), else skipped with a note (never silently "covered").
//
// the Postgres leg now provisions its OWN database. It previously opened
// the ONE database CI shares across the whole workspace, where core's
// TestPostgresIntegration had already created a user — so /v1/setup answered 409
// setup_complete and this test failed deterministically, not flakily.
func TestCrossBackend_AuthzParity(t *testing.T) {
	type backend struct {
		name string
		// opts is built lazily, inside the subtest, so the isolated Postgres
		// database is provisioned (and dropped) around THAT subtest only.
		opts func(t *testing.T) harnessOpts
	}
	backends := []backend{{name: "sqlite", opts: func(*testing.T) harnessOpts { return harnessOpts{} }}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, backend{name: "postgres", opts: func(t *testing.T) harnessOpts {
			pg := enginetest.IsolatedPostgres(t)
			// pg.Admin as well as pg.App: since a Postgres engine without the BYPASSRLS
			// pool refuses the cross-tenant System read that /v1/setup performs, so an
			// app-only engine cannot complete first boot. Passing it exercises the SUPPORTED
			// deployment; the unsupported one is covered by own battery.
			return harnessOpts{engine: store.EnginePostgres, dsn: pg.App, adminDSN: pg.Admin}
		}})
	} else {
		t.Logf("%s unset: Postgres backend NOT exercised (SQLite-only this run)", enginetest.EnvSuperuserDSN)
	}

	for _, be := range backends {
		be := be
		t.Run(be.name, func(t *testing.T) {
			h := newHarnessWith(t, be.opts(t))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme-"+be.name)
			other := h.createOrg(admin, "globex-"+be.name)
			_, viewer := h.roleUser(admin, tenant, "v-"+be.name+"@x.io", auth.RoleViewer)
			agent := h.createAgent(tenant, "a1", "ext-1-"+be.name)
			hdr := tenantHdr(tenant)

			// RBAC deny: a viewer holds no write, so deleting an agent is forbidden.
			if r := h.do("DELETE", "/v1/agents/"+agent.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
				t.Errorf("[%s] viewer DELETE agent = %d, want 403", be.name, r.code)
			}
			// Cross-tenant isolation: a viewer of `tenant` may not act in `other`.
			if r := h.do("DELETE", "/v1/agents/"+agent.ID.String(), viewer, nil, tenantHdr(other)); r.code == http.StatusOK || r.code == http.StatusNoContent {
				t.Errorf("[%s] viewer acted cross-tenant = %d, want denied", be.name, r.code)
			}
		})
	}
}
