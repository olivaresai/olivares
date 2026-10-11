// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestReadSkillsTargetReadsTemplateAndSessionRows(t *testing.T) {
	ctx := context.Background()
	f := newWorkFixture(t, ":memory:", nil)
	t.Cleanup(func() { _ = f.st.Close() })
	var template, archived model.Record
	var otherWorkspace model.Workspace
	runIDs := map[string]model.ID{}
	const ownRun, otherRun = "skills-own-run", "skills-other-run"
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		templates, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		row := func(name string) model.Record {
			return model.Record{colTplName: name, colTplDescription: "", colTplAuthor: "test", colTplBuiltin: false, colTplBody: "{}"}
		}
		if template, err = templates.Create(ctx, row("live")); err != nil {
			return err
		}
		gone := row("gone")
		gone[colTplArchivedAt] = time.Now().UTC().Format(time.RFC3339)
		if archived, err = templates.Create(ctx, gone); err != nil {
			return err
		}
		if otherWorkspace, err = sc.Workspaces().Create(ctx, model.Workspace{Name: "Other", Slug: model.NewID().String(), Status: model.StatusActive}); err != nil {
			return err
		}
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for ref, workspace := range map[string]model.ID{ownRun: f.workspace, otherRun: otherWorkspace.ID} {
			row, err := runs.Create(ctx, model.Record{
				colRunRef: ref, colTransport: string(TransportStreamJSON), colPermissionMode: "",
				colIsolation: string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0),
				colRunAuthzWorkspaceID: workspace.String(),
			})
			if err != nil {
				return err
			}
			runIDs[ref] = model.ID(row.String(model.ColID))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func(sc store.Scope, kind, id string, write bool) (SkillsTarget, error) {
		return f.m.ReadSkillsTarget(ctx, sc, kind, id, write)
	}

	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		got, err := read(sc, "template", template.String(model.ColID), false)
		if err != nil || got.ID.String() != template.String(model.ColID) || got.Permission != permTemplateRead || got.Version != template.Int(model.ColVersion) || !got.WorkspaceID.IsZero() {
			t.Errorf("read a template: %+v, %v", got, err)
		}
		written, err := read(sc, "template", template.String(model.ColID), true)
		if err != nil || written.Permission != permTemplateWrite || written.Version <= got.Version {
			t.Errorf("write fences the template version: %+v, %v", written, err)
		}
		if _, err := read(sc, "template", archived.String(model.ColID), true); !errors.Is(err, store.ErrConflict) {
			t.Errorf("write an archived template: %v", err)
		}
		// A session is named by its run reference and identified by its row ID.
		if named, err := read(sc, "session", ownRun, false); err != nil || named.ID != runIDs[ownRun] || named.ID.String() == ownRun {
			t.Errorf("read a session: %+v, %v, want row ID %s", named, err, runIDs[ownRun])
		}
		own, err := read(sc, "session", ownRun, true)
		if err != nil || own.ID != runIDs[ownRun] || own.Permission != permRunWrite || own.WorkspaceID != f.workspace || own.Version < 2 {
			t.Errorf("write a session: %+v, %v", own, err)
		}
		if _, err := read(sc, "session", "skills-absent-run", false); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("absent session: %v", err)
		}
		if _, err := read(sc, "workspace", f.workspace.String(), false); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a kind sessions does not own: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A principal confined to one workspace reaches that workspace's sessions
	// only; a template has no workspace, so it is refused as not found.
	if err := f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, f.workspace)
		if err != nil {
			return err
		}
		if got, err := read(sc, "session", ownRun, false); err != nil || got.WorkspaceID != f.workspace {
			t.Errorf("confined read of its own session: %+v, %v", got, err)
		}
		if _, err := read(sc, "session", otherRun, false); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("confined read of another workspace's session: %v", err)
		}
		if _, err := read(sc, "template", template.String(model.ColID), false); !errors.Is(err, auth.ErrRouteDenied) {
			t.Errorf("confined read of a template: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
