// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestScopedGrantExpiryLeavesCedarProjection(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		expiresAt string
		want      bool
	}{
		{name: "expired", expiresAt: now.Add(-time.Hour).Format(time.RFC3339), want: false},
		{name: "active", expiresAt: now.Add(time.Hour).Format(time.RFC3339), want: true},
		{name: "legacy", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := scopedGrant{
				SubjectKind: "role", SubjectRef: "viewer", Role: "viewer",
				Scope: scopeSpec{Tree: "workspace", Ref: "dept", Class: "model"}, ExpiresAt: tc.expiresAt,
			}
			projected := projectManagedCedarAt([]scopedGrant{grant}, nil, nil, now)
			if got := strings.Contains(projected, "permit"); got != tc.want {
				t.Fatalf("projection contains permit = %v, want %v:\n%s", got, tc.want, projected)
			}
		})
	}
}

func TestGrantExpiryReloadRequiredAfterFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   scopedTenantState
		loaded  bool
		changed bool
		want    bool
	}{
		{name: "unavailable after failed reload", state: scopedTenantState{available: false}, loaded: true, want: true},
		{name: "never loaded", state: scopedTenantState{available: true}, want: true},
		{name: "changed projection", state: scopedTenantState{available: true}, loaded: true, changed: true, want: true},
		{name: "coherent unchanged", state: scopedTenantState{available: true}, loaded: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := grantExpiryReloadRequired(tc.state, tc.loaded, tc.changed); got != tc.want {
				t.Fatalf("grantExpiryReloadRequired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGrantExpiryPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{name: "default and maximum", spec: `{"default_seconds":3600,"maximum_seconds":7200}`},
		{name: "legacy unlimited", spec: `{}`},
		{name: "negative default", spec: `{"default_seconds":-1}`, wantErr: true},
		{name: "negative maximum", spec: `{"maximum_seconds":-1}`, wantErr: true},
		{name: "default above maximum", spec: `{"default_seconds":7200,"maximum_seconds":3600}`, wantErr: true},
		{name: "unknown field", spec: `{"unexpected":true}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := policyRequest{Name: "Access expiry", Kind: "grant_expiry", Enabled: true, Spec: json.RawMessage(tc.spec)}
			canonical, msg := in.validateAndCanonicalize()
			if tc.wantErr {
				if msg == "" {
					t.Fatalf("spec %s was accepted: %#v", tc.spec, canonical)
				}
				return
			}
			if msg != "" {
				t.Fatalf("valid spec refused: %s", msg)
			}
		})
	}
}

func TestDelegatedGrantCannotOutliveDelegator(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	tenant := model.TenantID("t1")
	actor := auth.ScopedPrincipal("c", "u", tenant, auth.RoleViewer)
	parent := scopedGrant{
		SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: auth.RoleAdmin,
		Scope: scopeSpec{Tree: scopeWorkspace, Ref: "dept"}, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}
	for _, tc := range []struct {
		name         string
		childExpiry  string
		parentExpiry string
		wantErr      bool
	}{
		{name: "permanent child", wantErr: true},
		{name: "later child", childExpiry: now.Add(2 * time.Hour).Format(time.RFC3339), wantErr: true},
		{name: "equal child", childExpiry: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "earlier child", childExpiry: now.Add(30 * time.Minute).Format(time.RFC3339)},
		{name: "expired parent", childExpiry: now.Add(30 * time.Minute).Format(time.RFC3339), parentExpiry: now.Add(-time.Minute).Format(time.RFC3339), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent.ExpiresAt = tc.parentExpiry
			if parent.ExpiresAt == "" {
				parent.ExpiresAt = now.Add(time.Hour).Format(time.RFC3339)
			}
			child := scopedGrant{
				SubjectKind: subjectUser, SubjectRef: "person", Role: auth.RoleEditor,
				Scope: scopeSpec{Tree: scopeWorkspace, Ref: "dept"}, ExpiresAt: tc.childExpiry,
			}
			err := canDelegateAt(context.Background(), nil, actor, tenant, child, []scopedGrant{parent}, nil, nil, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("canDelegateAt() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type brokenGrantPageRepo struct {
	store.GenericRepo
}

func (brokenGrantPageRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return []model.Record{{model.ColID: "one"}}, model.Page{HasMore: true}, nil
}

func TestListAllRefusesBrokenPagination(t *testing.T) {
	_, err := listAll(context.Background(), brokenGrantPageRepo{})
	if !errors.Is(err, store.ErrPageContinuation) {
		t.Fatalf("listAll() error = %v, want ErrPageContinuation", err)
	}
}
