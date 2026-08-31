// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWhoamiReportsOnlyCommunicationSessionPurposeCeilingAndWorkspace(t *testing.T) {
	h := newHarness(t)
	_ = h.adminLogin() // complete the installation gate before exercising whoami
	ctx := context.Background()
	var tenant model.TenantID
	if err := h.st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "communication whoami", Slug: "communication-whoami", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			workspace = ws.ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	system, err := auth.NewSystemOperator(
		"test:sessions-runtime", "exercise communication-session whoami projection",
	)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := h.authr.IssueCommunicationSessionCredential(
		ctx,
		system,
		auth.CommunicationSessionCredentialSpec{
			Tenant: tenant, WorkspaceID: workspace,
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
			ClaimFence: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := h.do(http.MethodGet, "/v1/auth/whoami", issued.Token, nil, nil)
	if got.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", got.code, got.raw)
	}
	grants, ok := got.body["grants"].([]any)
	if !ok || len(grants) != 1 {
		t.Fatalf("grants = %#v, want one", got.body["grants"])
	}
	grant, ok := grants[0].(map[string]any)
	if !ok {
		t.Fatalf("grant = %#v", grants[0])
	}
	if gotWorkspace, _ := grant["confined_workspace"].(string); gotWorkspace != workspace.String() {
		t.Fatalf("confined_workspace = %q, want %s", gotWorkspace, workspace)
	}
	permissions, ok := grant["permissions"].([]any)
	if !ok || len(permissions) != 4 {
		t.Fatalf("permissions = %#v, want exact four-permission ceiling", grant["permissions"])
	}
	want := map[string]bool{
		string(auth.CommunicationSessionDeliveryRead):         true,
		string(auth.CommunicationSessionDeliveryWrite):        true,
		string(auth.CommunicationSessionMessageSendWrite):     true,
		string(auth.CommunicationSessionHandoffResponseWrite): true,
	}
	for _, raw := range permissions {
		permission, _ := raw.(string)
		if !want[permission] {
			t.Fatalf("whoami widened purpose ceiling with %q", permission)
		}
		delete(want, permission)
	}
	if len(want) != 0 {
		t.Fatalf("whoami omitted purpose permissions: %v", want)
	}
}
