// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Real boot, native work transaction, OS child and HTTP MCP edge. Only the
// provider executable is a stand-in; no vendor account or model is required.
func TestManagedOwnerRefreshKeepsNativeWorkProcessAndMCP(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			backing := communicationHTTPTestSQLiteStore(t)
			if engine == "postgres" {
				backing = communicationHTTPTestPostgresStore(t)
			}
			e := bootWorkLaunchAuthorityEstate(t, backing)
			dir := filepath.Dir(e.marker)
			tokenFile := filepath.Join(dir, "managed-token")
			script := filepath.Join(dir, "claude-stand-in")
			body := "#!/bin/sh\numask 077\nprintf '%s\\n' \"$$\" >> '" + e.marker + "'\nprintf '%s' \"$OLIVARES_HOOK_PEP_TOKEN\" > '" + tokenFile + "'\nexec cat >/dev/null\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, pid := range readLaunchedPIDs(t, e.marker) {
					if processAlive(pid) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			// Obtain the exact login bearer whose authenticated principal launches work.
			bearer, _, err := e.eng.authr.Login(t.Context(), "root@work-launch-authority.test", "work-launch-authority-root-password", "test")
			if err != nil {
				t.Fatal(err)
			}
			e.launcher, err = e.eng.authr.Authenticate(t.Context(), bearer)
			if err != nil {
				t.Fatal(err)
			}
			owner := seedWorkLaunchAuthorityOwner(t, e, "renewal")
			profile, err := e.eng.sessionsMod.CreateProfile(t.Context(), e.tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: dir, DisplayName: "refresh test", AuthSource: sessions.AuthSourceAccountHome})
			if err != nil {
				t.Fatal(err)
			}
			out := launchWorkWithinBound(t, e, sessions.WorkLaunchSpec{WorkItemID: owner.item, AuditActorRef: e.launcher.UserID.String(), Runtime: sessions.CreateRunParams{Name: "renewal", Transport: sessions.TransportStreamJSON, Isolation: sessions.IsolationNative, Actor: e.launcher.Actor(), ActorKind: e.launcher.ActorKind(), AgentRef: owner.ownerExternal, ProviderProfileRef: profile.Ref}})
			if out.err != nil {
				t.Fatal(out.err)
			}
			run := out.managed
			waitForWorkLaunchCondition(t, 5*time.Second, "native launch credential", func() bool {
				_, err := os.Stat(tokenFile)
				return len(readLaunchedPIDs(t, e.marker)) == 1 && err == nil
			})
			pid := readLaunchedPIDs(t, e.marker)[0]
			token, err := os.ReadFile(tokenFile)
			if err != nil {
				t.Fatal(err)
			}
			credentials := e.eng.hookCredentials()
			before, scope, err := credentials.Resolve(t.Context(), string(token))
			if err != nil {
				t.Fatal(err)
			}
			// Reuse the real session MCP handler and work port over HTTP.
			server := httptest.NewServer(&sessionMCPHandler{authr: credentials, issuedSessionOnly: true, admits: e.eng.admits(), work: e.eng.sessionsMod.CallSessionWork})
			defer server.Close()
			call := func() {
				t.Helper()
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"olivares_work_list","arguments":{}}}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+string(token))
				req.Header.Set("Content-Type", "application/json")
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 || strings.Contains(string(raw), `"isError":true`) || strings.Contains(string(raw), `"error":`) || !strings.Contains(string(raw), `"result"`) {
					t.Fatalf("native MCP work call: status=%d body=%s", resp.StatusCode, raw)
				}
			}
			call()
			for cycle := 1; cycle <= 2; cycle++ {
				old := bearer
				bearer, _, err = e.eng.authr.RefreshSession(t.Context(), e.launcher)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.eng.authr.Authenticate(t.Context(), old); err == nil {
					t.Fatal("old bearer stayed valid")
				}
				e.launcher, err = e.eng.authr.Authenticate(t.Context(), bearer)
				if err != nil {
					t.Fatal(err)
				}
				call()
				after, got, err := credentials.ResolveRun(t.Context(), e.tenant, run.RunRef)
				if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(scope, got) {
					t.Fatalf("refresh changed live authority: %v", err)
				}
				lease, err := e.eng.sessionsMod.GetLease(t.Context(), e.tenant, sessions.WorkPrincipal{}, owner.item)
				if err != nil || !lease.Live || lease.HolderRunRef != run.RunRef || lease.HolderSID != run.SessionID || lease.Fence != run.WorkLeaseFence || !processAlive(pid) || len(readLaunchedPIDs(t, e.marker)) != 1 {
					t.Fatalf("cycle %d lost the original process/work lease: %v", cycle, err)
				}
			}
			if err = e.eng.authr.RevokeSession(t.Context(), e.launcher, e.launcher.CredID); err != nil {
				t.Fatal(err)
			}
			if _, _, err = credentials.Resolve(t.Context(), string(token)); err == nil {
				t.Fatal("revoked owner retained MCP authority")
			}
			waitForWorkLaunchCondition(t, 5*time.Second, "revoked owner process exit", func() bool { return !processAlive(pid) })
			if len(readLaunchedPIDs(t, e.marker)) != 1 {
				t.Fatal("revocation restarted work")
			}
			if _, _, err = credentials.CheckOwnerAccess(t.Context(), e.tenant, run.RunRef); err != auth.ErrSessionAccessEnded {
				t.Fatalf("revocation lost terminal owner reason: %v", err)
			}
		})
	}
}
