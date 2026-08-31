// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These tests cross the REAL HTTP gate: every principal below is the one
// core/auth builds from a stored credential, never a WorkPrincipal literal.
// That distinction is the whole point of the file. The K2 acceptance suite
// fabricates `WorkPrincipal{SessionID: ...}` (work_k1_acceptance_test.go:854,
// work_lease_test.go:68, runtime_work_control_test.go:164) and so it exercises
// a principal shape `workPrincipalFromAuth` (work_api.go:367-381) can never
// produce: `WorkPrincipal.SessionID` had NO producer in the whole tree. That is
// how 29 mutants with zero survivors coexisted with owner_kind="session" being
// answerable only with 403 in production.

// k2WorkIdentity is a DENY-CLOSED resolver with the same shape as the
// production one (cmd/olivares/workkernel.go:53-187): a participant is eligible
// only if registered, and a session acts for an agent only if that exact agent
// identity drives it. allowWorkIdentity says yes to everything, so a test built
// on it cannot tell an identity check from its absence.
type k2WorkIdentity struct {
	mu sync.Mutex
	// drives maps a canonical sid onto the agent identity ref that runs it,
	// mirroring session.AgentID -> agent.IdentityID in the real resolver.
	drives map[string]string
	// eligible holds "<kind>|<ref>" for every registered participant.
	eligible map[string]bool
}

func newK2WorkIdentity() *k2WorkIdentity {
	return &k2WorkIdentity{drives: map[string]string{}, eligible: map[string]bool{}}
}

func (r *k2WorkIdentity) admit(kind, ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eligible[kind+"|"+ref] = true
}

func (r *k2WorkIdentity) drive(sid, agentRef string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drives[sid] = agentRef
}

func (r *k2WorkIdentity) ResolveParticipant(
	_ context.Context, _ model.TenantID, _ model.ID, kind, ref string,
) (Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.eligible[kind+"|"+ref] {
		return Participant{Kind: kind, CanonicalRef: ref}, nil
	}
	return Participant{Kind: kind, CanonicalRef: ref, Active: true, WorkspaceEligible: true}, nil
}

func (r *k2WorkIdentity) SessionActsForAgent(
	_ context.Context, _ model.TenantID, sid, agentRef string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return agentRef != "" && r.drives[sid] == agentRef, nil
}

func (r *k2WorkIdentity) ObserveAgentWorkAuthority(
	_ context.Context,
	_ model.TenantID,
	_ model.ID,
	canonicalRef string,
	_ string,
) (WorkAgentAuthoritySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	eligible := canonicalRef != "" && r.eligible["agent|"+canonicalRef]
	return WorkAgentAuthoritySnapshot{
		Eligible: eligible, Digest: "test-authority:" + canonicalRef, Token: canonicalRef,
	}, nil
}

func (r *k2WorkIdentity) LockAgentWorkAuthority(
	_ context.Context,
	_ store.Scope,
	snapshot WorkAgentAuthoritySnapshot,
) error {
	canonicalRef, ok := snapshot.Token.(string)
	if !ok {
		return store.ErrRowLockUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.eligible["agent|"+canonicalRef] {
		return store.ErrConflict
	}
	return nil
}

func (r *k2WorkIdentity) AuthenticatedAgentMatches(
	_ context.Context,
	_ model.TenantID,
	canonicalRef string,
	authenticatedRef string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return canonicalRef != "" && r.drives["principal:"+authenticatedRef] == canonicalRef, nil
}

func (r *k2WorkIdentity) authenticate(externalRef, canonicalRef string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drives["principal:"+externalRef] = canonicalRef
}

// k2AgentToken uses the production exact-session issuer whenever sessionRef is
// present. The empty-session branch remains an ordinary agent token for launch
// preflight tests: a launcher has no canonical SID until the runtime admits it.
// Nothing here fabricates a Principal; the HTTP middleware authenticates the
// bearer and applies core/auth's private permission ceiling.
func k2AgentToken(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	agentRef, sessionRef, role string,
) string {
	return k2AgentTokenAtFence(t, h, tenant, agentRef, sessionRef, role, 1)
}

func k2AgentTokenAtFence(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	agentRef, sessionRef, role string,
	claimFence int64,
) string {
	return k2AgentTokenForRunAtFence(
		t, h, tenant, agentRef, sessionRef, role, model.NewID().String(), claimFence,
	)
}

func k2AgentTokenForRunAtFence(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	agentRef, sessionRef, role, runRef string,
	claimFence int64,
) string {
	t.Helper()
	if sessionRef != "" {
		actor, err := auth.NewSystemOperator("test:sessions-runtime", "mint an admitted test session credential")
		if err != nil {
			t.Fatalf("build runtime issuer: %v", err)
		}
		issued, err := auth.NewAuthenticator(h.st, nil).IssueWorkSessionCredential(
			context.Background(), actor, auth.WorkSessionCredentialSpec{
				Tenant: tenant, SessionRef: sessionRef, RunRef: runRef,
				AgentRef: agentRef, ClaimFence: claimFence,
			})
		if err != nil {
			t.Fatalf("mint work-session credential: %v", err)
		}
		return issued.Token
	}
	cred, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatalf("mint agent credential: %v", err)
	}
	if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		_, err := as.Tokens().Create(context.Background(), model.APIToken{
			Name: "k2-agent-" + agentRef, Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: tenant, Role: role, AgentRef: agentRef,
		})
		return err
	}); err != nil {
		t.Fatalf("store agent token: %v", err)
	}
	return cred.Token
}

// k2Fixture is one session-owned WorkItem, ready, plus the identities that may
// and may not reach it.
type k2Fixture struct {
	h         *harness
	resolver  *k2WorkIdentity
	tenant    model.TenantID
	workspace model.ID
	admin     string
	// sid is the canonical Olivares session id (osn_<uuid>) minted through the
	// production identity plane. ownerRef IS that sid: a session-owned item
	// stores the canonical id, prefix included, so no reader has to strip it and
	// mistake it for some other uuid.
	sid      string
	ownerRef string
	// driver drives sid; stranger is a different, equally valid agent; human is
	// a console editor. All three carry sessions:lease:write.
	driverRef, strangerRef           string
	driver, sibling, stranger, human string
	itemID                           string
	version                          int64
}

func newK2Fixture(t *testing.T, name string) *k2Fixture {
	t.Helper()
	resolver := newK2WorkIdentity()
	h := newHarness(t, New(
		WithWorkIdentityResolver(resolver),
		WithWorkContentGuard(allowWorkContent{}),
	))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, name)
	workspace := workAPIWorkspace(t, h, tenant)

	sid, err := h.m.ResolveSession(context.Background(), tenant, SessionBinding{
		Provider: "k2-http", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve canonical sid: %v", err)
	}
	f := &k2Fixture{
		h: h, resolver: resolver, tenant: tenant, workspace: workspace, admin: admin,
		sid: sid, ownerRef: sid,
		driverRef: model.NewID().String(), strangerRef: model.NewID().String(),
	}
	siblingSID, err := h.m.ResolveSession(context.Background(), tenant, SessionBinding{
		Provider: "k2-http", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve sibling sid: %v", err)
	}
	if _, err := h.m.Claim(context.Background(), tenant, f.sid, "k2-fixture-owner", 0); err != nil {
		t.Fatalf("claim owner sid: %v", err)
	}
	if _, err := h.m.Claim(context.Background(), tenant, siblingSID, "k2-fixture-sibling", 0); err != nil {
		t.Fatalf("claim sibling sid: %v", err)
	}
	f.driver = k2AgentToken(t, h, tenant, f.driverRef, f.sid, auth.RoleEditor)
	// Same authenticated agent, different authenticated session: this is the
	// sibling path an agent-wide fallback would incorrectly admit.
	f.sibling = k2AgentToken(t, h, tenant, f.driverRef, siblingSID, auth.RoleEditor)
	f.stranger = k2AgentToken(t, h, tenant, f.strangerRef,
		sidPrefix+model.NewID().String(), auth.RoleEditor)
	f.human = workAPIRoleToken(t, h, admin, tenant, auth.RoleEditor, "k2-human-"+name+"@a.test")

	resolver.drive(sid, f.driverRef)
	resolver.admit("session", f.ownerRef)
	resolver.admit("agent", f.driverRef)
	resolver.admit("agent", f.strangerRef)

	f.seedReadySessionOwnedItem(t)
	return f
}

func (f *k2Fixture) seedReadySessionOwnedItem(t *testing.T) {
	t.Helper()
	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
		map[string]any{
			"workspace_id":    f.workspace.String(),
			"work_kind":       "implementation",
			"title":           "session-owned execution authority",
			"brief_md":        "The owning session must be able to take its own lease.",
			"context_refs":    []any{},
			"priority":        "p1",
			"owner_kind":      "session",
			"owner_ref":       f.ownerRef,
			"provenance_kind": "human",
			"provenance_ref":  "test:k2-session-identity",
			"acceptance": []any{map[string]any{
				"criterion_key": "reachable",
				"ordinal":       0,
				"statement":     "The owning session acquires, evaluates and submits.",
				"required":      true,
			}},
		},
		workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("create session-owned WorkItem = %d %s", created.code, created.raw)
	}
	f.itemID = itemID
	f.version = int64(created.body["version"].(float64))

	ready := f.apply(http.MethodPost, "/transitions", f.admin, map[string]any{"command": "item.ready"})
	if ready.code != http.StatusOK {
		t.Fatalf("ready session-owned WorkItem = %d %s", ready.code, ready.raw)
	}
	f.version = int64(ready.body["version"].(float64))
}

// apply issues one mode=apply mutation at the fixture's current version.
func (f *k2Fixture) apply(method, suffix, token string, body map[string]any) resp {
	return f.h.doJSON(method, "/v1/m/sessions/work-items/"+f.itemID+suffix+"?mode=apply", token, body,
		workAPIHeaders(f.tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(),
			"If-Match":        etag(f.version),
		}))
}

func (f *k2Fixture) acquire(token string, body map[string]any) resp {
	full := map[string]any{"holder_sid": f.sid, "ttl_seconds": 60}
	for k, v := range body {
		full[k] = v
	}
	return f.apply(http.MethodPost, "/lease/acquire", token, full)
}

func (f *k2Fixture) lease(t *testing.T) WorkLease {
	t.Helper()
	got := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+f.itemID+"/lease", f.admin, tenantHdr(f.tenant))
	if got.code != http.StatusOK {
		t.Fatalf("read lease = %d %s", got.code, got.raw)
	}
	state, _ := got.body["state"].(string)
	sid, _ := got.body["holder_sid"].(string)
	runRef, _ := got.body["holder_run_ref"].(string)
	agentRef, _ := got.body["holder_agent_ref"].(string)
	fence, _ := got.body["fence"].(float64)
	return WorkLease{
		State: state, HolderSID: sid, HolderRunRef: runRef,
		HolderAgentRef: agentRef, Fence: int64(fence),
	}
}

func rotateK2FixtureClaim(t *testing.T, f *k2Fixture) Lease {
	t.Helper()
	claim, ok, err := f.h.m.ActiveClaim(context.Background(), f.tenant, f.sid)
	if err != nil || !ok {
		t.Fatalf("read fixture Claim: %+v, %v", claim, err)
	}
	if err := f.h.m.Release(
		context.Background(), f.tenant, f.sid, claim.Holder, claim.Fence,
	); err != nil {
		t.Fatalf("release fixture Claim: %v", err)
	}
	rotated, err := f.h.m.Claim(context.Background(), f.tenant, f.sid, claim.Holder, 0)
	if err != nil || rotated.Fence <= claim.Fence {
		t.Fatalf("rotate fixture Claim = %+v, %v; old = %+v", rotated, err, claim)
	}
	return rotated
}

func etag(version int64) string { return `"v` + strconv.FormatInt(version, 10) + `"` }

// TestWorkK2SessionOwnedLeaseIsReachableOnlyByTheSessionsOwnDriver is THE gate
// test for the K2 P0. Every call below goes through
// POST /v1/m/sessions/work-items/{id}/lease/acquire with an identity the server
// derived from a stored credential.
func TestWorkK2SessionOwnedLeaseIsReachableOnlyByTheSessionsOwnDriver(t *testing.T) {
	t.Run("the driving agent acquires", func(t *testing.T) {
		f := newK2Fixture(t, "k2-owner-reaches")
		got := f.acquire(f.driver, nil)
		if got.code != http.StatusOK {
			t.Fatalf("owning session lease.acquire = %d %s, want 200", got.code, got.raw)
		}
		lease := f.lease(t)
		if lease.State != workLeaseActive || lease.HolderSID != f.sid || lease.Fence != 1 ||
			lease.HolderAgentRef != "" {
			t.Fatalf("acquired session-owned lease = %#v", lease)
		}
	})

	// NO-FIRE DIRECTION for the acquire above: a control that refuses everybody
	// passes every "it refuses" assertion below, so the 200 has to be measured
	// in the same suite as the refusals.
	t.Run("a stranger agent that does not drive the sid is refused", func(t *testing.T) {
		f := newK2Fixture(t, "k2-stranger-refused")
		got := f.acquire(f.stranger, nil)
		if got.code == http.StatusOK {
			t.Fatalf("a non-driving agent acquired the session's lease: %s", got.raw)
		}
		if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
			t.Fatalf("stranger lease.acquire = %d %s, want 422 owner_ineligible", got.code, got.raw)
		}
		if lease := f.lease(t); lease.State != workLeaseVacant || lease.Fence != 0 {
			t.Fatalf("refused acquire still moved the lease: %#v", lease)
		}
	})

	t.Run("a sibling session of the same agent is refused", func(t *testing.T) {
		f := newK2Fixture(t, "k2-sibling-refused")
		got := f.acquire(f.sibling, nil)
		if got.code == http.StatusOK {
			t.Fatalf("a sibling session acquired the holder SID's lease: %s", got.raw)
		}
		if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
			t.Fatalf("sibling lease.acquire = %d %s, want 422 owner_ineligible", got.code, got.raw)
		}
		if lease := f.lease(t); lease.State != workLeaseVacant || lease.Fence != 0 {
			t.Fatalf("refused sibling acquire still moved the lease: %#v", lease)
		}
	})

	t.Run("a human operator cannot take a session's lease", func(t *testing.T) {
		f := newK2Fixture(t, "k2-human-refused")
		got := f.acquire(f.human, nil)
		if got.code == http.StatusOK {
			t.Fatalf("a user principal acquired a session-owned lease: %s", got.raw)
		}
		if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
			t.Fatalf("human lease.acquire = %d %s, want 422 owner_ineligible", got.code, got.raw)
		}
		if lease := f.lease(t); lease.State != workLeaseVacant {
			t.Fatalf("refused human acquire still moved the lease: %#v", lease)
		}
	})

	t.Run("a superadmin is not exempt", func(t *testing.T) {
		f := newK2Fixture(t, "k2-superadmin-refused")
		got := f.acquire(f.admin, nil)
		if got.code == http.StatusOK {
			t.Fatalf("superadmin acquired a session-owned lease by acquire: %s", got.raw)
		}
	})
}

// TestWorkK2SessionOwnedLeaseHolderIsProvenNotDeclared covers the second half of
// the same predicate: holding the lease is not enough, the caller must keep
// proving it, and the fence still rules.
func TestWorkK2SessionOwnedLeaseHolderIsProvenNotDeclared(t *testing.T) {
	f := newK2Fixture(t, "k2-holder-proof")
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("seed acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	t.Run("a stranger cannot renew by declaring the holder sid", func(t *testing.T) {
		got := f.apply(http.MethodPost, "/lease/renew", f.stranger, map[string]any{
			"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60,
		})
		if got.code == http.StatusOK {
			t.Fatalf("a second actor renewed by sending another session's holder_sid: %s", got.raw)
		}
		if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
			t.Fatalf("stranger renew = %d %s, want 422 owner_ineligible", got.code, got.raw)
		}
	})

	t.Run("a same-agent sibling session cannot renew", func(t *testing.T) {
		got := f.apply(http.MethodPost, "/lease/renew", f.sibling, map[string]any{
			"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60,
		})
		if got.code == http.StatusOK {
			t.Fatalf("a same-agent sibling renewed the holder lease: %s", got.raw)
		}
		if got.code != http.StatusUnprocessableEntity || workAPIErrorCode(got) != "owner_ineligible" {
			t.Fatalf("sibling renew = %d %s, want 422 owner_ineligible", got.code, got.raw)
		}
	})

	t.Run("a stale fence is still a conflict for the proven holder", func(t *testing.T) {
		got := f.apply(http.MethodPost, "/lease/renew", f.driver, map[string]any{
			"holder_sid": f.sid, "fence": fence + 1, "ttl_seconds": 60,
		})
		if got.code != http.StatusConflict || workAPIErrorCode(got) != "stale_fence" {
			t.Fatalf("proven holder with a stale fence = %d %s, want 409 stale_fence", got.code, got.raw)
		}
	})

	t.Run("the proven holder renews", func(t *testing.T) {
		got := f.apply(http.MethodPost, "/lease/renew", f.driver, map[string]any{
			"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60,
		})
		if got.code != http.StatusOK {
			t.Fatalf("proven holder renew = %d %s, want 200", got.code, got.raw)
		}
		f.version = int64(got.body["version"].(float64))
	})
}

func TestWorkK2SessionLeaseReleaseRejectsSiblingAndAcceptsExactSID(t *testing.T) {
	f := newK2Fixture(t, "k2-release-sibling")
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	denied := f.apply(http.MethodPost, "/lease/release", f.sibling, map[string]any{
		"holder_sid": f.sid, "fence": fence,
	})
	if denied.code != http.StatusUnprocessableEntity || workAPIErrorCode(denied) != "owner_ineligible" {
		t.Fatalf("sibling release = %d %s, want 422 owner_ineligible", denied.code, denied.raw)
	}
	exact := f.apply(http.MethodPost, "/lease/release", f.driver, map[string]any{
		"holder_sid": f.sid, "fence": fence,
	})
	if exact.code != http.StatusOK {
		t.Fatalf("exact release = %d %s", exact.code, exact.raw)
	}
	if lease := f.lease(t); lease.State != workLeaseReleased {
		t.Fatalf("released lease = %#v", lease)
	}
}

func TestWorkK2SessionCredentialRejectsStaleClaimFence(t *testing.T) {
	f := newK2Fixture(t, "k2-stale-claim-fence")
	rotateK2FixtureClaim(t, f)

	denied := f.acquire(f.driver, nil)
	if denied.code != http.StatusUnprocessableEntity ||
		workAPIErrorCode(denied) != "owner_ineligible" {
		t.Fatalf("stale Claim bearer acquire = %d %s, want 422 owner_ineligible",
			denied.code, denied.raw)
	}
	if lease := f.lease(t); lease.State != workLeaseVacant || lease.Fence != 0 {
		t.Fatalf("stale Claim bearer changed WorkLease: %#v", lease)
	}
}

func TestWorkK2SessionCredentialAcceptsCurrentClaimFence(t *testing.T) {
	f := newK2Fixture(t, "k2-current-claim-fence")
	claim := rotateK2FixtureClaim(t, f)
	current := k2AgentTokenAtFence(
		t, f.h, f.tenant, f.driverRef, f.sid, auth.RoleEditor, claim.Fence,
	)

	acquired := f.acquire(current, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("current Claim bearer acquire = %d %s, want 200", acquired.code, acquired.raw)
	}
	if lease := f.lease(t); lease.State != workLeaseActive || lease.Fence != 1 {
		t.Fatalf("current Claim bearer WorkLease = %#v", lease)
	}
}

// TestWorkK2SessionOwnedItemCompletesItsExecutionLoop proves REACHABLE means the
// whole loop, not just acquire: a lease nobody can submit under is still an
// unreachable half of the ownership model. item.submit and acceptance.evaluate
// go through validateExecutionLeaseInScope (work_service.go:480,554), which
// reads the same predicate as acquire.
func TestWorkK2SessionOwnedItemCompletesItsExecutionLoop(t *testing.T) {
	f := newK2Fixture(t, "k2-execution-loop")
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	criterion := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+f.itemID+"/acceptance",
		f.admin, tenantHdr(f.tenant))
	items, _ := criterion.body["items"].([]any)
	if criterion.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("read acceptance = %d %s", criterion.code, criterion.raw)
	}
	row, _ := items[0].(map[string]any)
	criterionID, _ := row["id"].(string)

	evaluated := f.h.doJSON(http.MethodPatch,
		"/v1/m/sessions/work-items/"+f.itemID+"/acceptance/"+criterionID+"?mode=apply", f.driver,
		map[string]any{
			"holder_sid": f.sid, "fence": fence,
			"acceptance": []any{map[string]any{
				"state": "passed", "evidence_ref": "job:k2-session-loop",
				"evidence_hash": hexHash(hashBytes([]byte("green"))),
			}},
		},
		workAPIHeaders(f.tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(),
			"If-Match":        etag(f.version),
		}))
	if evaluated.code != http.StatusOK {
		t.Fatalf("owning session acceptance.evaluate = %d %s, want 200", evaluated.code, evaluated.raw)
	}
	f.version = int64(evaluated.body["version"].(float64))

	submitted := f.apply(http.MethodPost, "/transitions", f.driver, map[string]any{
		"command": "item.submit", "holder_sid": f.sid, "fence": fence,
	})
	if submitted.code != http.StatusOK {
		t.Fatalf("owning session item.submit = %d %s, want 200", submitted.code, submitted.raw)
	}
	if status, _ := submitted.body["status"].(string); status != "review" {
		t.Fatalf("submitted status = %q, want review (%s)", status, submitted.raw)
	}
}

// TestWorkK2RunBindingProvesSIDAndDerivesAgentRef closes both confused-deputy
// edges on holder_run_ref. The run must be the operated alias of the exact
// authenticated SID, and holder_agent_ref is read from that run rather than
// trusted from the request body.
func TestWorkK2RunBindingProvesSIDAndDerivesAgentRef(t *testing.T) {
	runner := &fakeRunner{}
	resolver := newK2WorkIdentity()
	m := New(
		WithRunner(runner),
		WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(resolver),
		WithWorkContentGuard(allowWorkContent{}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k2-run-sid-binding")
	workspace := workAPIWorkspace(t, h, tenant)
	agentRef := model.NewID().String()
	launcher := k2AgentToken(t, h, tenant, agentRef, "", auth.RoleAdmin)

	launch := func() string {
		t.Helper()
		got := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", launcher, map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		}, tenantHdr(tenant))
		ref, _ := got.body["run_ref"].(string)
		if got.code != http.StatusCreated || ref == "" {
			t.Fatalf("create attributed run = %d %s", got.code, got.raw)
		}
		return ref
	}
	sidFor := func(runRef string) string {
		t.Helper()
		sid, err := m.ResolveSession(context.Background(), tenant, SessionBinding{
			Provider: ProviderOperated, ExternalID: runRef, Origin: OriginOperated,
		})
		if err != nil {
			t.Fatalf("resolve operated run %s: %v", runRef, err)
		}
		return sid
	}
	runA, runB := launch(), launch()
	sidA, sidB := sidFor(runA), sidFor(runB)
	resolver.admit("session", sidA)
	resolver.admit("session", sidB)
	resolver.admit("agent", agentRef)
	resolver.drive(sidA, agentRef)
	resolver.drive(sidB, agentRef)

	f := &k2Fixture{
		h: h, resolver: resolver, tenant: tenant, workspace: workspace, admin: admin,
		sid: sidA, ownerRef: sidA, driverRef: agentRef,
	}
	f.driver = k2AgentTokenForRunAtFence(
		t, h, tenant, agentRef, sidA, auth.RoleEditor, runA, 1,
	)
	f.seedReadySessionOwnedItem(t)

	// Same agent and a real sibling run are not enough: the operated alias must
	// resolve to the holder SID named by the authenticated credential.
	wrongRun := f.acquire(f.driver, map[string]any{"holder_run_ref": runB})
	if wrongRun.code != http.StatusUnprocessableEntity || workAPIErrorCode(wrongRun) != "owner_ineligible" {
		t.Fatalf("bind sibling run = %d %s, want 422 owner_ineligible", wrongRun.code, wrongRun.raw)
	}
	if lease := f.lease(t); lease.State != workLeaseVacant || lease.Fence != 0 {
		t.Fatalf("sibling-run refusal moved lease: %#v", lease)
	}

	// The inverse crossing is independent: the request names the correct
	// SID/run pair, but the bearer was minted for another runtime generation.
	// Alias validation alone accepts this unless Principal preserves RunRef.
	wrongBearer := k2AgentTokenForRunAtFence(
		t, h, tenant, agentRef, sidA, auth.RoleEditor, runB, 1,
	)
	crossedCredential := f.acquire(wrongBearer, map[string]any{"holder_run_ref": runA})
	if crossedCredential.code != http.StatusUnprocessableEntity ||
		workAPIErrorCode(crossedCredential) != "owner_ineligible" {
		t.Fatalf("crossed credential run = %d %s, want 422 owner_ineligible",
			crossedCredential.code, crossedCredential.raw)
	}

	// A caller cannot author the informational agent link either.
	forged := f.acquire(f.driver, map[string]any{
		"holder_run_ref": runA, "holder_agent_ref": model.NewID().String(),
	})
	if forged.code != http.StatusUnprocessableEntity || workAPIErrorCode(forged) != "owner_ineligible" {
		t.Fatalf("forged holder_agent_ref = %d %s, want 422 owner_ineligible", forged.code, forged.raw)
	}

	// NO-FIRE direction for session authentication: the same agent identity and
	// exact run, but no session binding on its credential, still cannot acquire.
	agentWide := k2AgentToken(t, h, tenant, agentRef, "", auth.RoleEditor)
	unscoped := f.acquire(agentWide, map[string]any{"holder_run_ref": runA})
	if unscoped.code != http.StatusUnprocessableEntity || workAPIErrorCode(unscoped) != "owner_ineligible" {
		t.Fatalf("agent-wide acquire = %d %s, want 422 owner_ineligible", unscoped.code, unscoped.raw)
	}

	got := f.acquire(f.driver, map[string]any{"holder_run_ref": runA})
	if got.code != http.StatusOK {
		t.Fatalf("exact SID/run acquire = %d %s", got.code, got.raw)
	}
	lease := f.lease(t)
	if lease.State != workLeaseActive || lease.HolderSID != sidA ||
		lease.HolderRunRef != runA || lease.HolderAgentRef != agentRef {
		t.Fatalf("bound lease = %#v", lease)
	}
	run, err := m.loadRun(context.Background(), tenant, runA)
	if err != nil {
		t.Fatalf("load bound run: %v", err)
	}
	if run.String(colRunWorkItemID) != f.itemID || run.Int(colRunWorkLeaseFence) != lease.Fence {
		t.Fatalf("run stamp does not match lease: %#v", run)
	}
}

// TestRuntimeFilesystemWorkspaceDoesNotBecomeWorkLineage guards two different
// workspace domains. sessions.workspace names a governed host filesystem root;
// identity.workspace_id names a core tenant workspace. Until K4 launches from a
// WorkItem, POST /runs has no authoritative core workspace and must stay on the
// default rather than copying an unrelated entity ID.
func TestRuntimeFilesystemWorkspaceDoesNotBecomeWorkLineage(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()))
	workspaceRef := registerTestWorkspace(t, m, tenant, t.TempDir())
	run, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: workspaceRef, Actor: "user:workspace-domain", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("create run in filesystem workspace: %v", err)
	}
	sid, err := m.ResolveSession(context.Background(), tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: run.RunRef, Origin: OriginOperated,
	})
	if err != nil {
		t.Fatalf("resolve operated identity: %v", err)
	}
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		identity, found, err := findIdentity(context.Background(), sc, sid)
		if err != nil {
			return err
		}
		if !found {
			t.Fatal("operated identity was not persisted")
		}
		if !identity.IsNull(colIDWorkspaceID) {
			t.Fatalf("filesystem workspace id leaked into core work lineage: %q",
				identity.String(colIDWorkspaceID))
		}
		return nil
	}); err != nil {
		t.Fatalf("read operated identity: %v", err)
	}
	finishWorkRuntimeRun(t, m, tenant, run.RunRef, runner.lastProc())
}

// TestWorkK2SessionOwnedItemCanBeFailedByItsOwnHolder covers the OTHER place a
// WorkItem asks who its owner is (work_service.go, item.fail). That comparison
// was `principal.ActorKind == owner_kind`, and no principal is ever of kind
// "session" (core/model/audit.go:117-124), so the session half of item.fail was
// unreachable for exactly the reason its lease was.
func TestWorkK2SessionOwnedItemCanBeFailedByItsOwnHolder(t *testing.T) {
	seed := func(t *testing.T) (*k2Fixture, int64) {
		t.Helper()
		f := newK2Fixture(t, "k2-fail-"+model.NewID().String()[:8])
		acquired := f.acquire(f.driver, nil)
		if acquired.code != http.StatusOK {
			t.Fatalf("acquire = %d %s", acquired.code, acquired.raw)
		}
		f.version = int64(acquired.body["version"].(float64))
		return f, f.lease(t).Fence
	}

	t.Run("a stranger cannot fail somebody else's session work", func(t *testing.T) {
		f, fence := seed(t)
		got := f.apply(http.MethodPost, "/transitions", f.stranger, map[string]any{
			"command": "item.fail", "code": "test_failed",
			"reason":     "A caller that does not drive the owning session reports failure.",
			"holder_sid": f.sid, "fence": fence,
		})
		if got.code == http.StatusOK {
			t.Fatalf("a non-driving agent failed a session-owned item: %s", got.raw)
		}
		if got.code != http.StatusForbidden || workAPIErrorCode(got) != "forbidden" {
			t.Fatalf("stranger item.fail = %d %s, want 403 forbidden", got.code, got.raw)
		}
	})

	t.Run("the owning session fails its own work", func(t *testing.T) {
		f, fence := seed(t)
		got := f.apply(http.MethodPost, "/transitions", f.driver, map[string]any{
			"command": "item.fail", "code": "test_failed",
			"reason":     "The owning session reports a failed implementation.",
			"holder_sid": f.sid, "fence": fence,
		})
		if got.code != http.StatusOK {
			t.Fatalf("owning session item.fail = %d %s, want 200", got.code, got.raw)
		}
		if status, _ := got.body["status"].(string); status != "failed" {
			t.Fatalf("failed status = %q (%s)", status, got.raw)
		}
	})
}

// TestWorkK2AgentOwnedLeaseIsNotLoosened is the third acceptance row: the fix
// must not widen the agent half. An agent-owned item stays reachable only by the
// agent that owns it, through a session that agent provably drives.
func TestWorkK2AgentOwnedLeaseIsNotLoosened(t *testing.T) {
	f := newK2Fixture(t, "k2-agent-branch")
	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
		map[string]any{
			"workspace_id": f.workspace.String(), "work_kind": "implementation",
			"title": "agent-owned execution authority", "brief_md": "The owning agent keeps its lease.",
			"context_refs": []any{}, "priority": "p1",
			"owner_kind": "agent", "owner_ref": f.driverRef,
			"provenance_kind": "human", "provenance_ref": "test:k2-agent-branch",
			"acceptance": []any{map[string]any{
				"criterion_key": "owned", "ordinal": 0,
				"statement": "The owning agent acquires.", "required": true,
			}},
		},
		workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	agentItem, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || agentItem == "" {
		t.Fatalf("create agent-owned WorkItem = %d %s", created.code, created.raw)
	}
	f.itemID, f.version = agentItem, int64(created.body["version"].(float64))
	ready := f.apply(http.MethodPost, "/transitions", f.admin, map[string]any{"command": "item.ready"})
	if ready.code != http.StatusOK {
		t.Fatalf("ready agent-owned WorkItem = %d %s", ready.code, ready.raw)
	}
	f.version = int64(ready.body["version"].(float64))

	// A sibling session of a DIFFERENT agent must not reach it, even though it
	// is a perfectly valid session with lease:write.
	strangerSID, err := f.h.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "k2-http", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve stranger sid: %v", err)
	}
	if _, err := f.h.m.Claim(
		context.Background(), f.tenant, strangerSID, "k2-agent-owner-foreign", 0,
	); err != nil {
		t.Fatalf("claim stranger sid: %v", err)
	}
	f.resolver.drive(strangerSID, f.strangerRef)
	f.resolver.admit("session", strangerSID)
	foreign := k2AgentToken(
		t, f.h, f.tenant, f.strangerRef, strangerSID, auth.RoleEditor,
	)
	denied := f.apply(http.MethodPost, "/lease/acquire", foreign, map[string]any{
		"holder_sid": strangerSID, "ttl_seconds": 60,
	})
	if denied.code == http.StatusOK {
		t.Fatalf("a foreign agent acquired an agent-owned lease: %s", denied.raw)
	}

	// The owning agent, through a session it drives, still acquires.
	got := f.apply(http.MethodPost, "/lease/acquire", f.driver, map[string]any{
		"holder_sid": f.sid, "ttl_seconds": 60,
	})
	if got.code != http.StatusOK {
		t.Fatalf("owning agent lease.acquire = %d %s, want 200", got.code, got.raw)
	}
	if lease := f.lease(t); lease.HolderAgentRef != f.driverRef || lease.State != workLeaseActive {
		t.Fatalf("agent-owned lease = %#v", lease)
	}
}

// TestWorkK2CanonicalAgentOwnerCompletesFailureLoop pins the two namespaces the
// production composition root bridges: WorkItem.owner_ref is Identity.ID, while
// the authenticated token's AgentIdentity is Identity.ExternalID. The exact
// session-to-owner proof, not a direct string comparison, authorizes both lease
// possession and item.fail; the lease keeps the canonical owner reference.
func TestWorkK2CanonicalAgentOwnerCompletesFailureLoop(t *testing.T) {
	f := newK2Fixture(t, "k2-canonical-agent-owner")
	ownerID := model.NewID().String()
	externalID := "agent-external:" + model.NewID().String()
	f.resolver.admit("agent", ownerID)
	f.resolver.drive(f.sid, ownerID)
	f.resolver.authenticate(externalID, ownerID)
	driver := k2AgentToken(t, f.h, f.tenant, externalID, f.sid, auth.RoleEditor)

	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
		map[string]any{
			"workspace_id": f.workspace.String(), "work_kind": "implementation",
			"title":        "canonical agent-owned execution",
			"brief_md":     "The external agent identity acts for its canonical owner row.",
			"context_refs": []any{}, "priority": "p1",
			"owner_kind": "agent", "owner_ref": ownerID,
			"provenance_kind": "human", "provenance_ref": "test:k2-canonical-owner",
			"acceptance": []any{map[string]any{
				"criterion_key": "failure", "ordinal": 0,
				"statement": "The canonical owner can report failure.", "required": true,
			}},
		},
		workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("create canonical agent WorkItem = %d %s", created.code, created.raw)
	}
	f.itemID, f.version = itemID, int64(created.body["version"].(float64))
	ready := f.apply(http.MethodPost, "/transitions", f.admin, map[string]any{"command": "item.ready"})
	if ready.code != http.StatusOK {
		t.Fatalf("ready canonical agent WorkItem = %d %s", ready.code, ready.raw)
	}
	f.version = int64(ready.body["version"].(float64))

	// A caller cannot choose another canonical owner spelling for the durable
	// holder tuple, even while it presents the exact authenticated SID.
	forged := f.apply(http.MethodPost, "/lease/acquire", driver, map[string]any{
		"holder_sid": f.sid, "holder_agent_ref": model.NewID().String(), "ttl_seconds": 60,
	})
	if forged.code != http.StatusUnprocessableEntity || workAPIErrorCode(forged) != "owner_ineligible" {
		t.Fatalf("forged canonical holder = %d %s, want 422 owner_ineligible", forged.code, forged.raw)
	}

	acquired := f.apply(http.MethodPost, "/lease/acquire", driver, map[string]any{
		"holder_sid": f.sid, "ttl_seconds": 60,
	})
	if acquired.code != http.StatusOK {
		t.Fatalf("canonical owner acquire = %d %s, want 200", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	lease := f.lease(t)
	if lease.HolderAgentRef != ownerID || lease.HolderAgentRef == externalID {
		t.Fatalf("agent lease holder ref = %q, want canonical %q (external %q)",
			lease.HolderAgentRef, ownerID, externalID)
	}

	// Same authenticated agent, different session: AgentIdentity equality is
	// not enough to become the holder or owner responsible.
	siblingSID, err := f.h.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "k2-http", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve canonical-owner sibling: %v", err)
	}
	if _, err := f.h.m.Claim(context.Background(), f.tenant, siblingSID, "canonical-owner-sibling", 0); err != nil {
		t.Fatalf("claim canonical-owner sibling: %v", err)
	}
	f.resolver.drive(siblingSID, ownerID)
	sibling := k2AgentToken(t, f.h, f.tenant, externalID, siblingSID, auth.RoleEditor)
	denied := f.apply(http.MethodPost, "/transitions", sibling, map[string]any{
		"command": "item.fail", "code": "sibling_refused",
		"reason":     "A sibling session must not report failure for the live holder.",
		"holder_sid": f.sid, "fence": lease.Fence,
	})
	if denied.code != http.StatusForbidden || workAPIErrorCode(denied) != "forbidden" {
		t.Fatalf("sibling item.fail = %d %s, want 403 forbidden", denied.code, denied.raw)
	}

	failed := f.apply(http.MethodPost, "/transitions", driver, map[string]any{
		"command": "item.fail", "code": "owner_reported_failure",
		"reason":     "The canonical agent owner reports its execution failure.",
		"holder_sid": f.sid, "fence": lease.Fence,
	})
	if failed.code != http.StatusOK || failed.body["status"] != "failed" {
		t.Fatalf("canonical owner item.fail = %d %s, want 200 failed", failed.code, failed.raw)
	}
}

// TestWorkK2CanonicalAgentOwnerCanFailWithoutALiveLease proves owner authority
// remains reachable in blocked/review after the execution lease has ended. The
// caller carries only the authenticated ExternalID, while owner_ref stays the
// canonical Identity.ID. A sibling ExternalID is the non-trigger direction.
func TestWorkK2CanonicalAgentOwnerCanFailWithoutALiveLease(t *testing.T) {
	for _, state := range []string{"blocked", "review"} {
		state := state
		t.Run(state, func(t *testing.T) {
			f := newK2Fixture(t, "k2-canonical-agent-no-lease-"+state)
			ownerID := model.NewID().String()
			externalID := "agent-external:" + model.NewID().String()
			siblingExternal := "agent-external:" + model.NewID().String()
			f.resolver.admit("agent", ownerID)
			f.resolver.authenticate(externalID, ownerID)
			f.resolver.drive(f.sid, ownerID)
			driver := k2AgentToken(t, f.h, f.tenant, externalID, "", auth.RoleEditor)
			sibling := k2AgentToken(t, f.h, f.tenant, siblingExternal, "", auth.RoleEditor)

			created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
				map[string]any{
					"workspace_id": f.workspace.String(), "work_kind": "implementation",
					"title":        "canonical owner without live lease " + state,
					"brief_md":     "The canonical agent owner reports a terminal failure.",
					"context_refs": []any{}, "priority": "p1",
					"owner_kind": "agent", "owner_ref": ownerID,
					"provenance_kind": "human", "provenance_ref": "test:k2-owner-no-lease",
					"acceptance": []any{map[string]any{
						"criterion_key": "terminal", "ordinal": 0,
						"statement": "The owner can report terminal failure.", "required": true,
					}},
				},
				workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
			itemID, _ := created.body["result_id"].(string)
			if created.code != http.StatusOK || itemID == "" {
				t.Fatalf("create no-lease WorkItem = %d %s", created.code, created.raw)
			}
			f.itemID, f.version = itemID, int64(created.body["version"].(float64))
			ready := f.apply(http.MethodPost, "/transitions", f.admin,
				map[string]any{"command": "item.ready"})
			if ready.code != http.StatusOK {
				t.Fatalf("ready no-lease WorkItem = %d %s", ready.code, ready.raw)
			}
			f.version = int64(ready.body["version"].(float64))

			switch state {
			case "blocked":
				blocked := f.apply(http.MethodPost, "/transitions", f.admin, map[string]any{
					"command": "item.block", "code": "external_wait",
					"reason": "The item is waiting without an execution lease.",
				})
				if blocked.code != http.StatusOK || blocked.body["status"] != "blocked" {
					t.Fatalf("seed blocked = %d %s", blocked.code, blocked.raw)
				}
				f.version = int64(blocked.body["version"].(float64))
			case "review":
				executor := k2AgentToken(t, f.h, f.tenant, externalID, f.sid, auth.RoleEditor)
				acquired := f.apply(http.MethodPost, "/lease/acquire", executor, map[string]any{
					"holder_sid": f.sid, "ttl_seconds": 60,
				})
				if acquired.code != http.StatusOK {
					t.Fatalf("seed review acquire = %d %s", acquired.code, acquired.raw)
				}
				f.version = int64(acquired.body["version"].(float64))
				fence := f.lease(t).Fence
				evaluated := k2RestrictedEvaluate(t, f, executor, fence, "passed")
				if evaluated.code != http.StatusOK {
					t.Fatalf("seed review acceptance = %d %s", evaluated.code, evaluated.raw)
				}
				f.version = int64(evaluated.body["version"].(float64))
				submitted := k2RestrictedTransition(f, executor, "item.submit", fence)
				if submitted.code != http.StatusOK || submitted.body["status"] != "review" {
					t.Fatalf("seed review submit = %d %s", submitted.code, submitted.raw)
				}
				f.version = int64(submitted.body["version"].(float64))
			}
			if lease := f.lease(t); lease.State == workLeaseActive {
				t.Fatalf("%s item unexpectedly has a live lease: %#v", state, lease)
			}

			denied := f.apply(http.MethodPost, "/transitions", sibling, map[string]any{
				"command": "item.fail", "code": "sibling_refused",
				"reason": "An unrelated authenticated agent must not become the owner.",
			})
			if denied.code != http.StatusForbidden || workAPIErrorCode(denied) != "forbidden" {
				t.Fatalf("%s sibling item.fail = %d %s, want 403", state, denied.code, denied.raw)
			}

			failed := f.apply(http.MethodPost, "/transitions", driver, map[string]any{
				"command": "item.fail", "code": "owner_reported_failure",
				"reason": "The authenticated canonical owner reports terminal failure.",
			})
			if failed.code != http.StatusOK || failed.body["status"] != "failed" {
				t.Fatalf("%s canonical owner item.fail = %d %s, want 200 failed",
					state, failed.code, failed.raw)
			}
		})
	}
}

// TestWorkK2SessionOwnedLeaseKeepsItsAdministrativeRecovery is the direction the
// holder proof must NOT close, and it was closed until an adversarial contrast
// caught it. The decision recorded on sessionHolderProven refuses a user actor
// precisely BECAUSE lease.takeover is the human path; a proof that also blocked
// takeover would have left a session-owned item with no administrative recovery
// at all, and made the change contradict its own justification.
func TestWorkK2SessionOwnedLeaseKeepsItsAdministrativeRecovery(t *testing.T) {
	f := newK2Fixture(t, "k2-admin-recovery")
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("seed acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	// NO-FIRE: a non-admin still cannot take over, so this is not "takeover is open".
	denied := f.apply(http.MethodPost, "/lease/takeover", f.stranger, map[string]any{
		"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60,
	})
	if denied.code == http.StatusOK {
		t.Fatalf("a non-admin took over a session-owned lease: %s", denied.raw)
	}

	// The admin reaches its own authorization instead of being refused as an
	// ineligible holder. A live fence additionally demands Force + a Decision, so
	// lease_held here means the administrative path is OPEN and its own guards
	// are what answer.
	got := f.apply(http.MethodPost, "/lease/takeover", f.admin, map[string]any{
		"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60,
	})
	if code := workAPIErrorCode(got); code == "owner_ineligible" {
		t.Fatalf("admin takeover refused as an ineligible holder = %d %s", got.code, got.raw)
	}
	if got.code != http.StatusConflict || workAPIErrorCode(got) != "lease_held" {
		t.Fatalf("admin takeover of a live lease = %d %s, want 409 lease_held", got.code, got.raw)
	}
}

// TestWorkK2SessionOwnerRefMustBeACanonicalSID pins the door check. Without it
// the mutant that accepts a bare uuid as a session owner_ref survives, because
// every other test already passes a well-formed sid — a hardening nothing
// exercises is a hardening nobody can rely on.
//
// The shape matters beyond tidiness: a bare uuid and a canonical sid are
// indistinguishable strings once "osn_" is gone, and treating one as the other
// is exactly what sent a canonical sid to a core model.Session lookup and left
// owner_kind="session" unreachable. Refusing it at the door means the ambiguity
// cannot be created, rather than being resolved differently by each reader.
func TestWorkK2SessionOwnerRefMustBeACanonicalSID(t *testing.T) {
	f := newK2Fixture(t, "k2-owner-ref-shape")
	body := func(ownerRef string) map[string]any {
		return map[string]any{
			"workspace_id": f.workspace.String(), "work_kind": "implementation",
			"title": "owner ref shape", "brief_md": "The session owner ref must be canonical.",
			"context_refs": []any{}, "priority": "p1",
			"owner_kind": "session", "owner_ref": ownerRef,
			"provenance_kind": "human", "provenance_ref": "test:owner-ref-shape",
			"acceptance": []any{map[string]any{
				"criterion_key": "shape", "ordinal": 0,
				"statement": "The owner ref is a canonical sid.", "required": true,
			}},
		}
	}
	create := func(ownerRef string) resp {
		return f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.admin,
			body(ownerRef),
			workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	}

	bare := strings.TrimPrefix(f.sid, sidPrefix)
	if got := create(bare); got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" {
		t.Fatalf("bare uuid as a session owner_ref = %d %s, want 400 invalid_command", got.code, got.raw)
	}
	if got := create("osn_not-a-uuid"); got.code != http.StatusBadRequest {
		t.Fatalf("malformed sid as a session owner_ref = %d %s, want 400", got.code, got.raw)
	}
	// NO-FIRE: the canonical sid is accepted, so this is a shape check and not a
	// refusal of every session owner.
	if got := create(f.sid); got.code != http.StatusOK {
		t.Fatalf("canonical sid as a session owner_ref = %d %s, want 200", got.code, got.raw)
	}
}

// TestWorkK2AgentTokenRunCarriesItsAuthenticatedAgent closes the link that made
// the whole chain unreachable, and it is not only about work ownership.
//
// An agent-OBO token authenticates as kind "token" with actor "token:<cred-id>"
// — correct evidence, and exactly why the agent dimension cannot be recovered
// from Actor/ActorKind. The runtime derived agent_ref from ActorKind alone, so a
// run launched over HTTP by an agent stored NULL. Two consequences, and the
// second is the serious one: SessionActsForAgent could not recognize the driver,
// AND an agent-scoped EMERGENCY STOP did not match the run, because the
// kill-switch decides scope on exactly this value. The empty column was the
// dangerous state; leaving it alone would have been protecting the hole.
func TestWorkK2AgentTokenRunCarriesItsAuthenticatedAgent(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k2-agent-run-attribution")
	agentRef := model.NewID().String()
	agentToken := k2AgentToken(t, h, tenant, agentRef, "", auth.RoleAdmin)

	runRef := func(token string) string {
		t.Helper()
		created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", token, map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		}, tenantHdr(tenant))
		ref, _ := created.body["run_ref"].(string)
		if created.code != http.StatusCreated || ref == "" {
			t.Fatalf("create run = %d %s", created.code, created.raw)
		}
		return ref
	}
	storedAgentRef := func(ref string) string {
		t.Helper()
		var got string
		if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rec, err := findRunRec(context.Background(), repo, ref)
			if err != nil {
				return err
			}
			got = rec.String(colRunAgentRef)
			return nil
		}); err != nil {
			t.Fatalf("read run %s: %v", ref, err)
		}
		return got
	}

	byAgent := runRef(agentToken)
	if got := storedAgentRef(byAgent); got != agentRef {
		t.Fatalf("agent-token run stored agent_ref %q, want %q", got, agentRef)
	}
	// The kill-switch scopes on this exact value, so the run is now reachable by
	// an agent-scoped emergency stop instead of only by an estate-wide one.
	if lr, ok := m.rt.getLive(tenant, byAgent); !ok || lr.agentRef != agentRef {
		t.Fatalf("live handle carries agent_ref %q, want %q", lr.agentRef, agentRef)
	}

	// NO-FIRE: a human run is still attributed to nobody, so this records an
	// authenticated identity rather than attributing every run to some agent.
	if got := storedAgentRef(runRef(admin)); got != "" {
		t.Fatalf("human run stored agent_ref %q, want empty", got)
	}

	// And the audit actor is untouched: the agent dimension is a SEPARATE fact,
	// not a rewrite of who the credential was.
	events := h.do(http.MethodGet, "/v1/m/sessions/runs/"+byAgent, agentToken, tenantHdr(tenant))
	if events.code != http.StatusOK {
		t.Fatalf("read run dto = %d %s", events.code, events.raw)
	}
	if got, _ := events.body["agent_ref"].(string); got != agentRef {
		t.Fatalf("run dto agent_ref = %q, want %q", got, agentRef)
	}
}
