// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// k2LifecycleIdentity models the neutral sessions-side answer, not governance
// internals. Production reaches the same answer through
// workIdentityResolver.resolveAgent, which translates canonical Identity.ID to
// the lifecycle convergence ref and checks both agent state and human sponsor.
type k2LifecycleIdentity struct {
	base     *k2WorkIdentity
	ownerRef string

	mu                 sync.Mutex
	condition          string
	observer           error
	inScopeObserver    error
	seenRefs           []string
	flipAfterPreflight bool
	authorityRevision  int64
	lockCalls          int
}

func (r *k2LifecycleIdentity) set(condition string, observer error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.condition = condition
	r.observer = observer
	r.inScopeObserver = nil
	r.seenRefs = nil
	r.flipAfterPreflight = false
	r.lockCalls = 0
}

func (r *k2LifecycleIdentity) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seenRefs...)
}

func (r *k2LifecycleIdentity) locks() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lockCalls
}

func (r *k2LifecycleIdentity) flipEligibilityAfterPreflight() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.condition = "eligible"
	r.observer = nil
	r.inScopeObserver = nil
	r.seenRefs = nil
	r.flipAfterPreflight = true
	r.lockCalls = 0
}

func (r *k2LifecycleIdentity) failInScope(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.condition = "eligible"
	r.observer = nil
	r.inScopeObserver = err
	r.seenRefs = nil
	r.flipAfterPreflight = false
	r.lockCalls = 0
}

func (r *k2LifecycleIdentity) rotateEligibleAuthority() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.condition = "eligible"
	r.observer = nil
	r.inScopeObserver = nil
	r.authorityRevision++
}

func (r *k2LifecycleIdentity) ResolveParticipant(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	kind string,
	ref string,
) (Participant, error) {
	if kind != "agent" || ref != r.ownerRef {
		return r.base.ResolveParticipant(ctx, tenant, workspace, kind, ref)
	}
	r.mu.Lock()
	r.seenRefs = append(r.seenRefs, ref)
	condition, observer := r.condition, r.observer
	r.mu.Unlock()
	if observer != nil {
		return Participant{}, observer
	}
	if condition == "eligible" {
		return r.base.ResolveParticipant(ctx, tenant, workspace, kind, ref)
	}
	// The vocabulary stays deliberately neutral. Governance can arrive at this
	// answer because enforcement blocked the identity, the agent is orphaned or
	// offboarded, or its human sponsor is no longer valid.
	return Participant{
		Kind: kind, CanonicalRef: ref,
		Active: false, WorkspaceEligible: condition != "orphaned",
	}, nil
}

func (r *k2LifecycleIdentity) SessionActsForAgent(
	ctx context.Context,
	tenant model.TenantID,
	sid string,
	agentRef string,
) (bool, error) {
	return r.base.SessionActsForAgent(ctx, tenant, sid, agentRef)
}

func (r *k2LifecycleIdentity) ObserveAgentWorkAuthority(
	_ context.Context,
	_ model.TenantID,
	_ model.ID,
	canonicalRef string,
	_ string,
) (WorkAgentAuthoritySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seenRefs = append(r.seenRefs, canonicalRef)
	if r.observer != nil {
		return WorkAgentAuthoritySnapshot{}, r.observer
	}
	eligible := canonicalRef == r.ownerRef && r.condition == "eligible"
	snapshot := WorkAgentAuthoritySnapshot{
		Eligible: eligible,
		Digest:   fmt.Sprintf("lifecycle:%s:%d", canonicalRef, r.authorityRevision),
		Token:    canonicalRef,
	}
	if eligible && r.flipAfterPreflight {
		r.condition = "offboarded"
		r.flipAfterPreflight = false
	}
	return snapshot, nil
}

func (r *k2LifecycleIdentity) LockAgentWorkAuthority(
	_ context.Context,
	_ store.Scope,
	snapshot WorkAgentAuthoritySnapshot,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	canonicalRef, ok := snapshot.Token.(string)
	if !ok {
		return store.ErrRowLockUnavailable
	}
	r.seenRefs = append(r.seenRefs, canonicalRef)
	r.lockCalls++
	if r.inScopeObserver != nil {
		return r.inScopeObserver
	}
	if canonicalRef != r.ownerRef || r.condition != "eligible" {
		return store.ErrConflict
	}
	return nil
}

func (r *k2LifecycleIdentity) AuthenticatedAgentMatches(
	ctx context.Context,
	tenant model.TenantID,
	canonicalRef string,
	authenticatedRef string,
) (bool, error) {
	return r.base.AuthenticatedAgentMatches(ctx, tenant, canonicalRef, authenticatedRef)
}

type k2LifecycleFixture struct {
	work       *k2Fixture
	identity   *k2LifecycleIdentity
	driver     string
	ownerRef   string
	externalID string
	fence      int64
}

func newK2LifecycleFixture(t *testing.T, name string) k2LifecycleFixture {
	t.Helper()
	f := newK2Fixture(t, name)
	ownerRef := model.NewID().String()
	externalID := "agent-external:" + model.NewID().String()
	f.resolver.admit("agent", ownerRef)
	f.resolver.drive(f.sid, ownerRef)
	f.resolver.authenticate(externalID, ownerRef)
	identity := &k2LifecycleIdentity{
		base: f.resolver, ownerRef: ownerRef, condition: "eligible", authorityRevision: 1,
	}
	f.h.m.UseWorkIdentityResolver(identity)
	driver := k2AgentToken(t, f.h, f.tenant, externalID, f.sid, auth.RoleEditor)

	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
		map[string]any{
			"workspace_id": f.workspace.String(), "work_kind": "implementation",
			"title":        "lifecycle-revalidated agent execution",
			"brief_md":     "Every fenced write revalidates the canonical agent owner.",
			"context_refs": []any{}, "priority": "p1",
			"owner_kind": "agent", "owner_ref": ownerRef,
			"provenance_kind": "human", "provenance_ref": "test:k2-lifecycle-revalidation",
			"acceptance": []any{map[string]any{
				"criterion_key": "lifecycle", "ordinal": 0,
				"statement": "Only a currently eligible owner can publish execution evidence.",
				"required":  true,
			}},
		},
		workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("create lifecycle WorkItem = %d %s", created.code, created.raw)
	}
	f.itemID, f.version = itemID, int64(created.body["version"].(float64))
	ready := f.apply(http.MethodPost, "/transitions", f.admin,
		map[string]any{"command": "item.ready"})
	if ready.code != http.StatusOK {
		t.Fatalf("ready lifecycle WorkItem = %d %s", ready.code, ready.raw)
	}
	f.version = int64(ready.body["version"].(float64))
	acquired := f.acquire(driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("eligible owner acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	// Make item.submit otherwise valid before eligibility changes. This is a
	// real fenced write through the purpose-restricted runtime credential.
	evaluated := k2RestrictedEvaluate(t, f, driver, fence, "passed")
	if evaluated.code != http.StatusOK {
		t.Fatalf("seed acceptance evidence = %d %s", evaluated.code, evaluated.raw)
	}
	f.version = int64(evaluated.body["version"].(float64))
	identity.set("eligible", nil)
	return k2LifecycleFixture{
		work: f, identity: identity, driver: driver,
		ownerRef: ownerRef, externalID: externalID, fence: fence,
	}
}

type k2LifecycleDurableState struct {
	snapshot string
	lease    WorkLease
	claim    Lease
	counts   [5]int
}

func k2LifecycleState(t *testing.T, f *k2Fixture) k2LifecycleDurableState {
	t.Helper()
	snapshot, err := f.h.m.Get(
		context.Background(), f.tenant, WorkPrincipal{}, model.ID(f.itemID),
	)
	if err != nil {
		t.Fatalf("read WorkItem: %v", err)
	}
	lease, err := f.h.m.GetLease(
		context.Background(), f.tenant, WorkPrincipal{}, model.ID(f.itemID),
	)
	if err != nil {
		t.Fatalf("read WorkLease: %v", err)
	}
	claim, ok, err := f.h.m.ActiveClaim(context.Background(), f.tenant, f.sid)
	if err != nil || !ok {
		t.Fatalf("read Claim: %#v ok=%v err=%v", claim, ok, err)
	}
	raw, err := canonicalJSON(snapshot)
	if err != nil {
		t.Fatalf("encode WorkItem snapshot: %v", err)
	}
	out := k2LifecycleDurableState{snapshot: string(raw), lease: lease, claim: claim}
	err = f.h.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		for i, kind := range []model.Kind{
			workItemKind, workLeaseKind, workAcceptanceKind, workCommandKind, workEventKind,
		} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, err := listAll(context.Background(), repo)
			if err != nil {
				return err
			}
			out.counts[i] = len(rows)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("count durable work rows: %v", err)
	}
	return out
}

func k2LifecycleWrite(t *testing.T, f k2LifecycleFixture, command string) resp {
	t.Helper()
	switch command {
	case "lease.renew":
		return f.work.apply(http.MethodPost, "/lease/renew", f.driver, map[string]any{
			"holder_sid": f.work.sid, "fence": f.fence, "ttl_seconds": 60,
		})
	case "lease.release":
		return f.work.apply(http.MethodPost, "/lease/release", f.driver, map[string]any{
			"holder_sid": f.work.sid, "fence": f.fence,
		})
	case "acceptance.evaluate":
		return k2RestrictedEvaluate(t, f.work, f.driver, f.fence, "failed")
	case "item.submit", "item.block", "item.fail":
		return k2RestrictedTransition(f.work, f.driver, command, f.fence)
	default:
		t.Fatalf("unknown lifecycle test command %q", command)
		return resp{}
	}
}

func TestWorkK2FencedWritesRevalidateAgentLifecycleAndSponsor(t *testing.T) {
	f := newK2LifecycleFixture(t, "k2-lifecycle-revalidation")
	commands := []string{
		"lease.renew", "acceptance.evaluate",
		"item.submit", "item.block", "item.fail",
	}

	// ExternalID is authenticated attribution, not an alternate owner spelling.
	before := k2LifecycleState(t, f.work)
	forged := f.work.apply(http.MethodPost, "/transitions", f.driver, map[string]any{
		"command": "item.block", "code": "external_owner_spelling",
		"reason":     "An external identity spelling must not select the durable owner.",
		"holder_sid": f.work.sid, "holder_agent_ref": f.externalID, "fence": f.fence,
	})
	if forged.code != http.StatusUnprocessableEntity ||
		workAPIErrorCode(forged) != "owner_ineligible" {
		t.Fatalf("ExternalID owner spelling = %d %s, want 422 owner_ineligible",
			forged.code, forged.raw)
	}
	if after := k2LifecycleState(t, f.work); after != before {
		t.Fatalf("forged owner spelling changed durable state:\n before=%#v\n after=%#v",
			before, after)
	}

	// Reduction of authority is deliberately reachable after ineligibility. It
	// must not call the lifecycle guard and must end the active generation.
	t.Run("ineligible owner can release", func(t *testing.T) {
		releaseFixture := newK2LifecycleFixture(t, "k2-lifecycle-release")
		releaseFixture.identity.set("offboarded", nil)
		got := k2LifecycleWrite(t, releaseFixture, "lease.release")
		if got.code != http.StatusOK {
			t.Fatalf("offboarded owner release = %d %s, want 200", got.code, got.raw)
		}
		if lease := releaseFixture.work.lease(t); lease.State == workLeaseActive {
			t.Fatalf("offboarded owner release retained authority: %#v", lease)
		}
		if seen := releaseFixture.identity.seen(); len(seen) != 0 {
			t.Fatalf("release consulted denying lifecycle guard: %v", seen)
		}
	})

	for _, condition := range []string{
		"enforcement_blocked", "orphaned", "offboarded", "sponsor_invalid",
	} {
		for _, command := range commands {
			t.Run(condition+"/"+command, func(t *testing.T) {
				f.identity.set(condition, nil)
				before := k2LifecycleState(t, f.work)
				got := k2LifecycleWrite(t, f, command)
				if got.code != http.StatusUnprocessableEntity ||
					workAPIErrorCode(got) != "owner_ineligible" {
					t.Fatalf("ineligible owner %s = %d %s, want 422 owner_ineligible",
						command, got.code, got.raw)
				}
				if after := k2LifecycleState(t, f.work); after != before {
					t.Fatalf("ineligible %s changed durable state:\n before=%#v\n after=%#v",
						command, before, after)
				}
				seen := f.identity.seen()
				if len(seen) == 0 {
					t.Fatalf("%s did not observe canonical agent eligibility", command)
				}
				for _, ref := range seen {
					if ref != f.ownerRef || ref == f.externalID {
						t.Fatalf("%s revalidated ref %q, want canonical owner %q",
							command, ref, f.ownerRef)
					}
				}
			})
		}
	}

	// Observer failure is the third outcome, distinct from known ineligibility,
	// and still happens before Claim, WorkLease, receipt, event or item writes.
	f.identity.set("eligible", errors.New("lifecycle observer unavailable"))
	before = k2LifecycleState(t, f.work)
	unknown := k2LifecycleWrite(t, f, "item.block")
	if unknown.code != http.StatusServiceUnavailable ||
		workAPIErrorCode(unknown) != "evidence_unavailable" ||
		unknown.body["verdict"] != string(VerdictUnknown) {
		t.Fatalf("unavailable lifecycle observer = %d %s, want 503 %s/evidence_unavailable",
			unknown.code, unknown.raw, VerdictUnknown)
	}
	if after := k2LifecycleState(t, f.work); after != before {
		t.Fatalf("unknown lifecycle evidence changed durable state:\n before=%#v\n after=%#v",
			before, after)
	}

	// NO-FIRE direction: a guard that simply rejects every request would pass
	// every refusal above. Restoring current eligibility must permit a real
	// renewal and advance the durable aggregate.
	f.identity.set("eligible", nil)
	allowedBefore := k2LifecycleState(t, f.work)
	allowed := k2LifecycleWrite(t, f, "lease.renew")
	if allowed.code != http.StatusOK || allowed.body["status"] != workLeaseActive {
		t.Fatalf("eligible owner renew = %d %s, want 200 active", allowed.code, allowed.raw)
	}
	f.work.version = int64(allowed.body["version"].(float64))
	if after := k2LifecycleState(t, f.work); after == allowedBefore {
		t.Fatal("eligible owner renewal did not change durable state")
	}
}

func TestWorkK2LifecycleChangeBetweenPreflightAndMutationIsRefused(t *testing.T) {
	f := newK2LifecycleFixture(t, "k2-lifecycle-toctou")
	f.identity.flipEligibilityAfterPreflight()
	before := k2LifecycleState(t, f.work)
	got := k2LifecycleWrite(t, f, "item.block")
	if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
		t.Fatalf("lifecycle changed after preflight = %d %s, want 422 owner_ineligible",
			got.code, got.raw)
	}
	if after := k2LifecycleState(t, f.work); after != before {
		t.Fatalf("TOCTOU refusal changed durable state:\n before=%#v\n after=%#v", before, after)
	}
	if seen := f.identity.seen(); len(seen) < 2 {
		t.Fatalf("lifecycle observed %d times, want preflight plus transaction recheck", len(seen))
	}
}

func TestWorkK2AgentAuthorityRevisionBindsPlanHash(t *testing.T) {
	f := newK2LifecycleFixture(t, "k2-lifecycle-plan-hash")
	path := "/v1/m/sessions/work-items/" + f.work.itemID + "/lease/renew"
	body := map[string]any{
		"holder_sid": f.work.sid, "fence": f.fence, "ttl_seconds": 60,
	}
	headers := workAPIHeaders(f.work.tenant, map[string]string{
		"If-Match": etag(f.work.version),
	})
	plan := f.work.h.doJSON(http.MethodPost, path+"?mode=plan", f.driver, body, headers)
	planHash, _ := plan.body["plan_hash"].(string)
	if plan.code != http.StatusOK || len(planHash) != 64 {
		t.Fatalf("plan eligible renewal = %d %s", plan.code, plan.raw)
	}
	if got := f.identity.locks(); got != 0 {
		t.Fatalf("Plan acquired %d authority locks, want observational read only", got)
	}

	// The agent stays eligible, but a server-owned authority fact changes
	// version. Plan must therefore change even though the WorkItem ETag does not.
	f.identity.rotateEligibleAuthority()
	replanned := f.work.h.doJSON(http.MethodPost, path+"?mode=plan", f.driver, body, headers)
	replannedHash, _ := replanned.body["plan_hash"].(string)
	if replanned.code != http.StatusOK || len(replannedHash) != 64 || replannedHash == planHash {
		t.Fatalf("replanned authority digest = %d %s, original=%s", replanned.code, replanned.raw, planHash)
	}
	if got := f.identity.locks(); got != 0 {
		t.Fatalf("replan acquired %d authority locks, want observational read only", got)
	}

	applyHeaders := workAPIHeaders(f.work.tenant, map[string]string{
		"Idempotency-Key": model.NewID().String(),
		"If-Match":        etag(f.work.version),
		"If-Plan-Hash":    planHash,
	})
	before := k2LifecycleState(t, f.work)
	got := f.work.h.doJSON(http.MethodPost, path+"?mode=apply", f.driver, body, applyHeaders)
	if got.code != http.StatusPreconditionFailed || workAPIErrorCode(got) != "plan_changed" {
		t.Fatalf("stale authority plan = %d %s, want 412 plan_changed", got.code, got.raw)
	}
	if got := f.identity.locks(); got != 1 {
		t.Fatalf("Apply authority locks = %d, want exactly one", got)
	}
	if after := k2LifecycleState(t, f.work); after != before {
		t.Fatalf("stale authority plan changed durable state:\n before=%#v\n after=%#v", before, after)
	}
}

func TestWorkK2MissingTransactionalLifecycleEvidenceIsUnknown(t *testing.T) {
	f := newK2LifecycleFixture(t, "k2-lifecycle-row-lock-unavailable")
	f.identity.failInScope(store.ErrWorkspaceConfinement)
	before := k2LifecycleState(t, f.work)
	got := k2LifecycleWrite(t, f, "item.block")
	if got.code != http.StatusServiceUnavailable ||
		workAPIErrorCode(got) != "evidence_unavailable" ||
		got.body["verdict"] != string(VerdictUnknown) {
		t.Fatalf("unavailable transactional lifecycle = %d %s, want 503 %s/evidence_unavailable",
			got.code, got.raw, VerdictUnknown)
	}
	if after := k2LifecycleState(t, f.work); after != before {
		t.Fatalf("unknown transactional lifecycle changed durable state:\n before=%#v\n after=%#v",
			before, after)
	}
}

func TestWorkK2OwnerFailureRevalidatesLifecycleAfterLeaseEnds(t *testing.T) {
	for _, state := range []string{"blocked", "review"} {
		t.Run(state, func(t *testing.T) {
			f := newK2LifecycleFixture(t, "k2-lifecycle-owner-fail-"+state)
			switch state {
			case "blocked":
				blocked := f.work.apply(http.MethodPost, "/transitions", f.work.admin,
					map[string]any{
						"command": "item.block", "code": "operator_blocked",
						"reason": "The operator blocked the execution before owner failure.",
					})
				if blocked.code != http.StatusOK || blocked.body["status"] != "blocked" {
					t.Fatalf("seed blocked item = %d %s", blocked.code, blocked.raw)
				}
				f.work.version = int64(blocked.body["version"].(float64))
			case "review":
				submitted := k2RestrictedTransition(f.work, f.driver, "item.submit", f.fence)
				if submitted.code != http.StatusOK || submitted.body["status"] != "review" {
					t.Fatalf("seed review item = %d %s", submitted.code, submitted.raw)
				}
				f.work.version = int64(submitted.body["version"].(float64))
			}
			if lease := f.work.lease(t); lease.State == workLeaseActive {
				t.Fatalf("%s item retained a live lease: %#v", state, lease)
			}

			owner := k2AgentToken(
				t, f.work.h, f.work.tenant, f.externalID, "", auth.RoleEditor,
			)
			f.identity.set("offboarded", nil)
			before := k2LifecycleState(t, f.work)
			denied := f.work.apply(http.MethodPost, "/transitions", owner, map[string]any{
				"command": "item.fail", "code": "owner_failure",
				"reason": "An offboarded owner must not retain terminal authority.",
			})
			if denied.code != http.StatusUnprocessableEntity ||
				workAPIErrorCode(denied) != "owner_ineligible" {
				t.Fatalf("offboarded owner fail = %d %s, want 422 owner_ineligible",
					denied.code, denied.raw)
			}
			if after := k2LifecycleState(t, f.work); after != before {
				t.Fatalf("offboarded %s owner changed durable state:\n before=%#v\n after=%#v",
					state, before, after)
			}

			// NO-FIRE: the exact same authenticated ExternalID remains able to use
			// owner authority once the canonical lifecycle becomes eligible again.
			f.identity.set("eligible", nil)
			allowed := f.work.apply(http.MethodPost, "/transitions", owner, map[string]any{
				"command": "item.fail", "code": "owner_failure",
				"reason": "The currently eligible canonical owner reports terminal failure.",
			})
			if allowed.code != http.StatusOK || allowed.body["status"] != "failed" {
				t.Fatalf("eligible %s owner fail = %d %s, want 200 failed",
					state, allowed.code, allowed.raw)
			}
		})
	}
}
