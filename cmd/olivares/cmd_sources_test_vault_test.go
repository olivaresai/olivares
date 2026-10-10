// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// ACN-11 (AUDCONNC on 09b and RC10): `sources test --kind vault` against an address
// nothing listens on exited 0 with answered=true, because the connector's Open
// contacts nothing. The probe now makes the connector's own check: no answer is exit
// 6 with the reason, and an answering Vault is still ANSWERED.
func TestSourcesTestVaultAnswersOnlyWhenVaultDoes(t *testing.T) {
	dir := initialisedDataDir(t)
	if _, err := runCLIStdin(t, "fixture-vault-token", "secrets", "put", "--name", "fh/probe-token", "--value-file", "-",
		"--data-dir", dir, "--actor", "FH", "--reason", "synthetic connector check"); err != nil {
		t.Fatalf("seal the fixture token: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + l.Addr().String()
	_ = l.Close()
	probe := func(baseURL string) (string, error) {
		return runCLI(t, "sources", "test", "--data-dir", dir, "--name", "fh-vault", "--kind", "vault",
			"--tenant", planTenantA, "--config", "base_url="+baseURL, "--config", "token=store:fh/probe-token",
			"--timeout", "5s", "-o", "json")
	}
	out, err := probe(closed)
	if exitcode.From(err) != exitcode.Server || !strings.Contains(out, `"answered": false`) || !strings.Contains(out, "did not answer") {
		t.Fatalf("a Vault nothing listens on = %v (%d)\n%s, want exit 6 and answered=false", err, exitcode.From(err), out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/token/lookup-self" || r.Header.Get("X-Vault-Token") != "fixture-vault-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	if out, err := probe(srv.URL); err != nil || !strings.Contains(out, `"answered": true`) {
		t.Fatalf("an answering Vault = %v\n%s, want answered=true", err, out)
	}
}
