// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

const adoptedGrantSrc = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"payments-bots" };`

func TestAdoptBundlePolicyPersistsRevisionFreshnessAndAudit(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-adopt")
	in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)

	report, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !report.Adopted || report.Reason != "adopted" || report.SurfaceRevision != 1 {
		t.Fatalf("adopt report = %+v", report)
	}
	rows := ddilRevisionRows(t, h.st, tenant)
	if len(rows) != 1 || rows[0].String("content") != adoptedGrantSrc || !rows[0].Bool("active") {
		t.Fatalf("cedar-ddil rows = %+v", rows)
	}
	fresh, found, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if err != nil || !found {
		t.Fatalf("freshness: found=%t err=%v", found, err)
	}
	if !fresh.RefreshedAt.Equal(baseTime) || fresh.MaxStaleness != 24*time.Hour ||
		fresh.AdoptedRevision != in.Revision || !fresh.AdoptedCreatedAt.Equal(baseTime) {
		t.Fatalf("freshness = %+v", fresh)
	}
	if !auditHasAction(t, h.st, tenant, "governance.ddil.policy_adopt") {
		t.Fatal("adoption audit event was not appended")
	}
}

func TestAdoptBundlePolicyIsIdempotentWithoutRestamp(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-idempotent")
	in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime); err != nil {
		t.Fatal(err)
	}
	before, _, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	report, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime.Add(10*time.Hour))
	if err != nil {
		t.Fatalf("re-adopt: %v", err)
	}
	if report.Adopted || report.Reason != "already adopted" || report.SurfaceRevision != 0 {
		t.Fatalf("re-adopt report = %+v", report)
	}
	after, _, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFreshness(before, after) || len(ddilRevisionRows(t, h.st, tenant)) != 1 {
		t.Fatalf("idempotent adoption changed state: before=%+v after=%+v rows=%d", before, after, len(ddilRevisionRows(t, h.st, tenant)))
	}
}

func TestAdoptBundlePolicySignedTupleMatrix(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		createdAt  time.Time
		bound      time.Duration
		wantMode   string
		wantEpoch  int64
		wantRows   int
		wantReason string
	}{
		{
			name: "exact replay", source: adoptedGrantSrc, createdAt: baseTime,
			bound: 24 * time.Hour, wantMode: "noop", wantReason: "already adopted",
		},
		{
			name: "same revision and time but different signed bound", source: adoptedGrantSrc,
			createdAt: baseTime, bound: 48 * time.Hour, wantMode: "refuse",
			wantReason: "replay/rollback refused",
		},
		{
			name: "same revision older", source: adoptedGrantSrc,
			createdAt: baseTime.Add(-time.Nanosecond), bound: 24 * time.Hour,
			wantMode: "refuse", wantReason: "replay/rollback refused",
		},
		{
			name: "different revision equal time", source: adoptedGrantSrc + "\n",
			createdAt: baseTime, bound: 24 * time.Hour, wantMode: "refuse",
			wantReason: "replay/rollback refused",
		},
		{
			name: "different revision older", source: adoptedGrantSrc + "\n",
			createdAt: baseTime.Add(-time.Nanosecond), bound: 24 * time.Hour,
			wantMode: "refuse", wantReason: "replay/rollback refused",
		},
		{
			name: "same revision newer", source: adoptedGrantSrc,
			createdAt: baseTime.Add(time.Nanosecond), bound: 24 * time.Hour,
			wantMode: "reattest", wantEpoch: 1, wantReason: "re-attest",
		},
		{
			name: "same revision newer and new signed bound", source: adoptedGrantSrc,
			createdAt: baseTime.Add(time.Hour), bound: 48 * time.Hour,
			wantMode: "reattest", wantEpoch: 1, wantReason: "re-attest",
		},
		{
			name: "different revision newer", source: adoptedGrantSrc + "\n",
			createdAt: baseTime.Add(time.Hour), bound: 48 * time.Hour,
			wantMode: "full", wantEpoch: 1, wantRows: 1, wantReason: "adopted",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := h.createOrg(h.adminLogin(), "ddil-tuple")
			initial := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
			if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, initial, baseTime.Add(90*time.Hour)); err != nil {
				t.Fatal(err)
			}
			beforeEpoch := ddilAuthorizationEpoch(t, h.st, tenant).Version
			beforeRows := len(ddilRevisionRows(t, h.st, tenant))
			beforeAudits := ddilAdoptionAuditCount(t, h.st, tenant)
			beforeFresh, _, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
			if err != nil {
				t.Fatal(err)
			}

			candidate := ddilAdoption(tc.source, tc.createdAt, tc.bound)
			report, adoptErr := governance.AdoptBundlePolicy(
				context.Background(), h.st, tenant, candidate, baseTime.Add(200*time.Hour),
			)
			afterEpoch := ddilAuthorizationEpoch(t, h.st, tenant).Version
			afterRows := len(ddilRevisionRows(t, h.st, tenant))
			afterAudits := ddilAdoptionAuditCount(t, h.st, tenant)
			afterFresh, _, freshErr := governance.PolicyFreshness(context.Background(), h.st, tenant)
			if freshErr != nil {
				t.Fatal(freshErr)
			}

			if tc.wantMode == "refuse" {
				if adoptErr == nil || !strings.Contains(adoptErr.Error(), tc.wantReason) {
					t.Fatalf("error = %v, want %q", adoptErr, tc.wantReason)
				}
			} else {
				if adoptErr != nil {
					t.Fatalf("adopt: %v", adoptErr)
				}
				if !strings.Contains(report.Reason, tc.wantReason) {
					t.Fatalf("report = %+v, want reason containing %q", report, tc.wantReason)
				}
				wantAdopted := tc.wantMode != "noop"
				if report.Adopted != wantAdopted {
					t.Fatalf("report.Adopted = %t, want %t", report.Adopted, wantAdopted)
				}
			}
			if delta := afterEpoch - beforeEpoch; delta != tc.wantEpoch {
				t.Fatalf("epoch delta = %d, want %d", delta, tc.wantEpoch)
			}
			if delta := afterRows - beforeRows; delta != tc.wantRows {
				t.Fatalf("revision-row delta = %d, want %d", delta, tc.wantRows)
			}
			if delta := afterAudits - beforeAudits; int64(delta) != tc.wantEpoch {
				t.Fatalf("adoption-audit delta = %d, want %d", delta, tc.wantEpoch)
			}
			if tc.wantMode == "noop" || tc.wantMode == "refuse" {
				if !sameFreshness(beforeFresh, afterFresh) {
					t.Fatalf("zero-write branch changed freshness: before=%+v after=%+v", beforeFresh, afterFresh)
				}
			} else if !afterFresh.RefreshedAt.Equal(candidate.BundleCreatedAt) ||
				!afterFresh.AdoptedCreatedAt.Equal(candidate.BundleCreatedAt) ||
				afterFresh.AdoptedRevision != candidate.Revision ||
				afterFresh.MaxStaleness != candidate.MaxStaleness {
				t.Fatalf("signed tuple not persisted verbatim: got=%+v candidate=%+v", afterFresh, candidate)
			}
		})
	}
}

func TestAdoptBundlePolicyDifferentSignedBoundAtSameTimeIsNotNoop(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-bound-replay")
	initial := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, initial, baseTime); err != nil {
		t.Fatal(err)
	}
	before := ddilAuthorizationEpoch(t, h.st, tenant).Version
	candidate := ddilAdoption(adoptedGrantSrc, baseTime, 48*time.Hour)
	report, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, candidate, baseTime.Add(7*24*time.Hour))
	if err == nil || !strings.Contains(err.Error(), "replay/rollback refused") {
		t.Fatalf("changed signed bound at equal created_at: report=%+v err=%v", report, err)
	}
	if got := ddilAuthorizationEpoch(t, h.st, tenant).Version; got != before {
		t.Fatalf("changed-bound replay bumped epoch: before=%d after=%d", before, got)
	}
	fresh, _, _ := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if fresh.MaxStaleness != 24*time.Hour {
		t.Fatalf("changed-bound replay rewrote freshness: %+v", fresh)
	}
}

func TestAdoptBundlePolicyRefusesReplayOrRollback(t *testing.T) {
	for _, delta := range []time.Duration{0, -time.Hour} {
		t.Run(delta.String(), func(t *testing.T) {
			h := newHarness(t)
			tenant := h.createOrg(h.adminLogin(), "ddil-replay")
			first := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
			if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, first, baseTime); err != nil {
				t.Fatal(err)
			}
			second := ddilAdoption(adoptedGrantSrc+"\n", baseTime.Add(delta), 0)
			if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, second, baseTime); err == nil || !strings.Contains(err.Error(), "replay/rollback refused") {
				t.Fatalf("replay error = %v", err)
			}
			fresh, _, _ := governance.PolicyFreshness(context.Background(), h.st, tenant)
			if fresh.AdoptedRevision != first.Revision || len(ddilRevisionRows(t, h.st, tenant)) != 1 {
				t.Fatalf("refused replay wrote state: freshness=%+v rows=%d", fresh, len(ddilRevisionRows(t, h.st, tenant)))
			}
		})
	}
}

func TestAdoptBundlePolicyRefusesMismatchAndBrokenUnionWithoutPersist(t *testing.T) {
	t.Run("revision mismatch", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-mismatch")
		in := ddilAdoption(adoptedGrantSrc, baseTime, 0)
		in.Revision = "sha256:" + strings.Repeat("0", 64)
		if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime); err == nil || !strings.Contains(err.Error(), "revision/bytes mismatch") {
			t.Fatalf("mismatch error = %v", err)
		}
		assertNoDDILAdoption(t, h.st, tenant)
	})

	for _, surface := range []string{"cedar", "cedar-managed"} {
		t.Run("broken "+surface+" prospective union", func(t *testing.T) {
			h := newHarness(t)
			tenant := h.createOrg(h.adminLogin(), "ddil-union")
			// Bypass authoring to model a corrupt/legacy active surface. The carried
			// snapshot compiles alone, but adoption must compile every live surface too.
			seedDDILSurface(t, h.st, tenant, surface, "not cedar")
			in := ddilAdoption(adoptedGrantSrc, baseTime, 0)
			if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime); err == nil || !strings.Contains(err.Error(), "active Cedar union") {
				t.Fatalf("%s union error = %v", surface, err)
			}
			assertNoDDILAdoption(t, h.st, tenant)
		})
	}
}

func TestAdoptBundlePolicyBoundSetThenCleared(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-bound")
	first := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, first, baseTime); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if fresh.MaxStaleness != 24*time.Hour {
		t.Fatalf("set bound = %s", fresh.MaxStaleness)
	}
	second := ddilAdoption(adoptedGrantSrc+"\n", baseTime.Add(time.Hour), 0)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, second, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ = governance.PolicyFreshness(context.Background(), h.st, tenant)
	if fresh.MaxStaleness != 0 || fresh.AdoptedRevision != second.Revision {
		t.Fatalf("cleared bound freshness = %+v", fresh)
	}
}

func TestReloadActivePDPDoesNotResetDurableFreshness(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-boot-freshness")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("adopted cedar-ddil policy must grant before expiry, got %v (%s)", sd.Effect, sd.Reason)
	}

	h.clk.advance(24*time.Hour + time.Second)
	freshModule := governance.New(governance.WithClock(h.clk))
	freshModule.UseData(api.NewModuleData(h.st))
	if err := freshModule.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	res := auth.ResourceFor("agent:write")
	res.ID = member.ID.String()
	sd, err := freshModule.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal: viewer, Permission: "agent:write", Tenant: tenant, Resource: res,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sd.Effect != auth.EffectAbstain {
		t.Fatalf("boot must restore expired durable clock, got %v (%s)", sd.Effect, sd.Reason)
	}
	fresh, _, _ := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if !fresh.RefreshedAt.Equal(baseTime) {
		t.Fatalf("boot re-stamped durable freshness: got %s want %s", fresh.RefreshedAt, baseTime)
	}
}

func TestAdoptBundlePolicyIsStaleOnArrival(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-stale-arrival")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	createdAt := baseTime.Add(-25 * time.Hour)
	in := ddilAdoption(adoptedGrantSrc, createdAt, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, in, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectAbstain {
		t.Fatalf("25h-old policy with 24h bound must be stale on arrival, got %v (%s)", sd.Effect, sd.Reason)
	}
	fresh, found, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if err != nil || !found || !fresh.RefreshedAt.Equal(createdAt) {
		t.Fatalf("stale adoption freshness: found=%t rec=%+v err=%v", found, fresh, err)
	}
}

func TestAdoptBundlePolicyUsesOneMutateAndNoViewWithMaxConnsOne(t *testing.T) {
	st, tenant := newDDILTestStore(t, 1)
	probe := &ddilNoViewStore{Store: st}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	report, err := governance.AdoptBundlePolicy(
		ctx, probe, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour), baseTime.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("single-connection adoption: %v", err)
	}
	if !report.Adopted || probe.views != 0 || probe.mutates != 1 {
		t.Fatalf("report=%+v views=%d mutates=%d, want adopted/0/1", report, probe.views, probe.mutates)
	}
}

func TestAdoptBundlePolicyAbsentAndPartialDurableState(t *testing.T) {
	t.Run("local freshness only is no prior adoption", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-local-fresh")
		seedDDILFreshness(t, h.st, tenant, governance.FreshnessRecord{RefreshedAt: baseTime.Add(-time.Hour)})
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		report, err := governance.AdoptBundlePolicy(
			context.Background(), h.st, tenant,
			ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour), baseTime.Add(30*24*time.Hour),
		)
		if err != nil || !report.Adopted {
			t.Fatalf("first signed adoption over local freshness: report=%+v err=%v", report, err)
		}
		if got := ddilAuthorizationEpoch(t, h.st, tenant).Version; got != before+1 {
			t.Fatalf("epoch = %d, want %d", got, before+1)
		}
	})

	tests := []struct {
		name string
		seed func(*testing.T, store.Store, model.TenantID)
	}{
		{
			name: "adopted policy without freshness anchors",
			seed: func(t *testing.T, st store.Store, tenant model.TenantID) {
				seedDDILRevision(t, st, tenant, adoptedGrantSrc)
			},
		},
		{
			name: "freshness anchors without adopted policy",
			seed: func(t *testing.T, st store.Store, tenant model.TenantID) {
				in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
				seedDDILFreshness(t, st, tenant, governance.FreshnessRecord{
					RefreshedAt: in.BundleCreatedAt, MaxStaleness: in.MaxStaleness,
					AdoptedRevision: in.Revision, AdoptedCreatedAt: in.BundleCreatedAt,
				})
			},
		},
		{
			name: "partial freshness anchor",
			seed: func(t *testing.T, st store.Store, tenant model.TenantID) {
				in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
				seedDDILFreshness(t, st, tenant, governance.FreshnessRecord{
					RefreshedAt: in.BundleCreatedAt, MaxStaleness: in.MaxStaleness,
					AdoptedRevision: in.Revision,
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := h.createOrg(h.adminLogin(), "ddil-partial")
			tc.seed(t, h.st, tenant)
			beforeEpoch := ddilAuthorizationEpoch(t, h.st, tenant).Version
			beforeRows := len(ddilRevisionRows(t, h.st, tenant))
			beforeAudits := ddilAdoptionAuditCount(t, h.st, tenant)
			_, err := governance.AdoptBundlePolicy(
				context.Background(), h.st, tenant,
				ddilAdoption(adoptedGrantSrc+"\n", baseTime.Add(time.Hour), 48*time.Hour),
				baseTime.Add(90*24*time.Hour),
			)
			if err == nil || !strings.Contains(err.Error(), "inconsistent DDIL durable adoption state") {
				t.Fatalf("partial-state error = %v", err)
			}
			if got := ddilAuthorizationEpoch(t, h.st, tenant).Version; got != beforeEpoch {
				t.Fatalf("partial state bumped epoch: before=%d after=%d", beforeEpoch, got)
			}
			if got := len(ddilRevisionRows(t, h.st, tenant)); got != beforeRows {
				t.Fatalf("partial state wrote revision: before=%d after=%d", beforeRows, got)
			}
			if got := ddilAdoptionAuditCount(t, h.st, tenant); got != beforeAudits {
				t.Fatalf("partial state wrote audit: before=%d after=%d", beforeAudits, got)
			}
		})
	}
}

func TestAdoptBundlePolicyRejectsDivergentDurableAnchorsBeforeExactReplay(t *testing.T) {
	hashCandidate := ddilAdoption(adoptedGrantSrc+"\n", baseTime, 24*time.Hour)
	clockCandidate := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	tests := []struct {
		name      string
		active    string
		freshness governance.FreshnessRecord
		candidate governance.PolicyAdoption
		wantError string
	}{
		{
			name:   "active content hash differs from revision anchor",
			active: adoptedGrantSrc,
			freshness: governance.FreshnessRecord{
				RefreshedAt: hashCandidate.BundleCreatedAt, MaxStaleness: hashCandidate.MaxStaleness,
				AdoptedRevision: hashCandidate.Revision, AdoptedCreatedAt: hashCandidate.BundleCreatedAt,
			},
			candidate: hashCandidate,
			wantError: "active adopted policy does not match its revision anchor",
		},
		{
			name:   "signed freshness clock differs from adopted created at",
			active: adoptedGrantSrc,
			freshness: governance.FreshnessRecord{
				RefreshedAt: clockCandidate.BundleCreatedAt.Add(time.Hour), MaxStaleness: clockCandidate.MaxStaleness,
				AdoptedRevision: clockCandidate.Revision, AdoptedCreatedAt: clockCandidate.BundleCreatedAt,
			},
			candidate: clockCandidate,
			wantError: "signed freshness clock does not equal adopted created_at",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := h.createOrg(h.adminLogin(), "ddil-divergent-anchor")
			seedDDILRevision(t, h.st, tenant, tc.active)
			seedDDILFreshness(t, h.st, tenant, tc.freshness)
			beforeEpoch := ddilAuthorizationEpoch(t, h.st, tenant).Version
			beforeRows := ddilRevisionRows(t, h.st, tenant)
			beforeAudits := ddilAdoptionAuditCount(t, h.st, tenant)
			beforeFresh, found, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
			if err != nil || !found {
				t.Fatalf("seeded freshness: found=%t err=%v", found, err)
			}

			report, err := governance.AdoptBundlePolicy(
				context.Background(), h.st, tenant, tc.candidate, baseTime.Add(30*24*time.Hour),
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("report=%+v error=%v, want %q", report, err, tc.wantError)
			}
			if got := ddilAuthorizationEpoch(t, h.st, tenant).Version; got != beforeEpoch {
				t.Fatalf("inconsistent exact replay bumped epoch: before=%d after=%d", beforeEpoch, got)
			}
			afterRows := ddilRevisionRows(t, h.st, tenant)
			if len(afterRows) != len(beforeRows) || afterRows[0].String("content") != beforeRows[0].String("content") {
				t.Fatalf("inconsistent exact replay changed revisions: before=%+v after=%+v", beforeRows, afterRows)
			}
			if got := ddilAdoptionAuditCount(t, h.st, tenant); got != beforeAudits {
				t.Fatalf("inconsistent exact replay appended audit: before=%d after=%d", beforeAudits, got)
			}
			afterFresh, found, freshErr := governance.PolicyFreshness(context.Background(), h.st, tenant)
			if freshErr != nil || !found || !sameFreshness(beforeFresh, afterFresh) {
				t.Fatalf("inconsistent exact replay changed freshness: before=%+v after=%+v found=%t err=%v", beforeFresh, afterFresh, found, freshErr)
			}
		})
	}
}

func TestAdoptBundlePolicyCapabilityLockCASCompileAndAuditFailuresAreAtomic(t *testing.T) {
	injected := errors.New("injected DDIL writer failure")

	t.Run("capability absent", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-cap-absent")
		wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
			return struct{ store.Scope }{Scope: sc}
		}}
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 0), baseTime)
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("error = %v, want ErrAuthorizationEpochUnavailable", err)
		}
		assertDDILZeroWrite(t, h.st, tenant, before)
	})

	t.Run("partial capabilities", func(t *testing.T) {
		cases := []struct {
			name string
			wrap func(store.Scope) store.Scope
		}{
			{name: "reader only", wrap: func(sc store.Scope) store.Scope {
				return &ddilReaderOnlyScope{Scope: sc, reader: sc.(store.AuthorizationEpochReader)}
			}},
			{name: "bumper only", wrap: func(sc store.Scope) store.Scope {
				return &ddilBumperOnlyScope{Scope: sc, bumper: sc.(store.AuthorizationEpochBumper)}
			}},
			{name: "epoch without locker", wrap: func(sc store.Scope) store.Scope {
				return &ddilEpochOnlyScope{Scope: sc, epochs: sc.(store.AuthorizationEpochStore)}
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newHarness(t)
				tenant := h.createOrg(h.adminLogin(), "ddil-cap-partial")
				wrapped := &ddilWrappingStore{Store: h.st, wrap: tc.wrap}
				before := ddilAuthorizationEpoch(t, h.st, tenant).Version
				_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 0), baseTime)
				if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
					t.Fatalf("error = %v, want ErrAuthorizationEpochUnavailable", err)
				}
				assertDDILZeroWrite(t, h.st, tenant, before)
			})
		}
	})

	t.Run("lock error", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-lock-error")
		var ports *ddilEpochPorts
		wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
			ports = newDDILEpochPorts(sc)
			ports.lockErr = injected
			return ports
		}}
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 0), baseTime)
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) || !strings.Contains(err.Error(), injected.Error()) {
			t.Fatalf("lock error = %v", err)
		}
		if ports == nil || ports.reads != 1 || ports.locks != 1 || ports.bumps != 0 {
			t.Fatalf("ports = %+v, want read/lock/bump 1/1/0", ports)
		}
		assertDDILZeroWrite(t, h.st, tenant, before)
	})

	t.Run("CAS error", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-cas-error")
		var ports *ddilEpochPorts
		wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
			ports = newDDILEpochPorts(sc)
			ports.bumpErr = injected
			return ports
		}}
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 0), baseTime)
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) || !strings.Contains(err.Error(), injected.Error()) {
			t.Fatalf("CAS error = %v", err)
		}
		if ports == nil || ports.reads != 2 || ports.locks != 1 || ports.bumps != 1 {
			t.Fatalf("ports = %+v, want read/lock/bump 2/1/1", ports)
		}
		assertDDILZeroWrite(t, h.st, tenant, before)
	})

	t.Run("compile error after lock", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-compile-error")
		seedDDILSurface(t, h.st, tenant, "cedar", "not cedar")
		var ports *ddilEpochPorts
		wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
			ports = newDDILEpochPorts(sc)
			return ports
		}}
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 0), baseTime)
		if err == nil || !strings.Contains(err.Error(), "active Cedar union") {
			t.Fatalf("compile error = %v", err)
		}
		if ports == nil || ports.reads != 1 || ports.locks != 1 || ports.bumps != 0 {
			t.Fatalf("ports = %+v, want read/lock/bump 1/1/0", ports)
		}
		if got := ddilAuthorizationEpoch(t, h.st, tenant).Version; got != before {
			t.Fatalf("compile error bumped epoch: before=%d after=%d", before, got)
		}
		if got := len(ddilRevisionRows(t, h.st, tenant)); got != 0 {
			t.Fatalf("compile error wrote %d DDIL revisions", got)
		}
	})

	t.Run("audit error after bump", func(t *testing.T) {
		h := newHarness(t)
		tenant := h.createOrg(h.adminLogin(), "ddil-audit-error")
		var ports *ddilEpochPorts
		wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
			ports = newDDILEpochPorts(sc)
			ports.audit = ddilFailingAudit{AuditLog: sc.Audit(), err: injected}
			return ports
		}}
		before := ddilAuthorizationEpoch(t, h.st, tenant).Version
		_, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour), baseTime)
		if !errors.Is(err, injected) {
			t.Fatalf("audit error = %v", err)
		}
		if ports == nil || ports.reads != 2 || ports.locks != 1 || ports.bumps != 1 {
			t.Fatalf("ports = %+v, want read/lock/bump 2/1/1", ports)
		}
		assertDDILZeroWrite(t, h.st, tenant, before)
	})
}

func TestAdoptBundlePolicyEpochCASIsFirstWrite(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-write-order")
	var trace []string
	wrapped := &ddilWrappingStore{Store: h.st, wrap: func(sc store.Scope) store.Scope {
		ports := newDDILEpochPorts(sc)
		ports.trace = &trace
		ports.audit = ddilRecordingAudit{AuditLog: sc.Audit(), trace: &trace}
		ports.ext = map[model.Kind]store.GenericRepo{}
		for _, kind := range []model.Kind{"governance.policy_revision", "governance.policy_freshness"} {
			repo, err := sc.Ext(kind)
			if err != nil {
				t.Fatal(err)
			}
			ports.ext[kind] = ddilRecordingRepo{GenericRepo: repo, kind: kind, trace: &trace}
		}
		return ports
	}}
	report, err := governance.AdoptBundlePolicy(context.Background(), wrapped, tenant, ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour), baseTime)
	if err != nil || !report.Adopted {
		t.Fatalf("adopt: report=%+v err=%v", report, err)
	}
	lockAt := traceIndex(trace, "epoch-lock")
	firstRepoRead := traceIndexPrefix(trace, "repo-read:")
	bumpAt := traceIndex(trace, "epoch-bump")
	firstWrite := traceIndexPrefix(trace, "write:")
	if lockAt < 0 || firstRepoRead < 0 || lockAt >= firstRepoRead {
		t.Fatalf("lock must precede durable policy reads: %v", trace)
	}
	if bumpAt < 0 || firstWrite < 0 || bumpAt >= firstWrite {
		t.Fatalf("epoch bump must precede every data/audit write: %v", trace)
	}
	wantWrites := []string{
		"write:epoch", "write:governance.policy_revision:create",
		"write:governance.policy_freshness:create", "write:audit",
	}
	if got := traceWrites(trace); fmt.Sprint(got) != fmt.Sprint(wantWrites) {
		t.Fatalf("writes = %v, want %v; full trace=%v", got, wantWrites, trace)
	}
}

func TestAdoptBundlePolicyConcurrentSameSignedGenerationCommitsOnce(t *testing.T) {
	st, tenant := newDDILTestStore(t, 4)
	initial := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(context.Background(), st, tenant, initial, baseTime); err != nil {
		t.Fatal(err)
	}
	beforeEpoch := ddilAuthorizationEpoch(t, st, tenant).Version
	beforeRows := len(ddilRevisionRows(t, st, tenant))
	beforeAudits := ddilAdoptionAuditCount(t, st, tenant)
	candidates := []governance.PolicyAdoption{
		ddilAdoption(adoptedGrantSrc+"\n", baseTime.Add(time.Hour), 48*time.Hour),
		ddilAdoption(adoptedGrantSrc+"\n\n", baseTime.Add(time.Hour), 72*time.Hour),
	}
	type result struct {
		report governance.AdoptReport
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, len(candidates))
	var wg sync.WaitGroup
	for _, in := range candidates {
		in := in
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			report, err := governance.AdoptBundlePolicy(context.Background(), st, tenant, in, baseTime.Add(99*time.Hour))
			results <- result{report: report, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var adopted, refused int
	for got := range results {
		switch {
		case got.err == nil && got.report.Adopted:
			adopted++
		case got.err != nil && strings.Contains(got.err.Error(), "replay/rollback refused"):
			refused++
		default:
			t.Fatalf("unexpected concurrent outcome: report=%+v err=%v", got.report, got.err)
		}
	}
	if adopted != 1 || refused != 1 {
		t.Fatalf("concurrent outcomes adopted/refused = %d/%d, want 1/1", adopted, refused)
	}
	if delta := ddilAuthorizationEpoch(t, st, tenant).Version - beforeEpoch; delta != 1 {
		t.Fatalf("epoch delta = %d, want 1", delta)
	}
	if delta := len(ddilRevisionRows(t, st, tenant)) - beforeRows; delta != 1 {
		t.Fatalf("revision delta = %d, want 1", delta)
	}
	if delta := ddilAdoptionAuditCount(t, st, tenant) - beforeAudits; delta != 1 {
		t.Fatalf("audit delta = %d, want 1", delta)
	}
}

func ddilAdoption(source string, createdAt time.Time, bound time.Duration) governance.PolicyAdoption {
	snapshot := []byte(source)
	sum := sha256.Sum256(snapshot)
	return governance.PolicyAdoption{
		Snapshot: snapshot, Revision: "sha256:" + hex.EncodeToString(sum[:]),
		BundleCreatedAt: createdAt, MaxStaleness: bound, Actor: "ddil-test",
	}
}

func ddilRevisionRows(t *testing.T, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if rec.String("surface") == "cedar-ddil" {
				out = append(out, rec)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func auditHasAction(t *testing.T, st store.Store, tenant model.TenantID, action string) bool {
	t.Helper()
	found := false
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			found = found || ev.Action == action
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func assertNoDDILAdoption(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	if rows := ddilRevisionRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("unexpected cedar-ddil rows: %+v", rows)
	}
	if fresh, found, err := governance.PolicyFreshness(context.Background(), st, tenant); err != nil || found {
		t.Fatalf("unexpected freshness: found=%t rec=%+v err=%v", found, fresh, err)
	}
}

func sameFreshness(a, b governance.FreshnessRecord) bool {
	return a.RefreshedAt.Equal(b.RefreshedAt) && a.MaxStaleness == b.MaxStaleness &&
		a.AdoptedRevision == b.AdoptedRevision && a.AdoptedCreatedAt.Equal(b.AdoptedCreatedAt)
}

func newDDILTestStore(t *testing.T, maxConns int) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	m := governance.New()
	st, err := engine.Open(ctx, store.Config{
		Engine:   store.EngineSQLite,
		DSN:      filepath.Join(t.TempDir(), "ddil.sqlite"),
		Debug:    true,
		MaxConns: maxConns,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "DDIL epoch test", Slug: "ddil-epoch-test", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st, tenant
}

func ddilAuthorizationEpoch(t *testing.T, st store.Store, tenant model.TenantID) store.AuthorizationFactRef {
	t.Helper()
	var fact store.AuthorizationFactRef
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("tenant scope lacks authorization epoch reader")
		}
		var err error
		fact, err = reader.ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fact
}

func ddilAdoptionAuditCount(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	count := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "governance.ddil.policy_adopt" || ev.Action == "governance.ddil.policy_reattest" {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func seedDDILRevision(t *testing.T, st store.Store, tenant model.TenantID, content string) {
	t.Helper()
	seedDDILSurface(t, st, tenant, "cedar-ddil", content)
}

func seedDDILSurface(t *testing.T, st store.Store, tenant model.TenantID, surface, content string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"surface": surface, "revision": int64(1), "content": content,
			"author": "legacy-test", "validated": true, "active": true, "note": "",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedDDILFreshness(t *testing.T, st store.Store, tenant model.TenantID, in governance.FreshnessRecord) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_freshness"))
		if err != nil {
			return err
		}
		rec := model.Record{
			"refreshed_at":       model.NewTimestamp(in.RefreshedAt).String(),
			"max_staleness":      "",
			"adopted_revision":   in.AdoptedRevision,
			"adopted_created_at": nil,
		}
		if in.MaxStaleness > 0 {
			rec["max_staleness"] = in.MaxStaleness.String()
		}
		if !in.AdoptedCreatedAt.IsZero() {
			rec["adopted_created_at"] = model.NewTimestamp(in.AdoptedCreatedAt).String()
		}
		_, err = repo.Create(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDDILZeroWrite(t *testing.T, st store.Store, tenant model.TenantID, epoch int64) {
	t.Helper()
	if got := ddilAuthorizationEpoch(t, st, tenant).Version; got != epoch {
		t.Fatalf("epoch changed: before=%d after=%d", epoch, got)
	}
	assertNoDDILAdoption(t, st, tenant)
	if got := ddilAdoptionAuditCount(t, st, tenant); got != 0 {
		t.Fatalf("failure appended %d DDIL audit events", got)
	}
}

type ddilNoViewStore struct {
	store.Store
	views   int
	mutates int
}

func (s *ddilNoViewStore) View(
	context.Context,
	model.TenantID,
	func(store.Scope) error,
) error {
	s.views++
	return errors.New("unexpected Store.View during DDIL adoption")
}

func (s *ddilNoViewStore) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	s.mutates++
	return s.Store.Mutate(ctx, tenant, fn)
}

type ddilWrappingStore struct {
	store.Store
	wrap func(store.Scope) store.Scope
}

func (s *ddilWrappingStore) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		if s.wrap != nil {
			sc = s.wrap(sc)
		}
		return fn(sc)
	})
}

type ddilReaderOnlyScope struct {
	store.Scope
	reader store.AuthorizationEpochReader
}

func (s *ddilReaderOnlyScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	return s.reader.ReadAuthorizationEpoch(ctx)
}

type ddilBumperOnlyScope struct {
	store.Scope
	bumper store.AuthorizationEpochBumper
}

func (s *ddilBumperOnlyScope) BumpAuthorizationEpoch(
	ctx context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	return s.bumper.BumpAuthorizationEpoch(ctx, expected)
}

type ddilEpochOnlyScope struct {
	store.Scope
	epochs store.AuthorizationEpochStore
}

func (s *ddilEpochOnlyScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	return s.epochs.ReadAuthorizationEpoch(ctx)
}

func (s *ddilEpochOnlyScope) BumpAuthorizationEpoch(
	ctx context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	return s.epochs.BumpAuthorizationEpoch(ctx, expected)
}

type ddilEpochPorts struct {
	store.Scope
	epochs    store.AuthorizationEpochStore
	authority store.AuthoritySnapshotLocker
	audit     store.AuditLog
	ext       map[model.Kind]store.GenericRepo
	readErr   error
	lockErr   error
	bumpErr   error
	reads     int
	locks     int
	bumps     int
	trace     *[]string
}

func newDDILEpochPorts(sc store.Scope) *ddilEpochPorts {
	return &ddilEpochPorts{
		Scope: sc, epochs: sc.(store.AuthorizationEpochStore),
		authority: sc.(store.AuthoritySnapshotLocker),
	}
}

func (s *ddilEpochPorts) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	if s.trace != nil {
		*s.trace = append(*s.trace, "epoch-read")
	}
	if s.readErr != nil {
		return store.AuthorizationFactRef{}, s.readErr
	}
	return s.epochs.ReadAuthorizationEpoch(ctx)
}

func (s *ddilEpochPorts) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	s.locks++
	if s.trace != nil {
		*s.trace = append(*s.trace, "epoch-lock")
	}
	if s.lockErr != nil {
		return s.lockErr
	}
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *ddilEpochPorts) BumpAuthorizationEpoch(
	ctx context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	s.bumps++
	if s.trace != nil {
		*s.trace = append(*s.trace, "epoch-bump", "write:epoch")
	}
	if s.bumpErr != nil {
		return store.AuthorizationFactRef{}, s.bumpErr
	}
	return s.epochs.BumpAuthorizationEpoch(ctx, expected)
}

func (s *ddilEpochPorts) Audit() store.AuditLog {
	if s.audit != nil {
		return s.audit
	}
	return s.Scope.Audit()
}

func (s *ddilEpochPorts) Ext(kind model.Kind) (store.GenericRepo, error) {
	if s.trace != nil {
		*s.trace = append(*s.trace, "repo-read:"+string(kind)+":ext")
	}
	if repo := s.ext[kind]; repo != nil {
		return repo, nil
	}
	return s.Scope.Ext(kind)
}

type ddilRecordingRepo struct {
	store.GenericRepo
	kind  model.Kind
	trace *[]string
}

func (r ddilRecordingRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	*r.trace = append(*r.trace, "repo-read:"+string(r.kind)+":get")
	return r.GenericRepo.Get(ctx, id)
}

func (r ddilRecordingRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	*r.trace = append(*r.trace, "repo-read:"+string(r.kind)+":list")
	return r.GenericRepo.List(ctx, q)
}

func (r ddilRecordingRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	*r.trace = append(*r.trace, "write:"+string(r.kind)+":create")
	return r.GenericRepo.Create(ctx, rec)
}

func (r ddilRecordingRepo) CreateWithID(ctx context.Context, id model.ID, rec model.Record) (model.Record, error) {
	*r.trace = append(*r.trace, "write:"+string(r.kind)+":create-with-id")
	return r.GenericRepo.CreateWithID(ctx, id, rec)
}

func (r ddilRecordingRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	*r.trace = append(*r.trace, "write:"+string(r.kind)+":update")
	return r.GenericRepo.Update(ctx, rec)
}

func (r ddilRecordingRepo) Delete(ctx context.Context, id model.ID) error {
	*r.trace = append(*r.trace, "write:"+string(r.kind)+":delete")
	return r.GenericRepo.Delete(ctx, id)
}

type ddilFailingAudit struct {
	store.AuditLog
	err error
}

func (a ddilFailingAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, a.err
}

type ddilRecordingAudit struct {
	store.AuditLog
	trace *[]string
}

func (a ddilRecordingAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	*a.trace = append(*a.trace, "write:audit")
	return a.AuditLog.Append(ctx, draft)
}

func traceIndex(trace []string, want string) int {
	for i, got := range trace {
		if got == want {
			return i
		}
	}
	return -1
}

func traceIndexPrefix(trace []string, prefix string) int {
	for i, got := range trace {
		if strings.HasPrefix(got, prefix) {
			return i
		}
	}
	return -1
}

func traceWrites(trace []string) []string {
	var out []string
	for _, item := range trace {
		if strings.HasPrefix(item, "write:") {
			out = append(out, item)
		}
	}
	return out
}
