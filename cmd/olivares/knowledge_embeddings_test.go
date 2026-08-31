// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// embedEnv is a complete model-backed embeddings config (the four required vars).
func embedEnv() map[string]string {
	return map[string]string{
		"OLIVARES_EMBEDDINGS_BASE_URL": "https://voyage.example",
		"OLIVARES_EMBEDDINGS_KEY":      "k",
		"OLIVARES_EMBEDDINGS_MODEL":    "voyage-3",
		"OLIVARES_EMBEDDINGS_DIM":      "1024",
	}
}

// TestEmbeddingsRequireGate proves the default-on-with-honest-fallback rule: when
// the operator REQUIRES a semantic embedder but none is configured, boot refuses
// (never serves lexical local-hash as semantic); when one is configured, it passes;
// and without the requirement an unconfigured embedder is fine (the zero-egress
// LocalHashEmbedder remains the visible air-gap default).
func TestEmbeddingsRequireGate(t *testing.T) {
	// Required + unconfigured ⇒ error.
	if err := checkEmbeddingsRequirement(func(k string) string {
		return map[string]string{"OLIVARES_EMBEDDINGS_REQUIRE": "1"}[k]
	}); err == nil {
		t.Fatal("REQUIRE set with no embeddings config must refuse boot")
	}
	// Required + fully configured ⇒ ok.
	env := embedEnv()
	env["OLIVARES_EMBEDDINGS_REQUIRE"] = "true"
	if err := checkEmbeddingsRequirement(func(k string) string { return env[k] }); err != nil {
		t.Fatalf("REQUIRE set with a complete config must pass, got %v", err)
	}
	// Not required + unconfigured ⇒ ok (the local-hash default is allowed).
	if err := checkEmbeddingsRequirement(func(string) string { return "" }); err != nil {
		t.Fatalf("no requirement must never fail boot, got %v", err)
	}
}

// TestEmbedderGeoCapability proves the embedder exposes its data-residency region (the
// optional Region() capability the module reads to enforce the residency↔egress gate),
// sourced from OLIVARES_EMBEDDINGS_GEO and normalized.
func TestEmbedderGeoCapability(t *testing.T) {
	env := embedEnv()
	env["OLIVARES_EMBEDDINGS_GEO"] = "EU"
	emb, ok := resolveEmbedder(func(k string) string { return env[k] })
	if !ok {
		t.Fatal("a complete config must resolve an embedder")
	}
	if emb.Region() != "eu" {
		t.Fatalf("Region() = %q, want normalized \"eu\"", emb.Region())
	}
	if !emb.AllowsEgress() {
		t.Fatal("a model-backed embedder egresses")
	}
	// Undeclared geo ⇒ "" (every residency-locked KB then refuses egress, fail-closed).
	emb2, _ := resolveEmbedder(func(k string) string { return embedEnv()[k] })
	if emb2.Region() != "" {
		t.Fatalf("undeclared geo must be empty, got %q", emb2.Region())
	}
}
