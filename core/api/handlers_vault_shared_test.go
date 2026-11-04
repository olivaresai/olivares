// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// vaultGrants is the scoped engine's answer for a shared vault: a positive grant for one
// person, one permission and one folder, abstain for everything else. Revoking is deleting
// the row, as it is for the real engine. A row holds the node its permit is anchored at: the
// folder itself, or a node above it (a workspace or the tenant), which the real engine also
// answers with a grant for everything under that node.
type vaultGrants struct {
	mu   sync.Mutex
	rows map[string]string
}

const aboveTheVault = "workspace-above-the-vault"

func vaultGrantKey(user, permission, folder string) string {
	return user + "|" + permission + "|" + folder
}

// set grants (or, with on false, revokes) the right at the folder itself.
func (g *vaultGrants) set(user model.ID, permission string, folder model.ID, on bool) {
	anchor := ""
	if on {
		anchor = folder.String()
	}
	g.anchor(user, permission, folder, anchor)
}

// anchor grants the right for the folder through a permit anchored at node.
func (g *vaultGrants) anchor(user model.ID, permission string, folder model.ID, node string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rows == nil {
		g.rows = map[string]string{}
	}
	g.rows[vaultGrantKey(user.String(), permission, folder.String())] = node
}

func (g *vaultGrants) Scoped(ctx context.Context, req auth.Request) (auth.ScopedDecision, error) {
	sd, _, err := g.ScopedExplanation(ctx, req)
	return sd, err
}

// ScopedExplanation names the grant that decided and the node it is anchored at, as the real
// engine does: the organization owner's implicit grant has none.
func (g *vaultGrants) ScopedExplanation(_ context.Context, req auth.Request) (auth.ScopedDecision, auth.DecisionExplanation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if node := g.rows[vaultGrantKey(req.Principal.UserID.String(), string(req.Permission), req.Resource.ID)]; node != "" {
		return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "vault grant"},
			auth.DecisionExplanation{Because: []auth.DecisionGrant{{GrantID: "vault-grant", Node: auth.DecisionNode{Kind: "resource", Ref: node}}}}, nil
	}
	return auth.ScopedDecision{}, auth.DecisionExplanation{}, nil
}

// vaultOverlay is the deny overlay (ABAC or an external PDP) of an organization that
// refuses reads of one resource, for example a sensitivity label or an outside network.
type vaultOverlay struct {
	mu     sync.Mutex
	denied string
}

func (o *vaultOverlay) deny(folder model.ID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.denied = folder.String()
}

func (o *vaultOverlay) lift() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.denied = ""
}

func (o *vaultOverlay) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if req.Permission == "resource:read" && o.denied != "" && req.Resource.ID == o.denied {
		return auth.Decision{Allow: false, Reason: "overlay: reads of this vault are refused"}, nil
	}
	return auth.Decision{Allow: true, Reason: "overlay: no rule"}, nil
}

// A shared vault is open to the people granted a right at its folder and to nobody else:
// not an editor or an admin of the organization, not through another folder, and not once
// the grant is gone. One ciphertext serves every grantee.
func TestSharedVaultItemsFollowTheGrantAtTheFolder(t *testing.T) {
	grants, overlay := &vaultGrants{}, &vaultOverlay{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
		o.Authorizer = auth.NewAuthorizer(overlay, auth.WithScopedGrants(grants))
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "shared-vault-co")
	f := vaultFixture{h: h, root: root, tenant: tenant}
	sharer := h.mkMember(root, "sharer@vault.io", "fixturepass1", auth.RoleViewer, tenant)
	reader := h.mkMember(root, "reader@vault.io", "fixturepass1", auth.RoleViewer, tenant)
	outsider := h.mkMember(root, "outsider@vault.io", "fixturepass1", auth.RoleAdmin, tenant)
	owner := h.mkMember(root, "owner@vault.io", "fixturepass1", auth.RoleOwner, tenant)
	sharerID, readerID := h.principalOf(sharer).UserID, h.principalOf(reader).UserID
	ownerID := h.principalOf(owner).UserID

	var vault, plain model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		v, err := sc.Resources().Create(context.Background(), model.Resource{Name: "team", Kind: "folder", URI: auth.SharedVaultURIPrefix + "team"})
		if err != nil {
			return err
		}
		p, err := sc.Resources().Create(context.Background(), model.Resource{Name: "docs", Kind: "folder", URI: "workspace:docs"})
		vault, plain = v.ID, p.ID
		return err
	}); err != nil {
		t.Fatalf("seed folders: %v", err)
	}
	grants.set(sharerID, "resource:write", vault, true)
	grants.set(readerID, "resource:read", vault, true)
	for _, who := range []model.ID{sharerID, readerID} {
		grants.set(who, "resource:read", plain, true)
		grants.set(who, "resource:write", plain, true)
	}

	item := f.item(vault, "bank")
	if got := f.put(sharer, item, vaultValue); got.code != http.StatusOK {
		t.Fatalf("the member granted write puts into the vault = %d %s", got.code, got.raw)
	}
	if got := f.reveal(reader, item); got.code != http.StatusOK || got.body["value"] != vaultValue {
		t.Fatalf("the member granted read reveals the item = %d %s", got.code, got.raw)
	}
	if names := listed(t, f.list(reader)); len(names) != 1 || names[item] == "" {
		t.Fatalf("the member granted read lists %v, want the one item", names)
	}

	// A right is the right granted: read does not write, write does not reveal.
	if got := f.put(reader, f.item(vault, "other"), vaultValue); got.code != http.StatusForbidden {
		t.Errorf("a reader writing = %d %s, want 403", got.code, got.raw)
	}
	if got := f.reveal(sharer, item); got.code != http.StatusForbidden {
		t.Errorf("a writer who was not granted read revealing = %d %s, want 403", got.code, got.raw)
	}
	// Nobody else, whatever the organization role: the owner's implicit grant opens no vault,
	// only the grant the vault's owners gave does.
	if got := f.reveal(outsider, item); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Errorf("an admin with no grant revealing = %d %s, want 403 without the value", got.code, got.raw)
	}
	if got := f.reveal(owner, item); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Errorf("the organization owner with no grant revealing = %d %s, want 403 without the value", got.code, got.raw)
	}
	grants.set(ownerID, "resource:read", vault, true)
	if got := f.reveal(owner, item); got.code != http.StatusOK || got.body["value"] != vaultValue {
		t.Errorf("the organization owner granted read revealing = %d %s, want 200", got.code, got.raw)
	}
	grants.set(ownerID, "resource:read", vault, false)
	// A permit anchored above the vault is not a grant at the vault: an administrator who writes
	// one for themselves (a workspace or tenant viewer grant) opens no vault by it, and neither
	// does a grant written for another purpose.
	grants.anchor(h.principalOf(outsider).UserID, "resource:read", vault, aboveTheVault)
	if got := f.reveal(outsider, item); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Errorf("an admin whose only permit is anchored above the vault revealing = %d %s, want 403 without the value", got.code, got.raw)
	}
	if names := listed(t, f.list(outsider)); len(names) != 0 {
		t.Errorf("an admin whose only permit is anchored above the vault lists %v, want nothing", names)
	}
	grants.anchor(h.principalOf(outsider).UserID, "resource:read", vault, "")
	if names := listed(t, f.list(outsider)); len(names) != 0 {
		t.Errorf("an admin with no grant lists %v, want nothing", names)
	}
	// A folder that is not a vault never serves items, whatever it grants.
	if got := f.put(sharer, f.item(plain, "bank"), vaultValue); got.code != http.StatusForbidden {
		t.Errorf("a put into a folder that is not a vault = %d %s, want 403", got.code, got.raw)
	}

	// An item is stored under the name as typed, so only the vault id's canonical spelling names a
	// shared item: another spelling would hide the item from the list and escape its cap.
	if got := f.put(sharer, "vault/"+strings.ToUpper(vault.String())+"/bank", vaultValue); got.code != http.StatusForbidden {
		t.Errorf("a put under another spelling of the vault id = %d %s, want 403", got.code, got.raw)
	}

	// The tenant's own ledger names the person and the vault of each reveal that succeeded, and
	// of nothing that was refused.
	if opened := vaultReveals(t, h, tenant); len(opened) != 2 || opened[0].TargetID != vault || !strings.Contains(opened[0].Actor, readerID.String()) || opened[0].TargetKind != "resource" {
		t.Errorf("the tenant ledger's vault.reveal entries = %+v, want the reader's and the granted owner's at the vault", opened)
	}

	// A deny overlay that refuses reads of the vault refuses the granted reader too: a grant
	// is one term of the decision, not the whole of it.
	overlay.deny(vault)
	if got := f.reveal(reader, item); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Errorf("a granted reader revealing under a deny overlay = %d %s, want 403 without the value", got.code, got.raw)
	}
	if names := listed(t, f.list(reader)); len(names) != 0 {
		t.Errorf("a granted reader lists %v under a deny overlay, want nothing", names)
	}
	overlay.lift()

	// Revoking takes effect on the next request.
	grants.set(readerID, "resource:read", vault, false)
	if got := f.reveal(reader, item); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Errorf("a revoked reader revealing = %d %s, want 403 without the value", got.code, got.raw)
	}
	if names := listed(t, f.list(reader)); len(names) != 0 {
		t.Errorf("a revoked reader lists %v, want nothing", names)
	}
}

// vaultReveals returns the vault.reveal entries of the tenant's own audit chain.
func vaultReveals(t *testing.T, h *harness, tenant model.TenantID) []model.AuditEvent {
	t.Helper()
	var out []model.AuditEvent
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "vault.reveal" {
				out = append(out, e)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the tenant ledger: %v", err)
	}
	return out
}
