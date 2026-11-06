// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// OwnedEntity is a current responsibility, with the version reviewed by a caller.
// Terminal work stays historical and is reported without being reassigned.
type OwnedEntity struct {
	ID           model.ID `json:"id"`
	Name         string   `json:"name"`
	Version      int64    `json:"version"`
	WorkspaceID  model.ID `json:"workspace_id,omitempty"`
	Transferable bool     `json:"transferable"`
}

// OwnershipInventory contains no folder content or credentials. Its digest binds
// the complete inventory and its versions to one tenant and source account.
type OwnershipInventory struct {
	Subject   model.ID      `json:"subject"`
	Folders   []OwnedEntity `json:"folders"`
	WorkItems []OwnedEntity `json:"work_items"`
	Templates []OwnedEntity `json:"templates"`
	Digest    string        `json:"digest"`
}

const ownershipInventoryLimit = 1000

func templateOwner(row model.Record) string {
	if owner := row.String(colTplOwner); owner != "" {
		return owner
	}
	// Templates predating current ownership retain their author's responsibility.
	// Only the canonical human actor form identifies an account; token and system
	// authors are historical evidence and are never guessed into a human owner.
	author := row.String(colTplAuthor)
	if raw, ok := strings.CutPrefix(author, "user:"); ok {
		if id, err := model.ParseID(raw); err == nil && !id.IsZero() {
			return "user:" + id.String()
		}
	}
	return ""
}

// OwnershipInventory reads the account's session responsibilities. The trusted
// in-process caller must authorize access before requesting the report.
func (m *Module) OwnershipInventory(ctx context.Context, tenant model.TenantID, subject model.ID) (OwnershipInventory, error) {
	var out OwnershipInventory
	if m == nil || m.Data == nil || tenant.IsZero() || tenant.IsSystem() || subject.IsZero() {
		return out, store.ErrInvalidID
	}
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		out, e = ownershipInventoryInScope(ctx, sc, subject)
		return e
	})
	return out, err
}

func ownershipRows(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) ([]model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	rows, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: ownershipInventoryLimit + 1})
	if err != nil {
		return nil, err
	}
	if page.HasMore || len(rows) > ownershipInventoryLimit {
		return nil, fmt.Errorf("sessions: ownership inventory exceeds %d rows", ownershipInventoryLimit)
	}
	return rows, nil
}

func ownershipInventoryInScope(ctx context.Context, sc store.Scope, subject model.ID) (OwnershipInventory, error) {
	out := OwnershipInventory{Subject: subject, Folders: []OwnedEntity{}, WorkItems: []OwnedEntity{}, Templates: []OwnedEntity{}}
	owner := "user:" + subject.String()
	folders, page, err := sc.Resources().List(ctx, model.Query{Filters: []model.Filter{eq("owner", owner), eq("kind", "folder")}, Limit: ownershipInventoryLimit + 1})
	if err != nil {
		return out, err
	}
	if page.HasMore || len(folders) > ownershipInventoryLimit {
		return out, fmt.Errorf("sessions: folder ownership inventory is too large")
	}
	for _, row := range folders {
		out.Folders = append(out.Folders, OwnedEntity{row.ID, row.Name, row.Version, row.WorkspaceID, true})
	}
	items, err := ownershipRows(ctx, sc, workItemKind, eq(colWorkOwnerKind, "user"), eq(colWorkOwnerRef, subject.String()))
	if err != nil {
		return out, err
	}
	for _, row := range items {
		out.WorkItems = append(out.WorkItems, OwnedEntity{recordID(row), row.String(colWorkTitle), row.Int(model.ColVersion), model.ID(row.String(colWorkWorkspaceID)), !terminalWorkStatuses[row.String(colWorkStatus)] && row.String(colWorkArchivedAt) == ""})
	}
	templates, err := ownershipRows(ctx, sc, templateKind, eq(colTplOwner, owner))
	if err != nil {
		return out, err
	}
	legacy, err := ownershipRows(ctx, sc, templateKind, eq(colTplAuthor, owner), model.Filter{Column: colTplOwner, Op: model.OpIsNull})
	if err != nil {
		return out, err
	}
	templates = append(templates, legacy...)
	if len(templates) > ownershipInventoryLimit {
		return out, fmt.Errorf("sessions: template ownership inventory is too large")
	}
	for _, row := range templates {
		if !row.Bool(colTplBuiltin) {
			out.Templates = append(out.Templates, OwnedEntity{recordID(row), row.String(colTplName), row.Int(model.ColVersion), "", true})
		}
	}
	for _, rows := range [][]OwnedEntity{out.Folders, out.WorkItems, out.Templates} {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	}
	raw, err := json.Marshal(struct {
		Tenant    model.TenantID
		Inventory OwnershipInventory
	}{sc.Tenant(), out})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

// PreparedOwnershipTransfer is bound to its module, tenant, exact reviewed
// inventory and recipient authority. Its evidence is read before the caller
// opens a tenant transaction, avoiding nested auth reads on SQLite.
type PreparedOwnershipTransfer struct {
	module    *Module
	tenant    model.TenantID
	inventory OwnershipInventory
	target    model.ID
	principal WorkPrincipal
	refs      []store.UserAuthorityFactRef
	evidence  *preparedEvidence
	commands  []WorkCommand
	extra     store.AuthoritySnapshotBundle
}

// PrepareOwnershipTransfer validates the reviewed inventory and prepares the
// same participant evidence the work assignment kernel requires. Authorization
// and recipient membership remain the owning workflow's responsibility.
func (m *Module) PrepareOwnershipTransfer(ctx context.Context, tenant model.TenantID, principal WorkPrincipal, from, to model.ID, digest string, authority ...store.AuthoritySnapshotBundle) (*PreparedOwnershipTransfer, error) {
	if !principal.Admin || from.IsZero() || to.IsZero() || from == to {
		return nil, store.ErrInvalidID
	}
	inv, err := m.OwnershipInventory(ctx, tenant, from)
	if err != nil {
		return nil, err
	}
	if digest == "" || inv.Digest != digest {
		return nil, store.ErrConflict
	}
	reader := m.standingFor(ctx)
	if reader == nil {
		return nil, store.ErrStoreUnavailable
	}
	standing, err := reader.Standing(ctx, tenant, []model.ID{from})
	if err != nil {
		return nil, err
	}
	source, found := standing[from]
	if !found || !source.Exists {
		return nil, store.ErrNotFound
	}
	if err := source.Refusal(); err != nil && err != auth.ErrSubjectRetirementActive {
		return nil, err
	}
	refs, err := auth.FenceSubjects(ctx, m.standingFor(ctx), tenant, []model.ID{to})
	if err != nil {
		return nil, err
	}
	if len(refs) != 1 {
		return nil, store.ErrNotFound
	}
	refs = append(refs, store.UserAuthorityFactRef{UserID: from, Version: source.AuthorityVersion})
	sort.Slice(refs, func(i, j int) bool { return refs[i].UserID < refs[j].UserID })
	plan := ProtocolReplayPlan{Accounts: []model.ID{to}}
	p := &PreparedOwnershipTransfer{module: m, tenant: tenant, inventory: inv, target: to, principal: principal, refs: refs, evidence: newPreparedEvidence()}
	for _, bundle := range authority {
		p.extra.Facts = append(p.extra.Facts, bundle.Facts...)
		p.extra.UserAuthorities = append(p.extra.UserAuthorities, bundle.UserAuthorities...)
	}

	for _, item := range inv.WorkItems {
		if item.Transferable {
			plan.Participants = append(plan.Participants, ProtocolReplayParticipant{WorkspaceID: item.WorkspaceID, Kind: "user", Ref: to.String()})
			p.commands = append(p.commands, WorkCommand{Command: "item.assign", WorkItemID: item.ID, WorkspaceID: item.WorkspaceID, ExpectedVersion: item.Version, OwnerKind: "user", OwnerRef: to.String(), IdempotencyKey: model.NewID().String(), HTTPMethod: "POST", CommandScope: "ownership.transfer"})
		}
	}
	if err := m.prepareProtocolReplayEvidence(ctx, tenant, "", plan, p.evidence); err != nil {
		return nil, err
	}
	return p, nil
}

// ApplyOwnershipTransfer joins the caller's tenant mutation. It pins recipient
// authority before any row write and rejects any change since review. The caller
// must roll back the transaction on error, including its own completion audit.
// No credential, session, file, template authorship or historical work is moved.
func (m *Module) ApplyOwnershipTransfer(ctx context.Context, tenant model.TenantID, sc store.Scope, p *PreparedOwnershipTransfer) error {
	if p == nil || p.module != m || p.tenant != tenant || sc == nil || sc.Tenant() != tenant {
		return store.ErrInvalidID
	}
	if len(p.extra.Facts) == 0 && len(p.extra.UserAuthorities) == 0 {
		if _, err := auth.PinFence(ctx, sc, auth.FenceDirectory, p.refs); err != nil {
			return err
		}
	} else {
		// Merge the caller's captured admission facts before taking the one native
		// directory barrier. A second lock call would violate the global lock order.
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return store.ErrDirectoryUnavailable
		}
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return store.ErrDirectoryUnavailable
		}
		base := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{{Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version}}, UserAuthorities: p.refs}
		extra := p.extra
		if len(extra.Facts) == 0 {
			extra.Facts = append([]store.AuthorizationFactRef(nil), base.Facts...)
		}
		bundle, err := auth.MergeAuthoritySnapshotBundles(base, extra)
		if err != nil {
			return fmt.Errorf("%w: %w", store.ErrConflict, err)
		}
		if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
	}
	current, err := ownershipInventoryInScope(ctx, sc, p.inventory.Subject)
	if err != nil {
		return err
	}
	if current.Digest != p.inventory.Digest {
		return store.ErrConflict
	}
	joined, joinedCtx := newProtocolReplayTransactionContext(ctx, tenant, sc)
	defer joined.active.Store(false)
	joined.prepared = &preparedReplay{accounts: map[model.ID]bool{p.target: true}, evidence: p.evidence}
	for _, cmd := range p.commands {
		if _, err := m.Apply(joinedCtx, tenant, p.principal, cmd); err != nil {
			return err
		}
	}
	for _, folder := range p.inventory.Folders {
		row, err := sc.Resources().Get(ctx, folder.ID)
		if err != nil {
			return err
		}
		row.Owner = "user:" + p.target.String()
		if _, err = sc.Resources().Update(ctx, row); err != nil {
			return err
		}
		// A transferred automatic folder leaves its original person's path occupied.
		// Retain a durable marker so their next My files can allocate a sibling,
		// while an unrelated pre-registered path continues to be refused.
		workspaces, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		registered, err := workspaces.Get(ctx, folder.ID)
		if err == nil {
			registered[colWsTransferred] = true
			if _, err = workspaces.Update(ctx, registered); err != nil {
				return err
			}
		} else if err != store.ErrNotFound {
			return err
		}
	}
	repo, err := sc.Ext(templateKind)
	if err != nil {
		return err
	}
	for _, template := range p.inventory.Templates {
		row, err := repo.Get(ctx, template.ID)
		if err != nil {
			return err
		}
		row[colTplOwner] = "user:" + p.target.String()
		if _, err = repo.Update(ctx, row); err != nil {
			return err
		}
	}
	return nil
}
