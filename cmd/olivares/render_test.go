// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestGlobalOutputRejectsInvalidValueAsUsage(t *testing.T) {
	_, _, err := execRoot(t, "version", "-o", "yaml")
	if err == nil {
		t.Fatal("invalid -o value must error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d (usage): %v", got, exitcode.Usage, err)
	}
}

func TestFormatAliasMatchesGlobalOutput(t *testing.T) {
	globalOut, globalErr, err := execRoot(t, "config", "effective", "-o", "json")
	if err != nil {
		t.Fatalf("config effective -o json: %v", err)
	}
	if globalErr != "" {
		t.Fatalf("config effective -o json stderr = %q, want empty", globalErr)
	}

	aliasOut, aliasErr, err := execRoot(t, "config", "effective", "--format", "json")
	if err != nil {
		t.Fatalf("config effective --format json: %v", err)
	}
	if aliasErr != "--format "+deprecationWarningFor("format")+"\n" {
		t.Fatalf("deprecated alias warning = %q", aliasErr)
	}
	assertSameJSON(t, globalOut, aliasOut)

	var values map[string]string
	if err := json.Unmarshal([]byte(globalOut), &values); err != nil {
		t.Fatalf("config effective JSON shape is not an object of strings: %v\n%s", err, globalOut)
	}
}

func TestJSONAliasMatchesGlobalOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/sessions/runs" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"run_ref":"run-1","state":"running","transport":"stream-json","name":"demo"}],"next_cursor":"cursor-2"}`))
	}))
	defer srv.Close()
	t.Setenv("OLIVARES_SERVER_URL", srv.URL)
	t.Setenv("OLIVARES_TOKEN", "token")
	t.Setenv("OLIVARES_TENANT", "11111111-1111-1111-1111-111111111111")

	globalOut, globalErr, err := execRoot(t, "agent", "session", "ls", "-o", "json")
	if err != nil {
		t.Fatalf("agent session ls -o json: %v", err)
	}
	if globalErr != "" {
		t.Fatalf("agent session ls -o json stderr = %q, want empty", globalErr)
	}

	aliasOut, aliasErr, err := execRoot(t, "agent", "session", "ls", "--json")
	if err != nil {
		t.Fatalf("agent session ls --json: %v", err)
	}
	if aliasErr != "--json "+deprecationWarningFor("json")+"\n" {
		t.Fatalf("deprecated alias warning = %q", aliasErr)
	}
	assertSameJSON(t, globalOut, aliasOut)

	var response struct {
		Items []struct {
			RunRef string `json:"run_ref"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(globalOut), &response); err != nil {
		t.Fatalf("agent JSON is invalid: %v\n%s", err, globalOut)
	}
	if len(response.Items) != 1 || response.Items[0].RunRef != "run-1" || response.NextCursor != "cursor-2" {
		t.Fatalf("agent API shape changed: %#v", response)
	}
}

func TestStatusGlobalOutputPreservesRawAPIShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"operational","components":[],"timestamp":"2026-07-20T00:00:00Z","future_field":{"kept":true}}`))
	}))
	defer srv.Close()

	globalOut, globalErr, err := execRoot(t, "status", "--server", srv.URL, "-o", "json")
	if err != nil {
		t.Fatalf("status -o json: %v", err)
	}
	if globalErr != "" {
		t.Fatalf("status -o json stderr = %q, want empty", globalErr)
	}

	aliasOut, aliasErr, err := execRoot(t, "status", "--server", srv.URL, "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if aliasErr != "--json "+deprecationWarningFor("json")+"\n" {
		t.Fatalf("deprecated alias warning = %q", aliasErr)
	}
	assertSameJSON(t, globalOut, aliasOut)

	var response map[string]any
	if err := json.Unmarshal([]byte(globalOut), &response); err != nil {
		t.Fatalf("status JSON is invalid: %v\n%s", err, globalOut)
	}
	future, ok := response["future_field"].(map[string]any)
	if !ok || future["kept"] != true {
		t.Fatalf("status did not preserve the raw API shape: %#v", response)
	}
}

// The degraded exit contract must hold in JSON mode too: the raw report prints
// and the process still exits Degraded, silently.
func TestStatusJSONOutputKeepsDegradedExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"degraded","components":[]}`))
	}))
	defer srv.Close()

	out, _, err := execRoot(t, "status", "--server", srv.URL, "-o", "json")
	if exitcode.From(err) != exitcode.Degraded || !exitcode.Silent(err) {
		t.Fatalf("status -o json on degraded engine: want silent degraded exit, got %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(out), &response); err != nil || response["status"] != "degraded" {
		t.Fatalf("degraded JSON report missing/invalid: %v\n%s", err, out)
	}
}

func TestVersionGlobalOutputJSONShape(t *testing.T) {
	out, stderr, err := execRoot(t, "version", "-o", "json")
	if err != nil {
		t.Fatalf("version -o json: %v", err)
	}
	if stderr != "" {
		t.Fatalf("version -o json stderr = %q, want empty", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("version JSON is invalid: %v\n%s", err, out)
	}
	gotKeys := make([]string, 0, len(got))
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{"arch", "commit", "date", "fips", "go", "license_key", "module", "os", "ota_key", "version"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("version JSON keys = %v, want %v", gotKeys, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := got[key].(string); !ok {
			t.Fatalf("version JSON field %q is not a string: %#v", key, got[key])
		}
	}
}

func assertSameJSON(t *testing.T, want, got string) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("reference JSON is invalid: %v\n%s", err, want)
	}
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("alias JSON is invalid: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON differs:\nglobal: %s\nalias: %s", want, got)
	}
}

// THE TEXT FORM MUST NOT LOSE A FIELD THE JSON FORM CARRIES (2026-08-05).
// writeStatusLines had an explicit empty branch for a slice — it prints `(none)` —
// and none for a map: the loop ran zero times and the field simply VANISHED from
// -o text while -o json still reported it. The two outputs of one command then
// disagreed about which fields exist, and the text reader concluded the engine had
// never reported something it did report, as empty.
func TestWriteStatusLinesKeepsEmptyContainers(t *testing.T) {
	var buf strings.Builder
	writeStatusLines(&buf, "", map[string]any{
		"by_kind":   map[string]any{},
		"by_source": map[string]any{"mcp": float64(2)},
		"tags":      []any{},
		"total":     float64(0),
	})
	got := buf.String()
	for _, want := range []string{
		"by_kind\t{}",      // the empty map is REPORTED, not dropped
		"tags\t(none)",     // the pre-existing empty-slice behavior is unchanged
		"by_source.mcp\t2", // a populated map still walks
		"total\t0",         // and a zero scalar is not an empty container
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output lacks %q:\n%s", want, got)
		}
	}
	// The discriminating half: the key must appear AT ALL. A field that is present
	// in JSON and absent from text is the defect, not a formatting preference.
	if !strings.Contains(got, "by_kind") {
		t.Errorf("an empty map disappeared from the text form entirely:\n%s", got)
	}
}
