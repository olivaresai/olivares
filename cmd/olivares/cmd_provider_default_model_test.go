// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestProviderListKeepsPublishedTextWithDefaultModel(t *testing.T) {
	const wantText = "PROVIDER                        NAME       KIND       KEY    STATE   CONNECTION\n" +
		"prv_01J8ABCDEFGHJKMNPQRSTVWXYZ  Anthropic  anthropic  …0000  active  ok\n"
	for _, tc := range []struct {
		name  string
		model any
	}{{"cleared", nil}, {"awaiting probe", ""}, {"saved", "coding-model"}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := providerFixture()
			rec["default_model"] = tc.model
			payload, err := json.Marshal(map[string]any{"items": []map[string]any{rec}})
			if err != nil {
				t.Fatal(err)
			}
			p := newProviderProbeServer(t, http.StatusOK, string(payload))
			args := []string{"provider", "ls", "--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"}
			out, stderr, err := execRootStdin(t, "", args...)
			if err != nil {
				t.Fatalf("list: %v %s", err, stderr)
			}
			if out != wantText {
				t.Errorf("a default model changed the published table:\n got: %q\nwant: %q", out, wantText)
			}
			out, stderr, err = execRootStdin(t, "", append(args, "-o", "json")...)
			if err != nil {
				t.Fatalf("list JSON: %v %s", err, stderr)
			}
			var got []map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, []map[string]any{rec}) {
				t.Fatalf("JSON lost provider fields or the saved default: %s", out)
			}
		})
	}
}

func TestProviderDefaultModelCLIUsesOneTransport(t *testing.T) {
	for _, value := range []string{"coding-model", ""} {
		t.Run("set="+value, func(t *testing.T) {
			p := newProviderProbeServer(t, http.StatusOK, strings.TrimSuffix(providerRecordJSON, "}")+`,"default_model":"`+value+`"}`)
			out, stderr, err := execRootStdin(t, "", "provider", "set", "prv_fixture", "--default-model", value,
				"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a", "-o", "json")
			if err != nil {
				t.Fatalf("set default: %v %s", err, stderr)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(p.lastBody()), &body); err != nil {
				t.Fatal(err)
			}
			if p.calls.Load() != 1 || p.lastMethod() != "PATCH" || p.lastPath() != "/v1/m/sessions/providers/prv_fixture" || len(body) != 1 || body["default_model"] != value || !strings.Contains(out, `"default_model"`) {
				t.Fatalf("default setter diverged from the existing provider transport: calls=%d method=%s path=%s body=%v", p.calls.Load(), p.lastMethod(), p.lastPath(), body)
			}
		})
	}
	t.Run("add without a probe", func(t *testing.T) {
		p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
		_, stderr, err := execRootStdin(t, "sk-ant-fixture-key-0123456789\n", providerArgs(p.URL, "--default-model", "coding-model")...)
		if err != nil {
			t.Fatalf("add default: %v %s", err, stderr)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(p.lastBody()), &body); err != nil {
			t.Fatal(err)
		}
		if p.calls.Load() != 1 || p.lastMethod() != "POST" || body["default_model"] != "coding-model" {
			t.Fatalf("--no-test lost the chosen default or probed: calls=%d body=%v", p.calls.Load(), body)
		}
	})
}
