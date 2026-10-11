// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRuntimeAPIRejectsUnsupportedIsolationBeforeLaunch(t *testing.T) {
	for name, runner := range map[string]Runner{"stdio": NewProcRunner(), "pty": NewPTYRunner()} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			minted := 0
			m := New(WithSessionWorkspaceRoot(root), WithRunner(runner), WithCredentialSource(
				CredentialSourceFunc(func(ctx context.Context, req CredentialRequest) (Credential, error) {
					minted++
					return Credential{ID: "isolation-test", Token: "test-token", Scheme: "mock", NotAfter: farFuture}, nil
				})))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "isolation")
			for _, isolation := range []string{"container", "sandbox"} {
				r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
					"isolation": isolation,
				}, tenantHdr(tenant))
				if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "isolation=native") ||
					!strings.Contains(r.raw, `"code":"isolation_unsupported"`) {
					t.Errorf("%s: create = %d %s; want 422 with native remedy", isolation, r.code, r.raw)
				}
			}
			if minted != 0 {
				t.Errorf("unsupported isolation minted %d credentials", minted)
			}
			r := h.do("GET", "/v1/m/sessions/runs", admin, tenantHdr(tenant))
			items, ok := r.body["items"].([]any)
			if r.code != http.StatusOK || !ok || len(items) != 0 {
				t.Errorf("unsupported isolation left run rows: %d %s", r.code, r.raw)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Errorf("unsupported isolation left workspaces: %v, %v", entries, err)
			}
		})
	}
}

func TestResumeRejectsUnsupportedIsolationBeforeLaunch(t *testing.T) {
	for _, isolation := range []Isolation{IsolationContainer, IsolationSandbox} {
		t.Run(string(isolation), func(t *testing.T) {
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(NewProcRunner()), WithCredentialSource(staticCred()))
			if _, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Isolation: isolation}); !isStatus(err, http.StatusUnprocessableEntity) {
				t.Fatalf("in-process create = %v; want 422", err)
			}
			const ref = "01900000-0000-7000-8000-000000000035"
			if err := st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(t.Context(), model.Record{
					colRunRef: ref, colTransport: string(TransportStreamJSON),
					colPermissionMode: "default", colIsolation: string(isolation),
					colState: stateFailed, colLastEventSeq: int64(0),
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, err := m.resumeRun(t.Context(), tenant, ref, actorU, actorKindU, "")
			if !isStatus(err, http.StatusUnprocessableEntity) || !strings.Contains(err.Error(), "isolation=native") {
				t.Fatalf("resume = %v; want 422 with native remedy", err)
			}
			if got := listRunEvents(t, st, tenant, ref); len(got) != 0 {
				t.Fatalf("refused resume added lifecycle events: %+v", got)
			}
		})
	}
}

func TestUnsupportedIsolationPrecedesDefaultProfileResolution(t *testing.T) {
	m := New(WithRunner(NewProcRunner()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	resolved := 0
	m.ToolLogin = func(context.Context, model.TenantID, string) (bool, bool, error) {
		resolved++
		return true, true, nil
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "isolation-profile")
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"isolation": "container",
	}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "isolation=native") {
		t.Fatalf("create = %d %s; want isolation refusal", r.code, r.raw)
	}
	if resolved != 0 {
		t.Fatalf("unsupported isolation resolved a default profile %d times", resolved)
	}
}
