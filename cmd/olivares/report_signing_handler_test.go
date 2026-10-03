// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestReportingSigningCLIReachesTheEnginesRoute is EU21 on Business 09: `reporting
// signing status -o json` exited 4 with HTTP 404 "No route is registered at that path".
// The CLI called /v1/reporting/signing; the engine serves /v1/m/reporting/signing
// (modules/reporting/signing_api.go), and the CLI test's fake was written for the wrong
// path. Here every signing verb drives the engine's own handler.
func TestReportingSigningCLIReachesTheEnginesRoute(t *testing.T) {
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION"} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	handler := eng.api.Handler()
	if code, _, raw := doDemoViewJSON(t, handler, "POST", "/v1/setup", "", "", map[string]any{"token": setup,
		"email": "signing-cli@olivares.ai", "password": "fixture-password-2026!", "organization": "Signing CLI test"}); code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	code, login, raw := doDemoViewJSON(t, handler, "POST", "/v1/auth/login", "", "",
		map[string]any{"email": "signing-cli@olivares.ai", "password": "fixture-password-2026!"})
	admin, _ := login["token"].(string)
	if code != http.StatusOK || admin == "" {
		t.Fatalf("login = %d: %s", code, raw)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// A Community build does not include signing, so the reporting module itself answers
	// every verb with 501 and its sentence; the PUTs reach it past its strict body check. On
	// the old path the router answered 404 "No route is registered at that path" before any
	// module ran.
	for _, verb := range []string{"status", "enable", "disable"} {
		_, _, err := execRoot(t, "reporting", "signing", verb, "--server", srv.URL, "--token", admin, "-o", "json")
		var refusal *apiRefusal
		if !errors.As(err, &refusal) || refusal.status != http.StatusNotImplemented ||
			err.Error() != "Report signing is part of Business; this edition does not include it." {
			t.Fatalf("signing %s: %v; want the reporting module's own 501 sentence", verb, err)
		}
	}
}
