// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package paperclip

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/recorded holds the four GET answers of a real Paperclip, byte for byte:
// paperclipai 2026.1001.0 (server commit 8f8a0ab7), deployment mode local_trusted,
// recorded 2026-10-05. The throwaway instance was seeded with one company, two
// process agents (the engineer reports to the CEO), one cost event dated the day
// before and one on-demand heartbeat run. They carry fields the connector must
// ignore (avatars, org-chain health, adapterConfig, runtimeConfig).
const (
	recordedCompany = "18441100-b5d7-4169-b36b-b0d6355e809c"
	recordedCEO     = "0f60e247-9e35-4e70-afc2-26903be83d73"
	recordedEng     = "4ffe5145-786a-46c6-a250-ff6669c9980b"
)

func recordedServer(t *testing.T) *httptest.Server {
	t.Helper()
	serve := func(w http.ResponseWriter, name string) {
		data, err := os.ReadFile(filepath.Join("testdata", "recorded", name))
		if err != nil {
			t.Errorf("read recording: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("connector sent %s %s", r.Method, r.URL.Path)
		}
		p := r.URL.Path
		switch {
		case p == "/api/companies":
			serve(w, "companies.json")
		case p == "/api/companies/"+recordedCompany+"/agents":
			serve(w, "agents.json")
		case p == "/api/companies/"+recordedCompany+"/heartbeat-runs":
			serve(w, "runs.json")
		case p == "/api/companies/"+recordedCompany+"/costs/by-agent-model":
			// The cost event was dated 2026-10-04; every other day is empty.
			if strings.HasPrefix(r.URL.Query().Get("from"), "2026-10-04T") {
				serve(w, "costs.json")
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
