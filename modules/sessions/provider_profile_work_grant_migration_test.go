// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type beforeWorkGrantRegistry struct{ store.ExtensionRegistry }

func (r beforeWorkGrantRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerProfileKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if field.Name != colPPSessionWorkGrant {
				fields = append(fields, field)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestProviderProfileWorkGrantNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := context.Background()
			oldModule := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error { return oldModule.RegisterSchema(beforeWorkGrantRegistry{reg}) })
			tenant := ensureTenant(t, st, "work-grant-upgrade")
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			upgraded, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if upgraded.SessionWorkGrant != "" || upgraded.ID != profile.ID || upgraded.ConfigHome != profile.ConfigHome || upgraded.State != profile.State {
				t.Fatal("nullable upgrade changed the existing profile or backfilled authority")
			}
			actor, err := auth.NewSystemOperator("test:profile-operator", "exercise the profile grant expansion")
			if err != nil {
				t.Fatal(err)
			}
			WithWorkAuthorizer(auth.NewAuthorizer(nil))(m)
			var workspace model.ID
			if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{"work.read"}})
			granted, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{SessionWorkGrant: raw, WorkGrantActor: actor})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := decodeProfileWorkGrant(granted.SessionWorkGrant)
			if err != nil || grant == nil || grant.WorkspaceID != workspace {
				t.Fatal("new nullable column did not store the grant")
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			persisted, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil || persisted.SessionWorkGrant != granted.SessionWorkGrant {
				t.Fatal("grant did not survive reopen")
			}
		})
	}
}
