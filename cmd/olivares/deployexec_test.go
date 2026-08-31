// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/deploy"
)

// This proves the DoD at the seam: once a real executor is wired, the deploy
// module's Executor port ACTS (plan/apply/verify/retire return real results) instead
// of the unwiredExecutor's errNoExecutor → 503. It drives the composition-root seam
// adapter (deployExecutor) over a REAL backend (the imperative Kubernetes backend)
// pointed at an httptest API server, with a real short-lived file-token credential
// source — exercising the full translation deploySpec→Desired→backend and the
// blast-radius gate, with NO mock executor.

// kubeStub is a minimal apps/v1 Deployment API: the named deployment is absent until
// applied (PATCH server-side apply), then present.
type kubeStub struct {
	exists bool
}

func (s *kubeStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// every call must carry the bearer credential in the header, never a URL/secret
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !s.exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":404}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kind":"Deployment","metadata":{"name":"bot"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":""}]}}},"status":{}}`))
		case http.MethodPatch, http.MethodPut, http.MethodPost:
			s.exists = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kind":"Deployment","metadata":{"name":"bot"},"spec":{"replicas":1}}`))
		case http.MethodDelete:
			s.exists = false
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// wiredKubeExecutor builds a deployExecutor over a real KubeBackend at srv with a
// real file-token credential source (deny-closed if the token file is absent).
func wiredKubeExecutor(t *testing.T, srv *httptest.Server, withCred bool) *deployExecutor {
	t.Helper()
	var src executor.CredentialSource = executor.DenyCredentialSource{}
	if withCred {
		dir := t.TempDir()
		// least privilege: a separate short-lived token per mode (read for plan/observe,
		// write for apply/destroy). An external attester rotates these files.
		for _, mode := range []string{"read", "write"} {
			if err := os.WriteFile(filepath.Join(dir, "prod-"+mode+".token"), []byte("SHORT-LIVED-K8S-TOKEN-"+mode+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// the file source resolves {env}-{mode}.token at mint time
		src = executor.NewFileTokenSource(executor.FileTokenConfig{PathTemplate: filepath.Join(dir, "{env}-{mode}.token"), Scheme: "test"})
	}
	kb := executor.NewKubeBackend(executor.KubeConfig{APIBaseURL: srv.URL, InsecureSkipVerify: true, DefaultNamespace: "default"})
	return &deployExecutor{e: executor.New(executor.WithBackend(kb, "k8s"), executor.WithCredentialSource(src))}
}

func k8sReq() deploy.ExecRequest {
	// Spec is left at its zero value (its type is unexported; the seam adapter reads
	// only exported fields, which is sufficient for an empty-spec create).
	return deploy.ExecRequest{
		Tenant: model.TenantID("t-123"), Environment: "prod", Runtime: "k8s",
		Target: "k8s.namespace/default", SubjectKind: "agent", SubjectRef: "bot",
	}
}

func TestDeployExecutorActsNotUnwired(t *testing.T) {
	stub := &kubeStub{}
	srv := httptest.NewTLSServer(stub.handler())
	t.Cleanup(srv.Close)
	a := wiredKubeExecutor(t, srv, true)
	ctx := context.Background()
	req := k8sReq()

	// PLAN: a wired executor returns a real diff (create), never errNoExecutor → 503.
	changes, err := a.Plan(ctx, req)
	if err != nil {
		t.Fatalf("plan against a wired executor must succeed, got %v", err)
	}
	if len(changes) == 0 {
		t.Fatalf("plan of an absent deployment must report a create change")
	}

	// APPLY: reconciles for real; the result detail records the backend + the
	// NON-SENSITIVE credential id (never the token material).
	res, err := a.Apply(ctx, req)
	if err != nil {
		t.Fatalf("apply against a wired executor must succeed, got %v", err)
	}
	if !strings.Contains(res.Detail, "backend=k8s") {
		t.Fatalf("apply detail should record the backend: %q", res.Detail)
	}
	if strings.Contains(res.Detail, "SHORT-LIVED-K8S-TOKEN") {
		t.Fatalf("the credential MATERIAL must never appear in the result: %q", res.Detail)
	}

	// VERIFY: now in sync (the stub reports the deployment exists).
	vres, err := a.Verify(ctx, req)
	if err != nil {
		t.Fatalf("verify err = %v", err)
	}
	_ = vres // in-sync or drift both acceptable; the point is it ACTS, never 503

	// RETIRE: governed teardown succeeds (delete).
	if _, err := a.Retire(ctx, req); err != nil {
		t.Fatalf("retire against a wired executor must succeed, got %v", err)
	}
}

func TestDeployExecutorFailsClosedWithoutCredential(t *testing.T) {
	stub := &kubeStub{}
	srv := httptest.NewTLSServer(stub.handler())
	t.Cleanup(srv.Close)
	a := wiredKubeExecutor(t, srv, false) // deny-closed credential source
	_, err := a.Apply(context.Background(), k8sReq())
	if !errors.Is(err, executor.ErrNoCredentialSource) {
		t.Fatalf("apply with no credential source must fail closed (never a default key), got %v", err)
	}
	if stub.exists {
		t.Fatalf("a credential-denied apply must NEVER mutate the backend")
	}
}

func TestNewDeployExecutorNilWhenUnconfigured(t *testing.T) {
	// No backend blocks => the module keeps its deny-closed unwiredExecutor (honest 503).
	if e := newDeployExecutor(deployExecutorConfig{}, nil, discardLog()); e != nil {
		t.Fatalf("an unconfigured deploy executor must be nil (keep unwiredExecutor), got %v", e)
	}
}
