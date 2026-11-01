// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// usageAsk is one call the account routes made to the usage source.
type usageAsk struct {
	tenant      model.TenantID
	driver, ref string
	wait        bool
}

// The account read carries the usage windows its tool reported for that account: the
// single read waits for the tool, the list never does, and an account the tool reported
// nothing for carries no usage field at all (absent is unknown, never 0).
func TestProviderAccount_UsageIsTheToolsReportAndAbsentWhenUnknown(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "usage-"+be.name)
			a := newAcctAPI(m, st, tenant)
			claude := acctProfiles(t, m, tenant, "claude", 2)
			for i, name := range []string{"claude-main", "claude-spare"} {
				if r := a.adopt(claude[i].Ref, name); r.code != http.StatusOK {
					t.Fatalf("adopt %s = %d %s", name, r.code, r.raw)
				}
			}
			if r := a.call(http.MethodGet, "/provider-accounts/"+claude[0].Ref, nil); r.code != http.StatusOK {
				t.Fatalf("get with no usage source = %d %s", r.code, r.raw)
			} else if _, has := r.body["usage"]; has {
				t.Fatalf("an engine with no usage source invented usage: %s", r.raw)
			}

			observed := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
			resets := observed.Add(2 * time.Hour)
			var mu sync.Mutex
			var asks []usageAsk
			unbounded := false
			m.rt.AccountUsage = func(ctx context.Context, tn model.TenantID, driver, ref string, wait bool) []AccountUsage {
				mu.Lock()
				asks = append(asks, usageAsk{tn, driver, ref, wait})
				// A single read waits for the tool, but never longer than accountUsageWait.
				if dl, ok := ctx.Deadline(); wait && (!ok || time.Until(dl) > accountUsageWait) {
					unbounded = true
				}
				mu.Unlock()
				if ref != claude[0].Ref {
					return nil
				}
				return []AccountUsage{
					{Window: "five_hour", Label: "5-hour", Utilization: 40, ResetsAt: &resets, ObservedAt: observed},
					{Window: "seven_day", Label: "Weekly", Utilization: 0, ObservedAt: observed},
					{Window: "credits", Label: "Credits", Credit: &AccountCredit{Balance: func() *string { s := "0"; return &s }(), HasCredits: false}, ObservedAt: observed},
				}
			}
			want := []any{
				map[string]any{"window": "five_hour", "label": "5-hour", "utilization": 40.0, "resets_at": "2026-10-21T11:30:00Z", "observed_at": "2026-10-21T09:30:00Z"},
				map[string]any{"window": "seven_day", "label": "Weekly", "utilization": 0.0, "observed_at": "2026-10-21T09:30:00Z"},
				map[string]any{"window": "credits", "label": "Credits", "credit": map[string]any{"balance": "0", "has_credits": false, "unlimited": false}, "observed_at": "2026-10-21T09:30:00Z"},
			}

			r := a.call(http.MethodGet, "/provider-accounts/"+claude[0].Ref, nil)
			if r.code != http.StatusOK || !reflect.DeepEqual(r.body["usage"], want) {
				t.Fatalf("get = %d %s, want usage %v", r.code, r.raw, want)
			}
			if r := a.call(http.MethodGet, "/provider-accounts/"+claude[1].Ref, nil); r.code != http.StatusOK {
				t.Fatalf("get of an account the tool reported nothing for = %d %s", r.code, r.raw)
			} else if _, has := r.body["usage"]; has {
				t.Fatalf("an account with no report carries usage: %s", r.raw)
			}
			if !reflect.DeepEqual(asks, []usageAsk{{tenant, "claude", claude[0].Ref, true}, {tenant, "claude", claude[1].Ref, true}}) {
				t.Fatalf("single reads asked %+v", asks)
			}
			if unbounded {
				t.Fatalf("a single read waited for the tool without the %s bound", accountUsageWait)
			}

			asks = nil
			byName := map[string]map[string]any{}
			for _, item := range a.list("").items() {
				byName[item["name"].(string)] = item
			}
			if !reflect.DeepEqual(byName["claude-main"]["usage"], want) {
				t.Fatalf("list usage of claude-main = %v, want %v", byName["claude-main"]["usage"], want)
			}
			if _, has := byName["claude-spare"]["usage"]; has {
				t.Fatalf("list carries usage for an account with no report: %v", byName["claude-spare"])
			}
			for _, ask := range asks {
				if ask.wait {
					t.Fatalf("the list waited for a tool: %+v", asks)
				}
			}
			if len(asks) != 2 {
				t.Fatalf("the list asked %+v, want one cold read per account", asks)
			}
		})
	}
}

// An account that is not active is never asked about: its home may not be used, and a
// probe there would start a tool for nothing.
func TestProviderAccount_UsageIsNotAskedForADisabledAccount(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "usage-off-"+be.name)
			a := newAcctAPI(m, st, tenant)
			prof := acctProfiles(t, m, tenant, "claude", 1)[0]
			if r := a.adopt(prof.Ref, "claude-off"); r.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", r.code, r.raw)
			}
			disabled := ProfileDisabled
			if _, err := m.PatchProfile(context.Background(), tenant, prof.Ref, ProfilePatch{State: &disabled}); err != nil {
				t.Fatal(err)
			}
			m.rt.AccountUsage = func(context.Context, model.TenantID, string, string, bool) []AccountUsage {
				t.Error("the usage source was asked about a disabled account")
				return []AccountUsage{{Window: "five_hour", Utilization: 1, ObservedAt: time.Now()}}
			}
			r := a.call(http.MethodGet, "/provider-accounts/"+prof.Ref, nil)
			if _, has := r.body["usage"]; r.code != http.StatusOK || has || r.body["state"] != string(ProfileDisabled) {
				t.Fatalf("get of a disabled account = %d %s", r.code, r.raw)
			}
			a.list("")
		})
	}
}
