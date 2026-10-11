// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestOpenCodeExplicitProfileWithoutSignInRefusesBeforeSpawn(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		installed             bool
		err                   error
		missing               bool
		wantStatus            int
		wantCode, wantMessage string
	}{
		{name: "selected profile unsigned", installed: true, wantStatus: http.StatusConflict, wantCode: resolveCodeNothingToRunOn, wantMessage: "OpenCode is not signed in"},
		{name: "status unavailable", installed: true, err: errors.New("private-native-status-output"), wantStatus: http.StatusServiceUnavailable, wantMessage: "could not be read"},
		{name: "status unwired", missing: true, wantStatus: http.StatusServiceUnavailable, wantMessage: "cannot be checked"},
		{name: "tool absent", wantStatus: http.StatusConflict, wantCode: resolveCodeToolNotInstalled, wantMessage: "Install OpenCode first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
			// A login in the default home is not authority for this selected profile.
			m.ToolLogin = func(context.Context, model.TenantID, string) (bool, bool, error) { return true, true, nil }
			calls := 0
			m.ProfileLogin = func(_ context.Context, gotTenant model.TenantID, driver, ref string) (bool, bool, error) {
				calls++
				if gotTenant != tenant || driver != providerDriverOpenCode || ref != prof.Ref {
					t.Fatal("status read escaped the selected profile")
				}
				return tc.installed, false, tc.err
			}
			if tc.missing {
				m.ProfileLogin = nil
			}
			record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "must-not-start"})
			dto, err := openCodeLaunch(t, m, tenant, prof)
			if err == nil {
				_, _ = m.stopRun(t.Context(), tenant, dto.RunRef, "user:u1", model.ActorUser)
				t.Fatal("an explicit OpenCode profile without sign-in or a bound provider started")
			}
			var re *runErr
			if !errors.As(err, &re) || re.status != tc.wantStatus || !strings.Contains(err.Error(), tc.wantMessage) || strings.Contains(err.Error(), "private-native-status-output") {
				t.Fatalf("wrong public refusal: %v", err)
			}
			if tc.wantCode != "" {
				var coded *codedRunErr
				if !errors.As(err, &coded) || coded.code != tc.wantCode {
					t.Fatalf("wrong refusal code: %v", err)
				}
			}
			if !tc.missing && calls != 1 {
				t.Fatalf("native status reads = %d", calls)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatal("the unsigned-in profile spawned a child")
			}
		})
	}
}

func TestOpenCodeSelectedProfileSignInIsRecheckedOnResume(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	signedIn, calls := true, 0
	m.ProfileLogin = func(_ context.Context, gotTenant model.TenantID, driver, ref string) (bool, bool, error) {
		calls++
		if gotTenant != tenant || driver != providerDriverOpenCode || ref != prof.Ref {
			t.Fatal("status read escaped the selected profile")
		}
		return true, signedIn, nil
	}
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "signed-in-session"})
	first, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("selected signed-in profile refused: %v", err)
	}
	if _, err := m.stopRun(t.Context(), tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	signedIn = false
	record := setOpenCodeFixture(t, prof, openCodeFixture{RecordPath: t.TempDir() + "/refused-resume.json"})
	if _, err := m.resumeRun(t.Context(), tenant, first.RunRef, "user:u1", model.ActorUser, ""); err == nil || !strings.Contains(err.Error(), "OpenCode is not signed in") {
		t.Fatalf("resume reused old sign-in: %v", err)
	}
	if calls != 2 {
		t.Fatalf("native status reads = %d, want create and resume", calls)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("resume spawned after sign-out")
	}
}
