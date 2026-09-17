// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Focused production-sink qualification for joined Apply post-commit drains.
// Helpers are the current-tree communication HTTP boot, not the archived r115
// fixture suite. The original fixture remains causal evidence only.

const (
	joinedApplyPostcommitBound   = 10 * time.Second
	joinedApplyPostcommitStackAt = 2 * time.Second
	joinedApplyPostcommitProbe   = 2 * time.Second
)

var errJoinedApplyPostcommitRollback = errors.New("joined apply postcommit rolls back")

type joinedApplyPostcommitEstate struct {
	eng    *engine
	tenant model.TenantID
}

type joinedApplyPostcommitOwner struct {
	workspace     model.ID
	ownerID       model.ID
	ownerExternal string
	item          model.ID
}

type joinedApplyPostcommitProbeSink struct {
	name      string
	inner     sessions.WorkEventSink
	st        store.Store
	tenant    model.TenantID
	entered   atomic.Int32
	enteredAt atomic.Int64
	nestedNS  atomic.Int64
	nestedOK  atomic.Bool
}

func (s *joinedApplyPostcommitProbeSink) IngestDurable(ctx context.Context, event sessions.WorkEventEnvelope) error {
	s.entered.Add(1)
	s.enteredAt.Store(time.Now().UnixNano())
	if s.st != nil {
		nestedCtx, cancel := context.WithTimeout(context.Background(), joinedApplyPostcommitProbe)
		start := time.Now()
		err := s.st.Mutate(nestedCtx, s.tenant, func(store.Scope) error { return nil })
		s.nestedNS.Store(time.Since(start).Nanoseconds())
		cancel()
		if err == nil {
			s.nestedOK.Store(true)
		} else if s.inner != nil {
			return err
		}
	}
	if s.inner == nil {
		return nil
	}
	return s.inner.IngestDurable(ctx, event)
}

func (s *joinedApplyPostcommitProbeSink) identity() string {
	if s.inner == nil {
		return s.name + "/no-store"
	}
	return s.name + "/forward:" + fmt.Sprintf("%T", s.inner)
}

type countingJoinedApplyPostcommitResolver struct {
	workIdentityResolver
	storeReads atomic.Int32
	inScope    atomic.Int32
}

var (
	_ sessions.WorkAgentEligibilityInScope     = (*countingJoinedApplyPostcommitResolver)(nil)
	_ sessions.WorkAgentAuthorityReadValidator = (*countingJoinedApplyPostcommitResolver)(nil)
	_ sessions.WorkAuthenticatedAgentMatcher   = (*countingJoinedApplyPostcommitResolver)(nil)
)

func (r *countingJoinedApplyPostcommitResolver) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (sessions.Participant, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.ResolveParticipant(ctx, tenant, workspace, kind, ref)
}

func (r *countingJoinedApplyPostcommitResolver) SessionActsForAgent(
	ctx context.Context, tenant model.TenantID, sid, agentRef string,
) (bool, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.SessionActsForAgent(ctx, tenant, sid, agentRef)
}

func (r *countingJoinedApplyPostcommitResolver) AuthenticatedAgentMatches(
	ctx context.Context, tenant model.TenantID, canonicalRef, authenticatedRef string,
) (bool, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.AuthenticatedAgentMatches(ctx, tenant, canonicalRef, authenticatedRef)
}

func (r *countingJoinedApplyPostcommitResolver) ObserveAgentWorkAuthority(
	ctx context.Context, tenant model.TenantID, workspace model.ID, canonicalRef, authenticatedRef string,
) (sessions.WorkAgentAuthoritySnapshot, error) {
	r.storeReads.Add(1)
	return r.workIdentityResolver.ObserveAgentWorkAuthority(ctx, tenant, workspace, canonicalRef, authenticatedRef)
}

func (r *countingJoinedApplyPostcommitResolver) ValidateAgentWorkAuthorityInScope(
	ctx context.Context, sc store.Scope, snapshot sessions.WorkAgentAuthoritySnapshot,
) error {
	r.inScope.Add(1)
	return r.workIdentityResolver.ValidateAgentWorkAuthorityInScope(ctx, sc, snapshot)
}

func (r *countingJoinedApplyPostcommitResolver) LockAgentWorkAuthority(
	ctx context.Context, sc store.Scope, snapshot sessions.WorkAgentAuthoritySnapshot,
) error {
	r.inScope.Add(1)
	return r.workIdentityResolver.LockAgentWorkAuthority(ctx, sc, snapshot)
}

func bootJoinedApplyPostcommitEstate(t *testing.T, backing communicationHTTPTestStore) joinedApplyPostcommitEstate {
	t.Helper()
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), backing.bootConfig())
	if err != nil {
		t.Fatalf("boot joined apply postcommit estate: %v", err)
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
	if code, _, _ := doDemoViewJSON(t, handler, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": setupToken, "email": "root@joined-apply-postcommit.test",
		"password": "joined-apply-postcommit-root-password",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	code, login, _ := doDemoViewJSON(t, handler, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "root@joined-apply-postcommit.test", "password": "joined-apply-postcommit-root-password",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
	adminToken, _ := login["token"].(string)
	code, org, _ := doDemoViewJSON(t, handler, http.MethodPost, "/v1/system/orgs", adminToken, "",
		map[string]any{"name": "joined apply postcommit", "slug": "joined-apply-postcommit"})
	if code != http.StatusCreated {
		t.Fatalf("create tenant = %d", code)
	}
	tenantID, _ := org["tenant_id"].(string)
	if tenantID == "" {
		t.Fatal("created tenant has no id")
	}
	return joinedApplyPostcommitEstate{eng: eng, tenant: model.TenantID(tenantID)}
}

func seedJoinedApplyPostcommitOwner(t *testing.T, e joinedApplyPostcommitEstate, slug string) joinedApplyPostcommitOwner {
	t.Helper()
	ctx := context.Background()
	var owner joinedApplyPostcommitOwner
	if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "joined apply postcommit " + slug, Slug: slug, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		owner.workspace = ws.ID
		return nil
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	owner.ownerID, owner.ownerExternal = seedJoinedApplyPostcommitAgent(t, e, owner.workspace)
	setup := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "japc-setup",
		Actor: "system:japc-setup", Admin: true,
	}
	created, err := e.eng.sessionsMod.Apply(ctx, e.tenant, setup, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: owner.workspace, WorkKind: "implementation",
		Title: "Joined apply postcommit", BriefMD: "Synthetic item.",
		ContextRefs: []sessions.ContextRef{}, Priority: "p1",
		OwnerKind: "agent", OwnerRef: owner.ownerID.String(),
		ProvenanceKind: "workflow", ProvenanceRef: "test:joined-apply-postcommit",
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

func seedJoinedApplyPostcommitAgent(t *testing.T, e joinedApplyPostcommitEstate, workspace model.ID) (model.ID, string) {
	t.Helper()
	ctx := context.Background()
	ownerExternal := "agent:japc-" + model.NewID().String()
	sponsorExternal := "human:japc-sponsor-" + model.NewID().String()
	var ownerID model.ID
	if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(ctx, model.Identity{
			Name: "joined apply postcommit executor", Kind: "agent_nhi",
			ExternalID: ownerExternal, Provider: "test",
		})
		if err != nil {
			return err
		}
		ownerID = identity.ID
		if _, err := sc.Agents().Create(ctx, model.Agent{
			Name: "joined apply postcommit executor", Kind: "test", ExternalID: ownerExternal,
			Status: model.StatusActive, IdentityID: identity.ID, WorkspaceID: workspace,
		}); err != nil {
			return err
		}
		if _, err := sc.Identities().Create(ctx, model.Identity{
			Name: "joined apply postcommit sponsor", Kind: "user", ExternalID: sponsorExternal,
			Provider: "test", Metadata: map[string]any{"principal_type": "human", "disabled": false},
		}); err != nil {
			return err
		}
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
		t.Fatalf("seed agent facts: %v", err)
	}
	return ownerID, ownerExternal
}

func blockJoinedApplyPostcommitItem(t *testing.T, e joinedApplyPostcommitEstate, item model.ID) int64 {
	t.Helper()
	snapshot, err := e.eng.sessionsMod.Get(context.Background(), e.tenant, sessions.WorkPrincipal{}, item)
	if err != nil {
		t.Fatalf("read WorkItem: %v", err)
	}
	blocked, err := e.eng.sessionsMod.Apply(context.Background(), e.tenant, sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "japc-operator", Actor: "system:japc-operator", Admin: true,
	}, sessions.WorkCommand{
		Command: "item.block", WorkItemID: item, Code: "operator_blocked",
		Reason:          "The operator blocked the execution before owner failure.",
		ExpectedVersion: snapshot.Item.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || blocked.Status != "blocked" {
		t.Fatalf("block WorkItem = %#v, %v", blocked, err)
	}
	return blocked.Version
}

func joinedApplyPostcommitOperator() sessions.WorkPrincipal {
	return sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "japc-operator", Actor: "system:japc-operator", Admin: true,
	}
}

func joinedApplyPostcommitFailCommand(item model.ID, version int64) sessions.WorkCommand {
	return sessions.WorkCommand{
		Command: "item.fail", WorkItemID: item, Code: "owner_failure",
		Reason:          "The currently eligible canonical owner reports terminal failure.",
		ExpectedVersion: version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}
}

func joinedApplyPostcommitItemStatus(t *testing.T, e joinedApplyPostcommitEstate, item model.ID) (string, int64) {
	t.Helper()
	snapshot, err := e.eng.sessionsMod.Get(context.Background(), e.tenant, sessions.WorkPrincipal{}, item)
	if err != nil {
		t.Fatalf("read WorkItem: %v", err)
	}
	return snapshot.Item.Status, snapshot.Item.Version
}

func joinedApplyPostcommitStacks() string {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, block := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(block, "drainWorkOutbox") ||
			strings.Contains(block, "IngestDurable") ||
			strings.Contains(block, "captureEventOnce") ||
			strings.Contains(block, "ApplyProtocolReplay") ||
			strings.Contains(block, "sessions.(*Module).applyWithData") ||
			strings.Contains(block, "sqlStore.Mutate") ||
			strings.Contains(block, "database/sql.(*DB).conn") {
			out = append(out, block)
		}
	}
	if len(out) == 0 {
		return "(no goroutine matched the joined apply/outbox/sink/store filter)"
	}
	return strings.Join(out, "\n\n")
}

type joinedApplyPostcommitOutcome struct {
	parent    sessions.ProtocolReplayResult
	parentErr error
	elapsed   time.Duration
	reads     int32
	inScope   int32
	ingests   int32
	ingestAt  time.Time
	nestedOK  bool
	nestedNS  time.Duration
	stacks    string
	captured  bool
}

func runJoinedApplyPostcommit(
	t *testing.T,
	e joinedApplyPostcommitEstate,
	resolver *countingJoinedApplyPostcommitResolver,
	probe *joinedApplyPostcommitProbeSink,
	claim sessions.ProtocolReplayClaim,
	fn func(context.Context) error,
	capture bool,
) joinedApplyPostcommitOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), joinedApplyPostcommitBound)
	defer cancel()
	readsBefore, inScopeBefore := resolver.storeReads.Load(), resolver.inScope.Load()
	type done struct {
		parent    sessions.ProtocolReplayResult
		parentErr error
		elapsed   time.Duration
	}
	ch := make(chan done, 1)
	started := time.Now()
	go func() {
		parent, err := e.eng.sessionsMod.ApplyProtocolReplay(ctx, e.tenant, claim,
			func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
				return sessions.ProtocolReplaySettlement{}, fn(joinedCtx)
			})
		ch <- done{parent: parent, parentErr: err, elapsed: time.Since(started)}
	}()
	var out joinedApplyPostcommitOutcome
	if capture {
		timer := time.NewTimer(joinedApplyPostcommitStackAt)
		defer timer.Stop()
		select {
		case got := <-ch:
			out.parent, out.parentErr, out.elapsed = got.parent, got.parentErr, got.elapsed
		case <-timer.C:
			out.captured = true
			out.stacks = joinedApplyPostcommitStacks()
			got := <-ch
			out.parent, out.parentErr, out.elapsed = got.parent, got.parentErr, got.elapsed
		}
	} else {
		got := <-ch
		out.parent, out.parentErr, out.elapsed = got.parent, got.parentErr, got.elapsed
	}
	out.reads = resolver.storeReads.Load() - readsBefore
	out.inScope = resolver.inScope.Load() - inScopeBefore
	out.ingests = probe.entered.Load()
	out.nestedOK = probe.nestedOK.Load()
	out.nestedNS = time.Duration(probe.nestedNS.Load())
	if ns := probe.enteredAt.Load(); ns > 0 {
		out.ingestAt = time.Unix(0, ns)
	}
	return out
}

func installJoinedApplyPostcommitProbe(
	t *testing.T, e joinedApplyPostcommitEstate, name string, forward bool,
) *joinedApplyPostcommitProbeSink {
	t.Helper()
	probe := &joinedApplyPostcommitProbeSink{
		name: name, st: e.eng.store, tenant: e.tenant,
	}
	if forward {
		probe.inner = e.eng.workSink
	}
	e.eng.sessionsMod.UseWorkEventSink(probe)
	t.Logf("sink identity=%s production_type=%T", probe.identity(), e.eng.workSink)
	return probe
}

func claimForJoinedApplyPostcommit(workspace model.ID, kind sessions.ProtocolReplayKind) sessions.ProtocolReplayClaim {
	return sessions.ProtocolReplayClaim{
		WorkspaceID: workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: "https://peer.joined-apply-postcommit.test", Kind: kind,
		ReplayID: "japc-" + model.NewID().String(), ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestJoinedApplyPostcommitProductionSink(t *testing.T) {
	for _, engineCase := range []struct {
		name    string
		backing func(*testing.T) communicationHTTPTestStore
	}{
		{name: "sqlite", backing: communicationHTTPTestSQLiteStore},
		{name: "postgres", backing: communicationHTTPTestPostgresStore},
	} {
		t.Run(engineCase.name, func(t *testing.T) {
			e := bootJoinedApplyPostcommitEstate(t, engineCase.backing(t))
			lifecycle, ok := e.eng.nhiEnforcer.(workAgentLifecycle)
			if !ok {
				t.Fatalf("booted governance module %T does not serve agent lifecycle", e.eng.nhiEnforcer)
			}
			resolver := &countingJoinedApplyPostcommitResolver{workIdentityResolver: workIdentityResolver{
				st: e.eng.store, sessions: e.eng.sessionsMod, agentLifecycle: lifecycle,
			}}
			e.eng.sessionsMod.UseWorkIdentityResolver(resolver)
			sm := e.eng.sessionsMod
			operator := joinedApplyPostcommitOperator()

			t.Run("production_eventing_commit_before_sink", func(t *testing.T) {
				owner := seedJoinedApplyPostcommitOwner(t, e, "japc-prod")
				version := blockJoinedApplyPostcommitItem(t, e, owner.item)
				probe := installJoinedApplyPostcommitProbe(t, e, "production-eventing", true)
				var applied sessions.CommandResult
				var applyErr error
				out := runJoinedApplyPostcommit(t, e, resolver, probe, claimForJoinedApplyPostcommit(owner.workspace, sessions.ProtocolReplayJTI),
					func(ctx context.Context) error {
						applied, applyErr = sm.Apply(ctx, e.tenant, operator, joinedApplyPostcommitFailCommand(owner.item, version))
						return applyErr
					}, true)
				t.Logf("production sink: apply=%#v applyErr=%v parentErr=%v elapsed=%s reads=%d inScope=%d ingests=%d nestedOK=%t nested=%s captured=%t",
					applied, applyErr, out.parentErr, out.elapsed.Round(time.Millisecond),
					out.reads, out.inScope, out.ingests, out.nestedOK, out.nestedNS.Round(time.Millisecond), out.captured)
				if out.stacks != "" {
					t.Logf("stacks at %s while parent still open:\n%s", joinedApplyPostcommitStackAt, out.stacks)
				}
				status, _ := joinedApplyPostcommitItemStatus(t, e, owner.item)
				if applyErr != nil || applied.Status != "failed" || applied.EventID.IsZero() {
					t.Fatalf("inner Apply = %#v, %v", applied, applyErr)
				}
				if out.reads != 0 || out.inScope != 0 {
					t.Fatalf("operator exclusion lost: resolver reads=%d in-scope=%d", out.reads, out.inScope)
				}
				if out.parentErr != nil {
					t.Fatalf("parent replay did not commit: %v (durable_status=%s elapsed=%s ingests=%d)",
						out.parentErr, status, out.elapsed.Round(time.Millisecond), out.ingests)
				}
				if status != "failed" {
					t.Fatalf("durable status %s after parent return", status)
				}
				if out.ingests < 1 {
					t.Fatal("production sink was never entered")
				}
				if !out.nestedOK || out.nestedNS >= joinedApplyPostcommitProbe {
					t.Fatalf("commit-before-sink failed: nestedOK=%t nested=%s", out.nestedOK, out.nestedNS)
				}
				if out.elapsed >= 2*time.Second {
					t.Fatalf("production parent took %s; post-commit drain must not wait on the 10s bound", out.elapsed)
				}
			})

			t.Run("production_eventing_rollback_no_capture", func(t *testing.T) {
				owner := seedJoinedApplyPostcommitOwner(t, e, "japc-rollback")
				version := blockJoinedApplyPostcommitItem(t, e, owner.item)
				probe := installJoinedApplyPostcommitProbe(t, e, "production-eventing-rollback", true)
				var applied sessions.CommandResult
				var applyErr error
				out := runJoinedApplyPostcommit(t, e, resolver, probe, claimForJoinedApplyPostcommit(owner.workspace, sessions.ProtocolReplayJTI),
					func(ctx context.Context) error {
						applied, applyErr = sm.Apply(ctx, e.tenant, operator, joinedApplyPostcommitFailCommand(owner.item, version))
						if applyErr != nil {
							return applyErr
						}
						return errJoinedApplyPostcommitRollback
					}, false)
				status, _ := joinedApplyPostcommitItemStatus(t, e, owner.item)
				t.Logf("rollback: apply=%s applyErr=%v parentErr=%v durable=%s ingests=%d elapsed=%s",
					applied.Status, applyErr, out.parentErr, status, out.ingests, out.elapsed.Round(time.Millisecond))
				if applyErr != nil || applied.Status != "failed" {
					t.Fatalf("inner Apply before callback refusal = %#v, %v", applied, applyErr)
				}
				if !errors.Is(out.parentErr, errJoinedApplyPostcommitRollback) {
					t.Fatalf("parent err = %v, want callback rollback", out.parentErr)
				}
				if status != "blocked" {
					t.Fatalf("rollback left durable status %s", status)
				}
				if out.ingests != 0 {
					t.Fatalf("rollback captured Eventing %d times", out.ingests)
				}
				if out.elapsed >= 2*time.Second {
					t.Fatalf("rollback took %s", out.elapsed)
				}
			})

			t.Run("nested_owner_only_flush", func(t *testing.T) {
				owner := seedJoinedApplyPostcommitOwner(t, e, "japc-nested")
				version := blockJoinedApplyPostcommitItem(t, e, owner.item)
				probe := installJoinedApplyPostcommitProbe(t, e, "production-eventing-nested", true)
				var applied sessions.CommandResult
				var applyErr error
				var sinkAtInner int32
				out := runJoinedApplyPostcommit(t, e, resolver, probe, claimForJoinedApplyPostcommit(owner.workspace, sessions.ProtocolReplayJTI),
					func(ctx context.Context) error {
						_, innerErr := sm.ApplyProtocolReplay(ctx, e.tenant,
							claimForJoinedApplyPostcommit(owner.workspace, sessions.ProtocolReplayMessageID),
							func(innerCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
								applied, applyErr = sm.Apply(innerCtx, e.tenant, operator, joinedApplyPostcommitFailCommand(owner.item, version))
								sinkAtInner = probe.entered.Load()
								return sessions.ProtocolReplaySettlement{}, applyErr
							})
						return innerErr
					}, true)
				status, _ := joinedApplyPostcommitItemStatus(t, e, owner.item)
				t.Logf("nested: apply=%s parentErr=%v durable=%s ingests=%d sinkAtInner=%d nestedOK=%t elapsed=%s",
					applied.Status, out.parentErr, status, out.ingests, sinkAtInner, out.nestedOK, out.elapsed.Round(time.Millisecond))
				if applyErr != nil || applied.Status != "failed" || out.parentErr != nil || status != "failed" {
					t.Fatalf("nested owner apply = %#v %v parent %v durable %s", applied, applyErr, out.parentErr, status)
				}
				if sinkAtInner != 0 {
					t.Fatalf("nested frame flushed before owner commit: sinkAtInner=%d", sinkAtInner)
				}
				if out.ingests < 1 || !out.nestedOK {
					t.Fatalf("owner did not flush production sink after commit: ingests=%d nestedOK=%t", out.ingests, out.nestedOK)
				}
				if out.elapsed >= 2*time.Second {
					t.Fatalf("nested owner took %s", out.elapsed)
				}
			})

			t.Run("exact_command_replay_skips_sink", func(t *testing.T) {
				owner := seedJoinedApplyPostcommitOwner(t, e, "japc-replay")
				version := blockJoinedApplyPostcommitItem(t, e, owner.item)
				cmd := joinedApplyPostcommitFailCommand(owner.item, version)
				first, err := sm.Apply(context.Background(), e.tenant, operator, cmd)
				if err != nil || first.Status != "failed" {
					t.Fatalf("complete operator failure before joined replay = %#v, %v", first, err)
				}
				probe := installJoinedApplyPostcommitProbe(t, e, "production-eventing-replay", true)
				var replayed sessions.CommandResult
				var replayErr error
				out := runJoinedApplyPostcommit(t, e, resolver, probe, claimForJoinedApplyPostcommit(owner.workspace, sessions.ProtocolReplayJTI),
					func(ctx context.Context) error {
						replayed, replayErr = sm.Apply(ctx, e.tenant, operator, cmd)
						return replayErr
					}, false)
				t.Logf("exact replay: replayed=%v parentErr=%v elapsed=%s ingests=%d",
					replayed.Replayed, out.parentErr, out.elapsed.Round(time.Millisecond), out.ingests)
				if out.parentErr != nil || replayErr != nil || !replayed.Replayed || replayed.CommandID != first.CommandID {
					t.Fatalf("joined exact replay = %#v %v parent %v", replayed, replayErr, out.parentErr)
				}
				if out.ingests != 0 {
					t.Fatalf("exact replay entered the sink %d times", out.ingests)
				}
			})
		})
	}
}
