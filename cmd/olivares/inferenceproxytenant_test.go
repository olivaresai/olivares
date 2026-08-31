// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxytenant_test.go pins on the inference proxy: the sixth reader of a
// configured tenant, and the only one that used to WARN AND CONTINUE. A fixed tenant
// exists so this proxy serves ONE organization; on an operator typo the old code fell
// back to per-credential resolution, i.e. the config asked for "only this org" and the
// binary delivered "whichever org the credential names". A log.Warn at the startup of a
// long-lived server is read by nobody.
//
// EVERY build-level test here carries a CONTROL with a valid tenant and the SAME engine.
// buildClaudeMessagesProxyServer returns (nil, nil) when governance dependencies are
// unwired, so without the control an assertion of "it errored" could be green for a
// reason that has nothing to do with the tenant.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// writeInferenceProxyConfig provisions the operator config with the given tenant on the
// `direct` surface (the surface must be set, else the builder returns "not provisioned"
// before it ever looks at the tenant).
func writeInferenceProxyConfig(t *testing.T, tenant string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "inferenceproxy.json")
	body := `{"surface":"direct","tenant":` + quoteJSON(tenant) + `}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", path)
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// TestInferenceProxyRefusesAPresentButInvalidTenant is the D1 closer: a present but
// unparseable tenant must refuse the mount, like its five siblings, instead of widening
// to per-credential resolution.
func TestInferenceProxyRefusesAPresentButInvalidTenant(t *testing.T) {
	// CONTROL: a valid tenant with the very same (unwired) engine must NOT error.
	// Without this, the assertion below could pass on the dependency guard alone.
	writeInferenceProxyConfig(t, model.NewTenantID().String())
	if srv, err := buildClaudeMessagesProxyServer(&engine{}, discardLog()); err != nil {
		t.Fatalf("control: a VALID tenant must not error here; got %v (the test below would be meaningless)", err)
	} else if srv != nil {
		t.Fatalf("control: an unwired engine must mount nothing; got %+v", srv)
	}

	writeInferenceProxyConfig(t, "not-a-tenant-id")
	srv, err := buildClaudeMessagesProxyServer(&engine{}, discardLog())
	if err == nil {
		t.Fatal("a present but invalid tenant must refuse the mount, not fall back to per-credential resolution")
	}
	if srv != nil {
		t.Errorf("a refused mount must return no server; got %+v", srv)
	}
	if !strings.Contains(err.Error(), "not-a-tenant-id") {
		t.Errorf("the refusal must name the offending value so the operator can find the typo; got %q", err)
	}
}

// TestInferenceProxyRefusesTheConfiguredSystemTenant is the D2 closer. The premise
// assertions prove the old shape (`err == nil && !tid.IsZero()`) could not catch this.
func TestInferenceProxyRefusesTheConfiguredSystemTenant(t *testing.T) {
	sys := model.SystemTenantID.String()

	// Premise: the system tenant parses WITHOUT error and is NOT zero, so neither of
	// the two checks the old code performed would have rejected it.
	parsed, perr := model.ParseTenantID(sys)
	if perr != nil || parsed.IsZero() {
		t.Fatalf("premise: the system tenant must parse cleanly and be non-zero; got parsed=%q err=%v", parsed, perr)
	}

	// CONTROL: same engine, a real business tenant → no error.
	writeInferenceProxyConfig(t, model.NewTenantID().String())
	if _, err := buildClaudeMessagesProxyServer(&engine{}, discardLog()); err != nil {
		t.Fatalf("control: a VALID tenant must not error here; got %v", err)
	}

	writeInferenceProxyConfig(t, sys)
	if _, err := buildClaudeMessagesProxyServer(&engine{}, discardLog()); err == nil {
		t.Fatal("the reserved system tenant must not be accepted as a governed surface's fixed tenant")
	}
}

// TestInferenceProxyAbsentTenantDoesNotRefuse guards the case that must NOT change: an
// omitted tenant is the documented default ("" = infer from the credential). Refusing
// it would break every deployment that never set the field.
func TestInferenceProxyAbsentTenantDoesNotRefuse(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		writeInferenceProxyConfig(t, raw)
		srv, err := buildClaudeMessagesProxyServer(&engine{}, discardLog())
		if err != nil {
			t.Errorf("an ABSENT tenant (%q) is legitimate and must not refuse the mount; got %v", raw, err)
		}
		if srv != nil {
			t.Errorf("an unwired engine must still mount nothing; got %+v", srv)
		}
	}
}

// TestEmptyTenantHintStillResolvesPerCredential pins the SEMANTIC of the empty case,
// which the build-level test above cannot see: with no fixed tenant the decider falls
// back to the principal's sole grant. This is the behavior the change must leave
// untouched.
func TestEmptyTenantHintStillResolvesPerCredential(t *testing.T) {
	sole := model.NewTenantID()
	p := auth.ScopedPrincipal(model.ID("u1"), "user one", sole, "editor")

	d := &inferenceProxyDecider{} // tenantHint unset — the "" configuration
	got, ok := d.resolveTenant(p)
	if !ok || got != sole {
		t.Fatalf("an empty tenantHint must resolve to the credential's sole grant; got (%q, %v), want (%q, true)", got, ok, sole)
	}

	// And a CONFIGURED hint still pins the tenant for a member: the two legs differ.
	fixed := model.NewTenantID()
	pf := auth.ScopedPrincipal(model.ID("u2"), "user two", fixed, "editor")
	df := &inferenceProxyDecider{tenantHint: fixed}
	if got, ok := df.resolveTenant(pf); !ok || got != fixed {
		t.Fatalf("a configured tenantHint must pin the tenant for a member; got (%q, %v)", got, ok)
	}
	// A non-member gets nothing, even with a configured hint.
	if got, ok := df.resolveTenant(p); ok {
		t.Fatalf("a configured tenantHint must not resolve for a non-member; got (%q, %v)", got, ok)
	}
}
