// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package sessions

import (
	"context"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
)

type beforeRunWorkScopeRegistry struct{ store.ExtensionRegistry }

func (r beforeRunWorkScopeRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != colRunWorkScope {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}
func TestRunWorkScopeNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := context.Background()
			old := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error { return old.RegisterSchema(beforeRunWorkScopeRegistry{reg}) })
			tenant := ensureTenant(t, st, "run-work-scope-upgrade")
			ref := model.NewID().String()
			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{colRunRef: ref, colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			got, err := m.getRun(ctx, tenant, ref)
			if err != nil || got.WorkScope != nil || got.ProcessState != stateStopped {
				t.Fatal("upgrade changed or backfilled historical launch scope")
			}
			var workspace model.ID
			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				ws, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				workspace = ws.ID
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				record, err := findRunRec(ctx, repo, ref)
				if err != nil {
					return err
				}
				if !record.IsNull(colRunWorkScope) {
					t.Fatal("new nullable column was backfilled")
				}
				record[colRunAuthzWorkspaceID] = workspace.String()
				if err := setRunWorkScope(record, runtimeCredentials{work: WorkSessionCredential{ID: model.NewID()}}); err != nil {
					return err
				}
				_, err = repo.Update(ctx, record)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			got, err = m.getRun(ctx, tenant, ref)
			if err != nil || got.WorkScope == nil || got.WorkScope.Role != "worker" || got.WorkScope.WorkspaceID != workspace {
				t.Fatal("new launch scope did not survive reopen")
			}
		})
	}
}
