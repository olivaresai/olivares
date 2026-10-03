// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// HU2-04: with no account and only the product's own Ollama in Providers, Now said
// "Codex is ready" and every Codex turn failed (Ollama 0.35 rejects the
// additional_tools Codex 0.160 sends). The rule does not offer Codex on a local model
// alone: OpenCode runs on it, and Codex says it needs a sign-in or an OpenAI key.
func TestResolve_CodexIsNotOfferedOnALocalModelAlone(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	local := addRecord(t, m, tenant, ProviderKindOllama, "Ollama on this server", "http://127.0.0.1:11434", "")
	ctx := context.Background()
	for _, ask := range []func() (ResolvedProfile, error){
		func() (ResolvedProfile, error) { return m.PreviewProfile(ctx, tenant, "codex") },
		func() (ResolvedProfile, error) { return m.ResolveProfile(ctx, tenant, "codex") },
	} {
		_, err := ask()
		var coded *codedRunErr
		if statusOf(err) != http.StatusConflict || !errors.As(err, &coded) || coded.code != resolveCodeNothingToRunOn ||
			err.Error() != "Codex is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an OpenAI key in Providers." {
			t.Fatalf("codex on the local model alone = %v, want the sign-in or key refusal", err)
		}
	}
	if got, err := m.PreviewProfile(ctx, tenant, "opencode"); err != nil || got.Reason != ResolveAPIKey || got.Provider == nil || got.Provider.Ref != local.Ref {
		t.Fatalf("opencode = %+v %v, want the local model", got, err)
	}
	stub.mu.Lock()
	stub.signedIn["codex"] = true
	stub.mu.Unlock()
	if got, err := m.PreviewProfile(ctx, tenant, "codex"); err != nil || got.Reason != ResolveOwnLogin {
		t.Fatalf("a signed-in Codex = %+v %v, want its own login", got, err)
	}
}
