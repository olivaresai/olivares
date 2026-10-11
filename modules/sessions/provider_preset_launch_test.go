// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"testing"
)

// HU 043/044: the chosen preset must reach the child's own native controls,
// rather than relying on approval requests the child may never send.
func TestProviderSessionPresetsReachNativeControls(t *testing.T) {
	for _, driver := range []string{providerDriverCodex, providerDriverGrok, providerDriverOpenCode} {
		for _, tc := range []struct{ name, mode, approval, sandbox, permission string }{
			{"read-only", "plan", "on-request", "read-only", "deny"},
			{"ask", "default", "untrusted", "workspace-write", "ask"},
			{"edits-only", "acceptEdits", "untrusted", "workspace-write", "ask"},
		} {
			t.Run(driver+"/"+tc.name, func(t *testing.T) {
				m := New(WithProviderDriver(NewCodexDriver()), WithProviderDriver(NewGrokDriver()), WithProviderDriver(NewOpenCodeDriver()))
				p := CreateRunParams{Transport: TransportStreamJSON, PermissionMode: tc.mode, WorkspaceDir: "/workspace/preset-fixture", ProviderHome: &ProviderHomeSnapshot{Driver: driver, AuthSource: AuthSourceAccountHome}}
				var answer launchTermsAnswer
				switch driver {
				case providerDriverCodex:
					answer = launchTermsCodexAnswer
				case providerDriverGrok:
					answer = func(method string, params map[string]any) (any, bool) {
						if method == acpMethodSessionNew {
							return map[string]any{"sessionId": "grok-preset", "modes": map[string]any{"currentModeId": "default", "availableModes": []any{map[string]any{"id": "default", "name": "Ask"}, map[string]any{"id": "plan", "name": "Read only"}, map[string]any{"id": "acceptEdits", "name": "Edits"}}}}, true
						}
						if method == "session/set_mode" {
							return map[string]any{}, true
						}
						return launchTermsGrokAnswer(method, params)
					}
				case providerDriverOpenCode:
					answer = launchTermsOpenCodeAnswer
				}
				spec, frames := launchTermsLaunch(t, m, p, answer)
				switch driver {
				case providerDriverCodex:
					var params map[string]any
					for _, f := range frames {
						if f.method == codexMethodThreadStart {
							params = f.body.(map[string]any)["params"].(map[string]any)
						}
					}
					if params == nil || params["approvalPolicy"] != tc.approval || params["sandbox"] != tc.sandbox {
						t.Fatalf("%s preset lost at native thread/start: %v", tc.name, params)
					}
				case providerDriverGrok:
					var mode any
					for _, f := range frames {
						if f.method == "session/set_mode" {
							mode = f.body.(map[string]any)["params"].(map[string]any)["modeId"]
						}
					}
					wantMode := "default"
					if tc.name == "read-only" {
						wantMode = "plan"
					}
					if mode != wantMode {
						t.Fatalf("%s preset lost despite advertised native mode: %v", tc.name, mode)
					}
				case providerDriverOpenCode:
					var cfg map[string]any
					for _, e := range spec.Env {
						if e.Name == envOpenCodeConfigContent {
							if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
								t.Fatal(err)
							}
						}
					}
					permission, _ := cfg["permission"].(map[string]any)
					if permission["bash"] != tc.permission {
						t.Fatalf("%s preset lost at native permissions: %v", tc.name, cfg)
					}
					wantEdit := tc.permission
					read, _ := permission["read"].(map[string]any)
					if permission["edit"] != wantEdit || read["*"] != "allow" || read["*.env"] != "deny" {
						t.Fatalf("%s native read/edit permissions=%v", tc.name, permission)
					}
				}
			})
		}
	}
}

func TestGrokPresetRefusesAnUnchangeableWiderNativeMode(t *testing.T) {
	for _, preset := range []string{PresetReadOnly, PresetAsk, PresetEditsOnly} {
		for _, mode := range []string{"acceptEdits", "bypassPermissions"} {
			t.Run(preset+"/"+mode, func(t *testing.T) {
				var response grokSessionIDResponse
				raw, _ := json.Marshal(map[string]any{"sessionId": "resume", "modes": map[string]any{"currentModeId": mode, "availableModes": []any{}}})
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				s := grokSession{acpSession: acpSession{cfg: DriverSessionConfig{Preset: preset}}}
				if err := s.selectPresetMode(t.Context(), response.SessionID, response); err == nil {
					t.Fatal("a wider current mode survived the chosen preset")
				}
			})
		}
	}
}
