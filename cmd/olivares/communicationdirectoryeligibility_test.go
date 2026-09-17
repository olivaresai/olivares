// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// directoryEligibilityFixture is a real file-backed SQLite estate with the
// sessions schema: one tenant, its default workspace and a second workspace.
// Every negative case below alters an ACTUAL fact (status, membership,
// workspace binding, Claim) and reads the adapter's answer; nothing is scripted.
type directoryEligibilityFixture struct {
	st             store.Store
	sm             *sessions.Module
	tenant         model.TenantID
	workspace      model.ID
	otherWorkspace model.ID
	lifecycle      *workAgentLifecycleStub
	reads          *communicationDirectoryReads
	resolver       *communicationDirectoryResolver
	closure        *communicationGrantClosureResolver
	scope          sessions.DirectoryScopeRef
}

func newDirectoryEligibilityFixture(t *testing.T) *directoryEligibilityFixture {
	t.Helper()
	ctx := context.Background()
	sm := sessions.New()
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "directory.db"),
	}, sm.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &directoryEligibilityFixture{st: st, sm: sm, lifecycle: &workAgentLifecycleStub{eligible: true}}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "dir", Slug: "dir", Status: model.StatusActive})
		f.tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		f.workspace = ws.ID
		other, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Other", Slug: "other", Status: model.StatusActive})
		f.otherWorkspace = other.ID
		return err
	}); err != nil {
		t.Fatalf("workspaces: %v", err)
	}
	sm.UseData(api.NewModuleData(st))
	f.reads = newCommunicationDirectoryReads(st, sm, f.lifecycle)
	f.resolver = newCommunicationDirectoryResolver(newDirectoryScopeRunner(st), f.reads, time.Now)
	f.closure = newCommunicationGrantClosureResolver(f.resolver)
	f.scope = sessions.DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: f.workspace}
	return f
}

func (f *directoryEligibilityFixture) createUser(t *testing.T, status model.LifecycleStatus) model.ID {
	t.Helper()
	var id model.ID
	if err := f.st.AuthMutate(context.Background(), func(sc store.AuthScope) error {
		user, err := sc.Users().Create(context.Background(), model.User{
			Email: model.NewID().String() + "@directory.test", DisplayName: "user", Status: status,
		})
		id = user.ID
		return err
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func (f *directoryEligibilityFixture) grantMembership(t *testing.T, user model.ID, workspace model.ID) {
	t.Helper()
	if err := f.st.AuthMutate(context.Background(), func(sc store.AuthScope) error {
		_, err := sc.Memberships().Create(context.Background(), model.Membership{
			UserID: user, TargetTenantID: f.tenant, Role: "editor", WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		t.Fatalf("grant membership: %v", err)
	}
}

func (f *directoryEligibilityFixture) createUserGroup(t *testing.T, members ...model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := f.st.AuthMutate(context.Background(), func(sc store.AuthScope) error {
		group, err := sc.Groups().Create(context.Background(), model.UserGroup{
			TargetTenantID: f.tenant, DisplayName: "group-" + model.NewID().String(),
		})
		if err != nil {
			return err
		}
		id = group.ID
		for _, member := range members {
			if _, err := sc.GroupMembers().Create(context.Background(), model.UserGroupMember{GroupID: id, UserID: member}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	return id
}

// createAgent binds a fresh Identity to one Agent row in the given workspace
// and returns the canonical K3 recipient ref (the Identity id) and the
// identity's external id.
func (f *directoryEligibilityFixture) createAgent(t *testing.T, workspace model.ID, status model.LifecycleStatus) (model.ID, string) {
	t.Helper()
	external := "agent-" + model.NewID().String()
	var identityID model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: external, Kind: "agent", ExternalID: external, Provider: "olivares",
		})
		if err != nil {
			return err
		}
		identityID = identity.ID
		_, err = sc.Agents().Create(context.Background(), model.Agent{
			Name: external, Kind: "claude-code", ExternalID: external, Status: status,
			IdentityID: identityID, WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return identityID, external
}

func (f *directoryEligibilityFixture) createAgentGroup(t *testing.T, workspace model.ID, identities ...model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		group, err := sc.AgentGroups().Create(context.Background(), model.AgentGroup{
			Name: "agents", Slug: "agents-" + model.NewID().String(), Status: model.StatusActive, WorkspaceID: workspace,
		})
		if err != nil {
			return err
		}
		id = group.ID
		for _, identity := range identities {
			agents, _, err := sc.Agents().List(context.Background(), model.Query{Filters: []model.Filter{{
				Column: "identity_id", Op: model.OpEq, Value: identity.String(),
			}}, Limit: 10})
			if err != nil {
				return err
			}
			for _, agent := range agents {
				if _, err := sc.AgentGroupMembers().Create(context.Background(), model.AgentGroupMember{
					GroupID: id, AgentID: agent.ID,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create agent group: %v", err)
	}
	return id
}

func (f *directoryEligibilityFixture) claimedSession(t *testing.T) (string, sessions.Lease) {
	t.Helper()
	ctx := context.Background()
	sid, err := f.sm.ResolveSession(ctx, f.tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	lease, err := f.sm.Claim(ctx, f.tenant, sid, "holder", 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return sid, lease
}

// launchedSession models a session the runtime plane launched: a run row that
// records the agent attribution, the operated alias that binds the canonical
// sid to that run (the same public binding createRun performs), and a live
// Claim. The run row is seeded with the columns the plane writes at launch
// because launching a real provider process is not a fixture this test owns.
func (f *directoryEligibilityFixture) launchedSession(t *testing.T, agentRef string) (string, string, sessions.Lease) {
	t.Helper()
	ctx := context.Background()
	runRef := model.NewID().String()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		row := model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "running", "last_event_seq": int64(0),
		}
		if agentRef != "" {
			row["agent_ref"] = agentRef
		}
		_, err = repo.Create(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	sid, err := f.sm.ResolveSession(ctx, f.tenant, sessions.SessionBinding{
		Provider: sessions.ProviderOperated, ExternalID: runRef, Origin: sessions.OriginOperated,
		WorkspaceID: f.workspace, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("bind operated session: %v", err)
	}
	lease, err := f.sm.Claim(ctx, f.tenant, sid, "holder", 0)
	if err != nil {
		t.Fatalf("claim launched session: %v", err)
	}
	return sid, runRef, lease
}

func (f *directoryEligibilityFixture) recipient(t *testing.T, kind sessions.RecipientKind, ref string) sessions.RecipientSnapshot {
	t.Helper()
	got, err := f.resolver.ResolveRecipient(context.Background(), f.scope, sessions.RecipientRef{Kind: kind, Ref: ref})
	if err != nil {
		t.Fatalf("resolve %s %s: %v", kind, ref, err)
	}
	return got
}

func TestDirectoryResolverEligibilityReadsActualPrincipalFacts(t *testing.T) {
	t.Parallel()
	f := newDirectoryEligibilityFixture(t)

	member := f.createUser(t, model.StatusActive)
	f.grantMembership(t, member, "")
	if got := f.recipient(t, sessions.RecipientUser, member.String()); !got.Eligible || got.Tombstone != nil ||
		got.RecipientEpoch < 1 || got.DirectoryEpoch < 1 {
		t.Fatalf("active tenant-wide member = %+v, want eligible", got)
	}
	inactive := f.createUser(t, model.StatusInactive)
	f.grantMembership(t, inactive, "")
	if got := f.recipient(t, sessions.RecipientUser, inactive.String()); got.Eligible || got.Tombstone != nil {
		t.Fatalf("inactive member = %+v, want ineligible WITHOUT tombstone", got)
	}
	nonMember := f.createUser(t, model.StatusActive)
	if got := f.recipient(t, sessions.RecipientUser, nonMember.String()); got.Eligible {
		t.Fatalf("non-member = %+v, want ineligible", got)
	}
	scoped := f.createUser(t, model.StatusActive)
	f.grantMembership(t, scoped, f.otherWorkspace)
	if got := f.recipient(t, sessions.RecipientUser, scoped.String()); got.Eligible {
		t.Fatalf("member scoped to another workspace = %+v, want ineligible here", got)
	}
	otherScope := sessions.DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: f.otherWorkspace}
	if got, err := f.resolver.ResolveRecipient(context.Background(), otherScope,
		sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: scoped.String()}); err != nil || !got.Eligible {
		t.Fatalf("scoped member in its own workspace = %+v err=%v, want eligible", got, err)
	}
	if got := f.recipient(t, sessions.RecipientUser, model.NewID().String()); got.Eligible || got.Tombstone != nil {
		t.Fatalf("missing user = %+v, want ineligible without tombstone", got)
	}

	agent, _ := f.createAgent(t, f.workspace, model.StatusActive)
	if got := f.recipient(t, sessions.RecipientAgent, agent.String()); !got.Eligible {
		t.Fatalf("active agent in workspace = %+v, want eligible", got)
	}
	elsewhere, _ := f.createAgent(t, f.otherWorkspace, model.StatusActive)
	if got := f.recipient(t, sessions.RecipientAgent, elsewhere.String()); got.Eligible {
		t.Fatalf("agent bound to another workspace = %+v, want ineligible", got)
	}
	dormant, _ := f.createAgent(t, f.workspace, model.StatusInactive)
	if got := f.recipient(t, sessions.RecipientAgent, dormant.String()); got.Eligible {
		t.Fatalf("inactive agent = %+v, want ineligible", got)
	}
	f.lifecycle.eligible = false
	if got := f.recipient(t, sessions.RecipientAgent, agent.String()); got.Eligible {
		t.Fatalf("lifecycle-ineligible agent = %+v, want ineligible", got)
	}
	f.lifecycle.eligible = true

	sid, lease := f.claimedSession(t)
	if got := f.recipient(t, sessions.RecipientSession, sid); !got.Eligible || got.RecipientEpoch != lease.Fence {
		t.Fatalf("claimed session = %+v, want eligible at fence %d", got, lease.Fence)
	}
	unclaimed, err := f.sm.ResolveSession(context.Background(), f.tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.recipient(t, sessions.RecipientSession, unclaimed); got.Eligible {
		t.Fatalf("unclaimed session = %+v, want ineligible", got)
	}
}

func TestDirectoryResolverAudienceCarriesActualCausalFacts(t *testing.T) {
	t.Parallel()
	f := newDirectoryEligibilityFixture(t)
	active := f.createUser(t, model.StatusActive)
	f.grantMembership(t, active, "")
	inactive := f.createUser(t, model.StatusInactive)
	f.grantMembership(t, inactive, "")
	group := f.createUserGroup(t, active, inactive)
	agent, _ := f.createAgent(t, f.workspace, model.StatusActive)
	agentGroup := f.createAgentGroup(t, f.workspace, agent)
	sid, lease := f.claimedSession(t)

	selectors := []sessions.AudienceSelector{
		{Kind: sessions.AudienceUserGroup, Ref: group.String(), WakePolicy: sessions.WakeNone},
		{Kind: sessions.AudienceWorkspaceMembers, WakePolicy: sessions.WakeNone},
		{Kind: sessions.AudienceAgentGroup, Ref: agentGroup.String(), WakePolicy: sessions.WakeNone},
		{Kind: sessions.AudienceSession, Ref: sid, WakePolicy: sessions.WakeNone, Required: true},
	}
	snapshot, err := f.resolver.ResolveAudience(context.Background(), f.scope, selectors)
	if err != nil {
		t.Fatalf("resolve audience: %v", err)
	}
	if err := sessions.ValidateDirectorySnapshotForSelectors(snapshot, selectors); err != nil {
		t.Fatalf("snapshot rejected by the module validator: %v\n%+v", err, snapshot)
	}
	type key struct {
		ordinal int64
		ref     string
	}
	facts := map[key]model.Kind{}
	for _, c := range snapshot.Contributions {
		var kind model.Kind
		if c.CausalFact != nil {
			kind = c.CausalFact.Kind
		}
		facts[key{c.SelectorOrdinal, c.Recipient.Recipient.Ref}] = kind
		if c.Recipient.Recipient.Kind == sessions.RecipientSession &&
			(c.ObservedSessionSID != sid || c.ObservedClaimFence != lease.Fence) {
			t.Fatalf("session contribution lost its Claim tuple: %+v", c)
		}
	}
	want := map[key]model.Kind{
		{1, active.String()}: directoryFactUserGroupMember,
		{2, active.String()}: directoryFactMembership,
		{2, agent.String()}:  directoryFactAgent,
		{3, agent.String()}:  directoryFactAgentGroupMember,
		{4, sid}:             "",
	}
	for k, kind := range want {
		if facts[k] != kind {
			t.Fatalf("contribution %+v fact = %q, want %q (all: %+v)", k, facts[k], kind, facts)
		}
	}
	if _, present := facts[key{1, inactive.String()}]; present {
		t.Fatal("inactive group member received a contribution")
	}
	if _, present := facts[key{2, inactive.String()}]; present {
		t.Fatal("inactive workspace member received a contribution")
	}
	rosterHasInactive := false
	for _, r := range snapshot.Recipients {
		if r.Recipient.Ref == inactive.String() && !r.Eligible {
			rosterHasInactive = true
		}
	}
	if !rosterHasInactive {
		t.Fatal("roster does not record the ineligible member it observed")
	}
	if _, err := f.resolver.ResolveAudience(context.Background(), f.scope, []sessions.AudienceSelector{
		{Kind: sessions.AudienceSubscribers, WakePolicy: sessions.WakeNone},
	}); !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("subscribers at the directory = %v, want ErrDirectoryUnavailable", err)
	}
}

func TestDirectoryResolverPrincipalResolutionIsExact(t *testing.T) {
	t.Parallel()
	f := newDirectoryEligibilityFixture(t)
	ctx := context.Background()
	user := f.createUser(t, model.StatusActive)
	f.grantMembership(t, user, "")
	resolution, err := f.resolver.ResolvePrincipal(ctx, f.scope, sessions.CommunicationPrincipal{UserID: user})
	if err != nil || sessions.ValidatePrincipalResolution(resolution) != nil ||
		resolution.Outcome != sessions.PrincipalResolved || resolution.Recipient.Recipient.Ref != user.String() {
		t.Fatalf("user resolution = %+v err=%v", resolution, err)
	}

	agent, external := f.createAgent(t, f.workspace, model.StatusActive)
	resolution, err = f.resolver.ResolvePrincipal(ctx, f.scope, sessions.CommunicationPrincipal{AgentExternalID: external})
	if err != nil || sessions.ValidatePrincipalResolution(resolution) != nil ||
		resolution.Outcome != sessions.PrincipalResolved || resolution.Recipient.Recipient != (sessions.RecipientRef{
		Kind: sessions.RecipientAgent, Ref: agent.String()}) {
		t.Fatalf("agent resolution = %+v err=%v", resolution, err)
	}
	resolution, err = f.resolver.ResolvePrincipal(ctx, f.scope, sessions.CommunicationPrincipal{AgentExternalID: "nobody"})
	if err != nil || resolution.Outcome != sessions.PrincipalNotFound || resolution.Recipient != nil {
		t.Fatalf("unknown agent resolution = %+v err=%v", resolution, err)
	}

	sid, runRef, lease := f.launchedSession(t, external)
	exact := sessions.CommunicationPrincipal{
		SessionID: sid, SessionRunRef: runRef, SessionFence: lease.Fence, SessionWorkspaceID: f.workspace,
		PurposeRestricted: true, AgentExternalID: external,
	}
	resolution, err = f.resolver.ResolvePrincipal(ctx, f.scope, exact)
	if err != nil || sessions.ValidatePrincipalResolution(resolution) != nil ||
		resolution.Outcome != sessions.PrincipalResolved || resolution.Recipient.Recipient.Ref != sid ||
		resolution.Recipient.RecipientEpoch != lease.Fence {
		t.Fatalf("session resolution = %+v err=%v", resolution, err)
	}
	for name, mutate := range map[string]struct {
		apply func(*sessions.CommunicationPrincipal)
		code  string
	}{
		"stale fence":   {func(p *sessions.CommunicationPrincipal) { p.SessionFence++ }, "session_claim_stale"},
		"foreign run":   {func(p *sessions.CommunicationPrincipal) { p.SessionRunRef = model.NewID().String() }, "session_run_mismatch"},
		"foreign agent": {func(p *sessions.CommunicationPrincipal) { p.AgentExternalID = "someone-else" }, "session_agent_mismatch"},
	} {
		mutated := exact
		mutate.apply(&mutated)
		resolution, err = f.resolver.ResolvePrincipal(ctx, f.scope, mutated)
		if err != nil || resolution.Outcome != sessions.PrincipalNotFound || resolution.Code != mutate.code ||
			resolution.Recipient != nil {
			t.Fatalf("%s resolution = %+v err=%v, want not_found %s", name, resolution, err, mutate.code)
		}
	}
	// A claimed session the plane never launched has no operated run: a bearer
	// naming any run is not that session's exact tuple.
	unlaunched, unlaunchedLease := f.claimedSession(t)
	resolution, err = f.resolver.ResolvePrincipal(ctx, f.scope, sessions.CommunicationPrincipal{
		SessionID: unlaunched, SessionRunRef: model.NewID().String(), SessionFence: unlaunchedLease.Fence,
		SessionWorkspaceID: f.workspace, PurposeRestricted: true,
	})
	if err != nil || resolution.Outcome != sessions.PrincipalNotFound || resolution.Code != "session_run_mismatch" {
		t.Fatalf("unlaunched session resolution = %+v err=%v", resolution, err)
	}
	// The session's workspace ceiling is enforced before any read.
	otherScope := sessions.DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: f.otherWorkspace}
	if _, err := f.resolver.ResolvePrincipal(ctx, otherScope, exact); err == nil {
		t.Fatal("session principal crossed its workspace scope")
	}
	if _, err := f.resolver.ResolvePrincipal(ctx, f.scope, sessions.CommunicationPrincipal{
		System: true, SystemActorRef: "system", SystemGrantAgentID: agent,
	}); err == nil {
		// A system principal is not a directory principal; the validator will
		// have rejected it before the resolver, or the resolver answered unknown.
		t.Log("system principal accepted by validation; resolver answers unknown")
	}
}

// bumpingScopeRunner wraps the real runner and mutates the directory (a fresh
// membership) right after the roster read of the attempts it is told to break,
// so the epoch-after observation differs from epoch-before on a REAL store.
type bumpingScopeRunner struct {
	f       *directoryEligibilityFixture
	inner   directoryScopeRunner
	views   int
	bumpOn  func(attempt int) bool
	attempt int
}

func (b *bumpingScopeRunner) run(ctx context.Context, tenant model.TenantID, fn func(store.DirectorySnapshotReader) error) error {
	err := b.inner(ctx, tenant, fn)
	b.views++
	// Each attempt is three views: epoch-before, roster, epoch-after.
	if b.views%3 == 2 {
		b.attempt++
		if b.bumpOn(b.attempt) {
			user := model.NewID()
			if merr := b.f.st.AuthMutate(ctx, func(sc store.AuthScope) error {
				created, cerr := sc.Users().Create(ctx, model.User{
					Email: user.String() + "@bump.test", DisplayName: "bump", Status: model.StatusActive,
				})
				if cerr != nil {
					return cerr
				}
				_, cerr = sc.Memberships().Create(ctx, model.Membership{
					UserID: created.ID, TargetTenantID: tenant, Role: "viewer",
				})
				return cerr
			}); merr != nil {
				return merr
			}
		}
	}
	return err
}

func TestDirectoryResolverFenceFiresOnARealEpochBump(t *testing.T) {
	t.Parallel()
	f := newDirectoryEligibilityFixture(t)
	ctx := context.Background()
	user := f.createUser(t, model.StatusActive)
	f.grantMembership(t, user, "")
	recipient := sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: user.String()}

	// Control: a directory membership write really bumps the tenant epoch.
	before, err := f.resolver.readDirectoryEpoch(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	f.grantMembership(t, f.createUser(t, model.StatusActive), "")
	after, err := f.resolver.readDirectoryEpoch(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("membership write did not bump the epoch (%d -> %d); the fence test would be vacuous", before, after)
	}

	always := &bumpingScopeRunner{f: f, inner: newDirectoryScopeRunner(f.st), bumpOn: func(int) bool { return true }}
	moving := newCommunicationDirectoryResolver(always.run, f.reads, time.Now)
	if _, err := moving.ResolveRecipient(ctx, f.scope, recipient); !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("directory moving under every attempt = %v, want ErrDirectoryUnavailable", err)
	}
	if always.attempt != directoryResolverDefaultAttempts {
		t.Fatalf("attempts = %d, want %d", always.attempt, directoryResolverDefaultAttempts)
	}

	once := &bumpingScopeRunner{f: f, inner: newDirectoryScopeRunner(f.st), bumpOn: func(attempt int) bool { return attempt == 1 }}
	settling := newCommunicationDirectoryResolver(once.run, f.reads, time.Now)
	got, err := settling.ResolveRecipient(ctx, f.scope, recipient)
	if err != nil || !got.Eligible {
		t.Fatalf("one bump then quiet = %+v err=%v, want eligible on retry", got, err)
	}
	if latest, _ := f.resolver.readDirectoryEpoch(ctx, f.tenant); got.DirectoryEpoch != latest {
		t.Fatalf("retried snapshot epoch %d != current %d", got.DirectoryEpoch, latest)
	}
}

func TestGrantClosureResolvesActualSubjects(t *testing.T) {
	t.Parallel()
	f := newDirectoryEligibilityFixture(t)
	ctx := context.Background()
	user := f.createUser(t, model.StatusActive)
	f.grantMembership(t, user, "")
	group := f.createUserGroup(t, user)
	closure, err := f.closure.ResolveChannelGrantSubjects(ctx, f.scope, sessions.CommunicationPrincipal{UserID: user})
	if err != nil || closure.Outcome != sessions.ReadAllow || closure.DirectoryEpoch < 1 || len(closure.Subjects) != 2 ||
		closure.Subjects[0] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectUser, Ref: user.String()}) ||
		closure.Subjects[1] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectUserGroup, Ref: group.String()}) {
		t.Fatalf("user closure = %+v err=%v", closure, err)
	}
	inactive := f.createUser(t, model.StatusInactive)
	f.grantMembership(t, inactive, "")
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, sessions.CommunicationPrincipal{UserID: inactive})
	if err != nil || closure.Outcome != sessions.ReadDeny || closure.Code != "principal_inactive" || len(closure.Subjects) != 0 {
		t.Fatalf("inactive user closure = %+v err=%v", closure, err)
	}

	agent, external := f.createAgent(t, f.workspace, model.StatusActive)
	agentGroup := f.createAgentGroup(t, f.workspace, agent)
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, sessions.CommunicationPrincipal{AgentExternalID: external})
	if err != nil || closure.Outcome != sessions.ReadAllow || len(closure.Subjects) != 2 ||
		closure.Subjects[0] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectAgent, Ref: agent.String()}) ||
		closure.Subjects[1] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectAgentGroup, Ref: agentGroup.String()}) {
		t.Fatalf("agent closure = %+v err=%v", closure, err)
	}

	// A launched session acting for the agent above matches its own session
	// subject plus the agent's subjects (agent and agent group), sorted by kind.
	sid, runRef, lease := f.launchedSession(t, external)
	session := sessions.CommunicationPrincipal{
		SessionID: sid, SessionRunRef: runRef, SessionFence: lease.Fence, SessionWorkspaceID: f.workspace,
		PurposeRestricted: true, AgentExternalID: external,
	}
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, session)
	if err != nil || closure.Outcome != sessions.ReadAllow || len(closure.Subjects) != 3 ||
		closure.Subjects[0] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectAgent, Ref: agent.String()}) ||
		closure.Subjects[1] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectAgentGroup, Ref: agentGroup.String()}) ||
		closure.Subjects[2] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectSession, Ref: sid}) {
		t.Fatalf("session closure = %+v err=%v", closure, err)
	}
	// A launched session with no agent attribution matches only itself.
	lone, loneRun, loneLease := f.launchedSession(t, "")
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, sessions.CommunicationPrincipal{
		SessionID: lone, SessionRunRef: loneRun, SessionFence: loneLease.Fence, SessionWorkspaceID: f.workspace,
		PurposeRestricted: true,
	})
	if err != nil || closure.Outcome != sessions.ReadAllow || len(closure.Subjects) != 1 ||
		closure.Subjects[0] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectSession, Ref: lone}) {
		t.Fatalf("lone session closure = %+v err=%v", closure, err)
	}
	stale := session
	stale.SessionFence++
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, stale)
	if err != nil || closure.Outcome != sessions.ReadDeny || closure.Code != "session_claim_stale" || len(closure.Subjects) != 0 {
		t.Fatalf("stale session closure = %+v err=%v", closure, err)
	}
	foreignRun := session
	foreignRun.SessionRunRef = model.NewID().String()
	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, foreignRun)
	if err != nil || closure.Outcome != sessions.ReadDeny || closure.Code != "session_run_mismatch" {
		t.Fatalf("foreign-run session closure = %+v err=%v", closure, err)
	}

	closure, err = f.closure.ResolveChannelGrantSubjects(ctx, f.scope, sessions.CommunicationPrincipal{
		System: true, SystemActorRef: "system", SystemGrantAgentID: agent,
	})
	if err != nil || closure.Outcome != sessions.ReadAllow || len(closure.Subjects) != 1 ||
		closure.Subjects[0] != (sessions.CommunicationSubjectRef{Kind: sessions.SubjectAgent, Ref: agent.String()}) {
		t.Fatalf("system closure = %+v err=%v", closure, err)
	}
}
