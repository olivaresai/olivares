// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/capabilities"
)

type pinManagementVerifier struct{ t *testing.T }

func (v pinManagementVerifier) Verify(context.Context, string, string, string) (bool, string, error) {
	v.t.Error("a management refusal must not invoke tool verification")
	return false, "", nil
}

func (v pinManagementVerifier) RecordPin(context.Context, string, string, string) error {
	v.t.Error("a management refusal must not change pins")
	return nil
}

// Mount only the real module handlers under test; authentication remains covered
// by the capabilities module's authenticated route tests.
type pinManagementRoutes struct {
	api.RouteRegistrar
	mux *http.ServeMux
}

func (r pinManagementRoutes) Handle(method, path string, _ auth.Permission, h api.ModuleHandler) {
	if strings.HasPrefix(path, "/toolpins") {
		r.mux.HandleFunc(method+" /v1/m/capabilities"+path, func(w http.ResponseWriter, req *http.Request) {
			h(w, req, api.ModuleContext{})
		})
	}
}

func TestMCPPinsVerifierWithoutManagementDoesNotReportMissingEdition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier mcpc.ToolPinVerifier
		code     int
		message  string
	}{
		{"absent", nil, exitcode.Edition, "Business feature"},
		{"verification available", pinManagementVerifier{t}, exitcode.Server, "Tool pin management is unavailable in this build."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareMCPCLITest(t)
			mux := http.NewServeMux()
			capabilities.New(capabilitiesOpts(tc.verifier)...).APIRoutes(pinManagementRoutes{mux: mux})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			for _, args := range [][]string{
				{"mcp", "pins", "ls", "-o", "json"},
				{"mcp", "pins", "approve", "search", "--fingerprint", "approved"},
				{"mcp", "pins", "rm", "search"},
			} {
				args = append(args, "--server", srv.URL, "--token", "test-token", "--tenant", "tenant-a")
				_, _, err := execRoot(t, args...)
				if err == nil || exitcode.From(err) != tc.code || !strings.Contains(err.Error(), tc.message) {
					t.Errorf("%v: got %v, want exit %d with %q", args[:4], err, tc.code, tc.message)
				}
				if tc.verifier != nil && err != nil && strings.Contains(err.Error(), "pricing") {
					t.Errorf("configured verifier still gets a purchase message: %v", err)
				}
			}
		})
	}
}
