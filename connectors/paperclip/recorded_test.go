// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package paperclip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
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

func TestRecordedPaperclipAnswers(t *testing.T) {
	srv := recordedServer(t)
	s := New()
	// 2026-10-06: the run (2026-10-05) and the cost event (2026-10-04) fall on
	// completed days of the default 3-day window.
	s.now = func() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"base_url": srv.URL}}); err != nil {
		t.Fatal(err)
	}

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Collections) != 1 || g.Collections[0].DisplayName != "Acme Robotics" || len(g.Identities) != 2 || len(g.Memberships) != 2 {
		t.Fatalf("graph = %+v", g)
	}
	eng, ok := g.FindIdentity("paperclip/agent/" + recordedEng)
	if !ok || eng.Kind != "paperclip/process" || eng.Attributes["role"] != "engineer" ||
		eng.Attributes["title"] != "Staff Engineer" || eng.Attributes["reports_to"] != "paperclip/agent/"+recordedCEO || eng.Disabled {
		t.Errorf("engineer = %+v", eng)
	}

	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var runs, costs int
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.MetricSample:
			runs++
			if v.SubjectRef != "paperclip/agent/"+recordedEng || v.Value != 1 || v.Dimensions["status"] != "succeeded" || v.OccurredAt.Format("2006-01-02") != "2026-10-05" {
				t.Errorf("run metric = %+v", v)
			}
		case model.CostSample:
			costs++
			if v.ModelRef != "gpt-5" || v.CostMicroUSD != 1_250_000 || v.InputTokens != 120000 || v.OutputTokens != 4000 ||
				v.Actor != "agent:paperclip/agent/"+recordedEng || v.OccurredAt.Format("2006-01-02") != "2026-10-04" {
				t.Errorf("cost sample = %+v", v)
			}
		case model.FindingReport:
			if v.Kind == "coverage" {
				t.Errorf("unexpected coverage finding on a healthy instance: %+v", v)
			}
		}
	}
	if runs != 1 || costs != 1 {
		t.Errorf("run metrics = %d, cost samples = %d, want 1 and 1", runs, costs)
	}
}
