// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityOrganizationsCommandUnavailable(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"tenants"})
	if err != nil || cmd == root || !cmd.Hidden {
		t.Fatalf("Community tenants command must remain callable but hidden: %v", err)
	}
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	for _, name := range []string{"tenants", "orgs"} {
		for _, args := range [][]string{
			{},
			{"--help"},
			{"-h"},
			{"create", "--help"},
			{"ls", "--help"},
			{"rm", "--help"},
			{"set-status", "--help"},
			{"set-region", "--help"},
			{"ls"},
			{"create", "--name", "Second organization"},
			{"rm", "tenant-id", "--yes"},
			{"set-status", "tenant-id", "--status", "suspended", "--yes"},
			{"set-region", "tenant-id", "--region", "eu"},
		} {
			invocation := append([]string{name}, args...)
			invocation = append(invocation, "--server", srv.URL, "--token", "fixture")
			_, _, err := execRoot(t, invocation...)
			if exitcode.From(err) != exitcode.Edition {
				t.Fatalf("%v = %v, want edition refusal", invocation, err)
			}
		}
	}
	if hits := srv.hits.Load(); hits != 0 {
		t.Fatalf("Community organization seam contacted the engine %d times", hits)
	}
}
