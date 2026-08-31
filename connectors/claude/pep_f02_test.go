// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import "testing"

// TestHookPEPPlanHashBindsArguments is the F-02 red repro. The anti-TOCTOU
// approval binding (planHash) used to fingerprint only the derived structural fields
// (tool, resourceKind, resourceRef, mode) and DROP the raw arguments. For tools whose
// derived resourceRef is coarser than the call (Bash -> the program; Write/Edit -> the
// file_path, never the content), two materially different tool-calls collapsed to the
// SAME planHash — so a human approval bound to a benign call auto-authorized a
// destructive one within the approval's validity window.
//
// SECURE behavior (asserted here): different arguments MUST produce a different plan
// hash. planHash now folds a canonical digest of the raw tool_input (plus session) into
// the binding; a call retried with identical arguments still hashes stably (see
// TestHookPEPDerivesPlanHashStably).
func TestHookPEPPlanHashBindsArguments(t *testing.T) {
	// Bash: same program "rm" derives the same (kind=shell, ref="rm", mode=…); before the
	// fix the arguments (which path is wiped) never reached the hash.
	t.Run("bash_arguments", func(t *testing.T) {
		dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
		pep := NewHookPEP(dec, &fakeAuditor{}, nil)

		serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "rm -rf /tmp/cache"}, nil))
		benign := dec.gotInput.PlanHash
		serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "rm -rf /etc"}, nil))
		destructive := dec.gotInput.PlanHash

		if benign == "" || destructive == "" {
			t.Fatalf("plan hash must be non-empty: benign=%q destructive=%q", benign, destructive)
		}
		if benign == destructive {
			t.Fatalf("F-02: plan hash collides across different Bash arguments (%q) — an approval "+
				"bound to `rm -rf /tmp/cache` would authorize `rm -rf /etc`", benign)
		}
	})

	// Write: same file_path, different content — the classic content-swap TOCTOU.
	t.Run("write_content", func(t *testing.T) {
		dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
		pep := NewHookPEP(dec, &fakeAuditor{}, nil)

		serve(t, pep, pepRequest(hookPreToolUse, "Write",
			map[string]any{"file_path": "/app/authorized_keys", "content": "# empty"}, nil))
		reviewed := dec.gotInput.PlanHash
		serve(t, pep, pepRequest(hookPreToolUse, "Write",
			map[string]any{"file_path": "/app/authorized_keys", "content": "ssh-ed25519 AAAA... attacker"}, nil))
		swapped := dec.gotInput.PlanHash

		if reviewed == "" || swapped == "" {
			t.Fatalf("plan hash must be non-empty: reviewed=%q swapped=%q", reviewed, swapped)
		}
		if reviewed == swapped {
			t.Fatalf("F-02: plan hash collides across different Write content for the same path "+
				"(%q) — an approval bound to the reviewed content would authorize a swap", reviewed)
		}
	})
}
