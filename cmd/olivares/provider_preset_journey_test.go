// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Only the test runner substitutes a loopback origin, after checking the real
// catalog endpoint and key-bound network policy. The production runner still
// creates the real egress proxy, namespace and filesystem boundary.
type presetJourneyRunner struct {
	sessions.Runner
	row      modelprovider.Preset
	endpoint string
}

func (r presetJourneyRunner) Launch(ctx context.Context, spec sessions.LaunchSpec) (sessions.Process, error) {
	if spec.NetworkPolicy == nil || len(spec.NetworkPolicy.Providers) != 1 || spec.NetworkPolicy.Providers[0] != r.row.BaseURL || spec.NetworkPolicy.CredentialSHA256 == ([32]byte{}) {
		return nil, fmt.Errorf("missing catalog network binding")
	}
	found := false
	for i, env := range spec.Env {
		if env.Name == "ANTHROPIC_BASE_URL" {
			if env.Value != r.row.BaseURL {
				return nil, fmt.Errorf("wrong catalog launch endpoint")
			}
			spec.Env[i].Value = r.endpoint
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("missing catalog launch endpoint")
	}
	spec.NetworkPolicy.Providers = []string{r.endpoint}
	return r.Runner.Launch(ctx, spec)
}

func TestProviderPresetClaudeTurnWithoutModel(t *testing.T) {
	for _, tc := range []struct {
		ref, modelsPath, messagesPath, model string
		missingCatalog, chooseDefault        bool
	}{
		{"deepseek-anthropic", "/models", "/anthropic/v1/messages", "deepseek-flash", false, false},
		{"zai-anthropic", "/api/anthropic/v1/models", "/api/anthropic/v1/messages", "glm-5.3", false, false},
		{"opencode-zen", "/inference/v1/models", "/inference/anthropic/v1/messages", "claude-sonnet-5-5", false, false},
		{"deepseek-anthropic", "/models", "/anthropic/v1/messages", "deepseek-flash", true, true},
		{"deepseek-anthropic", "/models", "/anthropic/v1/messages", "deepseek-flash", true, false},
		{"zai-anthropic", "/api/anthropic/v1/models", "/api/anthropic/v1/messages", "glm-5.3", true, false},
	} {
		name := tc.ref
		if tc.missingCatalog {
			name += "/without-catalog"
			if tc.chooseDefault {
				name += "/saved"
			} else {
				name += "/preset"
			}
		}
		t.Run(name, func(t *testing.T) {
			row, _ := modelprovider.LookupPreset(tc.ref)
			if row.Models.Default != tc.model {
				t.Fatalf("catalog default = %q, want %q", row.Models.Default, tc.model)
			}
			turn := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer "+presetFixtureKey || req.Header.Get("x-api-key") != "" {
					http.Error(w, "wrong auth", 401)
					return
				}
				switch {
				case req.Method == "GET" && req.URL.Path == tc.modelsPath:
					if tc.missingCatalog {
						http.NotFound(w, req)
						return
					}
					fmt.Fprintf(w, `{"data":[{"id":%q}]}`, tc.model)
				case req.Method == "POST" && req.URL.Path == tc.messagesPath:
					var body struct {
						Model string `json:"model"`
					}
					if json.NewDecoder(req.Body).Decode(&body) != nil || body.Model != tc.model {
						http.Error(w, "wrong model", 400)
						return
					}
					turn <- body.Model
					fmt.Fprint(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"preset turn answered"}]}`)
				default:
					http.NotFound(w, req)
				}
			}))
			defer upstream.Close()
			h := newHarness(t)
			vaultDir := t.TempDir()
			sealer, err := newSecretSealer(vaultDir, osGetenv)
			if err != nil {
				t.Fatal(err)
			}
			sessions.WithProviderSecretVault(providerSecretVault{store: auth.NewSecretStore(h.st, sealer)})(h.set.sessions)
			// Redirect only this row's HTTPS origin for discovery; preserve every path.
			origin, _ := url.Parse(row.BaseURL)
			probeClient := upstream.Client()
			baseTransport := probeClient.Transport
			if baseTransport == nil {
				baseTransport = http.DefaultTransport
			}
			probeClient.Transport = diagRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme != "https" || req.URL.Host != origin.Host {
					return nil, fmt.Errorf("unexpected probe origin")
				}
				target, _ := url.Parse(upstream.URL)
				copy := req.Clone(req.Context())
				copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
				return baseTransport.RoundTrip(copy)
			})
			sessions.WithProviderProbe(providerProbe{client: probeClient})(h.set.sessions)
			dir := t.TempDir()
			script := filepath.Join(dir, "claude-stand-in")
			// Parse the driver's --model, wait for one real input frame, and send one
			// Messages request using only the credential the engine injected.
			standIn := `#!/bin/sh
model=''
while [ "$#" -gt 0 ]; do
 if [ "$1" = --model ]; then shift; model="$1"; fi
 shift
done
IFS= read -r input || exit 90
[ -n "$input" ] || exit 91
[ -z "${ANTHROPIC_API_KEY:-}" ] || exit 93
reply=$(curl --silent --show-error --fail --max-time 15 -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" -H 'Content-Type: application/json' --data "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}" "$ANTHROPIC_BASE_URL/v1/messages") || exit 92
printf '{"type":"assistant","message":%s}\n' "$reply"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'
exec cat >/dev/null
`
			if err := os.WriteFile(script, []byte(standIn), 0700); err != nil {
				t.Fatal(err)
			}
			m := h.set.sessions
			m.UseStanding(h.authr)
			runner, err := sessions.NewOfficialRunner("claude")
			if err != nil {
				t.Fatal(err)
			}
			sessions.WithRunner(presetJourneyRunner{runner, row, upstream.URL + origin.Path})(m)
			sessions.WithProgram(script)(m)
			sessions.WithConfinement([]string{vaultDir}, true)(m)
			m.UseExecutionEnvironmentRef("preset-journey")
			sessions.WithLaunchGate(approvalProjectionLaunchGate(func(context.Context, model.TenantID, sessions.LaunchIntent) (sessions.LaunchDecision, error) {
				return sessions.LaunchDecision{Allowed: true, RecordIO: true}, nil
			}))(m)
			server := httptest.NewServer(h.h)
			defer server.Close()
			cli := func(input string, args ...string) string {
				t.Helper()
				args = append(args, "--server", server.URL, "--token", h.adminToken, "--tenant", h.tenantA)
				out, errb, err := execSessionCLI(t, strings.NewReader(input), args...)
				if err != nil {
					t.Fatalf("%v: %v %s", args[:2], err, errb)
				}
				return out
			}
			out := cli(presetFixtureKey+"\n", "provider", "add", "--preset", tc.ref, "--no-test", "-o", "json")
			var record struct {
				Ref string `json:"provider_ref"`
			}
			if err := json.Unmarshal([]byte(out), &record); err != nil {
				t.Fatal(err)
			}
			if tc.chooseDefault {
				cli("", "provider", "set", record.Ref, "--default-model", tc.model)
			}
			if tc.missingCatalog {
				_, _, err := execSessionCLI(t, strings.NewReader(""), "provider", "test", record.Ref,
					"--server", server.URL, "--token", h.adminToken, "--tenant", h.tenantA)
				if err == nil || exitcode.From(err) != exitcode.Indeterminate {
					t.Fatalf("missing catalog test = %v, want indeterminate, not key acceptance", err)
				}
			} else {
				cli("", "provider", "test", record.Ref)
			}
			var profile struct {
				Ref string `json:"profile_ref"`
			}
			if code := h.reqInto("POST", "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "managed_injection", "provider_record_ref": record.Ref, "config_home": dir, "user_home": dir, "display_name": "deepseek"}, &profile); code != 201 {
				t.Fatalf("profile=%d", code)
			}
			// No --model: use the saved choice or the selected preset's declared preference.
			reply := cli("", "session", "start", dir, "hello", "--tool", "claude", "--account", profile.Ref, "--permission", "supervised")
			if !strings.Contains(reply, "preset turn answered") {
				t.Fatalf("turn did not answer: %s", reply)
			}
			select {
			case <-turn:
			case <-time.After(time.Second):
				t.Fatal("no Messages request")
			}
		})
	}
}
