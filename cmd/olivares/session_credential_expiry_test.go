// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type sessionExpiryClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *sessionExpiryClock) Now() model.Timestamp {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return model.NewTimestamp(c.now)
}
func (c *sessionExpiryClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestExpiredManagedCredentialStopsNativeProcessAndExplainsSuccessor(t *testing.T) {
	for _, path := range []string{"exact-scope", "fallback-scope"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			m := h.set.sessions
			dir := t.TempDir()
			marker := filepath.Join(dir, "owned.pids")
			script := filepath.Join(dir, "claude-stand-in")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" >> '"+marker+"'\nexec cat >/dev/null\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, pid := range readLaunchedPIDs(t, marker) {
					if processAlive(pid) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			sessions.WithRunner(sessions.NewProcRunner())(m)
			sessions.WithProgram(script)(m)
			sessions.WithKillSwitchSweep(5 * time.Millisecond)(m)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if qualified := os.Getenv("OLIVARES_TEST_AUTH_EXPIRY_BINARY"); qualified != "" {
				executable = qualified
			}
			sessions.WithClaudeHookPEP(dir, executable)(m)
			m.UseExecutionEnvironmentRef("expiry-native-test")
			clock := &sessionExpiryClock{now: time.Now().UTC().Truncate(time.Millisecond)}
			a := auth.NewAuthenticator(h.st, clock)
			credentials := newSessionHookCredentials(a, h.st, m, h.set.gov)
			if path == "fallback-scope" {
				m.SessionAccessCheck = func(ctx context.Context, tenant model.TenantID, run string) (auth.SessionScope, string, error) {
					scope, user, err := credentials.CheckOwnerAccess(ctx, tenant, run)
					if errors.Is(err, auth.ErrSessionCredentialExpired) {
						scope.WorkspaceID = model.NewID()
					}
					return scope, user, err
				}
			}
			sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
				_, err := credentials.mint(ctx, tenant, intent)
				return sessions.LaunchDecision{Allowed: err == nil}, err
			}))(m)
			var profile struct {
				Ref string `json:"profile_ref"`
			}
			if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": dir, "user_home": dir}, &profile); code != 201 {
				t.Fatalf("profile=%d", code)
			}
			ownerBearer := h.newUser("expiry-owner@e2e.test", "expiry-owner-password", h.tenantA, auth.RoleAdmin)
			launch := func(bearer string) string {
				t.Helper()
				var r struct {
					Ref string `json:"run_ref"`
				}
				code, raw := h.req(http.MethodPost, "/v1/m/sessions/runs", bearer, h.tenantA, map[string]any{"transport": "stream-json", "isolation": "native", "permission_mode": "plan", "provider_profile_ref": profile.Ref})
				if code != 201 {
					t.Fatalf("launch=%d: %s", code, raw)
				}
				if err := json.Unmarshal(raw, &r); err != nil {
					t.Fatal(err)
				}
				return r.Ref
			}
			ref := launch(ownerBearer)
			waitForWorkLaunchCondition(t, 3*time.Second, "owned native process", func() bool { return len(readLaunchedPIDs(t, marker)) == 1 })
			pid := readLaunchedPIDs(t, marker)[0]
			if err = m.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				clock.advance(8 * time.Hour)
				p, err := a.Authenticate(t.Context(), ownerBearer)
				if err != nil {
					t.Fatal(err)
				}
				ownerBearer, _, err = a.RefreshSession(t.Context(), p)
				if err != nil {
					t.Fatal(err)
				}
			}
			// The independently launched current bearer has not reached its deadline.
			other := launch(ownerBearer)
			clock.advance(8 * time.Hour)
			if _, err = a.Authenticate(t.Context(), ownerBearer); err != nil {
				t.Fatalf("owner expired before child boundary: %v", err)
			}
			var view map[string]any
			waitForWorkLaunchCondition(t, 3*time.Second, "expired internal bearer stops owned process", func() bool {
				view = nil
				h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+ref, h.adminToken, h.tenantA, nil, &view)
				return view["state"] == "stopped" && !processAlive(pid)
			})
			reason := "Session credential expired after 24 hours. Start a successor session."
			if view["reason"] != reason || view["pid"] != nil {
				t.Fatalf("expiry stop is unreadable or incomplete: %v", view["reason"])
			}
			var survivor map[string]any
			h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+other, h.adminToken, h.tenantA, nil, &survivor)
			if survivor["process_state"] != "running" {
				t.Fatal("expiry stopped another current process")
			}
			server := httptest.NewServer(h.h)
			defer server.Close()
			var output string
			if qualified := os.Getenv("OLIVARES_TEST_AUTH_EXPIRY_BINARY"); qualified != "" {
				command := exec.CommandContext(t.Context(), qualified, "session", "show", ref)
				command.Env = append(os.Environ(), "OLIVARES_SERVER_URL="+server.URL, "OLIVARES_TOKEN="+h.adminToken, "OLIVARES_TENANT="+h.tenantA)
				raw, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("qualified binary session show: %v", err)
				}
				output = string(raw)
			} else {
				var err error
				output, _, err = execSessionCLI(t, nil, "session", "show", ref, "--server", server.URL, "--token", h.adminToken, "--tenant", h.tenantA)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(output, reason) {
				t.Fatalf("CLI omitted the expiry/successor reason: %s", output)
			}
			terminal := 0
			if err := h.st.View(t.Context(), model.TenantID(h.tenantA), func(sc store.Scope) error {
				reader := sc.Audit().(store.VerifiedAuditAnchorReader)
				return sc.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
					if event.Action != "sessions.run.stopped" {
						return nil
					}
					_, canonical, found, err := reader.ReadVerifiedAuditAnchor(t.Context(), event.Seq)
					if err != nil {
						return err
					}
					if !found {
						t.Fatal("terminal audit unverified")
					}
					var meta map[string]any
					if err = json.Unmarshal([]byte(canonical), &meta); err != nil {
						return err
					}
					if meta["run_ref"] == ref {
						terminal++
						if meta["stop_cause"] != "session_credential_expired" {
							t.Fatalf("wrong typed expiry stop cause: %v", meta["stop_cause"])
						}
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if terminal != 1 {
				t.Fatalf("expiry terminal events=%d", terminal)
			}
		})
	}
}
