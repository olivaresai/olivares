// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/modules/sessions"
)

type promotionRecoveryProbe struct {
	enabled bool
	calls   []model.TenantID
	errFor  map[model.TenantID]error
}

func (p *promotionRecoveryProbe) CommunicationSessionCredentialsEnabled() bool {
	return p.enabled
}

func (p *promotionRecoveryProbe) RecoverRuntimeCredentials(
	_ context.Context,
	tenant model.TenantID,
) error {
	p.calls = append(p.calls, tenant)
	return p.errFor[tenant]
}

func recoveryOrg(id model.ID, region string, status model.LifecycleStatus) model.Org {
	return model.Org{
		BaseFields: model.BaseFields{ID: id, TenantID: model.TenantID(id)},
		DataRegion: region,
		Status:     status,
	}
}

func TestSessionRuntimePromotionRecoveryIsOffUntilExplicitlyEnabled(t *testing.T) {
	sentinel := errors.New("authoritative enumeration unavailable")
	listed := 0
	probe := &promotionRecoveryProbe{}
	err := recoverSessionRuntimeCredentialsForPromotion(
		context.Background(),
		func(context.Context) ([]model.Org, error) {
			listed++
			return nil, sentinel
		},
		nil,
		probe,
	)
	if err != nil || listed != 0 || len(probe.calls) != 0 {
		t.Fatalf("K3-OFF promotion recovery = err %v listed %d calls %v", err, listed, probe.calls)
	}
}

func TestSessionRuntimePromotionRecoveryPropagatesAuthoritativeEnumerationFailure(t *testing.T) {
	sentinel := errors.New("authoritative enumeration unavailable")
	probe := &promotionRecoveryProbe{enabled: true}
	err := recoverSessionRuntimeCredentialsForPromotion(
		context.Background(),
		func(context.Context) ([]model.Org, error) { return nil, sentinel },
		nil,
		probe,
	)
	if !errors.Is(err, sentinel) || len(probe.calls) != 0 {
		t.Fatalf("enumeration failure = %v calls %v", err, probe.calls)
	}
}

func TestSessionRuntimePromotionRecoveryRejectsMalformedInventoryBeforeEffects(t *testing.T) {
	valid := model.TenantID(model.NewID())
	reg, err := residency.NewRegistry("", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe := &promotionRecoveryProbe{enabled: true}
	err = recoverSessionRuntimeCredentialsForPromotion(
		context.Background(),
		func(context.Context) ([]model.Org, error) {
			return []model.Org{
				recoveryOrg(model.ID(valid), "", model.StatusActive),
				{BaseFields: model.BaseFields{ID: model.ID("malformed-business-org")}},
			}, nil
		},
		reg,
		probe,
	)
	if err == nil || !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("malformed authoritative inventory = %v", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("malformed inventory caused partial recovery effects: %v", probe.calls)
	}
}

func TestSessionRuntimePromotionRecoveryIncludesSuspendedLocalAndSkipsForeign(t *testing.T) {
	localActive := model.TenantID(model.NewID())
	localSuspended := model.TenantID(model.NewID())
	foreign := model.TenantID(model.NewID())
	reg, err := residency.NewRegistry("us-east", []string{"eu-west"})
	if err != nil {
		t.Fatal(err)
	}
	probe := &promotionRecoveryProbe{
		enabled: true,
		errFor: map[model.TenantID]error{
			localSuspended: errors.New("suspended tenant credential revoke failed"),
		},
	}
	err = recoverSessionRuntimeCredentialsForPromotion(
		context.Background(),
		func(context.Context) ([]model.Org, error) {
			return []model.Org{
				recoveryOrg(model.ID(model.SystemTenantID), "", model.StatusActive),
				recoveryOrg(model.ID(localActive), "us-east", model.StatusActive),
				recoveryOrg(model.ID(localSuspended), "us-east", model.StatusSuspended),
				recoveryOrg(model.ID(foreign), "eu-west", model.StatusActive),
			}, nil
		},
		reg,
		probe,
	)
	if err == nil || !slices.Equal(probe.calls, []model.TenantID{localActive, localSuspended}) {
		t.Fatalf("regional promotion recovery = err %v calls %v", err, probe.calls)
	}
}

func TestSessionRuntimePromotionRecoveryRevokesRealTokensForSuspendedTenant(t *testing.T) {
	ctx := context.Background()
	ss := sessions.New()
	st, err := coreengine.Open(
		ctx,
		store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		ss.RegisterSchema,
	)
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
			Name: "Suspended", Slug: "suspended", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rawAuthenticator := auth.NewAuthenticator(st, nil)
	actor, err := auth.NewSystemOperator("sessions-runtime-test", "exercise suspended custody recovery")
	if err != nil {
		t.Fatal(err)
	}
	runRef := model.NewID().String()
	sid := "osn_" + model.NewID().String()
	const agentRef = "agent:suspended-recovery"
	workSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: sid, RunRef: runRef, AgentRef: agentRef, ClaimFence: 1,
	}
	communicationSpec := auth.CommunicationSessionCredentialSpec{
		Tenant: tenant, WorkspaceID: workspace, SessionRef: sid,
		RunRef: runRef, AgentRef: agentRef, ClaimFence: 1,
	}
	work, err := rawAuthenticator.IssueWorkSessionCredential(ctx, actor, workSpec)
	if err != nil {
		t.Fatal(err)
	}
	communication, err := rawAuthenticator.IssueCommunicationSessionCredential(
		ctx, actor, communicationSpec,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "stopped", "last_event_seq": int64(0),
			"agent_ref": agentRef, "claim_sid": sid,
			"claim_holder": agentRef, "claim_fence": int64(1),
			"communication_workspace_id":          workspace.String(),
			"work_credential_id":                  work.ID.String(),
			"work_credential_expires_at":          model.NewTimestamp(work.ExpiresAt).String(),
			"communication_credential_id":         communication.ID.String(),
			"communication_credential_expires_at": model.NewTimestamp(communication.ExpiresAt).String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	guarded := suspension.Guard(st, nil)
	normalAuthenticator := auth.NewAuthenticator(guarded, nil)
	ss.UseData(api.NewModuleData(guarded))
	ss.RecoveryData = api.NewModuleData(st)
	ss.WorkSessionCreds = sessionWorkCredentialSource{authenticator: normalAuthenticator}
	ss.CommunicationSessionCreds = sessionCommunicationCredentialSource{authenticator: normalAuthenticator}
	ss.RecoveryWorkSessionCreds, ss.RecoveryCommunicationSessionCreds = sessionWorkCredentialSource{authenticator: rawAuthenticator}, sessionCommunicationCredentialSource{authenticator: rawAuthenticator}
	ss.EnableCommunicationSessionCredentials()
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(ctx, tenant, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: "ordinary-service-check", At: time.Now(),
	}); !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("ordinary sessions service bypassed suspension: %v", err)
	}
	if err := ss.RecoverRuntimeCredentials(ctx, tenant); err != nil {
		t.Fatalf("suspended custody recovery: %v", err)
	}
	if _, err := rawAuthenticator.Authenticate(ctx, work.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("work bearer survived suspended recovery: %v", err)
	}
	if _, err := rawAuthenticator.Authenticate(ctx, communication.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("communication bearer survived suspended recovery: %v", err)
	}
	if _, err := ss.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: "ordinary-service-still-denied", At: time.Now(),
	}); !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("custody seam re-enabled ordinary service: %v", err)
	}
}

// An offline CLI command (secrets put/rotate, superadmin status, audit, dr) boots
// the engine against the same SQLite data directory a running `serve` owns. Its
// process holds no runtime handle for any session, so leadership-promotion recovery
// there would mark every live session stopped and revoke its credentials while serve
// keeps the agent running. Only the serve boot may recover.
func TestBootNonServeLeavesLiveSessionsAndCredentials(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("serve boot: %v", err)
	}
	if !eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		_ = eng.Close()
		t.Fatal("precondition: local K3 activation must enable promotion recovery")
	}
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Live", Slug: "live", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	actor, err := auth.NewSystemOperator("sessions-runtime-test", "seed a live session")
	if err != nil {
		t.Fatal(err)
	}
	runRef := model.NewID().String()
	sid := "osn_" + model.NewID().String()
	const agentRef = "agent:live-session"
	authr := auth.NewAuthenticator(eng.store, nil)
	work, err := authr.IssueWorkSessionCredential(ctx, actor, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: sid, RunRef: runRef, AgentRef: agentRef, ClaimFence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	communication, err := authr.IssueCommunicationSessionCredential(ctx, actor, auth.CommunicationSessionCredentialSpec{
		Tenant: tenant, WorkspaceID: workspace, SessionRef: sid, RunRef: runRef, AgentRef: agentRef, ClaimFence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "running", "last_event_seq": int64(0),
			"agent_ref": agentRef, "claim_sid": sid,
			"claim_holder": agentRef, "claim_fence": int64(1),
			"communication_workspace_id":          workspace.String(),
			"work_credential_id":                  work.ID.String(),
			"work_credential_expires_at":          model.NewTimestamp(work.ExpiresAt).String(),
			"communication_credential_id":         communication.ID.String(),
			"communication_credential_expires_at": model.NewTimestamp(communication.ExpiresAt).String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	runState := func(st store.Store) string {
		t.Helper()
		var state string
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("sessions.run")
			if err != nil {
				return err
			}
			records, _, err := repo.List(ctx, model.Query{Limit: 10})
			for _, r := range records {
				if r.String("run_ref") == runRef {
					state = r.String("state")
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if state == "" {
			t.Fatalf("run %s not found", runRef)
		}
		return state
	}
	bearersValid := func(st store.Store) (bool, bool) {
		t.Helper()
		checker := auth.NewAuthenticator(st, nil)
		valid := func(token string) bool {
			_, err := checker.Authenticate(ctx, token)
			if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("authenticate: %v", err)
			}
			return err == nil
		}
		return valid(work.Token), valid(communication.Token)
	}

	// The CLI boots: mutating (auditBoot), read-only (auditBootRO) and plain (dr).
	for _, cfg := range []bootConfig{
		{DataDir: dir, Engine: "sqlite", Version: "test", NoImplicitInstall: true},
		{DataDir: dir, Engine: "sqlite", Version: "test", ReadOnly: true},
		{DataDir: dir, Engine: "sqlite", Version: "test"},
	} {
		cli, err := boot(ctx, cfg)
		if err != nil {
			t.Fatalf("offline boot (read-only=%v no-implicit-install=%v): %v", cfg.ReadOnly, cfg.NoImplicitInstall, err)
		}
		state := runState(cli.store)
		workOK, commOK := bearersValid(cli.store)
		if err := cli.Close(); err != nil {
			t.Fatal(err)
		}
		if state != "running" || !workOK || !commOK {
			t.Fatalf("offline boot (read-only=%v no-implicit-install=%v) touched a live session: state=%q work valid=%v communication valid=%v",
				cfg.ReadOnly, cfg.NoImplicitInstall, state, workOK, commOK)
		}
	}

	// A serve restart still owns the estate: the orphaned row is recovered and its
	// bearers revoked, which also proves the setup above reaches recovery at all.
	eng, err = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("serve restart: %v", err)
	}
	defer func() { _ = eng.Close() }()
	if state := runState(eng.store); state != "stopped" {
		t.Fatalf("serve restart left the orphaned run %q", state)
	}
	if workOK, commOK := bearersValid(eng.store); workOK || commOK {
		t.Fatalf("serve restart kept orphan bearers: work=%v communication=%v", workOK, commOK)
	}
}

// The same offline boots must not resume watching the launches a running `serve`
// holds waiting for an approval. The watcher would act on the
// live engine's runs from a process that owns none of them: an approved launch
// would start inside the CLI and die with it, and any other status would end the
// live engine's waiting run. Only the serve boot may recover them.
func TestBootNonServeLeavesWaitingLaunches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("serve boot: %v", err)
	}
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Waiting", Slug: "waiting", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The approval reference names no approval, so every watcher reads it as
	// expired at its first tick and ends the run: a deterministic recovery probe.
	runRef := model.NewID().String()
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "waiting_approval", "last_event_seq": int64(0),
			"agent_ref": "agent:waiting-launch", "approval_ref": model.NewID().String(),
			"queued_intent": "{}",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	runState := func(st store.Store) string {
		t.Helper()
		var state string
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("sessions.run")
			if err != nil {
				return err
			}
			records, _, err := repo.List(ctx, model.Query{Limit: 10})
			for _, r := range records {
				if r.String("run_ref") == runRef {
					state = r.String("state")
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if state == "" {
			t.Fatalf("run %s not found", runRef)
		}
		return state
	}

	// The CLI boots: mutating (auditBoot), read-only (auditBootRO) and plain. The
	// read-only one cannot end the run, so only the other two can show the bug; it
	// stays to cover every offline boot shape. Each boot stays up for about eight
	// watcher ticks (250 ms) before it reads the run.
	for _, cfg := range []bootConfig{
		{DataDir: dir, Engine: "sqlite", Version: "test", NoImplicitInstall: true},
		{DataDir: dir, Engine: "sqlite", Version: "test", ReadOnly: true},
		{DataDir: dir, Engine: "sqlite", Version: "test"},
	} {
		cli, err := boot(ctx, cfg)
		if err != nil {
			t.Fatalf("offline boot (read-only=%v no-implicit-install=%v): %v", cfg.ReadOnly, cfg.NoImplicitInstall, err)
		}
		time.Sleep(2 * time.Second)
		state := runState(cli.store)
		if err := cli.Close(); err != nil {
			t.Fatal(err)
		}
		if state != "waiting_approval" {
			t.Fatalf("offline boot (read-only=%v no-implicit-install=%v) acted on a waiting launch: state=%q",
				cfg.ReadOnly, cfg.NoImplicitInstall, state)
		}
	}

	// A serve restart still recovers the waiting launch, which also proves the
	// setup above reaches the approval watcher at all.
	eng, err = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("serve restart: %v", err)
	}
	defer func() { _ = eng.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	for runState(eng.store) == "waiting_approval" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if state := runState(eng.store); state != "expired" {
		t.Fatalf("serve restart did not recover the waiting launch: state=%q", state)
	}
}
