// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

type runLeaseReadDeniedPolicy struct{}

func (runLeaseReadDeniedPolicy) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: req.Permission != "sessions:lease:read"}, nil
}

// The real schema refuses a materialized lease without expiry. Model an
// incomplete store read through the public data port instead of bypassing that
// constraint. All other records and the transaction clock remain real.
type missingLeaseExpiryData struct {
	api.ModuleData
	omit *atomic.Bool
}

func (d missingLeaseExpiryData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !d.omit.Load() {
		return d.ModuleData.View(ctx, tenant, fn)
	}
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(missingLeaseExpiryScope{Scope: sc, TransactionClock: sc.(store.TransactionClock)})
	})
}

type missingLeaseExpiryScope struct {
	store.Scope
	store.TransactionClock
}

func (sc missingLeaseExpiryScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := sc.Scope.Ext(kind)
	if err == nil && kind == workLeaseKind {
		return missingLeaseExpiryRepo{repo}, nil
	}
	return repo, err
}

type missingLeaseExpiryRepo struct{ store.GenericRepo }

func (repo missingLeaseExpiryRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := repo.GenericRepo.List(ctx, q)
	for i, row := range rows {
		copy := make(model.Record, len(row))
		for key, value := range row {
			copy[key] = value
		}
		copy[colLeaseExpiresAt] = nil
		rows[i] = copy
	}
	return rows, page, err
}

func TestRuntimeRunProjectsMissingActiveLeaseExpiryAsUnknown(t *testing.T) {
	omit := new(atomic.Bool)
	f := newRuntimeWorkAPIFixtureWithData(t, func(data api.ModuleData) api.ModuleData {
		return missingLeaseExpiryData{ModuleData: data, omit: omit}
	})
	proc := f.runner.lastProc()
	t.Cleanup(func() { finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc) })
	omit.Store(true)
	defer omit.Store(false)
	run := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.runRef, f.admin, tenantHdr(f.tenant))
	if run.code != http.StatusOK || run.body["work_lease_state"] != "unknown" {
		t.Errorf("incomplete active lease run read = %d %s; want unknown", run.code, run.raw)
	}
}

func TestRuntimeRunProjectsLeaseStateWithoutLeaseReadPermission(t *testing.T) {
	f := newRuntimeWorkAPIFixture(t)
	proc := f.runner.lastProc()
	t.Cleanup(func() {
		if _, live := f.m.rt.getLive(f.tenant, f.runRef); live {
			finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
		}
	})
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := auth.NewAuthorizer(runLeaseReadDeniedPolicy{})
	WithWorkAuthorizer(authorizer)(f.m)
	f.h.srv, err = api.New(api.Options{
		Store: f.h.st, Authenticator: auth.NewAuthenticator(f.h.st, nil), Authorizer: authorizer,
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Version: "test", Modules: []api.Module{f.m},
	})
	if err != nil {
		t.Fatal(err)
	}
	runPath := "/v1/m/sessions/runs/" + f.runRef
	leasePath := "/v1/m/sessions/work-items/" + f.itemID.String() + "/lease"
	assertState := func(want string) {
		t.Helper()
		lease := f.h.do(http.MethodGet, leasePath, f.admin, tenantHdr(f.tenant))
		if lease.code != http.StatusForbidden {
			t.Fatalf("lease read = %d; want 403", lease.code)
		}
		run := f.h.do(http.MethodGet, runPath, f.admin, tenantHdr(f.tenant))
		if run.code != http.StatusOK || run.body["work_lease_state"] != want {
			t.Errorf("authorized run read = %d %s; want work_lease_state=%s", run.code, run.raw, want)
		}
		if run.body["work_lease_fence"] != float64(f.fence) || run.body["work_item_id"] != f.itemID.String() {
			t.Error("lease state projection erased the historical work binding")
		}
		listed := f.h.do(http.MethodGet, "/v1/m/sessions/runs", f.admin, tenantHdr(f.tenant))
		items, _ := listed.body["items"].([]any)
		if listed.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["work_lease_state"] != want {
			t.Errorf("run list = %d %s; want the same lease state", listed.code, listed.raw)
		}
		for _, key := range []string{"holder_sid", "expires_at", "liveness_code"} {
			if _, exists := run.body[key]; exists {
				t.Errorf("run disclosed private lease field %s", key)
			}
		}
	}
	assertState("active")
	active := f.h.doJSON(http.MethodPost, runPath+"/input", f.admin, map[string]any{"line": "unfenced active input"}, tenantHdr(f.tenant))
	if active.code != http.StatusConflict || proc.sentCount() != 0 {
		t.Fatalf("active lease lost its fence: %d, writes=%d", active.code, proc.sentCount())
	}
	submitRuntimeWork(t, f)
	assertState("ended")
	input := f.h.doJSON(http.MethodPost, runPath+"/input", f.admin, map[string]any{"line": "ordinary input after submit"}, tenantHdr(f.tenant))
	if input.code != http.StatusAccepted || proc.sentCount() != 1 {
		t.Fatalf("input without lease-read = %d, writes=%d; want 202 and one write", input.code, proc.sentCount())
	}
	stopped := f.h.do(http.MethodPost, runPath+"/stop", f.admin, tenantHdr(f.tenant))
	if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
		t.Fatalf("Stop without lease-read = %d %s; want 200 stopped", stopped.code, stopped.raw)
	}
}

// Write permission does not grant the read-time lease projection, even when the
// same caller may also read runs. Control replies retain their unknown posture.
type runReadDeniedPolicy struct{}

func (runReadDeniedPolicy) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: req.Permission != permRunRead}, nil
}

func TestRuntimeWorkControlLeaseStateRemainsUnknown(t *testing.T) {
	for _, canRead := range []bool{false, true} {
		for _, action := range []string{"interrupt", "stop"} {
			name := "write-only/" + action
			if canRead {
				name = "read-and-write/" + action
			}
			t.Run(name, func(t *testing.T) {
				f := newRuntimeWorkAPIFixture(t)
				proc := f.runner.lastProc()
				t.Cleanup(func() {
					if _, live := f.m.rt.getLive(f.tenant, f.runRef); live {
						finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
					}
				})
				_, private, err := ed25519.GenerateKey(nil)
				if err != nil {
					t.Fatal(err)
				}
				signer, err := audit.NewSigner(private)
				if err != nil {
					t.Fatal(err)
				}
				var policy auth.PolicyEvaluator = runReadDeniedPolicy{}
				if canRead {
					policy = runLeaseReadDeniedPolicy{}
				}
				authorizer := auth.NewAuthorizer(policy)
				WithWorkAuthorizer(authorizer)(f.m)
				f.h.srv, err = api.New(api.Options{
					Store: f.h.st, Authenticator: auth.NewAuthenticator(f.h.st, nil), Authorizer: authorizer,
					Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
					Version: "test", Modules: []api.Module{f.m},
				})
				if err != nil {
					t.Fatal(err)
				}
				runPath := "/v1/m/sessions/runs/" + f.runRef
				before := f.h.do(http.MethodGet, runPath, f.admin, tenantHdr(f.tenant))
				if canRead {
					if before.code != http.StatusOK || before.body["work_lease_state"] != "active" {
						t.Fatalf("authorized read = %d %s; want active", before.code, before.raw)
					}
				} else if before.code != http.StatusNotFound {
					t.Fatalf("denied read = %d %s; want concealed 404", before.code, before.raw)
				}
				if action == "interrupt" {
					claudeProtocolStub(t, proc, "success")
				}
				controlled := f.h.doJSON(http.MethodPost, runPath+"/"+action, f.admin,
					map[string]any{"work_lease_fence": f.fence}, tenantHdr(f.tenant))
				if controlled.code != http.StatusOK || controlled.body["work_lease_state"] != "unknown" {
					t.Errorf("%s = %d %s; want successful control with unknown lease posture", action, controlled.code, controlled.raw)
				}
				if action == "stop" && controlled.body["state"] != stateStopped {
					t.Errorf("Stop did not stop the run: %s", controlled.raw)
				}
			})
		}
	}
}
