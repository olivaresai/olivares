// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SES1: Policy.Enabled is the authoritative current selection fact for per-seat
// spend limits. These tests pin the observable behavior of that decision and,
// just as deliberately, pin what it must NOT change — the administrative
// listing, the upsert identity, and the wire/audit shapes.
//
// Every scenario disables a row through the typed Policy update, because that is
// the only write that exists today: there is no Governance spend-limit
// enable/disable route (Governance accepts abac/approval kinds only), so the
// operator-facing control is the separately tracked SES2 obligation. The typed
// update re-encodes the decoded Spec, which is exactly why SES2 owes a
// byte-preserving Enabled-only seam; the fixtures here use canonical integer
// amounts so that re-encode is not the thing under test.
func disableSpendLimitPolicy(t *testing.T, m *Module, tenant model.TenantID, wireID string) {
	t.Helper()
	setSpendLimitPolicyEnabled(t, m, tenant, wireID, false)
}

func setSpendLimitPolicyEnabled(t *testing.T, m *Module, tenant model.TenantID, wireID string, enabled bool) {
	t.Helper()
	id, err := ParseSpendLimitID(wireID)
	if err != nil {
		t.Fatalf("parse spend limit id %q: %v", wireID, err)
	}
	ctx := context.Background()
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(ctx, id)
		if err != nil {
			return err
		}
		p.Enabled = enabled
		_, err = sc.Policies().Update(ctx, p)
		return err
	}); err != nil {
		t.Fatalf("set enabled=%v on %s: %v", enabled, wireID, err)
	}
}

// effectiveRow resolves exactly one principal×period row.
func effectiveRow(t *testing.T, m *Module, tenant model.TenantID, actor, period string) SpendLimitEffectiveRow {
	t.Helper()
	res, err := m.SpendLimitEffective(context.Background(), tenant, SpendLimitEffectiveOptions{
		UserIDs: []string{actor}, Periods: []string{period}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("effective(%s): %v", actor, err)
	}
	if len(res.Data) != 1 {
		t.Fatalf("effective(%s) rows = %d, want 1: %+v", actor, len(res.Data), res.Data)
	}
	return res.Data[0]
}

// wantCap asserts the source scope and amount a principal resolves to.
func wantCap(t *testing.T, row SpendLimitEffectiveRow, sourceType, groupID, amount string) {
	t.Helper()
	if row.Source == nil {
		t.Fatalf("%s resolved to NO policy, want %s %s", row.Actor.UserID, sourceType, amount)
	}
	if row.Source.Type != sourceType || row.Source.RBACGroupID != groupID {
		t.Fatalf("%s source = %+v, want type=%s group=%q", row.Actor.UserID, row.Source, sourceType, groupID)
	}
	if amount == "" {
		if row.Amount != nil {
			t.Fatalf("%s amount = %q, want unlimited", row.Actor.UserID, *row.Amount)
		}
		return
	}
	if row.Amount == nil || *row.Amount != amount {
		t.Fatalf("%s amount = %v, want %s", row.Actor.UserID, row.Amount, amount)
	}
}

func wantNoCap(t *testing.T, row SpendLimitEffectiveRow) {
	t.Helper()
	if row.Source != nil || row.SpendLimitID != nil || row.Amount != nil {
		t.Fatalf("%s resolved to %+v, want the existing no-seat-policy result", row.Actor.UserID, row)
	}
}

// TestSpendLimitDisabledRowsDoNotGovern_CrossBackend is the admission test for
// SES1's selection change. It runs on real SQLite always and on the owned
// PostgreSQL harness when one is configured, because persistence and selection
// together — not a unit-only resolver call — are what the revision requires.
func TestSpendLimitDisabledRowsDoNotGovern_CrossBackend(t *testing.T) {
	for name, cfg := range spendLimitEnabledBackends(t) {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg(t))
			runDisabledSelection(t, m, st, tenant)
		})
	}
}

// spendLimitEnabledBackends returns the engines these tests run on. The
// PostgreSQL leg is announced when it is skipped: a silently absent engine is
// indistinguishable from a passing one.
func spendLimitEnabledBackends(t *testing.T) map[string]func(t *testing.T) store.Config {
	t.Helper()
	configs := map[string]func(t *testing.T) store.Config{
		"sqlite": func(*testing.T) store.Config {
			return store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
		},
	}
	if enginetest.PostgresAvailable(t) {
		configs["postgres"] = func(t *testing.T) store.Config {
			return store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 4}
		}
	} else {
		t.Logf("%s unset: skipping the PostgreSQL leg of the SES1 selection tests", enginetest.EnvSuperuserDSN)
	}
	return configs
}

func runDisabledSelection(t *testing.T, m *Module, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	m.clock = spendLimitClock{at: now}

	u1 := createCanonicalUser(t, st, "ses1 member one").ID
	u2 := createCanonicalUser(t, st, "ses1 member two").ID
	u3 := createCanonicalUser(t, st, "ses1 member three").ID
	gA := createUserGroup(t, st, tenant, "ses1-a", u1)
	gB := createUserGroup(t, st, tenant, "ses1-b", u3)
	a1, a2, a3 := "user:"+u1.String(), "user:"+u2.String(), "user:"+u3.String()

	upsert := func(spec SpendLimitSpec) SpendLimit {
		t.Helper()
		row, _, err := m.SpendLimitUpsert(ctx, tenant, spec, "user:admin")
		if err != nil {
			t.Fatalf("upsert %+v: %v", spec.Scope, err)
		}
		return row
	}
	org := upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "organization"}, Amount: cents("1000"), Period: "monthly"})
	capA := upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: gA.ID.String()}, Amount: cents("300"), Period: "monthly"})
	capB := upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: gB.ID.String()}, Amount: cents("200"), Period: "monthly"})
	userStrict := upsert(userLimit(a1, "500", "monthly"))
	userUnlimited := upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "user", UserID: a2}, Amount: nil, Period: "monthly"})

	// Baseline: with every row enabled, precedence is user > group > organization
	// and the group tie-break is unchanged.
	wantCap(t, effectiveRow(t, m, tenant, a1, "monthly"), "user", "", "500")
	wantCap(t, effectiveRow(t, m, tenant, a2, "monthly"), "user", "", "")
	wantCap(t, effectiveRow(t, m, tenant, a3, "monthly"), "rbac_group", gB.ID.String(), "200")

	// A disabled STRICTER user cap falls through to the enabled group cap — it
	// does not remove the principal's cap and does not become an override.
	disableSpendLimitPolicy(t, m, tenant, userStrict.ID)
	wantCap(t, effectiveRow(t, m, tenant, a1, "monthly"), "rbac_group", gA.ID.String(), "300")

	// A disabled UNLIMITED user cap stops shadowing the organization cap, which
	// is the looser-direction case: the principal becomes MORE constrained.
	disableSpendLimitPolicy(t, m, tenant, userUnlimited.ID)
	wantCap(t, effectiveRow(t, m, tenant, a2, "monthly"), "organization", "", "1000")

	// A disabled group cap falls through to the organization cap.
	disableSpendLimitPolicy(t, m, tenant, capB.ID)
	wantCap(t, effectiveRow(t, m, tenant, a3, "monthly"), "organization", "", "1000")

	// Active precedence is untouched: an enabled user cap still beats everything
	// for that principal even while the principal's group cap is disabled.
	userThree := upsert(userLimit(a3, "150", "monthly"))
	wantCap(t, effectiveRow(t, m, tenant, a3, "monthly"), "user", "", "150")

	// A disabled ORGANIZATION cap leaves a principal with no seat policy at all,
	// which is the existing no-policy representation, not a zero cap.
	disableSpendLimitPolicy(t, m, tenant, org.ID)
	wantNoCap(t, effectiveRow(t, m, tenant, "user:outside", "monthly"))

	// Check, Effective and Reserve share resolveSpendLimitForPeriod, so all three
	// must agree on the same principal. u1 now governs under the 300-cent gA cap;
	// spending 400 cents must deny in Check and refuse the reservation, while
	// "user:outside" has no cap and is admitted.
	cost := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 4_000_000, now)
	cost.Actor = a1
	m.ingest(t, tenant, cost)

	chk, err := m.CheckSpendLimit(ctx, tenant, a1, []string{gA.ID.String()})
	if err != nil {
		t.Fatalf("CheckSpendLimit(u1): %v", err)
	}
	if chk.Allowed || chk.SpendLimitID != capA.ID || chk.LimitMicroUSD != 3_000_000 {
		t.Fatalf("CheckSpendLimit(u1) = %+v, want deny under the enabled group cap %s", chk, capA.ID)
	}
	res, err := m.ReserveSpendLimit(ctx, tenant, a1, []string{gA.ID.String()}, 1)
	if err != nil {
		t.Fatalf("ReserveSpendLimit(u1): %v", err)
	}
	if res.Allowed {
		t.Fatalf("ReserveSpendLimit(u1) = %+v, want the same refusal Check reports", res)
	}
	okRes, err := m.ReserveSpendLimit(ctx, tenant, "user:outside", nil, 1)
	if err != nil {
		t.Fatalf("ReserveSpendLimit(outside): %v", err)
	}
	if !okRes.Allowed {
		t.Fatalf("ReserveSpendLimit(outside) = %+v, want admitted: every seat policy is disabled", okRes)
	}
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:outside", nil); err != nil || !chk.Allowed {
		t.Fatalf("CheckSpendLimit(outside) = %+v err=%v, want allowed", chk, err)
	}

	// The administrative surface is deliberately NOT filtered: every disabled row
	// stays listable, gettable and paginable exactly as before.
	page, err := m.SpendLimitList(ctx, tenant, 1000, "", "")
	if err != nil || len(page.Data) != 6 {
		t.Fatalf("admin list = %d rows err=%v, want all 6 including the 4 disabled", len(page.Data), err)
	}
	for _, wire := range []string{org.ID, capB.ID, userStrict.ID, userUnlimited.ID} {
		id, err := ParseSpendLimitID(wire)
		if err != nil {
			t.Fatalf("parse %s: %v", wire, err)
		}
		if got, err := m.SpendLimitGet(ctx, tenant, id); err != nil || got.ID != wire {
			t.Fatalf("admin get disabled %s = %+v err=%v, want the row", wire, got, err)
		}
	}
	// Keyset paging across single-row pages still traverses the disabled rows.
	seen := map[string]bool{}
	var cursor model.ID
	for i := 0; i < 8; i++ {
		p, err := m.SpendLimitList(ctx, tenant, 1, cursor, "")
		if err != nil {
			t.Fatalf("paged admin list: %v", err)
		}
		if len(p.Data) == 0 {
			break
		}
		seen[p.Data[0].ID] = true
		cursor, _ = ParseSpendLimitID(p.Data[0].ID)
		if !p.HasMore {
			break
		}
	}
	for _, wire := range []string{org.ID, capA.ID, capB.ID, userStrict.ID, userUnlimited.ID, userThree.ID} {
		if !seen[wire] {
			t.Fatalf("single-row paging missed %s; disabled rows must stay discoverable on every page", wire)
		}
	}

	// The unfiltered shared traversal is what upsert matches against. Assert it
	// directly: a filtered listing here would silently fork the logical key.
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		all, err := listSpendLimitPolicies(ctx, sc)
		if err != nil {
			return err
		}
		disabled := 0
		for _, p := range all {
			if !p.Enabled {
				disabled++
			}
		}
		if len(all) != 6 || disabled != 4 {
			t.Fatalf("shared listing = %d rows (%d disabled), want 6 rows with 4 disabled", len(all), disabled)
		}
		return nil
	}); err != nil {
		t.Fatalf("shared listing: %v", err)
	}
}

// TestSpendLimitDisabledGroupCapSkipsAuthorityLookup proves a disabled group cap
// never reaches the group-authority read. Both failure modes are exercised
// through their observable consequence: while the row is enabled the malformed
// or absent group reference fails the whole effective view; once disabled the
// same view succeeds, which is only possible if no lookup was attempted.
func TestSpendLimitDisabledGroupCapSkipsAuthorityLookup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scopeKey func(t *testing.T, st store.Store, tenant model.TenantID) string
	}{
		{
			name:     "malformed group reference",
			scopeKey: func(*testing.T, store.Store, model.TenantID) string { return "not-a-group-id" },
		},
		{
			name:     "inaccessible group reference",
			scopeKey: func(*testing.T, store.Store, model.TenantID) string { return model.NewID().String() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			ctx := context.Background()
			m.clock = spendLimitClock{at: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
			key := tc.scopeKey(t, st, tenant)
			var planted model.ID
			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Create(ctx, model.Policy{
					Name: "planted group cap", Kind: policyKindSpendLimit, Enabled: true,
					Spec: map[string]any{
						"scope_type": "rbac_group", "scope_key": key,
						"amount_micro_usd": int64(1_000_000), "unlimited": false, "period": "monthly",
					},
				})
				planted = p.ID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			opts := SpendLimitEffectiveOptions{UserIDs: []string{"user:probe"}, Periods: []string{"monthly"}, Limit: 10}
			if _, err := m.SpendLimitEffective(ctx, tenant, opts); err == nil {
				t.Fatalf("enabled %s must fail the effective view; the control case proves the lookup happens", tc.name)
			}
			disableSpendLimitPolicy(t, m, tenant, spendLimitWireID(planted))
			res, err := m.SpendLimitEffective(ctx, tenant, opts)
			if err != nil {
				t.Fatalf("disabled %s still reached the group authority: %v", tc.name, err)
			}
			if len(res.Data) != 1 {
				t.Fatalf("effective rows = %+v, want exactly the requested principal", res.Data)
			}
			wantNoCap(t, res.Data[0])
			if len(res.Data[0].Groups) != 0 {
				t.Fatalf("groups = %v, want none: a disabled cap grants no group membership", res.Data[0].Groups)
			}
		})
	}
}

// TestSpendLimitDisabledGroupCapDoesNotMaskEnabledRow pins the seen-group
// tracking order. A disabled row for a group must not claim that group's key and
// suppress the lookup an enabled row for the SAME group still needs.
func TestSpendLimitDisabledGroupCapDoesNotMaskEnabledRow(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	m.clock = spendLimitClock{at: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	member := createCanonicalUser(t, st, "masked group member").ID
	g := createUserGroup(t, st, tenant, "masked", member)
	actor := "user:" + member.String()

	// Two rows for one logical key: the earlier (lower-id) one is disabled and is
	// deliberately the MORE restrictive of the two, so a resolver that still reads
	// it reports 90 instead of 250. Planted directly because upsert heals duplicates.
	var disabledID model.ID
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		spec := func(amount int64) map[string]any {
			return map[string]any{
				"scope_type": "rbac_group", "scope_key": g.ID.String(),
				"amount_micro_usd": amount, "unlimited": false, "period": "monthly",
			}
		}
		first, err := sc.Policies().Create(ctx, model.Policy{Name: "stale group cap", Kind: policyKindSpendLimit, Enabled: true, Spec: spec(900_000)})
		if err != nil {
			return err
		}
		disabledID = first.ID
		_, err = sc.Policies().Create(ctx, model.Policy{Name: "live group cap", Kind: policyKindSpendLimit, Enabled: true, Spec: spec(2_500_000)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	disableSpendLimitPolicy(t, m, tenant, spendLimitWireID(disabledID))

	row := effectiveRow(t, m, tenant, actor, "monthly")
	wantCap(t, row, "rbac_group", g.ID.String(), "250")
	if len(row.Groups) != 1 || row.Groups[0] != g.ID.String() {
		t.Fatalf("groups = %v, want the group the ENABLED cap still resolves", row.Groups)
	}
}

// TestSpendLimitDisabledUserCapNotImplicitlyEnumerated pins the effective
// principal population: a disabled cap is not, on its own, a reason to list a
// principal, while explicit user_ids and historical-spend discovery are intact.
func TestSpendLimitDisabledUserCapNotImplicitlyEnumerated(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	m.clock = spendLimitClock{at: now}

	ghost, _, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:ghost", "400", "monthly"), "user:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:live", "400", "monthly"), "user:admin"); err != nil {
		t.Fatal(err)
	}
	cost := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 1_000_000, now)
	cost.Actor = "user:spender"
	m.ingest(t, tenant, cost)
	disableSpendLimitPolicy(t, m, tenant, ghost.ID)

	res, err := m.SpendLimitEffective(ctx, tenant, SpendLimitEffectiveOptions{Periods: []string{"monthly"}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, row := range res.Data {
		listed[row.Actor.UserID] = true
	}
	if listed["user:ghost"] {
		t.Fatalf("implicit enumeration listed user:ghost from a disabled cap alone: %+v", res.Data)
	}
	if !listed["user:live"] || !listed["user:spender"] {
		t.Fatalf("implicit enumeration = %v, want the enabled cap and the historical spender", listed)
	}
	// Explicitly requesting the principal still works and reports no cap.
	wantNoCap(t, effectiveRow(t, m, tenant, "user:ghost", "monthly"))
}

// TestSpendLimitUpsertReactivatesLowestIDRow_CrossBackend pins the create-or-
// replace identity across enabled and disabled rows, and — in the same test —
// that SES1 changed no response or audit shape. Revision 1's additive `enabled`
// field on this shared object was withdrawn because this object IS the
// Anthropic-compatible gateway response, so the exact key sets are asserted.
func TestSpendLimitUpsertReactivatesLowestIDRow_CrossBackend(t *testing.T) {
	for name, cfg := range spendLimitEnabledBackends(t) {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			m, _, tenant, _ := openFinCfg(t, cfg(t))
			runUpsertReactivation(t, m, tenant)
		})
	}
}

func runUpsertReactivation(t *testing.T, m *Module, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	m.clock = spendLimitClock{at: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}

	first, created, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:u1", "500", "monthly"), "user:admin")
	if err != nil || !created {
		t.Fatalf("create = %+v created=%v err=%v", first, created, err)
	}
	// Spend ABOVE the cap before disabling, so "disabled stops enforcing" is a real
	// discriminator rather than a cap that happened to have nothing to enforce.
	overCap := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 6_000_000, m.clock.Now().Time())
	overCap.Actor = "user:u1"
	m.ingest(t, tenant, overCap)
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", nil); err != nil || chk.Allowed {
		t.Fatalf("control: enabled cap must deny before the row is disabled: %+v err=%v", chk, err)
	}
	// Unrelated rows so the target is not the only — nor the newest — row.
	if _, _, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:u2", "1000", "monthly"), "user:admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:u1", "1000", "daily"), "user:admin"); err != nil {
		t.Fatal(err)
	}
	disableSpendLimitPolicy(t, m, tenant, first.ID)
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", nil); err != nil || !chk.Allowed {
		t.Fatalf("disabled monthly cap still enforced: %+v err=%v", chk, err)
	}

	again, created, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:u1", "400", "monthly"), "user:admin")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatalf("upsert created a NEW row; the disabled lowest-id row must be reactivated in place")
	}
	if again.ID != first.ID {
		t.Fatalf("upsert id = %s, want the original %s", again.ID, first.ID)
	}
	if again.Amount == nil || *again.Amount != "400" {
		t.Fatalf("upsert amount = %v, want the replacing spec", again.Amount)
	}
	// Reactivated in place: the same id governs again against the same spend.
	chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", nil)
	if err != nil || chk.Allowed || chk.SpendLimitID != first.ID {
		t.Fatalf("reactivated cap not enforced under the original id: %+v err=%v", chk, err)
	}

	// No response shape change: the exact JSON key set of the shared gateway
	// object, and of the audit before/after snapshots, is unchanged.
	wantKeys := []string{"type", "id", "created_at", "updated_at", "scope", "amount", "currency", "period"}
	assertExactJSONKeys(t, again, wantKeys, "SpendLimit response")
	audit, err := m.SpendLimitAudit(ctx, tenant, 10)
	if err != nil || len(audit.Data) == 0 {
		t.Fatalf("audit = %+v err=%v", audit, err)
	}
	latest := audit.Data[0]
	if latest.Action != "update" || latest.SpendLimitID != first.ID || latest.Before == nil || latest.After == nil {
		t.Fatalf("reactivation audit = %+v, want an update with both snapshots", latest)
	}
	assertExactJSONKeys(t, *latest.Before, wantKeys, "audit before snapshot")
	assertExactJSONKeys(t, *latest.After, wantKeys, "audit after snapshot")
	if latest.Before.ID != first.ID || latest.After.ID != first.ID {
		t.Fatalf("audit snapshots reference %s/%s, want %s", latest.Before.ID, latest.After.ID, first.ID)
	}
}

// assertExactJSONKeys fails on any added or removed top-level key.
func assertExactJSONKeys(t *testing.T, v any, want []string, what string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", what, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s keys = %d (%s), want exactly %v", what, len(got), raw, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("%s is missing %q: %s", what, k, raw)
		}
	}
	if _, ok := got["enabled"]; ok {
		t.Fatalf("%s carries an `enabled` field; revision 2 withdrew it from this shared gateway object: %s", what, raw)
	}
}

// TestSpendLimitDisabledRowIsTenantIsolated confirms disabling in one tenant has
// no effect on an identically shaped policy in another.
func TestSpendLimitDisabledRowIsTenantIsolated(t *testing.T) {
	m, st, tenantA, _ := newFin(t)
	ctx := context.Background()
	m.clock = spendLimitClock{at: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	slug := "other-" + uniqueSlugSuffix(t)
	var tenantB model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenantB = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision second tenant: %v", err)
	}
	rowA, _, err := m.SpendLimitUpsert(ctx, tenantA, userLimit("user:shared", "100", "monthly"), "user:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SpendLimitUpsert(ctx, tenantB, userLimit("user:shared", "100", "monthly"), "user:admin"); err != nil {
		t.Fatal(err)
	}
	disableSpendLimitPolicy(t, m, tenantA, rowA.ID)

	wantNoCap(t, effectiveRow(t, m, tenantA, "user:shared", "monthly"))
	wantCap(t, effectiveRow(t, m, tenantB, "user:shared", "monthly"), "user", "", "100")
	if page, err := m.SpendLimitList(ctx, tenantB, 100, "", ""); err != nil || len(page.Data) != 1 {
		t.Fatalf("tenant B list = %+v err=%v, want its own single row", page, err)
	}
}

// TestSpendLimitUpsertReactivatesAcrossPages_CrossBackend is the cross-page
// case. listSpendLimitPolicies pages at listCap, which is the generic repo's
// maxLimit (1000) with the implicit `id ASC` order, so a duplicate beyond the
// first page is only reachable if the traversal really follows its cursor.
//
// The fixture is built so a single-page traversal gives a WRONG answer rather
// than a slower one: the lowest-id canonical row for the logical key is
// DISABLED and sits first, 1000 non-matching rows fill the rest of page one,
// and the ENABLED duplicate of the same scope and period is the highest id, so
// it lands on page two with the last filler. A pager that stopped at page one would find only
// the disabled row, leave the duplicate alive, and still look green on every
// other assertion — which is exactly why this case exists.
//
// Seeding is one transaction and the ids are asserted, not assumed: they are
// UUIDv7, so creation order is id order, and the test proves that before it
// relies on it.
func TestSpendLimitUpsertReactivatesAcrossPages_CrossBackend(t *testing.T) {
	for name, cfg := range spendLimitEnabledBackends(t) {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			m, _, tenant, _ := openFinCfg(t, cfg(t))
			runCrossPageReactivation(t, m, tenant)
		})
	}
}

// spendLimitPageFillers is the number of non-matching rows planted between the
// two rows of the logical key. With the canonical row first and the duplicate
// last it puts exactly listCap rows on page one and the duplicate on page two.
const spendLimitPageFillers = listCap

func runCrossPageReactivation(t *testing.T, m *Module, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	m.clock = spendLimitClock{at: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}

	const actor = "user:crosspage"
	keySpec := func(amount int64) map[string]any {
		return map[string]any{
			"scope_type": "user", "scope_key": actor,
			"amount_micro_usd": amount, "unlimited": false, "period": "monthly",
		}
	}

	var canonicalID, duplicateID model.ID
	seedStart := time.Now()
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// 1. The canonical row: lowest id, and disabled.
		canonical, err := sc.Policies().Create(ctx, model.Policy{
			Name: "crosspage canonical", Kind: policyKindSpendLimit, Enabled: true, Spec: keySpec(9_000_000),
		})
		if err != nil {
			return err
		}
		canonicalID = canonical.ID
		canonical.Enabled = false
		if _, err := sc.Policies().Update(ctx, canonical); err != nil {
			return err
		}
		// 2. listCap non-matching rows, so the canonical row plus the fillers
		//    exactly fill page one. They are real spend_limit policies, so they
		//    are qualifying rows for the shared listing and cannot be skipped by
		//    the kind filter.
		for i := 0; i < spendLimitPageFillers; i++ {
			if _, err := sc.Policies().Create(ctx, model.Policy{
				Name: "crosspage filler", Kind: policyKindSpendLimit, Enabled: true,
				Spec: map[string]any{
					"scope_type": "user", "scope_key": "user:filler-" + strconv.Itoa(i),
					"amount_micro_usd": int64(1_000_000), "unlimited": false, "period": "monthly",
				},
			}); err != nil {
				return err
			}
		}
		// 3. The enabled duplicate of the SAME logical key: highest id, page two.
		duplicate, err := sc.Policies().Create(ctx, model.Policy{
			Name: "crosspage duplicate", Kind: policyKindSpendLimit, Enabled: true, Spec: keySpec(2_000_000),
		})
		if err != nil {
			return err
		}
		duplicateID = duplicate.ID
		return nil
	}); err != nil {
		t.Fatalf("seed cross-page fixture: %v", err)
	}
	t.Logf("seeded %d qualifying spend_limit rows in %s", spendLimitPageFillers+2, time.Since(seedStart).Round(time.Millisecond))

	// The fixture's premises, asserted rather than assumed.
	if canonicalID >= duplicateID {
		t.Fatalf("fixture premise broken: canonical id %s must sort below duplicate id %s", canonicalID, duplicateID)
	}
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		firstPage, page, err := sc.Policies().List(ctx, model.Query{
			Filters: []model.Filter{eq("kind", policyKindSpendLimit)}, Limit: listCap,
		})
		if err != nil {
			return err
		}
		if len(firstPage) != listCap || !page.HasMore {
			t.Fatalf("page one = %d rows hasMore=%v, want exactly listCap=%d with more", len(firstPage), page.HasMore, listCap)
		}
		for _, p := range firstPage {
			if p.ID == duplicateID {
				t.Fatalf("fixture premise broken: the enabled duplicate is on page one, so this case would not test traversal")
			}
		}
		if firstPage[0].ID != canonicalID || firstPage[0].Enabled {
			t.Fatalf("page one row 0 = %s enabled=%v, want the DISABLED canonical %s", firstPage[0].ID, firstPage[0].Enabled, canonicalID)
		}
		all, err := listSpendLimitPolicies(ctx, sc)
		if err != nil {
			return err
		}
		if len(all) != spendLimitPageFillers+2 {
			t.Fatalf("shared traversal = %d rows, want %d across both pages", len(all), spendLimitPageFillers+2)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify fixture: %v", err)
	}

	// Spend 300 cents, above the enabled duplicate's 200-cent cap, so enforcement
	// has something to decide. While the canonical row is disabled the page-two
	// duplicate is the only enabled row for the key, so it is what governs — the
	// control that the row this upsert must reach is live beforehand.
	cost := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 3_000_000, m.clock.Now().Time())
	cost.Actor = actor
	m.ingest(t, tenant, cost)
	if chk, err := m.CheckSpendLimit(ctx, tenant, actor, nil); err != nil || chk.Allowed || chk.SpendLimitID != spendLimitWireID(duplicateID) {
		t.Fatalf("control: page-two enabled duplicate must govern before upsert: %+v err=%v", chk, err)
	}

	// Create-or-replace through the real module method.
	out, created, err := m.SpendLimitUpsert(ctx, tenant, userLimit(actor, "250", "monthly"), "user:admin")
	if err != nil {
		t.Fatalf("upsert across pages: %v", err)
	}
	if created {
		t.Fatalf("upsert created a NEW row; the disabled lowest-id row on page one must be reactivated in place")
	}
	if out.ID != spendLimitWireID(canonicalID) {
		t.Fatalf("upsert returned %s, want the lowest-id canonical %s", out.ID, spendLimitWireID(canonicalID))
	}
	if out.Amount == nil || *out.Amount != "250" {
		t.Fatalf("upsert amount = %v, want the replacing spec", out.Amount)
	}

	// Duplicate behavior is preserved ACROSS the page boundary: the page-two row
	// is healed away, and exactly one row holds the logical key.
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Policies().Get(ctx, duplicateID); !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("page-two duplicate %s survived the heal (err=%v); the traversal stopped at page one", duplicateID, err)
		}
		survivor, err := sc.Policies().Get(ctx, canonicalID)
		if err != nil {
			return err
		}
		if !survivor.Enabled {
			return fmt.Errorf("canonical row %s was not reactivated", canonicalID)
		}
		all, err := listSpendLimitPolicies(ctx, sc)
		if err != nil {
			return err
		}
		matching := 0
		for _, p := range all {
			if s := parseStoredSpendLimit(p); s.ScopeType == "user" && s.ScopeKey == actor && s.Period == "monthly" {
				matching++
			}
		}
		if matching != 1 {
			return fmt.Errorf("logical key holds %d rows after heal, want exactly 1", matching)
		}
		if len(all) != spendLimitPageFillers+1 {
			return fmt.Errorf("total qualifying rows = %d, want %d (one duplicate removed)", len(all), spendLimitPageFillers+1)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The reactivated row now governs the same spend, at its replaced 250-cent
	// amount and under its ORIGINAL id — not the healed duplicate's.
	if chk, err := m.CheckSpendLimit(ctx, tenant, actor, nil); err != nil || chk.Allowed || chk.SpendLimitID != spendLimitWireID(canonicalID) {
		t.Fatalf("reactivated cap = %+v err=%v, want deny under %s", chk, err, spendLimitWireID(canonicalID))
	}

	// Response and audit shapes are unchanged by the cross-page path too.
	wantKeys := []string{"type", "id", "created_at", "updated_at", "scope", "amount", "currency", "period"}
	assertExactJSONKeys(t, out, wantKeys, "cross-page SpendLimit response")
	audit, err := m.SpendLimitAudit(ctx, tenant, 5)
	if err != nil || len(audit.Data) == 0 {
		t.Fatalf("audit = %+v err=%v", audit, err)
	}
	latest := audit.Data[0]
	if latest.Action != "update" || latest.SpendLimitID != out.ID || latest.Before == nil || latest.After == nil {
		t.Fatalf("cross-page audit = %+v, want an update on the canonical id with both snapshots", latest)
	}
	assertExactJSONKeys(t, *latest.Before, wantKeys, "cross-page audit before snapshot")
	assertExactJSONKeys(t, *latest.After, wantKeys, "cross-page audit after snapshot")
}
