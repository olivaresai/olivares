// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityHelpHidesRedTeamAndFinOps(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"redteam", "finops"} {
		command, _, err := root.Find([]string{name})
		if err != nil || command == root {
			t.Fatalf("published command %s disappeared", name)
		}
		if !command.Hidden {
			t.Errorf("Community advertises %s in help", name)
		}
	}
}

func TestCommunityPaidCommandsExplainEditionRefusal(t *testing.T) {
	for _, args := range [][]string{{"redteam", "catalog"}, {"finops", "spend", "summary"}} {
		t.Run(args[0], func(t *testing.T) {
			server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
			})
			_, _, err := execRoot(t, lot3Args(server.URL, args...)...)
			if exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), pricingURL) {
				t.Fatalf("paid command refusal = %v; want Business sentence and exit 9", err)
			}
		})
	}
}

func TestCommunitySpendLimitWriteReturnsEditionRefusal(t *testing.T) {
	h := newAppsGatewayHarness(t)
	response, body := doProxy(t, h.proxy.URL, http.MethodPost, appsGatewaySpendLimitPath,
		`{"scope":{"type":"organization"},"amount":"1","period":"daily"}`,
		map[string]string{"Authorization": "Bearer admin"})
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("Community spend-limit write: %d %s; want 501", response.StatusCode, body)
	}
}
