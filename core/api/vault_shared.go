// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A shared vault is a folder Resource whose URI starts with auth.SharedVaultURIPrefix, and
// its items are the sealed entries vault/<resource-id>/<item>: one ciphertext each, never a
// copy per person. A signed-in person holding a positive scoped grant at the folder uses
// them: read to list, reveal and read the authenticator code, write to add, replace and
// remove. No organization role opens one, the owner's included, and neither does a grant
// anchored above the vault: a vault is private to the people and groups holding a grant at
// the vault itself, so a workspace or tenant grant written for another purpose, or by an
// administrator for themselves, reaches none. Every edition enforces the grants it holds, so a
// team keeps its passwords when a licence lapses; making a vault and sharing it is Business
// (modules/governance, vault routes).

// sharedVaultNode names the folder that guards name when name is an item of a shared vault.
// The second result is false for every other name, including a personal item and a folder
// that is not a vault.
func (s *Server) sharedVaultNode(ctx context.Context, tenant model.TenantID, name string) (auth.ResourceAttrs, bool, error) {
	rest, ok := strings.CutPrefix(name, auth.PersonalVaultPrefix)
	if !ok || name != strings.TrimSpace(name) {
		return auth.ResourceAttrs{}, false, nil
	}
	owner, item, ok := strings.Cut(rest, "/")
	id, err := model.ParseID(owner)
	// An item is stored under the name as typed: only the canonical spelling of the vault id
	// names a shared item, or its row would escape the vault's list and its item cap.
	if !ok || item == "" || err != nil || id.String() != owner {
		return auth.ResourceAttrs{}, false, nil
	}
	var folder model.Resource
	err = s.st.View(ctx, tenant, func(sc store.Scope) (e error) {
		folder, e = sc.Resources().Get(ctx, id)
		return e
	})
	if errors.Is(err, store.ErrNotFound) {
		return auth.ResourceAttrs{}, false, nil
	}
	if err != nil {
		return auth.ResourceAttrs{}, false, err
	}
	if folder.Kind != "folder" || !strings.HasPrefix(folder.URI, auth.SharedVaultURIPrefix) {
		return auth.ResourceAttrs{}, false, nil
	}
	return auth.ResourceAttrs{Kind: "resource", ID: id.String()}, true, nil
}

// mayUseVaultItem decides whether p holds right on the vault item name in a tenant scope:
// the owner of a personal item, or a person granted the right at the shared vault's folder.
// The shared case is one Authorize question with the role term removed (RequireScopedGrant),
// so the deny overlay and the decision evidence apply as they do to every other folder right.
// The folder comes back for the caller's ledger entry, so one request looks it up once; it
// is empty for a personal item. A store failure is an error; a scoped engine that cannot
// decide refuses, as every other governed route does.
func (s *Server) mayUseVaultItem(ctx context.Context, p auth.Principal, tenant model.TenantID, name string, right auth.TrusteeRight) (auth.ResourceAttrs, bool, error) {
	none := auth.ResourceAttrs{}
	if ownsVaultItem(p, name) {
		return none, true, nil
	}
	if !auth.IsVaultOwner(p) {
		return none, false, nil
	}
	node, shared, err := s.sharedVaultNode(ctx, tenant, name)
	if err != nil || !shared {
		return none, false, err
	}
	question, ok := right.Question(p, tenant, node)
	if !ok {
		return none, false, nil
	}
	question.Route = auth.RouteMetadata{RequireScopedGrant: true}
	if _, err := s.admit(ctx, question, errForbidden, ungovernedRoute); err != nil {
		if status, _ := statusFor(err); status == http.StatusForbidden {
			return none, false, nil
		}
		return none, false, err
	}
	// The same question, asked for its provenance: the vault opens only through a grant
	// anchored at the vault itself. The owner's implicit grant names none, and a permit
	// anchored at a workspace or the tenant names that node, so neither opens it.
	decision, why := s.authz.AuthorizeExplanation(ctx, question)
	if !decision.Allow {
		return none, false, nil
	}
	for _, grant := range why.Because {
		if grant.Node.Ref == node.ID {
			return node, true, nil
		}
	}
	return none, false, nil
}

// recordSharedVaultUse writes on the tenant's own ledger that p opened an item of the shared
// vault at folder: the actor and the vault, never the item's name or value. The entry per
// item (secret.reveal) stays on the server's ledger; this one is what the organization's
// audit export shows. A personal item has no folder and writes nothing. A failure is the
// caller's to refuse with: a value is not shown unless its use was recorded.
func (s *Server) recordSharedVaultUse(ctx context.Context, p auth.Principal, tenant model.TenantID, folder auth.ResourceAttrs, action string) error {
	if folder.ID == "" {
		return nil
	}
	return s.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		// A full spool in degrade mode drops the entry and reports no error: the value is not
		// shown then either, as in every other place that needs the entry to exist.
		event, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: p.Actor(), ActorKind: p.ActorKind(),
			Action: action, TargetKind: "resource", TargetID: model.ID(folder.ID),
		})
		if err == nil && event.Seq == 0 {
			return store.ErrAuditSpoolFull
		}
		return err
	})
}

// vaultLister decides which vault items one list shows. The shared vault folders are read
// once per list, so another member's personal items cost no lookup, and a vault's grant is
// asked once however many items it holds.
type vaultLister struct {
	s       *Server
	p       auth.Principal
	tenant  model.TenantID
	folders map[string]bool
	opened  map[string]bool
}

func (s *Server) newVaultLister(p auth.Principal, tenant model.TenantID) *vaultLister {
	return &vaultLister{s: s, p: p, tenant: tenant, opened: map[string]bool{}}
}

func (l *vaultLister) sharedFolders(ctx context.Context) error {
	if l.folders != nil {
		return nil
	}
	folders := map[string]bool{}
	err := l.s.st.View(ctx, l.tenant, func(sc store.Scope) error {
		return store.WalkPages(ctx, sc.Resources().List, model.Query{Filters: []model.Filter{
			{Column: "kind", Op: model.OpEq, Value: "folder"},
			{Column: "uri", Op: model.OpLike, Value: auth.SharedVaultURIPrefix + "%"},
		}, Limit: 200}, store.UnboundedRows, func(rows []model.Resource) error {
			for _, r := range rows {
				folders[r.ID.String()] = true
			}
			return nil
		})
	})
	if err == nil {
		l.folders = folders
	}
	return err
}

// visible reports whether the list shows the vault item name to the caller.
func (l *vaultLister) visible(ctx context.Context, name string) (bool, error) {
	if ownsVaultItem(l.p, name) {
		return true, nil
	}
	if err := l.sharedFolders(ctx); err != nil {
		return false, err
	}
	vault, _, _ := strings.Cut(strings.TrimPrefix(name, auth.PersonalVaultPrefix), "/")
	if !l.folders[vault] {
		return false, nil
	}
	if seen, ok := l.opened[vault]; ok {
		return seen, nil
	}
	_, open, err := l.s.mayUseVaultItem(ctx, l.p, l.tenant, name, auth.TrusteeRead)
	if err == nil {
		l.opened[vault] = open
	}
	return open, err
}
