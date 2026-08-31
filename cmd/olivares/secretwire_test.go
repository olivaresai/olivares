// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
)

// stubSecretHandler resolves any locator to a fixed value (a stand-in for the
// sealed store / a backend, so the wiring test needs no real store).
type stubSecretHandler struct{ val string }

func (h stubSecretHandler) Resolve(_ context.Context, _ string) ([]byte, error) {
	return []byte(h.val), nil
}

// TestWireSourcesResolvesStoreReference proves the end-to-end wiring: a source
// whose secret field carries a `store:<name>` reference is RESOLVED to a live value
// and wired, while a source carrying an inline literal in a declared-secret field is
// REFUSED (strict mode) and NOT wired — the resolution running inside wireSources.
func TestWireSourcesResolvesStoreReference(t *testing.T) {
	resolver := secret.NewResolver(map[string]secret.Handler{
		secret.SchemeStore: stubSecretHandler{val: "hvs.live-token"},
		secret.SchemeEnv:   secret.EnvHandler{},
	})

	// A vault source (in-process; its "token" field is declared Secret) carrying a
	// store reference resolves and wires.
	var okBuf bytes.Buffer
	okLog := slog.New(slog.NewTextHandler(&okBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	wireSources(context.Background(), rt, sourcesConfig{Sources: []sourceSpec{
		{Name: "vault-ref", Kind: "vault", Tenant: "acme", Config: map[string]string{
			"base_url": "https://vault.example:8200", "token": "store:vault-token",
		}},
	}}, t.TempDir(), resolver, okLog)
	if !logHasLine(okBuf.String(), "wired source (in-process fast-path)", "kind=vault") {
		t.Errorf("a vault source with a store: token reference was not wired; log = %q", okBuf.String())
	}

	// The same source with an INLINE secret in the declared-secret token field is
	// refused (strict no-inline-secret) and not wired.
	var badBuf bytes.Buffer
	badLog := slog.New(slog.NewTextHandler(&badBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt2 := runtime.New(runtime.Options{Logger: quietLog()})
	const inlineSecret = "hvs.CAESrawinlinetokenvaluexxxxxxxxxxxxxxxx"
	wireSources(context.Background(), rt2, sourcesConfig{Sources: []sourceSpec{
		{Name: "vault-inline", Kind: "vault", Tenant: "acme", Config: map[string]string{
			"base_url": "https://vault.example:8200", "token": inlineSecret,
		}},
	}}, t.TempDir(), resolver, badLog)
	if logHasLine(badBuf.String(), "wired source (in-process fast-path)", "name=vault-inline") {
		t.Errorf("an inline secret in a declared-secret field must NOT wire; log = %q", badBuf.String())
	}
	if !logHasLine(badBuf.String(), "not wired", "name=vault-inline") {
		t.Errorf("an inline secret must warn 'not wired'; log = %q", badBuf.String())
	}
	if bytes.Contains(badBuf.Bytes(), []byte(inlineSecret)) {
		t.Error("the inline secret value leaked into the boot log")
	}
}

// TestNewSecretResolverBuiltins proves the composition-root resolver wires the
// built-in env/file handlers (and, with no store, fails a store: reference closed).
func TestNewSecretResolverBuiltins(t *testing.T) {
	getenv := func(k string) string {
		if k == "MY_SECRET" {
			return "from-env"
		}
		return ""
	}
	r := newSecretResolver(nil, getenv, slog.Default())
	desc := sdk.Descriptor{ConfigFields: []sdk.ConfigField{{Key: "token", Secret: true}}}
	out, err := r.Resolve(context.Background(), desc, sdk.Config{Settings: map[string]string{"token": "env:MY_SECRET"}})
	if err != nil || out.Settings["token"] != "from-env" {
		t.Fatalf("env resolution = (%q, %v)", out.Settings["token"], err)
	}
	// store: has no handler (nil store) → fail closed.
	if _, err := r.Resolve(context.Background(), desc, sdk.Config{Settings: map[string]string{"token": "store:x"}}); err == nil {
		t.Error("store: reference with no store wired should fail closed")
	}
}
