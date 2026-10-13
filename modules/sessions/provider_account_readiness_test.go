// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestProviderAccountReadinessUsesSelectedHome(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		defaultSigned, accountSigned  bool
		missingReader, missingProgram bool
		err                           error
		want                          int
	}{
		{name: "only selected account signed in", accountSigned: true},
		{name: "only default signed in", defaultSigned: true, want: http.StatusConflict},
		{name: "neither signed in", want: http.StatusConflict},
		{name: "status unreadable", defaultSigned: true, err: errors.New("private native output"), want: http.StatusServiceUnavailable},
		{name: "status unwired", defaultSigned: true, missingReader: true, want: http.StatusServiceUnavailable},
		{name: "tool absent", defaultSigned: true, accountSigned: true, missingProgram: true, want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{initSID: "account-readiness-session"}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner))
			m.UseExecutionEnvironmentRef(testEnvRef)
			acctHomeRoot(t, m)
			a := newAcctAPI(m, st, tenant)
			created := a.create("claude", "claude-b")
			if created.code != http.StatusCreated {
				t.Fatal(created.raw)
			}
			ref := created.body["account_ref"].(string)
			m.ToolLogin = func(context.Context, model.TenantID, string) (bool, bool, error) {
				t.Fatal("named account readiness read the default login")
				return true, tc.defaultSigned, nil
			}
			m.ProfileLogin = func(_ context.Context, gotTenant model.TenantID, driver, gotRef string) (bool, bool, error) {
				if gotTenant != tenant || driver != "claude" || gotRef != ref {
					t.Fatal("status escaped the selected account")
				}
				return !tc.missingProgram, tc.accountSigned, tc.err
			}
			if tc.missingReader {
				m.ProfileLogin = nil
			}
			for _, probe := range []struct {
				name string
				run  func() error
			}{
				{"resolve", func() error { _, err := m.ResolveAccountProfile(t.Context(), tenant, "claude", "claude-b"); return err }},
				{"launch", func() error {
					run, err := m.createRun(t.Context(), tenant, CreateRunParams{
						ProviderProfileRef: ref, Transport: TransportStreamJSON,
						Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
					})
					if err == nil {
						_, _ = m.stopRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser)
					}
					return err
				}},
			} {
				t.Run(probe.name, func(t *testing.T) {
					err := probe.run()
					if tc.want == 0 {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if statusOf(err) != tc.want || strings.Contains(err.Error(), "private native output") {
						t.Fatalf("selected account readiness = %v, want HTTP %d", err, tc.want)
					}
				})
			}
		})
	}
}

func TestProviderAccountReadinessIsRecheckedOnResume(t *testing.T) {
	runner := &fakeRunner{initSID: "account-resume-session"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner))
	m.UseExecutionEnvironmentRef(testEnvRef)
	acctHomeRoot(t, m)
	created := newAcctAPI(m, st, tenant).create("claude", "claude-b")
	if created.code != http.StatusCreated {
		t.Fatal(created.raw)
	}
	ref := created.body["account_ref"].(string)
	signedIn, calls := true, 0
	m.ProfileLogin = func(_ context.Context, gotTenant model.TenantID, driver, gotRef string) (bool, bool, error) {
		calls++
		if gotTenant != tenant || driver != "claude" || gotRef != ref {
			t.Fatal("status escaped the selected account")
		}
		return true, signedIn, nil
	}
	run, err := m.createRun(t.Context(), tenant, CreateRunParams{ProviderProfileRef: ref, Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "account session id capture", func() bool {
		got, _ := m.getRun(t.Context(), tenant, run.RunRef)
		return got.ClaudeSessionID == "account-resume-session"
	})
	if _, err := m.stopRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	signedIn = false
	if _, err := m.resumeRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser, ""); statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "claude-b is not signed in") {
		t.Fatalf("resume reused old login: %v", err)
	}
	if calls != 2 {
		t.Fatalf("native status reads = %d, want create and resume", calls)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.specs) != 1 {
		t.Fatalf("resume spawned after sign-out: %d launches", len(runner.specs))
	}
}
