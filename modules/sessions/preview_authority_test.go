// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type previewAuthorityGateFunc func(context.Context, model.TenantID, PreviewIntent) (PreviewDecision, error)

func (g previewAuthorityGateFunc) Authorize(ctx context.Context, tenant model.TenantID, in PreviewIntent) (PreviewDecision, error) {
	return g(ctx, tenant, in)
}

// Only the transaction clock/capability boundary is injected. Authorization,
// revocation, complete authority locking and audit persistence use the real store.
type previewIssuanceData struct {
	api.ScopedData
	st     store.Store
	tenant model.TenantID
	wrap   func(store.Scope) store.Scope
}

func (d previewIssuanceData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

type previewIssuanceScope struct {
	store.Scope
	store.DirectoryAuthoritySnapshotLocker
	now func() (model.Timestamp, error)
}

func (s previewIssuanceScope) TransactionNow(context.Context) (model.Timestamp, error) {
	return s.now()
}

// Losing authority between gate authorization and issuance must not disclose a
// link or leave an opened event/grant. No sleeps or synthetic allow proofs.
func TestRunPreviewRetainsMutationAuthorityThroughIssuance(t *testing.T) {
	for _, variant := range []string{"fresh", "credential revoked", "user deactivated", "membership revoked", "grant revoked", "expired before lock", "expired after lock", "expired after audit", "empty proof", "wrong port", "no clock", "no directory barrier", "clock error", "commit error"} {
		t.Run(variant, func(t *testing.T) {
			f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
			ctx := t.Context()
			caller, err := f.authr.Authenticate(ctx, f.editor)
			if err != nil {
				t.Fatal(err)
			}
			var authority *PreviewAuthority
			gate := previewAuthorityGateFunc(func(ctx context.Context, tenant model.TenantID, in PreviewIntent) (PreviewDecision, error) {
				ctx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				ref, _ := in.Principal.Ref()
				p, err := f.authr.ResolvePrincipalScope(ctx, ref, tenant)
				if err != nil {
					return PreviewDecision{}, err
				}
				question := auth.Request{Principal: p, Tenant: tenant, Permission: permRunWrite,
					Resource: auth.ResourceAttrs{Kind: "preview", WorkspaceID: in.WorkspaceID},
					Route:    auth.RouteMetadata{CedarAction: "preview:open", RBACMinimumRole: auth.RoleEditor}}
				proof, err := f.authz.AuthorizeRouteMutation(ctx, question)
				if err != nil {
					return PreviewDecision{}, err
				}
				authority = &PreviewAuthority{Intent: in, Request: question, Authorization: proof}
				switch variant {
				case "credential revoked":
					err = f.authr.RevokeSession(ctx, p, p.CredID)
				case "user deactivated":
					err = f.st.AuthMutate(ctx, func(sc store.AuthScope) error {
						user, err := sc.Users().Get(ctx, p.UserID)
						if err != nil {
							return err
						}
						user.Status = model.StatusInactive
						_, err = sc.Users().Update(ctx, user)
						return err
					})
				case "membership revoked":
					err = f.st.AuthMutate(ctx, func(sc store.AuthScope) error {
						rows, _, err := sc.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: p.UserID.String()}}})
						if err != nil {
							return err
						}
						for _, row := range rows {
							if row.TargetTenantID == tenant {
								row.Role = auth.RoleViewer
								_, err = sc.Memberships().Update(ctx, row)
								return err
							}
						}
						return errors.New("membership missing")
					})
				case "grant revoked":
					err = f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
						epoch := sc.(store.AuthorizationEpochStore)
						fact, err := epoch.ReadAuthorizationEpoch(ctx)
						if err != nil {
							return err
						}
						_, err = epoch.BumpAuthorizationEpoch(ctx, fact)
						return err
					})
				case "empty proof":
					authority.Authorization = auth.RouteMutationAuthorization{}
				case "wrong port":
					authority.Intent.Port++
				}
				if err != nil {
					t.Fatalf("%s fixture mutation failed: %v", variant, err)
				}
				return PreviewDecision{Allowed: true, Authority: authority}, nil
			})
			WithPreviewGate(gate)(f.m)
			data := previewIssuanceData{st: f.st, tenant: f.tenant, wrap: func(sc store.Scope) store.Scope {
				if variant == "no clock" {
					return struct{ store.Scope }{sc}
				}
				var locker store.DirectoryAuthoritySnapshotLocker
				if variant != "no directory barrier" {
					locker = sc.(store.DirectoryAuthoritySnapshotLocker)
				}
				reads := 0
				return previewIssuanceScope{Scope: sc, DirectoryAuthoritySnapshotLocker: locker, now: func() (model.Timestamp, error) {
					reads++
					if variant == "clock error" {
						return model.Timestamp{}, errors.New("clock unavailable")
					}
					if variant == "expired before lock" || variant == "expired after lock" && reads > 1 || variant == "expired after audit" && reads > 2 {
						meta, err := authority.Authorization.MetadataFor(time.Now(), authority.Request)
						if err != nil {
							return model.Timestamp{}, err
						}
						return model.NewTimestamp(meta.FreshUntil), nil
					}
					return sc.(store.TransactionClock).TransactionNow(ctx)
				}}
			}}
			// Do not advertise an optional interface with a nil implementation.
			if variant == "no directory barrier" {
				data.wrap = func(sc store.Scope) store.Scope {
					return struct {
						store.Scope
						store.TransactionClock
					}{sc, sc.(store.TransactionClock)}
				}
			}
			var scoped api.ScopedData = data
			if variant == "commit error" {
				scoped = previewCommitFailure{data}
			}
			req := httptest.NewRequest("POST", "/runs/"+f.runRef+"/preview", strings.NewReader(`{"port":`+strconv.Itoa(f.appPort)+`}`))
			route := chi.NewRouteContext()
			route.URLParams.Add("ref", f.runRef)
			req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
			rec := httptest.NewRecorder()
			f.m.handleOpenRunPreview(rec, req, api.ModuleContext{Tenant: f.tenant, Principal: caller, Resource: auth.ResourceAttrs{WorkspaceID: f.workspace}, Data: scoped})
			if variant == "fresh" {
				if rec.Code != http.StatusCreated || f.openPreviews() != 1 || f.audited("sessions.run.preview", f.appPort) != 1 {
					t.Fatalf("fresh issuance = %d, grants=%d", rec.Code, f.openPreviews())
				}
				return
			}
			if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "/session/preview/") || f.openPreviews() != 0 || f.audited("sessions.run.preview", f.appPort) != 0 {
				t.Fatalf("%s crossed issuance: status=%d, grants=%d, opened=%d", variant, rec.Code, f.openPreviews(), f.audited("sessions.run.preview", f.appPort))
			}
			if variant != "commit error" {
				const reason = "the preview authority is no longer valid; open it again"
				refusals := f.previewAudits("sessions.run.preview.refused")
				if f.audited("sessions.run.preview.refused", f.appPort) != 1 || len(refusals) != 1 || refusals[0]["reason"] != reason || !strings.Contains(rec.Body.String(), reason) {
					t.Fatalf("%s did not record its run/port/reason refusal: response=%s, audit=%v", variant, rec.Body.String(), refusals)
				}
			}
		})
	}
}

type previewCommitFailure struct{ previewIssuanceData }

func (d previewCommitFailure) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, func(sc store.Scope) error {
		if err := fn(d.wrap(sc)); err != nil {
			return err
		}
		return errors.New("transaction cannot commit")
	})
}
