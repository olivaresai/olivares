// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// On a working install `olivares status` printed STATUS not_configured and the
// knowledge module's internals (EMBEDDER_KIND local-hash, GUARD_PROFILE acl_aware). It now
// says "running; knowledge search not set up" and keeps the posture under --verbose;
// the exit code and the -o json document are unchanged.
func TestStatusSaysRunningAndKeepsThePostureUnderVerbose(t *testing.T) {
	const doc = `{"status":"not_configured","timestamp":"2026-10-02T18:23:08Z","embedder_kind":"local-hash","retrieval_semantic":false,` +
		`"knowledge_status_reason":"embeddings_provider_missing","guard_profile":"acl_aware","components":[` +
		`{"name":"api","status":"operational"},{"name":"knowledge","status":"not_configured","embedder_kind":"local-hash",` +
		`"retrieval_semantic":false,"reason":"embeddings_provider_missing","guard_profile":"acl_aware"},{"name":"store","status":"operational"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(srv.Close)
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := newStatusCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--server", srv.URL}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("status %v: want exit 0, got %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	plain := run()
	if !strings.Contains(plain, "running; knowledge search not set up") {
		t.Fatalf("status does not say the engine runs and what is not set up:\n%s", plain)
	}
	for _, internal := range []string{"EMBEDDER_KIND", "GUARD_PROFILE", "RETRIEVAL_SEMANTIC", "embedder=local-hash"} {
		if strings.Contains(plain, internal) {
			t.Fatalf("status prints %q without --verbose:\n%s", internal, plain)
		}
	}
	if verbose := run("--verbose"); !strings.Contains(verbose, "EMBEDDER_KIND") || !strings.Contains(verbose, "embedder=local-hash") {
		t.Fatalf("--verbose lost the posture:\n%s", verbose)
	}
	raw, errb, err := execRoot(t, "status", "--server", srv.URL, "-o", "json")
	if err != nil || !strings.Contains(raw, `"embedder_kind"`) || !strings.Contains(raw, "not_configured") || strings.Contains(raw, "knowledge search not set up") {
		t.Fatalf("-o json is no longer the endpoint's document: %v %s\n%s", err, errb, raw)
	}
}
