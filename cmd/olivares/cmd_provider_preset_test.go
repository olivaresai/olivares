// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// `--preset <ref>` takes kind, endpoint and name from the catalog row, so the key's
// prefix (an `sk-` key is OpenAI's, not DeepSeek's) decides nothing.
func TestProviderAddPresetTakesKindAndNameFromTheCatalog(t *testing.T) {
	for _, row := range modelprovider.Presets() {
		t.Run(row.Ref, func(t *testing.T) {
			p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
			_, errb, err := execRootStdin(t, "sk-fixture-preset-key\n", "provider", "add", "--preset", row.Ref, "--no-test",
				"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
			if err != nil {
				t.Fatalf("add --preset %s: %v %s", row.Ref, err, errb)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(p.lastBody()), &body); err != nil {
				t.Fatal(err)
			}
			if body["kind"] != row.Kind || body["service"] != row.Ref || body["display_name"] != row.Title || body["api_key"] != "sk-fixture-preset-key" {
				t.Fatalf("body %v does not follow row %+v", body, row)
			}
			if base, _ := body["base_url"].(string); base != "" {
				t.Fatalf("base_url %q typed by the CLI: the engine takes it from the row", base)
			}
			if p.calls.Load() != 1 {
				t.Fatalf("calls = %d, want the one add", p.calls.Load())
			}
		})
	}
}

func TestProviderAddPresetKeepsAnExplicitName(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	if _, errb, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", "deepseek-anthropic", "--name", "DS prod", "--no-test",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("%v %s", err, errb)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(p.lastBody()), &body)
	if body["display_name"] != "DS prod" {
		t.Fatalf("name = %v, want the one given", body["display_name"])
	}
}

func TestProviderAddUnknownPresetListsTheValidRefs(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	_, _, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", "not-a-vendor", "--no-test",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Usage || p.calls.Load() != 0 {
		t.Fatalf("unknown preset: exit=%d calls=%d, want usage and no call", exitcode.From(err), p.calls.Load())
	}
	if !strings.Contains(err.Error(), strings.Join(modelprovider.PresetRefs(), ", ")) {
		t.Fatalf("error %q does not list the valid refs", err)
	}
}

// A preset that was asked for and came out empty (an unset shell variable) must not
// fall back to guessing the vendor from the key: an `sk-` key would be registered as
// OpenAI's and its first test would send the key to OpenAI.
func TestProviderAddRefusesAnExplicitEmptyPreset(t *testing.T) {
	for _, tc := range []struct{ flag, value string }{{"--preset", ""}, {"--preset", " "}, {"--service", " "}} {
		p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
		_, _, err := execRootStdin(t, "sk-fixture-deepseek-key\n", "provider", "add", tc.flag, tc.value,
			"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
		if exitcode.From(err) != exitcode.Usage || p.calls.Load() != 0 {
			t.Fatalf("%s %q: exit=%d calls=%d, want usage and no call", tc.flag, tc.value, exitcode.From(err), p.calls.Load())
		}
		if !strings.Contains(err.Error(), strings.Join(modelprovider.PresetRefs(), ", ")) {
			t.Fatalf("%s %q: error %q does not list the valid refs", tc.flag, tc.value, err)
		}
	}
}

// --service is a published flag and keeps its exact old meaning: an empty value is no
// service (a script passing --service "$SERVICE" with SERVICE unset keeps working) and
// the key's prefix decides the kind, as before the catalog. Only --preset is strict.
func TestProviderAddEmptyServiceFlagMeansNoService(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	if _, errb, err := execRootStdin(t, "sk-ant-fixture-key\n", "provider", "add", "--service", "", "--no-test",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("--service \"\": %v %s", err, errb)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(p.lastBody()), &body); err != nil {
		t.Fatal(err)
	}
	if _, has := body["service"]; has || body["kind"] != "anthropic" || p.calls.Load() != 1 {
		t.Fatalf("--service \"\" body %v calls=%d, want no service and the kind from the key", body, p.calls.Load())
	}
}

// The older name keeps its older default: --service deepseek is named by its kind, as
// it was. Only --preset names the credential after the catalog row.
func TestProviderAddServiceKeepsTheOlderDefaultName(t *testing.T) {
	for flag, want := range map[string]string{"--service": "OpenAI-compatible", "--preset": "DeepSeek"} {
		p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
		if _, errb, err := execRootStdin(t, "fixture-key\n", "provider", "add", flag, "deepseek", "--no-test",
			"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
			t.Fatalf("%s: %v %s", flag, err, errb)
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(p.lastBody()), &body)
		if body["display_name"] != want {
			t.Fatalf("%s deepseek: name %v, want %q", flag, body["display_name"], want)
		}
	}
}

// Two names for one setting are refused, not resolved by whichever came last: a
// `--preset zai --service deepseek` must never register the governed service.
func TestProviderAddRefusesPresetAndServiceTogether(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	_, _, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", "zai-anthropic", "--service", "deepseek", "--no-test",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Usage || p.calls.Load() != 0 {
		t.Fatalf("--preset with --service: exit=%d calls=%d, want usage and no call", exitcode.From(err), p.calls.Load())
	}
}

// What the add prints as the next step follows the row: the governed service has no
// tool binding, every other preset binds to a profile.
func TestProviderAddNextStepFollowsTheRow(t *testing.T) {
	for ref, want := range map[string]string{
		"deepseek":           "governed text execution profile",
		"deepseek-anthropic": "olivares provider bind prv_fixture --profile",
	} {
		rec := `{"provider_ref":"prv_fixture","state":"active","probe_state":"ok","kind":"anthropic","display_name":"Fixture","service":"` + ref + `"}`
		p := newProviderProbeServer(t, http.StatusCreated, rec)
		out, errb, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", ref, "--no-test",
			"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("%s: err=%v stdout=%q stderr=%q, want a next step with %q", ref, err, out, errb, want)
		}
	}
}

// Only the governed deepseek service is API-only; a native preset binds to a profile.
func TestProviderAddPresetBindsOnlyWhenTheRowIsNative(t *testing.T) {
	governed := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	_, _, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", "deepseek", "--profile", "ppf_fixture", "--no-test",
		"--server", governed.URL, "--token", "test-token", "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Usage || governed.calls.Load() != 0 {
		t.Fatalf("governed deepseek with --profile: exit=%d calls=%d, want usage before any call", exitcode.From(err), governed.calls.Load())
	}
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = io.WriteString(w, providerRecordJSON)
	}))
	defer srv.Close()
	if _, errb, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--preset", "deepseek-anthropic", "--profile", "ppf_fixture", "--no-test",
		"--server", srv.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("native preset with --profile: %v %s", err, errb)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"POST /v1/m/sessions/providers", "PATCH /v1/m/sessions/provider-profiles/ppf_fixture"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls %v, want the add and then the bind %v", calls, want)
	}
}

// --service is the older name of --preset and keeps working for every row.
func TestProviderAddServiceIsAnAliasOfPreset(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	if _, errb, err := execRootStdin(t, "fixture-key\n", "provider", "add", "--service", "zai-anthropic", "--no-test",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("%v %s", err, errb)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(p.lastBody()), &body)
	if body["service"] != "zai-anthropic" || body["kind"] != "anthropic" {
		t.Fatalf("--service body %v, want the zai-anthropic row", body)
	}
}

func TestProviderAddPresetCompletesFromTheCatalog(t *testing.T) {
	out, _, err := execRootStdin(t, "", "__complete", "provider", "add", "--preset", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range modelprovider.PresetRefs() {
		if !strings.Contains(out, ref+"\n") && !strings.Contains(out, ref+"\t") {
			t.Errorf("completion lacks %q:\n%s", ref, out)
		}
	}
}
