// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// WikiSchema returns the same verified schema delivered to the native tool.
func WikiSchema(ctx context.Context) ([]byte, error) {
	pack, _, err := importBuiltin(ctx, "olivares-wiki")
	if err != nil {
		return nil, err
	}
	body, ok := pack.File("olivares-wiki/schema.md")
	if !ok {
		return nil, refuse("source_unavailable", "wiki schema")
	}
	return body, nil
}

// EnsureWikiFolder uses the common publisher and pin transaction. The composition
// root supplies catalog-authorized data; native folder and assignment authority
// are prepared and fenced here just as on the public assignment route.
func (m *Module) EnsureWikiFolder(ctx context.Context, mc api.ModuleContext, target Target) (Assignment, error) {
	if target.Kind != "folder" || !validTarget(target) {
		return Assignment{}, errInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	if err != nil {
		return Assignment{}, err
	}
	request, release, err := m.prepareTarget(request, mc, target, true)
	defer release()
	if err != nil {
		return Assignment{}, err
	}
	ctx = request.Context()
	pack, source, err := importBuiltin(ctx, "olivares-wiki")
	if err != nil {
		return Assignment{}, err
	}
	plan := importPlan{name: "olivares-wiki", fingerprint: pack.ManifestDigest, read: func() (*ValidatedPack, Source, error) { return pack, source, nil }}
	// Tenant-wide, actor-independent receipt prevents simultaneous initializers
	// from publishing duplicate copies of the same bundled revision.
	var existing Revision
	err = mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 2, Filters: []model.Filter{{Column: "name", Op: model.OpEq, Value: "olivares-wiki"}}})
		if err != nil {
			return err
		}
		if len(rows) > 1 {
			return store.ErrConflict
		}
		if len(rows) == 0 {
			return nil
		}
		existing, err = pinnedRevision(ctx, sc, rows[0].String("latest_revision_id"))
		if err != nil {
			return err
		}
		if existing.Source.Kind != "builtin" || existing.Source.RequestedRef != "olivares-wiki" || existing.ManifestDigest != pack.ManifestDigest {
			return store.ErrConflict
		}
		return nil
	})
	if err != nil {
		return Assignment{}, err
	}
	if existing.ID != "" {
		return m.pinAssignment(ctx, mc, AssignmentRequest{Target: target, RevisionID: existing.ID, Members: []string{"olivares-wiki"}}, model.ID(""), 0, true)
	}
	published, _, err := m.publishImportWithActor(ctx, mc, "", "wiki:"+pack.ManifestDigest, plan, "builtin:olivares-wiki")
	if err != nil {
		return Assignment{}, err
	}
	return m.pinAssignment(ctx, mc, AssignmentRequest{Target: target, RevisionID: published.Revision.ID, Members: []string{"olivares-wiki"}}, model.ID(""), 0, true)
}
