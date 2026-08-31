// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestS467GenerationMirrorsDispatcherFilter: the frozen dispatcher generation must
// apply the SAME validity filter/precedence as newOrchestrationDispatcher, so an
// INVALID duplicate entry the dispatcher ignores cannot overwrite the generation of
// the valid target the dispatcher actually actuates (item 6, Codex review 3).
func TestS467GenerationMirrorsDispatcherFilter(t *testing.T) {
	withDup := orchDispatchConfig{}
	withDup.Runtime.Targets = []orchRuntimeTargetJSON{
		{SubjectKind: "agent", SubjectRef: "victim", Runtime: "k8s", Image: "good"},
		// Invalid (empty runtime) — orchdispatch_load.go SKIPS it, so it is NOT the
		// backend Fire selects; it must NOT contribute to the generation.
		{SubjectKind: "agent", SubjectRef: "victim", Runtime: "", Image: "attacker-ignored"},
	}
	got := newDispatcherGeneration(withDup).Generation("agent", "victim")

	validOnly := orchDispatchConfig{}
	validOnly.Runtime.Targets = []orchRuntimeTargetJSON{
		{SubjectKind: "agent", SubjectRef: "victim", Runtime: "k8s", Image: "good"},
	}
	if want := newDispatcherGeneration(validOnly).Generation("agent", "victim"); got != want {
		t.Fatalf("invalid duplicate overwrote the generation: got %q want %q", got, want)
	}

	// Restart with an EVIL valid image (invalid dup unchanged) MUST change the gen,
	// so a paused approval frozen against "good" is voided.
	evil := orchDispatchConfig{}
	evil.Runtime.Targets = []orchRuntimeTargetJSON{
		{SubjectKind: "agent", SubjectRef: "victim", Runtime: "k8s", Image: "evil"},
		{SubjectKind: "agent", SubjectRef: "victim", Runtime: "", Image: "attacker-ignored"},
	}
	if newDispatcherGeneration(evil).Generation("agent", "victim") == got {
		t.Fatal("re-pointing the valid image good->evil did NOT change the generation: a paused approval would still pass")
	}
}

// TestS467GenDigestInnerFieldsAreCanonical: inner tuples (Resources k/v, EnvRef
// name/secret_ref) must be length-prefixed, not flattened with "=" — else
// {"cpu=x":"y"} and {"cpu":"x=y"} collide to the same generation, a target
// substitution WITHOUT a race (item 1, Codex review 3).
func TestS467GenDigestInnerFieldsAreCanonical(t *testing.T) {
	mk := func(res map[string]string, env []secretBindJSON) string {
		cfg := orchDispatchConfig{}
		cfg.Runtime.Targets = []orchRuntimeTargetJSON{{
			SubjectKind: "agent", SubjectRef: "s", Runtime: "k8s", Resources: res, EnvRefs: env,
		}}
		return newDispatcherGeneration(cfg).Generation("agent", "s")
	}
	if mk(map[string]string{"cpu=x": "y"}, nil) == mk(map[string]string{"cpu": "x=y"}, nil) {
		t.Fatal("Resources key/value split collides in the generation (non-canonical inner encoding)")
	}
	if mk(nil, []secretBindJSON{{Name: "a=x", SecretRef: "y"}}) == mk(nil, []secretBindJSON{{Name: "a", SecretRef: "x=y"}}) {
		t.Fatal("EnvRef name/secret_ref split collides in the generation (non-canonical inner encoding)")
	}
}
