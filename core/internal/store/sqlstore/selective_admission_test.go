// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openSelectivePGSplit opens a split-owner PostgreSQL store: the topology the
// selective envelope actually runs under, where DDL belongs to a separate owner
// role and runtime traffic to the RLS-bound application role.
func openSelectivePGSplit(t *testing.T, maxConns int) store.Store {
	t.Helper()
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: maxConns,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestSelectiveEnvelopeAdmitsOnlyCanonicalTenantsWithAnExistingOrg is the
// permanent control for the raw-envelope admission contract the ratification
// assigns to the SQL envelope: "canonical tenant binding, existing-org evidence
// and L0 stability".
//
// It goes at the RAW store on purpose. The optional residency and suspension
// wrappers also read Org, but neither discharges this requirement: residency is
// inert in a single-region deployment, suspension is optional, and the public
// evidence helper can be handed the raw SQL store directly. A raw envelope that
// admits an identity the engine's own canonical seam rejects has already run the
// caller's callback inside a tenant-bound transaction before anything else can
// object.
func TestSelectiveEnvelopeAdmitsOnlyCanonicalTenantsWithAnExistingOrg(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres_split"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			var st store.Store
			if engine == "sqlite" {
				st = openSQLiteTest(t, nil)
			} else {
				st = openSelectivePGSplit(t, 4)
			}
			selective, ok := st.(store.SelectiveMutator)
			if !ok {
				t.Fatal("the SQL store does not expose SelectiveMutator")
			}
			active := provisionTenant(t, st, "selective-admission-active")
			keyPlan, err := store.NewTransactionLockPlan("selective:admission")
			if err != nil {
				t.Fatal(err)
			}

			// Refusals. Each identity is checked through BOTH raw methods, because
			// the envelope is shared and a correction applied to only one of them
			// leaves the other open.
			for _, tc := range []struct {
				name   string
				tenant model.TenantID
				want   error
			}{
				{"malformed", model.TenantID("not-a-tenant"), store.ErrDirectoryUnavailable},
				{
					"uppercase_system",
					model.TenantID(strings.ToUpper(model.SystemTenantID.String())),
					store.ErrDirectoryUnavailable,
				},
				{"canonical_missing_org", model.NewTenantID(), store.ErrNotFound},
				{"zero", model.TenantID(""), store.ErrNoTenant},
				{"system", model.SystemTenantID, store.ErrSelectiveMutationPlan},
			} {
				t.Run(tc.name, func(t *testing.T) {
					opPlan, err := store.NewEvidenceOperationPlan("selective-admission-" + tc.name)
					if err != nil {
						t.Fatal(err)
					}
					ran := false
					err = selective.MutateCoordination(ctx, tc.tenant, keyPlan,
						func(store.CoordinationMutationScope) error { ran = true; return nil })
					t.Logf("coordination tenant=%q ran=%t err=%v", tc.tenant, ran, err)
					if ran {
						t.Error("coordination callback ran for an identity the envelope must refuse")
					}
					if !errors.Is(err, tc.want) {
						t.Errorf("coordination error = %v, want one wrapping %v", err, tc.want)
					}
					ran = false
					err = selective.MutateEvidenceOperation(ctx, tc.tenant, opPlan,
						func(store.EvidenceOperationMutationScope) error { ran = true; return nil })
					t.Logf("evidence tenant=%q ran=%t err=%v", tc.tenant, ran, err)
					if ran {
						t.Error("evidence callback ran for an identity the envelope must refuse")
					}
					if !errors.Is(err, tc.want) {
						t.Errorf("evidence error = %v, want one wrapping %v", err, tc.want)
					}
				})
			}

			// Positives: an existing, active, canonical business tenant is admitted
			// through both methods and commits. Without this half the refusals above
			// would also be satisfied by an envelope that refuses everything.
			t.Run("active_tenant_positive", func(t *testing.T) {
				ran := false
				if err := selective.MutateCoordination(ctx, active, keyPlan,
					func(sc store.CoordinationMutationScope) error {
						ran = true
						if sc.Tenant() != active {
							t.Errorf("bound tenant = %s, want %s", sc.Tenant(), active)
						}
						org, err := sc.Org(ctx)
						if err != nil {
							return err
						}
						if org.TenantID != active || org.Status != model.StatusActive {
							t.Errorf("org = %s/%s, want %s/active", org.TenantID, org.Status, active)
						}
						return nil
					}); err != nil || !ran {
					t.Fatalf("active tenant coordination ran=%t err=%v", ran, err)
				}
				opPlan, err := store.NewEvidenceOperationPlan("selective-admission-positive")
				if err != nil {
					t.Fatal(err)
				}
				before := countAuditAction(t, st, active, "mcp.tool.call.claim")
				ran = false
				if err := selective.MutateEvidenceOperation(ctx, active, opPlan,
					func(sc store.EvidenceOperationMutationScope) error {
						ran = true
						res, err := sc.EvidenceOperations().Claim(
							ctx, testClaim("selective-admission-positive", "digest"))
						if err != nil {
							return err
						}
						if !res.Fresh {
							t.Error("first claim was not fresh")
						}
						return nil
					}); err != nil || !ran {
					t.Fatalf("active tenant evidence ran=%t err=%v", ran, err)
				}
				if _, err := getEvidenceOp(t, st, active, "selective-admission-positive"); err != nil {
					t.Fatalf("admitted claim did not commit: %v", err)
				}
				if got := countAuditAction(t, st, active, "mcp.tool.call.claim"); got != before+1 {
					t.Fatalf("admitted claim audit count = %d, want %d", got, before+1)
				}
			})
		})
	}
}

// TestSelectiveHelperRefusesOrglessTenantWithoutResidue drives the REAL public
// helper — no repository fake, no manufactured conflict — for a canonical tenant
// that has no organization on this instance, and then reads back through the
// public API. The helper must refuse and leave neither a journal row nor a tenant
// audit event behind.
//
// It also records the measured baseline for bare legacy Store.Mutate on the same
// input, and asserts it UNCHANGED. That assertion is a scope fence, not an
// endorsement: legacy Mutate admitting a canonical tenant with no org is an open
// question for root about the older path, and this correction deliberately does
// not answer it. Its behaviour is pinned here so that a later change to the
// selective envelope cannot quietly move the legacy one with it.
func TestSelectiveHelperRefusesOrglessTenantWithoutResidue(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres_split"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			var st store.Store
			if engine == "sqlite" {
				st = openSQLiteTest(t, nil)
			} else {
				st = openSelectivePGSplit(t, 4)
			}
			// A real instance the tenant could have belonged to, so the refusal is
			// about THIS tenant and not about an empty database.
			provisionTenant(t, st, "selective-orphan-neighbour")
			missing := model.NewTenantID()

			legacyRan := false
			legacyErr := st.Mutate(ctx, missing, func(store.Scope) error { legacyRan = true; return nil })
			t.Logf("baseline: legacy Mutate for a canonical tenant with no org ran=%t err=%v",
				legacyRan, legacyErr)
			if !legacyRan || legacyErr != nil {
				t.Errorf("this correction changed legacy Mutate: ran=%t err=%v (want ran=true, err=nil)",
					legacyRan, legacyErr)
			}

			out, err := store.ClaimEvidenceOperation(
				ctx, st, missing, testClaim("selective-orphan", "digest"))
			refused := out.Receipt.MustRefuse(out.Binding)
			t.Logf("helper for a canonical tenant with no org: fresh=%t refused=%t err=%v",
				out.Fresh, refused, err)
			if err == nil && !refused {
				t.Error("the helper issued an accepted receipt for a tenant with no organization")
			}
			if out.Fresh {
				t.Error("the helper reported a fresh claim for a tenant with no organization")
			}
			if _, readErr := getEvidenceOp(t, st, missing, "selective-orphan"); !errors.Is(readErr, store.ErrNotFound) {
				t.Errorf("orphan journal row survived: %v", readErr)
			}
			if got := countAuditAction(t, st, missing, "mcp.tool.call.claim"); got != 0 {
				t.Errorf("orphan tenant audit events = %d, want 0", got)
			}
		})
	}
}
