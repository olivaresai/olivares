// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type limitResumeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *limitResumeClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.at)
}

func TestUsageLimitResumeAtReset(t *testing.T) {
	for _, action := range []string{"resume", "stop", "next reset", "stop permission"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			clock := &limitResumeClock{at: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
			runner := &fakeRunner{initSID: "limited-conversation"}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithClock(clock))
			m.QueuedCredentialCapture = sealCredential
			originalCredential := launchCredential(model.NewID())
			credential := launchCredential(model.NewID())
			principal := auth.Principal{Kind: auth.KindUser, UserID: credential.UserID}
			m.QueuedLaunchAuthorization = func(context.Context, model.TenantID, auth.QueuedCredential, string, model.ID) (auth.Principal, error) {
				return principal, nil
			}
			m.UseExecutionEnvironmentRef(testEnvRef)
			m.UseToolLoginsRoot(t.TempDir())
			home, _, _ := ToolLoginHome(m.toolLoginsRoot, tenant, "claude")
			configHome, err := m.ownLoginConfigHome(tenant, "claude")
			if err != nil {
				t.Fatal(err)
			}
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", UserHome: home, ConfigHome: configHome, AuthSource: AuthSourceAccountHome})
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{queuedCredential: originalCredential, ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "conversation", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
			if action != "stop permission" {
				if _, err = m.stopRun(ctx, tenant, run.RunRef, actorU, actorKindU); err != nil {
					t.Fatal(err)
				}
			}
			reset := clock.at.Add(time.Hour)
			raw, _ := json.Marshal(runLimit{Kind: limitKindUsage, ResetsAt: reset.Format(time.RFC3339), AccountRef: run.ProviderProfileRef})
			if _, err = m.transition(ctx, tenant, run.RunRef, transitionInput{event: eventLimitReached, keepReason: true, mutate: func(r model.Record) { r[colRunUsageLimit] = string(raw) }}); err != nil {
				t.Fatal(err)
			}
			if action == "stop permission" {
				if _, err = m.scheduleLimitResume(ctx, tenant, run.RunRef, principal, credential); err == nil {
					t.Fatal("scheduling stopped a live session without stop authority")
				}
				d, err := m.getRun(ctx, tenant, run.RunRef)
				if err != nil || d.State != stateRunning {
					t.Fatalf("refused schedule changed live run: %+v %v", d, err)
				}
				return
			}
			if _, err = m.scheduleLimitResume(ctx, tenant, run.RunRef, principal, credential); err != nil {
				t.Fatal(err)
			}
			stored, err := m.loadRun(ctx, tenant, run.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if stored.String(colRunLauncherUserID) != originalCredential.UserID.String() {
				t.Fatal("scheduling changed original-launch evidence")
			}
			before := launchCount(runner)
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			if launchCount(runner) != before {
				t.Fatal("resumed before reset")
			}
			if action == "stop" {
				if _, err = m.stopRun(ctx, tenant, run.RunRef, actorU, actorKindU); err != nil {
					t.Fatal(err)
				}
			}
			if action == "next reset" {

				m.LimitResets = func(context.Context, model.TenantID, string) (time.Time, bool) { return reset.Add(time.Hour), true }
			}
			clock.mu.Lock()
			clock.at = reset.Add(4 * time.Second)
			clock.mu.Unlock()
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			if launchCount(runner) != before {
				t.Fatal("resumed before the reset margin")
			}
			clock.mu.Lock()
			clock.at = reset.Add(10 * time.Second)
			clock.mu.Unlock()
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			want := before
			if action == "resume" {
				want++
			}
			if got := launchCount(runner); got != want {
				t.Fatalf("launches=%d want=%d", got, want)
			}
			d, err := m.getRun(ctx, tenant, run.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if action == "resume" {
				if d.ProviderProfileRef != run.ProviderProfileRef || d.ClaudeSessionID != "limited-conversation" {
					t.Fatal("scheduled resume changed account or conversation")
				}
				proc := runner.lastProc()
				proc.mu.Lock()
				sent := append([][]byte(nil), proc.sent...)
				proc.mu.Unlock()
				if len(sent) != 1 || !bytes.Contains(sent[0], []byte(limitResumeMessage)) {
					t.Fatalf("scheduled messages=%q", sent)
				}
			}
			if action == "next reset" && d.ResumeAt != model.NewTimestamp(reset.Add(time.Hour+limitResumeMargin)).String() {
				t.Fatalf("next reset not scheduled: %+v", d)
			}
		})
	}
}

func TestUsageLimitAutomaticPreferenceIsPersonalAndOffByDefault(t *testing.T) {
	m := New()
	m.log = slog.Default()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "limit-preferences")
	viewer := h.viewerToken(admin, tenant, "reader@limit-preferences.example")
	path := "/v1/m/sessions/limit-resume-settings"
	for _, credential := range []string{admin, viewer} {
		r := h.do("GET", path, credential, tenantHdr(tenant))
		if r.code != 200 || r.body["automatic"] != false {
			t.Fatalf("default=%d %s", r.code, r.raw)
		}
	}
	r := h.doJSON("PUT", path, admin, map[string]any{"automatic": true}, tenantHdr(tenant))
	if r.code != 200 || r.body["automatic"] != true {
		t.Fatalf("set=%d %s", r.code, r.raw)
	}
	if r = h.do("GET", path, viewer, tenantHdr(tenant)); r.code != 200 || r.body["automatic"] != false {
		t.Fatalf("another person's preference leaked: %d %s", r.code, r.raw)
	}
	if r = h.do("GET", path, admin, tenantHdr(tenant)); r.code != 200 || r.body["automatic"] != true {
		t.Fatalf("preference not retained: %d %s", r.code, r.raw)
	}
	if r = h.doJSON("PUT", path, admin, map[string]any{"automatic": false}, tenantHdr(tenant)); r.code != 200 || r.body["automatic"] != false {
		t.Fatalf("disable=%d %s", r.code, r.raw)
	}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(limitResumeSettingsKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 1})
		if len(rows) != 0 {
			t.Error("disabled preference retains an authority-bearing row")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r = h.doJSON("PUT", path, viewer, map[string]any{"automatic": true}, tenantHdr(tenant)); r.code != 403 {
		t.Fatalf("read-only caller changed resume behavior: %d %s", r.code, r.raw)
	}
	if r = h.doJSON("PUT", path, admin, map[string]any{}, tenantHdr(tenant)); r.code != 400 {
		t.Fatalf("missing choice=%d %s", r.code, r.raw)
	}
}

func TestUsageLimitAutomaticSchedulesAndUserActionsCancel(t *testing.T) {
	for _, action := range []string{"stop", "done", "archive", "message", "delete", "snooze"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			clock := &limitResumeClock{at: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
			runner := &fakeRunner{initSID: "automatic-conversation"}
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithClock(clock))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "automatic-limit")
			issuer := auth.NewAuthenticator(h.st, nil)
			m.WorkAuthorizer = auth.NewAuthorizer(nil)
			m.QueuedCredentialCapture = issuer.BindQueuedCredential
			m.QueuedLaunchAuthorization = func(ctx context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
				return issuer.RevalidateQueuedCredential(ctx, c)
			}
			settings := h.doJSON("PUT", "/v1/m/sessions/limit-resume-settings", admin, map[string]any{"automatic": true}, tenantHdr(tenant))
			if settings.code != 200 {
				t.Fatalf("preference=%d %s", settings.code, settings.raw)
			}
			created := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "transport": "stream-json"}, tenantHdr(tenant))
			if created.code != 201 {
				t.Fatalf("create=%d %s", created.code, created.raw)
			}
			ref := created.body["run_ref"].(string)
			path := "/v1/m/sessions/runs/" + ref
			reset := clock.at.Add(time.Hour)
			frame, _ := json.Marshal(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "resetsAt": reset.Unix()}})
			runner.lastProc().out <- OutputFrame{Stream: streamStdout, Data: frame}
			runner.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","is_error":true,"api_error":"usage_limit_reached"}`)}
			waitFor(t, "the recorded usage limit", func() bool { d, _ := m.getRun(ctx, tenant, ref); return d.Limit != nil })
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			if d, err := m.getRun(ctx, tenant, ref); err != nil || d.ResumeAt == "" {
				t.Fatalf("automatic schedule missing: %+v %v", d, err)
			}
			var result resp
			switch action {
			case "stop", "done", "archive":
				result = h.doJSON("POST", path+"/"+action, admin, nil, tenantHdr(tenant))
			case "message":
				result = h.doJSON("POST", path+"/resume", admin, nil, tenantHdr(tenant))
				if result.code != 200 {
					t.Fatalf("manual resume=%d %s", result.code, result.raw)
				}
				if result.body["resume_cancelled"] != true {
					t.Fatal("manual continuation did not report cancellation of the schedule")
				}
				result = h.doJSON("POST", path+"/input", admin, map[string]any{"text": "new work"}, tenantHdr(tenant))
			case "delete":
				result = h.doJSON("POST", path+"/cleanup", admin, nil, tenantHdr(tenant))
				if result.code != 200 {
					t.Fatalf("cleanup=%d %s", result.code, result.raw)
				}
				result = h.do("DELETE", path, admin, tenantHdr(tenant))
			case "snooze":
				if r := h.doJSON("POST", path+"/done", admin, nil, tenantHdr(tenant)); r.code != 200 {
					t.Fatalf("mark done=%d %s", r.code, r.raw)
				}
				if r := h.doJSON("POST", path+"/resume", admin, map[string]any{"at_reset": true}, tenantHdr(tenant)); r.code != 200 {
					t.Fatalf("schedule after done=%d %s", r.code, r.raw)
				}
				result = h.doJSON("POST", path+"/snooze", admin, map[string]any{"until": reset.Format(time.RFC3339)}, tenantHdr(tenant))
			}
			if result.code < 200 || result.code >= 300 {
				t.Fatalf("%s=%d %s", action, result.code, result.raw)
			}
			if action != "delete" {
				row, err := m.loadRun(ctx, tenant, ref)
				if err != nil {
					t.Fatal(err)
				}
				if action == "snooze" {
					if !row.Bool(colRunResumeAfterLimit) || row.String(colRunResumeAt) == "" {
						t.Fatal("snoozing a done session cancelled its later scheduled continuation")
					}
				} else if row.Bool(colRunResumeAfterLimit) || row.String(colRunResumeAt) != "" {
					t.Fatalf("%s retains the scheduled continuation's consent", action)
				}
			}
			before := launchCount(runner)
			clock.mu.Lock()
			clock.at = reset.Add(10 * time.Second)
			clock.mu.Unlock()
			m.sweepLimitResumes(ctx, []model.TenantID{tenant})
			want := before
			if action == "snooze" {
				want++
			}
			if got := launchCount(runner); got != want {
				t.Fatalf("launches after %s=%d want=%d", action, got, want)
			}
		})
	}
}
