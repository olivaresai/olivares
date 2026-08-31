// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	sessionsmod "github.com/olivaresai/olivares/modules/sessions"
)

const (
	testWorkItemID   = "01989d7d-32ac-7bb0-878d-3254f349a102"
	testDependencyID = "01989d7d-39dc-719d-985a-2298ccf7b925"
	testCriterionID  = "01989d7d-3d34-731a-a9c6-280d32922285"
	testDecisionID   = "01989d7d-4221-7429-ac66-d118af429159"
)

func TestWorkCommandRoutesCoverDurableMutations(t *testing.T) {
	doc := map[string]any{
		"work_item_id": testWorkItemID,
		"target_id":    testDependencyID,
		"criterion_id": testCriterionID,
		"decision_id":  testDecisionID,
	}
	want := map[string]struct {
		method, path string
		version      bool
	}{
		"item.create":         {http.MethodPost, "/work-items", false},
		"item.update":         {http.MethodPatch, "/work-items/" + testWorkItemID, true},
		"item.ready":          {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.block":          {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.unblock":        {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.submit":         {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.complete":       {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.fail":           {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.cancel":         {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.archive":        {http.MethodPost, "/work-items/" + testWorkItemID + "/transitions", true},
		"item.assign":         {http.MethodPost, "/work-items/" + testWorkItemID + "/assignments", true},
		"dependency.add":      {http.MethodPost, "/work-items/" + testWorkItemID + "/dependencies", true},
		"dependency.remove":   {http.MethodDelete, "/work-items/" + testWorkItemID + "/dependencies/" + testDependencyID, true},
		"acceptance.add":      {http.MethodPost, "/work-items/" + testWorkItemID + "/acceptance", true},
		"acceptance.update":   {http.MethodPatch, "/work-items/" + testWorkItemID + "/acceptance/" + testCriterionID, true},
		"acceptance.evaluate": {http.MethodPatch, "/work-items/" + testWorkItemID + "/acceptance/" + testCriterionID, true},
		"decision.set":        {http.MethodPost, "/decisions", true},
		"decision.supersede":  {http.MethodPost, "/decisions", true},
		"decision.revoke":     {http.MethodPost, "/decisions/" + testDecisionID + "/revoke", true},
		"lease.acquire":       {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/acquire", true},
		"lease.renew":         {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/renew", true},
		"lease.release":       {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/release", true},
		"lease.takeover":      {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/takeover", true},
		"lease.revoke":        {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/revoke", true},
		"lease.clock_rebase":  {http.MethodPost, "/work-items/" + testWorkItemID + "/lease/clock-rebase", true},
	}
	if len(workCommandRoutes) != len(want) {
		t.Fatalf("route count = %d, want %d", len(workCommandRoutes), len(want))
	}
	for command, expected := range want {
		route, ok := workCommandRoutes[command]
		if !ok {
			t.Errorf("missing route for %s", command)
			continue
		}
		path, err := route.path(doc)
		if err != nil {
			t.Errorf("%s path: %v", command, err)
			continue
		}
		if route.method != expected.method || path != expected.path || route.requiresVersion != expected.version {
			t.Errorf("%s = %s %s version=%v, want %s %s version=%v", command,
				route.method, path, route.requiresVersion, expected.method, expected.path, expected.version)
		}
	}
}

func TestWorkDocumentFlagsMatchExportedWorkCommandDTO(t *testing.T) {
	typ := reflect.TypeOf(sessionsmod.WorkCommand{})
	known := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	for flag, wireName := range workDocumentStringFlags {
		if !known[wireName] {
			t.Errorf("--%s emits unknown WorkCommand JSON field %q", flag, wireName)
		}
	}
	for flag, wireName := range workDocumentInt64Flags {
		if !known[wireName] {
			t.Errorf("--%s emits unknown WorkCommand JSON field %q", flag, wireName)
		}
	}
	for flag, wireName := range workDocumentBoolFlags {
		if !known[wireName] {
			t.Errorf("--%s emits unknown WorkCommand JSON field %q", flag, wireName)
		}
	}
}

func TestWorkLeaseApplySendsTypedFieldsAndNestedRoute(t *testing.T) {
	const holderSID = "osn_01989d7d-42c0-7e81-83b4-e2222e647fa5"
	var captured struct {
		path string
		body map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&captured.body)
		_, _ = w.Write([]byte(`{"verdict":"LIMPIO","version":4}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"apply", "lease.acquire", "--work-item-id", testWorkItemID,
		"--holder-sid", holderSID, "--holder-run-ref", "run-7", "--holder-agent-ref", "agent-9",
		"--ttl-seconds", "300", "--fence", "6", "--force", "--unblock", "--changes-requested",
		"--version", "3", "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("lease acquire: %v", err)
	}
	if want := workAPIBase + "/work-items/" + testWorkItemID + "/lease/acquire"; captured.path != want {
		t.Fatalf("path = %q, want %q", captured.path, want)
	}
	for key, want := range map[string]any{
		"command": "lease.acquire", "holder_sid": holderSID, "holder_run_ref": "run-7",
		"holder_agent_ref": "agent-9", "ttl_seconds": float64(300), "fence": float64(6),
		"force": true, "unblock": true, "changes_requested": true,
	} {
		if got := captured.body[key]; got != want {
			t.Errorf("body[%s] = %#v, want %#v (body=%#v)", key, got, want, captured.body)
		}
	}
}

func TestWorkReplayEventUsesStableAdminRoute(t *testing.T) {
	var captured struct {
		method, path, mode, key, etag, planHash string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method, captured.path, captured.mode = r.Method, r.URL.Path, r.URL.Query().Get("mode")
		captured.key = r.Header.Get("Idempotency-Key")
		captured.etag = r.Header.Get("If-Match")
		captured.planHash = r.Header.Get("If-Plan-Hash")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"verdict":"LIMPIO","code":"requeued","event_id":"` + testDecisionID + `","state":"pending","attempts":10}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"replay", "event", testDecisionID,
		"--mode", "apply", "--version", "7", "--plan-hash", strings.Repeat("a", 64),
		"--idempotency-key", testWorkItemID,
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("replay event: %v", err)
	}
	wantPath := workAPIBase + "/work-events/" + testDecisionID + "/replay"
	if captured.method != http.MethodPost || captured.path != wantPath || captured.mode != "apply" ||
		captured.key != testWorkItemID || captured.etag != `"v7"` ||
		captured.planHash != strings.Repeat("a", 64) {
		t.Fatalf("replay request = %#v, want POST %s with apply envelope", captured, wantPath)
	}
	if !strings.Contains(stdout.String(), `"event_id": "`+testDecisionID+`"`) {
		t.Fatalf("replay output = %q", stdout.String())
	}
}

func TestWorkReplayEventRejectsInvalidIDWithoutFiring(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetArgs([]string{
		"replay", "event", "not-a-uuid",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := cmd.Execute(); err == nil || hits.Load() != 0 {
		t.Fatalf("invalid event id = %v, hits=%d", err, hits.Load())
	}
}

func TestWorkReplayEventPlanNeedsNoMutationHeaders(t *testing.T) {
	var captured struct {
		mode, key, etag, planHash string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.mode = r.URL.Query().Get("mode")
		captured.key = r.Header.Get("Idempotency-Key")
		captured.etag = r.Header.Get("If-Match")
		captured.planHash = r.Header.Get("If-Plan-Hash")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"verdict":"LIMPIO","code":"ok","plan_hash":"` +
			strings.Repeat("b", 64) + `","command":"outbox.replay","expected_etag":"\"v3\""}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"replay", "event", testDecisionID, "--mode", "plan",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("replay plan: %v", err)
	}
	if captured.mode != "plan" || captured.key != "" || captured.etag != "" || captured.planHash != "" {
		t.Fatalf("replay plan headers = %#v", captured)
	}
}

func TestWorkReplayEventApplyEnvelopeFailsBeforeFiring(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing plan hash", args: []string{"--version", "3"}},
		{name: "missing version", args: []string{"--plan-hash", strings.Repeat("a", 64)}},
		{name: "bad key", args: []string{
			"--version", "3", "--plan-hash", strings.Repeat("a", 64), "--idempotency-key", "not-a-uuid",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newWorkCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{
				"replay", "event", testDecisionID,
				"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
			}
			cmd.SetArgs(append(args, tc.args...))
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid replay apply envelope succeeded")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid replay envelopes fired %d requests", hits.Load())
	}
}

func TestWorkApplySendsFenceHeadersAndDerivedRoute(t *testing.T) {
	var captured struct {
		method, path, mode, key, etag, auth, tenant, stderr string
		body                                                map[string]any
	}
	var stderr bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method, captured.path = r.Method, r.URL.Path
		captured.mode = r.URL.Query().Get("mode")
		captured.key = r.Header.Get("Idempotency-Key")
		captured.etag = r.Header.Get("If-Match")
		captured.auth = r.Header.Get("Authorization")
		captured.tenant = r.Header.Get("X-Olivares-Tenant")
		captured.stderr = stderr.String()
		_ = json.NewDecoder(r.Body).Decode(&captured.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + testWorkItemID + `","version":4}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"apply", "item.update", "--work-item-id", testWorkItemID, "--title", "durable kernel",
		"--version", "3", "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if captured.method != http.MethodPatch || captured.path != workAPIBase+"/work-items/"+testWorkItemID || captured.mode != "apply" {
		t.Fatalf("request = %s %s mode=%q", captured.method, captured.path, captured.mode)
	}
	if captured.etag != `"v3"` {
		t.Fatalf("If-Match = %q, want strong v3", captured.etag)
	}
	if captured.key == "" || !strings.Contains(captured.stderr, captured.key) {
		t.Fatalf("generated key %q was not printed before transmit: %q", captured.key, captured.stderr)
	}
	if captured.auth != "Bearer tok" || captured.tenant != "tenant-a" {
		t.Fatalf("auth headers = %q tenant=%q", captured.auth, captured.tenant)
	}
	if captured.body["command"] != "item.update" || captured.body["work_item_id"] != testWorkItemID || captured.body["title"] != "durable kernel" {
		t.Fatalf("body = %#v", captured.body)
	}
	if !strings.Contains(stdout.String(), `"version": 4`) {
		t.Fatalf("JSON output did not preserve response: %q", stdout.String())
	}
}

func TestWorkApplyMissingVersionDoesNotFire(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"id":"unexpected"}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetArgs([]string{
		"apply", "item.update", "--work-item-id", testWorkItemID,
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	err := cmd.Execute()
	if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "requires --version") {
		t.Fatalf("error = %v (code %d), want local version refusal", err, exitcode.From(err))
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("missing version fired %d HTTP request(s)", got)
	}
}

func TestWorkPlanArtifactReplaysHashAndETag(t *testing.T) {
	var calls atomic.Int32
	var applyHeader, applyHash string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			if got := r.URL.Query().Get("mode"); got != "plan" {
				t.Errorf("first mode = %q, want plan", got)
			}
			w.Header().Set("ETag", `"v3"`)
			_, _ = w.Write([]byte(`{"verdict":"LIMPIO","plan_hash":"abc123","checks":[]}`))
		case 2:
			applyHeader = r.Header.Get("If-Match")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			applyHash, _ = body["plan_hash"].(string)
			if got := r.URL.Query().Get("mode"); got != "apply" {
				t.Errorf("second mode = %q, want apply", got)
			}
			_, _ = w.Write([]byte(`{"id":"` + testWorkItemID + `","version":4}`))
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer srv.Close()

	planPath := filepath.Join(t.TempDir(), "plan.json")
	plan := newWorkCmd()
	plan.SetOut(&bytes.Buffer{})
	plan.SetErr(&bytes.Buffer{})
	plan.SetArgs([]string{
		"plan", "item.update", "--work-item-id", testWorkItemID, "--title", "planned",
		"--version", "3", "--out", planPath, "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := plan.Execute(); err != nil {
		t.Fatalf("plan: %v", err)
	}
	info, err := os.Stat(planPath)
	if err != nil {
		t.Fatalf("stat plan: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("plan mode = %04o, want 0600", got)
	}

	apply := newWorkCmd()
	apply.SetOut(&bytes.Buffer{})
	apply.SetErr(&bytes.Buffer{})
	apply.SetArgs([]string{
		"apply", "item.update", "--plan", planPath,
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := apply.Execute(); err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	if calls.Load() != 2 || applyHeader != `"v3"` || applyHash != "abc123" {
		t.Fatalf("replay calls=%d If-Match=%q plan_hash=%q", calls.Load(), applyHeader, applyHash)
	}
}

func TestWorkDocumentRejectsCommandMismatchAndMultipleDocuments(t *testing.T) {
	for name, content := range map[string]struct {
		content string
		want    string
	}{
		"mismatch": {"command: item.create\nwork_item_id: " + testWorkItemID + "\n", "does not match"},
		"multiple": {"title: one\n---\ntitle: two\n", "multiple YAML documents"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "command.yaml")
			if err := os.WriteFile(path, []byte(content.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := newWorkCmd()
			cmd.SetArgs([]string{"validate", "item.update", "-f", path})
			err := cmd.Execute()
			if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), content.want) {
				t.Fatalf("error = %v (code %d), want %q", err, exitcode.From(err), content.want)
			}
		})
	}
}

func TestWorkGetLeaseUsesWorkItemNestedRoute(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"` + testDependencyID + `","work_item_id":"` + testWorkItemID + `","state":"active","fence":9223372036854775807}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"get", "lease", testWorkItemID,
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if want := workAPIBase + "/work-items/" + testWorkItemID + "/lease"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if got := out.String(); !strings.Contains(got, `"fence": 9223372036854775807`) {
		t.Fatalf("lease JSON rounded fencing token: %q", got)
	}
}

func TestWorkListUsesAllowlistedEncodedFilters(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
			http.Error(w, "missing CLI authentication context", http.StatusUnauthorized)
			return
		}
		rawQuery = r.URL.RawQuery
		if r.URL.Path != workAPIBase+"/work-items" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[],"cursor":"","has_more":false}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"list", "items", "--status", "ready", "--owner-ref", "agent/a+b", "--archived=false", "--limit", "2",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"status=ready", "owner_ref=agent%2Fa%2Bb", "archived=false", "limit=2"} {
		if !strings.Contains(rawQuery, want) {
			t.Errorf("query %q lacks %q", rawQuery, want)
		}
	}
	if !strings.Contains(out.String(), `"has_more": false`) {
		t.Fatalf("list JSON = %q", out.String())
	}
}

func TestWorkDecisionListUsesAPIDeciderFilterNames(t *testing.T) {
	var gotValues url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotValues = r.URL.Query()
		if r.URL.Path != workAPIBase+"/decisions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":"","has_more":false}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"list", "decisions", "--work-item-id", testWorkItemID,
		"--actor-kind", "agent", "--actor-ref", "agent/a",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	if gotValues.Get("decided_by_kind") != "agent" || gotValues.Get("decided_by_ref") != "agent/a" {
		t.Fatalf("decision filters = %#v", gotValues)
	}
	if gotValues.Has("actor_kind") || gotValues.Has("actor_ref") {
		t.Fatalf("client emitted non-API actor filter names: %#v", gotValues)
	}
}

func TestWorkLeaseListUsesLeaseAllowlistAndRendersFence(t *testing.T) {
	var gotValues url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotValues = r.URL.Query()
		if r.URL.Path != workAPIBase+"/leases" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"` + testDependencyID + `","state":"active","holder_sid":"osn_holder","fence":9223372036854775807,"expires_at":"2026-08-10T12:00:00Z"}],"next_cursor":"","has_more":false}`))
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"list", "leases", "--work-item-id", testWorkItemID, "--holder-sid", "osn_holder",
		"--state", "active", "--expires-before", "2026-08-11T00:00:00Z",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list leases: %v", err)
	}
	for key, want := range map[string]string{
		"work_item_id": testWorkItemID, "holder_sid": "osn_holder", "state": "active",
		"expires_before": "2026-08-11T00:00:00Z",
	} {
		if got := gotValues.Get(key); got != want {
			t.Errorf("query[%s] = %q, want %q (%#v)", key, got, want, gotValues)
		}
	}
	if got := out.String(); !strings.Contains(got, "\tactive\tosn_holder\t9223372036854775807\t2026-08-10T12:00:00Z\n") {
		t.Fatalf("lease list render = %q", got)
	}
}

func TestWorkDecisionStateFiltersAuthenticateAndRenderHeadState(t *testing.T) {
	var requests []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
			http.Error(w, "missing CLI authentication context", http.StatusUnauthorized)
			return
		}
		requests = append(requests, r.URL.Query())
		if r.URL.Query().Has("effective") {
			_, _ = w.Write([]byte(`{"items":[{"id":"` + testDecisionID + `","operation":"set","decision_key":"scope","state":"effective"}],"next_cursor":"","has_more":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"` + testDecisionID + `","operation":"set","decision_key":"scope"}],"next_cursor":"","has_more":false}`))
	}))
	defer srv.Close()

	headCmd := newWorkCmd()
	var headOut bytes.Buffer
	headCmd.SetOut(&headOut)
	headCmd.SetArgs([]string{
		"list", "decisions", "--effective=true", "--revoked=false",
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := headCmd.Execute(); err != nil {
		t.Fatalf("list effective decisions: %v", err)
	}
	if len(requests) != 1 || requests[0].Get("effective") != "true" || requests[0].Get("revoked") != "false" {
		t.Fatalf("head filter query = %#v", requests)
	}
	if got := headOut.String(); !strings.Contains(got, "\teffective\n") || strings.Contains(got, "\thistory\n") {
		t.Fatalf("effective render = %q", got)
	}

	historyCmd := newWorkCmd()
	var historyOut bytes.Buffer
	historyCmd.SetOut(&historyOut)
	historyCmd.SetArgs([]string{
		"list", "decisions", "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := historyCmd.Execute(); err != nil {
		t.Fatalf("list decision history: %v", err)
	}
	if got := historyOut.String(); !strings.Contains(got, "\thistory\n") {
		t.Fatalf("history render = %q", got)
	}
}

func TestWorkDecisionListStateDistinguishesHeadFromHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]any
		want string
	}{
		{name: "effective head", row: map[string]any{"state": "effective"}, want: "effective"},
		{name: "revoked head", row: map[string]any{"state": "revoked"}, want: "revoked"},
		{name: "append-only history", row: map[string]any{}, want: "history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workDecisionListState(tc.row); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkWatchResumesDurableSSEWithoutDeadline(t *testing.T) {
	var gotQuery, gotLastID, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("cursor")
		gotLastID = r.Header.Get("Last-Event-ID")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, ": heartbeat\n\nid: "+testDependencyID+"\nevent: work\ndata: {\"event_type\":\"item.updated\",\ndata: \"seq\":2}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"watch", "--cursor", testWorkItemID,
		"--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	if err := cmd.Execute(); err == nil || exitcode.From(err) != exitcode.Indeterminate {
		t.Fatalf("watch EOF = %v (code %d), want indeterminate after emitted event", err, exitcode.From(err))
	}
	if gotQuery != testWorkItemID || gotLastID != testWorkItemID || gotAccept != "text/event-stream" {
		t.Fatalf("cursor query=%q header=%q accept=%q", gotQuery, gotLastID, gotAccept)
	}
	if want := testDependencyID + "\twork\t"; !strings.Contains(out.String(), want) || strings.Contains(out.String(), "heartbeat") {
		t.Fatalf("watch output = %q, want durable frame and no heartbeat", out.String())
	}
}

func TestWorkWatchSurfacesServerObservationFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: olivares.error\ndata: {\"verdict\":\"NO_HE_PODIDO_MIRAR\",\"code\":\"observation_unavailable\"}\n\n")
	}))
	defer srv.Close()

	cmd := newWorkCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"watch", "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a",
	})
	err := cmd.Execute()
	if err == nil || exitcode.From(err) != exitcode.Indeterminate ||
		!strings.Contains(err.Error(), "observation_unavailable") {
		t.Fatalf("server SSE failure = %v (code %d), want observation indeterminate", err, exitcode.From(err))
	}
}

func TestWorkVerdictsUseStableGlobalExitContract(t *testing.T) {
	for verdict, want := range map[string]int{
		"LIMPIO":             exitcode.OK,
		"ROTO":               exitcode.Degraded,
		"NO_HE_PODIDO_MIRAR": exitcode.Indeterminate,
	} {
		t.Run(verdict, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetOut(&bytes.Buffer{})
			err := renderWorkResponse(cmd, []byte(`{"verdict":"`+verdict+`","checks":[]}`))
			got := exitcode.OK
			if err != nil {
				got = exitcode.From(err)
			}
			if got != want {
				t.Fatalf("verdict %s exit = %d, want %d (err=%v)", verdict, got, want, err)
			}
		})
	}
}

func TestWorkClockRollbackUsesIndeterminateExit(t *testing.T) {
	err := workHTTPError(http.StatusServiceUnavailable,
		[]byte(`{"verdict":"NO_HE_PODIDO_MIRAR","error":{"code":"clock_rollback"}}`))
	if err == nil || exitcode.From(err) != exitcode.Indeterminate {
		t.Fatalf("clock rollback = %v (code %d), want indeterminate", err, exitcode.From(err))
	}
}

func TestWorkResponseTextPreservesMaxFence(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := renderWorkResponse(cmd, []byte(`{"verdict":"LIMPIO","fence":9223372036854775807}`)); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "9223372036854775807") {
		t.Fatalf("text output rounded fencing token: %q", got)
	}
}
