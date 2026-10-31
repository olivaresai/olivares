// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
	"strings"
	"testing"
)

func TestProviderProfileMaxDataClassRoundTrip(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "class-"+be.name)
			config, user, _, _ := twoHomes(t)
			p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, MaxDataClass: "internal"})
			if p.MaxDataClass != "internal" {
				t.Fatalf("create class = %q", p.MaxDataClass)
			}
			for _, class := range []string{"confidential", "regulated", "public", ""} {
				p, err := m.PatchProfile(context.Background(), tenant, p.Ref, ProfilePatch{MaxDataClass: &class})
				if err != nil {
					t.Fatal(err)
				}
				got, err := m.GetProfile(context.Background(), tenant, p.Ref)
				if err != nil {
					t.Fatal(err)
				}
				if got.MaxDataClass != class || m.toProfileDTO(got).MaxDataClass != class {
					t.Fatalf("class = %q, want %q", got.MaxDataClass, class)
				}
			}
		})
	}
}

func TestProviderProfileMaxDataClassRequiresAdmin(t *testing.T) {
	h := newHarness(t, New())
	h.m.UseExecutionEnvironmentRef(testEnvRef)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-admin")
	editor := dlpPatchToken(h, admin, tenant, "class-editor@example.invalid", auth.RoleEditor)
	created := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.code, created.raw)
	}
	path := "/v1/m/sessions/provider-profiles/" + created.body["profile_ref"].(string)
	rename := h.doJSON("PATCH", path, editor, map[string]any{"display_name": "Editor label"}, tenantHdr(tenant))
	if rename.code != http.StatusOK {
		t.Fatalf("editor cannot write labels: %d %s", rename.code, rename.raw)
	}
	r := h.doJSON("PATCH", path, editor, map[string]any{"max_data_class": "regulated"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("editor approved profile: %d %s", r.code, r.raw)
	}
	r = h.doJSON("PATCH", path, admin, map[string]any{"max_data_class": "regulated"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["max_data_class"] != "regulated" {
		t.Fatalf("admin approval=%d %s", r.code, r.raw)
	}
	r = h.doJSON("POST", "/v1/m/sessions/provider-profiles", editor, map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(), "max_data_class": "regulated"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("editor created approval: %d %s", r.code, r.raw)
	}
}

func TestProviderProfileMaxDataClassAudit(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "class-audit-"+be.name)
			actor := testActor()
			p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), MaxDataClass: "internal", WorkGrantActor: actor})
			previous := ""
			for i, class := range []string{"internal", "regulated", "internal", ""} {
				if i > 0 {
					if _, err := m.PatchProfile(t.Context(), tenant, p.Ref, ProfilePatch{MaxDataClass: &class, WorkGrantActor: actor}); err != nil {
						t.Fatal(err)
					}
				}
				events := acctAuditsOf(t, m, tenant, "sessions.provider_profile.max_data_class")
				if len(events) != i+1 {
					t.Fatalf("events=%d, want %d", len(events), i+1)
				}
				ev := events[i]
				if ev.actor != actor.Actor() || ev.targetKind != providerProfileKind || ev.targetID != p.ID || ev.meta["profile_ref"] != p.Ref || ev.meta["max_data_class"] != class || ev.meta["previous_max_data_class"] != previous {
					t.Fatalf("approval event=%+v, want actor=%s class=%q previous=%q", ev, actor.Actor(), class, previous)
				}
				previous = class
			}
			name := "rename"
			if _, err := m.PatchProfile(t.Context(), tenant, p.Ref, ProfilePatch{DisplayName: &name}); err != nil {
				t.Fatal(err)
			}
			mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
			if events := acctAuditsOf(t, m, tenant, "sessions.provider_profile.max_data_class"); len(events) != 4 {
				t.Fatalf("unrelated writes added approval events: %+v", events)
			}
		})
	}
}

type profileClassAuditFaultData struct {
	api.ModuleData
	dropped bool
}
type profileClassAuditFaultScope struct {
	store.Scope
	store.TransactionLocker
	dropped bool
}
type profileClassAuditFaultLog struct {
	store.AuditLog
	dropped bool
}

func (d profileClassAuditFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(profileClassAuditFaultScope{sc, sc.(store.TransactionLocker), d.dropped})
	})
}
func (s profileClassAuditFaultScope) Audit() store.AuditLog {
	return profileClassAuditFaultLog{s.Scope.Audit(), s.dropped}
}
func (l profileClassAuditFaultLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == "sessions.provider_profile.max_data_class" {
		if l.dropped {
			return model.AuditEvent{}, nil
		}
		return model.AuditEvent{}, errors.New("synthetic classification audit failure")
	}
	return l.AuditLog.Append(ctx, draft)
}
func TestProviderProfileMaxDataClassAuditRollsBack(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "class-rollback-"+be.name)
			p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), MaxDataClass: "internal"})
			data := m.Data
			for _, dropped := range []bool{false, true} {
				m.Data = profileClassAuditFaultData{data, dropped}
				class := "regulated"
				wantError := "synthetic classification audit failure"
				if dropped {
					wantError = "audit was not persisted"
				}
				if _, err := m.PatchProfile(t.Context(), tenant, p.Ref, ProfilePatch{MaxDataClass: &class}); err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("approval patch did not reach the audit fault: %v", err)
				}
				in := CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), MaxDataClass: class}
				if _, err := m.CreateProfile(t.Context(), tenant, in); err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("approval create did not reach the audit fault: %v", err)
				}
				m.Data = data
				got, err := m.GetProfile(t.Context(), tenant, p.Ref)
				if err != nil || got.MaxDataClass != "internal" {
					t.Fatalf("failed audit changed approval: %+v %v", got, err)
				}
				rows, _, err := m.ListProfiles(t.Context(), tenant, "", model.Query{Limit: 10})
				if err != nil || len(rows) != 1 {
					t.Fatalf("failed audit left a profile: rows=%d err=%v", len(rows), err)
				}
				if events := acctAuditsOf(t, m, tenant, "sessions.provider_profile.max_data_class"); len(events) != 1 {
					t.Fatalf("failed audit left events: %+v", events)
				}
			}
		})
	}
}
