// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Exercise the production TCP server, authority, plain-git adapter and executor.
// Only the network dial to Git is replaced by the existing file test transport.
func TestPublicationReplyBeyondHTTPWriteTimeout(t *testing.T) {
	f := plainGitModule(t, "main", "main")
	// A receive hook proves that an actual write started and holds it past the
	// ordinary HTTP budget. The executor's finite dispatch deadline expires first.
	if err := os.WriteFile(filepath.Join(f.remote, "hooks", "pre-receive"), []byte("#!/bin/sh\necho dispatch >> attempts\nsleep 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newServer(t)
	s.m.UseCustody(f.m.opts.Custody)
	s.m.UseGit(f.m.opts.Git)
	s.m.opts.AdmissionTimeout = time.Second
	s.m.opts.DispatchTimeout = 500 * time.Millisecond
	if code, _, raw := s.do("POST", "/v1/setup", "", map[string]any{"token": s.setup, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	root := s.login("root@x.io", "supersecret1")
	code, org, raw := s.do("POST", "/v1/system/orgs", root, map[string]any{"name": "acme", "slug": "acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("org = %d %s", code, raw)
	}
	s.tenant = model.TenantID(org["tenant_id"].(string))
	admin := s.member(root, "admin")
	code, target, raw := s.do("POST", "/v1/m/gitpublish/targets", root, targetBody{
		WorkspaceID: model.NewID().String(), CredentialBindingID: "cb1", RepositoryBindingID: "rb1", PushPrefix: "olivares/", MergeBases: []string{"main"},
	}, s.tenant)
	if code != http.StatusCreated {
		t.Fatalf("target = %d %s", code, raw)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := s.srv.NewHTTPServer(listener.Addr().String())
	httpServer.WriteTimeout = 100 * time.Millisecond
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		if err := <-serveDone; err != http.ErrServerClosed {
			t.Errorf("HTTP server: %v", err)
		}
	})
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(pushBody{OperationID: "held-receive", Ref: "refs/heads/olivares/held", Commit: f.commit, Tree: f.tree})
	if err != nil {
		t.Fatal(err)
	}
	push := func() (int, intentDTO) {
		t.Helper()
		req, err := http.NewRequest("POST", "http://"+listener.Addr().String()+"/v1/m/gitpublish/targets/"+target["id"].(string)+"/pushes", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Olivares-Tenant", s.tenant.String())
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("publication reply lost after the ordinary write timeout: %v", err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var intent intentDTO
		if err := json.Unmarshal(b, &intent); err != nil {
			t.Fatalf("receipt: %v: %s", err, b)
		}
		return res.StatusCode, intent
	}
	started := time.Now()
	status, intent := push()
	if time.Since(started) <= httpServer.WriteTimeout || status != http.StatusAccepted || intent.State != StateUncertain || intent.ID == "" || intent.Receipt != ReceiptNone {
		t.Fatalf("held publication = %d %+v", status, intent)
	}
	status, replay := push()
	if status != http.StatusAccepted || replay.ID != intent.ID || replay.State != StateUncertain {
		t.Fatalf("stored replay = %d %+v", status, replay)
	}
	attempts, err := os.ReadFile(filepath.Join(f.remote, "attempts"))
	if err != nil || strings.Count(string(attempts), "dispatch\n") != 1 {
		t.Fatalf("receive attempts = %q, %v; want one", attempts, err)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
	err      error
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return w.err
}

func TestPublicationReplyBudgetIsFiniteAndRouteScoped(t *testing.T) {
	for _, opts := range []Options{{}, {AdmissionTimeout: 7 * time.Second, DispatchTimeout: 3 * time.Minute}} {
		m := New(opts)
		var routes []mounted
		m.APIRoutes(sealedRecorder{routes: &routes})
		for _, route := range routes {
			publication := route.pattern == "/targets/{id}/pushes" || route.pattern == "/targets/{id}/pull-requests" || route.pattern == "/targets/{id}/merges"
			if !publication && route.pattern != "/targets" {
				continue
			}
			if route.method != "POST" {
				continue
			}
			for _, deadlineErr := range []error{nil, http.ErrNotSupported, errors.New("deadline failed")} {
				t.Run(route.pattern+"/"+fmt.Sprint(opts.DispatchTimeout)+"/"+fmt.Sprint(deadlineErr), func(t *testing.T) {
					w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), err: deadlineErr}
					before := time.Now()
					route.h(w, httptest.NewRequest("POST", "/", strings.NewReader("{")), api.ModuleContext{})
					if publication {
						budget := m.opts.AdmissionTimeout + m.opts.DispatchTimeout + 90*time.Second
						if w.deadline.Before(before.Add(budget)) || w.deadline.After(time.Now().Add(budget)) {
							t.Fatalf("deadline = %v, want finite operation budget %s", w.deadline, budget)
						}
						if deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
							if w.Code != http.StatusServiceUnavailable {
								t.Fatalf("deadline failure must refuse before decoding or dispatch: %d", w.Code)
							}
							return
						}
					} else if !w.deadline.IsZero() {
						t.Fatal("ordinary target route acquired the publication budget")
					}
					if w.Code != http.StatusBadRequest {
						t.Fatalf("malformed body = %d", w.Code)
					}
				})
			}
		}
	}
}
