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
	ss.UseRuntimeCredentialRecoveryData(api.NewModuleData(st))
	ss.UseWorkSessionCredentialSource(sessionWorkCredentialSource{authenticator: normalAuthenticator})
	ss.UseCommunicationSessionCredentialSource(
		sessionCommunicationCredentialSource{authenticator: normalAuthenticator},
	)
	ss.UseRuntimeCredentialRecoverySources(
		sessionWorkCredentialSource{authenticator: rawAuthenticator},
		sessionCommunicationCredentialSource{authenticator: rawAuthenticator},
	)
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
