// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAdmitAnswersWithoutATransport asks the admission seam directly, with a context and
// no request or response, and checks every answer the REST door renders: admitted, the
// route's own denial (403, or 404 when it conceals), undecided (503) and step-up. The
// HTTP shapes stay pinned by the route tests; this pins that the decision needs no HTTP.
func TestAdmitAnswersWithoutATransport(t *testing.T) {
	tenant := model.NewTenantID()
	viewer := auth.ScopedPrincipal(model.NewID(), "viewer", tenant, auth.RoleViewer)
	outsider := auth.ScopedPrincipal(model.NewID(), "outsider", model.NewTenantID(), auth.RoleOwner)
	ask := func(p auth.Principal, perm auth.Permission, meta auth.RouteMetadata) auth.Request {
		return auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: auth.ResourceFor(perm), Route: meta}
	}
	stepUp := auth.RouteMetadata{MinimumAAL: auth.AAL3}
	s := &Server{authz: auth.NewAuthorizer(nil), log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	cases := []struct {
		name     string
		req      auth.Request
		denial   error
		governed routeGovernance
		want     error // nil: admitted
	}{
		{"ungoverned read by a member is admitted", ask(viewer, "agent:read", auth.RouteMetadata{}), errForbidden, ungovernedRoute, nil},
		{"ungoverned denial is the route's denial", ask(viewer, "agent:write", auth.RouteMetadata{}), errForbidden, ungovernedRoute, errForbidden},
		{"ungoverned concealed action denial stays not found", ask(viewer, "agent:write", auth.RouteMetadata{}), store.ErrNotFound, ungovernedRoute, store.ErrNotFound},
		{"ungoverned concealed read by a non-member stays not found", ask(outsider, "agent:read", auth.RouteMetadata{}), store.ErrNotFound, ungovernedRoute, store.ErrNotFound},
		{"ungoverned read by a non-member is forbidden", ask(outsider, "agent:read", auth.RouteMetadata{}), errForbidden, ungovernedRoute, errForbidden},
		{"a nil denial still refuses", ask(viewer, "agent:write", auth.RouteMetadata{}), nil, ungovernedRoute, errForbidden},
		{"governed without sealed evidence is undecided", ask(viewer, "agent:read", auth.RouteMetadata{}), errForbidden, governedRoute, auth.ErrRouteUndecided},
		{"governed undecided is not concealed as not found", ask(viewer, "agent:read", auth.RouteMetadata{}), store.ErrNotFound, governedRoute, auth.ErrRouteUndecided},
		{"step-up comes before the decision", ask(viewer, "agent:read", stepUp), errForbidden, governedRoute, auth.ErrStepUpRequired},
		{"step-up binds an ungoverned door too", ask(viewer, "agent:read", stepUp), errForbidden, ungovernedRoute, auth.ErrStepUpRequired},
		{"step-up comes before a concealed denial", ask(outsider, "agent:read", stepUp), store.ErrNotFound, governedRoute, auth.ErrStepUpRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			witness, err := s.admit(t.Context(), c.req, c.denial, c.governed)
			if c.want == nil && err != nil {
				t.Fatalf("admit refused with %v, want admitted", err)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("admit answered %v, want %v", err, c.want)
			}
			if !reflect.ValueOf(witness).IsZero() {
				t.Fatalf("admit returned a witness for an answer that mints none: %+v", witness)
			}
		})
	}
}

// TestAdmitCarriesTheDeploymentStepUpRemedy pins that the step-up refusal names the remedy
// the deployment's policy asks for, read from the context and not from a request.
func TestAdmitCarriesTheDeploymentStepUpRemedy(t *testing.T) {
	tenant := model.NewTenantID()
	s := &Server{authz: auth.NewAuthorizer(nil), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := auth.WithStepUpSource(t.Context(), func(context.Context) (string, error) { return auth.StepUpTOTP, nil })
	_, err := s.admit(ctx, auth.Request{
		Principal:  auth.ScopedPrincipal(model.NewID(), "viewer", tenant, auth.RoleViewer),
		Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read"),
		Route: auth.RouteMetadata{MinimumAAL: auth.AAL3},
	}, errForbidden, governedRoute)
	if want := auth.StepUpRequiredFor(auth.StepUpTOTP); !errors.Is(err, auth.ErrStepUpRequired) || err.Error() != want.Error() {
		t.Fatalf("admit answered %v, want the TOTP remedy %v", err, want)
	}
}

// TestAdmitRecordsTheQuestionItAsked pins two behaviors the HTTP answer cannot show: a
// concealed read is decided through AuthorizeDisclosure, so present, absent and foreign rows
// cost the same policy work, and a step-up refusal is retained as authorization evidence.
func TestAdmitRecordsTheQuestionItAsked(t *testing.T) {
	tenant := model.NewTenantID()
	outsider := auth.ScopedPrincipal(model.NewID(), "outsider", model.NewTenantID(), auth.RoleOwner)
	s := &Server{authz: auth.NewAuthorizer(nil), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		name     string
		perm     auth.Permission
		meta     auth.RouteMetadata
		denial   error
		governed routeGovernance
		want     func(auth.RetainedAuthorization) bool
	}{
		{"a concealed read asks the disclosure question", "agent:read", auth.RouteMetadata{}, store.ErrNotFound, ungovernedRoute,
			func(r auth.RetainedAuthorization) bool { return r.Disclosure }},
		{"a concealed write asks the ordinary question", "agent:write", auth.RouteMetadata{}, store.ErrNotFound, ungovernedRoute,
			func(r auth.RetainedAuthorization) bool { return !r.Disclosure }},
		{"a plain read asks the ordinary question", "agent:read", auth.RouteMetadata{}, errForbidden, ungovernedRoute,
			func(r auth.RetainedAuthorization) bool { return !r.Disclosure }},
		{"a step-up refusal is retained", "agent:read", auth.RouteMetadata{MinimumAAL: auth.AAL3}, errForbidden, governedRoute,
			func(r auth.RetainedAuthorization) bool { return r.Precondition == "step_up_required" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var records []auth.AuthorizationRecord
			ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { records = append(records, r) })
			_, err := s.admit(ctx, auth.Request{
				Principal: outsider, Permission: c.perm, Tenant: tenant,
				Resource: auth.ResourceFor(c.perm), Route: c.meta,
			}, c.denial, c.governed)
			if err == nil {
				t.Fatal("admit admitted a principal with no membership in the tenant")
			}
			if len(records) != 1 || !c.want(records[0].Snapshot) {
				t.Fatalf("admit recorded %+v, want one record matching %q", records, c.name)
			}
		})
	}
}

// TestAdmitSeedsTheOrchestrationCredentialWorkspace pins the one rewrite admit makes to the
// question before asking it: a dedicated orchestration credential's collection read or write
// of work and decisions is asked about the credential's own workspace. Another principal,
// another permission, an entity request and a request that already names a workspace are
// asked as they came.
func TestAdmitSeedsTheOrchestrationCredentialWorkspace(t *testing.T) {
	st, tenant, workspace, other := confineTestFixture(t)
	ctx := t.Context()
	authr := auth.NewAuthenticator(st, nil)
	actor, err := auth.NewSystemOperator("test:admission", "exercise the orchestration workspace seeding")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := authr.IssueOrchestrationSessionCredential(ctx, actor, auth.OrchestrationSessionCredentialSpec{
		Tenant: tenant, WorkspaceID: workspace, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1,
		ProfileRef: "ppf_" + model.NewID().String(), GrantID: model.NewID(),
		Capabilities: []string{"work.create", "work.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := authr.Authenticate(ctx, issued.Token)
	if err != nil || !orchestrator.IsOrchestrationSessionCredential() {
		t.Fatalf("fixture: the bearer did not authenticate as an orchestration credential (%v)", err)
	}
	// A session workspace alone is not an orchestration credential: the control must carry one,
	// or seeding it would be a no-op whichever principal the seam accepted.
	viewer := auth.ScopedPrincipal(model.NewID(), "viewer", tenant, auth.RoleViewer)
	viewer.SessionWorkspaceID = workspace
	collection := func(perm auth.Permission) auth.ResourceAttrs { return auth.ResourceFor(perm) }
	entity := collection("sessions:work:read")
	entity.ID = model.NewID().String()
	named := collection("sessions:work:read")
	named.WorkspaceID = other
	// An allowed read leaves no authorization record, so every question is answered no.
	s := &Server{authz: auth.NewAuthorizer(denyEveryQuestion{}), log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	cases := []struct {
		name string
		p    auth.Principal
		perm auth.Permission
		res  auth.ResourceAttrs
		want model.ID
	}{
		{"work read", orchestrator, "sessions:work:read", collection("sessions:work:read"), workspace},
		{"work write", orchestrator, "sessions:work:write", collection("sessions:work:write"), workspace},
		{"decision read", orchestrator, "sessions:decision:read", collection("sessions:decision:read"), workspace},
		{"decision write", orchestrator, "sessions:decision:write", collection("sessions:decision:write"), workspace},
		{"another permission", orchestrator, "sessions:work:admin", collection("sessions:work:admin"), ""},
		{"an entity", orchestrator, "sessions:work:read", entity, ""},
		{"a named workspace", orchestrator, "sessions:work:read", named, other},
		{"another principal", viewer, "sessions:work:read", collection("sessions:work:read"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var records []auth.AuthorizationRecord
			rctx := auth.WithAuthorizationRecording(ctx, func(r auth.AuthorizationRecord) { records = append(records, r) })
			// The answer is not under test here, only the question the authorizer was asked.
			_, _ = s.admit(rctx, auth.Request{Principal: c.p, Permission: c.perm, Tenant: tenant, Resource: c.res}, errForbidden, ungovernedRoute)
			if len(records) != 1 || records[0].Snapshot.Resource.WorkspaceID != c.want {
				t.Fatalf("admit asked about %+v, want one question about workspace %q", records, c.want)
			}
		})
	}
}

// denyEveryQuestion is a policy overlay that refuses whatever reaches it.
type denyEveryQuestion struct{}

func (denyEveryQuestion) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Reason: "fixture: every question is refused"}, nil
}
