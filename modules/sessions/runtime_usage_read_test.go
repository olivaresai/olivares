// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// usageRows reads GET /usage and returns its items as maps, failing on any other status.
func usageRows(t *testing.T, h *harness, token string, tenant model.TenantID, query string) []map[string]any {
	t.Helper()
	r := h.do("GET", "/v1/m/sessions/usage?"+query, token, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET usage?%s = %d %s", query, r.code, r.raw)
	}
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("GET usage?%s: items is %T, want a list: %s", query, r.body["items"], r.raw)
	}
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		rows = append(rows, it.(map[string]any))
	}
	return rows
}

// wantUsage checks one row field by field. A nil want means the JSON null: a sum nobody reported.
func wantUsage(t *testing.T, row map[string]any, key string, runs float64, in, out, cost any, noTokens, noCost float64) {
	t.Helper()
	got, present := row["key"]
	if key == "" {
		if present {
			t.Errorf("row %v: key present, want absent for a run that recorded none", row)
		}
		got = ""
	}
	if got != key || row["runs"] != runs || row["runs_without_tokens"] != noTokens || row["runs_without_cost"] != noCost {
		t.Errorf("row = %v, want key %q runs %v without tokens %v without cost %v", row, key, runs, noTokens, noCost)
	}
	for name, want := range map[string]any{"input_tokens": in, "output_tokens": out, "cost_micro_usd": cost} {
		v, present := row[name]
		if !present || v != want {
			t.Errorf("row %q %s = %v (present %t), want %v: unreported must be null, never 0", key, name, v, present, want)
		}
	}
}

// TestRunUsage_GroupsRunsAndNeverInventsAValue drives GET /usage over runs that report
// everything, only tokens, nothing, and a legacy run that names no profile or driver.
func TestRunUsage_GroupsRunsAndNeverInventsAValue(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedUsageRuns(t, h, tenant, []model.Record{
		{colRunUsageModelRef: "claude-opus-5-5", colRunProfileID: "ppf_A", colRunProfileDriver: "claude",
			colRunInputTokens: int64(1000), colRunOutputTokens: int64(200), colRunCostMicroUSD: int64(5000)},
		// tokens reported, price not
		{colRunUsageModelRef: "claude-opus-5-5", colRunProfileID: "ppf_A", colRunProfileDriver: "claude",
			colRunInputTokens: int64(500), colRunOutputTokens: int64(100)},
		// reported a free turn: a zero that IS a value
		{colRunUsageModelRef: "claude-opus-5-5", colRunProfileID: "ppf_A", colRunProfileDriver: "claude",
			colRunInputTokens: int64(0), colRunOutputTokens: int64(0), colRunCostMicroUSD: int64(0)},
		// nothing reported
		{colRunUsageModelRef: "gpt-6.1", colRunProfileID: "ppf_B", colRunProfileDriver: "codex"},
		// legacy: the model the launch asked for, no profile, no driver (a Claude run)
		{colRunModelRef: "legacy-model"},
	})
	sum := func(v int64) any { return float64(v) }

	t.Run("model", func(t *testing.T) {
		rows := usageRows(t, h, admin, tenant, "group_by=model")
		if len(rows) != 3 {
			t.Fatalf("rows = %v, want 3", rows)
		}
		wantUsage(t, rows[0], "claude-opus-5-5", 3, sum(1500), sum(300), sum(5000), 0, 1)
		wantUsage(t, rows[1], "gpt-6.1", 1, nil, nil, nil, 1, 1)
		wantUsage(t, rows[2], "legacy-model", 1, nil, nil, nil, 1, 1)
	})
	t.Run("tool", func(t *testing.T) {
		rows := usageRows(t, h, admin, tenant, "group_by=tool")
		if len(rows) != 2 {
			t.Fatalf("rows = %v, want 2", rows)
		}
		wantUsage(t, rows[0], "claude", 4, sum(1500), sum(300), sum(5000), 1, 2)
		wantUsage(t, rows[1], "codex", 1, nil, nil, nil, 1, 1)
	})
	t.Run("provider", func(t *testing.T) {
		rows := usageRows(t, h, admin, tenant, "group_by=provider")
		if len(rows) != 2 {
			t.Fatalf("rows = %v, want 2", rows)
		}
		wantUsage(t, rows[0], "anthropic", 4, sum(1500), sum(300), sum(5000), 1, 2)
		wantUsage(t, rows[1], "openai", 1, nil, nil, nil, 1, 1)
	})
	t.Run("account", func(t *testing.T) {
		rows := usageRows(t, h, admin, tenant, "group_by=account")
		if len(rows) != 3 {
			t.Fatalf("rows = %v, want 3", rows)
		}
		wantUsage(t, rows[0], "ppf_A", 3, sum(1500), sum(300), sum(5000), 0, 1)
		// equal run counts sort by key, so the run with no account comes before ppf_B
		wantUsage(t, rows[1], "", 1, nil, nil, nil, 1, 1)
		wantUsage(t, rows[2], "ppf_B", 1, nil, nil, nil, 1, 1)
	})
	t.Run("window", func(t *testing.T) {
		now := time.Now().UTC()
		q := func(since, until time.Time) string {
			v := url.Values{"group_by": {"tool"}, "since": {since.Format(time.RFC3339)}, "until": {until.Format(time.RFC3339)}}
			return v.Encode()
		}
		if rows := usageRows(t, h, admin, tenant, q(now.Add(-time.Hour), now.Add(time.Hour))); len(rows) != 2 {
			t.Errorf("window around now = %v, want both tools", rows)
		}
		if rows := usageRows(t, h, admin, tenant, q(now.Add(time.Hour), now.Add(2*time.Hour))); len(rows) != 0 {
			t.Errorf("window in the future = %v, want none", rows)
		}
		if rows := usageRows(t, h, admin, tenant, q(now.Add(-2*time.Hour), now.Add(-time.Hour))); len(rows) != 0 {
			t.Errorf("window in the past = %v, want none", rows)
		}
	})
	t.Run("bucket day", func(t *testing.T) {
		rows := usageRows(t, h, admin, tenant, "group_by=tool&bucket=day")
		today := time.Now().UTC().Format("2006-01-02")
		if len(rows) != 2 || rows[0]["day"] != today || rows[1]["day"] != today {
			t.Errorf("rows = %v, want one row per tool on %s", rows, today)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		for _, bad := range []string{
			"", "group_by=colour", "group_by=model&bucket=hour", "group_by=model&since=yesterday",
			"group_by=model&until=soon",
			"group_by=model&since=2026-11-02T00:00:00Z&until=2026-11-01T00:00:00Z",
		} {
			if r := h.do("GET", "/v1/m/sessions/usage?"+bad, admin, tenantHdr(tenant)); r.code != http.StatusBadRequest {
				t.Errorf("usage?%s = %d, want 400", bad, r.code)
			}
		}
	})
}

// TestRunUsage_ProviderIsTheKindOfTheRecordTheRunUsed: a run on a stored provider record is
// grouped under that record's kind, not under the vendor of its tool's own login; a run whose
// record is gone has no provider rather than a guessed one.
func TestRunUsage_ProviderIsTheKindOfTheRecordTheRunUsed(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()), WithProviderSecretVault(newFakeVault()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.doJSON("POST", "/v1/m/sessions/providers", admin, map[string]any{
		"kind": "openai", "display_name": "Coding", "api_key": testProviderKey,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create provider = %d %s", r.code, r.raw)
	}
	ref, kind := r.body["provider_ref"].(string), r.body["kind"].(string)
	seedUsageRuns(t, h, tenant, []model.Record{
		{colRunProfileDriver: "opencode", colRunProviderRecordRef: ref},
		{colRunProfileDriver: "opencode", colRunProviderRecordRef: providerRecordRefPrefix + model.NewID().String()},
		{colRunProfileDriver: "claude"},
	})
	rows := usageRows(t, h, admin, tenant, "group_by=provider")
	got := map[string]float64{}
	for _, row := range rows {
		key, _ := row["key"].(string)
		got[key] = row["runs"].(float64)
	}
	if len(rows) != 3 || got[kind] != 1 || got["anthropic"] != 1 || got[""] != 1 {
		t.Errorf("rows = %v, want one run each under %q (the record), anthropic (own login) and no provider (record gone)", rows, kind)
	}
}

// TestRunUsage_ReadsOnlyTheRunsTheCallerMayRead: a person confined to one workspace
// totals that workspace's runs and nothing else; the tenant-wide reader totals every run.
func TestRunUsage_ReadsOnlyTheRunsTheCallerMayRead(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	var workspaceA, workspaceB model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, target := range []*model.ID{&workspaceA, &workspaceB} {
			id := model.NewID()
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: id.String(), Slug: id.String(), Status: model.StatusActive})
			if err != nil {
				return err
			}
			*target = ws.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	member := func(email, role string, workspace model.ID) string {
		t.Helper()
		body := map[string]any{"email": email, "password": "usage-member-pass1", "tenant": tenant.String(), "role": role}
		if !workspace.IsZero() {
			body["workspace_id"] = workspace.String()
		}
		if r := h.doJSON("POST", "/v1/users", admin, body, nil); r.code != http.StatusCreated {
			t.Fatalf("member = %d %s", r.code, r.raw)
		}
		r := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "usage-member-pass1"}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login = %d %s", r.code, r.raw)
		}
		return r.body["token"].(string)
	}
	confined := member("confined-usage@test.io", auth.RoleAdmin, workspaceA)
	everyone := member("tenant-usage@test.io", auth.RoleViewer, "")
	run := func(workspace model.ID) model.Record {
		rec := model.Record{colRunUsageModelRef: "m", colRunCostMicroUSD: int64(10)}
		if !workspace.IsZero() {
			rec[colRunAuthzWorkspaceID] = workspace.String()
		}
		return rec
	}
	// workspace A twice, workspace B once, and one run with no lawful lineage (hidden from a confined reader)
	seedUsageRuns(t, h, tenant, []model.Record{run(workspaceA), run(workspaceA), run(workspaceB), run("")})

	if rows := usageRows(t, h, confined, tenant, "group_by=model"); len(rows) != 1 || rows[0]["runs"] != float64(2) || rows[0]["cost_micro_usd"] != float64(20) {
		t.Errorf("confined to workspace A = %v, want its 2 runs and 20", rows)
	}
	if rows := usageRows(t, h, everyone, tenant, "group_by=model"); len(rows) != 1 || rows[0]["runs"] != float64(4) || rows[0]["cost_micro_usd"] != float64(40) {
		t.Errorf("tenant-wide reader = %v, want all 4 runs and 40", rows)
	}
}

// TestRunUsage_ThousandRunsWithinTheBound is the stated bound: 1000 runs, grouped by model
// and day, answer within runUsageBound on the in-memory SQLite store of the test harness.
func TestRunUsage_ThousandRunsWithinTheBound(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	const runs = 1000
	recs := make([]model.Record, 0, runs)
	for i := 0; i < runs; i++ {
		recs = append(recs, model.Record{
			colRunUsageModelRef: fmt.Sprintf("model-%d", i%10), colRunProfileID: fmt.Sprintf("ppf_%d", i%4), colRunProfileDriver: "claude",
			colRunInputTokens: int64(100), colRunOutputTokens: int64(10), colRunCostMicroUSD: int64(7),
		})
	}
	seedUsageRuns(t, h, tenant, recs)
	const runUsageBound = 3 * time.Second
	start := time.Now()
	rows := usageRows(t, h, admin, tenant, "group_by=model&bucket=day")
	took := time.Since(start)
	if len(rows) != 10 {
		t.Fatalf("rows = %d, want 10 models", len(rows))
	}
	for _, row := range rows {
		if row["runs"] != float64(100) || row["cost_micro_usd"] != float64(700) {
			t.Fatalf("row = %v, want 100 runs and 700", row)
		}
	}
	t.Logf("%d runs grouped by model and day in %s (bound %s)", runs, took, runUsageBound)
	if took > runUsageBound {
		t.Errorf("took %s, want within %s", took, runUsageBound)
	}
}

// seedUsageRuns stores one run per record, filling the columns every run row needs.
func seedUsageRuns(t *testing.T, h *harness, tenant model.TenantID, recs []model.Record) {
	t.Helper()
	if err := h.m.Data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for i, rec := range recs {
			row := model.Record{
				colRunRef: fmt.Sprintf("run-usage-%04d", i), colTransport: "stream-json", colPermissionMode: "default",
				colIsolation: "native", colState: stateStopped, colLastEventSeq: int64(0),
			}
			for k, v := range rec {
				row[k] = v
			}
			if _, err := repo.Create(context.Background(), row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
