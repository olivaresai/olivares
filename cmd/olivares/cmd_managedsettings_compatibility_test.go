// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestManagedSettingsRenderAllowsProtectedSessionHooks(t *testing.T) {
	for _, noHook := range []bool{false, true} {
		t.Run(map[bool]string{false: "static hook", true: "env only"}[noHook], func(t *testing.T) {
			cmd := newAgentManagedSettingsCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			if noHook {
				cmd.SetArgs([]string{"--no-hook"})
			} else {
				cmd.SetArgs([]string{})
			}
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var policy struct {
				Disabled    bool                       `json:"disableAllHooks"`
				ManagedOnly bool                       `json:"allowManagedHooksOnly"`
				Hooks       map[string]json.RawMessage `json:"hooks"`
			}
			if json.Unmarshal(out.Bytes(), &policy) != nil {
				t.Fatal("rendered policy is invalid")
			}
			if policy.Disabled || !policy.ManagedOnly {
				t.Fatal("rendered host policy must retain the published managed-only posture")
			}
			if noHook && len(policy.Hooks) != 0 {
				t.Fatal("env-only render adds static hooks")
			}
			if !noHook && (len(policy.Hooks["PreToolUse"]) == 0 || len(policy.Hooks["PostToolUse"]) == 0) {
				t.Fatal("static PEP hook coverage changed")
			}
		})
	}
}
