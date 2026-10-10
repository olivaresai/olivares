// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func invokeCedarDisable(t *testing.T, f *managedEpochFixture, data api.ScopedData) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handlePdpDisable(rec, httptest.NewRequest(http.MethodDelete, "/pdp/active?engine=cedar", nil), f.moduleContext(data))
	return rec
}

func TestCedarDisableFailureRollsBackAllAuthority(t *testing.T) {
	injected := errors.New("injected disable failure")
	for _, stage := range []string{"audit", "freshness", "epoch CAS"} {
		t.Run(stage, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			f.seedSelectedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
			before := f.cedarAuthoritySnapshot(t)
			data := &managedEpochScopedData{st: f.st, tenant: f.tenant, wrap: func(sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				switch stage {
				case "audit":
					out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), err: injected}
				case "freshness":
					repo, err := sc.Ext(policyFreshnessKind)
					if err != nil {
						t.Fatal(err)
					}
					out.ext = map[model.Kind]store.GenericRepo{policyFreshnessKind: managedEpochFailingRepo{GenericRepo: repo, err: injected}}
				case "epoch CAS":
					fact, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					out.epochs = &managedEpochScriptedStore{fact: fact, bumpErr: store.ErrConflict}
				}
				return out
			}}
			rec := invokeCedarDisable(t, f, data)
			if rec.Code < 400 {
				t.Fatalf("disable with %s failure=%d %s", stage, rec.Code, rec.Body.String())
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
		})
	}
}

func TestCedarDisableReportsDeferredWithoutReload(t *testing.T) {
	f := newManagedEpochFixture(t)
	f.seedSelectedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
	if err := f.m.ReloadActivePDP(t.Context(), f.tenant); err != nil {
		t.Fatal(err)
	}
	f.m.reloadGrantsFn = func(context.Context, model.TenantID) error { return errors.New("injected reload failure") }
	rec := invokeCedarDisable(t, f, &managedEpochScopedData{st: f.st, tenant: f.tenant})
	var result pdpPublishResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || result.LiveActivation != liveDeferred {
		t.Fatalf("reload failure=%d %+v", rec.Code, result)
	}
}

type wrappedOPAMutationData struct{ api.ScopedData }

func (d wrappedOPAMutationData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	if err := d.ScopedData.Mutate(ctx, fn); err != nil {
		return fmt.Errorf("store mutation: %w", err)
	}
	return nil
}
func TestOPARollbackWrappedMissingRevisionKeeps404(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := wrappedOPAMutationData{&managedEpochScopedData{st: f.st, tenant: f.tenant}}
	rec := httptest.NewRecorder()
	f.m.handlePdpRollback(rec, managedEpochRequest(t, http.MethodPost, "/pdp/rollback", map[string]any{"engine": "opa", "revision": 1}, "", ""), f.moduleContext(data))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrapped missing OPA revision=%d %s, want404", rec.Code, rec.Body.String())
	}
}
