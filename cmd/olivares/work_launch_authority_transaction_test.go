// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// These regressions drive the agent-owned work launch through the REAL
// composition: boot(), the production workIdentityResolver installed by boot,
// governance's lifecycle rows and sessions.LaunchForWork. A scripted resolver
// opens no View, which is how the nested authority View stayed invisible to the
// module suites while SQLite's single core connection deadlocked on it.

const (
	// workLaunchAuthorityBound limits only the launch phase, after the estate
	// is booted and seeded. A healthy launch needs a small fraction of it.
	workLaunchAuthorityBound = 30 * time.Second
	// workLaunchAuthorityStackAt captures goroutine stacks while a stalled
	// launch is still inside its nested store read, before the deadline ends it.
	workLaunchAuthorityStackAt = 20 * time.Second
	workLaunchAuthorityJoin    = 15 * time.Second
)

type workLaunchAuthorityEstate struct {
	eng    *engine
	tenant model.TenantID
	marker string
}

type workLaunchAuthorityOwner struct {
	workspace     model.ID
	ownerID       model.ID
	ownerExternal string
	sponsorID     model.ID
	item          model.ID
}

func bootWorkLaunchAuthorityEstate(
	t *testing.T,
	backing communicationHTTPTestStore,
) workLaunchAuthorityEstate {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched.pids")
	script := filepath.Join(dir, "claude-stand-in")
	// The stand-in records that the runtime really executed it, then lives
	// until the runtime closes its stdin.
	standIn := "#!/bin/sh\nprintf '%s\\n' \"$$\" >> '" + marker + "'\nexec cat >/dev/null\n"
	if err := os.WriteFile(script, []byte(standIn), 0o700); err != nil {
		t.Fatalf("write runtime stand-in: %v", err)
	}
	token := filepath.Join(dir, "runtime.token")
	if err := os.WriteFile(token, []byte("work-launch-authority-stand-in-token\n"), 0o600); err != nil {
		t.Fatalf("write runtime token: %v", err)
	}
	t.Setenv(envSessionClaudeBin, script)
	t.Setenv(envSessionTokenFile, token)

	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), backing.bootConfig())
	if err != nil {
		t.Fatalf("boot work launch authority estate: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.sessionsMod == nil {
		t.Fatal("boot composed no sessions module")
	}

	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	handler := eng.api.Handler()
	if code, _, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": setupToken, "email": "root@work-launch-authority.test",
		"password": "work-launch-authority-root-password",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	code, login, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "root@work-launch-authority.test", "password": "work-launch-authority-root-password",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	adminToken, _ := login["token"].(string)
	code, org, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/system/orgs", adminToken, "",
		map[string]any{"name": "work launch authority", "slug": "work-launch-authority"})
	if code != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", code, raw)
	}
	tenantID, _ := org["tenant_id"].(string)
	if tenantID == "" {
		t.Fatalf("created tenant has no id: %s", raw)
	}
	return workLaunchAuthorityEstate{eng: eng, tenant: model.TenantID(tenantID), marker: marker}
}

// seedWorkLaunchAuthorityOwner writes the legitimate authority facts the real
// resolver reads: a canonical agent identity active in its workspace, a human
// sponsor and the governance lifecycle naming both. It then creates and readies
// the agent-owned WorkItem through the kernel.
func seedWorkLaunchAuthorityOwner(
	t *testing.T,
	e workLaunchAuthorityEstate,
	slug string,
) workLaunchAuthorityOwner {
	t.Helper()
	ctx := context.Background()
	var owner workLaunchAuthorityOwner
	if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "work launch authority " + slug, Slug: slug, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		owner.workspace = ws.ID
		return nil
	}); err != nil {
		t.Fatalf("seed work launch workspace: %v", err)
	}
	owner.ownerID, owner.ownerExternal, owner.sponsorID = seedWorkAuthorityAgent(t, e, owner.workspace)

	setup := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "wla-test-setup",
		Actor: "system:wla-test-setup", Admin: true,
	}
	created, err := e.eng.sessionsMod.Apply(ctx, e.tenant, setup, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: owner.workspace, WorkKind: "implementation",
		Title: "Launch under current agent authority", BriefMD: "Run under the durable lease.",
		ContextRefs: []sessions.ContextRef{}, Priority: "p1",
		OwnerKind: "agent", OwnerRef: owner.ownerID.String(),
		ProvenanceKind: "workflow", ProvenanceRef: "test:work-launch-authority",
		Acceptance: []sessions.AcceptanceInput{{
			Key: "runtime", Ordinal: 0, Statement: "The managed runtime starts.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create agent-owned WorkItem: %v", err)
	}
	ready, err := e.eng.sessionsMod.Apply(ctx, e.tenant, setup, sessions.WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID, ExpectedVersion: created.Version,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready agent-owned WorkItem = %#v, %v", ready, err)
	}
	owner.item = created.ResultID
	return owner
}

// seedWorkAuthorityAgent writes one canonical agent active in workspace, its
// enabled human sponsor and the governance lifecycle naming both.
func seedWorkAuthorityAgent(
	t *testing.T,
	e workLaunchAuthorityEstate,
	workspace model.ID,
) (ownerID model.ID, ownerExternal string, sponsorID model.ID) {
	t.Helper()
	ctx := context.Background()
	ownerExternal = "agent:wla-" + model.NewID().String()
	sponsorExternal := "human:wla-sponsor-" + model.NewID().String()
	if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(ctx, model.Identity{
			Name: "work launch executor", Kind: "agent_nhi",
			ExternalID: ownerExternal, Provider: "test",
		})
		if err != nil {
			return err
		}
		ownerID = identity.ID
		if _, err := sc.Agents().Create(ctx, model.Agent{
			Name: "work launch executor", Kind: "test", ExternalID: ownerExternal,
			Status: model.StatusActive, IdentityID: identity.ID, WorkspaceID: workspace,
		}); err != nil {
			return err
		}
		sponsor, err := sc.Identities().Create(ctx, model.Identity{
			Name: "work launch sponsor", Kind: "user", ExternalID: sponsorExternal,
			Provider: "test", Metadata: map[string]any{"principal_type": "human", "disabled": false},
		})
		if err != nil {
			return err
		}
		sponsorID = sponsor.ID
		lifecycle, err := sc.Ext("governance.nhi_lifecycle")
		if err != nil {
			return err
		}
		_, err = lifecycle.Create(ctx, model.Record{
			"identity_ref": ownerExternal, "source": "test", "criticality": "high",
			"sponsor_ref": sponsorExternal, "max_age_seconds": int64(0),
			"staleness_status": "unknown", "enforcement": "monitor",
			"orphaned": false, "offboard_state": "none", "kind": "agent",
		})
		return err
	}); err != nil {
		t.Fatalf("seed work authority agent facts: %v", err)
	}
	return ownerID, ownerExternal, sponsorID
}

type workLaunchAuthorityOutcome struct {
	managed sessions.ManagedRunRef
	err     error
}

// launchWorkWithinBound runs one LaunchForWork under a finite context and
// always joins the call before returning. If the call is still blocked when the
// stack capture point arrives, the goroutines that hold the work kernel or the
// authority observer are logged so a stall is diagnosable from the test output.
func launchWorkWithinBound(
	t *testing.T,
	e workLaunchAuthorityEstate,
	spec sessions.WorkLaunchSpec,
) workLaunchAuthorityOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), workLaunchAuthorityBound)
	defer cancel()
	done := make(chan workLaunchAuthorityOutcome, 1)
	started := time.Now()
	go func() {
		managed, err := e.eng.sessionsMod.LaunchForWork(ctx, e.tenant, spec)
		done <- workLaunchAuthorityOutcome{managed: managed, err: err}
	}()
	capture := time.NewTimer(workLaunchAuthorityStackAt)
	defer capture.Stop()
	select {
	case out := <-done:
		t.Logf("LaunchForWork returned after %s", time.Since(started).Round(time.Millisecond))
		return out
	case <-capture.C:
		t.Logf("LaunchForWork still blocked after %s; work authority goroutines:\n%s",
			workLaunchAuthorityStackAt, workAuthorityGoroutines())
	}
	select {
	case out := <-done:
		t.Logf("LaunchForWork returned after %s", time.Since(started).Round(time.Millisecond))
		return out
	case <-time.After(workLaunchAuthorityBound - workLaunchAuthorityStackAt + workLaunchAuthorityJoin):
	}
	cancel()
	select {
	case out := <-done:
		t.Logf("LaunchForWork returned after cancellation at %s", time.Since(started).Round(time.Millisecond))
		return out
	case <-time.After(workLaunchAuthorityJoin):
		t.Fatalf("LaunchForWork did not return after its deadline and cancellation; goroutines:\n%s",
			workAuthorityGoroutines())
	}
	return workLaunchAuthorityOutcome{}
}

func workAuthorityGoroutines() string {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, block := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(block, "ObserveAgentWorkAuthority") ||
			strings.Contains(block, "sessions.(*Module).applyWithData") ||
			strings.Contains(block, "sessions.(*Module).planWithData") {
			out = append(out, block)
		}
	}
	if len(out) == 0 {
		return "(no goroutine is inside the work kernel or the authority observer)"
	}
	return strings.Join(out, "\n\n")
}

func readLaunchedPIDs(t *testing.T, marker string) []int {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read launched process marker: %v", err)
	}
	var pids []int
	for _, line := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid < 1 {
			t.Fatalf("launched process marker holds %q", line)
		}
		pids = append(pids, pid)
	}
	return pids
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitForWorkLaunchCondition(t *testing.T, bound time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, bound)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exerciseBootWorkLaunchAuthority(t *testing.T, backing communicationHTTPTestStore) {
	e := bootWorkLaunchAuthorityEstate(t, backing)
	owner := seedWorkLaunchAuthorityOwner(t, e, "wla-launch")
	ctx := context.Background()
	profile, err := e.eng.sessionsMod.CreateProfile(ctx, e.tenant, sessions.CreateProfileInput{
		Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "work launch authority", AuthSource: sessions.AuthSourceAccountHome,
	})
	if err != nil {
		t.Fatalf("provider profile: %v", err)
	}
	spec := sessions.WorkLaunchSpec{
		WorkItemID: owner.item, AuditActorRef: owner.ownerExternal,
		Runtime: sessions.CreateRunParams{
			Name: "wla-managed", Transport: sessions.TransportStreamJSON,
			Isolation: sessions.IsolationNative, Actor: owner.ownerExternal,
			ActorKind: model.ActorAgent, AgentRef: owner.ownerExternal,
			ProviderProfileRef: profile.Ref,
		},
	}
	// Any child the runtime starts is joined here even when an assertion fails.
	t.Cleanup(func() {
		for _, pid := range readLaunchedPIDs(t, e.marker) {
			if processAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	out := launchWorkWithinBound(t, e, spec)
	if out.err != nil {
		t.Fatalf("agent-owned LaunchForWork through the production resolver: %v "+
			"(deadline=%v, launched processes=%d)",
			out.err, errors.Is(out.err, context.DeadlineExceeded), len(readLaunchedPIDs(t, e.marker)))
	}
	managed := out.managed
	if managed.Replayed || managed.RunRef == "" || !strings.HasPrefix(managed.SessionID, "osn_") ||
		managed.WorkItemID != owner.item || managed.WorkspaceID != owner.workspace ||
		managed.WorkLeaseFence < 1 || managed.OwnerEpoch < 1 || managed.DispatchKey == "" ||
		managed.State != "running" {
		t.Fatalf("managed work run = %#v", managed)
	}
	lease, err := e.eng.sessionsMod.GetLease(ctx, e.tenant, sessions.WorkPrincipal{}, owner.item)
	if err != nil {
		t.Fatalf("read bound WorkLease: %v", err)
	}
	if !lease.Live || lease.Fence != managed.WorkLeaseFence || lease.HolderSID != managed.SessionID ||
		lease.HolderRunRef != managed.RunRef || lease.HolderAgentRef != owner.ownerID.String() ||
		lease.WorkspaceID != owner.workspace {
		t.Fatalf("WorkLease is not bound to the launched run: %#v", lease)
	}
	var pid int
	waitForWorkLaunchCondition(t, 10*time.Second, "the runtime stand-in execution", func() bool {
		pids := readLaunchedPIDs(t, e.marker)
		if len(pids) == 1 {
			pid = pids[0]
		}
		return pid != 0
	})
	if !processAlive(pid) {
		t.Fatalf("launched stand-in process %d is not running", pid)
	}

	// Exact dispatch replay returns the durable run and never spawns again.
	replay := launchWorkWithinBound(t, e, spec)
	if replay.err != nil {
		t.Fatalf("exact LaunchForWork replay: %v", replay.err)
	}
	if !replay.managed.Replayed || replay.managed.RunRef != managed.RunRef ||
		replay.managed.SessionID != managed.SessionID || replay.managed.DispatchKey != managed.DispatchKey {
		t.Fatalf("exact replay diverged: first=%#v replay=%#v", managed, replay.managed)
	}

	stopCtx, cancel := context.WithTimeout(ctx, workLaunchAuthorityBound)
	defer cancel()
	if err := e.eng.sessionsMod.StopForWork(
		stopCtx, e.tenant, managed.RunRef, managed.WorkLeaseFence, "work launch authority regression complete",
	); err != nil {
		t.Fatalf("StopForWork: %v", err)
	}
	waitForWorkLaunchCondition(t, workLaunchAuthorityJoin, "the stand-in process exit", func() bool {
		return !processAlive(pid)
	})
	if pids := readLaunchedPIDs(t, e.marker); len(pids) != 1 {
		t.Fatalf("launched stand-in processes = %v, want exactly the first launch", pids)
	}
}

// TestBootWorkLaunchAuthoritySingleConnection is the SQLite regression for the
// nested authority View. The estate keeps SQLite's unchanged one-connection
// store; a launch that reads tenant-wide authority while still holding the
// WorkItem View cannot obtain a second connection and never binds.
func TestBootWorkLaunchAuthoritySingleConnection(t *testing.T) {
	exerciseBootWorkLaunchAuthority(t, communicationHTTPTestSQLiteStore(t))
}

// TestBootWorkLaunchAuthorityPostgres runs the same composed launch on the
// existing isolated PostgreSQL fixture.
func TestBootWorkLaunchAuthorityPostgres(t *testing.T) {
	exerciseBootWorkLaunchAuthority(t, communicationHTTPTestPostgresStore(t))
}

// workCommandCode reads the published work code from a kernel error. The
// kernel renders its errors as "<code>" or "<code>: <cause>".
func workCommandCode(err error) string {
	if err == nil {
		return ""
	}
	code, _, _ := strings.Cut(err.Error(), ": ")
	return code
}

func workAuthorityBoundContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), workLaunchAuthorityBound)
	t.Cleanup(cancel)
	return ctx
}

// blockWorkAuthorityItem moves the agent-owned item to blocked as an operator,
// so its canonical owner can report failure without holding a lease: a command
// that needs the owner's agent authority and no session fixture.
func blockWorkAuthorityItem(t *testing.T, e workLaunchAuthorityEstate, item model.ID) int64 {
	t.Helper()
	snapshot, err := e.eng.sessionsMod.Get(context.Background(), e.tenant, sessions.WorkPrincipal{}, item)
	if err != nil {
		t.Fatalf("read agent-owned WorkItem: %v", err)
	}
	blocked, err := e.eng.sessionsMod.Apply(context.Background(), e.tenant, sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "wla-operator", Actor: "system:wla-operator", Admin: true,
	}, sessions.WorkCommand{
		Command: "item.block", WorkItemID: item, Code: "operator_blocked",
		Reason:          "The operator blocked the execution before owner failure.",
		ExpectedVersion: snapshot.Item.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || blocked.Status != "blocked" {
		t.Fatalf("block agent-owned WorkItem = %#v, %v", blocked, err)
	}
	return blocked.Version
}

func ownerFailureCommand(item model.ID, version int64) sessions.WorkCommand {
	return sessions.WorkCommand{
		Command: "item.fail", WorkItemID: item, Code: "owner_failure",
		Reason:          "The currently eligible canonical owner reports terminal failure.",
		ExpectedVersion: version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}
}

func agentOwnerPrincipal(externalRef string) sessions.WorkPrincipal {
	return sessions.WorkPrincipal{ActorKind: model.ActorAgent, ActorRef: externalRef, Actor: externalRef}
}

func workAuthorityItemStatus(t *testing.T, e workLaunchAuthorityEstate, item model.ID) (string, int64) {
	t.Helper()
	snapshot, err := e.eng.sessionsMod.Get(context.Background(), e.tenant, sessions.WorkPrincipal{}, item)
	if err != nil {
		t.Fatalf("read WorkItem: %v", err)
	}
	return snapshot.Item.Status, snapshot.Item.Version
}

func workAuthorityBackings() []struct {
	name    string
	backing func(*testing.T) communicationHTTPTestStore
} {
	return []struct {
		name    string
		backing func(*testing.T) communicationHTTPTestStore
	}{
		{name: "sqlite", backing: communicationHTTPTestSQLiteStore},
		{name: "postgres", backing: communicationHTTPTestPostgresStore},
	}
}

// TestWorkPlanAuthorityObservationUsesClosedPreflightView drives Plan and
// Validate for an authority-consuming owner command through the booted kernel
// and the production resolver. On SQLite an observation made inside the
// planning View could never obtain the single connection.
func TestWorkPlanAuthorityObservationUsesClosedPreflightView(t *testing.T) {
	for _, engine := range workAuthorityBackings() {
		t.Run(engine.name, func(t *testing.T) {
			e := bootWorkLaunchAuthorityEstate(t, engine.backing(t))
			owner := seedWorkLaunchAuthorityOwner(t, e, "wla-plan")
			version := blockWorkAuthorityItem(t, e, owner.item)
			sm := e.eng.sessionsMod
			cmd := ownerFailureCommand(owner.item, version)

			plan, err := sm.Plan(workAuthorityBoundContext(t), e.tenant, agentOwnerPrincipal(owner.ownerExternal), cmd)
			if err != nil || plan.Verdict != sessions.VerdictClean || len(plan.PlanHash) != 64 {
				t.Fatalf("owner failure plan = %#v, %v", plan, err)
			}
			assessment, err := sm.Validate(workAuthorityBoundContext(t), e.tenant, agentOwnerPrincipal(owner.ownerExternal), cmd)
			if err != nil || assessment.Verdict != sessions.VerdictClean {
				t.Fatalf("owner failure validate = %#v, %v", assessment, err)
			}
			if status, got := workAuthorityItemStatus(t, e, owner.item); status != "blocked" || got != version {
				t.Fatalf("Plan/Validate wrote the WorkItem: %s v%d, want blocked v%d", status, got, version)
			}

			// The authenticated ExternalID is the only actor spelling admitted.
			forged, err := sm.Plan(workAuthorityBoundContext(t), e.tenant, agentOwnerPrincipal("agent:forged"), cmd)
			if err != nil || forged.Verdict != sessions.VerdictBroken || forged.Code != "owner_ineligible" {
				t.Fatalf("forged actor plan = %#v, %v; want owner_ineligible", forged, err)
			}

			// The plan hash binds the observed authority digest that Apply locks.
			cmd.ExpectedPlanHash = plan.PlanHash
			applied, err := sm.Apply(workAuthorityBoundContext(t), e.tenant, agentOwnerPrincipal(owner.ownerExternal), cmd)
			if err != nil || applied.Status != "failed" || applied.PlanHash != plan.PlanHash {
				t.Fatalf("apply planned owner failure = %#v, %v", applied, err)
			}
		})
	}
}

// barrierWorkIdentityResolver delegates every method to the production
// resolver and runs one concurrent writer right after observation returns,
// the window between the closed preliminary View and the caller's View/Mutate.
type barrierWorkIdentityResolver struct {
	workIdentityResolver
	mu           sync.Mutex
	afterObserve func()
}

var (
	_ sessions.WorkAgentEligibilityInScope     = (*barrierWorkIdentityResolver)(nil)
	_ sessions.WorkAgentAuthorityReadValidator = (*barrierWorkIdentityResolver)(nil)
	_ sessions.WorkAuthenticatedAgentMatcher   = (*barrierWorkIdentityResolver)(nil)
)

func (r *barrierWorkIdentityResolver) ObserveAgentWorkAuthority(
	ctx context.Context, tenant model.TenantID, workspace model.ID, canonicalRef, authenticatedRef string,
) (sessions.WorkAgentAuthoritySnapshot, error) {
	snapshot, err := r.workIdentityResolver.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, canonicalRef, authenticatedRef,
	)
	r.mu.Lock()
	hook := r.afterObserve
	r.afterObserve = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return snapshot, err
}

func (r *barrierWorkIdentityResolver) arm(hook func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterObserve = hook
}

// countingWorkIdentityResolver delegates every call to the production resolver
// and counts the calls that open their own store read (participant, session,
// actor matching and authority observation) or touch authority in the caller's
// transaction.
type countingWorkIdentityResolver struct {
	workIdentityResolver
	storeReads atomic.Int32
	inScope    atomic.Int32
}

var (
	_ sessions.WorkAgentEligibilityInScope     = (*countingWorkIdentityResolver)(nil)
	_ sessions.WorkAgentAuthorityReadValidator = (*countingWorkIdentityResolver)(nil)
	_ sessions.WorkAuthenticatedAgentMatcher   = (*countingWorkIdentityResolver)(nil)
)

func (r *countingWorkIdentityResolver) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (sessions.Participant, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.ResolveParticipant(ctx, tenant, workspace, kind, ref)
}

func (r *countingWorkIdentityResolver) SessionActsForAgent(
	ctx context.Context, tenant model.TenantID, sid, agentRef string,
) (bool, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.SessionActsForAgent(ctx, tenant, sid, agentRef)
}

func (r *countingWorkIdentityResolver) AuthenticatedAgentMatches(
	ctx context.Context, tenant model.TenantID, canonicalRef, authenticatedRef string,
) (bool, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.AuthenticatedAgentMatches(ctx, tenant, canonicalRef, authenticatedRef)
}

func (r *countingWorkIdentityResolver) ObserveAgentWorkAuthority(
	ctx context.Context, tenant model.TenantID, workspace model.ID, canonicalRef, authenticatedRef string,
) (sessions.WorkAgentAuthoritySnapshot, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.ObserveAgentWorkAuthority(ctx, tenant, workspace, canonicalRef, authenticatedRef)
}

func (r *countingWorkIdentityResolver) ValidateAgentWorkAuthorityInScope(
	ctx context.Context, sc store.Scope, snapshot sessions.WorkAgentAuthoritySnapshot,
) error {
	r.inScope.Add(1)
	return r.workIdentityResolver.ValidateAgentWorkAuthorityInScope(ctx, sc, snapshot)
}

func (r *countingWorkIdentityResolver) LockAgentWorkAuthority(
	ctx context.Context, sc store.Scope, snapshot sessions.WorkAgentAuthoritySnapshot,
) error {
	r.inScope.Add(1)
	return r.workIdentityResolver.LockAgentWorkAuthority(ctx, sc, snapshot)
}

// workAuthorityJoinedBound limits each joined replay transaction. A resolver
// read opened beside the joined SQLite transaction can only end at this deadline.
const workAuthorityJoinedBound = 10 * time.Second

var errWorkAuthorityJoinedRollback = errors.New("joined work authority regression rolls back")

// TestWorkAuthorityJoinedReplayRefusesBeforeResolverPreflight runs new
// authority-consuming owner commands inside a real protocol replay transaction.
// The joined parent transaction stays open while its callback runs. On SQLite
// every resolver read beside it waits for the single connection. The kernel
// must refuse from the joined, confined item read before identity preflight.
func TestWorkAuthorityJoinedReplayRefusesBeforeResolverPreflight(t *testing.T) {
	for _, engine := range workAuthorityBackings() {
		t.Run(engine.name, func(t *testing.T) {
			exerciseWorkAuthorityJoinedReplay(t, engine.backing(t))
		})
	}
}

func exerciseWorkAuthorityJoinedReplay(t *testing.T, backing communicationHTTPTestStore) {
	e := bootWorkLaunchAuthorityEstate(t, backing)
	lifecycle, ok := e.eng.nhiEnforcer.(workAgentLifecycle)
	if !ok {
		t.Fatalf("booted governance module %T does not serve agent lifecycle", e.eng.nhiEnforcer)
	}
	resolver := &countingWorkIdentityResolver{workIdentityResolver: workIdentityResolver{
		st: e.eng.store, sessions: e.eng.sessionsMod, agentLifecycle: lifecycle,
	}}
	e.eng.sessionsMod.UseWorkIdentityResolver(resolver)
	sm := e.eng.sessionsMod
	operator := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "wla-operator", Actor: "system:wla-operator", Admin: true,
	}
	claimFor := func(workspace model.ID) sessions.ProtocolReplayClaim {
		return sessions.ProtocolReplayClaim{
			WorkspaceID: workspace, Protocol: sessions.BindingProtocolA2A,
			PeerAuthority: "https://peer.work-authority.test", Kind: sessions.ProtocolReplayJTI,
			ReplayID: "wla-joined-" + model.NewID().String(), ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	// joined runs fn inside one real ApplyProtocolReplay transaction and reports
	// how long it took and how many resolver calls it made.
	joined := func(
		t *testing.T, claim sessions.ProtocolReplayClaim, fn func(context.Context) error,
	) (sessions.ProtocolReplayResult, error, time.Duration, int32, int32) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), workAuthorityJoinedBound)
		defer cancel()
		readsBefore, inScopeBefore := resolver.storeReads.Load(), resolver.inScope.Load()
		started := time.Now()
		result, err := sm.ApplyProtocolReplay(ctx, e.tenant, claim,
			func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
				return sessions.ProtocolReplaySettlement{}, fn(joinedCtx)
			})
		return result, err, time.Since(started),
			resolver.storeReads.Load() - readsBefore, resolver.inScope.Load() - inScopeBefore
	}

	t.Run("new plan is refused before resolver preflight", func(t *testing.T) {
		owner := seedWorkLaunchAuthorityOwner(t, e, "wla-joined-plan")
		version := blockWorkAuthorityItem(t, e, owner.item)
		var plan sessions.Plan
		var planErr error
		_, err, elapsed, reads, inScope := joined(t, claimFor(owner.workspace), func(ctx context.Context) error {
			plan, planErr = sm.Plan(ctx, e.tenant, agentOwnerPrincipal(owner.ownerExternal),
				ownerFailureCommand(owner.item, version))
			return errWorkAuthorityJoinedRollback
		})
		if !errors.Is(err, errWorkAuthorityJoinedRollback) {
			t.Fatalf("joined plan transaction = %v, want the callback rollback", err)
		}
		if planErr != nil || plan.Verdict != sessions.VerdictUnknown || plan.Code != "evidence_unavailable" ||
			plan.PlanHash != "" {
			t.Fatalf("joined plan = %#v, %v; want UNKNOWN evidence_unavailable without a hash", plan, planErr)
		}
		if reads != 0 || inScope != 0 {
			t.Fatalf("joined plan made %d resolver store reads and %d in-scope authority calls after %s, want 0/0",
				reads, inScope, elapsed.Round(time.Millisecond))
		}
		if status, got := workAuthorityItemStatus(t, e, owner.item); status != "blocked" || got != version {
			t.Fatalf("joined plan changed the item: %s v%d, want blocked v%d", status, got, version)
		}
	})

	t.Run("new apply is refused before resolver preflight and writes nothing", func(t *testing.T) {
		owner := seedWorkLaunchAuthorityOwner(t, e, "wla-joined-apply")
		version := blockWorkAuthorityItem(t, e, owner.item)
		claim := claimFor(owner.workspace)
		var applyErr error
		_, err, elapsed, reads, inScope := joined(t, claim, func(ctx context.Context) error {
			_, applyErr = sm.Apply(ctx, e.tenant, agentOwnerPrincipal(owner.ownerExternal),
				ownerFailureCommand(owner.item, version))
			return applyErr
		})
		if code := workCommandCode(applyErr); code != "evidence_unavailable" || errors.Is(applyErr, context.DeadlineExceeded) {
			t.Fatalf("joined apply = %v after %s, want a prompt evidence_unavailable",
				applyErr, elapsed.Round(time.Millisecond))
		}
		if err == nil {
			t.Fatal("joined replay transaction committed a refused authority command")
		}
		if reads != 0 || inScope != 0 {
			t.Fatalf("joined apply made %d resolver store reads and %d in-scope authority calls after %s, want 0/0",
				reads, inScope, elapsed.Round(time.Millisecond))
		}
		if status, got := workAuthorityItemStatus(t, e, owner.item); status != "blocked" || got != version {
			t.Fatalf("refused joined apply changed the item: %s v%d, want blocked v%d", status, got, version)
		}
		// The refusal rolled back the replay guard as well: the same claim is new again.
		mutated := false
		replay, err, _, _, _ := joined(t, claim, func(context.Context) error {
			mutated = true
			return errWorkAuthorityJoinedRollback
		})
		if replay.Replayed || !mutated || !errors.Is(err, errWorkAuthorityJoinedRollback) {
			t.Fatalf("replay guard after refused apply = %#v, mutated=%v, %v; want no committed guard",
				replay, mutated, err)
		}
	})

	t.Run("exact completed replay returns without observers", func(t *testing.T) {
		owner := seedWorkLaunchAuthorityOwner(t, e, "wla-joined-replay")
		version := blockWorkAuthorityItem(t, e, owner.item)
		cmd := ownerFailureCommand(owner.item, version)
		first, err := sm.Apply(workAuthorityBoundContext(t), e.tenant, agentOwnerPrincipal(owner.ownerExternal), cmd)
		if err != nil || first.Status != "failed" {
			t.Fatalf("complete owner failure before joined replay = %#v, %v", first, err)
		}
		var replayed sessions.CommandResult
		var replayErr error
		_, err, elapsed, reads, inScope := joined(t, claimFor(owner.workspace), func(ctx context.Context) error {
			replayed, replayErr = sm.Apply(ctx, e.tenant, agentOwnerPrincipal(owner.ownerExternal), cmd)
			return replayErr
		})
		if err != nil || replayErr != nil || !replayed.Replayed || replayed.CommandID != first.CommandID ||
			replayed.EventID != first.EventID {
			t.Fatalf("joined exact replay = %#v, %v (transaction %v); want the durable result", replayed, replayErr, err)
		}
		if reads != 0 || inScope != 0 {
			t.Fatalf("joined exact replay made %d resolver store reads and %d in-scope authority calls after %s",
				reads, inScope, elapsed.Round(time.Millisecond))
		}
	})

	// The operator does not consume agent authority, so the guard must not
	// refuse it. Plan is used because it proves the exclusion without the
	// post-Apply outbox nudge, whose joined behavior is a separate finding
	// (correction-1/FINDING-JOINED-APPLY-PARENT-COMMIT.md).
	t.Run("operator plan keeps its exclusion inside the joined transaction", func(t *testing.T) {
		owner := seedWorkLaunchAuthorityOwner(t, e, "wla-joined-operator")
		version := blockWorkAuthorityItem(t, e, owner.item)
		var plan sessions.Plan
		var planErr error
		_, err, elapsed, reads, inScope := joined(t, claimFor(owner.workspace), func(ctx context.Context) error {
			plan, planErr = sm.Plan(ctx, e.tenant, operator, ownerFailureCommand(owner.item, version))
			return errWorkAuthorityJoinedRollback
		})
		if !errors.Is(err, errWorkAuthorityJoinedRollback) {
			t.Fatalf("joined operator plan transaction = %v after %s, want the callback rollback",
				err, elapsed.Round(time.Millisecond))
		}
		if planErr != nil || plan.Verdict != sessions.VerdictClean || len(plan.PlanHash) != 64 {
			t.Fatalf("joined operator plan = %#v, %v; want a clean plan", plan, planErr)
		}
		if reads != 0 || inScope != 0 {
			t.Fatalf("joined operator plan made %d resolver store reads and %d in-scope authority calls after %s",
				reads, inScope, elapsed.Round(time.Millisecond))
		}
		if status, got := workAuthorityItemStatus(t, e, owner.item); status != "blocked" || got != version {
			t.Fatalf("joined operator plan changed the item: %s v%d, want blocked v%d", status, got, version)
		}
	})
}

func TestWorkAuthorityDetachedStampRejectsConcurrentChange(t *testing.T) {
	for _, engine := range workAuthorityBackings() {
		t.Run(engine.name, func(t *testing.T) {
			exerciseWorkAuthorityConcurrentChange(t, engine.backing(t))
		})
	}
}

func exerciseWorkAuthorityConcurrentChange(t *testing.T, backing communicationHTTPTestStore) {
	e := bootWorkLaunchAuthorityEstate(t, backing)
	lifecycle, ok := e.eng.nhiEnforcer.(workAgentLifecycle)
	if !ok {
		t.Fatalf("booted governance module %T does not serve agent lifecycle", e.eng.nhiEnforcer)
	}
	barrier := &barrierWorkIdentityResolver{workIdentityResolver: workIdentityResolver{
		st: e.eng.store, sessions: e.eng.sessionsMod, agentLifecycle: lifecycle,
	}}
	e.eng.sessionsMod.UseWorkIdentityResolver(barrier)
	sm := e.eng.sessionsMod
	ctx := context.Background()
	operator := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "wla-operator", Actor: "system:wla-operator", Admin: true,
	}

	bumpItem := func(t *testing.T, owner workLaunchAuthorityOwner) func() {
		return func() {
			if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
				items, err := sc.Ext("sessions.work_item")
				if err != nil {
					return err
				}
				row, err := items.Get(ctx, owner.item)
				if err != nil {
					return err
				}
				row["title"] = "concurrently edited " + model.NewID().String()
				_, err = items.Update(ctx, row)
				return err
			}); err != nil {
				t.Errorf("concurrent WorkItem edit: %v", err)
			}
		}
	}
	reassign := func(t *testing.T, owner workLaunchAuthorityOwner) func() {
		return func() {
			successor, _, _ := seedWorkAuthorityAgent(t, e, owner.workspace)
			_, version := workAuthorityItemStatus(t, e, owner.item)
			if _, err := sm.Apply(ctx, e.tenant, operator, sessions.WorkCommand{
				Command: "item.assign", WorkItemID: owner.item, OwnerKind: "agent",
				OwnerRef: successor.String(), ExpectedVersion: version,
				IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
			}); err != nil {
				t.Errorf("concurrent owner reassignment: %v", err)
			}
		}
	}
	reviseLifecycle := func(t *testing.T, owner workLaunchAuthorityOwner) func() {
		return func() {
			if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.nhi_lifecycle")
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
					Column: "identity_ref", Op: model.OpEq, Value: owner.ownerExternal,
				}}, Limit: 2})
				if err != nil {
					return err
				}
				if len(rows) != 1 {
					return errors.New("lifecycle row not found")
				}
				// Still eligible: only the exact fact version changes.
				rows[0]["source"] = "test-revised"
				_, err = repo.Update(ctx, rows[0])
				return err
			}); err != nil {
				t.Errorf("concurrent lifecycle revision: %v", err)
			}
		}
	}
	reviseSponsor := func(t *testing.T, owner workLaunchAuthorityOwner) func() {
		return func() {
			if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
				sponsor, err := sc.Identities().Get(ctx, owner.sponsorID)
				if err != nil {
					return err
				}
				// Still an enabled human sponsor: only the exact fact version changes.
				sponsor.Name = "work launch sponsor revised"
				_, err = sc.Identities().Update(ctx, sponsor)
				return err
			}); err != nil {
				t.Errorf("concurrent sponsor revision: %v", err)
			}
		}
	}

	type arm struct {
		name   string
		change func(*testing.T, workLaunchAuthorityOwner) func()
	}
	for i, tc := range []arm{
		{name: "item_version", change: bumpItem},
		{name: "item_owner", change: reassign},
		{name: "lifecycle_revision", change: reviseLifecycle},
		{name: "sponsor_revision", change: reviseSponsor},
	} {
		t.Run("plan/"+tc.name, func(t *testing.T) {
			owner := seedWorkLaunchAuthorityOwner(t, e, "wla-plan-change-"+strconv.Itoa(i))
			version := blockWorkAuthorityItem(t, e, owner.item)
			barrier.arm(tc.change(t, owner))
			plan, err := sm.Plan(workAuthorityBoundContext(t), e.tenant,
				agentOwnerPrincipal(owner.ownerExternal), ownerFailureCommand(owner.item, version))
			if err != nil || plan.Verdict != sessions.VerdictBroken || plan.Code != "plan_changed" || plan.PlanHash != "" {
				t.Fatalf("plan across %s = %#v, %v; want plan_changed without a hash", tc.name, plan, err)
			}
			if status, _ := workAuthorityItemStatus(t, e, owner.item); status != "blocked" {
				t.Fatalf("plan across %s changed status to %s", tc.name, status)
			}
		})
	}

	applyArms := []struct {
		name     string
		change   func(*testing.T, workLaunchAuthorityOwner) func()
		planned  bool
		version  func(observed int64) int64
		wantCode string
	}{
		{name: "lifecycle_revision_without_plan", change: reviseLifecycle,
			version: func(v int64) int64 { return v }, wantCode: "owner_ineligible"},
		{name: "sponsor_revision_with_plan", change: reviseSponsor, planned: true,
			version: func(v int64) int64 { return v }, wantCode: "plan_changed"},
		// Expected-version precedence: the caller's stale version is reported first.
		{name: "owner_change_expected_version_wins", change: reassign,
			version: func(v int64) int64 { return v }, wantCode: "version_mismatch"},
		// A future ExpectedVersion that becomes current after observation passes the
		// version check; the stamp still refuses the old owner's snapshot.
		{name: "owner_change_future_version", change: reassign,
			version: func(v int64) int64 { return v + 1 }, wantCode: "owner_ineligible"},
		{name: "item_version_future_version_with_plan", change: bumpItem, planned: true,
			version: func(v int64) int64 { return v + 1 }, wantCode: "plan_changed"},
	}
	for i, tc := range applyArms {
		t.Run("apply/"+tc.name, func(t *testing.T) {
			owner := seedWorkLaunchAuthorityOwner(t, e, "wla-apply-change-"+strconv.Itoa(i))
			version := blockWorkAuthorityItem(t, e, owner.item)
			principal := agentOwnerPrincipal(owner.ownerExternal)
			cmd := ownerFailureCommand(owner.item, tc.version(version))
			if tc.planned {
				plan, err := sm.Plan(workAuthorityBoundContext(t), e.tenant, principal, ownerFailureCommand(owner.item, version))
				if err != nil || len(plan.PlanHash) != 64 {
					t.Fatalf("plan before %s = %#v, %v", tc.name, plan, err)
				}
				cmd.ExpectedPlanHash = plan.PlanHash
			}
			barrier.arm(tc.change(t, owner))
			_, err := sm.Apply(workAuthorityBoundContext(t), e.tenant, principal, cmd)
			if got := workCommandCode(err); got != tc.wantCode {
				t.Fatalf("apply across %s = %v, want %s", tc.name, err, tc.wantCode)
			}
			if status, _ := workAuthorityItemStatus(t, e, owner.item); status != "blocked" {
				t.Fatalf("refused apply across %s changed status to %s", tc.name, status)
			}
		})
	}

	// NO-FIRE: the same wrapped production resolver still admits a current owner.
	t.Run("apply/no_concurrent_change", func(t *testing.T) {
		owner := seedWorkLaunchAuthorityOwner(t, e, "wla-apply-unchanged")
		version := blockWorkAuthorityItem(t, e, owner.item)
		applied, err := sm.Apply(workAuthorityBoundContext(t), e.tenant,
			agentOwnerPrincipal(owner.ownerExternal), ownerFailureCommand(owner.item, version))
		if err != nil || applied.Status != "failed" {
			t.Fatalf("unchanged owner failure = %#v, %v", applied, err)
		}
	})
}
